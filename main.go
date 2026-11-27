// Command agyproxy is a lightweight, cross-platform launcher that starts
// Antigravity behind an observable local proxy and shows a live dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"agyproxy/internal/config"
	"agyproxy/internal/gateway"
	"agyproxy/internal/icon"
	"agyproxy/internal/install"
	"agyproxy/internal/launcher"
	"agyproxy/internal/monitor"
	"agyproxy/internal/proc"
	"agyproxy/internal/shortcut"
	"agyproxy/internal/spawn"
	"agyproxy/internal/state"
	"agyproxy/internal/upstream"
	"agyproxy/internal/webui"
)

const version = "1.0.0"

func main() {
	var (
		portFlag     = flag.Int("port", 0, "本地网关端口，默认 10909")
		upstreamFlag = flag.String("upstream", "", "上游代理：http://127.0.0.1:7890、socks5://127.0.0.1:1080 或 direct；留空自动探测")
		agyFlag      = flag.String("antigravity", "", "Antigravity 可执行文件路径；留空自动查找")
		noLaunch     = flag.Bool("no-launch", false, "只启动网关，不启动 Antigravity")
		noWindow     = flag.Bool("no-window", false, "不弹出监控窗口")
		installFlag  = flag.Bool("install", false, "安装到用户目录、写入 PATH 并创建桌面图标后退出")
		shortcutOnly = flag.Bool("install-shortcut", false, "仅创建桌面快捷方式后退出")
		foreground   = flag.Bool("fg", false, "前台运行，日志打在终端里（Ctrl+C 退出）")
		closeFlag    = flag.Bool("close", false, "启动后关闭当前控制台窗口（会一起结束该窗口里的其他任务）")
		stopFlag     = flag.Bool("stop", false, "退出正在后台运行的实例")
		versionFlag  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *versionFlag {
		fmt.Printf("agyproxy %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Printf("配置读取失败，使用默认值：%v", err)
	}
	if _, statErr := os.Stat(config.Path()); errors.Is(statErr, fs.ErrNotExist) {
		if err := config.Save(cfg); err != nil {
			log.Printf("写入默认配置失败：%v", err)
		}
	}

	if *portFlag > 0 {
		cfg.Port = *portFlag
	}
	if *upstreamFlag != "" {
		if err := applyUpstreamFlag(&cfg, *upstreamFlag); err != nil {
			log.Fatalf("无效的 -upstream 参数：%v", err)
		}
	}
	if *agyFlag != "" {
		cfg.AntigravityPath = *agyFlag
	}

	if *installFlag || *shortcutOnly {
		runInstall(*installFlag)
		return
	}

	if *stopFlag {
		if err := stopRunning(); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	// Reuse a live instance instead of failing on the busy gateway port.
	if info, ok := state.Load(); ok {
		if !*noWindow && info.UI != "" {
			openAppWindow(info.UI)
		}
		fmt.Printf("已在运行：PID %d\n", info.PID)
		if info.UI != "" {
			fmt.Printf("监控面板: %s\n", info.UI)
		}
		fmt.Println("停止请用: agy -stop")
		return
	}

	// `agy` hands off to a detached copy so the prompt comes back immediately
	// and closing the terminal does not stop the gateway. Only a process that
	// owns a console needs this; the windowed build and .app bundles run here.
	if !*foreground && os.Getenv(daemonEnv) == "" && spawn.HasConsole() {
		// A console that exists only for this launch (double-clicked exe,
		// Win+R) is closed afterwards; a shell the user is typing in is not,
		// unless they ask for it with -close.
		quiet := spawn.OwnsConsole() || *closeFlag
		if err := startDaemon(quiet); err != nil {
			log.Fatalf("后台启动失败：%v", err)
		}
		if quiet {
			spawn.CloseConsole()
		}
		return
	}

	quit := make(chan struct{})
	var quitOnce sync.Once
	shutdown := func() { quitOnce.Do(func() { close(quit) }) }

	listenAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	up := resolveUpstream(cfg, listenAddr)
	chain := &chainState{
		dialer:   up.dialer,
		kind:     chainKind(up),
		addr:     chainAddr(up),
		source:   chainSource(up),
		verified: up.candidate == nil || up.candidate.Verified,
	}

	gw, err := gateway.New(listenAddr, up.dialer)
	if err != nil {
		log.Fatalf("本地网关启动失败：%v\n端口 %d 可能已被占用，可用 -port 换一个端口。", err, cfg.Port)
	}
	go func() {
		if err := gw.Serve(); err != nil {
			log.Printf("网关退出：%v", err)
			shutdown()
		}
	}()

	proxyURL := "http://" + gw.Addr()
	googleEvery := cfg.GoogleEvery

	mon := monitor.New(up.dialer, gw.Stats(), gw.Addr(), cfg.Port,
		time.Duration(cfg.ProbeIntervalMs)*time.Millisecond, googleEvery)
	go mon.Run()

	agyPath, findErr := launcher.Discover(cfg.AntigravityPath)
	app := launcher.New(agyPath)

	var cfgMu sync.Mutex

	stateFn := func() any {
		mon.Touch() // proves the dashboard is still alive, see startWatchdog
		appState := app.State()
		info := chain.info()
		warns := make([]string, 0, 3)
		if appState.Path == "" {
			warns = append(warns, "未找到 Antigravity，请用 -antigravity 指定或在 "+config.Path()+" 的 antigravityPath 中填写")
		}
		if info.Direct {
			warns = append(warns, "未检测到上游代理，当前直连 → 在面板「上游代理」里选择或填写一个代理地址")
		} else if !info.Verified {
			warns = append(warns, "上游 "+info.Addr+" 端口开放但握手失败，请确认代理可用")
		}
		if appState.Err != "" {
			warns = append(warns, "Antigravity: "+appState.Err)
		}
		return uiState{
			Snapshot: mon.Snapshot(info.describe(), info.Direct),
			Upstream: info,
			App:      appState,
			Warnings: warns,
		}
	}

	// scanFn lists local proxies for the settings panel.
	scanFn := func() any {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return upstream.Scan(ctx, selfSkip(listenAddr))
	}

	// setUpstreamFn validates and hot-swaps the chain, then persists it.
	setUpstreamFn := func(kind, addr string) error {
		kind = strings.ToLower(strings.TrimSpace(kind))
		addr = strings.TrimSpace(addr)

		if kind == upstream.KindDirect || (kind == "" && addr == "direct") {
			d := upstream.Direct{}
			gw.SetDialer(d)
			mon.SetDialer(d)
			chain.set(d, upstream.KindDirect, "", "手动设置", true)
			cfgMu.Lock()
			cfg.UpstreamKind, cfg.UpstreamAddr = upstream.KindDirect, ""
			saveErr := config.Save(cfg)
			cfgMu.Unlock()
			if saveErr != nil {
				return fmt.Errorf("已切换为直连，但写入配置失败：%v", saveErr)
			}
			return nil
		}

		if kind == "" {
			kind = upstream.KindHTTP
		}
		if addr == "" {
			return errors.New("请填写代理地址，例如 127.0.0.1:7890")
		}
		addr = normalizeAddr(addr)

		// Reuse stored credentials only when re-applying the same address.
		user, pass := "", ""
		cfgMu.Lock()
		if strings.EqualFold(addr, cfg.UpstreamAddr) {
			user, pass = cfg.UpstreamUser, cfg.UpstreamPass
		}
		cfgMu.Unlock()

		d, err := upstream.New(kind, addr, user, pass)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := upstream.Verify(ctx, d); err != nil {
			return fmt.Errorf("%s 无法转发流量：%s", addr, friendlyDialError(err))
		}

		gw.SetDialer(d)
		mon.SetDialer(d)
		chain.set(d, kind, addr, "手动设置", true)

		cfgMu.Lock()
		cfg.UpstreamKind, cfg.UpstreamAddr = kind, addr
		cfg.UpstreamUser, cfg.UpstreamPass = user, pass
		saveErr := config.Save(cfg)
		cfgMu.Unlock()
		if saveErr != nil {
			return fmt.Errorf("已切换上游，但写入配置失败：%v", saveErr)
		}
		return nil
	}

	actionFn := func(name string) error {
		switch name {
		case "restart", "launch":
			if agyPath == "" {
				return errors.New("未找到 Antigravity 可执行文件")
			}
			return app.Restart(proxyURL, cfg.ExtraArgs)
		case "quit":
			go func() {
				time.Sleep(250 * time.Millisecond)
				shutdown()
			}()
			return nil
		default:
			return fmt.Errorf("未知操作 %q", name)
		}
	}

	ui, err := webui.New("127.0.0.1:0", webui.Options{
		State:       stateFn,
		Action:      actionFn,
		Scan:        scanFn,
		SetUpstream: setUpstreamFn,
	})
	if err != nil {
		log.Fatalf("监控面板启动失败：%v", err)
	}
	go func() { _ = ui.Serve() }()

	// Publish this instance so a second `agy` focuses it instead of starting a
	// second gateway, and so `agy -stop` can find it.
	if err := state.Write(state.Info{
		PID:     os.Getpid(),
		Port:    cfg.Port,
		UI:      ui.URL(),
		Started: time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Printf("写入运行状态失败（不影响使用）：%v", err)
	}
	defer state.Remove()

	openWindow := cfg.OpenWindow && !*noWindow
	uiPID := 0
	if openWindow {
		uiPID = openAppWindow(ui.URL())
	}

	autoLaunch := cfg.AutoLaunch && !*noLaunch
	if autoLaunch {
		if findErr != nil {
			log.Printf("%v", findErr)
		} else if err := app.Start(proxyURL, cfg.ExtraArgs); err != nil {
			if errors.Is(err, launcher.ErrAlreadyRunning) {
				log.Printf("检测到已在运行的 Antigravity，未重复启动；如需走代理请退出后重启本工具")
			} else {
				log.Printf("%v", err)
			}
		}
	}

	printBanner(proxyURL, up, agyPath, ui.URL())

	if openWindow {
		startWatchdog(mon, app, shutdown)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case <-quit:
	case sig := <-sigCh:
		log.Printf("收到 %v，正在关闭网关", sig)
	}

	mon.Stop()
	_ = ui.Shutdown()
	// Close the editor while the proxy is still up so its shutdown requests
	// (telemetry, session flush) do not fail.
	app.QuitGraceful()
	_ = gw.Close()
	// The dashboard lives in its own browser profile; without this a stale
	// "网关已停止" window survives and the next launch reuses its size.
	if uiPID > 0 {
		_ = proc.KillPID(uiPID)
	}
	log.Println("已退出")
}

// runInstall copies the binaries to the per-user install dir, registers that
// dir on PATH and drops a desktop icon pointing at the installed build.
func runInstall(full bool) {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("无法定位自身可执行文件：%v", err)
	}

	icoPath, err := icon.Ensure(config.Dir())
	if err != nil {
		log.Printf("生成图标失败（将使用默认图标）：%v", err)
		icoPath = ""
	}

	target := exe
	if full {
		res, err := install.Run(exe)
		if err != nil {
			log.Fatalf("%v", err)
		}
		target = res.Console
		if res.GUI != "" {
			target = res.GUI
		}
		fmt.Printf("已安装到 %s\n", res.Dir)
		fmt.Printf("  agy    任意目录直接运行（默认后台常驻，关掉 cmd 也不影响）\n")
		if res.GUI != "" {
			fmt.Printf("  agyw   无窗口启动（桌面图标使用）\n")
		}
		switch {
		case res.PathChanged:
			fmt.Println("已写入用户 PATH，重新打开 cmd 后即可使用 agy")
		case res.OnPath:
			fmt.Println("用户 PATH 已包含该目录，直接使用 agy 即可")
		default:
			fmt.Printf("该目录不在 PATH 中，请手动添加：%s\n", res.Dir)
			fmt.Printf("  export PATH=\"%s:$PATH\"\n", res.Dir)
		}
	} else if strings.HasSuffix(exe, ".exe") {
		// Prefer the windowed build so double-clicking never flashes a console.
		if twin := strings.TrimSuffix(exe, ".exe") + "w.exe"; fileExists(twin) {
			target = twin
		}
	}

	icon, err := shortcut.Install(target, icoPath)
	if err != nil {
		log.Fatalf("%v", err)
	}
	fmt.Println("桌面图标:", icon)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// daemonEnv marks the detached copy so it does not re-detach forever.
const daemonEnv = "AGY_DAEMON"

// startDaemon relaunches the app without a console and returns as soon as the
// child publishes itself, so the shell prompt comes back. quiet suppresses the
// summary when the caller is about to close its own console window.
func startDaemon(quiet bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	child := exe
	if gui := install.GUI(exe); gui != "" {
		child = gui // windowed build: never owns a console
	}

	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(config.Dir(), "run.log")
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(logPath); err == nil && st.Size() > 2<<20 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	logFile, err := os.OpenFile(logPath, flags, 0o600)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(child, os.Args[1:]...)
	spawn.Prepare(cmd)
	cmd.Env = append(os.Environ(), daemonEnv+"=1")
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := state.Load(); ok {
			if !quiet {
				fmt.Println("G PROXY 已在后台启动")
				if info.UI != "" {
					fmt.Printf("  监控面板  %s\n", info.UI)
				}
				fmt.Printf("  网关端口  %d\n", info.Port)
				fmt.Printf("  日志      %s\n", logPath)
				fmt.Println("停止请用: agy -stop")
			}
			return nil
		}
		time.Sleep(120 * time.Millisecond)
	}
	if !quiet {
		fmt.Println("已在后台启动，监控面板稍后出现")
		fmt.Printf("  日志  %s\n", logPath)
		fmt.Println("停止请用: agy -stop")
	}
	return nil
}

// stopRunning asks the live instance to shut down through its own API.
func stopRunning() error {
	info, ok := state.Load()
	if !ok {
		fmt.Println("当前没有正在运行的实例")
		return nil
	}
	if info.UI == "" {
		return errors.New("实例尚未就绪，请稍后再试")
	}

	req, err := http.NewRequest(http.MethodPost,
		strings.TrimSuffix(info.UI, "/")+"/api/action",
		strings.NewReader(`{"action":"quit"}`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("无法通知实例退出：%w", err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if _, still := state.Load(); !still {
			fmt.Println("已退出")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("实例未在 25 秒内退出")
}

// friendlyDialError turns Windows/Go dial noise into something the settings
// panel can show without scaring the user.
func friendlyDialError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "actively refused"),
		strings.Contains(msg, "forcibly closed"):
		return "连接被拒绝，请确认这个端口上是正在运行的代理"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"):
		return "连接超时，代理可能没有响应"
	case strings.Contains(msg, "no such host"):
		return "地址无法解析"
	}
	return msg
}

// uiState is the JSON document the dashboard polls.
type uiState struct {
	monitor.Snapshot
	Upstream upstreamInfo   `json:"upstreamInfo"`
	App      launcher.State `json:"app"`
	Warnings []string       `json:"warnings"`
}

// upstreamInfo is the structured view of the current chain.
type upstreamInfo struct {
	Kind     string `json:"kind"`
	Addr     string `json:"addr"`
	Source   string `json:"source"`
	Direct   bool   `json:"direct"`
	Verified bool   `json:"verified"`
}

func (u upstreamInfo) describe() string {
	if u.Direct || u.Kind == upstream.KindDirect {
		return "direct"
	}
	return u.Kind + "://" + u.Addr
}

// chainState holds the live upstream; the dashboard can swap it at runtime.
type chainState struct {
	mu       sync.RWMutex
	dialer   upstream.Dialer
	kind     string
	addr     string
	source   string
	verified bool
}

func (c *chainState) info() upstreamInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return upstreamInfo{
		Kind:     c.kind,
		Addr:     c.addr,
		Source:   c.source,
		Direct:   c.kind == "" || c.kind == upstream.KindDirect,
		Verified: c.verified,
	}
}

func (c *chainState) set(d upstream.Dialer, kind, addr, source string, verified bool) {
	c.mu.Lock()
	c.dialer, c.kind, c.addr, c.source, c.verified = d, kind, addr, source, verified
	c.mu.Unlock()
}

func chainKind(up upstreamChoice) string {
	if up.direct {
		return upstream.KindDirect
	}
	if up.candidate != nil {
		return up.candidate.Kind
	}
	return upstream.KindHTTP
}

func chainAddr(up upstreamChoice) string {
	if up.candidate != nil {
		return up.candidate.Addr
	}
	return ""
}

func chainSource(up upstreamChoice) string {
	if up.candidate != nil {
		return up.candidate.Source
	}
	return "自动探测"
}

// selfSkip keeps the scanner from ever proposing our own gateway port.
func selfSkip(listenAddr string) map[string]bool {
	_, port, _ := net.SplitHostPort(listenAddr)
	return map[string]bool{
		net.JoinHostPort("127.0.0.1", port): true,
		net.JoinHostPort("localhost", port): true,
	}
}

// normalizeAddr accepts "7890", "host", "host:port" or a full proxy URL and
// returns host:port.
func normalizeAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		if _, addr, _, _, err := upstream.ParseURL(raw); err == nil {
			return addr
		}
	}
	// A bare port is a common shortcut for a local proxy.
	if port, err := strconv.Atoi(raw); err == nil && port > 0 && port < 65536 {
		return net.JoinHostPort("127.0.0.1", raw)
	}
	if _, _, err := net.SplitHostPort(raw); err != nil {
		return net.JoinHostPort(raw, "7890")
	}
	return raw
}

// upstreamChoice is the resolved proxy chain.
type upstreamChoice struct {
	dialer    upstream.Dialer
	desc      string
	direct    bool
	candidate *upstream.Candidate
}

func resolveUpstream(cfg config.Config, listenAddr string) upstreamChoice {
	_, port, _ := net.SplitHostPort(listenAddr)
	skip := map[string]bool{
		net.JoinHostPort("127.0.0.1", port): true,
		net.JoinHostPort("localhost", port): true,
	}

	if cfg.UpstreamKind == upstream.KindDirect && cfg.UpstreamAddr == "" {
		return upstreamChoice{dialer: upstream.Direct{}, desc: "direct", direct: true}
	}
	if cfg.UpstreamAddr != "" {
		d, err := upstream.New(cfg.UpstreamKind, cfg.UpstreamAddr, cfg.UpstreamUser, cfg.UpstreamPass)
		if err != nil {
			log.Printf("上游配置无效（%v），改为自动探测", err)
		} else {
			return upstreamChoice{
				dialer: d,
				desc:   d.Describe(),
				candidate: &upstream.Candidate{
					Kind:     cfg.UpstreamKind,
					Addr:     cfg.UpstreamAddr,
					Source:   "配置文件",
					Verified: true,
				},
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cand, d := upstream.Detect(ctx, skip)
	if cand == nil {
		return upstreamChoice{dialer: d, desc: "direct", direct: true}
	}
	return upstreamChoice{dialer: d, desc: cand.Describe(), candidate: cand}
}

// applyUpstreamFlag turns -upstream into config fields.
func applyUpstreamFlag(cfg *config.Config, raw string) error {
	if raw == "direct" {
		cfg.UpstreamKind = upstream.KindDirect
		cfg.UpstreamAddr = ""
		return nil
	}
	kind, addr, user, pass, err := upstream.ParseURL(raw)
	if err != nil {
		return err
	}
	cfg.UpstreamKind = kind
	cfg.UpstreamAddr = addr
	cfg.UpstreamUser = user
	cfg.UpstreamPass = pass
	return nil
}

// startWatchdog closes the gateway when the dashboard is gone and Antigravity
// is no longer running, so a double-click never leaves an orphan process.
func startWatchdog(mon *monitor.Monitor, app *launcher.App, shutdown func()) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			last := mon.LastPoll()
			if last.IsZero() || time.Since(last) < 3*time.Minute {
				continue
			}
			if app.State().Running {
				continue
			}
			log.Println("监控窗口已关闭且 Antigravity 未运行，自动退出")
			shutdown()
			return
		}
	}()
}

func printBanner(proxyURL string, up upstreamChoice, agyPath, uiURL string) {
	log.Printf("G PROXY %s · Antigravity 网关", version)
	log.Printf("本地网关: %s (HTTP CONNECT + SOCKS5)", proxyURL)
	if up.direct {
		log.Printf("上游代理: 未检测到，当前直连")
	} else {
		log.Printf("上游代理: %s", up.desc)
	}
	log.Printf("监控面板: %s", uiURL)
	if agyPath != "" {
		log.Printf("Antigravity: %s", agyPath)
	} else {
		log.Printf("Antigravity: 未找到，请用 -antigravity 指定")
	}
}

// openAppWindow shows the dashboard in a chrome-less app window and returns
// the browser PID (0 when unknown) so it can be closed with the gateway.
//
// The window uses its own browser profile: when the browser is already running
// it hands the URL to the existing process, which silently ignores
// --window-size and leaves the panel in a huge empty window.
func openAppWindow(url string) int {
	args := []string{
		"--app=" + url,
		// Chromium reads this in physical pixels on Windows; the panel needs
		// ~480 CSS px (452 + page padding), so 520 leaves an even margin.
		"--window-size=520,1040",
		"--user-data-dir=" + filepath.Join(config.Dir(), "ui-profile"),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,MediaRouter",
	}
	switch runtime.GOOS {
	case "windows":
		candidates := []string{
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		}
		for _, exe := range candidates {
			if _, err := os.Stat(exe); err != nil {
				continue
			}
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err != nil {
				continue
			}
			pid := cmd.Process.Pid
			_ = cmd.Process.Release()
			return pid
		}
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		for _, name := range []string{"Google Chrome", "Microsoft Edge"} {
			cmdArgs := append([]string{"-na", name, "--args"}, args...)
			if err := exec.Command("open", cmdArgs...).Run(); err == nil {
				return 0 // `open` does not expose the app's pid
			}
		}
		_ = exec.Command("open", url).Start()
	default:
		_ = exec.Command("xdg-open", url).Start()
	}
	return 0
}
