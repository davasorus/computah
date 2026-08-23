// Event bus — the shared output spine.
//
// Historically every part of the agent wrote straight to stdout with
// fmt.Println. That works for a REPL but makes alternate front-ends (a TUI,
// a web dashboard) impossible without duplicating output plumbing. The bus
// inverts it: code EMITS typed events, and any number of SUBSCRIBERS render
// them however they like. stdout is just the first subscriber; the TUI and
// dashboard become additional ones, reading the same stream.
//
// Migration is incremental and safe. This step introduces the bus and makes
// stdout a subscriber, but does NOT rewrite the 250+ existing fmt.Println
// sites — those keep printing directly for now. New structured emits
// (tokens, tool calls, stats) go through the bus; the plain-text sites get
// migrated file-by-file in later steps. During the transition the terminal
// output is identical because the stdout subscriber mirrors what the direct
// prints already do. Nothing breaks; the bus simply becomes available.
package agent

import (
	"sync"
	"time"
)

// EventKind classifies a bus event so subscribers can render selectively.
type EventKind string

const (
	EvLine      EventKind = "line"      // a plain status/trace line (the fmt.Println replacement)
	EvToken     EventKind = "token"     // a streamed assistant content token
	EvThinking  EventKind = "thinking"  // reasoning progress (token count / label)
	EvToolCall  EventKind = "tool_call" // a tool invocation (name + short args)
	EvToolDone  EventKind = "tool_done" // a tool result summary
	EvUser      EventKind = "user"      // the user's submitted message
	EvAssistant EventKind = "assistant" // a completed assistant message
	EvStats     EventKind = "stats"     // a stats/budget snapshot update
	EvStatus    EventKind = "status"    // connection/mode status (mcp connected, model, etc.)
	EvError     EventKind = "error"     // an error line
	EvBusy      EventKind = "busy"      // turn lifecycle: Text is "1" (working) or "0" (idle)
	EvApproval  EventKind = "approval"  // a tool/command awaits user approval (web mode)
)

// Event is one thing that happened, timestamped. Fields beyond Kind/Text are
// optional and kind-specific; subscribers read what they need.
type Event struct {
	Kind EventKind
	Text string
	// Optional structured payload for richer subscribers (TUI/dashboard).
	// The stdout subscriber mostly uses Text.
	Tool  string            // tool name (EvToolCall/EvToolDone)
	Meta  map[string]string // arbitrary extras (stats fields, args preview)
	Color string            // suggested color hint for the line (maps to term colors)
	Time  time.Time
}

// Subscriber receives every event. Implementations must be non-blocking or
// buffer internally — the emit path holds a lock briefly and must not stall.
type Subscriber interface {
	OnEvent(Event)
}

// SubscriberFunc adapts a plain function to Subscriber.
type SubscriberFunc func(Event)

func (f SubscriberFunc) OnEvent(e Event) { f(e) }

// bus is the process-wide event bus.
var bus = &eventBus{}

type eventBus struct {
	mu   sync.RWMutex
	subs []Subscriber
}

// Subscribe registers a subscriber. Returns an unsubscribe func.
func (b *eventBus) Subscribe(s Subscriber) func() {
	b.mu.Lock()
	b.subs = append(b.subs, s)
	idx := len(b.subs) - 1
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if idx < len(b.subs) {
			b.subs[idx] = nil
		}
		b.mu.Unlock()
	}
}

// Emit delivers an event to all subscribers. Cheap when there are none.
func (b *eventBus) Emit(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	b.mu.RLock()
	subs := b.subs
	b.mu.RUnlock()
	for _, s := range subs {
		if s != nil {
			s.OnEvent(e)
		}
	}
}

// --- Convenience emitters used by the rest of the agent ---

func emitLine(text string) { bus.Emit(Event{Kind: EvLine, Text: text}) }
func emitLineC(color, text string) {
	bus.Emit(Event{Kind: EvLine, Text: text, Color: color})
}

// emitDiff emits pre-formatted, already-styled output (diffs, edit traces)
// that must NOT be re-rendered as markdown or word-wrapped. The TUI prints it
// verbatim in-viewport instead of letting raw fmt.Print corrupt the
// bubbletea alt-screen.
func emitDiff(text string) {
	bus.Emit(Event{Kind: EvLine, Text: text, Meta: map[string]string{"raw": "1"}})
}
func emitStatus(text string)   { bus.Emit(Event{Kind: EvStatus, Text: text}) }
func emitError(text string)    { bus.Emit(Event{Kind: EvError, Text: text}) }
func emitToken(text string)    { bus.Emit(Event{Kind: EvToken, Text: text}) }
func emitThinking(text string) { bus.Emit(Event{Kind: EvThinking, Text: text}) }
func emitBusy(working bool) {
	v := "0"
	if working {
		v = "1"
	}
	bus.Emit(Event{Kind: EvBusy, Text: v})
}
func emitUser(text string)      { bus.Emit(Event{Kind: EvUser, Text: text}) }
func emitAssistant(text string) { bus.Emit(Event{Kind: EvAssistant, Text: text}) }

func emitToolCall(tool, argsPreview string) {
	bus.Emit(Event{Kind: EvToolCall, Tool: tool, Text: argsPreview})
}
func emitToolCallInline(tool, argsPreview string) {
	bus.Emit(Event{Kind: EvToolCall, Tool: tool, Text: argsPreview, Meta: map[string]string{"inline": "1"}})
}
func emitToolDone(tool, summary string) {
	bus.Emit(Event{Kind: EvToolDone, Tool: tool, Text: summary})
}
func emitStats(text string, meta map[string]string) {
	bus.Emit(Event{Kind: EvStats, Text: text, Meta: meta})
}
