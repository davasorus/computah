# Project Overview
Computah is a terminal tool for AI coding agents. It uses Go as the primary language. The system manages file changes and shell commands. It tracks state within sessions.

# Repository Structure
- `cmd/`: Contains entry points for CLI tools.
- `internal/agent/`: Contains core logic, session management, and tool integration.
- `internal/core/`: Contains shared types like Message and Event.
- `internal/tui/`: Provides the terminal user interface.
- `internal/web/`: Offers a web dashboard view.
- `internal/md/`: Handles markdown rendering in the terminal.
- `internal/tools_ext/`: Includes additional tools for expanded functionality.

# Build and Test
Install the tool: `go install github.com/davasorus/computah@latest`
Run tests: `go test ./...`
Perform evaluations: `go run . -eval <file>.json --runs 3`

# Code Conventions
Follow standard Go formatting. Use descriptive names for variables and functions. Separate core logic from user interface components. Maintain consistent naming for all tools. Add clear comments for complex operations.

# Commit Messages
All commits must follow the Conventional Commits standard.
The format is `type(scope): description`.
Use these types: feat, fix, docs, style, refactor, perf, test, build, ci, chore.
Use imperative mood.
Limit subjects to 72 characters.
Include a body for non-trivial changes.
Mark breaking changes with ! after the type or scope.

# Documentation Language
Write all documentation in ASD-STE100 Simplified Technical English.
Use active voice and simple verb tenses.
Write one instruction per sentence.
Keep sentences under 25 words.
Use one term for each concept.
Do not use vague words or -ing forms.
Limit paragraphs to six sentences on one topic.
