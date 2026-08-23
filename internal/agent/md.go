// Streaming markdown → ANSI renderer for model output.
//
// The model's replies are markdown; printing them raw means literal **bold**
// and unhighlighted code fences all day. This renderer styles them for the
// terminal: bold/italic/inline-code spans, dim bullets, bold headers, and
// colored code-fence blocks.
//
// It's LINE-buffered by design: tokens accumulate until a newline, then the
// completed line renders with the correct fence state. Styling half a line
// correctly is impossible (a ` or ** may not have arrived yet), and at local
// generation speeds line-by-line output reads naturally. Flush() emits any
// final partial line (rendered best-effort) at end of turn.
//
// When stdout isn't a terminal (NO_COLOR, pipes), tokens pass through
// untouched — byte-exact markdown for scripts.
package agent

import (
	"fmt"
	"strings"
)

type mdWriter struct {
	buf      strings.Builder
	inFence  bool
	pending  string   // a held-back line awaiting lookahead (table detection)
	tableBuf []string // accumulating table lines when inTable
	inTable  bool
}

func newMDWriter() *mdWriter { return &mdWriter{} }

// Write consumes a streamed token, printing every completed line. Both color
// and no-color modes route completed lines through handleLine so tables are
// detected and rendered (box-drawing with color, aligned plain without).
func (m *mdWriter) Write(tok string) {
	m.buf.WriteString(tok)
	for {
		s := m.buf.String()
		i := strings.IndexByte(s, '\n')
		if i == -1 {
			return
		}
		line := s[:i]
		m.handleLine(line)
		m.buf.Reset()
		m.buf.WriteString(s[i+1:])
	}
}

// handleLine processes one completed line with one-line lookahead so tables
// (which need header+separator+rows together) can be detected and buffered
// while everything else streams immediately. The `pending` field holds a
// candidate table-header line until the next line reveals whether it's a
// table (separator follows) or ordinary text.
func (m *mdWriter) handleLine(line string) {
	// Inside a fence, tables don't apply — stream directly.
	if m.inFence || strings.HasPrefix(strings.TrimSpace(line), "```") {
		m.flushPending()
		m.emitLine(line)
		return
	}
	if m.inTable {
		if strings.TrimSpace(line) != "" && looksLikeTableRow(line) {
			m.tableBuf = append(m.tableBuf, line)
			return
		}
		// table ended — render it, then process this line normally
		m.flushTable()
		m.handleLine(line)
		return
	}
	if m.pending != "" {
		// We held a candidate header; is THIS line a separator?
		if isTableSeparator(line) {
			m.inTable = true
			m.tableBuf = []string{m.pending, line}
			m.pending = ""
			return
		}
		// not a table — emit the held line, fall through for this one
		prev := m.pending
		m.pending = ""
		m.emitLine(prev)
	}
	// Could this line START a table? Hold it to peek at the next line.
	if looksLikeTableRow(line) && strings.TrimSpace(line) != "" {
		m.pending = line
		return
	}
	m.emitLine(line)
}

// emitLine renders and outputs a single non-table line to stdout + the bus.
// The rendered line is word-wrapped to the terminal width so long prose and
// list items don't overflow (the terminal would otherwise hard-wrap mid-word
// or scroll horizontally). The bus gets the RAW line (the dashboard/TUI wrap
// themselves). No-color mode stays byte-exact for pipes.
func (m *mdWriter) emitLine(line string) {
	if useColor {
		fmt.Println(wrapANSI(m.renderLine(line), termWidth()))
	} else {
		fmt.Println(line) // byte-exact markdown for pipes/NO_COLOR
	}
	emitAssistant(line)
}

// flushPending emits any held candidate line as ordinary text.
func (m *mdWriter) flushPending() {
	if m.pending != "" {
		m.emitLine(m.pending)
		m.pending = ""
	}
}

// flushTable parses the buffered table lines and emits a rendered table.
func (m *mdWriter) flushTable() {
	if len(m.tableBuf) < 2 {
		for _, l := range m.tableBuf {
			m.emitLine(l)
		}
		m.tableBuf, m.inTable = nil, false
		return
	}
	t, _ := parseTable(m.tableBuf, 0)
	var rendered string
	if useColor {
		rendered = renderTableANSI(t, renderInline, stripMarkdown, termWidth())
	} else {
		rendered = renderTablePlain(t, stripMarkdown, termWidth())
	}
	fmt.Println(rendered)
	// The dashboard gets the RAW markdown lines (it renders its own HTML).
	for _, l := range m.tableBuf {
		emitAssistant(l)
	}
	m.tableBuf, m.inTable = nil, false
}

// Flush drains any held state at end of turn: an in-progress table, a
// pending candidate line, and any partial (newline-less) buffered text.
func (m *mdWriter) Flush() {
	// Complete any buffered whole line first (rare: stream ended mid-line).
	if s := m.buf.String(); s != "" {
		// Treat a trailing partial line as a completed line for rendering.
		m.handleLine(s)
		m.buf.Reset()
	}
	if m.inTable {
		m.flushTable()
	}
	m.flushPending()
	m.inFence = false
}

func (m *mdWriter) renderLine(line string) string {
	trimmed := strings.TrimSpace(line)
	// Fence delimiters toggle state and render as a dim rule with the
	// language tag, visually bracketing the block.
	if strings.HasPrefix(trimmed, "```") {
		lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		m.inFence = !m.inFence
		rule := "──────────"
		if m.inFence && lang != "" {
			return tint(cDim, "┌─ "+lang+" "+rule)
		}
		if m.inFence {
			return tint(cDim, "┌─"+rule)
		}
		return tint(cDim, "└─"+rule)
	}
	if m.inFence {
		// Code: no inline styling (backticks/asterisks are CODE here),
		// cyan body with a dim gutter.
		return tint(cDim, "│ ") + tint(cCyan, line)
	}
	// Headers: whole line bold, hashes dimmed away.
	if strings.HasPrefix(trimmed, "#") {
		text := strings.TrimLeft(trimmed, "#")
		return "\033[1m" + strings.TrimSpace(text) + "\033[0m"
	}
	// Bullets: - / * become a proper bullet glyph (indent preserved).
	if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "- "); ok {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		return indent + tint(cDim, "•") + " " + renderInline(rest)
	}
	if rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "* "); ok {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		return indent + tint(cDim, "•") + " " + renderInline(rest)
	}
	return renderInline(line)
}

// renderInline styles `code`, **bold**, and *italic* spans. Inline code is
// handled first by splitting on backticks so its content is protected from
// bold/italic processing — asterisks inside `code` are code.
// renderMarkdownBlock renders a COMPLETE markdown string (not streaming) to
// ANSI (color) or aligned plain text. Used where the full text is already in
// hand — the TUI viewport re-renders the whole assistant block each update,
// and this keeps its tables/headings/emphasis identical to the CLI's
// streaming renderer. Both go through the same mdtable + renderLine code.
func renderMarkdownBlock(src string, color bool, maxWidth int) string {
	lines := strings.Split(src, "\n")
	var out []string
	w := &mdWriter{}
	i := 0
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if color {
				out = append(out, w.renderLine(line))
			} else {
				out = append(out, line)
			}
			i++
			continue
		}
		if !w.inFence && looksLikeTableRow(line) && strings.TrimSpace(line) != "" &&
			i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			tbl, next := parseTable(lines, i)
			if color {
				out = append(out, renderTableANSI(tbl, renderInline, stripMarkdown, maxWidth))
			} else {
				out = append(out, renderTablePlain(tbl, stripMarkdown, maxWidth))
			}
			i = next
			continue
		}
		if color {
			out = append(out, w.renderLine(line))
		} else {
			out = append(out, line)
		}
		i++
	}
	return strings.Join(out, "\n")
}

// stripMarkdown removes inline markdown markers so display width can be
// measured accurately (a **bold** cell is 4 chars wider than it looks).
func stripMarkdown(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	s = strings.ReplaceAll(s, "~~", "")
	s = strings.ReplaceAll(s, "`", "")
	// single-asterisk emphasis
	s = strings.ReplaceAll(s, "*", "")
	return s
}

func renderInline(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		// Unbalanced backticks — don't guess, style only emphasis.
		return renderEmphasis(s)
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("\033[36m" + p + "\033[0m") // inline code: cyan
		} else {
			b.WriteString(renderEmphasis(p))
		}
	}
	return b.String()
}

func renderEmphasis(s string) string {
	s = replacePaired(s, "**", "\033[1m", "\033[22m") // bold
	s = replacePaired(s, "*", "\033[3m", "\033[23m")  // italic
	return s
}

// replacePaired swaps balanced marker pairs for on/off ANSI codes, leaving
// unbalanced markers untouched (a lone * is probably multiplication).
func replacePaired(s, marker, on, off string) string {
	if strings.Count(s, marker)%2 != 0 {
		return s
	}
	var b strings.Builder
	open := false
	for {
		i := strings.Index(s, marker)
		if i == -1 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		if open {
			b.WriteString(off)
		} else {
			b.WriteString(on)
		}
		open = !open
		s = s[i+len(marker):]
	}
}
