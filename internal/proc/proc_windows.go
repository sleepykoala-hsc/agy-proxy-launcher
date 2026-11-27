//go:build windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	th32csSnapProcess = 0x00000002
	invalidHandle     = ^uintptr(0)
	// createNoWindow stops helper processes (taskkill, powershell) from opening
	// a console window. The windowed build has no console of its own, so
	// without this every call would flash a cmd window at the user.
	createNoWindow = 0x08000000
)

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

// count walks the process snapshot instead of running tasklist: the dashboard
// asks for the state once per second, and spawning a console process that
// often is both slow and visible.
func count(image string) int {
	n := 0
	walk(func(name string, _ uint32) {
		if strings.EqualFold(name, image) {
			n++
		}
	})
	return n
}

func alive(pid int) bool {
	found := false
	walk(func(_ string, p uint32) {
		if int(p) == pid {
			found = true
		}
	})
	return found
}

// walk calls fn for every process in the system snapshot.
func walk(fn func(name string, pid uint32)) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == invalidHandle {
		return
	}
	defer procCloseHandle.Call(snap)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	ok, _, _ := procProcess32FirstW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		fn(syscall.UTF16ToString(entry.ExeFile[:]), entry.ProcessID)
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, _ = procProcess32NextW.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
}

func kill(image string) error {
	return hidden("taskkill", "/IM", image, "/T", "/F").Run()
}

func killPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return hidden("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// quit posts WM_CLOSE to every top-level window owned by the app.
//
// taskkill without /F cannot terminate an Electron process ("can only be
// terminated forcefully"), and Process.CloseMainWindow often reports no window
// for the main process, so enumerate the windows of the whole process tree and
// close them individually. The app then runs its normal shutdown and can still
// prompt about unsaved work.
func quit(image string) error {
	name := strings.TrimSuffix(strings.TrimSuffix(image, ".exe"), ".EXE")
	cmd := hidden("powershell", "-NoProfile", "-NonInteractive", "-Command", closeWindowsScript)
	cmd.Env = append(os.Environ(), "AGY_PROCESS="+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("关闭 %s 失败: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// hidden builds a command that never flashes a console window.
func hidden(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}

const closeWindowsScript = `
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
public class AgyWin {
  public delegate bool EnumProc(IntPtr hWnd, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumProc cb, IntPtr lParam);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hWnd, uint msg, IntPtr wParam, IntPtr lParam);
  public static List<IntPtr> Find(uint[] pids) {
    HashSet<uint> set = new HashSet<uint>(pids);
    List<IntPtr> found = new List<IntPtr>();
    EnumWindows(delegate(IntPtr h, IntPtr l) {
      uint pid;
      GetWindowThreadProcessId(h, out pid);
      if (set.Contains(pid)) found.Add(h);
      return true;
    }, IntPtr.Zero);
    return found;
  }
}
'@
$procs = @(Get-Process -Name $env:AGY_PROCESS -ErrorAction SilentlyContinue)
if ($procs.Count -gt 0) {
  $ids = @($procs | Select-Object -ExpandProperty Id)
  foreach ($h in [AgyWin]::Find([uint32[]]$ids)) {
    [void][AgyWin]::PostMessage($h, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero)
  }
}
`
