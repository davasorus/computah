package agent

// This file bridges the agent package to internal/core during the incremental
// package split. The event bus and its types now live in internal/core; these
// aliases let the existing agent-side call sites (emitLine, bus, EvLine, …)
// keep working unchanged. As more code moves to core, these shims shrink.

import "github.com/davasorus/computah/internal/core"

// --- type aliases ---
type Event = core.Event
type EventKind = core.EventKind
type Subscriber = core.Subscriber
type SubscriberFunc = core.SubscriberFunc

// --- event-kind constants ---
const (
	EvLine      = core.EvLine
	EvStatus    = core.EvStatus
	EvError     = core.EvError
	EvToken     = core.EvToken
	EvThinking  = core.EvThinking
	EvBusy      = core.EvBusy
	EvUser      = core.EvUser
	EvAssistant = core.EvAssistant
	EvToolCall  = core.EvToolCall
	EvToolDone  = core.EvToolDone
	EvStats     = core.EvStats
	EvApproval  = core.EvApproval
)

// --- the bus ---
var bus = core.Bus

// --- emit* function aliases (agent call sites use the lowercase names) ---
var (
	emitLine           = core.EmitLine
	emitLineC          = core.EmitLineC
	emitDiff           = core.EmitDiff
	emitStatus         = core.EmitStatus
	emitError          = core.EmitError
	emitToken          = core.EmitToken
	emitThinking       = core.EmitThinking
	emitBusy           = core.EmitBusy
	emitUser           = core.EmitUser
	emitAssistant      = core.EmitAssistant
	emitToolCall       = core.EmitToolCall
	emitToolCallInline = core.EmitToolCallInline
	emitToolDone       = core.EmitToolDone
	emitStats          = core.EmitStats
)
