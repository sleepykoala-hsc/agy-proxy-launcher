//go:build !windows

package install

import (
	"os"
	"path/filepath"
	"strings"
)

// ensureOnPath only reports whether dir is already reachable; the shell rc is
// never edited behind the user's back, main prints the export line instead.
func ensureOnPath(dir string) (present, changed bool, err error) {
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true, false, nil
		}
	}
	return false, false, nil
}
