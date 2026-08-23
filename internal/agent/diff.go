// A real diff engine — first-class change display.
//
// The previous "diff preview" printed all old lines then all new lines: a
// six-line struct edit showed twelve lines of which eleven were identical
// noise, and the actual String→string change was a spot-the-difference
// puzzle. This produces proper unified-style hunks: unchanged context dim,
// removals red, additions green, and — for modified line pairs — the
// changed SPAN inside the line highlighted, so a one-token edit reads at a
// glance.
//
// Used by: edit_file and write_file previews (terminal AND the tool result,
// so the model sees exactly what its edit did), and the /diff command
// (session changes per file, current content vs the pre-session .bak).
//
// Algorithm: trim common prefix/suffix lines, LCS-align the middle when it's
// small enough (the overwhelmingly common case for tool edits — a single
// contiguous region), degrade to one replace-hunk when it isn't. O(n) for
// the trim, bounded DP for the middle, no dependencies.
package agent

import (
	"fmt"
	"strings"
)

type diffLine struct {
	kind byte // ' ' context, '-' removed, '+' added
	text string
}

type hunk struct {
	oldStart, newStart int // 1-based first line shown, per side
	lines              []diffLine
}

const diffContext = 2 // unchanged lines shown around changes

// diffLines computes hunks between two texts. Returns nil when identical.
func diffLines(oldText, newText string) []hunk {
	if oldText == newText {
		return nil
	}
	a := strings.Split(oldText, "\n")
	b := strings.Split(newText, "\n")

	// Trim common prefix and suffix.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	am, bm := a[pre:len(a)-suf], b[pre:len(b)-suf]

	var ops []diffLine
	// LCS alignment when the middle is small (tool edits change one small
	// region, so after trimming this is almost always tiny). Guard the DP
	// cost; past the bound a plain replace-hunk is still correct, just
	// coarser.
	if len(am)*len(bm) <= 250_000 {
		ops = lcsOps(am, bm)
	} else {
		for _, l := range am {
			ops = append(ops, diffLine{'-', l})
		}
		for _, l := range bm {
			ops = append(ops, diffLine{'+', l})
		}
	}

	// Split ops into hunks wherever a run of unchanged lines exceeds
	// 2*context, attaching context from the surrounding equal regions.
	var hunks []hunk
	oldLn, newLn := pre, pre // 0-based position after the prefix
	i := 0
	for i < len(ops) {
		// Skip equal runs between changes.
		for i < len(ops) && ops[i].kind == ' ' {
			oldLn++
			newLn++
			i++
		}
		if i == len(ops) {
			break
		}
		// Start a hunk: leading context from before this change.
		ctxStart := diffContext
		h := hunk{}
		var lines []diffLine
		// Pull context from the ops stream (already-skipped equals) or the
		// common prefix.
		lead := min(ctxStart, oldLn)
		h.oldStart = oldLn - lead + 1
		h.newStart = newLn - lead + 1
		for k := lead; k > 0; k-- {
			lines = append(lines, diffLine{' ', lineAt(a, oldLn-k)})
		}
		// Consume changes and small equal gaps.
		for i < len(ops) {
			if ops[i].kind == ' ' {
				// Count the equal run; if it's short, keep it inline.
				j := i
				for j < len(ops) && ops[j].kind == ' ' {
					j++
				}
				if j < len(ops) && j-i <= 2*diffContext {
					for ; i < j; i++ {
						lines = append(lines, ops[i])
						oldLn++
						newLn++
					}
					continue
				}
				// Long gap (or trailing equals): close with trailing context.
				trail := min(diffContext, j-i)
				for k := 0; k < trail; k++ {
					lines = append(lines, ops[i+k])
				}
				oldLn += j - i
				newLn += j - i
				i = j
				break
			}
			switch ops[i].kind {
			case '-':
				oldLn++
			case '+':
				newLn++
			}
			lines = append(lines, ops[i])
			i++
		}
		// Trailing context from the common suffix when the change ran to
		// the end of the middle.
		if i == len(ops) {
			for k := 0; k < diffContext && oldLn+k < len(a) && newLn+k < len(b); k++ {
				lines = append(lines, diffLine{' ', a[oldLn+k]})
			}
		}
		h.lines = lines
		hunks = append(hunks, h)
	}
	return hunks
}

func lineAt(s []string, i int) string {
	if i >= 0 && i < len(s) {
		return s[i]
	}
	return ""
}

// lcsOps aligns two line slices via LCS, emitting ' '/'-'/'+' ops.
func lcsOps(a, b []string) []diffLine {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffLine{' ', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffLine{'-', a[i]})
			i++
		default:
			ops = append(ops, diffLine{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffLine{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffLine{'+', b[j]})
	}
	return ops
}

// renderDiff renders hunks for the terminal (ANSI when color is on) or as
// plain unified text (pipes, and the copy embedded in tool results so the
// model sees what changed). maxChanged caps displayed +/- lines; the
// remainder is summarized.
func renderDiff(hunks []hunk, color bool, maxChanged int) string {
	if len(hunks) == 0 {
		return ""
	}
	var b strings.Builder
	changed := 0
	for _, h := range hunks {
		oldN, newN := 0, 0
		for _, l := range h.lines {
			switch l.kind {
			case ' ':
				oldN++
				newN++
			case '-':
				oldN++
			case '+':
				newN++
			}
		}
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.oldStart, oldN, h.newStart, newN)
		if color {
			b.WriteString(tint(cDim, "    "+header) + "\n")
		} else {
			b.WriteString(header + "\n")
		}
		for idx := 0; idx < len(h.lines); idx++ {
			l := h.lines[idx]
			if l.kind != ' ' {
				if changed >= maxChanged {
					b.WriteString(overflowNote(hunks, changed, color))
					return b.String()
				}
				changed++
			}
			// Char-level highlight for a 1:1 modified pair.
			if color && l.kind == '-' && idx+1 < len(h.lines) && h.lines[idx+1].kind == '+' &&
				(idx+2 >= len(h.lines) || h.lines[idx+2].kind != '+') &&
				(idx == 0 || h.lines[idx-1].kind != '-') {
				del, add := l.text, h.lines[idx+1].text
				b.WriteString(tint(cRed, "    - "+highlightSpan(del, add)) + "\n")
				if changed < maxChanged {
					changed++
					b.WriteString(tint(cGreen, "    + "+highlightSpan(add, del)) + "\n")
					idx++
					continue
				}
			}
			switch {
			case !color:
				b.WriteString(string(l.kind) + " " + l.text + "\n")
			case l.kind == '-':
				b.WriteString(tint(cRed, "    - "+l.text) + "\n")
			case l.kind == '+':
				b.WriteString(tint(cGreen, "    + "+l.text) + "\n")
			default:
				b.WriteString(tint(cDim, "      "+l.text) + "\n")
			}
		}
	}
	return b.String()
}

func overflowNote(hunks []hunk, shown int, color bool) string {
	total := 0
	for _, h := range hunks {
		for _, l := range h.lines {
			if l.kind != ' ' {
				total++
			}
		}
	}
	note := fmt.Sprintf("    … (+%d more changed lines — /diff <path> shows everything)", total-shown)
	if color {
		return tint(cDim, note) + "\n"
	}
	return note + "\n"
}

// highlightSpan reverses-video the part of line that differs from other,
// by common prefix/suffix — exactly right for the single-token edits that
// dominate agent work (String→string reads instantly).
func highlightSpan(line, other string) string {
	if len(line) > 300 || len(other) > 300 {
		return line
	}
	p := 0
	for p < len(line) && p < len(other) && line[p] == other[p] {
		p++
	}
	s := 0
	for s < len(line)-p && s < len(other)-p && line[len(line)-1-s] == other[len(other)-1-s] {
		s++
	}
	if p+s >= len(line) {
		return line // pure insertion on the other side; nothing to mark here
	}
	return line[:p] + "\033[7m" + line[p:len(line)-s] + "\033[27m" + line[len(line)-s:]
}
