// Package upstream dials TCP connections through a proxy chain.
//
// A Dialer is the single abstraction used by both the gateway (to forward
// Antigravity traffic) and the monitor (to probe the very same chain without
// touching the gateway's byte counters).
package upstream

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Dialer opens a TCP connection to addr through the proxy chain.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// Describe returns a short human readable form, e.g. "http://127.0.0.1:7890".
	Describe() string
}

// dialTimeout bounds the TCP dial plus the proxy handshake.
const dialTimeout = 12 * time.Second

// Kind values accepted by New.
const (
	KindDirect = "direct"
	KindHTTP   = "http"
	KindSOCKS5 = "socks5"
)

// New builds a Dialer. An empty kind means direct.
func New(kind, addr, user, pass string) (Dialer, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", KindDirect:
		return Direct{}, nil
	case KindHTTP, "https":
		if addr == "" {
			return nil, errors.New("http upstream requires an address")
		}
		return &HTTPProxy{Addr: normalizeAddr(addr), User: user, Pass: pass}, nil
	case KindSOCKS5, "socks":
		if addr == "" {
			return nil, errors.New("socks5 upstream requires an address")
		}
		return &SOCKS5{Addr: normalizeAddr(addr), User: user, Pass: pass}, nil
	default:
		return nil, fmt.Errorf("unknown upstream kind %q", kind)
	}
}

// normalizeAddr adds the default port when the user typed only a host.
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "socks5://")
	addr = strings.TrimSuffix(addr, "/")
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "7890")
	}
	return addr
}

// Direct connects without any proxy.
type Direct struct{}

func (Direct) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	return d.DialContext(ctx, network, addr)
}

func (Direct) Describe() string { return "direct" }

// HTTPProxy tunnels through an HTTP proxy using CONNECT.
type HTTPProxy struct {
	Addr string
	User string
	Pass string
}

func (p *HTTPProxy) Describe() string { return "http://" + p.Addr }

func (p *HTTPProxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !isTCP(network) {
		return nil, fmt.Errorf("http upstream does not support network %q", network)
	}
	d := &net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial upstream %s: %w", p.Addr, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(dialTimeout)
	}
	c.SetDeadline(deadline)

	var b strings.Builder
	b.WriteString("CONNECT " + addr + " HTTP/1.1\r\n")
	b.WriteString("Host: " + addr + "\r\n")
	if p.User != "" {
		token := base64.StdEncoding.EncodeToString([]byte(p.User + ":" + p.Pass))
		b.WriteString("Proxy-Authorization: Basic " + token + "\r\n")
	}
	b.WriteString("Proxy-Connection: keep-alive\r\n\r\n")
	if _, err := io.WriteString(c, b.String()); err != nil {
		c.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("read CONNECT reply: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("upstream refused CONNECT: %s", resp.Status)
	}
	c.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// SOCKS5 tunnels through a SOCKS5 proxy (no-auth or user/pass).
type SOCKS5 struct {
	Addr string
	User string
	Pass string
}

func (p *SOCKS5) Describe() string { return "socks5://" + p.Addr }

func (p *SOCKS5) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !isTCP(network) {
		return nil, fmt.Errorf("socks5 upstream does not support network %q", network)
	}
	d := &net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", p.Addr)
	if err != nil {
		return nil, fmt.Errorf("dial upstream %s: %w", p.Addr, err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(dialTimeout)
	}
	c.SetDeadline(deadline)
	if err := p.handshake(c, addr); err != nil {
		c.Close()
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func (p *SOCKS5) handshake(c net.Conn, addr string) error {
	methods := []byte{0x00}
	if p.User != "" {
		methods = []byte{0x02, 0x00}
	}
	if _, err := c.Write(append([]byte{0x05, byte(len(methods))}, methods...)); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(c, reply); err != nil {
		return fmt.Errorf("socks5 greeting reply: %w", err)
	}
	if reply[0] != 0x05 {
		return errors.New("socks5: bad protocol version from server")
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		if p.User == "" {
			return errors.New("socks5: server requires authentication")
		}
		req := []byte{0x01, byte(len(p.User))}
		req = append(req, p.User...)
		req = append(req, byte(len(p.Pass)))
		req = append(req, p.Pass...)
		if _, err := c.Write(req); err != nil {
			return fmt.Errorf("socks5 auth: %w", err)
		}
		authReply := make([]byte, 2)
		if _, err := io.ReadFull(c, authReply); err != nil {
			return fmt.Errorf("socks5 auth reply: %w", err)
		}
		if authReply[1] != 0x00 {
			return errors.New("socks5: authentication failed")
		}
	default:
		return errors.New("socks5: no acceptable authentication method")
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("socks5 target %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("socks5 target port %q", portStr)
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, 0x01)
			req = append(req, v4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("socks5: hostname too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := c.Write(req); err != nil {
		return fmt.Errorf("socks5 connect: %w", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return fmt.Errorf("socks5 connect reply: %w", err)
	}
	if head[0] != 0x05 {
		return errors.New("socks5: bad reply version")
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5: connect failed (%s)", socksReplyText(head[1]))
	}
	skip := 0
	switch head[3] {
	case 0x01:
		skip = 4
	case 0x04:
		skip = 16
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return fmt.Errorf("socks5 reply address: %w", err)
		}
		skip = int(l[0])
	default:
		return fmt.Errorf("socks5: unknown address type %d", head[3])
	}
	if _, err := io.ReadFull(c, make([]byte, skip+2)); err != nil {
		return fmt.Errorf("socks5 reply address: %w", err)
	}
	return nil
}

func socksReplyText(code byte) string {
	switch code {
	case 1:
		return "general failure"
	case 2:
		return "not allowed"
	case 3:
		return "network unreachable"
	case 4:
		return "host unreachable"
	case 5:
		return "connection refused"
	case 6:
		return "ttl expired"
	case 7:
		return "command not supported"
	case 8:
		return "address type not supported"
	default:
		return "code " + strconv.Itoa(int(code))
	}
}

func isTCP(network string) bool {
	return network == "tcp" || network == "tcp4" || network == "tcp6"
}

// bufferedConn keeps unread bytes that the proxy handshake left in the buffer.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
