// A tool-calling coding agent in pure Go (stdlib only), speaking the
// OpenAI-compatible chat completions API. Works with LM Studio, llama.cpp's
// llama-server, vLLM, Ollama's /v1 endpoint, or any other OpenAI-compatible
// server.
//
// v7 design philosophy — built for a capable model (Gemma 4 12B class):
//   - Trust the model: minimal system prompt (identity, workdir, hard rules),
//     no sampling overrides, no iteration cap, no call-pattern babysitting.
//   - Guards are tripwires, not walls: backups, empty-write rejection, the
//     repeat-call loop breaker, and y/N gating on mutating commands protect
//     against consequences without distorting what the model attempts.
//   - Close the loop against reality: a configured verify command runs after
//     any turn that modified files, and failures are fed back automatically.
//     Hooks enforce user invariants (gofmt, lint gates) without model turns.
//   - Repo-level safety: a git checkpoint before every turn, /rewind to any
//     of them, /commit with a model-written Conventional Commits message.
//   - Plan mode (/plan): read-only exploration → numbered plan → your
//     approval → execution. Cheaper to review a plan than flailed edits.
//   - Context is managed, not just accumulated: stale reads are pruned and
//     old tool outputs elided — in rare big batches, because history edits
//     invalidate the server's KV-cache prefix (see session.go).
//   - Streaming: tokens print as they generate — no dead air.
//
// Layout:
//
//	main.go     flags, config, system prompt, REPL
//	loop.go     the agent loop (runTurn), verify loop, spawn_task
//	tools.go    tool registry + handlers, command approval
//	session.go  session persistence, /compact, context management
//	api.go      OpenAI-compatible types, streaming chat, SSE parsing
//	mcp.go      MCP client (stdio transport) — external tool servers
//	eval.go     one-shot mode (-p) and the eval harness (-eval)
//	term.go     stdin ownership, colors, spinner, input reading
//
// Usage:
//
//	go run . [flags] [working-dir]
//	go run . -url http://172.22.208.1:1235 -model google/gemma-4-12b ~/myproject
//	go run . -p "fix the failing test" -yes ~/myproject     (one-shot)
//	go run . -eval evals.json -runs 3 ~/myproject           (benchmark)
//
// Flags:
//
//	-url     base URL of the server (default: auto-detect Windows host in WSL2)
//	-model   model id (default: first non-embedding model from GET /v1/models)
//	-resume  resume a session: 'latest' or a name from /sessions
//	-p       run one prompt non-interactively and exit
//	-yes     auto-approve mutating commands and fetches (for -p / scripts)
//	-eval    run an eval file (JSON array of {name,prompt,check}) and report
//	-runs    runs per eval case (default 1)
//
// ~/.agent/config.json (all optional): url, model, compact_tokens,
// command_timeout_sec, verify_command, mcp_servers.
//
// Input: single lines as usual; type """ alone to start a multi-line block
// (paste code freely), and """ alone again to send it.
//
// Trace legend:
//
//	⚙  tool call requested by the model
//	✏  file written or edited (absolute path shown)
//	✗  write/edit rejected by a guard (nothing touched disk)
//	$  command executed (auto-approved) or awaiting your y/N
//	⧉  subtask running in a fresh context
//	✓  verify command passed
package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/core"
)

// ---------- Runtime state ----------
//
// Set once in main (curModel also by /model). Package-level because tool
// handlers (spawn_task) and the verify loop need the connection details but
// the Tool handler signature is deliberately just (Sandbox, args).
var (
	curBaseURL    string
	curModel      string
	verifyCommand string // from config: runs after any turn that modified files
	assumeYes     bool   // -yes: auto-approve mutating commands (non-interactive modes)
)

// ---------- AGENTS.md (per-repo agent instructions) ----------

// loadAgentsMD walks from the working directory up to the git root (or the
// filesystem root), collecting AGENTS.md files. Outermost first, so a file
// nearer the working directory refines the repo-wide one. Returned content
// is appended to the system prompt; the binary's hard rules stay supreme.
func loadAgentsMD(workdir string) string {
	var chunks []string
	dir := workdir
	for {
		p := filepath.Join(dir, "AGENTS.md")
		if data, err := os.ReadFile(p); err == nil {
			chunks = append([]string{fmt.Sprintf("--- %s ---\n%s", p, strings.TrimSpace(string(data)))}, chunks...)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break // repo root reached
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return strings.Join(chunks, "\n\n")
}

// buildSystemPrompt assembles the full system prompt for a given working
// directory: identity + hard rules, verify-command note, AGENTS.md content,
// and current git state. Used by main, spawn_task, and the eval harness so
// every context — top-level or sub — starts from the same ground truth.
func buildSystemPrompt(root string) string {
	// Minimal core: identity, location, hard rules. Everything else — when
	// to search vs read, how to batch calls, when to verify — is the model's
	// judgment. The tool schemas carry their own usage guidance.
	s := fmt.Sprintf(
		"You are a coding agent running in a CLI on the user's machine. "+
			"Working directory: %s (relative paths resolve there; absolute paths are allowed). "+
			"Hard rules: never git push, force-push, or create/merge PRs unless the user explicitly asks; "+
			"never write empty or placeholder file content. "+
			"Otherwise, use your judgment — verify changes when it makes sense (the user approves mutating commands), "+
			"and keep responses concise. "+
			"Deliberate briefly: once you have identified a workable approach, act on it with a tool call instead of "+
			"re-examining alternatives — a wrong edit is cheap to see and fix, while extended analysis burns your generation budget. "+
			"Stay on the current task: do not expand scope mid-turn; note adjacent problems at the end instead of fixing them now. "+
			"When the user asks to open the project or a file in VS Code, use run_command with 'code .' "+
			"(the working directory) or 'code <path>'; these run without needing approval. "+
			"Git commit messages must always follow the Conventional Commits standard: "+
			"type(scope): description — types: feat, fix, docs, style, refactor, perf, test, build, ci, chore. "+
			"Use imperative mood, keep the subject under 72 characters, add a body for non-trivial changes, "+
			"and mark breaking changes with ! after the type/scope (e.g. feat(api)!: ...).", root)
	if len(preferredMCP) > 0 {
		s += "\n\nA knowledge/notes server is connected as \"" + strings.Join(preferredMCP, ", ") +
			"\": for anything involving the user's notes, knowledge base, decisions, or documentation — recording OR retrieving — " +
			"prefer its tools over writing files. Search it before assuming something isn't recorded; record durable decisions there when finishing significant work."
	}
	s += "\n\nHarness facts (your runtime, not the project): every file you write or edit is backed up once per session " +
		"(<file>.bak) and the user can /undo or /diff against it; a git checkpoint is taken before each of your turns and the user can /rewind; " +
		"a verify command may run automatically after turns that modify files, feeding failures back to you; edit results include a diff — read it. " +
		"Slash commands (/plan, /commit, /rewind, …) are USER commands: you cannot invoke them; never claim to have run one. " +
		"Repeating an identical read-only call is refused — reuse earlier results instead."
	if core.VaultPath != "" {
		s += "\n\nThe user's Obsidian knowledge vault is available: vault_search finds notes by content, " +
			"vault_read fetches one by name (follow its [[wikilinks]] when relevant), and vault_note records " +
			"durable decisions and runbooks into the vault's agent/ folder. Consult it when the user references " +
			"their notes or past decisions; offer to record significant conclusions."
	}
	if len(preferredMCP) > 0 {
		s += "\n\nA knowledge/notes server is connected as \"" + strings.Join(preferredMCP, ", ") +
			"\": for anything involving the user's notes, knowledge base, decisions, or documentation — recording OR retrieving — " +
			"prefer its tools over writing files. Search it before assuming something isn't recorded; record durable decisions there when finishing significant work."
	}
	s += "\n\nHarness facts (your runtime, not the project): every file you write or edit is backed up once per session " +
		"(<file>.bak) and the user can /undo or /diff against it; a git checkpoint is taken before each of your turns and the user can /rewind; " +
		"a verify command may run automatically after turns that modify files, feeding failures back to you; edit results include a diff — read it. " +
		"Slash commands (/plan, /commit, /rewind, …) are USER commands: you cannot invoke them; never claim to have run one. " +
		"Repeating an identical read-only call is refused — reuse earlier results instead."
	if core.VaultPath != "" {
		s += "\n\nThe user's Obsidian knowledge vault is available: vault_search finds notes by content, " +
			"vault_read fetches one by name (follow its [[wikilinks]] when relevant), and vault_note records " +
			"durable decisions and runbooks into the vault's agent/ folder. Consult it when the user references " +
			"their notes or past decisions; offer to record significant conclusions."
	}
	if verifyCommand != "" {
		s += fmt.Sprintf(
			"\n\nAfter any turn in which you modify files, the harness automatically runs `%s` "+
				"and feeds failures back to you. Fix real problems; never weaken, skip, or delete tests to make it pass.",
			verifyCommand)
	}
	if agents := loadAgentsMD(root); agents != "" {
		s += "\n\nProject instructions from AGENTS.md (apply these within the hard rules above):\n" + agents
	}
	if gc := gitContext(root); gc != "" {
		s += "\n\n" + gc
	}
	return s
}

// initPrompt drives /init: a deep repo exploration followed by creating or
// updating AGENTS.md, with Conventional Commits and ASD-STE100 Simplified
// Technical English as mandatory content.
const initPrompt = `Deeply explore this repository to build an accurate understanding before writing anything: list the directory tree, read the key files (build/module files, entry points, configs, existing docs and AGENTS.md if present), and use search_files where helpful. Take as many tool calls as you need.

Then create or update AGENTS.md in the repository root. If it already exists, read it first and update it — keep what is still accurate. The file must contain these sections:

1. Project Overview — what this repository is and does.
2. Repository Structure — each directory and its purpose.
3. Build and Test — the exact commands that build, run, and test the code.
4. Code Conventions — the style and patterns you observed, which future changes must follow.
5. Commit Messages — state that all commits MUST follow the Conventional Commits standard: format "type(scope): description"; allowed types: feat, fix, docs, style, refactor, perf, test, build, ci, chore; imperative mood; subject 72 characters or fewer; a body for non-trivial changes; "!" after the type/scope marks a breaking change.
6. Documentation Language — state that all documentation MUST be written in ASD-STE100 Simplified Technical English.

Write AGENTS.md itself — and all documentation you ever produce in this repository — in ASD-STE100 Simplified Technical English:
- Use the active voice. Use simple verb tenses.
- Write one instruction in each sentence.
- Keep sentences short: 20 words or fewer in procedures, 25 or fewer in descriptions.
- Use one term for one thing; do not vary terminology.
- Do not use vague words or unnecessary -ing forms.
- Keep each paragraph to one topic, six sentences or fewer.

When AGENTS.md is written, give a one-paragraph summary of what you recorded.`

// ---------- WSL2 host detection ----------

// windowsHostIP returns the default-gateway IP by parsing /proc/net/route.
// In WSL2 (NAT mode) the default gateway IS the Windows host.
func windowsHostIP() (string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[1] != "00000000" { // default route
			continue
		}
		gw, err := strconv.ParseUint(fields[2], 16, 32)
		if err != nil {
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, uint32(gw))
		return ip.String(), nil
	}
	return "", fmt.Errorf("no default route found")
}

func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

// ---------- Config file ----------

// ~/.agent/config.json persists defaults so you don't pass flags every
// launch. Command-line flags always override the file. Every field optional.
type Config struct {
	URL               string                     `json:"url,omitempty"`
	Model             string                     `json:"model,omitempty"`
	CompactTokens     int                        `json:"compact_tokens,omitempty"`
	CommandTimeoutSec int                        `json:"command_timeout_sec,omitempty"`   // run_command limit (default 300)
	MaxTokens         int                        `json:"max_tokens,omitempty"`            // per-generation cap (default 8192)
	MaxTurnIters      int                        `json:"max_turn_iters,omitempty"`        // hard per-turn tool-call budget (default 40)
	Protected         []string                   `json:"protected,omitempty"`             // extra write-protected glob patterns (e.g. ".env", "secrets/*")
	Journal           bool                       `json:"journal,omitempty"`               // append an aux-model session summary to <vault>/agent/journal.md on exit
	Audit             bool                       `json:"audit,omitempty"`                 // write a structured session audit note to <vault>/agent/audit/ on exit
	ContextV2         bool                       `json:"context_v2,omitempty"`            // distilled per-turn wire context (see context.go) — experimental
	BudgetMinutes     int                        `json:"budget_minutes,omitempty"`        // warn when a session exceeds this wall-clock (0 = off)
	BudgetKTokens     int                        `json:"budget_ktokens,omitempty"`        // warn when generated+reasoning tokens exceed this many thousand (0 = off)
	VerifyCommand     string                     `json:"verify_command,omitempty"`        // e.g. "go build ./... && go test ./..."
	AuxModel          string                     `json:"aux_model,omitempty"`             // smaller model for compaction/titles/commit messages
	VaultPath         string                     `json:"vault_path,omitempty"`            // Obsidian vault root — enables vault_search/read/note
	EmbedModel        string                     `json:"embed_model,omitempty"`           // embedding model id — enables semantic vault search
	ReasoningEffort   string                     `json:"reasoning_effort,omitempty"`      // low|medium|high — thinking budget for normal turns
	PlanEffort        string                     `json:"plan_reasoning_effort,omitempty"` // thinking budget for /plan turns (deep thinking earns its time there)
	NotifySec         *int                       `json:"notify_sec,omitempty"`            // toast+bell for turns longer than this (default 10; 0 = off)
	NoCheckpoints     bool                       `json:"no_checkpoints,omitempty"`        // disable per-turn git snapshots
	Hooks             map[string]string          `json:"hooks,omitempty"`                 // post_edit ({file}), pre_command ({cmd}), post_turn
	MCPServers        map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
}

func loadConfig() Config {
	var c Config
	home, err := os.UserHomeDir()
	if err != nil {
		return c
	}
	data, err := os.ReadFile(filepath.Join(home, ".agent", "config.json"))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c) // malformed config → zero value, harmless
	return c
}

// gitContext returns a short repo-state summary for the system prompt, or ""
// if the working directory isn't a git repo. Read-only; safe at startup.
func gitContext(root string) string {
	run := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if _, ok := run("rev-parse", "--is-inside-work-tree"); !ok {
		return ""
	}
	branch, _ := run("rev-parse", "--abbrev-ref", "HEAD")
	status, _ := run("status", "--porcelain")
	var b strings.Builder
	fmt.Fprintf(&b, "Git: on branch %s", branch)
	if status == "" {
		b.WriteString(", working tree clean.")
	} else {
		n := len(strings.Split(status, "\n"))
		fmt.Fprintf(&b, ", %d file(s) with uncommitted changes.", n)
	}
	return b.String()
}

// Options carries the resolved CLI inputs from the cmd/ (Cobra) layer into the
// agent. It replaces the old flag.* globals; each field maps 1:1 to a former
// flag. PositionalRoot is the optional working-directory argument.
type Options struct {
	URL            string // -url
	Model          string // -model
	Resume         string // -resume
	Prompt         string // -p (one-shot)
	Yes            bool   // -yes
	Eval           string // -eval
	ForceEval      bool   // -force-eval
	Runs           int    // -runs
	Serve          string // -serve
	ServeWrite     bool   // -serve-write
	Tui            bool   // -tui
	Headless       bool   // -headless
	PositionalRoot string // optional workdir arg (was flag.Arg(0))

	// Presentation hooks, supplied by the cmd layer so the engine never
	// imports tui/web (which would create an import cycle). Any may be nil.
	StartDashboard func(addr string)
	RunTUI         func(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message)
	RunHeadless    func(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message)
	SetDashWrite   func(bool)
}

// Run is the agent entry point. It preserves the exact behavior of the former
// func main(): resolve config, apply globals, register tools, start MCP, then
// dispatch to eval / one-shot / TUI / headless / REPL. Returns a process exit
// code (0 = normal). The cmd/ layer populates Options and calls this.
func Run(opts Options) int {
	urlFlag := &opts.URL
	modelFlag := &opts.Model
	resumeFlag := &opts.Resume
	promptFlag := &opts.Prompt
	yesFlag := &opts.Yes
	evalFlag := &opts.Eval
	forceEvalFlag := &opts.ForceEval
	runsFlag := &opts.Runs
	serveFlag := &opts.Serve
	serveWriteFlag := &opts.ServeWrite
	tuiFlag := &opts.Tui
	headlessFlag := &opts.Headless
	cfg := loadConfig()
	bus.Subscribe(stdoutSub) // terminal renders bus events; TUI/dashboard subscribe alongside

	// -serve: read-only web dashboard as a second bus subscriber.
	if *serveFlag != "" || *serveWriteFlag || *headlessFlag {
		if opts.SetDashWrite != nil {
			opts.SetDashWrite(*serveWriteFlag || *headlessFlag)
		}
		// Only headless has NO terminal, so only headless must route
		// approvals to the browser. In -serve-write REPL/TUI the user is at
		// the terminal, so approvals stay there (routing them to the browser
		// would be surprising). Headless sets web mode in runHeadless.
		addr := *serveFlag
		if addr == "" || addr == "on" || addr == "true" {
			addr = ":7777"
		} else if addr[0] != ':' {
			addr = ":" + addr // allow "-serve 7777"
		}
		if opts.StartDashboard != nil {
			opts.StartDashboard(addr)
		}
	}
	if cfg.CompactTokens > 0 {
		autoCompactTokens = cfg.CompactTokens
	}
	if cfg.CommandTimeoutSec > 0 {
		commandTimeout = time.Duration(cfg.CommandTimeoutSec) * time.Second
	}
	if cfg.MaxTokens > 0 {
		maxGenTokens = cfg.MaxTokens
	}
	if cfg.MaxTurnIters > 0 {
		maxTurnIters = cfg.MaxTurnIters
	}
	protectedPatterns = append(protectedPatterns, cfg.Protected...)
	journalEnabled = cfg.Journal
	auditEnabled = cfg.Audit
	contextV2 = cfg.ContextV2
	budgetMinutes = cfg.BudgetMinutes
	budgetKTokens = cfg.BudgetKTokens
	verifyCommand = cfg.VerifyCommand
	assumeYes = *yesFlag
	forceEval = *forceEvalFlag
	if cfg.NotifySec != nil {
		notifySec = *cfg.NotifySec
	}
	checkpointsOff = cfg.NoCheckpoints
	hooks = cfg.Hooks

	// Resolve base URL: explicit flag > config file > WSL2 gateway > localhost.
	baseURL := strings.TrimRight(*urlFlag, "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(cfg.URL, "/")
	}
	if baseURL == "" {
		host := "localhost"
		if isWSL() {
			if ip, err := windowsHostIP(); err == nil {
				host = ip
			}
		}
		baseURL = "http://" + host + ":1234"
	}

	// Resolve model: explicit flag > config file > first chat model on server.
	model := *modelFlag
	if model == "" {
		model = cfg.Model
	}
	if model == "" {
		m, err := firstModel(baseURL)
		if err != nil {
			fmt.Printf("can't reach %s or no usable model: %v\n", baseURL, err)
			fmt.Println("Is LM Studio's server running and reachable?")
			os.Exit(1)
		}
		model = m
	}
	curBaseURL, curModel = baseURL, model

	// Working directory: first positional arg, else cwd.
	root, _ := os.Getwd()
	if opts.PositionalRoot != "" {
		root, _ = filepath.Abs(opts.PositionalRoot)
	}
	sb := &Sandbox{Root: root}
	if wd, err := os.Getwd(); err == nil {
		agentSourceDir = wd // where /reload rebuilds from
	}
	sessionStart = time.Now()           // session budget clock
	completerRoot, curRoot = root, root // completion and hooks resolve against the workdir
	loadCustomCommands(root)            // /<name> templates from .agent/commands/
	loadAgentRoles(root)                // spawn_task roles from .agent/agents/
	auxModel = cfg.AuxModel             // housekeeping model (compact/titles/commits)
	core.VaultPath = cfg.VaultPath
	core.EmbedModel = cfg.EmbedModel
	runToolRegistrations() // decision/structured/embed tools, wired via cmd

	// External MCP tool servers from config: each one's tools join the
	// registry alongside the built-ins. Failures warn and continue — the
	// agent works without them.
	snapshotBuiltinTools() // record built-in names before MCP registration
	stopMCP := startMCPServers(cfg.MCPServers)
	defer stopMCP()

	// Command tools load LAST so their shadow-check sees every built-in,
	// structured, embed, vault, MCP, and Obsidian tool already registered.
	loadCommandTools(root) // user tools from .agent/tools/*.json

	// Stdin ownership and Ctrl+C handling start before ANY mode runs: a -p
	// run without -yes still prompts y/N on mutating commands, and that
	// prompt reads from the lines channel — no reader, deadlock.
	signal.Notify(interrupt, os.Interrupt)
	initInput()

	// Non-interactive modes exit before the REPL. os.Exit skips defers, so
	// MCP children are stopped explicitly here.
	if *evalFlag != "" {
		code := runEval(root, *evalFlag, *runsFlag)
		stopMCP()
		return code
	}
	if *promptFlag != "" {
		code := runOneShot(root, sb, *promptFlag)
		stopMCP()
		return code
	}

	messages := []Message{{Role: "system", Content: buildSystemPrompt(root)}}
	if strings.Contains(messages[0].Content, "AGENTS.md ---") {
		fmt.Println("loaded AGENTS.md instructions")
	}

	st := newSessionStore(root)
	if *resumeFlag != "" {
		var path string
		var err error
		if *resumeFlag == "pick" {
			path, err = st.pickSession()
		} else {
			path, err = st.resolveSession(*resumeFlag)
		}
		if err != nil {
			fmt.Println("resume:", err)
		} else if hist, herr := loadSession(path, resumeTail); herr != nil {
			fmt.Println("resume:", herr)
		} else {
			messages = append(messages, hist...)
			fmt.Printf("resumed %d messages from %s\n", len(hist), filepath.Base(path))
			fmt.Println("(note: the first request reprocesses the resumed context — expect a slower first turn)")
		}
	}
	st.Append(messages) // new session file starts with system prompt (+ resumed tail)

	fmt.Printf("agent ready — server=%s model=%s workdir=%s (Ctrl+C interrupts a turn, Ctrl+D quits)\n", baseURL, model, root)
	fmt.Println(`trace: ⚙ = tool call | ✏ = file written/edited | ✗ = rejected | $ = command | ⧉ = subtask | """ = multi-line`)
	fmt.Println(`commands: /help /plan /init /verify /commit /rewind /stats /budget /models /effort /reload /fork /tree /ctx /compact /context /undo /resume /sessions /tools /model /allow /copy — Tab completes, ↑ recalls, ! runs shell, @file attaches`)
	if verifyCommand != "" {
		fmt.Printf("verify: `%s` runs after any turn that modifies files\n", verifyCommand)
	}

	// -tui: run the full-screen interface instead of the REPL. It's a
	// separate event loop (bubbletea owns stdin), so it replaces — not
	// augments — the input loop below.
	if *tuiFlag {
		if opts.RunTUI != nil {
			opts.RunTUI(baseURL, model, sb, st, messages)
		}
		return 0
	}

	// -headless: no terminal interaction at all. The web dashboard is the
	// only interface — the agent blocks on browser submissions and runs
	// each as a turn, streaming all output through the bus (which the
	// dashboard renders over SSE). This is the clean web-only path: no TTY
	// reader, no select race, no TUI. Ctrl+C (SIGINT) still quits.
	if *headlessFlag {
		if opts.RunHeadless != nil {
			opts.RunHeadless(baseURL, model, sb, st, messages)
		}
		return 0
	}

	for {
		fmt.Println(statusLine(model, messages, root))
		// Drain any browser (dashboard) submission first — non-blocking — so
		// a prompt sent while idle is picked up without a terminal Enter.
		// Otherwise block on terminal input as normal. (Sequential by design:
		// an earlier goroutine version raced the status-line print and
		// offset the prompt cursor. For a fully web-driven workflow with no
		// terminal, use -headless.)
		var input string
		var ok bool
		if q := core.DrainBrowserSubmission(); q != "" {
			input, ok = q, true
			fmt.Println(tint(cDim, "  (from dashboard) ") + q)
		} else {
			input, ok = readInput()
		}
		if !ok {
			break
		}
		if input == "" {
			continue
		}
		// ! shell escape: run it yourself, no model round-trip. Output goes
		// into the context (and the transcript) so the model shares your
		// ground truth on its next turn.
		if strings.HasPrefix(input, "!") {
			cmdStr := strings.TrimSpace(input[1:])
			if cmdStr == "" {
				continue
			}
			out, code, err := execShell(cmdStr, root, true)
			detail := fmt.Sprintf("exit %d", code)
			if err != nil {
				detail = err.Error()
			}
			messages = append(messages, Message{
				Role:    "user",
				Content: fmt.Sprintf("[shell] $ %s (%s)\n%s", cmdStr, detail, tail(out, 8192)),
			})
			st.Append(messages)
			fmt.Println(tint(cDim, fmt.Sprintf("  (%s — output added to context)", detail)))
			continue
		}
		// Read-only/informational commands go through the shared dispatcher
		// (same path the TUI uses). Stateful commands fall through to the
		// inline handlers below.
		if strings.HasPrefix(input, "/") {
			if out, handled := runInfoCommand(input, baseURL, model, messages, st); handled {
				fmt.Print(out)
				continue
			}
		}
		if input == "/copy" {
			handleCopy(messages)
			continue
		}
		if input == "/resume" {
			if path, err := st.pickSession(); err != nil {
				fmt.Println("resume:", err)
			} else if hist, herr := loadSession(path, resumeTail); herr != nil {
				fmt.Println("resume:", herr)
			} else {
				messages = append(messages, hist...)
				st.Append(messages)
				fmt.Printf("pulled %d messages from %s into this session\n", len(hist), filepath.Base(path))
			}
			continue
		}
		if input == "/budget" {
			printBudget()
			continue
		}
		if input == "/stats" {
			stats.print()
			continue
		}
		if input == "/effort" || strings.HasPrefix(input, "/effort ") {
			arg := strings.TrimSpace(strings.TrimPrefix(input, "/effort"))
			switch arg {
			case "":
				cur := reasoningEffort
				if cur == "" {
					cur = "(unset — server default)"
				}
				plan := planReasoningEffort
				if plan == "" {
					plan = "(same as normal)"
				}
				fmt.Printf("reasoning effort: %s · plan mode: %s — /effort low|medium|high|off\n", cur, plan)
			case "off":
				reasoningEffort = ""
				fmt.Println("reasoning_effort no longer sent — server default applies")
			case "low", "medium", "high":
				reasoningEffort = arg
				fmt.Printf("reasoning effort → %s (takes effect next request)\n", arg)
			default:
				fmt.Println("usage: /effort [low|medium|high|off]")
			}
			continue
		}
		if input == "/models" {
			listServerModels(baseURL, model)
			continue
		}
		if input == "/ctx" || strings.HasPrefix(input, "/ctx ") {
			switch strings.TrimSpace(strings.TrimPrefix(input, "/ctx")) {
			case "":
				mode := "v1 (full transcript, cache-friendly)"
				if contextV2 {
					mode = "v2 (distilled per turn — experimental)"
				}
				fmt.Println("context mode:", mode, "— /ctx v1 | /ctx v2")
			case "v2":
				contextV2 = true
				fmt.Println("context v2 ON — each turn sends a distilled context; watch /stats TTFB for the reprocess cost")
			case "v1":
				contextV2 = false
				fmt.Println("context v1 — full transcript resumes next turn")
			default:
				fmt.Println("usage: /ctx [v1|v2]")
			}
			continue
		}
		if input == "/fork" {
			name, err := st.Fork(messages)
			if err != nil {
				fmt.Println("fork:", err)
				continue
			}
			fmt.Printf("forked — now writing to %s; the original session is frozen (return to it with /resume)\n", name)
			continue
		}
		if input == "/reload" {
			handleReload(baseURL, model, root)
			continue // only reached when the rebuild failed
		}
		if input == "/todos" {
			if len(todos) == 0 {
				fmt.Println("no checklist — the model maintains one via update_todos on multi-step work")
			} else {
				renderTodos()
			}
			continue
		}
		if input == "/agents" {
			listAgentRoles()
			continue
		}
		if input == "/rewind" {
			handleRewind(root)
			continue
		}
		if input == "/commit" {
			handleCommit(root)
			continue
		}
		if strings.HasPrefix(input, "/plan") {
			task := strings.TrimSpace(strings.TrimPrefix(input, "/plan"))
			if task == "" {
				fmt.Println("usage: /plan <task> — explores read-only, proposes a plan, executes on your approval")
				continue
			}
			takeCheckpoint(root, "plan: "+task)
			turnStart := time.Now()
			modifiedBefore := len(sb.Modified)
			messages = runPlanTurn(baseURL, model, sb, st, messages, task)
			st.Append(messages)
			messages = runVerifyLoop(baseURL, model, sb, st, messages, modifiedBefore)
			notifyTurnDone(time.Since(turnStart))
			checkBudget()
			continue
		}
		if input == "/tree" {
			st.printSessionTree()
			continue
		}
		if input == "/sessions" {
			st.listSessions()
			continue
		}
		if input == "/tools" {
			fmt.Println("available tools:")
			for _, t := range registry {
				fmt.Printf("  %-18s %s\n", t.Name, firstSentence(t.Desc))
			}
			continue
		}
		if input == "/context" {
			fmt.Printf("context: ~%d tokens of a %d-token budget (%d messages)\n",
				estimateTokens(messages), autoCompactTokens, len(messages))
			continue
		}
		if input == "/diff" || strings.HasPrefix(input, "/diff ") {
			handleDiff(sb, strings.TrimSpace(strings.TrimPrefix(input, "/diff")))
			continue
		}
		if strings.HasPrefix(input, "/undo") {
			handleUndo(sb, strings.TrimSpace(strings.TrimPrefix(input, "/undo")))
			continue
		}
		if input == "/verify" {
			if verifyCommand == "" {
				fmt.Println(`no verify command configured — set "verify_command" in ~/.agent/config.json`)
				continue
			}
			out, code, err := execShell(verifyCommand, root, true)
			switch {
			case err != nil:
				fmt.Printf("verify error: %v\n%s\n", err, tail(out, 4096))
			case code == 0:
				fmt.Println(tint(cGreen, "  ✓ verify passed"))
			default:
				fmt.Printf("%s\n%s\n", tint(cRed, fmt.Sprintf("  ✗ verify failed (exit %d)", code)), tail(out, 4096))
			}
			continue
		}
		if input == "/model" || strings.HasPrefix(input, "/model ") {
			arg := strings.TrimSpace(strings.TrimPrefix(input, "/model"))
			if arg == "" {
				fmt.Printf("current model: %s\n", model)
				if avail, err := listModels(baseURL); err == nil {
					fmt.Println("available:")
					for _, m := range avail {
						marker := "  "
						if m == model {
							marker = "* "
						}
						fmt.Println(marker + m)
					}
				}
			} else {
				model = arg
				curModel = arg
				fmt.Printf("switched to model: %s\n", model)
			}
			continue
		}
		if input == "/help" {
			fmt.Println("commands: /plan <task> /init /verify /commit /rewind /stats /budget /models /effort /reload /fork /tree /ctx /compact /context /diff [path] /undo <path> /resume /sessions /tools /model [id] /allow [prefix] /copy /todos /agents /help")
			fmt.Println("input: Tab completes /commands and paths · ↑/↓ history (persists across sessions) · !cmd runs shell directly, output joins context · @path attaches a file · paste multi-line directly · Ctrl+C clears (double = quit), Ctrl+D quits")
			fmt.Println(`input: single line, or """ alone to open a multi-line block (""" again to send)`)
			continue
		}
		if input == "/init" {
			fmt.Println("(exploring the repository and generating AGENTS.md — this may take a while)")
			messages = append(messages, Message{Role: "user", Content: initPrompt})
			messages = runTurn(baseURL, model, sb, st, messages)
			st.Append(messages)
			continue
		}
		if input == "/compact" {
			messages = compact(baseURL, auxModelFor(), messages, st)
			continue
		}
		if input == "/allow" || strings.HasPrefix(input, "/allow ") {
			handleAllow(strings.TrimSpace(strings.TrimPrefix(input, "/allow")))
			continue
		}
		// Custom command templates run as a normal model turn with the
		// expanded prompt (checkpoint, verify, notify all apply below).
		if expanded, ok := expandCustomCommand(input); ok {
			fmt.Println(tint(cDim, "  (expanded custom command)"))
			input = expanded
		} else if strings.HasPrefix(input, "/") {
			name, _, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
			if _, isCustom := customCommands[name]; isCustom {
				fmt.Printf("/%s requires arguments — usage: /%s <args>\n", name, name)
				continue
			}
			if !isBuiltinCommand(name) {
				fmt.Printf("unknown command /%s — /help lists commands\n", name)
				continue
			}
		}
		modifiedBefore := len(sb.Modified)
		expanded, attached := expandFileRefs(input, root)
		if len(attached) > 0 {
			fmt.Println(tint(cDim, "  (attached: "+strings.Join(attached, ", ")+")"))
		}
		takeCheckpoint(root, input)
		turnStart := time.Now()
		emitUser(expanded) // dashboard/TUI: show what the user asked
		messages = append(messages, Message{Role: "user", Content: expanded})
		if contextV2 {
			// v2: the model sees a distilled context; the canonical
			// transcript (messages) keeps full fidelity for sessions and
			// resume. The turn's new messages are grafted back on at the
			// end. Mid-turn autosave is off in this mode (wire ≠ canonical).
			wire := distill(messages, sb)
			fmt.Println(tint(cDim, fmt.Sprintf("  (context v2: sending ~%dk distilled from ~%dk)", estimateTokens(wire)/1000, estimateTokens(messages)/1000)))
			base := len(wire)
			out := runTurn(baseURL, model, sb, &SessionStore{}, wire)
			out = runVerifyLoop(baseURL, model, sb, &SessionStore{}, out, modifiedBefore)
			messages = append(messages, out[base:]...)
			st.Append(messages)
		} else {
			messages = runTurn(baseURL, model, sb, st, messages)
			st.Append(messages) // autosave every turn — crashes lose nothing

			// Close the loop against reality: if this turn modified files and a
			// verify command is configured, run it and feed failures back.
			messages = runVerifyLoop(baseURL, model, sb, st, messages, modifiedBefore)
		}
		notifyTurnDone(time.Since(turnStart))
		checkBudget()
		if out, ok := runHook("post_turn", nil); !ok {
			fmt.Println(tint(cYellow, "  (post_turn hook failed)\n"+tail(out, 1024)))
		} else if strings.TrimSpace(out) != "" {
			fmt.Println(tint(cDim, strings.TrimRight(out, "\n")))
		}

		// Long-session safety: when the conversation grows past the soft
		// budget, distill it before the next turn rather than letting it
		// eventually exceed the model's context and fail abruptly.
		if estimateTokens(messages) > autoCompactTokens {
			fmt.Println(tint(cDim, fmt.Sprintf("  (context ~%d tokens — auto-compacting)", estimateTokens(messages))))
			messages = compact(baseURL, auxModelFor(), messages, st)
			st.Append(messages)
		}
	}
	sb.Summary()
	if st.file != nil {
		st.generateTitle(messages) // one-line title for /resume pick (best-effort)
		fmt.Println("session saved:", st.Path(), "— resume with: -resume latest")
		if t := sessionTitle(st.Path()); t != "" {
			fmt.Printf("titled: %q\n", t)
		}
		writeJournal(root, sessionTitle(st.Path()), messages) // vault diary (config "journal")
		writeAuditNote(root, sessionTitle(st.Path()), sb)     // structured audit trail (config "audit")
	}
	return 0
}

// statusLine composes the dim per-prompt status: model · context pressure ·
// project (branch, * when dirty). It's what turns "auto-compact surprised
// me" into "I watched the number climb".
func statusLine(model string, messages []Message, root string) string {
	used := estimateTokens(messages)
	s := fmt.Sprintf("%s · ~%dk/%dk · %s", model, used/1000, autoCompactTokens/1000, filepath.Base(root))
	run := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	if branch, ok := run("rev-parse", "--abbrev-ref", "HEAD"); ok {
		dirty := ""
		if status, _ := run("status", "--porcelain"); status != "" {
			dirty = "*"
		}
		s += fmt.Sprintf(" (%s%s)", branch, dirty)
	}
	return tint(cDim, s)
}

// expandFileRefs inlines @path references: "fix the bug in @cmd/add.go"
// appends that file's content to the message so the model doesn't spend a
// turn reading it. Only existing files under 64KB expand; anything else is
// left as literal text (an email address is not a file reference). Returns
// the expanded message and the paths that were attached.
func expandFileRefs(input, root string) (string, []string) {
	const maxAttach = 64 * 1024
	var attached []string
	var blocks strings.Builder
	for _, f := range strings.Fields(input) {
		if !strings.HasPrefix(f, "@") || len(f) < 2 {
			continue
		}
		ref := strings.TrimRight(f[1:], ".,;:!?)('\"") // strip trailing punctuation
		p := ref
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		if fi.Size() > maxAttach {
			fmt.Fprintf(&blocks, "\n\n--- @%s: too large to attach (%d bytes) — use read_file with offset/limit ---", ref, fi.Size())
			attached = append(attached, ref+" (too large)")
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fmt.Fprintf(&blocks, "\n\n--- content of @%s ---\n%s", ref, string(data))
		attached = append(attached, ref)
	}
	return input + blocks.String(), attached
}

// handleCopy pipes the last assistant reply to the Windows clipboard via
// clip.exe (WSL interop). Fails gracefully outside WSL.
func handleCopy(messages []Message) {
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			last = messages[i].Content
			break
		}
	}
	if last == "" {
		fmt.Println("nothing to copy yet")
		return
	}
	cmd := exec.Command("clip.exe")
	cmd.Stdin = strings.NewReader(last)
	if err := cmd.Run(); err != nil {
		fmt.Println("copy failed (clip.exe unavailable?):", err)
		return
	}
	fmt.Printf("copied last reply to the Windows clipboard (%d chars)\n", len(last))
}

// handleReload implements /reload: rebuild the agent from its own source
// and re-exec into the new binary, resuming this session — the four-step
// install loop (quit, copy, rebuild, relaunch) as one command. The rebuild
// runs FIRST; a broken build leaves the current process running untouched.
func handleReload(baseURL, model, root string) {
	srcDir := agentSourceDir
	if srcDir == "" {
		fmt.Println("reload: agent source directory unknown (started outside `go run`?)")
		return
	}
	fmt.Println(tint(cDim, "  (rebuilding agent from "+srcDir+")"))
	out, code, err := execShell("go build -o /dev/null .", srcDir, false)
	if err != nil || code != 0 {
		fmt.Println("reload: build failed — staying on the current binary:")
		fmt.Println(tail(out, 2048))
		return
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		fmt.Println("reload:", err)
		return
	}
	args := []string{"go", "run", ".", "-url", baseURL, "-model", model, "-resume", "latest", root}
	fmt.Println("reloading — resuming this session in the new build…")
	if err := execReplace(goBin, args, os.Environ()); err != nil {
		fmt.Println("reload: exec failed:", err)
	}
}

// agentSourceDir is the directory the agent was launched from (captured at
// startup) — where /reload rebuilds.
var agentSourceDir string

// listServerModels implements /models: which models the server has and —
// when the server is LM Studio — which are LOADED right now. LM Studio's
// native REST API (/api/v0/models) reports per-model state and context
// length; anything else falls back to the plain /v1/models list. Note on
// switching: LM Studio's JIT loading (on by default in server settings)
// means /model <id> works without touching the server — the first request
// after a switch loads the model and will be slow; whether the OLD model
// unloads is governed by the server's auto-unload/TTL settings, not by us.
func listServerModels(baseURL, current string) {
	type v0Model struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		State     string `json:"state"`
		MaxCtx    int    `json:"max_context_length"`
		Quant     string `json:"quantization"`
		Publisher string `json:"publisher"`
	}
	var out struct {
		Data []v0Model `json:"data"`
	}
	resp, err := httpClient.Get(baseURL + "/api/v0/models")
	if err == nil && resp.StatusCode == http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		if json.NewDecoder(resp.Body).Decode(&out) == nil && len(out.Data) > 0 {
			sort.Slice(out.Data, func(i, j int) bool { // loaded first
				return out.Data[i].State == "loaded" && out.Data[j].State != "loaded"
			})
			for _, m := range out.Data {
				marker := "  "
				if m.ID == current {
					marker = "* "
				}
				state := m.State
				if state == "" {
					state = "?"
				}
				extra := m.Quant
				if m.MaxCtx > 0 {
					extra += fmt.Sprintf(" · %dk ctx", m.MaxCtx/1000)
				}
				fmt.Printf("%s%-45s %-11s %-6s %s\n", marker, m.ID, "["+state+"]", m.Type, extra)
			}
			fmt.Println("switch with /model <id> — an unloaded model JIT-loads on its first request (slow first turn)")
			return
		}
		_ = resp.Body.Close()
	} else if resp != nil {
		_ = resp.Body.Close()
	}
	// Not LM Studio (or native API disabled): plain list, no state info.
	models, err := listModels(baseURL)
	if err != nil {
		fmt.Println("models:", err)
		return
	}
	for _, m := range models {
		marker := "  "
		if m == current {
			marker = "* "
		}
		fmt.Println(marker + m)
	}
}

// handleDiff implements /diff [path]: what this session changed, per file —
// current content vs the pre-session .bak (the same baseline /undo restores
// to). With no argument, every modified file; with one, just that file.
func handleDiff(sb *Sandbox, arg string) {
	paths := map[string]bool{}
	if arg != "" {
		p, err := sb.resolve(arg)
		if err != nil {
			fmt.Println("diff:", err)
			return
		}
		paths[p] = true
	} else {
		for _, p := range sb.Modified {
			paths[p] = true
		}
		if len(paths) == 0 {
			fmt.Println("nothing modified this session")
			return
		}
	}
	for p := range paths {
		oldData, err := os.ReadFile(p + ".bak")
		if err != nil {
			// New file this session: everything is an addition.
			oldData = nil
		}
		curData, err := os.ReadFile(p)
		if err != nil {
			fmt.Printf("diff: %s: %v\n", p, err)
			continue
		}
		hunks := diffLines(string(oldData), string(curData))
		if len(hunks) == 0 {
			fmt.Printf("%s: no changes vs session start\n", p)
			continue
		}
		fmt.Println(tint(cCyan, "  "+p+" (vs pre-session .bak)"))
		fmt.Print(renderDiff(hunks, useColor, 400))
	}
}

// handleUndo implements /undo <path>: restore a file from its .bak, which —
// per the backup-once-per-session rule — holds the pre-session original.
func handleUndo(sb *Sandbox, arg string) {
	if arg == "" {
		if len(sb.Modified) == 0 {
			fmt.Println("nothing modified this session")
			return
		}
		fmt.Println("usage: /undo <path> — files modified this session:")
		seen := map[string]bool{}
		for _, p := range sb.Modified {
			if !seen[p] {
				seen[p] = true
				fmt.Println("  " + p)
			}
		}
		return
	}
	path, err := sb.resolve(arg)
	if err != nil {
		fmt.Println("undo:", err)
		return
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		fmt.Printf("undo: no backup at %s.bak\n", path)
		return
	}
	if err := writeAtomic(path, bak); err != nil {
		fmt.Println("undo:", err)
		return
	}
	fmt.Printf("restored %s from %s.bak (%d bytes)\n", path, path, len(bak))
}

// handleAllow implements /allow: with no argument it lists the effective
// allowlist; with an argument it appends that prefix to ~/.agent/allow.txt,
// which takes effect on the very next command check.
func handleAllow(arg string) {
	if arg == "" {
		fmt.Println("auto-approved (built-in prefixes):")
		for _, p := range autoApprovedPrefixes {
			fmt.Println("  " + p)
		}
		fmt.Println("auto-approved (built-in exact):")
		for _, e := range autoApprovedExact {
			fmt.Println("  " + e)
		}
		user := userAllowPrefixes()
		if len(user) == 0 {
			fmt.Printf("user prefixes: (none — add with /allow <prefix>, or edit %s)\n", allowFile())
			return
		}
		fmt.Printf("user prefixes (%s):\n", allowFile())
		for _, p := range user {
			fmt.Println("  " + p)
		}
		return
	}
	if msg := appendAllow(arg); msg != "" {
		fmt.Println("error:", msg)
		return
	}
	fmt.Printf("added to allowlist: %q (effective immediately; edit %s to remove)\n", arg, allowFile())
}

// tail returns the last n bytes of s (for showing the end of long output,
// where the actual error usually lives).
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...[truncated]\n" + s[len(s)-n:]
}
