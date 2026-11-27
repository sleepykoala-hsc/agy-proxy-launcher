//go:build windows

package shortcut

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Install creates "G Proxy.lnk" on the desktop and returns its path.
func Install(target, icon string) (string, error) {
	// Paths travel through the environment so no quoting rule can break them.
	script := `
$ErrorActionPreference = 'Stop'
$shell = New-Object -ComObject WScript.Shell
$desktop = [Environment]::GetFolderPath('Desktop')
$lnk = Join-Path $desktop 'G Proxy.lnk'
$s = $shell.CreateShortcut($lnk)
$s.TargetPath = $env:AGY_TARGET
$s.WorkingDirectory = Split-Path -Parent $env:AGY_TARGET
if ($env:AGY_ICON -ne '') { $s.IconLocation = $env:AGY_ICON }
$s.Description = 'Antigravity 代理网关'
$s.WindowStyle = 1
$s.Save()
Write-Output $lnk
`
	if icon == "" {
		icon = target + ",0"
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "AGY_TARGET="+target, "AGY_ICON="+icon)
	// The windowed build has no console; keep the helper from opening one.
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("创建桌面快捷方式失败: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
