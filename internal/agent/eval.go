// One-shot mode (-p) and the eval harness (-eval).
//
// One-shot makes the agent pipeable and scriptable:
//
//	agent -p "fix the failing test" -yes ~/proj && go test ./...
//
// The eval harness is how harness changes stop being judged by vibes: a JSON
// file of cases, each a prompt plus a shell check, run N times against the
// current model and harness. "Did the loop breaker help" and "is Gemma
// better than Qwen at this" become pass rates instead of impressions.
//
// Eval file format (JSON array):
//
//	[
//	  {"name": "add-position", "prompt": "set the position column on insert in cmd/add.go",
//	   "check": "go build ./... && grep -q position cmd/add.go"},
//	  ...
//	]
//
// Each run gets a FRESH context (system prompt rebuilt, empty history) and a
// fresh Sandbox, but shares the working directory — checks that mutate state
// should clean up after themselves (e.g. via git checkout in the check).
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// forceEval skips the preflight (dirty-tree + baseline-build) checks.
var forceEval bool

type evalCase struct {
	Name   string `json:"name"`
	Setup  string `json:"setup,omitempty"` // arrange: runs before EACH attempt (reset state, plant the bug)
	Prompt string `json:"prompt"`
	Check  string `json:"check"` // assert: exit 0 = pass
}

// runOneShot runs a single prompt through the full loop (verify included)
// and exits. Returns a process exit code: 0 on success, 1 on error.
func runOneShot(root string, sb *Sandbox, prompt string) int {
	if !assumeYes {
		fmt.Println("(tip: -p without -yes will still stop to ask about mutating commands)")
	}
	st := newSessionStore(root)
	messages := []Message{
		{Role: "system", Content: buildSystemPrompt(root)},
		{Role: "user", Content: prompt},
	}
	st.Append(messages)
	modifiedBefore := len(sb.Modified)
	messages = runTurn(curBaseURL, curModel, sb, st, messages)
	st.Append(messages)
	messages = runVerifyLoop(curBaseURL, curModel, sb, st, messages, modifiedBefore)
	st.Append(messages)
	sb.Summary()
	if st.file != nil {
		fmt.Println("session saved:", st.Path())
	}
	return 0
}

// runEval executes every case in the file `runs` times and reports pass
// rates. Returns a process exit code: 0 if every run of every case passed.
func runEval(root, path string, runs int) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("eval:", err)
		return 1
	}
	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		fmt.Println("eval: bad eval file:", err)
		return 1
	}
	if len(cases) == 0 {
		fmt.Println("eval: no cases in file")
		return 1
	}
	if runs < 1 {
		runs = 1
	}
	// PREFLIGHT — the two-second checks that prevent the two-hour wastes.
	// Both failure modes actually happened: setups ran `git checkout -- .`
	// against a live refactor (dirty tree), and an entire 6-case run was
	// measured against a HEAD that didn't compile (broken baseline).
	if !forceEval {
		if inGitRepo(root) {
			if status, _ := gitRun(root, "status", "--porcelain"); strings.TrimSpace(status) != "" {
				fmt.Println("eval: REFUSED — the working tree has uncommitted changes, and eval setups reset tracked files.")
				fmt.Println("  Commit or stash your work, or run evals in a worktree:")
				fmt.Println("    git worktree add -b eval-scratch ../" + filepath.Base(root) + "-eval HEAD")
				fmt.Println("  Override (dangerous): -force-eval")
				return 1
			}
		}
		buildCmd := verifyCommand
		if buildCmd == "" {
			if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
				buildCmd = "go build ./..."
			}
		}
		if buildCmd != "" {
			if out, code, err := execShell(buildCmd, root, false); err != nil || code != 0 {
				fmt.Println("eval: REFUSED — the baseline doesn't build; every check would fail regardless of the model.")
				fmt.Println(tail(out, 1024))
				fmt.Println("  Fix the tree (or commit the fixes) first. Override: -force-eval")
				return 1
			}
		}
	}
	// Evals are non-interactive by definition — there's no one at the
	// keyboard to answer y/N between runs.
	assumeYes = true
	statsTrace = true // per-request timing lines: the KV-cache diagnostic

	fmt.Printf("eval: %d case(s) × %d run(s) — model=%s workdir=%s\n\n", len(cases), runs, curModel, root)
	type result struct {
		name   string
		passes int
		times  []time.Duration
	}
	var results []result
	allPassed := true
	for _, c := range cases {
		if c.Name == "" || c.Prompt == "" || c.Check == "" {
			fmt.Printf("== %s: skipped (name, prompt, and check are all required)\n", c.Name)
			allPassed = false
			continue
		}
		r := result{name: c.Name}
		for run := 1; run <= runs; run++ {
			fmt.Printf("== %s (run %d/%d)\n", c.Name, run, runs)
			if c.Setup != "" {
				if out, code, err := execShell(c.Setup, root, false); err != nil || code != 0 {
					fmt.Printf("%s setup failed:\n%s\n\n", tint(cYellow, "   SKIP"), tail(out, 1024))
					allPassed = false
					continue
				}
			}
			start := time.Now()
			// Fresh everything per run: context, sandbox — each run measures
			// the harness+model, not leftover state. The transcript IS
			// persisted (eval-<case>-run<N>-*.jsonl in the sessions dir): a
			// 90-minute run you can't autopsy is a 90-minute run wasted.
			sb := &Sandbox{Root: root}
			st := newSessionStore(root)
			if st.dir != "" {
				// Eval transcripts live in a subdirectory: they must never
				// be resumable as conversations. (Observed: -resume latest
				// picked up an eval transcript, and the model spent two
				// sessions hunting the eval's planted-bug prompt in a repo
				// where the bug was already fixed.)
				st.dir = filepath.Join(st.dir, "evals")
				_ = os.MkdirAll(st.dir, 0o755)
			}
			st.name = fmt.Sprintf("eval-%s-run%d-%s", c.Name, run, time.Now().Format("150405"))
			// Per-run deltas from the global stats recorder.
			stats.mu.Lock()
			reqBefore, promptBefore, genBefore, thinkBefore, ttfbBefore := stats.requests, stats.promptTk, stats.genTk, stats.thinkTk, stats.ttfbDur
			stats.mu.Unlock()
			messages := []Message{
				{Role: "system", Content: buildSystemPrompt(root)},
				{Role: "user", Content: c.Prompt},
			}
			st.Append(messages)
			messages = runTurn(curBaseURL, curModel, sb, st, messages)
			st.Append(messages)
			messages = runVerifyLoop(curBaseURL, curModel, sb, st, messages, 0)
			st.Append(messages)
			out, code, err := execShell(c.Check, root, false)
			elapsed := time.Since(start).Round(time.Second)
			r.times = append(r.times, elapsed)
			stats.mu.Lock()
			dReq, dPrompt, dGen, dThink, dTTFB := stats.requests-reqBefore, stats.promptTk-promptBefore, stats.genTk-genBefore, stats.thinkTk-thinkBefore, stats.ttfbDur-ttfbBefore
			stats.mu.Unlock()
			detail := fmt.Sprintf("%v · %d req · ~%dk sent · %d thought · %d gen · %s prompt-processing",
				elapsed, dReq, dPrompt/1000, dThink, dGen, dTTFB.Round(time.Second))
			if err == nil && code == 0 {
				r.passes++
				fmt.Printf("%s (%s)\n\n", tint(cGreen, "   PASS"), detail)
			} else {
				allPassed = false
				fmt.Printf("%s (%s)\n%s\n\n", tint(cRed, "   FAIL"), detail, tail(out, 1024))
			}
		}
		results = append(results, r)
	}
	fmt.Println("---- eval summary ----")
	for _, r := range results {
		var total time.Duration
		for _, t := range r.times {
			total += t
		}
		avg := time.Duration(0)
		if len(r.times) > 0 {
			avg = (total / time.Duration(len(r.times))).Round(time.Second)
		}
		fmt.Printf("  %-30s %d/%d passed  (avg %v)\n", r.name, r.passes, runs, avg)
	}
	if allPassed {
		return 0
	}
	return 1
}
