// Package webui serves the embedded dashboard.
package webui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"time"
)

//go:embed web
var assets embed.FS

// Options wires the dashboard to the running gateway.
type Options struct {
	// State returns the snapshot the page polls.
	State func() any
	// Action runs a named command ("restart", "quit", ...).
	Action func(string) error
	// Scan lists local proxies currently listening.
	Scan func() any
	// SetUpstream validates and applies a new upstream chain.
	SetUpstream func(kind, addr string) error
}

// Server is the loopback dashboard HTTP server.
type Server struct {
	ln   net.Listener
	srv  *http.Server
	opts Options
}

// New listens on addr and prepares the handlers.
func New(addr string, opts Options) (*Server, error) {
	sub, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	s := &Server{ln: ln, opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/upstream/scan", s.handleScan)
	mux.HandleFunc("/api/upstream", s.handleSetUpstream)
	mux.Handle("/", http.FileServer(http.FS(sub)))

	s.srv = &http.Server{
		Handler:           noStore(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// Addr is the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// URL is the dashboard address including the scheme.
func (s *Server) URL() string { return "http://" + s.Addr() + "/" }

// Serve blocks until Shutdown.
func (s *Server) Serve() error { return s.srv.Serve(s.ln) }

// Shutdown stops accepting requests.
func (s *Server) Shutdown() error { return s.srv.Close() }

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s.opts.State())
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的请求体"})
		return
	}
	if err := s.opts.Action(body.Action); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.opts.Scan == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "扫描不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": s.opts.Scan()})
}

func (s *Server) handleSetUpstream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.opts.SetUpstream == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "不支持修改上游"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
		Addr string `json:"addr"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "无效的请求体"})
		return
	}
	if err := s.opts.SetUpstream(body.Kind, body.Addr); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, must-revalidate")
		next.ServeHTTP(w, r)
	})
}
