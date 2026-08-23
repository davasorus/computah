package agent

import (
	"testing"
	"time"
)

func TestApprovalWebRoundTrip(t *testing.T) {
	b := &approvalBrokerT{mode: approvalWeb}
	// Simulate the browser answering shortly after the request blocks.
	var got approvalDecision
	done := make(chan struct{})
	go func() {
		got = b.request("run this? ", "ls")
		close(done)
	}()
	// Wait for the pending approval to register, then answer it.
	var id string
	for i := 0; i < 100; i++ {
		b.mu.Lock()
		if b.pending != nil {
			id = b.pending.id
		}
		b.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("web approval never registered a pending request")
	}
	if !b.answerWeb(id, approveAlways) {
		t.Fatal("answerWeb should succeed for the pending id")
	}
	<-done
	if got != approveAlways {
		t.Fatalf("decision not delivered: got %d", got)
	}
	// A second answer to the same id is a no-op (already cleared).
	if b.answerWeb(id, approveOnce) {
		t.Fatal("stale approval answer should be rejected")
	}
}

func TestApprovalWebStaleIdRejected(t *testing.T) {
	b := &approvalBrokerT{mode: approvalWeb}
	if b.answerWeb("nonexistent", approveOnce) {
		t.Fatal("answering a nonexistent approval must return false")
	}
}

func TestNormalizeApproval(t *testing.T) {
	cases := map[string]string{"y": "y", "yes": "y", "Y": "y", "a": "a", "ALWAYS": "a", "n": "n", "": "n", "garbage": "n"}
	for in, want := range cases {
		if got := normalizeApproval(in); got != want {
			t.Fatalf("normalizeApproval(%q)=%q want %q", in, got, want)
		}
	}
}

func TestApprovalModeDefault(t *testing.T) {
	b := &approvalBrokerT{}
	if b.mode != approvalTerminal {
		t.Fatal("default approval mode must be terminal")
	}
}
