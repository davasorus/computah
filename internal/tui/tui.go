// TUI — a full-screen terminal interface (Charm bubbletea/bubbles/lipgloss).
//
// Step 3 of the front-end plan. Like the dashboard, the TUI is an event-bus
// subscriber; unlike the dashboard, it also OWNS the terminal, so when -tui
// is active the stdout subscriber is silenced (agent.SilenceStdout(true)) to
// avoid fighting the render loop.
//
// Architecture note: bubbletea runs its own event loop and owns stdin, which
// is incompatible with the blocking readInput()/runTurn() REPL loop. So the
// TUI is a SEPARATE MODE selected by -tui: runTUI replaces the REPL loop
// entirely. A submitted prompt runs runTurn in a goroutine; bus events and
// turn completion flow back into the model as tea.Msgs via Program.Send.
//
// Known gaps (deliberate, to keep this shippable — see the plan):
//   - Input is a plain bubbles/textinput: no Tab-completion, history, !shell,
//     @file, or """ multi-line yet. The REPL keeps those; the TUI gets them
//     in a follow-up. Core loop (type prompt → watch panes stream) works.
//   - Slash commands aren't routed through the TUI yet (they run in the REPL).
package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
	"github.com/davasorus/computah/internal/md"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- messages piped from the bus / turn runner into the tea loop ---

type busMsg core.Event    // a bus event forwarded into the model
type turnDoneMsg struct{} // the current turn finished
type turnErrMsg struct{ err string }
type tickMsg struct{} // periodic rail refresh (todos/stats)
type browserPromptMsg struct{ text string }

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// tuiModel is the bubbletea model.
type tuiModel struct {
	vp        viewport.Model
	input     textinput.Model
	width     int
	height    int
	lines     []string // rendered conversation lines (the transcript)
	todos     []agent.TodoItem
	thinking  string
	statusReq int
	busy      bool // a turn is running; input disabled

	// submit callback: hands a prompt to the turn runner. Set by runTUI.
	submit func(string)

	// rich input state
	hist      *tuiHistory
	mlBuf     []string // multi-line accumulation (active when mlOn)
	mlOn      bool
	onShell   func(string) // run a !shell command
	onFile    func(string) // attach an @file
	onCommand func(string) // handle a /command
}

// styles
var (
	stUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	stTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	stErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	stStatus = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	stDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stBar    = lipgloss.NewStyle().Foreground(lipgloss.Color("7")).Background(lipgloss.Color("0"))
	stRail   = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	stRailHd = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)
)

func newTUIModel(submit func(string)) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "message the agent…"
	ti.Focus()
	ti.Prompt = "❯ "
	vp := viewport.New(80, 20)
	return tuiModel{input: ti, vp: vp, submit: submit, hist: newTUIHistory()}
}

func (m tuiModel) Init() tea.Cmd { return tea.Batch(textinput.Blink, tickCmd()) }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		railW := 30
		if m.width < 80 {
			railW = 0 // hide rail on narrow terminals
		}
		m.vp.Width = m.width - railW - 1
		// Reserve exactly 3 lines: top bar + input + status.
		bodyH := m.height - 3
		if bodyH < 1 {
			bodyH = 1
		}
		m.vp.Height = bodyH
		m.input.Width = m.width - 4
		m.reflow()
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "ctrl+d":
			return m, tea.Quit
		case "up":
			m.input.SetValue(m.hist.up(m.input.Value()))
			m.input.CursorEnd()
			return m, nil
		case "down":
			m.input.SetValue(m.hist.down())
			m.input.CursorEnd()
			return m, nil
		case "tab":
			line, pos, ok := tuiComplete(m.input.Value(), m.input.Position())
			if ok {
				m.input.SetValue(line)
				m.input.SetCursor(pos)
			}
			return m, nil
		case "enter":
			if m.busy {
				break
			}
			raw := m.input.Value()
			m.hist.resetRecall()

			// Multi-line: a """ line toggles the buffer.
			if isMultilineToggle(raw) {
				if m.mlOn {
					// close: submit the accumulated block as a prompt
					text := strings.Join(m.mlBuf, "\n")
					m.mlBuf, m.mlOn = nil, false
					m.input.Reset()
					m.input.Placeholder = "message the agent…"
					if strings.TrimSpace(text) != "" {
						m.startPrompt(text)
					}
				} else {
					m.mlOn = true
					m.input.Reset()
					m.input.Placeholder = `multi-line — """ again to send`
				}
				return m, nil
			}
			if m.mlOn {
				m.mlBuf = append(m.mlBuf, raw)
				m.input.Reset()
				return m, nil
			}

			kind, arg := agent.ClassifyInput(raw)
			m.input.Reset()
			switch kind {
			case agent.InputBlank:
				// nothing
			case agent.InputShell:
				m.hist.add(raw)
				if m.onShell != nil {
					m.appendLine(stDim.Render("  ! " + arg))
					go m.onShell(arg)
				}
			case agent.InputFile:
				m.hist.add(raw)
				if m.onFile != nil {
					m.appendLine(stDim.Render("  @ " + arg))
					go m.onFile(arg)
				}
			case agent.InputCommand:
				m.hist.add(raw)
				if m.onCommand != nil {
					m.appendLine(stStatus.Render(arg))
					go m.onCommand(arg)
				}
			case agent.InputPrompt:
				m.hist.add(raw)
				m.startPrompt(arg)
			}
			return m, nil
		default:
			m.hist.resetRecall() // any other edit ends recall
		}
	case busMsg:
		m.applyEvent(core.Event(msg))
	case turnDoneMsg:
		m.busy = false
		m.thinking = ""
	case turnErrMsg:
		m.busy = false
		m.thinking = ""
		m.appendLine(stErr.Render("error: " + msg.err))
	case tickMsg:
		// Pull current todos and stats into the model for the rail.
		m.todos = append(m.todos[:0], agent.Todos()...)
		m.statusReq = agent.Stats().Requests
		return m, tickCmd()
	case browserPromptMsg:
		if !m.busy && strings.TrimSpace(msg.text) != "" {
			m.appendLine(stDim.Render("  (from dashboard)"))
			m.startPrompt(msg.text)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	// Only forward explicit scroll keys to the viewport. Feeding it every
	// key (as before) let the viewport treat space/arrows/etc. as scroll
	// commands WHILE TYPING, jerking the transcript around. Typing keys go
	// to the input only; the viewport sees just paging/scroll keys.
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "pgup", "pgdown", "home", "end":
			m.vp, cmd = m.vp.Update(msg)
			cmds = append(cmds, cmd)
		}
	} else {
		// Non-key messages (resize, mouse) still go to the viewport.
		m.vp, cmd = m.vp.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// applyEvent turns a bus event into transcript lines / state.
func (m *tuiModel) applyEvent(e core.Event) {
	switch e.Kind {
	case core.EvAssistant:
		// Coalesce consecutive assistant lines into the tail.
		if n := len(m.lines); n > 0 && strings.HasPrefix(m.lines[n-1], "\x00asst") {
			m.lines[n-1] += "\n" + e.Text
		} else {
			m.lines = append(m.lines, "\x00asst"+e.Text)
		}
		m.reflow()
	case core.EvToolCall:
		suffix := ""
		if e.Meta != nil && e.Meta["inline"] == "1" {
			suffix = " [inline]"
		}
		m.appendLine(stTool.Render("  ⚙ " + e.Tool + "(" + e.Text + ")" + suffix))
	case core.EvToolDone:
		if e.Text != "" {
			m.appendLine(stDim.Render("    " + e.Text))
		}
	case core.EvError:
		m.appendLine(stErr.Render(e.Text))
	case core.EvStatus:
		m.appendLine(stStatus.Render(e.Text))
	case core.EvLine:
		if e.Meta != nil && e.Meta["raw"] == "1" {
			// Pre-formatted diff — mark so reflow prints it verbatim (no
			// markdown, no wrap; it's already width-constrained).
			m.appendLine("\x00raw" + e.Text)
		} else if e.Color != "" {
			m.appendLine(tuiColor(e.Color, e.Text))
		} else {
			m.appendLine(e.Text)
		}
	case core.EvThinking:
		m.thinking = e.Text
		// header-only; transcript unchanged, no reflow (avoids flashing from
		// high-frequency thinking-token updates)
	case core.EvStats:
		// rail-only; nothing in the transcript changed
	}
}

// tuiColor styles a line with the same color hint the terminal uses, mapped
// to lipgloss. Keeps core.EvLine color hints consistent between REPL and TUI.
func tuiColor(code, text string) string {
	switch code {
	case core.ColorRed:
		return stErr.Render(text)
	case core.ColorGreen:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render(text)
	case core.ColorYellow:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(text)
	case core.ColorCyan:
		return stTool.Render(text)
	case core.ColorDim:
		return stDim.Render(text)
	default:
		return text
	}
}

func (m *tuiModel) appendLine(s string) { m.lines = append(m.lines, s); m.reflow() }

// startPrompt echoes the user's message and launches the turn.
func (m *tuiModel) startPrompt(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	m.appendLine(stUser.Render("❯ " + text))
	m.busy = true
	m.thinking = "thinking…"
	go m.submit(text)
}

// reflow rebuilds the viewport content and pins to bottom.
func (m *tuiModel) reflow() {
	w := m.vp.Width
	if w < 10 {
		w = 10
	}
	var b strings.Builder
	for _, l := range m.lines {
		var block string
		if body, ok := strings.CutPrefix(l, "\x00asst"); ok {
			// Tables are already constrained to width by renderMarkdownBlock;
			// other assistant text needs soft word-wrapping (the bubbletea
			// viewport clips rather than wraps long lines).
			block = md.RenderMarkdownBlock(body, core.UseColor, w)
		} else if body, ok := strings.CutPrefix(l, "\x00raw"); ok {
			// Pre-formatted (diff) — verbatim, no wrap, no markdown.
			b.WriteString(body)
			b.WriteByte('\n')
			continue
		} else {
			block = l
		}
		for _, line := range strings.Split(block, "\n") {
			b.WriteString(wrapLine(line, w))
			b.WriteByte('\n')
		}
	}
	// Auto-scroll to follow new output ONLY if the user was already at the
	// bottom. If they scrolled up to read, don't yank them back — that's the
	// "text jumping" during streaming.
	wasAtBottom := m.vp.AtBottom()
	m.vp.SetContent(strings.TrimRight(b.String(), "\n"))
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

// wrapLine soft-wraps a single line to width w, EXCEPT table/box-drawing
// lines (which are already width-constrained and would be corrupted by
// re-wrapping). ANSI escape codes are preserved and don't count toward width.
func wrapLine(line string, w int) string {
	// Leave box-drawing (table) lines untouched — they're pre-fit.
	if strings.ContainsAny(line, "│┌┐└┘├┤┬┴┼─") {
		return line
	}
	return md.WrapANSI(line, w) // shared wrapper (indent-preserving, ANSI-aware)
}

func (m tuiModel) View() string {
	if m.width == 0 {
		return "starting…"
	}
	railW := 30
	if m.width < 80 {
		railW = 0
	}

	// top bar
	th := m.thinking
	if th == "" && m.busy {
		th = "working…"
	}
	bar := stBar.Width(m.width).Render(" agent · tui   " + th)

	// main + rail
	main := m.vp.View()
	var body string
	if railW > 0 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, main, m.renderRail(railW))
	} else {
		body = main
	}

	// input + status
	in := m.input.View()
	status := stDim.Render(" enter to send · ctrl+c to quit")

	return lipgloss.JoinVertical(lipgloss.Left, bar, body, in, status)
}

func (m tuiModel) renderRail(w int) string {
	var b strings.Builder
	b.WriteString(stRailHd.Render("TODOS") + "\n")
	if len(m.todos) == 0 {
		b.WriteString(stDim.Render("none") + "\n")
	} else {
		for _, t := range m.todos {
			box := "[ ]"
			if t.Done {
				box = "[x]"
			}
			line := box + " " + t.Text
			if len(line) > w-1 {
				line = line[:w-1]
			}
			b.WriteString(stRail.Render(line) + "\n")
		}
	}
	return lipgloss.NewStyle().Width(w).Height(m.vp.Height).
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(lipgloss.Color("8")).PaddingLeft(1).Render(b.String())
}

// runTUI is the -tui entry point: replaces the REPL loop. It wires the bus
// into the tea program and runs turns in a goroutine.
func RunTUI(baseURL, model string, sb *agent.Sandbox, st *agent.SessionStore, messages []core.Message) {
	// bubbletea needs a real interactive terminal. Under a pipe, redirected
	// stdin, or some WSL/PowerShell invocations, stdin isn't a TTY and
	// bubbletea gets immediate EOF and quits cleanly — which looks like the
	// TUI "flashing and exiting." Detect that up front and explain, rather
	// than silently dropping back to the shell.
	if !agent.UseTTY() {
		fmt.Println("tui: no interactive terminal detected (stdin is not a TTY).")
		fmt.Println("     bubbletea needs a real terminal; that's why the TUI flashed and exited.")
		fmt.Println("     Options:")
		fmt.Println("       • run WITHOUT -tui for the plain REPL (works over pipes)")
		fmt.Println("       • use -headless and drive entirely from the web dashboard")
		return
	}
	agent.SilenceStdout(true) // the TUI owns the screen

	var prog *tea.Program
	// forward every bus event into the tea loop
	unsub := core.Bus.Subscribe(core.SubscriberFunc(func(e core.Event) {
		if prog != nil {
			prog.Send(busMsg(e))
		}
	}))
	defer unsub()

	submit := func(text string) {
		messages = append(messages, core.Message{Role: "user", Content: text})
		defer func() {
			if r := recover(); r != nil {
				prog.Send(turnErrMsg{err: "panic in turn"})
			}
		}()
		messages = agent.RunTurn(baseURL, model, sb, st, messages)
		st.Append(messages)
		prog.Send(turnDoneMsg{})
	}

	m := newTUIModel(submit)
	m.onShell = func(cmd string) {
		out, code, err := agent.ExecShell(cmd, sb.Root, true)
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		core.EmitLine(agent.Tail(out, 4096))
		messages = append(messages, core.Message{
			Role:    "user",
			Content: "[shell] $ " + cmd + " (exit " + strconv.Itoa(code) + detail + ")\n" + agent.Tail(out, 8192),
		})
		st.Append(messages)
	}
	m.onFile = func(path string) {
		abs, err := core.ConfinePath(sb.Root, path)
		if err != nil {
			core.EmitError("  @ " + err.Error())
			return
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			core.EmitError("  @ cannot read " + path + ": " + err.Error())
			return
		}
		messages = append(messages, core.Message{
			Role:    "user",
			Content: "[attached " + path + "]\n" + string(data),
		})
		st.Append(messages)
		core.EmitLine("  @ attached " + path + " (" + strconv.Itoa(len(data)) + " bytes)")
	}
	m.onCommand = func(cmd string) {
		if out, handled := agent.RunInfoCommand(cmd, baseURL, model, messages, st); handled {
			core.EmitLine(strings.TrimRight(out, "\n"))
			return
		}
		core.EmitStatus("  " + cmd + " — stateful commands (/plan, /commit, /reload, …) run in the REPL for now")
	}
	// periodic rail refresh (todos/stats) via a ticker command
	prog = tea.NewProgram(m, tea.WithAltScreen())

	// Forward browser (dashboard) submissions into the tea loop. Without
	// this, prompts submitted from -serve-write land in the queue and never
	// process while the TUI is running (the TUI doesn't call the REPL's
	// drainBrowserSubmission). This goroutine bridges the queue → tea loop.
	stopWatch := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopWatch:
				return
			case text := <-core.BrowserSubmissions:
				if prog != nil {
					prog.Send(browserPromptMsg{text: text})
				}
			}
		}
	}()
	defer close(stopWatch)
	if _, err := prog.Run(); err != nil {
		agent.SilenceStdout(false)
		core.EmitError("tui: " + err.Error())
	}
	agent.SilenceStdout(false)
}
