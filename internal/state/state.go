// Package state records the running instance so a second `agy` can reuse it
// instead of fighting over the gateway port.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"agyproxy/internal/config"
	"agyproxy/internal/proc"
)

// Info is what a running instance publishes about itself.
type Info struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	UI      string `json:"ui"`
	Started string `json:"startedAt"`
}

// Path is the state file location, next to the settings file.
func Path() string { return filepath.Join(config.Dir(), "state.json") }

// Write publishes the current instance.
func Write(info Info) error {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), raw, 0o600)
}

// Remove deletes the state file; safe to call when it does not exist.
func Remove() { _ = os.Remove(Path()) }

// Load returns the live instance. A file left behind by a crashed process is
// detected via its PID and cleaned up, so callers never act on a ghost.
func Load() (Info, bool) {
	raw, err := os.ReadFile(Path())
	if err != nil {
		return Info{}, false
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		Remove()
		return Info{}, false
	}
	if info.PID <= 0 || !proc.Alive(info.PID) {
		Remove()
		return Info{}, false
	}
	return info, true
}

// ErrNotRunning is returned by Stop when no instance is live.
var ErrNotRunning = errors.New("没有正在运行的实例")
