package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/davasorus/computah/internal/agent"
)

func TestDashSessionsHandler_NoStore(t *testing.T) {
	agent.SetActiveSessionStore(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	dashSessionsHandler(w, req)
	var got map[string]any
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got["available"] != false {
		t.Errorf("expected available=false with no store, got %v", got["available"])
	}
}

func TestDashSessionsHandler_WithStore(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	st := agent.NewSessionStore("/wd")
	st.Append([]agent.Message{{Role: "user", Content: "hello there"}})
	st.Rotate()
	agent.SetActiveSessionStore(st)
	defer agent.SetActiveSessionStore(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	dashSessionsHandler(w, req)
	var got struct {
		Available bool                     `json:"available"`
		Sessions  []map[string]interface{} `json:"sessions"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if !got.Available || len(got.Sessions) != 1 {
		t.Fatalf("expected 1 available session, got %+v", got)
	}
}

func TestDashSessionHandler_MissingName(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	w := httptest.NewRecorder()
	dashSessionHandler(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing name, got %d", w.Code)
	}
}
