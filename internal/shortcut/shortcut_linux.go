//go:build linux

package shortcut

import (
	"fmt"
	"os"
	"path/filepath"
)

// Install writes a .desktop entry on the desktop.
func Install(target, icon string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	desktop := filepath.Join(home, "Desktop")
	if err := os.MkdirAll(desktop, 0o755); err != nil {
		return "", err
	}
	body := "[Desktop Entry]\nType=Application\nName=G Proxy\n" +
		"Comment=Antigravity 代理网关\nExec=\"" + target + "\"\n" +
		"Terminal=false\nCategories=Network;\n"
	if icon != "" {
		body += "Icon=" + icon + "\n"
	}
	path := filepath.Join(desktop, "g-proxy.desktop")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("创建桌面图标失败: %w", err)
	}
	return path, nil
}
