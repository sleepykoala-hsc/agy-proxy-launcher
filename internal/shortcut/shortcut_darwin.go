//go:build darwin

package shortcut

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"agyproxy/internal/icon"
)

// Install builds a minimal "G Proxy.app" on the desktop. A real bundle is used
// instead of a .command script so double-clicking never opens Terminal.
func Install(target, iconPath string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	app := filepath.Join(home, "Desktop", "G Proxy.app")
	macOS := filepath.Join(app, "Contents", "MacOS")
	resources := filepath.Join(app, "Contents", "Resources")
	for _, dir := range []string{macOS, resources} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}

	// The bundle executable is a shell shim so the Go binary can live anywhere.
	script := "#!/bin/sh\nmkdir -p \"$HOME/Library/Logs\"\nexec \"" + target +
		"\" >> \"$HOME/Library/Logs/agyproxy.log\" 2>&1\n"
	if err := os.WriteFile(filepath.Join(macOS, "gproxy"), []byte(script), 0o755); err != nil {
		return "", err
	}

	icns := ""
	if iconFile, err := buildICNS(resources); err == nil {
		icns = filepath.Base(iconFile)
	}

	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>G Proxy</string>
  <key>CFBundleDisplayName</key><string>G Proxy</string>
  <key>CFBundleIdentifier</key><string>dev.agyproxy.launcher</string>
  <key>CFBundleVersion</key><string>1.0.0</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>gproxy</string>
  <key>LSUIElement</key><false/>
  <key>NSHighResolutionCapable</key><true/>
`
	if icns != "" {
		plist += "  <key>CFBundleIconFile</key><string>" + icns + "</string>\n"
	}
	plist += "</dict>\n</plist>\n"

	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		return "", err
	}
	return app, nil
}

// buildICNS renders the icon at the sizes iconutil expects and converts the
// iconset into AppIcon.icns. Any failure is non-fatal: the bundle simply keeps
// the default icon.
func buildICNS(resources string) (string, error) {
	base, err := os.MkdirTemp("", "agyproxy-iconset")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(base)

	iconset := filepath.Join(base, "AppIcon.iconset")
	if err := os.MkdirAll(iconset, 0o755); err != nil {
		return "", err
	}
	files := map[string]int{
		"icon_16x16.png":      16,
		"icon_16x16@2x.png":   32,
		"icon_32x32.png":      32,
		"icon_32x32@2x.png":   64,
		"icon_128x128.png":    128,
		"icon_128x128@2x.png": 256,
		"icon_256x256.png":    256,
		"icon_256x256@2x.png": 512,
		"icon_512x512.png":    512,
		"icon_512x512@2x.png": 1024,
	}
	for name, size := range files {
		data, err := icon.PNG(size)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(iconset, name), data, 0o644); err != nil {
			return "", err
		}
	}

	out := filepath.Join(resources, "AppIcon.icns")
	cmd := exec.Command("iconutil", "-c", "icns", "-o", out, iconset)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("iconutil: %w", err)
	}
	return out, nil
}
