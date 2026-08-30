// Event Bus — the shared output spine.
//
// Historically every part of the agent wrote straight to stdout with
// fmt.Println. That works for a REPL but makes alternate front-ends (a TUI,
// a web dashboard) impossible without duplicating output plumbing. The Bus
// inverts it: code EMITS typed events, and any number of SUBSCRIBERS render
// them however they like. stdout is just the first subscriber; the TUI and
// dashboard become additional ones, reading the same stream.
//
// Migration is incremental and safe. This step introduces the Bus and makes
// stdout a subscriber, but does NOT rewrite the 250+ existing fmt.Println
// sites — those keep printing directly for now. New structured emits
// (tokens, tool calls, stats) go through the Bus; the plain-text sites get
// migrated file-by-file in later steps. During the transition the terminal
// output is identical because the stdout subscriber mirrors what the direct
// prints already do. Nothing breaks; the Bus simply becomes available.
package core

import (
	"sync"
	"time"
)

// EventKind classifies a Bus event so subscribers can render selectively.
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

	// EvOverwrite is an in-place update to a line identified by Event.Key,
	// rather than a new discrete line — the spinner, a progress counter, or
	// any other "this line keeps changing" status. A subscriber that
	// understands Key re-renders that ONE line in place (terminal: erase +
	// reprint the current line; TUI/dashboard: update the existing widget
	// instead of appending a new one). Meta["clear"]=="1" means "remove the
	// line for this Key entirely" (e.g. the spinner finished). A subscriber
	// that doesn't implement in-place rendering may safely treat this like
	// EvLine and append it — degraded (a scrolling trail instead of one
	// updating line) but never broken, and a clear with no matching prior
	// line is simply a no-op.
	EvOverwrite EventKind = "overwrite"
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
	// Key identifies the in-place line an EvOverwrite event updates or
	// clears (see EvOverwrite). Unused by other kinds.
	Key string
}

// Subscriber receives every event. Implementations must be non-blocking or
// buffer internally — the emit path holds a lock briefly and must not stall.
type Subscriber interface {
	OnEvent(Event)
}

// SubscriberFunc adapts a plain function to Subscriber.
type SubscriberFunc func(Event)

func (f SubscriberFunc) OnEvent(e Event) { f(e) }

// Bus is the process-wide event Bus.
var Bus = &eventBus{}

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

func EmitLine(text string) { Bus.Emit(Event{Kind: EvLine, Text: text}) }
func EmitLineC(color, text string) {
	Bus.Emit(Event{Kind: EvLine, Text: text, Color: color})
}

// EmitDiff emits pre-formatted, already-styled output (diffs, edit traces)
// that must NOT be re-rendered as markdown or word-wrapped. The TUI prints it
// verbatim in-viewport instead of letting raw fmt.Print corrupt the
// bubbletea alt-screen.
func EmitDiff(text string) {
	Bus.Emit(Event{Kind: EvLine, Text: text, Meta: map[string]string{"raw": "1"}})
}
func EmitStatus(text string)   { Bus.Emit(Event{Kind: EvStatus, Text: text}) }
func EmitError(text string)    { Bus.Emit(Event{Kind: EvError, Text: text}) }
func EmitToken(text string)    { Bus.Emit(Event{Kind: EvToken, Text: text}) }
func EmitThinking(text string) { Bus.Emit(Event{Kind: EvThinking, Text: text}) }
func EmitBusy(working bool) {
	v := "0"
	if working {
		v = "1"
	}
	Bus.Emit(Event{Kind: EvBusy, Text: v})
}
func EmitUser(text string)      { Bus.Emit(Event{Kind: EvUser, Text: text}) }
func EmitAssistant(text string) { Bus.Emit(Event{Kind: EvAssistant, Text: text}) }

func EmitToolCall(tool, argsPreview string) {
	Bus.Emit(Event{Kind: EvToolCall, Tool: tool, Text: argsPreview})
}
func EmitToolCallInline(tool, argsPreview string) {
	Bus.Emit(Event{Kind: EvToolCall, Tool: tool, Text: argsPreview, Meta: map[string]string{"inline": "1"}})
}
func EmitToolDone(tool, summary string) {
	Bus.Emit(Event{Kind: EvToolDone, Tool: tool, Text: summary})
}
func EmitStats(text string, meta map[string]string) {
	Bus.Emit(Event{Kind: EvStats, Text: text, Meta: meta})
}

// EmitOverwrite emits (or updates) an in-place line identified by key —
// repeated calls with the same key replace that line rather than adding a
// new one. Use for spinners, progress counters, and other status text that
// changes rapidly and shouldn't scroll the transcript. color is an optional
// tint hint (see Event.Color), empty for none.
func EmitOverwrite(key, text, color string) {
	Bus.Emit(Event{Kind: EvOverwrite, Key: key, Text: text, Color: color})
}

// EmitClear removes the in-place line for key (e.g. a finished spinner) —
// subscribers that rendered it erase the line entirely rather than leaving
// stale text behind.
func EmitClear(key string) {
	Bus.Emit(Event{Kind: EvOverwrite, Key: key, Meta: map[string]string{"clear": "1"}})
}

// NewBus returns a fresh, empty event bus. Tests use it to exercise
// subscribe/emit in isolation from the process-global Bus.
func NewBus() *eventBus { return &eventBus{} }
