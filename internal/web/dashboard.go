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
package web

import (
	"encoding/json"
	"fmt"
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

// startDashboard subscribes to the core.Bus and serves the dashboard on addr.
// Runs in a goroutine; returns immediately. Best-effort — a bind failure
// warns and the agent continues (the dashboard is optional).
func StartDashboard(addr string) {
	core.Bus.Subscribe(dash)

	mux := http.NewServeMux()
	mux.HandleFunc("/", dashIndexHandler)
	mux.HandleFunc("/events", dashEventsHandler)
	mux.HandleFunc("/api/state", dashStateHandler)
	mux.HandleFunc("/api/sessions", dashSessionsHandler)
	mux.HandleFunc("/api/session", dashSessionHandler)
	mux.HandleFunc("/api/submit", dashSubmitHandler)
	mux.HandleFunc("/api/approve", dashApproveHandler)

	srv := &http.Server{Addr: addr, Handler: mux}
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
	_, _ = fmt.Fprint(w, dashHTML)
}

// SetDashWrite enables or disables browser prompt submissions (the cmd layer
// wires this to Options.SetDashWrite).
func SetDashWrite(allow bool) { dashAllowWrite = allow }
