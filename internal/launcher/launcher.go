// Package launcher finds Antigravity and starts it behind the local gateway.
package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"agyproxy/internal/proc"
)

// ErrAlreadyRunning means Antigravity was up before we asked to start it.
var ErrAlreadyRunning = errors.New("Antigravity 已在运行")

// State is the app block surfaced in the UI.
type State struct {
	Path    string `json:"path"`
	Running bool   `json:"running"`
	Managed bool   `json:"managed"`
	PID     int    `json:"pid"`
	Since   string `json:"since"`
	Err     string `json:"err"`
}

// App supervises one Antigravity process.
type App struct {
	mu      sync.Mutex
	path    string
	cmd     *exec.Cmd
	pid     int
	since   time.Time
	managed bool
	err     string

	countAt  time.Time
	countVal int
}

// New creates a supervisor for the discovered binary.
func New(path string) *App { return &App{path: path} }

// Path returns the resolved executable.
func (a *App) Path() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.path
}

// SetPath overrides the executable, e.g. from the settings file.
func (a *App) SetPath(p string) {
	a.mu.Lock()
	a.path = p
	a.mu.Unlock()
}

// State reports whether Antigravity is running, ours or not.
func (a *App) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()

	if time.Since(a.countAt) > 2*time.Second {
		a.countVal = proc.Count(processID())
		a.countAt = time.Now()
	}
	since := ""
	if !a.since.IsZero() {
		since = a.since.Format("15:04:05")
	}
	return State{
		Path:    a.path,
		Running: a.managed || a.countVal > 0,
		Managed: a.managed,
		PID:     a.pid,
		Since:   since,
		Err:     a.err,
	}
}

// Start launches Antigravity with the proxy flags and environment.
func (a *App) Start(proxyURL string, extra []string) error {
	if st := a.State(); st.Running {
		return ErrAlreadyRunning
	}
	a.mu.Lock()
	path := a.path
	a.mu.Unlock()
	if path == "" {
		return errors.New("未找到 Antigravity 可执行文件")
	}

	args := []string{"--proxy-server=" + proxyURL}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		args = append(args, "--proxy-bypass-list=localhost;127.0.0.1;[::1]")
	}
	args = append(args, extra...)

	cmd := exec.Command(path, args...)
	cmd.Dir = filepath.Dir(path)
	cmd.Env = withEnv(os.Environ(), map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"ALL_PROXY":   proxyURL,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"all_proxy":   proxyURL,
		"NO_PROXY":    noProxy,
		"no_proxy":    noProxy,
	})
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	sysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		a.setErr(err.Error())
		return fmt.Errorf("启动 Antigravity 失败: %w", err)
	}

	pid := cmd.Process.Pid
	a.mu.Lock()
	a.cmd = cmd
	a.pid = pid
	a.since = time.Now()
	a.managed = true
	a.err = ""
	a.countAt = time.Time{}
	a.mu.Unlock()

	go func() {
		waitErr := cmd.Wait()
		a.mu.Lock()
		if a.pid == pid {
			a.managed = false
			a.pid = 0
			if waitErr != nil {
				a.err = waitErr.Error()
			}
		}
		a.countAt = time.Time{}
		a.mu.Unlock()
	}()
	return nil
}

// Restart kills every Antigravity process and starts a fresh proxied one.
func (a *App) Restart(proxyURL string, extra []string) error {
	a.Stop()
	if err := a.Start(proxyURL, extra); err != nil {
		return err
	}
	return nil
}

// Stop terminates Antigravity and waits for the process tree to disappear.
func (a *App) Stop() {
	before := proc.Count(processID())
	if before > 0 {
		_ = proc.Kill(processID())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if proc.Count(processID()) == 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.mu.Lock()
	a.managed = false
	a.pid = 0
	a.cmd = nil
	a.countAt = time.Time{}
	a.mu.Unlock()
}

// QuitGraceful asks Antigravity to close itself and waits for the process tree
// to go away. Unlike Stop it never forces the process, so the editor can still
// prompt about unsaved work.
func (a *App) QuitGraceful() {
	a.mu.Lock()
	managed := a.managed
	a.mu.Unlock()
	if !managed {
		return
	}
	_ = proc.Quit(processID())
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && proc.Count(processID()) > 0 {
		time.Sleep(300 * time.Millisecond)
	}
}

func (a *App) setErr(msg string) {
	a.mu.Lock()
	a.err = msg
	a.mu.Unlock()
}

const noProxy = "localhost,127.0.0.1,::1"

// withEnv applies overrides and drops pre-existing duplicates so Windows does
// not end up with two differently-cased copies of the same variable.
func withEnv(base []string, overrides map[string]string) []string {
	keys := make(map[string]bool, len(overrides))
	for k := range overrides {
		keys[strings.ToLower(k)] = true
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 && keys[strings.ToLower(kv[:i])] {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

// Discover resolves the Antigravity executable, honouring an explicit path.
func Discover(explicit string) (string, error) {
	if explicit != "" {
		if p, ok := existing(explicit); ok {
			return p, nil
		}
	}
	for _, p := range defaultPaths() {
		if found, ok := existing(p); ok {
			return found, nil
		}
	}
	for _, pattern := range globPatterns() {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if found, ok := existing(m); ok {
				return found, nil
			}
		}
	}
	if p, err := exec.LookPath(exeName()); err == nil {
		return p, nil
	}
	return "", errors.New("未找到 Antigravity，请用 -antigravity 指定可执行文件")
}

func existing(p string) (string, bool) {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return "", false
	}
	return p, true
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "Antigravity.exe"
	}
	return "Antigravity"
}

func processID() string {
	switch runtime.GOOS {
	case "windows":
		return "Antigravity.exe"
	case "darwin":
		return "Antigravity.app/Contents/MacOS/Antigravity"
	default:
		return "antigravity"
	}
}

func defaultPaths() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, base := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, "Programs", "Antigravity", "Antigravity.exe"),
				filepath.Join(base, "Programs", "antigravity", "Antigravity.exe"),
				filepath.Join(base, "Antigravity", "Antigravity.exe"),
			)
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			out = append(out, filepath.Join(local, "Programs", "Antigravity", "Antigravity.exe"))
		}
		return out
	case "darwin":
		home, _ := os.UserHomeDir()
		apps := []string{"/Applications", filepath.Join(home, "Applications")}
		names := []string{"Antigravity.app", "Google Antigravity.app"}
		var out []string
		for _, app := range apps {
			for _, name := range names {
				out = append(out, filepath.Join(app, name, "Contents", "MacOS", "Antigravity"))
			}
		}
		return out
	default:
		return []string{
			"/usr/bin/antigravity",
			"/usr/local/bin/antigravity",
			"/opt/Antigravity/antigravity",
		}
	}
}

func globPatterns() []string {
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return nil
		}
		return []string{
			filepath.Join(local, "Programs", "*ntigravity*", "Antigravity.exe"),
			filepath.Join(local, "*ntigravity*", "Antigravity.exe"),
		}
	case "darwin":
		return []string{
			"/Applications/*ntigravity*.app/Contents/MacOS/Antigravity",
			"/Applications/*Antigravity*.app/Contents/MacOS/Antigravity",
		}
	}
	return nil
}
