package agent

import (
	"bufio"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// ---------- Input ownership ----------
//
// TWO input paths, exactly one active per process:
//
//  1. TTY (interactive use): a shared x/term line editor owns stdin. It
//     provides real line editing — Up/Down history, Left/Right cursor
//     movement — and bracketed paste, so a multi-line paste arrives as ONE
//     message. Raw mode is entered only WHILE a prompt is active and
//     restored immediately after, so Ctrl+C during a model turn still
//     raises SIGINT and interrupts the request as before.
//
//  2. Non-TTY (pipes, tests): one goroutine scans stdin into the lines
//     channel, exactly as before.
//
// Everything that needs input — the REPL prompt, multi-line blocks,
// run_command/fetch confirmations — goes through askLine/readInput, which
// route to whichever path is active. Nothing else touches stdin.

var lines = make(chan string, 8)

// ttyIn/ttyOut are swappable so pty tests can drive the REAL terminal code
// path; forceTTY lets those tests opt in even though the test process's
// stdin isn't a terminal.
var (
	// term.IsTerminal is a real isatty check — a plain char-device test is
	// fooled by /dev/null (which is exactly what `go test` wires to stdin).
	stdinTTY = term.IsTerminal(int(os.Stdin.Fd()))
	forceTTY bool
	ttyIn    *os.File  = os.Stdin
	ttyOut   io.Writer = os.Stdout

	uiMu   sync.Mutex
	uiTerm *term.Terminal // shared instance: its buffer and HISTORY must persist across reads
)

func useTTY() bool { return forceTTY || stdinTTY }

// termWidth returns the terminal's column count, or a conservative default
// when it can't be determined. Tries several fds (stdout/stderr/stdin) and
// the COLUMNS env var, because under WSL/PowerShell GetSize on stdout can
// fail even when the session is a real terminal. A too-large value causes
// tables and wrapped text to overflow and cascade, so the fallback is
// deliberately conservative.

// initInput starts whichever input path this process needs.
func initInput() {
	if useTTY() {
		return // the line editor reads on demand; no background goroutine
	}
	startStdinReader()
}

func startStdinReader() {
	go func() {
		for {
			sc := bufio.NewScanner(os.Stdin)
			sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
			for sc.Scan() {
				lines <- sc.Text()
			}
			// A too-long line makes Scan return false with ErrTooLong — the
			// old code treated ANY stop as EOF and silently exited the REPL,
			// killing the session over one oversized paste. Warn, restart the
			// scanner, and keep reading. (The oversized line's remainder may
			// arrive as garbage fragments — the model copes.)
			if err := sc.Err(); err != nil {
				fmt.Printf("\n(input error: %v — line dropped, still listening)\n> ", err)
				continue
			}
			close(lines) // real Ctrl+D / EOF
			return
		}
	}()
}

type termRW struct {
	io.Reader
	io.Writer
}

// keyWatchReader sits between the raw terminal and the line editor, fixing
// two byte-level realities the editor can't handle:
//
//  1. Ctrl+C (0x03): x/term treats it as io.EOF AND leaves its internal
//     line buffer inconsistent — a stray Ctrl+C would quit the agent (or
//     corrupt the editor). It's rewritten to Ctrl+U + Enter (kill line,
//     submit empty) and flagged, so ttyReadLine can reprompt cleanly.
//
//  2. SS3 cursor keys (ESC O A/B/C/D/H/F): terminals in application
//     cursor-key mode — common under Windows Terminal + zsh — send arrows
//     as ESC O A, but x/term only parses the CSI form ESC [ A. It swallows
//     "ESC O" as unknown and inserts a literal "A" into the line. SS3 is
//     translated to CSI here so history and cursor movement work in both
//     modes.
//
// Sequences can split across reads (ESC in one read, "OA" in the next), so
// a partial trailing ESC / ESC O is held back until the next read completes
// it.
type keyWatchReader struct {
	r        io.Reader
	sawCtrlC bool
	out      []byte // translated bytes ready to deliver
	hold     []byte // possible partial escape sequence awaiting more bytes
}

func (k *keyWatchReader) Read(p []byte) (int, error) {
	for len(k.out) == 0 {
		buf := make([]byte, 512)
		n, err := k.r.Read(buf)
		if n > 0 {
			k.ingest(buf[:n])
		}
		if err != nil {
			// Stream ending: whatever was held back is all there will be.
			k.out = append(k.out, k.hold...)
			k.hold = nil
			if len(k.out) > 0 {
				break
			}
			return 0, err
		}
	}
	n := copy(p, k.out)
	k.out = k.out[n:]
	return n, nil
}

func (k *keyWatchReader) ingest(b []byte) {
	b = append(k.hold, b...)
	k.hold = nil
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0x03 {
			k.sawCtrlC = true
			k.out = append(k.out, 0x15, '\r') // kill the line, submit empty
			continue
		}
		if c == 0x1b {
			if i+1 >= len(b) {
				k.hold = []byte{0x1b} // sequence may continue in next read
				return
			}
			if b[i+1] == 'O' {
				if i+2 >= len(b) {
					k.hold = []byte{0x1b, 'O'}
					return
				}
				k.out = append(k.out, 0x1b, '[', b[i+2]) // SS3 → CSI
				i += 2
				continue
			}
		}
		k.out = append(k.out, c)
	}
}

var uiWatch *keyWatchReader

// ---------- Persistent history ----------
//
// x/term's Terminal keeps history in memory; fileHistory implements its
// History interface backed by ~/.agent/history, so up-arrow recalls lines
// from previous sessions too. Only the newest maxHistory entries are kept.

const maxHistory = 500

type fileHistory struct {
	entries []string // oldest first
	path    string
}

func newFileHistory() *fileHistory {
	h := &fileHistory{}
	home, err := os.UserHomeDir()
	if err != nil {
		return h
	}
	h.path = filepath.Join(home, ".agent", "history")
	if data, err := os.ReadFile(h.path); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" {
				h.entries = append(h.entries, l)
			}
		}
		if len(h.entries) > maxHistory {
			h.entries = h.entries[len(h.entries)-maxHistory:]
		}
	}
	return h
}

// Add appends an entry (skipping blanks, immediate duplicates, and y/N
// confirmation answers — the editor feeds every submitted line here,
// including approval prompts) and persists best-effort. Multi-line entries
// are stored space-joined — the history file is line-oriented.
func (h *fileHistory) Add(entry string) {
	entry = strings.ReplaceAll(strings.TrimSpace(entry), "\n", " ")
	switch strings.ToLower(entry) {
	case "", "y", "n", "a", "yes", "no", "always":
		return
	}
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == entry {
		return
	}
	h.entries = append(h.entries, entry)
	if len(h.entries) > maxHistory {
		h.entries = h.entries[len(h.entries)-maxHistory:]
	}
	if h.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(h.path, []byte(strings.Join(h.entries, "\n")+"\n"), 0o644)
}

func (h *fileHistory) Len() int { return len(h.entries) }

// At returns the idx-th most recent entry (0 = newest), per x/term's contract.
func (h *fileHistory) At(idx int) string {
	if idx < 0 || idx >= len(h.entries) {
		return ""
	}
	return h.entries[len(h.entries)-1-idx]
}

// ---------- Tab completion ----------

// replCommands is completed at the start of a line; kept here next to the
// completer so adding a command means updating one list.
var replCommands = []string{
	"/help", "/init", "/plan", "/verify", "/commit", "/rewind", "/stats", "/budget",
	"/compact", "/context", "/diff", "/undo", "/resume", "/models", "/effort", "/reload", "/fork", "/tree", "/ctx", "/sessions", "/tools",
	"/model", "/allow", "/copy", "/todos", "/agents",
}

// completerRoot is the directory path completion resolves against (the
// working directory; set in main).
var completerRoot = "."

// completeLine implements Tab: at line start, complete /commands; otherwise
// complete the last whitespace-separated token as a path. Single match →
// fill it in (dirs get a trailing /); several → extend to the longest
// common prefix; none → leave the line alone.
func completeLine(line string, pos int, key rune) (string, int, bool) {
	if key != '\t' {
		return "", 0, false
	}
	head, tail := line[:pos], line[pos:]
	start := strings.LastIndexAny(head, " \t") + 1
	word := head[start:]
	if word == "" {
		return "", 0, false
	}
	var cands []string
	if strings.HasPrefix(word, "/") && start == 0 {
		for _, c := range replCommands {
			if strings.HasPrefix(c, word) {
				cands = append(cands, c)
			}
		}
	} else {
		pat := word
		if !filepath.IsAbs(pat) {
			pat = filepath.Join(completerRoot, pat)
		}
		matches, _ := filepath.Glob(pat + "*")
		for _, m := range matches {
			c := m
			if !filepath.IsAbs(word) {
				if rel, err := filepath.Rel(completerRoot, m); err == nil {
					c = rel
				}
			}
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				c += string(filepath.Separator)
			}
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return "", 0, false
	}
	repl := cands[0]
	if len(cands) > 1 {
		repl = commonPrefix(cands)
		if repl == word {
			return "", 0, false // nothing to extend
		}
	}
	newHead := head[:start] + repl
	return newHead + tail, len(newHead), true
}

func commonPrefix(ss []string) string {
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// ttyReadLine reads one line through the shared line editor: raw mode on,
// prompt, read, raw mode off. Bracketed paste is enabled for the read; the
// editor submits each pasted line separately (flagged ErrPasteIndicator),
// so a multi-line paste is assembled here into ONE message. A paste ending
// in a newline leaves the editor waiting — press Enter to send, which also
// gives you a moment to see what was pasted. Ctrl+C clears and reprompts;
// Ctrl+D returns ok=false (quit).
//
// Known edge: typing text and pasting on the SAME line before Enter makes
// the editor treat the first line as typed — the message splits. Paste at a
// fresh prompt.
func ttyReadLine(prompt string) (string, bool) {
	uiMu.Lock()
	defer uiMu.Unlock()
	fd := int(ttyIn.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		// Not actually a terminal after all — degrade to the channel path.
		fmt.Print(prompt)
		l, ok := <-lines
		return strings.TrimSpace(l), ok
	}
	defer func() { _ = term.Restore(fd, oldState) }()
	if uiTerm == nil {
		uiWatch = &keyWatchReader{r: ttyIn}
		uiTerm = term.NewTerminal(termRW{uiWatch, ttyOut}, "")
		uiTerm.History = newFileHistory()          // up-arrow reaches previous sessions
		uiTerm.AutoCompleteCallback = completeLine // Tab: /commands and paths
	}
	uiTerm.SetPrompt(prompt)
	uiTerm.SetBracketedPasteMode(true)
	defer uiTerm.SetBracketedPasteMode(false)
	// Ask the terminal for NORMAL cursor keys (ESC[A) rather than
	// application mode (ESC O A) — shells often leave application mode set.
	// The watcher also translates SS3→CSI for terminals that ignore this.
	_, _ = fmt.Fprint(ttyOut, "\x1b[?1l")

	uiWatch.sawCtrlC = false
	line, err := uiTerm.ReadLine()
	if err == term.ErrPasteIndicator {
		// Multi-line paste: keep reading while lines are flagged; the final
		// unflagged read (the fragment after the paste, or the Enter that
		// sends it) completes the message.
		var b strings.Builder
		b.WriteString(line)
		for {
			l2, err2 := uiTerm.ReadLine()
			b.WriteString("\n")
			b.WriteString(l2)
			if err2 == term.ErrPasteIndicator {
				continue
			}
			break // unflagged final line, or EOF mid-paste
		}
		return strings.TrimSpace(b.String()), true
	}
	if err != nil {
		return "", false // io.EOF: Ctrl+D
	}
	if uiWatch.sawCtrlC {
		lastCtrlC = true
		return "", true // Ctrl+C: line was killed, submit empty — reprompt
	}
	lastCtrlC = false
	return strings.TrimSpace(line), true
}

// lastCtrlC is set by ttyReadLine when the returned (empty) line was the
// result of Ctrl+C rather than a bare Enter. readInput uses it for the
// double-press-to-quit convention.
var lastCtrlC bool

// askLine is the single entry point for mid-turn confirmations (run_command,
// fetch_url): print/echo the prompt, read one answer line on whichever input
// path is active.
func askLine(prompt string) (string, bool) {
	if useTTY() {
		return ttyReadLine(prompt)
	}
	fmt.Print(prompt)
	l, ok := <-lines
	return strings.TrimSpace(l), ok
}

// ---------- Terminal polish (colors + spinner) ----------

// useColor: ANSI styling only when stdout is a real terminal and the user
// hasn't opted out via NO_COLOR.

// A little personality while the model works, Claude Code style.
var thinkingLabels = []string{
	"Thinking", "Pondering", "Exploring", "Percolating", "Noodling",
	"Brewing", "Scheming", "Ruminating", "Cogitating", "Marinating",
}

// spinner renders an animated status line ("⠹ Pondering… (12s)") that erases
// itself the moment real output starts. No-op when output isn't a terminal.
type spinner struct {
	once  sync.Once
	stop  chan struct{}
	done  chan struct{}
	mu    sync.Mutex
	label string
}

// SetLabel updates the spinner's status text in place — used to surface
// otherwise-silent activity, like a model generating a long tool call.
func (s *spinner) SetLabel(l string) {
	s.mu.Lock()
	s.label = l
	s.mu.Unlock()
}

// Each style cycles IN ORDER; the order creates the motion. A style is
// picked at random per turn for variety — the frames within it are never
// shuffled. Single-width glyphs only, so the label never shifts columns.
var spinnerStyles = [][]rune{
	[]rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"), // braille rotate
	[]rune("⣾⣽⣻⢿⡿⣟⣯⣷"),   // braille pulse
	[]rune("◐◓◑◒"),       // quarter circle
	[]rune("▖▘▝▗"),       // corner hop
	[]rune("┤┘┴└├┌┬┐"),   // box spin
	[]rune("⢹⢺⢼⣸⣇⡧⡗⡏"),   // braille orbit
	[]rune(".oO@*"),      // ascii pop
}

func startSpinner(label string) *spinner {
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{}), label: label}
	if !useColor {
		close(s.done)
		return s
	}
	go func() {
		frames := spinnerStyles[rand.Intn(len(spinnerStyles))]
		start := time.Now()
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-s.stop:
				emitClear("spinner") // erase the status line
				close(s.done)
				return
			case <-t.C:
				s.mu.Lock()
				l := s.label
				s.mu.Unlock()
				emitOverwrite("spinner",
					fmt.Sprintf("%c %s… (%ds)", frames[i%len(frames)], l, int(time.Since(start).Seconds())),
					cDim)
				i++
			}
		}
	}()
	return s
}

// Stop is idempotent and safe to call from the token callback and again
// after the request completes.
func (s *spinner) Stop() {
	s.once.Do(func() {
		if !useColor {
			return
		}
		close(s.stop)
		<-s.done
	})
}
func preview(s string) string {
	if len(s) > 160 {
		return s[:160] + fmt.Sprintf("... (%d chars)", len(s))
	}
	return s
}

// ---------- Bracketed paste ----------
//
// On the TTY path the line editor manages paste mode itself. These marker
// constants serve the CHANNEL path: input piped through something that
// preserved the markers still assembles into one message.

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// readInput reads one user message. TTY path: the line editor handles
// history, editing, and bracketed paste (multi-line pastes arrive as one
// message). Channel path (pipes): a single line, a marker-wrapped paste, or
// a `"""` block. In both, the `"""` opener is lenient — content on the same
// line becomes the block's first line.
func readInput() (string, bool) {
	if useTTY() {
		var ctrlCAt time.Time
		for {
			line, ok := ttyReadLine("> ")
			if !ok {
				return "", false
			}
			if lastCtrlC {
				// Double Ctrl+C within 2s = quit; single = clear + hint.
				if time.Since(ctrlCAt) < 2*time.Second {
					return "", false
				}
				ctrlCAt = time.Now()
				fmt.Println(tint(cDim, "(Ctrl+C again to quit — or Ctrl+D)"))
				continue
			}
			if line == "" {
				return "", true
			}
			if !strings.HasPrefix(line, `"""`) {
				return line, true
			}
			var b strings.Builder
			if len(line) > 3 {
				b.WriteString(strings.TrimSpace(line[3:]) + "\n")
			}
			fmt.Println(`(multi-line mode — end with """ on its own line; tip: pasting works directly at the > prompt)`)
			for {
				l, lok := ttyReadLine("… ")
				if !lok || l == `"""` {
					break
				}
				b.WriteString(l + "\n")
			}
			return strings.TrimSpace(b.String()), true
		}
	}

	fmt.Print("> ")
	line, ok := <-lines
	if !ok {
		return "", false
	}
	// Bracketed paste: collect until the end marker, however many lines.
	if i := strings.Index(line, pasteStart); i != -1 {
		var b strings.Builder
		b.WriteString(line[:i]) // anything typed before the paste
		rest := line[i+len(pasteStart):]
		for {
			if j := strings.Index(rest, pasteEnd); j != -1 {
				b.WriteString(rest[:j])
				b.WriteString(rest[j+len(pasteEnd):]) // anything typed after
				return strings.TrimSpace(b.String()), true
			}
			b.WriteString(rest + "\n")
			l, lok := <-lines
			if !lok {
				return strings.TrimSpace(b.String()), true
			}
			rest = l
		}
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, `"""`) {
		return trimmed, true
	}
	var b strings.Builder
	if len(trimmed) > 3 {
		// Lenient opener: `""" some text` starts the block WITH that text.
		b.WriteString(strings.TrimSpace(trimmed[3:]) + "\n")
	}
	fmt.Println(`(multi-line mode — end with """ on its own line)`)
	for l := range lines {
		if strings.TrimSpace(l) == `"""` {
			return strings.TrimSpace(b.String()), true
		}
		b.WriteString(l + "\n")
	}
	return strings.TrimSpace(b.String()), true // EOF also ends the block
}
