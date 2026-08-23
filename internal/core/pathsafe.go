package core

import (
	"fmt"
	"os"
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
	canonBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("resolve base symlinks: %w", err)
	}

	var candidate string
	if filepath.IsAbs(p) {
		candidate = filepath.Clean(p)
	} else {
		candidate = filepath.Clean(filepath.Join(canonBase, p))
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve target: %w", err)
	}

	canonTarget, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve target symlinks: %w", err)
		}
		parent := filepath.Dir(candidate)
		canonParent, perr := filepath.EvalSymlinks(parent)
		if perr != nil {
			return "", fmt.Errorf("resolve parent symlinks: %w", perr)
		}
		canonTarget = filepath.Join(canonParent, filepath.Base(candidate))
	}

	rel, err := filepath.Rel(canonBase, canonTarget)
	if err != nil {
		return "", fmt.Errorf("rel path check failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes the allowed directory", p)
	}
	return canonTarget, nil
}
