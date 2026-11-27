//go:build windows

package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

// ensureOnPath appends dir to the per-user PATH (HKCU\Environment) and tells
// already-running shells about it.
//
// The registry is edited through .NET instead of `setx`, which truncates long
// values and would flatten REG_EXPAND_SZ entries such as %USERPROFILE%.
func ensureOnPath(dir string) (present, changed bool, err error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", pathScript)
	cmd.Env = append(os.Environ(), "AGY_DIR="+dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		return false, false, fmt.Errorf("写入用户 PATH 失败：%w %s", err, msg)
	}
	switch strings.TrimSpace(string(out)) {
	case "changed":
		return true, true, nil
	case "present":
		return true, false, nil
	default:
		return false, false, fmt.Errorf("写入用户 PATH 失败：%s", strings.TrimSpace(string(out)))
	}
}

const pathScript = `
$ErrorActionPreference = 'Stop'
$dir = $env:AGY_DIR.TrimEnd('\')
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if ($null -eq $key) { $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment') }

$current = ''
$kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
try {
  $current = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  $kind = $key.GetValueKind('Path')
} catch { }

$parts = @($current -split ';' | Where-Object { $_ -ne '' })
$first = ''
if ($parts.Count -gt 0) { $first = $parts[0].TrimEnd('\') }

if ($first -ieq $dir) {
  Write-Output 'present'
} else {
  # Prepend rather than append: Antigravity ships its own agy.exe, so the
  # launcher has to win the name lookup.
  $rest = @($parts | Where-Object { $_.TrimEnd('\') -ine $dir })
  $parts = @($dir) + $rest
  $key.SetValue('Path', ($parts -join ';'), $kind)

  Add-Type -Namespace Agy -Name Native -MemberDefinition '[DllImport("user32.dll", SetLastError=true, CharSet=CharSet.Auto)] public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);'
  $result = [UIntPtr]::Zero
  [void][Agy.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1A, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
  Write-Output 'changed'
}

$key.Close()
`
