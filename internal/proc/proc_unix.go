//go:build darwin || linux

package proc

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
)

func count(pattern string) int {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func kill(pattern string) error {
	return exec.Command("pkill", "-f", pattern).Run()
}

func killPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// quit asks the app to close through AppleScript on macOS and falls back to
// SIGTERM elsewhere.
func quit(pattern string) error {
	if _, err := exec.LookPath("osascript"); err == nil {
		if err := exec.Command("osascript", "-e", `tell application "Antigravity" to quit`).Run(); err == nil {
			return nil
		}
	}
	return exec.Command("pkill", "-TERM", "-f", pattern).Run()
}
