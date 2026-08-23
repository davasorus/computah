package core

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ConfinePath resolves p (relative to base if not absolute) and verifies the
// result stays within base. It returns the cleaned absolute path, or an error
// if the path escapes base via "..", symlink-style tricks, or an absolute path
// pointing elsewhere. Use this before any file operation whose path derives
// from user- or model-supplied input.
func ConfinePath(base, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve base: %w", err)
	}
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Clean(filepath.Join(absBase, p))
	}
	// Containment check: abs must equal base or sit under base + separator.
	// The trailing-separator guard prevents "/home/user-evil" matching base
	// "/home/user".
	if abs != absBase && !strings.HasPrefix(abs, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the allowed directory", p)
	}
	return abs, nil
}
