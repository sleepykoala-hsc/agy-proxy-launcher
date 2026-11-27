// Package install puts the binaries in a stable per-user location and makes the
// agy command available from a shell.
package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Result describes what the installer did.
type Result struct {
	Dir         string
	Console     string
	GUI         string
	PathChanged bool
	OnPath      bool
}

// Run copies the current binaries into the install dir and registers the
// directory on PATH. exe is the running executable; a sibling "<name>w.exe"
// (the windowed build) is installed as agyw when present.
func Run(exe string) (Result, error) {
	dir, err := TargetDir()
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("创建目录 %s 失败：%w", dir, err)
	}

	res := Result{Dir: dir, Console: filepath.Join(dir, exeName("agy"))}
	if err := copyExe(exe, res.Console); err != nil {
		return res, fmt.Errorf("安装 %s 失败：%w", res.Console, err)
	}

	if twin := GUI(exe); twin != "" {
		res.GUI = filepath.Join(dir, exeName("agyw"))
		if err := copyExe(twin, res.GUI); err != nil {
			return res, fmt.Errorf("安装 %s 失败：%w", res.GUI, err)
		}
	}

	res.OnPath, res.PathChanged, err = ensureOnPath(dir)
	if err != nil {
		return res, err
	}
	return res, nil
}

// TargetDir is where the command lives once installed.
func TargetDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", fmt.Errorf("LOCALAPPDATA 未设置")
		}
		return filepath.Join(base, "Programs", "AgyProxy"), nil
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "bin"), nil
	}
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// GUI returns the windowed build that sits next to exe, or "" when there is
// none. The windowed build is what a background launch should use: it never
// owns a console window.
func GUI(exe string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	switch {
	case strings.HasSuffix(exe, "w.exe"):
		return "" // already the windowed build
	case strings.HasSuffix(exe, ".exe"):
		twin := strings.TrimSuffix(exe, ".exe") + "w.exe"
		if st, err := os.Stat(twin); err == nil && !st.IsDir() {
			return twin
		}
	}
	return ""
}

// copyExe copies src over dst, moving a locked destination aside first (Windows
// refuses to overwrite a running executable, but it can be renamed).
func copyExe(src, dst string) error {
	if a, err := filepath.Abs(src); err == nil {
		if b, err := filepath.Abs(dst); err == nil && strings.EqualFold(a, b) {
			return nil
		}
	}
	if _, err := os.Stat(src); err != nil {
		return err
	}
	if err := copyFile(src, dst); err == nil {
		return nil
	}

	old := dst + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dst, old); err != nil {
		return fmt.Errorf("目标文件被占用且无法替换：%w", err)
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	_ = os.Remove(old)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
