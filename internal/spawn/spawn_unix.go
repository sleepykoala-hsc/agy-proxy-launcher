//go:build !windows

package spawn

import (
	"os"
	"os/exec"
	"syscall"
)

// prepare puts the child in a new session, so closing the terminal window
// does not send it SIGHUP.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// hasConsole is false when stdout is a file or a pipe, which covers .app
// bundles and redirected invocations.
func hasConsole() bool {
	st, err := os.Stdout.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// ownsConsole has no meaning without console windows.
func ownsConsole() bool { return false }

// closeConsole is a no-op: terminals belong to the user.
func closeConsole() {}
