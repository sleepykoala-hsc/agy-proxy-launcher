// Package proc counts and terminates Antigravity processes.
//
// The identifier is an image name on Windows ("Antigravity.exe") and a
// command-line pattern elsewhere ("Antigravity.app/Contents/MacOS/Antigravity").
package proc

// Count reports how many processes match id.
func Count(id string) int { return count(id) }

// Kill terminates every process matching id, including child processes.
// It is forceful: only use it when the user asked to restart.
func Kill(id string) error { return kill(id) }

// Quit asks the application to close itself so it can still prompt for
// unsaved work. It is used when the gateway shuts down.
func Quit(id string) error { return quit(id) }

// Alive reports whether pid belongs to a running process.
func Alive(pid int) bool { return alive(pid) }

// KillPID terminates one process and its children. Used to close the dashboard
// window we opened, which lives in its own browser profile.
func KillPID(pid int) error { return killPID(pid) }
