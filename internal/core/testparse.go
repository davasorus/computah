package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	reTestFail    = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	reAssertLine  = regexp.MustCompile(`^\s*([\w./-]+\.go):(\d+):`)
	rePanicLine   = regexp.MustCompile(`^panic:`)
	reBuildFailGo = regexp.MustCompile(`^(# .+|.+\.go:\d+:\d+:)`)
)

// parseTestFailures distills `go test` (or build) output to the actionable
// parts: which tests failed and the file:line + message of each assertion.
// Returns "" when nothing failed (caller keeps the raw output for context).
func ParseTestFailures(out string) string {
	lines := strings.Split(out, "\n")
	var b strings.Builder
	failedTests := []string{}
	assertions := []string{}
	inFail := false
	for i, ln := range lines {
		if m := reTestFail.FindStringSubmatch(ln); m != nil {
			failedTests = append(failedTests, m[1])
			inFail = true
			continue
		}
		if strings.HasPrefix(ln, "=== RUN") || strings.HasPrefix(ln, "--- PASS") || strings.HasPrefix(ln, "ok ") {
			inFail = false
		}
		if inFail {
			if m := reAssertLine.FindStringSubmatch(ln); m != nil {
				msg := strings.TrimSpace(ln)
				// Pull a continuation line only if it's not itself a new
				// assertion, a test-status marker, or a summary line.
				if i+1 < len(lines) {
					nxt := strings.TrimSpace(lines[i+1])
					isNoise := nxt == "" || nxt == "FAIL" || nxt == "PASS" ||
						strings.HasPrefix(nxt, "---") || strings.HasPrefix(nxt, "===") ||
						strings.HasPrefix(nxt, "FAIL\t") || strings.HasPrefix(nxt, "ok ") ||
						reAssertLine.MatchString(lines[i+1])
					if !isNoise {
						msg += " " + nxt
					}
				}
				assertions = append(assertions, msg)
			}
		}
		if rePanicLine.MatchString(ln) {
			assertions = append(assertions, strings.TrimSpace(ln))
		}
	}
	// Build errors (no test framing): surface the compiler lines.
	if len(failedTests) == 0 {
		var buildErrs []string
		for _, ln := range lines {
			if reBuildFailGo.MatchString(ln) {
				buildErrs = append(buildErrs, strings.TrimSpace(ln))
			}
		}
		if len(buildErrs) == 0 {
			return ""
		}
		if len(buildErrs) > 12 {
			buildErrs = buildErrs[:12]
		}
		return "Build errors:\n" + strings.Join(buildErrs, "\n")
	}
	sort.Strings(failedTests)
	fmt.Fprintf(&b, "%d test(s) failed: %s\n", len(failedTests), strings.Join(Dedup(failedTests), ", "))
	if len(assertions) > 0 {
		if len(assertions) > 15 {
			assertions = assertions[:15]
		}
		b.WriteString("Failing assertions:\n")
		for _, a := range assertions {
			b.WriteString("  " + a + "\n")
		}
	}
	return b.String()
}
