//go:build !windows

package launcher

import (
	"os/exec"
	"syscall"
)

// sysProcAttr detaches Antigravity into its own session so it survives the
// gateway exiting and ignores terminal signals sent to us.
func sysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
