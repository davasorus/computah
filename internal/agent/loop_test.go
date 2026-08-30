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
