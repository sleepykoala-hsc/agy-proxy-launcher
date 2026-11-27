// Package spawn starts a process that outlives the terminal that launched it.
package spawn

import "os/exec"

// Prepare detaches cmd from the parent's console/session.
func Prepare(cmd *exec.Cmd) { prepare(cmd) }

// HasConsole reports whether this process owns the console it was started
// from. A windowed build, a .app bundle and a redirected command all report
// false, and those must run in place instead of spawning a detached copy.
func HasConsole() bool { return hasConsole() }

// OwnsConsole reports whether this is the only process attached to its console.
// That means the window exists just for us (double-clicked exe, Win+R), so it
// can be closed without killing somebody's shell.
func OwnsConsole() bool { return ownsConsole() }

// CloseConsole closes this process's console window. Only call it when
// OwnsConsole is true: it terminates every process attached to that console.
func CloseConsole() { closeConsole() }
