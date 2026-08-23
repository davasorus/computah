package core

// VaultPath is the configured Obsidian vault directory ("" = unconfigured).
// Shared config primitive: the vault tool feature (tools_ext) and the journal
// writer (agent) both read it.
var VaultPath string

// VaultAgentDir is the subdirectory under the vault where the agent writes.
const VaultAgentDir = "agent"

// EmbedModel is the embedding model id (config "embed_model"); "" = disabled.
// Shared: set by the engine from config, read by the vault/embed tools.
var EmbedModel string
