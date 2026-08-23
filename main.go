// Command computah is a self-hosted, terminal-based AI coding agent that runs
// against a local OpenAI-compatible model server (e.g. LM Studio). All behavior
// lives in internal/agent; this file only hands control to the Cobra command
// tree in cmd/.
package main

import "github.com/davasorus/computah/cmd"

func main() { cmd.Execute() }
