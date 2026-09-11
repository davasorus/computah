package agent

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestStreamChatFatalStopsAfterOneAttempt confirms a fatal classification
// (HTTP 400 → ErrKindClient, Retryable: false) short-circuits streamChat's
// retry loop instead of waiting out the old flat 1s-sleep-then-retry for
// every failure alike.
func TestStreamChatFatalStopsAfterOneAttempt(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer ts.Close()

	_, intr, err := streamChat(ts.URL, "test-model", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if intr {
		t.Fatal("must not report interrupted")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("fatal error must not retry: got %d attempts, want 1", got)
	}
}

// TestStreamChatRetryableTriesTwice confirms a retryable classification
// (HTTP 500 → ErrKindServer, Retryable: true) still gets the one retry.
func TestStreamChatRetryableTriesTwice(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer ts.Close()

	_, intr, err := streamChat(ts.URL, "test-model", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if intr {
		t.Fatal("must not report interrupted")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("retryable error must retry once: got %d attempts, want 2", got)
	}
}

// TestStreamChatRespectsConfiguredRetryAttempts confirms chatRetryAttempts
// (set from Config "chat_retry_attempts" in main.go) actually drives the
// retry count, not a hardcoded literal.
func TestStreamChatRespectsConfiguredRetryAttempts(t *testing.T) {
	old := chatRetryAttempts
	chatRetryAttempts = 3
	defer func() { chatRetryAttempts = old }()

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer ts.Close()

	_, _, err := streamChat(ts.URL, "test-model", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("chatRetryAttempts=3 must yield 3 attempts: got %d", got)
	}
}

// TestRunVerifyLoopRespectsConfiguredMaxFixAttempts confirms maxFixAttempts
// (set from Config "max_fix_attempts" in main.go) actually bounds the
// verify/fix cycle count, not a hardcoded literal.
func TestRunVerifyLoopRespectsConfiguredMaxFixAttempts(t *testing.T) {
	oldMax, oldVerify, oldRunTurn := maxFixAttempts, verifyCommand, runTurn
	defer func() { maxFixAttempts, verifyCommand, runTurn = oldMax, oldVerify, oldRunTurn }()

	maxFixAttempts = 3
	verifyCommand = "false" // always fails, forcing the loop to keep retrying
	var fixAttempts int32
	runTurn = func(baseURL, model string, sb *Sandbox, st *SessionStore, messages []Message) []Message {
		atomic.AddInt32(&fixAttempts, 1)
		sb.Modified = append(sb.Modified, "f.go") // keep the loop from stopping on "no files changed"
		return messages
	}

	sb := &Sandbox{Root: t.TempDir(), Modified: []string{"f.go"}}
	st := &SessionStore{}
	RunVerifyLoop("", "test-model", sb, st, []Message{{Role: "user", Content: "hi"}}, 0)

	if got := atomic.LoadInt32(&fixAttempts); got != 3 {
		t.Fatalf("maxFixAttempts=3 must yield 3 fix attempts: got %d", got)
	}
}
