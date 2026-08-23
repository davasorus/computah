package core

import (
	"fmt"
	"regexp"
	"strings"
)

// Catastrophic command patterns that are blocked outright, regardless of user
// approval. This is a defense-in-depth backstop against a model (or a
// fat-fingered !command) proposing something irreversibly destructive — it is
// NOT a substitute for the approval gate, which remains the primary control.
// The list targets high-blast-radius operations, not general "dangerous"
// commands (the user approves those deliberately).
var dangerousCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`rm\s+(-[a-zA-Z]*\s+)*(-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r)\s+/(\s|$)`), // rm -rf /
	regexp.MustCompile(`rm\s+(-[a-zA-Z]*\s+)*(-[a-zA-Z]*r[a-zA-Z]*f|-[a-zA-Z]*f[a-zA-Z]*r)\s+/\*`),     // rm -rf /*
	regexp.MustCompile(`:\s*\(\s*\)\s*\{.*\|.*&\s*\}\s*;`),                                             // fork bomb :(){ :|:& };:
	regexp.MustCompile(`\bmkfs\.\w+\s`),                                                                // mkfs.* (format)
	regexp.MustCompile(`\bdd\s+.*\bof=/dev/(sd|nvme|hd|vd|disk)`),                                      // dd to a raw disk
	regexp.MustCompile(`>\s*/dev/(sd|nvme|hd|vd|disk)`),                                                // redirect into a raw disk
	regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba)?sh\b`),                                 // curl … | sh (remote code exec)
	regexp.MustCompile(`\bchmod\s+-R\s+[0-7]*777\s+/(\s|$)`),                                           // chmod -R 777 /
}

// VetCommand returns a non-nil error if cmdStr matches a catastrophic,
// irreversible-damage pattern that must be blocked regardless of approval.
// It intentionally does NOT try to be a general command sanitizer — running
// arbitrary shell commands is the tool's purpose, and the approval gate is the
// primary control. This is only a last-resort guard against a handful of
// system-destroying mistakes.
func VetCommand(cmdStr string) error {
	c := strings.TrimSpace(cmdStr)
	for _, re := range dangerousCommandPatterns {
		if re.MatchString(c) {
			return fmt.Errorf("refusing to run a command matching a blocked destructive pattern (%s)", re.String())
		}
	}
	return nil
}
