package core

import (
	"sync"
	"testing"
	"time"
)

// collector is a simple Subscriber that records every event it receives.
type collector struct {
	mu     sync.Mutex
	events []Event
}

func (c *collector) OnEvent(e Event) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func TestBusSubscribeAndEmit(t *testing.T) {
	b := NewBus()
	c := &collector{}
	b.Subscribe(c)

	b.Emit(Event{Kind: EvLine, Text: "hello"})
	if c.count() != 1 {
		t.Fatalf("expected 1 event, got %d", c.count())
	}
	if c.events[0].Text != "hello" || c.events[0].Kind != EvLine {
		t.Errorf("event not delivered intact: %+v", c.events[0])
	}
}

func TestBusFanOutToMultipleSubscribers(t *testing.T) {
	b := NewBus()
	a, c := &collector{}, &collector{}
	b.Subscribe(a)
	b.Subscribe(c)
	b.Emit(Event{Kind: EvStatus, Text: "x"})
	if a.count() != 1 || c.count() != 1 {
		t.Errorf("both subscribers should receive the event: a=%d c=%d", a.count(), c.count())
	}
}

func TestBusUnsubscribe(t *testing.T) {
	b := NewBus()
	c := &collector{}
	unsub := b.Subscribe(c)

	b.Emit(Event{Kind: EvLine, Text: "1"})
	unsub()
	b.Emit(Event{Kind: EvLine, Text: "2"})

	if c.count() != 1 {
		t.Errorf("after unsubscribe, expected 1 event total, got %d", c.count())
	}
}

func TestBusEmitStampsTime(t *testing.T) {
	b := NewBus()
	c := &collector{}
	b.Subscribe(c)
	before := time.Now()
	b.Emit(Event{Kind: EvLine, Text: "t"}) // no Time set
	if c.events[0].Time.Before(before) || c.events[0].Time.IsZero() {
		t.Errorf("Emit should stamp a Time; got %v", c.events[0].Time)
	}
	// An explicit Time is preserved.
	fixed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Emit(Event{Kind: EvLine, Text: "t2", Time: fixed})
	if !c.events[1].Time.Equal(fixed) {
		t.Errorf("explicit Time should be preserved, got %v", c.events[1].Time)
	}
}

func TestBusEmitWithNoSubscribers(t *testing.T) {
	b := NewBus()
	// Must not panic with zero subscribers.
	b.Emit(Event{Kind: EvLine, Text: "nobody listening"})
}

func TestSubscriberFuncAdapter(t *testing.T) {
	b := NewBus()
	var got string
	b.Subscribe(SubscriberFunc(func(e Event) { got = e.Text }))
	b.Emit(Event{Kind: EvLine, Text: "via func"})
	if got != "via func" {
		t.Errorf("SubscriberFunc adapter failed, got %q", got)
	}
}

// TestBusConcurrentEmit exercises the RWMutex path: many goroutines emitting
// while a subscriber records. With -race this catches lock regressions.
func TestBusConcurrentEmit(t *testing.T) {
	b := NewBus()
	c := &collector{}
	b.Subscribe(c)

	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Emit(Event{Kind: EvToken, Text: "x"})
		}()
	}
	wg.Wait()
	if c.count() != n {
		t.Errorf("expected %d events under concurrency, got %d", n, c.count())
	}
}

// The convenience emitters target the process-global Bus; verify they produce
// the right Kind and encode their flags/values correctly.
func TestConvenienceEmitters(t *testing.T) {
	c := &collector{}
	unsub := Bus.Subscribe(c)
	defer unsub()

	EmitLine("a")
	EmitStatus("b")
	EmitError("c")
	EmitToken("d")
	EmitThinking("e")
	EmitUser("f")
	EmitAssistant("g")
	EmitBusy(true)
	EmitBusy(false)
	EmitDiff("diff-text")
	EmitToolCall("tool", "args")
	EmitToolCallInline("tool", "args")
	EmitToolDone("tool", "done")

	kinds := map[EventKind]int{}
	var busyVals []string
	var diffMeta, inlineMeta bool
	c.mu.Lock()
	for _, e := range c.events {
		kinds[e.Kind]++
		if e.Kind == EvBusy {
			busyVals = append(busyVals, e.Text)
		}
		if e.Kind == EvLine && e.Meta["raw"] == "1" {
			diffMeta = true
		}
		if e.Kind == EvToolCall && e.Meta["inline"] == "1" {
			inlineMeta = true
		}
	}
	c.mu.Unlock()

	if kinds[EvLine] < 2 { // EmitLine + EmitDiff both emit EvLine
		t.Errorf("expected >=2 EvLine (line+diff), got %d", kinds[EvLine])
	}
	if kinds[EvStatus] != 1 || kinds[EvError] != 1 || kinds[EvToken] != 1 {
		t.Errorf("status/error/token kinds wrong: %v", kinds)
	}
	if len(busyVals) != 2 || busyVals[0] != "1" || busyVals[1] != "0" {
		t.Errorf("busy should encode true->1, false->0, got %v", busyVals)
	}
	if !diffMeta {
		t.Error("EmitDiff should set meta raw=1")
	}
	if !inlineMeta {
		t.Error("EmitToolCallInline should set meta inline=1")
	}
}

// TestEmitOverwriteAndClear verifies the in-place-line emitters encode Kind,
// Key, Text/Color, and the clear flag correctly — subscribers key their
// replace/erase logic off exactly these fields.
func TestEmitOverwriteAndClear(t *testing.T) {
	c := &collector{}
	unsub := Bus.Subscribe(c)
	defer unsub()

	EmitOverwrite("spinner", "thinking…", ColorCyan)
	EmitOverwrite("spinner", "thinking harder…", ColorCyan)
	EmitClear("spinner")

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(c.events))
	}
	for i, e := range c.events {
		if e.Kind != EvOverwrite {
			t.Fatalf("event %d: expected EvOverwrite, got %v", i, e.Kind)
		}
		if e.Key != "spinner" {
			t.Fatalf("event %d: expected Key=spinner, got %q", i, e.Key)
		}
	}
	if c.events[0].Text != "thinking…" || c.events[0].Color != ColorCyan {
		t.Errorf("first overwrite wrong: %+v", c.events[0])
	}
	if c.events[1].Text != "thinking harder…" {
		t.Errorf("second overwrite should carry updated text: %+v", c.events[1])
	}
	if c.events[2].Meta["clear"] != "1" {
		t.Errorf("EmitClear must set meta clear=1: %+v", c.events[2])
	}
}
