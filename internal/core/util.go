package core

import "path/filepath"

// Dedup returns ss with duplicate strings removed, preserving first-seen order.
func Dedup(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// RelPaths converts absolute paths to paths relative to root (best-effort).
func RelPaths(root string, abs []string) []string {
	out := make([]string, len(abs))
	for i, p := range abs {
		r, _ := filepath.Rel(root, p)
		out[i] = r
	}
	return out
}
