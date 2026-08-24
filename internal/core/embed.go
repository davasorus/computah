package core

// EmbedModel is the embedding model id (config "embed_model"); "" = disabled.
// Set by the engine from config, read by the code_search tool for semantic
// (meaning-based) code search over the working directory.
var EmbedModel string
