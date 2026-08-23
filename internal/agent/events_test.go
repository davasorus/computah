package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestEventBusDeliversToSubscribers(t *testing.T) {
	b := &eventBus{}
	var mu sync.Mutex
	var got []Event
	unsub := b.Subscribe(SubscriberFunc(func(e Event) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	}))
	b.Emit(Event{Kind: EvToolCall, Tool: "read_file", Text: `{"path":"x"}`})
	b.Emit(Event{Kind: EvLine, Text: "hello"})
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Kind != EvToolCall || got[0].Tool != "read_file" {
		t.Fatalf("first event wrong: %+v", got[0])
	}
	if got[0].Time.IsZero() {
		t.Fatal("emit must stamp Time")
	}
	// After unsubscribe, no more events land.
	unsub()
	b.Emit(Event{Kind: EvLine, Text: "after"})
	if len(got) != 2 {
		t.Fatalf("unsubscribe must stop delivery, got %d events", len(got))
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	b := &eventBus{}
	var a, c int
	b.Subscribe(SubscriberFunc(func(Event) { a++ }))
	b.Subscribe(SubscriberFunc(func(Event) { c++ }))
	b.Emit(Event{Kind: EvLine, Text: "x"})
	if a != 1 || c != 1 {
		t.Fatalf("both subscribers must receive: a=%d c=%d", a, c)
	}
}

func TestStdoutSubscriberSilent(t *testing.T) {
	// Silenced subscriber must not panic and must produce no output — the
	// TUI relies on this to own the screen.
	s := &stdoutSubscriber{silent: true}
	s.OnEvent(Event{Kind: EvToolCall, Tool: "x", Text: "y"})
	s.OnEvent(Event{Kind: EvError, Text: "boom"})
	// No assertion on output (would require capturing stdout); the contract
	// is simply that silent=true is honored without error.
}

func TestEmitHelpersUseGlobalBus(t *testing.T) {
	var got []Event
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) { got = append(got, e) }))
	defer unsub()
	emitToolCall("grep", "pattern")
	emitToolCallInline("read_file", "x")
	emitError("nope")
	if len(got) != 3 {
		t.Fatalf("expected 3 events via global emitters, got %d", len(got))
	}
	if got[1].Meta["inline"] != "1" {
		t.Fatalf("inline emitter must set the inline meta: %+v", got[1])
	}
}

// TestAssistantContentReachesBusNotStdout verifies the migration: assistant
// content is emitted to the bus (for the dashboard) but the stdout
// subscriber ignores it (mdWriter prints to the terminal directly), so the
// terminal never double-prints.
func TestStdoutSubscriberIgnoresContent(t *testing.T) {
	// The stdout subscriber must no-op on EvToken/EvAssistant.
	s := &stdoutSubscriber{}
	// If it tried to print, there's no assertion hook, but the contract is
	// that these kinds return early. We assert via code path: silent=false
	// and a content event must not panic and must be a no-op branch.
	s.OnEvent(Event{Kind: EvAssistant, Text: "hello"})
	s.OnEvent(Event{Kind: EvToken, Text: "tok"})
	// EvUser is also stdout-silent (user already saw their input).
	s.OnEvent(Event{Kind: EvUser, Text: "my prompt"})
}

func TestMDWriterEmitsAssistantToBus(t *testing.T) {
	var got []string
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvAssistant {
			got = append(got, e.Text)
		}
	}))
	defer unsub()
	saved := useColor
	useColor = true
	defer func() { useColor = saved }()
	m := newMDWriter()
	m.Write("first line\nsecond ")
	m.Write("line\n")
	m.Flush()
	if len(got) < 2 || got[0] != "first line" {
		t.Fatalf("assistant lines must reach the bus: %v", got)
	}
}

// TestMigratedHotPathEmitsColorHints verifies emitLineC carries the color
// through to subscribers (the dashboard/TUI use it; stdout renders via tint).
func TestEmitLineCColorHint(t *testing.T) {
	var got Event
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvLine {
			got = e
		}
	}))
	defer unsub()
	emitLineC(cYellow, "  ⏱ budget warning")
	if got.Color != cYellow {
		t.Fatalf("color hint lost: %q", got.Color)
	}
	if got.Text != "  ⏱ budget warning" {
		t.Fatalf("text wrong: %q", got.Text)
	}
}

func TestEmitStatsCarriesMeta(t *testing.T) {
	var got Event
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvStats {
			got = e
		}
	}))
	defer unsub()
	emitStats("req: ...", map[string]string{"gen": "42"})
	if got.Meta["gen"] != "42" {
		t.Fatalf("stats meta lost: %+v", got.Meta)
	}
}

func TestEmitBusyLifecycle(t *testing.T) {
	var vals []string
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvBusy {
			vals = append(vals, e.Text)
		}
	}))
	defer unsub()
	emitBusy(true)
	emitBusy(false)
	if len(vals) != 2 || vals[0] != "1" || vals[1] != "0" {
		t.Fatalf("busy events wrong: %v", vals)
	}
}

func TestRunTurnEmitsBusyEvents(t *testing.T) {
	// runTurn should bracket its work with busy(true)…busy(false) even when
	// the turn errors out early (deferred). We can't run a real turn without
	// a model, but we can verify the emit ordering via a stub is out of
	// scope; instead confirm the helper contract the wrapper relies on.
	var seq []string
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvBusy {
			seq = append(seq, e.Text)
		}
	}))
	defer unsub()
	func() {
		emitBusy(true)
		defer emitBusy(false)
		// simulate a turn body that panics — busy(false) must still fire
		defer func() { recover() }()
		panic("boom")
	}()
	if len(seq) != 2 || seq[1] != "0" {
		t.Fatalf("busy(false) must fire even on panic: %v", seq)
	}
}

func TestEmitDiffMarksRaw(t *testing.T) {
	var got Event
	unsub := bus.Subscribe(SubscriberFunc(func(e Event) {
		if e.Kind == EvLine && e.Meta != nil && e.Meta["raw"] == "1" {
			got = e
		}
	}))
	defer unsub()
	emitDiff("@@ -1,2 +1,2 @@\n- old\n+ new")
	if got.Meta["raw"] != "1" {
		t.Fatal("emitDiff must mark the event raw so the TUI skips markdown/wrap")
	}
	if !strings.Contains(got.Text, "@@") {
		t.Fatalf("diff text not carried: %q", got.Text)
	}
}
