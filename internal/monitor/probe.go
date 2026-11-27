package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const latencyURL = "https://www.google.com/generate_204"

// googleHosts are the four endpoints Antigravity needs; the UI shows one row
// each. They answer 404 on the root path, which still proves reachability.
var googleHosts = []string{
	"oauth2.googleapis.com",
	"generativelanguage.googleapis.com",
	"daily-cloudcode-pa.googleapis.com",
	"cloudcode-pa.googleapis.com",
}

func emptyGoogle() []GoogleState {
	rows := make([]GoogleState, 0, len(googleHosts))
	for _, h := range googleHosts {
		rows = append(rows, GoogleState{Host: h, Label: shortLabel(h)})
	}
	return rows
}

// shortLabel trims the shared ".googleapis.com" suffix, matching the panel.
func shortLabel(host string) string {
	return strings.TrimSuffix(host, ".googleapis.com")
}

// client builds a transport that dials through the monitored chain rather than
// the local gateway, so probe traffic never lands in the counters.
func (m *Monitor) client(timeout time.Duration) *http.Client {
	dial := m.dialer()
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial.DialContext,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          -1,
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// fetch performs one GET and reports the status code and round-trip time.
func (m *Monitor) fetch(rawURL string, timeout time.Duration) (int, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "agyproxy/1.0")
	req.Header.Set("Cache-Control", "no-cache")

	start := time.Now()
	resp, err := m.client(timeout).Do(req)
	elapsed := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return 0, elapsed, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, elapsed, nil
}

func (m *Monitor) probeLatency() (float64, error) {
	code, ms, err := m.fetch(latencyURL, 10*time.Second)
	if err != nil {
		return 0, simplifyErr(err)
	}
	if code != http.StatusNoContent && code != http.StatusOK {
		return ms, fmt.Errorf("unexpected status %d", code)
	}
	return ms, nil
}

func (m *Monitor) probeGoogle() {
	rows := make([]GoogleState, len(googleHosts))
	var wg sync.WaitGroup
	for i, host := range googleHosts {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			row := GoogleState{Host: host, Label: shortLabel(host)}
			code, ms, err := m.fetch("https://"+host+"/", 10*time.Second)
			row.Code = code
			row.Ms = ms
			if err != nil {
				row.Err = simplifyErr(err).Error()
			} else {
				row.OK = true
			}
			rows[i] = row
		}(i, host)
	}
	wg.Wait()

	m.mu.Lock()
	m.google = rows
	m.mu.Unlock()
}

// exitInfo describes the public IP as seen from the far end of the chain.
type exitInfo struct {
	IP      string          `json:"ip"`
	City    string          `json:"city"`
	Country string          `json:"country"`
	Org     string          `json:"org"`
	ASN     json.RawMessage `json:"asn"`
	ISP     string          `json:"isp"`
}

func (m *Monitor) probeExit() {
	m.mu.Lock()
	m.exitAt = time.Now()
	m.mu.Unlock()

	info, err := m.lookupExit()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if m.exit.Masked == "" {
			m.exit = ExitState{Err: simplifyErr(err).Error()}
		}
		return
	}
	m.exit = info
}

func (m *Monitor) lookupExit() (ExitState, error) {
	// ipinfo.io carries the ASN in the org field ("AS15169 Google LLC").
	if raw, err := m.getJSON("https://ipinfo.io/json", 12*time.Second); err == nil {
		var info exitInfo
		if json.Unmarshal(raw, &info) == nil && info.IP != "" {
			return ExitState{
				OK:      true,
				Masked:  maskIP(info.IP),
				City:    info.City,
				Country: info.Country,
				Org:     strings.TrimSpace(info.Org),
			}, nil
		}
	}
	// Fallback: api.ip.sb/geoip returns ip + city + country + asn + isp.
	if raw, err := m.getJSON("https://api.ip.sb/geoip", 12*time.Second); err == nil {
		var v struct {
			IP      string `json:"ip"`
			City    string `json:"city"`
			Country string `json:"country"`
			ASN     any    `json:"asn"`
			ISP     string `json:"isp"`
			ASNOrg  string `json:"asn_organization"`
		}
		if json.Unmarshal(raw, &v) == nil && v.IP != "" {
			org := strings.TrimSpace(v.ASNOrg)
			if org == "" {
				org = v.ISP
			}
			return ExitState{OK: true, Masked: maskIP(v.IP), City: v.City, Country: v.Country, Org: org}, nil
		}
	}
	// Last resort: IP only.
	raw, err := m.getJSON("https://api.ipify.org?format=json", 12*time.Second)
	if err != nil {
		return ExitState{}, err
	}
	var v struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(raw, &v); err != nil || v.IP == "" {
		return ExitState{}, fmt.Errorf("exit node lookup returned no address")
	}
	return ExitState{OK: true, Masked: maskIP(v.IP)}, nil
}

func (m *Monitor) getJSON(url string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "agyproxy/1.0")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client(timeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<10))
}

// maskIP hides the last two octets (or most of an IPv6 address) so the panel
// can show the exit node without publishing it.
func maskIP(ip string) string {
	if parsed := net.ParseIP(ip); parsed != nil {
		if v4 := parsed.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.•.•", v4[0], v4[1])
		}
		groups := strings.Split(parsed.String(), ":")
		if len(groups) >= 2 {
			return groups[0] + ":" + groups[1] + ":••••"
		}
	}
	parts := strings.Split(ip, ".")
	if len(parts) == 4 {
		return parts[0] + "." + parts[1] + ".•.•"
	}
	if ip == "" {
		return ""
	}
	return "•"
}

// simplifyErr turns transport noise into something a user can act on.
func simplifyErr(err error) error {
	if err == nil {
		return nil
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return fmt.Errorf("连接超时")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("上游拒绝连接")
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("DNS 解析失败")
	case strings.Contains(msg, "certificate"):
		return fmt.Errorf("TLS 证书错误")
	case strings.Contains(msg, "EOF"):
		return fmt.Errorf("链路被中断")
	}
	return err
}
