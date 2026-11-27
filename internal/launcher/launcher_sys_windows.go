//go:build windows

package launcher

import (
	"os/exec"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// sysProcAttr keeps Antigravity out of our console and in its own process
// group, so a Ctrl+C aimed at the gateway does not reach the editor.
func sysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup,
	}
}
