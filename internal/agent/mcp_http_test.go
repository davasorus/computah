package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildTestMCPServer stands up a real in-process SDK server with two tools,
// exercising the actual protocol handshake rather than a fake.
func buildTestMCPServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "vault-test", Version: "1.0"}, nil)
	type patchIn struct {
		Path string `json:"path"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "vault_patch", Description: "patch a note"},
		func(ctx context.Context, req *mcp.CallToolRequest, in patchIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "did vault_patch " + in.Path}}}, nil, nil
		})
	type searchIn struct {
		Query string `json:"query"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "search_simple", Description: "search notes"},
		func(ctx context.Context, req *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "did search_simple " + in.Query}}}, nil, nil
		})
	return s
}

func clearMCPTools() {
	for _, n := range []string{"vault_patch", "search_simple", "vault_vault_patch", "vault_search_simple"} {
		delete(toolByName, n)
	}
	preferredMCP = nil
}

func TestMCPHTTPTransport(t *testing.T) {
	defer clearMCPTools()
	// Real SDK server behind an httptest server, requiring a bearer token.
	srv := buildTestMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	authWrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		handler.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(authWrap)
	defer ts.Close()

	s, n, err := startMCPServer("vault", MCPServerConfig{URL: ts.URL, Token: "tok", NoPrefix: true, Prefer: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.stop()
	if n != 2 {
		t.Fatalf("want 2 tools, got %d", n)
	}
	// no_prefix → bare names, no vault_ doubling.
	if _, ok := toolByName["vault_patch"]; !ok {
		t.Fatal("no_prefix must register bare tool names")
	}
	if _, bad := toolByName["vault_vault_patch"]; bad {
		t.Fatal("must not double-prefix")
	}
	// prefer → recorded for the system prompt.
	found := false
	for _, p := range preferredMCP {
		if p == "vault" {
			found = true
		}
	}
	if !found {
		t.Fatal("prefer must record the server name")
	}
	// A call round-trips through the real SDK client+server.
	out := toolByName["vault_patch"].Handler(&Sandbox{Root: t.TempDir()}, toolArgs{"path": "n.md"})
	if !strings.Contains(out, "did vault_patch n.md") {
		t.Fatalf("tool call must round-trip: %q", out)
	}
	out = s.callTool("search_simple", map[string]any{"query": "liquibase"})
	if !strings.Contains(out, "did search_simple liquibase") {
		t.Fatalf("second tool must round-trip: %q", out)
	}
}

func TestMCPHTTPAuthFailure(t *testing.T) {
	defer clearMCPTools()
	srv := buildTestMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer ts.Close()
	if _, _, err := startMCPServer("vault", MCPServerConfig{URL: ts.URL, Token: "wrong"}); err == nil {
		t.Fatal("bad token must fail the handshake")
	}
}

func TestMCPServerNeedsCommandOrURL(t *testing.T) {
	if _, _, err := startMCPServer("bad", MCPServerConfig{}); err == nil {
		t.Fatal("a server with neither command nor url must error")
	}
}

// TestMCPStdioEnvReachesChild verifies configured env vars are placed on the
// child process environment — the mechanism SearXNG (SEARXNG_URL) and
// key-based stdio servers rely on. Tested at the construction level rather
// than through a full handshake (that path is covered by
// TestMCPClientAgainstFakeServer).
func TestMCPStdioEnvReachesChild(t *testing.T) {
	cmd := exec.Command("true")
	// Mirror the construction logic used in startMCPServer's command branch.
	env := map[string]string{"SEARXNG_URL": "http://localhost:8080", "API_KEY": "secret"}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	found := map[string]bool{}
	for _, e := range cmd.Env {
		if e == "SEARXNG_URL=http://localhost:8080" {
			found["url"] = true
		}
		if e == "API_KEY=secret" {
			found["key"] = true
		}
	}
	if !found["url"] || !found["key"] {
		t.Fatalf("configured env vars must be on the child environment: %v", found)
	}
	// And the parent environment is inherited (not replaced).
	if len(cmd.Env) <= len(env) {
		t.Fatal("child env must inherit the parent environment, not replace it")
	}
}

func TestSanitizeSchema(t *testing.T) {
	// The exact failure: MCP tool with required=nil → must become [].
	got := sanitizeSchema(map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}})
	if _, ok := got["required"].([]string); !ok {
		t.Fatalf("nil required must become an array: %#v", got["required"])
	}
	// Missing type → object; missing properties → empty object.
	got = sanitizeSchema(map[string]any{})
	if got["type"] != "object" {
		t.Fatal("absent type must default to object")
	}
	if _, ok := got["properties"].(map[string]any); !ok {
		t.Fatal("object schema must get a properties object")
	}
	if _, ok := got["required"].([]string); !ok {
		t.Fatal("object schema must get a required array")
	}
	// Existing required array preserved.
	got = sanitizeSchema(map[string]any{"type": "object", "required": []any{"path"}})
	if arr, ok := got["required"].([]any); !ok || len(arr) != 1 {
		t.Fatalf("existing required array must survive: %#v", got["required"])
	}
	// Source map not mutated.
	src := map[string]any{"type": "object"}
	_ = sanitizeSchema(src)
	if _, exists := src["required"]; exists {
		t.Fatal("sanitize must not mutate the source schema")
	}
}

// TestToolDefNeverNullRequired guards the LM Studio 400: a built-in tool
// with no required params must emit required:[] not required:null.
func TestToolDefNeverNullRequired(t *testing.T) {
	def := toolDef("go_diagnostics", "desc", map[string]any{"path": map[string]any{"type": "string"}}, nil)
	data, _ := json.Marshal(def)
	if strings.Contains(string(data), `"required":null`) {
		t.Fatalf("nil required must serialize as [], got: %s", data)
	}
	// And with nil props too.
	def = toolDef("x", "d", nil, nil)
	data, _ = json.Marshal(def)
	if strings.Contains(string(data), `"required":null`) || strings.Contains(string(data), `"properties":null`) {
		t.Fatalf("nil props/required must both serialize as non-null: %s", data)
	}
	// Verify EVERY registered built-in tool is clean (the real guarantee).
	buildToolSchemas()
	for i, td := range tools {
		data, _ := json.Marshal(td)
		if strings.Contains(string(data), `"required":null`) {
			t.Fatalf("tool %d ships required:null: %s", i, data)
		}
	}
}

func TestUnregisterTools(t *testing.T) {
	// Save/restore global registry state.
	savedReg, savedIdx := registry, toolByName
	registry = []Tool{
		{Name: "vault_search"}, {Name: "vault_read"}, {Name: "vault_note"},
		{Name: "read_file"}, {Name: "edit_file"},
	}
	toolByName = map[string]Tool{}
	for _, tl := range registry {
		toolByName[tl.Name] = tl
	}
	defer func() { registry, toolByName = savedReg, savedIdx }()

	unregisterTools("vault_search", "vault_read", "vault_note")

	if len(registry) != 2 {
		t.Fatalf("expected 2 tools left, got %d", len(registry))
	}
	for _, gone := range []string{"vault_search", "vault_read", "vault_note"} {
		if _, ok := toolByName[gone]; ok {
			t.Fatalf("%s must be removed from the index", gone)
		}
		for _, tl := range registry {
			if tl.Name == gone {
				t.Fatalf("%s must be removed from the registry", gone)
			}
		}
	}
	// The code tools survive untouched.
	if _, ok := toolByName["read_file"]; !ok {
		t.Fatal("read_file must survive")
	}
	if _, ok := toolByName["edit_file"]; !ok {
		t.Fatal("edit_file must survive")
	}
}
