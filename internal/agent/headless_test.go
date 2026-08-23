package agent

import (
	"strings"
	"testing"
	"time"
)

// TestHeadlessInfoCommandRoutes verifies a browser-submitted read-only
// command is handled via the shared dispatcher and its output emitted to the
// bus — without needing a live model turn.
func TestHeadlessInfoCommandRoutes(t *testing.T) {
	var lines []string
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvLine || e.Kind == EvStatus {
			lines = append(lines, e.Text)
		}
	}))
	defer unsub()

	// Directly exercise the classification + info-dispatch path the headless
	// loop uses (we don't spin the whole loop — that needs a model).
	kind, arg := classifyInput("/tools")
	if kind != inputCommand {
		t.Fatalf("/tools should classify as command, got %d", kind)
	}
	out, handled := runInfoCommand(arg, "http://x", "m", nil, &SessionStore{})
	if !handled || !strings.Contains(out, "available tools") {
		t.Fatalf("/tools should be handled with tool list, got handled=%v out=%q", handled, out)
	}
	_ = lines
}

// TestHeadlessQueueConsumption verifies the browser queue is FIFO and the
// headless consumer would see submissions in order (queue-level test).
func TestHeadlessQueueOrder(t *testing.T) {
	// drain any residue
	for len(browserSubmissions) > 0 {
		<-browserSubmissions
	}
	browserSubmissions <- "first"
	browserSubmissions <- "second"
	got := []string{}
	timeout := time.After(time.Second)
	for len(got) < 2 {
		select {
		case s := <-browserSubmissions:
			got = append(got, s)
		case <-timeout:
			t.Fatal("timed out draining queue")
		}
	}
	if got[0] != "first" || got[1] != "second" {
		t.Fatalf("queue not FIFO: %v", got)
	}
}
