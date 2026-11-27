// Package monitor probes the proxy chain Antigravity is using and keeps the
// numbers the dashboard renders.
//
// Probes deliberately bypass the gateway: they dial the same upstream chain
// directly, so the traffic panel only ever counts Antigravity's own bytes.
package monitor

import (
	"sync"
	"sync/atomic"
	"time"

	"agyproxy/internal/gateway"
	"agyproxy/internal/upstream"
)

// TrendPoints is how many latency samples the sparkline keeps.
const TrendPoints = 24

// exitRefresh bounds how often the exit node is looked up (it rarely changes,
// and the geo-IP services are rate limited).
const exitRefresh = 5 * time.Minute

// TrendPoint is one latency sample.
type TrendPoint struct {
	Ms float64 `json:"ms"`
	OK bool    `json:"ok"`
}

// ChainState is the top status card.
type ChainState struct {
	OK     bool   `json:"ok"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// TrafficState is the Antigravity throughput card.
type TrafficState struct {
	DownBytes int64   `json:"downBytes"`
	UpBytes   int64   `json:"upBytes"`
	DownRate  float64 `json:"downRate"`
	UpRate    float64 `json:"upRate"`
	Active    int64   `json:"active"`
	Conns     int64   `json:"conns"`
}

// LatencyState feeds the average latency and success-rate cards.
type LatencyState struct {
	AvgMs   float64      `json:"avgMs"`
	Trend   []TrendPoint `json:"trend"`
	OK      int          `json:"ok"`
	Total   int          `json:"total"`
	MaxSize int          `json:"maxSize"`
}

// ExitState is the masked exit node card.
type ExitState struct {
	OK      bool   `json:"ok"`
	Masked  string `json:"masked"`
	City    string `json:"city"`
	Country string `json:"country"`
	Org     string `json:"org"`
	Err     string `json:"err"`
}

// GoogleState is one row of the Google API reachability list.
type GoogleState struct {
	Host  string  `json:"host"`
	Label string  `json:"label"`
	Code  int     `json:"code"`
	Ms    float64 `json:"ms"`
	OK    bool    `json:"ok"`
	Err   string  `json:"err"`
}

// Snapshot is the immutable view handed to the UI.
type Snapshot struct {
	ProxyAddr   string        `json:"proxyAddr"`
	Port        int           `json:"port"`
	Upstream    string        `json:"upstream"`
	UpstreamOK  bool          `json:"upstreamOk"`
	Direct      bool          `json:"direct"`
	Chain       ChainState    `json:"chain"`
	Traffic     TrafficState  `json:"traffic"`
	Latency     LatencyState  `json:"latency"`
	Exit        ExitState     `json:"exit"`
	Google      []GoogleState `json:"google"`
	GoogleEvery int           `json:"googleEvery"`
	Since       string        `json:"since"`
	UpdatedAt   string        `json:"updatedAt"`
}

// Monitor samples the chain on a fixed cadence.
type Monitor struct {
	dial      upstream.Dialer
	stats     *gateway.Stats
	interval  time.Duration
	googEvery int
	port      int
	addr      string
	started   time.Time

	mu         sync.RWMutex
	round      int
	trend      []TrendPoint
	okCount    int
	totalCount int
	failStreak int
	lastErr    string
	everOK     bool
	exit       ExitState
	exitAt     time.Time
	google     []GoogleState

	rateDown float64
	rateUp   float64
	lastDown int64
	lastUp   int64
	lastRate time.Time

	quit     chan struct{}
	stopOnce sync.Once
	polls    atomic.Int64
}

// New wires a monitor to the gateway counters.
func New(dial upstream.Dialer, stats *gateway.Stats, addr string, port int, interval time.Duration, googleEvery int) *Monitor {
	return &Monitor{
		dial:      dial,
		stats:     stats,
		interval:  interval,
		googEvery: googleEvery,
		addr:      addr,
		port:      port,
		started:   time.Now(),
		google:    emptyGoogle(),
		quit:      make(chan struct{}),
		lastRate:  time.Now(),
	}
}

func (m *Monitor) dialer() upstream.Dialer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dial
}

// SetDialer follows an upstream change so probes measure the new chain.
func (m *Monitor) SetDialer(d upstream.Dialer) {
	m.mu.Lock()
	m.dial = d
	m.mu.Unlock()
}

// Run blocks until Stop is called.
func (m *Monitor) Run() {
	m.tick()

	probe := time.NewTicker(m.interval)
	rate := time.NewTicker(time.Second)
	defer probe.Stop()
	defer rate.Stop()

	for {
		select {
		case <-m.quit:
			return
		case <-rate.C:
			m.sampleRate()
		case <-probe.C:
			m.tick()
		}
	}
}

// Stop ends the run loop.
func (m *Monitor) Stop() {
	m.stopOnce.Do(func() { close(m.quit) })
}

// Touch records UI activity; used to reap an orphaned process.
func (m *Monitor) Touch() { m.polls.Store(time.Now().UnixNano()) }

// LastPoll reports when the UI last asked for state.
func (m *Monitor) LastPoll() time.Time {
	ns := m.polls.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (m *Monitor) tick() {
	m.round++
	ms, err := m.probeLatency()

	m.mu.Lock()
	if err != nil {
		m.failStreak++
		m.lastErr = err.Error()
		m.totalCount++
		m.trend = appendTrend(m.trend, TrendPoint{Ms: 0, OK: false})
	} else {
		m.failStreak = 0
		m.everOK = true
		m.totalCount++
		m.okCount++
		m.lastErr = ""
		m.trend = appendTrend(m.trend, TrendPoint{Ms: ms, OK: true})
	}
	needExit := m.exitAt.IsZero() || time.Since(m.exitAt) > exitRefresh
	round := m.round
	m.mu.Unlock()

	if round == 1 || round%m.googEvery == 0 {
		m.probeGoogle()
	}
	if needExit {
		m.probeExit()
	}
}

func appendTrend(trend []TrendPoint, p TrendPoint) []TrendPoint {
	trend = append(trend, p)
	if len(trend) > TrendPoints {
		trend = trend[len(trend)-TrendPoints:]
	}
	return trend
}

func (m *Monitor) sampleRate() {
	now := time.Now()
	down := m.stats.Down()
	up := m.stats.Up()

	m.mu.Lock()
	defer m.mu.Unlock()
	dt := now.Sub(m.lastRate).Seconds()
	if dt <= 0 {
		return
	}
	dDown := float64(down - m.lastDown)
	dUp := float64(up - m.lastUp)
	if dDown < 0 {
		dDown = 0
	}
	if dUp < 0 {
		dUp = 0
	}
	// Exponential smoothing keeps the readout stable at 1 Hz.
	m.rateDown = m.rateDown*0.5 + (dDown/dt)*0.5
	m.rateUp = m.rateUp*0.5 + (dUp/dt)*0.5
	m.lastDown = down
	m.lastUp = up
	m.lastRate = now
}

// Snapshot copies the current state for the UI.
func (m *Monitor) Snapshot(upstreamDesc string, direct bool) Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	trend := make([]TrendPoint, len(m.trend))
	copy(trend, m.trend)

	var sum float64
	var n int
	for _, p := range trend {
		if p.OK {
			sum += p.Ms
			n++
		}
	}
	avg := 0.0
	if n > 0 {
		avg = sum / float64(n)
	}

	chain := ChainState{OK: true, Title: "代理连接正常", Detail: "Antigravity 代理链路工作正常"}
	switch {
	case direct:
		chain = ChainState{OK: false, Title: "直连模式", Detail: "未检测到上游代理，Antigravity 正在直连外网"}
	case m.failStreak >= 3:
		detail := "上游代理无响应"
		if m.lastErr != "" {
			detail = m.lastErr
		}
		chain = ChainState{OK: false, Title: "代理链路异常", Detail: detail}
	case !m.everOK:
		chain = ChainState{OK: false, Title: "正在建立链路", Detail: "首批探测尚未完成"}
	}

	return Snapshot{
		ProxyAddr:  "http://" + m.addr,
		Port:       m.port,
		Upstream:   upstreamDesc,
		UpstreamOK: !direct,
		Direct:     direct,
		Chain:      chain,
		Traffic: TrafficState{
			DownBytes: m.stats.Down(),
			UpBytes:   m.stats.Up(),
			DownRate:  m.rateDown,
			UpRate:    m.rateUp,
			Active:    m.stats.Active(),
			Conns:     m.stats.Total(),
		},
		Latency: LatencyState{
			AvgMs:   avg,
			Trend:   trend,
			OK:      m.okCount,
			Total:   m.totalCount,
			MaxSize: TrendPoints,
		},
		Exit:        m.exit,
		Google:      append([]GoogleState(nil), m.google...),
		GoogleEvery: m.googEvery,
		Since:       m.started.Format("15:04:05"),
		UpdatedAt:   time.Now().Format("15:04:05"),
	}
}
