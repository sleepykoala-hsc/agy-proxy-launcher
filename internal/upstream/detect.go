package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Candidate is one possible upstream proxy found on this machine.
type Candidate struct {
	Kind     string `json:"kind"`
	Addr     string `json:"addr"`
	User     string `json:"-"`
	Pass     string `json:"-"`
	Source   string `json:"source"`
	Verified bool   `json:"verified"`
}

// Describe renders the candidate as a URL for logs and the UI.
func (c Candidate) Describe() string { return c.Kind + "://" + c.Addr }

// ParseURL splits a user supplied proxy URL into config fields.
// A bare "host:port" is treated as HTTP.
func ParseURL(raw string) (kind, addr, user, pass string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", "", errors.New("空的上游地址")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", "", fmt.Errorf("无法解析上游地址 %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		kind = KindHTTP
	case "socks5", "socks5h", "socks":
		kind = KindSOCKS5
	default:
		return "", "", "", "", fmt.Errorf("不支持的上游协议 %q", u.Scheme)
	}
	addr = u.Host
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return kind, addr, user, pass, nil
}

// scanPorts are the ports the popular desktop proxies listen on.
// Ordered by how common they are so a machine with several clients running
// picks the most likely one first.
var scanPorts = []Candidate{
	{Kind: KindHTTP, Addr: "127.0.0.1:7890", Source: "Clash / Clash Verge"},
	{Kind: KindHTTP, Addr: "127.0.0.1:7897", Source: "Clash Verge (mixed)"},
	{Kind: KindSOCKS5, Addr: "127.0.0.1:7898", Source: "Clash Verge (socks)"},
	{Kind: KindHTTP, Addr: "127.0.0.1:10809", Source: "v2rayN"},
	{Kind: KindSOCKS5, Addr: "127.0.0.1:10808", Source: "v2rayN (socks)"},
	{Kind: KindHTTP, Addr: "127.0.0.1:1087", Source: "ShadowsocksX-NG / V2rayU"},
	{Kind: KindSOCKS5, Addr: "127.0.0.1:1086", Source: "ShadowsocksX-NG (socks)"},
	{Kind: KindHTTP, Addr: "127.0.0.1:6152", Source: "Surge"},
	{Kind: KindSOCKS5, Addr: "127.0.0.1:6153", Source: "Surge (socks)"},
	{Kind: KindHTTP, Addr: "127.0.0.1:20171", Source: "Mihomo Party"},
	{Kind: KindSOCKS5, Addr: "127.0.0.1:2080", Source: "sing-box"},
	{Kind: KindHTTP, Addr: "127.0.0.1:8889", Source: "ClashX"},
	{Kind: KindHTTP, Addr: "127.0.0.1:7891", Source: "Clash (socks/http)"},
}

// Candidates lists env-configured proxies first, then the scan list.
// Addresses in skip (for example our own gateway port) are dropped so the
// tool can never accidentally proxy into itself.
func Candidates(skip map[string]bool) []Candidate {
	var out []Candidate
	seen := map[string]bool{}

	add := func(c Candidate) {
		key := c.Kind + "|" + strings.ToLower(c.Addr)
		if skip[strings.ToLower(c.Addr)] || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, c)
	}

	for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if c, ok := parseProxyEnv(env, os.Getenv(env)); ok {
			add(c)
		}
	}
	for _, c := range scanPorts {
		add(c)
	}
	return out
}

func parseProxyEnv(name, raw string) (Candidate, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Candidate{}, false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Candidate{}, false
	}
	kind := KindHTTP
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h", "socks":
		kind = KindSOCKS5
	case "http", "https":
		kind = KindHTTP
	default:
		return Candidate{}, false
	}
	c := Candidate{Kind: kind, Addr: u.Host, Source: "env " + name}
	if u.User != nil {
		c.User = u.User.Username()
		c.Pass, _ = u.User.Password()
	}
	return c, true
}

// Detect returns the first usable upstream. It prefers a candidate that can
// actually tunnel a TCP connection, but falls back to the first port that
// merely accepts connections so the user gets a diagnosable state instead of
// "nothing found".
func Detect(ctx context.Context, skip map[string]bool) (*Candidate, Dialer) {
	var firstOpen *Candidate
	for _, c := range Candidates(skip) {
		if !portOpen(c.Addr) {
			continue
		}
		d, err := New(c.Kind, c.Addr, c.User, c.Pass)
		if err != nil {
			continue
		}
		if tunnelOK(ctx, d) {
			c.Verified = true
			return &c, d
		}
		if firstOpen == nil {
			open := c
			firstOpen = &open
		}
	}
	if firstOpen != nil {
		d, _ := New(firstOpen.Kind, firstOpen.Addr, firstOpen.User, firstOpen.Pass)
		return firstOpen, d
	}
	return nil, Direct{}
}

func portOpen(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// tunnelOK verifies the proxy really forwards TCP traffic.
func tunnelOK(ctx context.Context, d Dialer) bool {
	return Verify(ctx, d) == nil
}

// Verify reports whether d can actually tunnel TCP. The returned error is the
// reason, which the settings panel shows when an address is rejected.
func Verify(ctx context.Context, d Dialer) error {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	for _, target := range verifyTargets {
		c, err := d.DialContext(ctx, "tcp", target)
		if err == nil {
			c.Close()
			return nil
		}
	}
	// Report the first target's error; it is the one users expect.
	c, err := d.DialContext(ctx, "tcp", verifyTargets[0])
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

// verifyTargets are cheap endpoints that only need a TCP handshake.
var verifyTargets = []string{"www.gstatic.com:443", "www.google.com:443"}

// Scan probes every known candidate in parallel and returns the ports that are
// actually accepting connections, verified ones first. It is used by the
// settings panel so the user can pick a running proxy instead of typing it.
func Scan(ctx context.Context, skip map[string]bool) []Candidate {
	cands := Candidates(skip)
	results := make([]Candidate, len(cands))
	found := make([]bool, len(cands))

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	for i, c := range cands {
		wg.Add(1)
		go func(i int, c Candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if !portOpen(c.Addr) {
				return
			}
			if d, err := New(c.Kind, c.Addr, c.User, c.Pass); err == nil {
				vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				c.Verified = tunnelOK(vctx, d)
				cancel()
			}
			mu.Lock()
			results[i], found[i] = c, true
			mu.Unlock()
		}(i, c)
	}
	wg.Wait()

	out := make([]Candidate, 0, len(cands))
	for i := range results {
		if found[i] && results[i].Verified {
			out = append(out, results[i])
		}
	}
	for i := range results {
		if found[i] && !results[i].Verified {
			out = append(out, results[i])
		}
	}
	return out
}
