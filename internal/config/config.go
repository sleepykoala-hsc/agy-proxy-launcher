// Package config persists user settings next to the app data directory.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// DefaultPort is the local gateway port shown in the UI footer.
const DefaultPort = 10909

// Config is the on-disk settings file.
type Config struct {
	Port            int      `json:"port"`
	UpstreamKind    string   `json:"upstreamKind"`
	UpstreamAddr    string   `json:"upstreamAddr"`
	UpstreamUser    string   `json:"upstreamUser"`
	UpstreamPass    string   `json:"upstreamPass"`
	AntigravityPath string   `json:"antigravityPath"`
	ExtraArgs       []string `json:"extraArgs"`
	ProbeIntervalMs int      `json:"probeIntervalMs"`
	GoogleEvery     int      `json:"googleEveryNRounds"`
	OpenWindow      bool     `json:"openWindow"`
	AutoLaunch      bool     `json:"autoLaunch"`
}

// Default returns the built-in settings.
func Default() Config {
	return Config{
		Port:            DefaultPort,
		ProbeIntervalMs: 2000,
		GoogleEvery:     5,
		OpenWindow:      true,
		AutoLaunch:      true,
	}
}

// Dir is the per-user settings directory.
func Dir() string {
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "AgyProxy")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "AgyProxy")
		}
	default:
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "agyproxy")
		}
	}
	return "."
}

// Path is the settings file location.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the settings file, creating defaults on first run.
// A malformed file is reported but does not block startup.
func Load() (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", Path(), err)
	}
	cfg.normalize()
	return cfg, nil
}

func (c *Config) normalize() {
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = DefaultPort
	}
	if c.ProbeIntervalMs < 500 {
		c.ProbeIntervalMs = Default().ProbeIntervalMs
	}
	if c.GoogleEvery < 1 {
		c.GoogleEvery = Default().GoogleEvery
	}
}

// Save writes the settings with owner-only permissions.
func Save(c Config) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), append(raw, '\n'), 0o600)
}
