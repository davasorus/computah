package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---------- OpenAI-compatible API types ----------

type ChatRequest struct {
	Model           string           `json:"model"`
	Messages        []Message        `json:"messages"`
	Tools           []map[string]any `json:"tools,omitempty"`
	Stream          bool             `json:"stream"`
	MaxTokens       int              `json:"max_tokens,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"` // low|medium|high — attacks the measured ~90%-of-wall-time thinking cost
}

// reasoningEffort / planReasoningEffort control the model's thinking budget
// per request (config "reasoning_effort", "plan_reasoning_effort"; /effort
// changes it live). Plan mode gets its own setting because that's where deep
// thinking EARNS its time; tool-loop turns mostly don't need it. Empty =
// parameter omitted entirely, so servers/models that don't support it see
// nothing new. OpenAI-compatible servers ignore unknown fields, so the worst
// case for an unsupported server is "no effect" — but verify against your
// LM Studio version, and /effort off restores the old behavior instantly.
var (
	reasoningEffort     string
	planReasoningEffort string
)

func currentReasoningEffort() string {
	if planMode && planReasoningEffort != "" {
		return planReasoningEffort
	}
	return reasoningEffort
}

// maxGenTokens caps a single generation (config "max_tokens", default 8192).
// This is a tripwire, not a sampling knob: an eval transcript showed the
// model generating a runaway tool call for 15 SILENT minutes until the HTTP
// timeout killed the request and discarded everything. A cap converts that
// failure mode into a bounded, visible finish_reason=length the model can
// recover from. 8192 tokens ≈ a 30KB file write — generous for real work.
var maxGenTokens = 8192

// statsTrace prints a per-request timing line (enabled in eval mode).
var statsTrace bool

// lastFinishReason is the finish_reason of the most recent request —
// runTurn uses it to detect a cap-truncated deliberation (finish=length
// with no tool calls) and nudge the model into acting instead of ending
// the turn on words.
var lastFinishReason string

// ---------- Tool definitions ----------

func toolDef(name, desc string, props map[string]any, required []string) map[string]any {
	if required == nil {
		required = []string{} // nil marshals to JSON null; strict validators (LM Studio) demand an array
	}
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters": map[string]any{
				"type":       "object",
				"properties": props,
				"required":   required,
			},
		},
	}
}

// ---------- Chat plumbing (streaming) ----------

// apiKey is an optional Bearer token for authenticated OpenAI-compatible
// endpoints (cloud OpenAI, a proxied Anthropic, an authenticated gateway).
// Empty for local servers like LM Studio/Ollama, which need no auth — the
// zero-config local default is preserved. Resolved at startup from config
// "api_key" or the COMPUTAH_API_KEY env var (env wins).
var apiKey string

// setAuth adds the Bearer header when an API key is configured. A no-op for
// local servers, so unauthenticated endpoints see no new header.
func setAuth(req *http.Request) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

// Generous timeout: a local model on Vulkan can legitimately take minutes
// on a long generation; don't let the client be the thing that gives up.
var httpClient = &http.Client{Timeout: 15 * time.Minute}

// chat sends the conversation and streams the reply. Text tokens are printed
// live via onToken as they arrive; tool-call fragments are accumulated and
// returned assembled on the final Message. Cancelling ctx aborts the request
// mid-stream — the server sees the disconnect and stops generating.
func chat(ctx context.Context, baseURL, model string, messages []Message, onToken func(string)) (Message, error) {
	req := ChatRequest{
		Model:           model,
		Messages:        messages,
		Tools:           currentTools(),
		Stream:          true,
		MaxTokens:       maxGenTokens,
		ReasoningEffort: currentReasoningEffort(),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Message{}, err
	}
	start := time.Now()
	var firstByte time.Time
	promptTok := estimateTokens(messages)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	setAuth(httpReq)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// Non-streaming error body (bad request, model not found, ...).
		var e struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			return Message{}, fmt.Errorf("server: %s", e.Error.Message)
		}
		return Message{}, fmt.Errorf("server returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	onFirstByte = func() { firstByte = time.Now() } // any delta, incl. reasoning
	defer func() { onFirstByte = nil }()
	msg, finish, think, err := parseSSE(resp.Body, onToken)
	lastFinishReason = finish
	// Record throughput even on error/interrupt — partial requests cost real
	// time too. TTFB defaults to total when no byte ever arrived.
	end := time.Now()
	if firstByte.IsZero() {
		firstByte = end
	}
	genTok := (len(msg.Content) + toolCallChars(msg)) / 4
	thinkTok := think.chars / 4
	stats.record(promptTok, genTok, thinkTok, firstByte.Sub(start), end.Sub(start))
	if statsTrace {
		// Per-request line (eval mode): the KV-cache diagnostic. With prefix
		// reuse working, ttfb collapses after request 1 of a turn because
		// only the new suffix processes; a flat-high ttfb on every request
		// means the server is reprocessing the whole prompt each time.
		emitStats(fmt.Sprintf("    req: ~%dk prompt · ttfb %s · think %d tok · gen %d tok · total %s",
			promptTok/1000, firstByte.Sub(start).Round(100*time.Millisecond),
			thinkTok, genTok, end.Sub(start).Round(100*time.Millisecond)),
			map[string]string{
				"prompt_k": fmt.Sprintf("%d", promptTok/1000),
				"think":    fmt.Sprintf("%d", thinkTok),
				"gen":      fmt.Sprintf("%d", genTok),
			})
	}
	if err != nil {
		if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 && think.chars > 0 {
			msg.Content = "(interrupted while reasoning; the model's last thoughts:)\n..." + think.tail
		}
		return msg, err
	}
	if finish == "length" {
		emitLine("\n  (response truncated at the generation cap — reasoning models can spend the whole budget thinking; see max_tokens in config)")
	}
	// Reasoning models sometimes put their entire answer in the reasoning
	// stream and emit nothing else. An empty reply would end the turn in
	// silence; surface the thinking tail so the user (and the transcript)
	// see what the model concluded.
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 && think.chars > 0 {
		msg.Content = "(model produced only internal reasoning; its final thoughts:)\n..." + think.tail
	}
	return msg, nil
}

func toolCallChars(m Message) int {
	n := 0
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// parseSSE consumes an OpenAI-style server-sent-events stream, invoking
// onToken for each content fragment and assembling tool-call deltas
// (which arrive as indexed fragments) into complete ToolCalls.
// onToolProgress, when set, receives the tool name and accumulated argument
// size as a tool call streams in. Tool-call deltas never reach onToken, so
// without this a model generating a huge (or runaway) call looks like a
// silent, motionless spinner — an eval run burned 15 invisible minutes that
// way before the request timeout killed it.
var onToolProgress func(name string, chars int)

// onReasoning, when set, receives accumulated reasoning size (chars) as the
// model thinks. Reasoning tokens are generated compute like any other — a
// captured SSE stream from gemma-4-12b showed 262 reasoning deltas to 143
// content deltas, i.e. ~2/3 of all generation was invisible to every
// counter, spinner, and transcript before this existed.
var onReasoning func(chars int)

// onFirstByte, when set, fires once on the first delta of ANY kind. TTFB
// used to be stamped on the first CONTENT token — but agent turns are mostly
// tool-call and reasoning deltas with zero content, so entire generations
// were misattributed to "prompt processing" in /stats.
var onFirstByte func()

// reasoningStats carries what the model thought: total size, and the tail —
// kept because reasoning models sometimes put their entire ANSWER in the
// reasoning stream and emit no content at all (observed: a final assistant
// message with 0 chars and no tool calls after minutes of thinking).
type reasoningStats struct {
	chars int
	tail  string // last ~400 chars of reasoning
}

func parseSSE(r io.Reader, onToken func(string)) (Message, string, reasoningStats, error) {
	type deltaToolCall struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunk struct {
		Choices []struct {
			Delta struct {
				Content          string          `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
				ToolCalls        []deltaToolCall `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	msg := Message{Role: "assistant"}
	var calls []ToolCall
	var think reasoningStats
	finish := ""

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(payload), &c); err != nil {
			continue // tolerate keep-alives / unknown frames
		}
		if c.Error != nil {
			return msg, finish, think, fmt.Errorf("server: %s", c.Error.Message)
		}
		if len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if ch.Delta.Content != "" || ch.Delta.ReasoningContent != "" || ch.Delta.Reasoning != "" || len(ch.Delta.ToolCalls) > 0 {
			if onFirstByte != nil {
				onFirstByte()
				onFirstByte = nil // once per parse
			}
		}
		if rc := ch.Delta.ReasoningContent + ch.Delta.Reasoning; rc != "" {
			think.chars += len(rc)
			think.tail += rc
			if len(think.tail) > 400 {
				think.tail = think.tail[len(think.tail)-400:]
			}
			if onReasoning != nil {
				onReasoning(think.chars)
			}
		}
		if ch.Delta.Content != "" {
			msg.Content += ch.Delta.Content
			if onToken != nil {
				onToken(ch.Delta.Content)
			}
		}
		for _, d := range ch.Delta.ToolCalls {
			for len(calls) <= d.Index {
				calls = append(calls, ToolCall{Type: "function"})
			}
			tc := &calls[d.Index]
			if d.ID != "" {
				tc.ID = d.ID
			}
			if d.Type != "" {
				tc.Type = d.Type
			}
			if d.Function.Name != "" {
				tc.Function.Name += d.Function.Name
			}
			tc.Function.Arguments += d.Function.Arguments
			if onToolProgress != nil {
				onToolProgress(tc.Function.Name, len(tc.Function.Arguments))
			}
		}
		if ch.FinishReason != "" {
			finish = ch.FinishReason
		}
	}
	if len(calls) > 0 {
		msg.ToolCalls = calls
	}
	return msg, finish, think, sc.Err()
}

// firstModel asks GET /v1/models and returns the first chat-capable model,
// skipping embedding models (LM Studio always lists its built-in embedder).
// firstModel asks the server for its model list and picks the first one
// that doesn't look like an embedding model — the sane default when no
// -model flag or config entry names one.
func firstModel(baseURL string) (string, error) {
	resp, err := getWithAuth(baseURL + "/v1/models")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("server reports no models loaded")
	}
	for _, m := range out.Data {
		if !strings.Contains(strings.ToLower(m.ID), "embed") {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf("only embedding models loaded — load a chat model in LM Studio")
}

// getWithAuth issues a GET with the Bearer header attached when configured,
// so model discovery works against authenticated endpoints too.
func getWithAuth(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	setAuth(req)
	return httpClient.Do(req)
}

// listModels returns every model id the server offers (for /model).
func listModels(baseURL string) ([]string, error) {
	resp, err := getWithAuth(baseURL + "/v1/models")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range out.Data {
		if !strings.Contains(strings.ToLower(m.ID), "embed") {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// firstSentence trims a tool description to its first sentence for /tools.
// firstSentence trims a tool description for the /tools listing.
func firstSentence(s string) string {
	if i := strings.IndexByte(s, '.'); i != -1 {
		return s[:i+1]
	}
	return s
}
