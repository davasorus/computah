package agent

import (
	"os"
	"testing"
)

// TestSessionSummariesAndTranscript exercises the exported session-browsing
// API the dashboard uses, against real JSONL files written via Append.
func TestSessionSummariesAndTranscript(t *testing.T) {
	// Point HOME at a temp dir so NewSessionStore writes there.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	st := NewSessionStore("/some/workdir")
	if st.dir == "" {
		t.Fatal("expected a working session store")
	}

	// Write a session with a user + assistant message.
	msgs := []Message{
		{Role: "user", Content: "add a health endpoint"},
		{Role: "assistant", Content: "Done — added /healthz."},
	}
	st.Append(msgs)
	st.Rotate() // close the file so it's enumerable as a finished session

	sums := st.SessionSummaries()
	if len(sums) != 1 {
		t.Fatalf("expected 1 session summary, got %d", len(sums))
	}
	s := sums[0]
	if !s.Latest {
		t.Error("only session should be marked latest")
	}
	if s.Messages != 2 {
		t.Errorf("expected 2 messages, got %d", s.Messages)
	}
	if s.Title != "add a health endpoint" {
		t.Errorf("title should be the first user prompt, got %q", s.Title)
	}

	// Load the transcript back.
	tr, err := st.SessionTranscript(s.Name)
	if err != nil {
		t.Fatalf("transcript load failed: %v", err)
	}
	if len(tr) != 2 || tr[0].Content != "add a health endpoint" {
		t.Errorf("transcript mismatch: %+v", tr)
	}

	// Path-escape attempts must be rejected.
	for _, bad := range []string{"../evil", "sub/dir", "..\\win"} {
		if _, err := st.SessionTranscript(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}

	_ = os.RemoveAll(tmp)
}

// TestSessionSummariesDisabledStore returns nil safely when persistence is off.
func TestSessionSummariesDisabledStore(t *testing.T) {
	st := &SessionStore{} // dir == "" → disabled
	if got := st.SessionSummaries(); got != nil {
		t.Errorf("disabled store should return nil, got %v", got)
	}
	if _, err := st.SessionTranscript("x"); err == nil {
		t.Error("disabled store transcript should error")
	}
}
