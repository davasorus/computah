# Security

## Threat model

computah is a local AI coding agent. By design it:

- **Runs shell commands** proposed by the model (`run_command`), and
- **Reads and writes files** in a working directory (`read_file`, `edit_files`, `@file` attachments).

These behaviors are the tool's purpose, not vulnerabilities. Static analysis
(e.g. CodeQL's `go/command-injection` and `go/path-injection` rules) flags them
because model/user input reaches `exec` and file APIs. The controls below are
what make that safe in this tool's context.

### Command execution

Model-proposed commands go through a human-in-the-loop approval gate
(`toolRunCommand`):

- Read-only commands on a built-in allowlist (`git status`, `git diff`, `ls`,
  …) run immediately.
- **Everything else is declined by default** and requires explicit `y`
  (once) or `a` (always, added to the user's allowlist) at an interactive
  prompt.
- A user-defined `pre_command` hook can block commands programmatically.
- `-yes` mode (opt-in, for non-interactive/eval use) bypasses the prompt; use
  it only in trusted, sandboxed environments.

The command string is intentionally passed to `bash -lc` unescaped — sanitizing
it would break the feature. The approval gate, not input escaping, is the
primary control. As a defense-in-depth backstop, `core.VetCommand` blocks a
small set of catastrophic, irreversible commands (`rm -rf /`, fork bombs,
disk-format/`dd`, `curl … | sh`) outright, regardless of approval.

Static analysers (e.g. CodeQL's `go/command-injection`) will always flag the
`bash -lc` call, because a dynamic string reaches a shell interpreter. This is
inherent to any command-running agent and cannot be resolved without removing
the feature; it is an accepted, reviewed risk given the controls above.

### File access

All model-driven file operations resolve paths through `Sandbox.resolve`, which
calls `core.ConfinePath` to reject any path that escapes the working directory
(absolute paths elsewhere, `..` traversal, sibling-prefix tricks). The `@file`
attachment paths in the TUI and headless dashboard are confined the same way.

### Running untrusted code

`run_command` executes on the host (behind the approval gate and the
`core.VetCommand` denylist), which is appropriate for a coding agent working in
your own repo — but it is NOT a boundary for genuinely untrusted code. For that,
connect [`podman-sandbox-runner`](https://github.com/davasorus/podman-sandbox-runner)
as an MCP server (see the example in `internal/agent/mcp.go`). Its `run_sandbox`
and `run_script` tools execute code in an ephemeral, network-less container with
a read-only rootfs, dropped capabilities, a non-root user, and memory/CPU/time
limits — a real isolation boundary. In one-shot mode (the default) each call
gets a fresh container, so nothing persists between runs. Set `"prefer": true`
with a `"prefer_hint"` so the model reaches for the sandbox on unfamiliar or
untrusted code instead of running it on the host.

## Reporting a vulnerability

This is a personal project. If you find a security issue that isn't covered by
the threat model above, please open a private security advisory on the GitHub
repository rather than a public issue.
