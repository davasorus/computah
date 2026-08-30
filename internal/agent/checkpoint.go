// Git checkpointing — repo-level protection where .bak is file-level.
//
// Before every model turn, `git stash create` snapshots the working tree
// into an unreferenced commit WITHOUT touching the worktree or the stash
// list — the cheapest possible checkpoint. /rewind restores any turn's
// state with `git checkout <hash> -- .`.
//
// Honest limitations, stated because they matter: `git stash create` only
// records TRACKED files, so a brand-new untracked file the model created
// after a checkpoint is not deleted by rewinding (its content is simply not
// managed); and the snapshot commits are unreferenced, so git gc can
// eventually collect them — checkpoints are session-scale protection, not
// history. /commit is the durable path, and its message generation enforces
// the Conventional Commits standard the system prompt already mandates.
package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/davasorus/computah/internal/core"
)

type checkpoint struct {
	turn   int
	hash   string // "" = tree was clean at checkpoint time
	prompt string // first words of the user prompt that followed
	at     time.Time
}

var (
	checkpoints    []checkpoint
	checkpointsOff bool // config: "no_checkpoints"
)

func gitRun(root string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func inGitRepo(root string) bool {
	_, err := gitRun(root, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// takeCheckpoint snapshots the tree before a model turn. Failures are
// silent by design — a checkpoint problem must never block actual work.
func takeCheckpoint(root, prompt string) {
	if checkpointsOff || !inGitRepo(root) {
		return
	}
	hash, err := gitRun(root, "stash", "create", fmt.Sprintf("agent checkpoint turn %d", len(checkpoints)+1))
	if err != nil {
		return
	}
	if len(prompt) > 50 {
		prompt = prompt[:50] + "…"
	}
	checkpoints = append(checkpoints, checkpoint{
		turn:   len(checkpoints) + 1,
		hash:   hash, // empty when clean: rewind = discard all changes
		prompt: prompt,
		at:     time.Now(),
	})
}

// handleRewind implements /rewind: pick a checkpoint, confirm, restore.
func handleRewind(root string) {
	if len(checkpoints) == 0 {
		core.EmitStatus("no-checkpoints")
		return
	}
	core.EmitStatus("checkpoints (tree state BEFORE each turn):")
	start := 0
	if len(checkpoints) > 20 {
		start = len(checkpoints) - 20
	}
	for _, c := range checkpoints[start:] {
		state := c.hash[:min(8, len(c.hash))]
		if c.hash == "" {
			state = "clean"
		}
		core.EmitStatus(fmt.Sprintf("  %2d) %s  %-8s  %q", c.turn, c.at.Format("15:04:05"), state, c.prompt))
	}
	ans, ok := askLine("rewind to which? (number, Enter to cancel) ")
	if !ok || ans == "" {
		return
	}
	n, err := strconv.Atoi(ans)
	if err != nil || n < 1 || n > len(checkpoints) {
		core.EmitError(fmt.Sprintf("no checkpoint %q", ans))
		return
	}
	c := checkpoints[n-1]
	confirm, _ := askLine(fmt.Sprintf("restore tracked files to the state before turn %d? Uncommitted changes since will be lost. [y/N] ", c.turn))
	if s := strings.ToLower(confirm); s != "y" && s != "yes" {
		core.EmitStatus("(cancelled)")
		return
	}
	if c.hash == "" {
		if out, err := gitRun(root, "checkout", "HEAD", "--", "."); err != nil {
			core.EmitError("rewind failed: " + out)
			return
		}
	} else {
		if out, err := gitRun(root, "checkout", c.hash, "--", "."); err != nil {
			core.EmitError("rewind failed: " + out)
			return
		}
		// checkout <hash> -- . also stages the restored content; unstage so
		// the tree looks like a normal edited state, not a half-commit.
		_, _ = gitRun(root, "reset", "-q")
	}
	core.EmitStatus("rewound tracked files to the state before turn " + strconv.Itoa(c.turn))
	core.EmitLineC(cDim, "(files CREATED after that checkpoint still exist — they were untracked; check git status)")
	core.EmitLineC(cDim, "note: the conversation still describes the newer state — consider telling the model what you rewound, or /compact")
}

// handleCommit implements /commit: the model writes the Conventional
// Commits message from the actual diff; you approve; the harness commits.
func handleCommit(root string) {
	if !inGitRepo(root) {
		core.EmitStatus("not a git repository")
		return
	}
	status, _ := gitRun(root, "status", "--porcelain")
	if status == "" {
		core.EmitStatus("working tree clean — nothing to commit")
		return
	}
	diff, _ := gitRun(root, "diff", "HEAD")
	if len(diff) > 20*1024 {
		diff = diff[:20*1024] + "\n...[diff truncated for message generation]"
	}
	core.EmitStatus(tint(cDim, "  (generating commit message from the diff)"))
	msgs := []Message{
		{Role: "system", Content: promptCommitSystem()},
		{Role: "user", Content: "Changed files:\n" + status + "\n\nDiff:\n" + diff},
	}
	// Direct call, no tools: plan mode's currentTools doesn't apply here
	// because this uses its own context; keep it simple and tool-free.
	saved := planMode
	planMode = true // advertise read-only (nothing callable matters for a pure-text ask)
	reply, _, err := streamChat(curBaseURL, auxModelFor(), msgs)
	planMode = saved
	if err != nil {
		core.EmitError("message generation failed: " + err.Error())
		return
	}
	commitMsg := strings.TrimSpace(strings.Trim(strings.TrimSpace(reply.Content), "`"))
	if commitMsg == "" {
		core.EmitStatus("model produced no message — write it yourself with !git commit")
		return
	}
	ans, ok := askLine("commit all changes with this message? [y/N/edit] ")
	if !ok {
		return
	}
	switch strings.ToLower(ans) {
	case "y", "yes":
	case "edit":
		newMsg, nok := askLine("subject line: ")
		if !nok || strings.TrimSpace(newMsg) == "" {
			core.EmitStatus("(cancelled)")
			return
		}
		commitMsg = strings.TrimSpace(newMsg)
	default:
		core.EmitStatus("(cancelled)")
		return
	}
	if out, err := gitRun(root, "add", "-A"); err != nil {
		core.EmitError("git add failed: " + out)
		return
	}
	cmd := exec.Command("git", "commit", "-F", "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(commitMsg)
	out, err := cmd.CombinedOutput()
	core.EmitStatus(strings.TrimSpace(string(out)))
	if err == nil {
		checkpoints = nil // committed state is the new baseline
	}
}
