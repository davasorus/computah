// Shared markdown table support — one parser, rendered per surface.
//
// Tables can't render line-by-line (they need header + separator + rows
// together), which is why the streaming line renderer skipped them. This
// module provides the buffering + parsing once, and renders to ANSI
// box-drawing (CLI) or plain aligned text (TUI/no-color). The dashboard
// renders its own HTML from the same markdown, but the DETECTION logic here
// (isTableHeader/isTableSeparator) is the shared contract so all three
// surfaces agree on what counts as a table.
package md

import (
	"strings"

	"github.com/davasorus/computah/internal/core"
)

// isTableSeparator reports whether a line is a GFM table separator row —
// pipes and dashes with optional alignment colons, e.g. "| :--- | ---: |".
func isTableSeparator(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "-") || !strings.Contains(t, "|") {
		return false
	}
	for _, r := range t {
		switch r {
		case '|', '-', ':', ' ', '\t':
		default:
			return false
		}
	}
	return true
}

// looksLikeTableRow reports whether a line could be a table row (contains a
// pipe that isn't just inline code). A table starts when a row line is
// followed by a separator line.
func looksLikeTableRow(line string) bool {
	return strings.Contains(line, "|")
}

// tableCells splits a table row into trimmed cell values, tolerating optional
// leading/trailing pipes.
func tableCells(row string) []string {
	r := strings.TrimSpace(row)
	r = strings.TrimPrefix(r, "|")
	r = strings.TrimSuffix(r, "|")
	parts := strings.Split(r, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// mdTable is a parsed table: a header row and body rows, already split into
// cells with inline markdown still present (rendered per-surface).
type mdTable struct {
	header []string
	rows   [][]string
}

// parseTable consumes lines[start:] as a table (caller has verified
// lines[start] is a row and lines[start+1] is a separator). Returns the
// parsed table and the index just past the table's last row.
func parseTable(lines []string, start int) (mdTable, int) {
	t := mdTable{header: tableCells(lines[start])}
	i := start + 2 // skip header + separator
	for i < len(lines) && strings.TrimSpace(lines[i]) != "" && looksLikeTableRow(lines[i]) {
		t.rows = append(t.rows, tableCells(lines[i]))
		i++
	}
	return t, i
}

// colWidths computes the display width of each column (max over header and
// all rows), after inline markdown is stripped for measurement.
func (t mdTable) colWidths(strip func(string) string) []int {
	n := len(t.header)
	for _, r := range t.rows {
		if len(r) > n {
			n = len(r)
		}
	}
	w := make([]int, n)
	measure := func(cells []string) {
		for c := 0; c < n; c++ {
			var s string
			if c < len(cells) {
				s = strip(cells[c])
			}
			if l := displayWidth(s); l > w[c] {
				w[c] = l
			}
		}
	}
	measure(t.header)
	for _, r := range t.rows {
		measure(r)
	}
	return w
}

// displayWidth is a rune count (good enough for the ASCII/Latin content the
// agent emits; not full grapheme-aware but avoids a dependency).
func displayWidth(s string) int {
	return len([]rune(s))
}

// WrapANSI word-wraps s to a maximum visible width, treating ANSI escape
// sequences as zero-width so colored text wraps at the right column. Wrapping
// happens at spaces where possible; an over-long single word is hard-broken.
// Box-drawing (table) lines are returned unchanged — they're pre-fit. Leading
// indentation is preserved on continuation lines so wrapped list items and
// prose stay visually aligned.
func WrapANSI(s string, max int) string {
	if max <= 0 {
		return s
	}
	if strings.ContainsAny(s, "│┌┐└┘├┤┬┴┼") {
		return s // pre-fit table line
	}
	if visibleLen(s) <= max {
		return s
	}
	// Preserve leading whitespace as the continuation indent.
	indent := ""
	for _, r := range s {
		if r == ' ' || r == '\t' {
			indent += string(r)
		} else {
			break
		}
	}
	if visibleLen(indent) >= max { // pathological; give up wrapping
		return s
	}

	var out strings.Builder
	col := 0
	firstLineOfPara := true
	emitNewline := func() {
		out.WriteByte('\n')
		out.WriteString(indent)
		col = visibleLen(indent)
		firstLineOfPara = false
	}

	// Tokenize into words while carrying ANSI codes with their following text.
	words := splitWordsANSI(s)
	for _, wd := range words {
		wl := visibleLen(wd.text)
		if col+wl > max && col > visibleLen(indent) {
			emitNewline()
			// skip a leading space that caused the break
			if wd.text == " " {
				continue
			}
		}
		if wl > max { // a single word longer than the line — hard break it
			for _, r := range wd.text {
				if col >= max {
					emitNewline()
				}
				out.WriteRune(r)
				col++
			}
			continue
		}
		out.WriteString(wd.ansi + wd.text)
		col += wl
		_ = firstLineOfPara
	}
	return out.String()
}

// wordTok is a word plus any ANSI codes that immediately preceded it.
type wordTok struct {
	ansi string // escape sequences to emit before the text
	text string // visible text (may be a single space between words)
}

// splitWordsANSI breaks s into words and spaces, attaching any ANSI escape
// sequences to the token that follows them so styling survives wrapping.
func splitWordsANSI(s string) []wordTok {
	var toks []wordTok
	var ansi strings.Builder
	var cur strings.Builder
	inEsc := false
	flush := func() {
		if cur.Len() > 0 || ansi.Len() > 0 {
			toks = append(toks, wordTok{ansi: ansi.String(), text: cur.String()})
			ansi.Reset()
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\033' {
			inEsc = true
			ansi.WriteRune(r)
			continue
		}
		if inEsc {
			ansi.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == ' ' {
			flush()
			toks = append(toks, wordTok{text: " "})
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return toks
}

// visibleLen counts runes that aren't part of an ANSI escape sequence.
func visibleLen(s string) int {
	n, inEsc := 0, false
	for _, r := range s {
		if r == '\033' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		n++
	}
	return n
}

// renderTableANSI renders a table with box-drawing borders for the terminal.
// inlineFn styles cell contents; stripFn removes styling for width
// measurement. maxWidth caps the total rendered width (0 = no cap); columns
// are shrunk proportionally and cells truncated with … when they'd overflow,
// so the table can't spill past a terminal/viewport edge (which corrupts the
// borders and cascades indentation).
func renderTableANSI(t mdTable, inlineFn, stripFn func(string) string, maxWidth int) string {
	w := t.colWidths(stripFn)
	w = fitColumns(w, maxWidth)
	n := len(w)
	// truncate a stripped value to a column width with an ellipsis
	fit := func(raw string, width int) (shown, styled string) {
		s := stripFn(raw)
		if displayWidth(s) <= width {
			return s, styleWithin(raw, inlineFn)
		}
		// truncate on the stripped text; drop styling when we cut (keeping
		// balanced ANSI across a cut is error-prone — plain is safe).
		r := []rune(s)
		if width <= 1 {
			return string(r[:width]), string(r[:width])
		}
		cut := string(r[:width-1]) + "…"
		return cut, cut
	}
	pad := func(cells []string, header bool) string {
		var b strings.Builder
		b.WriteString(core.Tint(core.ColorDim, "│"))
		for c := 0; c < n; c++ {
			var raw string
			if c < len(cells) {
				raw = cells[c]
			}
			shown, styled := fit(raw, w[c])
			if header {
				styled = "\033[1m" + styled + "\033[0m"
			}
			gap := w[c] - displayWidth(shown)
			if gap < 0 {
				gap = 0
			}
			b.WriteString(" " + styled + strings.Repeat(" ", gap) + " ")
			b.WriteString(core.Tint(core.ColorDim, "│"))
		}
		return b.String()
	}
	rule := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for c := 0; c < n; c++ {
			b.WriteString(strings.Repeat("─", w[c]+2))
			if c < n-1 {
				b.WriteString(mid)
			}
		}
		b.WriteString(right)
		return core.Tint(core.ColorDim, b.String())
	}
	var b strings.Builder
	b.WriteString(rule("┌", "┬", "┐") + "\n")
	b.WriteString(pad(t.header, true) + "\n")
	b.WriteString(rule("├", "┼", "┤") + "\n")
	for _, r := range t.rows {
		b.WriteString(pad(r, false) + "\n")
	}
	b.WriteString(rule("└", "┴", "┘"))
	return b.String()
}

// styleWithin applies inline styling only when the value fits (helper for the
// non-truncated path).
func styleWithin(raw string, inlineFn func(string) string) string {
	return inlineFn(raw)
}

// fitColumns shrinks column widths so the total rendered table width (borders
// + padding included) fits within maxWidth. Border overhead is 3 chars per
// column (│ + two spaces) plus a trailing │. Shrinks the widest columns
// first. maxWidth<=0 means no constraint.
func fitColumns(w []int, maxWidth int) []int {
	if maxWidth <= 0 {
		return w
	}
	overhead := 3*len(w) + 1
	avail := maxWidth - overhead
	if avail < len(w) {
		avail = len(w) // at least 1 char per column
	}
	total := 0
	for _, x := range w {
		total += x
	}
	if total <= avail {
		return w
	}
	// Shrink the widest column repeatedly until we fit.
	out := make([]int, len(w))
	copy(out, w)
	for total > avail {
		max, mi := 0, 0
		for i, x := range out {
			if x > max {
				max, mi = x, i
			}
		}
		if max <= 1 {
			break
		}
		out[mi]--
		total--
	}
	return out
}

// renderTablePlain renders a table as aligned columns without color/box
// characters — for no-color mode.
func renderTablePlain(t mdTable, stripFn func(string) string, maxWidth int) string {
	w := t.colWidths(stripFn)
	if maxWidth > 0 {
		// plain layout overhead: 2 spaces between columns
		overhead := 2 * (len(w) - 1)
		w = fitColumns(w, maxWidth-overhead+3*len(w)+1) // reuse fitColumns' model loosely
	}
	n := len(w)
	trunc := func(s string, width int) string {
		if displayWidth(s) <= width {
			return s
		}
		r := []rune(s)
		if width <= 1 {
			return string(r[:width])
		}
		return string(r[:width-1]) + "…"
	}
	row := func(cells []string) string {
		var parts []string
		for c := 0; c < n; c++ {
			var s string
			if c < len(cells) {
				s = trunc(stripFn(cells[c]), w[c])
			}
			gap := w[c] - displayWidth(s)
			if gap < 0 {
				gap = 0
			}
			parts = append(parts, s+strings.Repeat(" ", gap))
		}
		return strings.Join(parts, "  ")
	}
	var b strings.Builder
	b.WriteString(row(t.header) + "\n")
	seps := make([]string, n)
	for c := range seps {
		seps[c] = strings.Repeat("-", w[c])
	}
	b.WriteString(strings.Join(seps, "  ") + "\n")
	for _, r := range t.rows {
		b.WriteString(row(r) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
