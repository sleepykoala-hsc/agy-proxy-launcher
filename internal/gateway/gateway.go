// Package gateway implements the local, observable proxy that Antigravity is
// pointed at. Every byte that crosses it is counted, which is how the UI can
// report the child process's real traffic without OS-level accounting.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agyproxy/internal/upstream"
)

// Stats holds the counters displayed in the traffic panel.
type Stats struct {
	down   atomic.Int64
	up     atomic.Int64
	active atomic.Int64
	total  atomic.Int64
}

func (s *Stats) Down() int64   { return s.down.Load() }
func (s *Stats) Up() int64     { return s.up.Load() }
func (s *Stats) Active() int64 { return s.active.Load() }
func (s *Stats) Total() int64  { return s.total.Load() }

// Gateway is an HTTP+SOCKS5 proxy listening on loopback.
type Gateway struct {
	ln      net.Listener
	stats   *Stats
	started time.Time
	closed  atomic.Bool
	wg      sync.WaitGroup

	dialMu sync.RWMutex
	dial   upstream.Dialer

	connsMu sync.Mutex
	conns   map[net.Conn]struct{}
}

// New listens on addr (e.g. "127.0.0.1:10909").
func New(addr string, dial upstream.Dialer) (*Gateway, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return &Gateway{
		ln:      ln,
		dial:    dial,
		stats:   &Stats{},
		started: time.Now(),
		conns:   make(map[net.Conn]struct{}),
	}, nil
}

// Addr is the bound address, useful when port 0 was requested.
func (g *Gateway) Addr() string { return g.ln.Addr().String() }

// Stats exposes the live counters.
func (g *Gateway) Stats() *Stats { return g.stats }

// Serve accepts connections until Close is called.
func (g *Gateway) Serve() error {
	for {
		c, err := g.ln.Accept()
		if err != nil {
			if g.closed.Load() {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}

		// Registering under the same lock Close uses guarantees the connection
		// is either tracked (and will be closed) or rejected as late.
		g.connsMu.Lock()
		if g.closed.Load() {
			g.connsMu.Unlock()
			c.Close()
			return nil
		}
		g.conns[c] = struct{}{}
		g.connsMu.Unlock()

		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			defer g.untrack(c)
			g.handle(c)
		}()
	}
}

// Close stops the listener and tears down in-flight tunnels, which would
// otherwise keep the process alive because TLS connections stay open for
// minutes.
func (g *Gateway) Close() error {
	g.closed.Store(true)
	err := g.ln.Close()
	g.connsMu.Lock()
	for c := range g.conns {
		_ = c.Close()
	}
	g.connsMu.Unlock()
	g.wg.Wait()
	return err
}

func (g *Gateway) untrack(c net.Conn) {
	g.connsMu.Lock()
	delete(g.conns, c)
	g.connsMu.Unlock()
}

func (g *Gateway) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == 0x05 {
		g.handleSOCKS5(c, br)
		return
	}
	g.handleHTTP(c, br)
}

func (g *Gateway) handleHTTP(c net.Conn, br *bufio.Reader) {
	req, err := http.ReadRequest(br)
	if err != nil {
		writeStatus(c, http.StatusBadRequest)
		return
	}
	if req.Method == http.MethodConnect {
		g.tunnel(c, br, req.Host)
		return
	}

	target := req.URL.Host
	if target == "" {
		target = req.Host
	}
	if target == "" {
		writeStatus(c, http.StatusBadRequest)
		return
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		if req.URL.Scheme == "https" {
			target = net.JoinHostPort(target, "443")
		} else {
			target = net.JoinHostPort(target, "80")
		}
	}
	remote, err := g.dialTo(target)
	if err != nil {
		writeStatus(c, http.StatusBadGateway)
		return
	}
	defer remote.Close()

	// Rewrite absolute-form to origin-form; the request line is the only part
	// a proxy has to touch. Everything after it is passed through verbatim so
	// keep-alive and pipelining keep working.
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		writeStatus(c, http.StatusBadGateway)
		return
	}
	g.stats.up.Add(int64(buf.Len()))
	if _, err := remote.Write(buf.Bytes()); err != nil {
		return
	}
	g.relay(c, br, remote)
}

func (g *Gateway) tunnel(c net.Conn, br *bufio.Reader, target string) {
	if target == "" {
		writeStatus(c, http.StatusBadRequest)
		return
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "443")
	}
	remote, err := g.dialTo(target)
	if err != nil {
		writeStatus(c, http.StatusBadGateway)
		return
	}
	defer remote.Close()

	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\nProxy-Agent: agyproxy\r\n\r\n"); err != nil {
		return
	}
	g.relay(c, br, remote)
}

func (g *Gateway) handleSOCKS5(c net.Conn, br *bufio.Reader) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(br, head); err != nil || head[0] != 0x05 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	if req[1] != 0x01 { // only CONNECT is supported
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var host string
	switch req[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(br, l); err != nil {
			return
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(br, b); err != nil {
			return
		}
		host = string(b)
	default:
		c.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(br, port); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1])))

	remote, err := g.dialTo(target)
	if err != nil {
		c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer remote.Close()
	if _, err := c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	g.relay(c, br, remote)
}

// dialTo routes a target either directly (loopback, or the upstream itself)
// or through the configured upstream chain.
func (g *Gateway) dialTo(target string) (net.Conn, error) {
	if g.selfTarget(target) {
		return nil, fmt.Errorf("refusing to proxy %s into itself", target)
	}
	c := &net.Dialer{Timeout: 15 * time.Second}
	if isLoopbackHost(target) {
		return c.Dial("tcp", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return g.Dialer().DialContext(ctx, "tcp", target)
}

// Dialer returns the upstream currently in use.
func (g *Gateway) Dialer() upstream.Dialer {
	g.dialMu.RLock()
	defer g.dialMu.RUnlock()
	return g.dial
}

// SetDialer swaps the upstream for new connections; established tunnels keep
// the chain they were opened with.
func (g *Gateway) SetDialer(d upstream.Dialer) {
	g.dialMu.Lock()
	g.dial = d
	g.dialMu.Unlock()
}

// selfTarget guards against a client (or a misconfigured upstream) pointing
// this proxy at itself, which would recurse forever.
func (g *Gateway) selfTarget(target string) bool {
	host, port, err := net.SplitHostPort(target)
	if err != nil || port != portOf(g.ln.Addr().String()) {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (g *Gateway) relay(client net.Conn, clientReader io.Reader, remote net.Conn) {
	g.stats.active.Add(1)
	g.stats.total.Add(1)
	defer g.stats.active.Add(-1)

	client.SetDeadline(time.Time{})
	remote.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(remote, &countingReader{r: clientReader, n: &g.stats.up})
		closeWrite(remote)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, &countingReader{r: remote, n: &g.stats.down})
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	<-done
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n.Add(int64(n))
	}
	return n, err
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

func writeStatus(c net.Conn, code int) {
	body := http.StatusText(code)
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, body, len(body), body)
}

func isLoopbackHost(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}
