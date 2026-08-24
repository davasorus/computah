// MCP (Model Context Protocol) client — built on the official Go SDK
// (github.com/modelcontextprotocol/go-sdk/mcp).
//
// This used to be a hand-rolled JSON-RPC client. It now delegates to the
// canonical SDK, which tracks the spec (protocol version negotiation, both
// transports, session management, notifications, cancellation) so we don't
// chase it by hand. The config surface and registry behavior are unchanged:
// each configured server's tools/list result is registered alongside the
// built-ins, calls proxy through as tools/call, and the model can't tell an
// MCP tool from a native one.
//
// Config (~/.agent/config.json):
//
//	"mcp_servers": {
//	  "pg":    {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-postgres", "postgres://..."]},
//	  "engram": {"url": "http://localhost:8088/mcp/", "no_prefix": true, "prefer": true},
//	  "sbx":   {"command": "sandbox", "args": ["mcp", "-image", "python:3-alpine"], "prefer": true,
//	           "prefer_hint": "a locked-down, network-less sandbox. Run untrusted or unfamiliar code here (sbx_run_script / sbx_run_sandbox) before running it on the host."}
//	}
//
// Two transports: "command" (stdio child process) or "url" (Streamable
// HTTP). Tool names are prefixed with the server name (pg_query) unless
// "no_prefix" is set. "prefer" surfaces the server in the system prompt;
// "prefer_hint" overrides the default notes-server wording for non-notes
// servers (e.g. a sandbox).
package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPServerConfig struct {
	// stdio transport: a child process speaking JSON-RPC on stdin/stdout.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"` // extra environment for the child (e.g. SEARXNG_URL, API keys)
	// HTTP (Streamable HTTP) transport: a URL instead of a command.
	URL      string            `json:"url,omitempty"`
	Token    string            `json:"token,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Insecure bool              `json:"insecure,omitempty"`  // skip TLS verify (self-signed loopback certs)
	NoPrefix bool              `json:"no_prefix,omitempty"` // register tools under their own names
	Prefer   bool              `json:"prefer,omitempty"`    // steer the model to this server in the system prompt
	// PreferHint overrides the default (notes-server) steering text used when
	// Prefer is set, so a non-notes server (e.g. a code sandbox) can describe
	// how the model should use it. Empty = the default knowledge/notes wording.
	PreferHint string `json:"prefer_hint,omitempty"`
}

const (
	mcpCallTimeout = 60 * time.Second
	mcpInitTimeout = 20 * time.Second
)

// preferredMCP names servers the model should reach for first (config
// "prefer"); surfaced in the system prompt.
var preferredMCP []string

// preferHints maps a preferred server name to custom steering text (config
// "prefer_hint"); servers without a hint use the default notes-server wording.
var preferHints map[string]string

// preferSteering builds the system-prompt line(s) for preferred MCP servers.
// Servers with a custom prefer_hint get that text verbatim; the rest are
// grouped under the default knowledge/notes wording. Returns "" when no
// servers are preferred, so callers can append unconditionally.
func preferSteering() string {
	if len(preferredMCP) == 0 {
		return ""
	}
	var custom []string
	var defaults []string
	for _, name := range preferredMCP {
		if h, ok := preferHints[name]; ok {
			custom = append(custom, "\n\nThe \""+name+"\" server is connected: "+h)
		} else {
			defaults = append(defaults, name)
		}
	}
	var s string
	if len(defaults) > 0 {
		s += "\n\nA knowledge/notes server is connected as \"" + strings.Join(defaults, ", ") +
			"\": for anything involving the user's notes, knowledge base, decisions, or documentation — recording OR retrieving — " +
			"prefer its tools over writing files. Search it before assuming something isn't recorded; record durable decisions there when finishing significant work."
	}
	for _, c := range custom {
		s += c
	}
	return s
}

// builtinToolNames snapshots non-MCP tool names so no_prefix registration
// can avoid clobbering a built-in.
var builtinToolNames = map[string]bool{}

func snapshotBuiltinTools() {
	for name := range toolByName {
		builtinToolNames[name] = true
	}
}

// mcpServer wraps a live SDK client session.
type mcpServer struct {
	name    string
	session *mcp.ClientSession
}

// headerRoundTripper injects static headers (bearer token, custom) on every
// request — the SDK takes auth via a custom HTTPClient, not a header map.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// startMCPServers launches every configured server, registers its tools, and
// returns a shutdown func. Any individual failure warns and continues.
func startMCPServers(cfgs map[string]MCPServerConfig) func() {
	var servers []*mcpServer
	connected := map[string]bool{}
	for name, cfg := range cfgs {
		srv, toolCount, err := startMCPServer(name, cfg)
		if err != nil {
			fmt.Printf("mcp: %s: %v (continuing without it)\n", name, err)
			continue
		}
		servers = append(servers, srv)
		connected[name] = true
		fmt.Printf("mcp: %s connected — %d tool(s) registered\n", name, toolCount)
	}
	if len(servers) > 0 {
		buildToolSchemas()
	}
	return func() {
		for _, s := range servers {
			s.stop()
		}
	}
}

func startMCPServer(name string, cfg MCPServerConfig) (*mcpServer, int, error) {
	var transport mcp.Transport
	switch {
	case cfg.URL != "":
		headers := map[string]string{}
		for k, v := range cfg.Headers {
			headers[k] = v
		}
		if cfg.Token != "" {
			headers["Authorization"] = "Bearer " + cfg.Token
		}
		base := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.Insecure {
			base.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
		httpClient := &http.Client{
			Timeout:   mcpCallTimeout + 30*time.Second, // room for long SSE streams
			Transport: &headerRoundTripper{base: base, headers: headers},
		}
		transport = &mcp.StreamableClientTransport{
			Endpoint:   strings.TrimRight(cfg.URL, "/"),
			HTTPClient: httpClient,
			// Some MCP servers are request-response and don't properly serve
			// the optional standalone SSE GET stream; leaving it on caused the
			// session to tear down between calls ("Server not initialized" on
			// every tool call after connect). We don't need server-initiated
			// messages, so disable it.
			DisableStandaloneSSE: true,
		}
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range cfg.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		return nil, 0, fmt.Errorf("server needs either a command (stdio) or a url (http)")
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1.0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), mcpInitTimeout)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("connect: %w", err)
	}
	srv := &mcpServer{name: name, session: session}

	if cfg.Prefer {
		preferredMCP = append(preferredMCP, name)
		if strings.TrimSpace(cfg.PreferHint) != "" {
			if preferHints == nil {
				preferHints = map[string]string{}
			}
			preferHints[name] = strings.TrimSpace(cfg.PreferHint)
		}
	}

	// Discover and register tools.
	listCtx, listCancel := context.WithTimeout(context.Background(), mcpInitTimeout)
	defer listCancel()
	res, err := session.ListTools(listCtx, nil)
	if err != nil {
		srv.stop()
		return nil, 0, fmt.Errorf("tools/list: %w", err)
	}
	for _, t := range res.Tools {
		mcpTool := t.Name
		local := name + "_" + t.Name
		if cfg.NoPrefix {
			local = t.Name
			if builtinToolNames[local] { // never clobber a built-in
				local = name + "_" + t.Name
			}
		}
		desc := t.Description
		if desc == "" {
			desc = "MCP tool " + mcpTool + " from server " + name + "."
		}
		schema := toSchemaMap(t.InputSchema)
		registerTools(Tool{
			Name:      local,
			Desc:      desc,
			RawSchema: schema,
			Handler: func(s *Sandbox, a toolArgs) string {
				return srv.callTool(mcpTool, map[string]any(a))
			},
		})
	}
	return srv, len(res.Tools), nil
}

// callTool proxies a model tool call to the server and flattens the result's
// content into the plain string the loop expects.
func (s *mcpServer) callTool(tool string, args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
	defer cancel()
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "ERROR: " + err.Error()
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	out := b.String()
	if out == "" {
		out = "(no text content returned)"
	}
	if res.IsError {
		return "ERROR: " + out
	}
	return out
}

func (s *mcpServer) stop() {
	if s.session != nil {
		_ = s.session.Close()
	}
}

// toSchemaMap normalizes the SDK's InputSchema (typed as any; often a
// *jsonschema.Schema) into the map[string]any the registry sends the model.
func toSchemaMap(schema any) map[string]any {
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	if schema != nil {
		if data, err := json.Marshal(schema); err == nil {
			var m map[string]any
			if json.Unmarshal(data, &m) == nil && len(m) > 0 {
				return m
			}
		}
	}
	return map[string]any{"type": "object"}
}
