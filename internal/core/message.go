package core

// Message is one turn in an OpenAI-compatible chat conversation. It is the
// central data type passed between the model client, session store, and UI.
type Message struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // set on role=tool replies
}

// ToolCall is a single tool invocation requested by the model. Arguments is a
// JSON *string* (OpenAI-compatible), unlike Ollama's pre-parsed map.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
