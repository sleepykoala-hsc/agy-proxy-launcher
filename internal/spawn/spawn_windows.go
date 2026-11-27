//go:build windows

package spawn

import (
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// prepare gives the child its own process group and no console, so closing the
// cmd window that ran `agy` does not raise CTRL_CLOSE_EVENT in the child.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}

var (
	procGetConsoleWindow      = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procGetConsoleProcessList = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	procFreeConsole           = syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole")
	procPostMessage           = syscall.NewLazyDLL("user32.dll").NewProc("PostMessageW")
)

// hasConsole is false for the windowsgui build and for anything started
// detached, which is exactly when re-detaching would spawn a duplicate.
func hasConsole() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

// ownsConsole counts the processes attached to this console.
func ownsConsole() bool {
	buf := make([]uint32, 8)
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return n == 1
}

// closeConsole detaches and then posts WM_CLOSE, so a console that exists only
// for this launch disappears without taking a shell down with it.
func closeConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	procFreeConsole.Call()
	if hwnd != 0 {
		procPostMessage.Call(hwnd, 0x0010 /* WM_CLOSE */, 0, 0)
	}
}
