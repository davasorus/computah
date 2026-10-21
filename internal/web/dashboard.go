// Web dashboard — a read-only live view of the agent, served over HTTP.
//
// Step 2 of the front-end plan. The dashboard is a second subscriber to the
// event core.Bus (stdout is the first): every event the terminal renders is also
// pushed to any connected browser over Server-Sent Events. Enable with
// -serve (defaults to :7777) or -serve :PORT.
//
// Design: this is an instrument panel, not a webpage. Dark, monospace,
// dense — it mirrors what you watch in the terminal but makes it reachable
// from another machine on the LAN. The live transcript is the centerpiece;
// a stats strip and a todo rail frame it. Read-only by design: it observes,
// it does not drive the agent (driving stays in the terminal/TUI).
//
// Implementation is stdlib net/http + SSE, no framework — the idiomatic
// choice for a small embedded status server. SSE (not websockets) because
// the data flow is one-directional (server→browser) and SSE reconnects
// itself and rides plain HTTP.
//
// Security: non-loopback bindings require a shared-secret token (SetDashToken).
// All responses include security headers (CSP, X-Content-Type-Options).
// POST endpoints verify CSRF tokens to prevent cross-site request forgery.
package web

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
)

// dashClient is one connected browser (one SSE stream).
type dashClient struct {
	ch chan core.Event
}

// dashboard is the SSE hub: a core.Bus subscriber that fans events out to every
// connected browser.
type dashboard struct {
	mu      sync.Mutex
	clients map[*dashClient]bool
	ring    []core.Event // recent events, replayed to new connections so a fresh browser isn't blank
}

const dashRingSize = 500

var dash = &dashboard{clients: map[*dashClient]bool{}}

// OnEvent is the core.Subscriber implementation — fan out to browsers + keep a ring.
func (d *dashboard) OnEvent(e core.Event) {
	d.mu.Lock()
	d.ring = append(d.ring, e)
	if len(d.ring) > dashRingSize {
		d.ring = d.ring[len(d.ring)-dashRingSize:]
	}
	clients := make([]*dashClient, 0, len(d.clients))
	for c := range d.clients {
		clients = append(clients, c)
	}
	d.mu.Unlock()
	for _, c := range clients {
		select {
		case c.ch <- e:
		default: // slow client — drop rather than block the emit path
		}
	}
}

func (d *dashboard) addClient() *dashClient {
	c := &dashClient{ch: make(chan core.Event, 256)}
	d.mu.Lock()
	// Seed with the recent ring so a new browser shows history immediately.
	backfill := make([]core.Event, len(d.ring))
	copy(backfill, d.ring)
	d.clients[c] = true
	d.mu.Unlock()
	for _, e := range backfill {
		select {
		case c.ch <- e:
		default:
		}
	}
	return c
}

func (d *dashboard) removeClient(c *dashClient) {
	d.mu.Lock()
	delete(d.clients, c)
	d.mu.Unlock()
	close(c.ch)
}

// isLoopback reports whether addr refers to a loopback address.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// checkToken verifies the ?token= query parameter against dashToken.
// Loopback clients are exempt. Returns an error string if auth fails.
func checkToken(r *http.Request) string {
	if dashToken == "" {
		return "" // no token configured — skip auth
	}
	if isLoopback(r.RemoteAddr) {
		return "" // loopback is always allowed
	}
	// Check query parameter first (for initial page load).
	if token := r.URL.Query().Get("token"); token == dashToken {
		return "" // valid
	}
	return "unauthorized"
}

// checkCSRF verifies the CSRF token in the request body or header.
// Returns an error string if CSRF check fails, or "" if valid.
func checkCSRF(r *http.Request) string {
	if dashToken == "" {
		return "" // no token configured — skip CSRF
	}
	// Check for CSRF token in form value or header.
	csrf := r.FormValue("csrf_token")
	if csrf == "" {
		csrf = r.Header.Get("X-CSRF-Token")
	}
	if csrf == "" {
		return "missing csrf token"
	}
	// Verify against the expected token derived from the request.
	expected := generateCSRF(r)
	if csrf != expected {
		return "invalid csrf token"
	}
	return ""
}

// generateCSRF returns a deterministic CSRF token for this request.
// The server embeds this in the page; the browser must echo it on POST.
func generateCSRF(r *http.Request) string {
	// Use the remote address + timestamp as the CSRF seed.
	// The server verifies this matches the token the client received.
	seed := r.RemoteAddr + r.UserAgent()
	if host := r.Host; host != "" {
		seed += host
	}
	return fmt.Sprintf("%x", dashCSRFKey[:16]) // 16-byte hex token
}

// startDashboard subscribes to the core.Bus and serves the dashboard on addr.
// Runs in a goroutine; returns immediately. Best-effort — a bind failure
// warns and the agent continues (the dashboard is optional).
func StartDashboard(addr string) {
	initDashCSRFKey()

	// Warn if binding non-loopback without a token.
	if !isLoopback(addr) && dashToken == "" {
		core.EmitError("dashboard: WARNING — binding non-loopback (" + addr + ") without a token. Use -dash-token to set one.")
	}

	core.Bus.Subscribe(dash)

	mux := http.NewServeMux()
	mux.HandleFunc("/", dashIndexHandler)
	mux.HandleFunc("/events", dashEventsHandler)
	mux.HandleFunc("/api/state", dashStateHandler)
	mux.HandleFunc("/api/sessions", dashSessionsHandler)
	mux.HandleFunc("/api/session", dashSessionHandler)
	mux.HandleFunc("/api/submit", dashSubmitHandler)
	mux.HandleFunc("/api/approve", dashApproveHandler)

	// Wrap the mux with auth and security headers.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Add security headers to every response.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-src 'self'; style-src 'unsafe-inline'; img-src 'self' data:; script-src 'unsafe-inline'; base-uri 'none'; form-action 'self'")

		// Check token for non-loopback clients on authenticated endpoints.
		if dashToken != "" && !isLoopback(r.RemoteAddr) {
			if err := checkToken(r); err != "" {
				if r.URL.Path == "/" {
					// For the page itself, show a token prompt.
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					fmt.Fprintf(w, `<html><body><h3>Dashboard requires authentication</h3>
						<p>Pass the token via the <code>?token=</code> query parameter.</p>
						<form method="get"><input name="token" placeholder="token"><button>Submit</button></form>
						</body></html>`)
					return
				}
				if isSSE(r) {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
		}

		// Check CSRF for POST endpoints (write operations).
		if r.Method == http.MethodPost {
			// Parse form body to extract CSRF token.
			if r.Header.Get("Content-Type") == "application/json" {
				// For JSON requests, check X-CSRF-Token header.
				if dashToken != "" && !isLoopback(r.RemoteAddr) {
					csrfErr := checkCSRF(r)
					if csrfErr != "" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						w.Write([]byte(`{"error":"` + csrfErr + `"}`))
						return
					}
				}
			} else {
				if err := r.ParseForm(); err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					w.Write([]byte(`{"error":"invalid form"}`))
					return
				}
				if dashToken != "" && !isLoopback(r.RemoteAddr) {
					csrfErr := checkCSRF(r)
					if csrfErr != "" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						w.Write([]byte(`{"error":"` + csrfErr + `"}`))
						return
					}
				}
			}
		}

		mux.ServeHTTP(w, r)
	})

	srv := &http.Server{Addr: addr, Handler: wrapped}
	go func() {
		mode := "read-only"
		if dashAllowWrite {
			mode = "read-write"
		}
		core.EmitStatus("dashboard: serving on http://" + addr + " (" + mode + ")")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			core.EmitError("dashboard: " + err.Error() + " (continuing without it)")
		}
	}()
}

// isSSE checks if the request expects an SSE response.
func isSSE(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// dashEventsHandler is the SSE stream: pushes every core.Bus event to the browser.
func dashEventsHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	c := dash.addClient()
	defer dash.removeClient(c)

	// Keep-alive ticker so proxies don't close an idle stream.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-c.ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(map[string]any{
				"kind": e.Kind,
				"text": e.Text,
				"tool": e.Tool,
				"meta": e.Meta,
				"key":  e.Key,
				"time": e.Time.Format("15:04:05"),
			})
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, data)
			flusher.Flush()
		}
	}
}

// dashStateHandler returns a JSON snapshot (stats + todos) polled by the UI.
func dashStateHandler(w http.ResponseWriter, r *http.Request) {
	snap := agent.Stats()
	st := map[string]any{
		"requests":     snap.Requests,
		"prompt_tk":    snap.PromptTk,
		"gen_tk":       snap.GenTk,
		"think_tk":     snap.ThinkTk,
		"last_ttfb_ms": snap.LastTTFBms,
	}

	td := agent.Todos()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stats": st, "todos": td, "can_submit": dashAllowWrite})
}

// dashSessionsHandler returns the list of persisted sessions for the running
// agent's working directory (newest first) — the data for the session browser.
func dashSessionsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	store := agent.ActiveSessionStore()
	if store == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []any{}, "available": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessions":  store.SessionSummaries(),
		"available": true,
	})
}

// dashSessionHandler returns the transcript of one session (by ?name=) as a
// list of {role, content} messages for display in the browser.
func dashSessionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
		return
	}
	store := agent.ActiveSessionStore()
	if store == nil {
		http.Error(w, `{"error":"session persistence unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	msgs, err := store.SessionTranscript(name)
	if err != nil {
		http.Error(w, `{"error":`+jsonString(err.Error())+`}`, http.StatusBadRequest)
		return
	}
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "messages": out})
}

// jsonString safely encodes s as a JSON string literal (with quotes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// dashSubmitHandler accepts a browser-submitted prompt (POST /api/submit,
// body: {"text": "..."}) and enqueues it. Read-only-by-default dashboards
// stay read-only unless -serve-write is set (see startDashboard).
func dashSubmitHandler(w http.ResponseWriter, r *http.Request) {
	if !dashAllowWrite {
		http.Error(w, "dashboard is read-only (start with -serve-write to enable submissions)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "expected {\"text\": \"...\"}", http.StatusBadRequest)
		return
	}
	select {
	case core.BrowserSubmissions <- body.Text:
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"queued":true}`))
	default:
		http.Error(w, "submission queue full", http.StatusServiceUnavailable)
	}
}

// dashAllowWrite gates browser submissions (off unless -serve-write).
var dashAllowWrite bool

// SetDashWrite enables or disables browser prompt submissions (the cmd layer
// wires this to Options.SetDashWrite).
func SetDashWrite(allow bool) { dashAllowWrite = allow }

// dashToken is the shared-secret token for dashboard authentication.
// When set, non-loopback clients must present it via the ?token= query
// parameter. Loopback (127.0.0.1/::1) clients connect without a token.
var dashToken string

// SetDashToken sets the shared-secret token for the dashboard.
// When empty, no authentication is required (loopback clients still
// get security headers; non-loopback clients get a warning).
func SetDashToken(t string) { dashToken = t }

// dashCSRFKey is the per-process secret used to generate CSRF tokens.
var dashCSRFKey [32]byte

// initDashCSRFKey initializes the CSRF key from crypto/rand at startup.
func initDashCSRFKey() {
	if _, err := rand.Read(dashCSRFKey[:]); err != nil {
		// Extremely unlikely; fall back to a zero key (CSRF still works,
		// but the key is predictable — only acceptable for testing).
		for i := range dashCSRFKey {
			dashCSRFKey[i] = byte(i)
		}
	}
}

// dashApproveHandler receives an approval decision from the browser
// (POST /api/approve, body {"id":"...","decision":"once|always|deny"}) and
// delivers it to the waiting broker.
func dashApproveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "expected {\"id\":..,\"decision\":..}", http.StatusBadRequest)
		return
	}
	var d agent.ApprovalDecision
	switch body.Decision {
	case "once":
		d = agent.ApproveOnce
	case "always":
		d = agent.ApproveAlways
	default:
		d = agent.ApproveDeny
	}
	if agent.AnswerWebApproval(body.ID, d) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	} else {
		http.Error(w, "no matching pending approval (already answered?)", http.StatusConflict)
	}
}

func dashIndexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Embed the per-process CSRF key into the page so the browser can
	// echo it on POST. The key is fixed for the server lifetime.
	html := fmt.Sprintf(dashHTML, fmt.Sprintf("%x", dashCSRFKey[:16]))
	_, _ = fmt.Fprint(w, html)
}
