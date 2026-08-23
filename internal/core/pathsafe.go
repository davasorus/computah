package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// reverse returns a new slice with the elements of s in reverse order. Used
// to reassemble a not-yet-existing path suffix that was collected bottom-up.
func reverse(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[len(s)-1-i] = v
	}
	return out
}

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
		// The target doesn't exist yet (e.g. creating a new file, possibly in
		// new subdirectories). Walk up to the nearest EXISTING ancestor,
		// resolve symlinks there, then re-append the not-yet-created suffix.
		// Collecting every missing component (not just the immediate parent)
		// handles creating a file several directories deep in one shot.
		missing := []string{} // missing components, collected deepest-first
		cur := candidate
		for {
			parent := filepath.Dir(cur)
			if parent == cur {
				canonTarget = candidate // hit root without an existing ancestor
				break
			}
			missing = append(missing, filepath.Base(cur)) // record this level
			if canonParent, perr := filepath.EvalSymlinks(parent); perr == nil {
				// parent exists: target = canonParent + missing (reversed to
				// top-down order).
				parts := append([]string{canonParent}, reverse(missing)...)
				canonTarget = filepath.Join(parts...)
				break
			} else if !os.IsNotExist(perr) {
				return "", fmt.Errorf("resolve parent symlinks: %w", perr)
			}
			cur = parent // parent also missing; keep climbing
		}
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
