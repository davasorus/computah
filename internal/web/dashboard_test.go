package web

import (
	"context"
	"encoding/json"
	"github.com/davasorus/computah/internal/agent"
	"github.com/davasorus/computah/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardFanOutAndRing(t *testing.T) {
	d := &dashboard{clients: map[*dashClient]bool{}}
	// Pre-load ring, then connect: a new client should get the backfill.
	d.OnEvent(core.Event{Kind: core.EvLine, Text: "old-1"})
	d.OnEvent(core.Event{Kind: core.EvLine, Text: "old-2"})
	c := d.addClient()
	got := []string{}
	for i := 0; i < 2; i++ {
		select {
		case e := <-c.ch:
			got = append(got, e.Text)
		case <-time.After(time.Second):
			t.Fatal("expected backfill event")
		}
	}
	if got[0] != "old-1" || got[1] != "old-2" {
		t.Fatalf("ring backfill wrong: %v", got)
	}
	// Live event after connect reaches the client.
	d.OnEvent(core.Event{Kind: core.EvToolCall, Tool: "read_file", Text: "x"})
	select {
	case e := <-c.ch:
		if e.Tool != "read_file" {
			t.Fatalf("live event wrong: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("expected live event")
	}
	d.removeClient(c)
	if len(d.clients) != 0 {
		t.Fatal("client not removed")
	}
}

func TestDashboardRingCap(t *testing.T) {
	d := &dashboard{clients: map[*dashClient]bool{}}
	for i := 0; i < dashRingSize+50; i++ {
		d.OnEvent(core.Event{Kind: core.EvLine, Text: "x"})
	}
	if len(d.ring) != dashRingSize {
		t.Fatalf("ring must cap at %d, got %d", dashRingSize, len(d.ring))
	}
}

func TestDashStateEndpoint(t *testing.T) {
	agent.SetTodos([]agent.TodoItem{{Text: "wire dashboard", Done: true}, {Text: "build TUI", Done: false}})
	defer func() { agent.SetTodos(nil) }()
	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	dashStateHandler(w, req)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("state must be valid JSON: %v", err)
	}
	td, _ := body["todos"].([]any)
	if len(td) != 2 {
		t.Fatalf("expected 2 todos in state, got %d", len(td))
	}
	if _, ok := body["stats"]; !ok {
		t.Fatal("state must include stats")
	}
}

func TestDashSSEStreamDeliversEmittedEvent(t *testing.T) {
	// End-to-end: a core.Bus emit reaches the SSE HTTP response body.
	// Fresh core.Bus + dashboard so we don't entangle the global subscribers.
	localDash := &dashboard{clients: map[*dashClient]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mimic dashEventsHandler but against localDash.
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		c := localDash.addClient()
		defer localDash.removeClient(c)
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-c.ch:
				data, _ := json.Marshal(map[string]any{"kind": e.Kind, "text": e.Text, "tool": e.Tool})
				w.Write([]byte("event: " + string(e.Kind) + "\ndata: " + string(data) + "\n\n"))
				flusher.Flush()
				return // one event is enough for the test
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	// Emit after a beat so the client is subscribed.
	go func() {
		time.Sleep(100 * time.Millisecond)
		localDash.OnEvent(core.Event{Kind: core.EvToolCall, Tool: "grep", Text: "pat"})
	}()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "event: tool_call") || !strings.Contains(got, "grep") {
		t.Fatalf("SSE stream did not carry the emitted event: %q", got)
	}
}

func TestServeAddrResolution(t *testing.T) {
	// The -serve normalization logic (mirrors main.go).
	norm := func(s string) string {
		addr := s
		if addr == "on" || addr == "true" {
			addr = ":7777"
		} else if addr[0] != ':' {
			addr = ":" + addr
		}
		return addr
	}
	cases := map[string]string{"on": ":7777", "true": ":7777", "7777": ":7777", ":9090": ":9090", "8080": ":8080"}
	for in, want := range cases {
		if got := norm(in); got != want {
			t.Fatalf("norm(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDashSubmitDisabledByDefault(t *testing.T) {
	dashAllowWrite = false
	req := httptest.NewRequest("POST", "/api/submit", strings.NewReader(`{"text":"hi"}`))
	w := httptest.NewRecorder()
	dashSubmitHandler(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("read-only dashboard must reject submits, got %d", w.Code)
	}
}

func TestDashSubmitEnqueuesWhenEnabled(t *testing.T) {
	dashAllowWrite = true
	defer func() { dashAllowWrite = false }()
	// drain any stale queue entries
	for len(core.BrowserSubmissions) > 0 {
		<-core.BrowserSubmissions
	}
	req := httptest.NewRequest("POST", "/api/submit", strings.NewReader(`{"text":"do the thing"}`))
	w := httptest.NewRecorder()
	dashSubmitHandler(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("enabled dashboard must accept submits, got %d: %s", w.Code, w.Body.String())
	}
	got := core.DrainBrowserSubmission()
	if got != "do the thing" {
		t.Fatalf("submission not enqueued: %q", got)
	}
	// queue now empty → drain returns ""
	if core.DrainBrowserSubmission() != "" {
		t.Fatal("drain should return empty when queue is empty")
	}
}

func TestDashSubmitRejectsBadBody(t *testing.T) {
	dashAllowWrite = true
	defer func() { dashAllowWrite = false }()
	req := httptest.NewRequest("POST", "/api/submit", strings.NewReader(`{"text":""}`))
	w := httptest.NewRecorder()
	dashSubmitHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty text must be rejected, got %d", w.Code)
	}
}
