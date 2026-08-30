# Phase 2 Implementation Doc — Robustness & Error Handling

> Local working doc. Not committed to GH — the durable record is the
> `computah-audit-roadmap` memory note. This file exists to work out the
> design before touching code; update the memory note when Phase 2 ships,
> not this file.

## 1. Problem, precisely

`internal/agent/api.go`'s `chat()` has exactly one return type for failure:
`error`. Every one of these collapses to that same flat type today:

| Site (api.go)              | Cause                                             | Should be   |
|-----------------------------|----------------------------------------------------|-------------|
| `json.Marshal(req)`         | encoding bug in our own request struct              | fatal       |
| `http.NewRequestWithContext`| malformed URL/method (config bug)                  | fatal       |
| `httpClient.Do(httpReq)`    | DNS failure, connection refused, TLS error, timeout | retryable   |
| `resp.StatusCode` 4xx       | bad model name, malformed payload, auth failure     | fatal       |
| `resp.StatusCode` 429       | rate limited                                        | retryable   |
| `resp.StatusCode` 5xx       | server crashed/overloaded                           | retryable   |
| `parseSSE` → `c.Error`      | mid-stream server-reported error (e.g. context overflow) | fatal (see §4.3) |
| `parseSSE` → `sc.Err()`     | connection dropped mid-stream (I/O error)           | retryable   |

`internal/agent/loop.go`'s `streamChat()` (lines 85–120) is the only
consumer of this error today, and it treats all eight rows identically:

```go
if err != nil && !intr && !streamed && attempt < maxAttempts {
    core.EmitStatus(fmt.Sprintf("  (request failed: %v — retrying once)", err))
    time.Sleep(time.Second)
    continue
}
```

Consequence: a user who typos a model name (fatal, HTTP 404) waits through
an identical silent 1-second retry as someone whose Wi-Fi hiccuped
(retryable). Both then fail with the same message. The audit's "blurred
retryable vs. fatal" finding is this exact code.

## 2. Design

### 2.1 New type: `ChatError`

Add to `api.go`:

```go
// ChatErrorKind classifies why a chat request failed, so callers can decide
// whether retrying is worth it instead of treating every failure alike.
type ChatErrorKind int

const (
	ErrKindUnknown    ChatErrorKind = iota
	ErrKindEncode                   // building the request failed — a code/config bug, not the network
	ErrKindConnection               // dial/TLS/DNS failure reaching the server
	ErrKindTimeout                  // context deadline exceeded
	ErrKindClient                   // HTTP 4xx — the request itself is bad
	ErrKindRateLimit                // HTTP 429 — bad timing, not a bad request
	ErrKindServer                   // HTTP 5xx — the server is unhealthy
	ErrKindStream                   // connection dropped mid-SSE-stream
	ErrKindModel                    // server reported an error object mid-stream (e.g. context length)
)

// ChatError wraps a chat() failure with enough context for the caller to
// decide retryable vs. fatal without string-matching error messages.
type ChatError struct {
	Kind      ChatErrorKind
	Retryable bool
	Status    int   // HTTP status code, 0 if not applicable
	Err       error // the underlying error
}

func (e *ChatError) Error() string { return e.Err.Error() }
func (e *ChatError) Unwrap() error { return e.Err }

// retryable reports whether err (as returned by chat()) is worth retrying.
// Unclassified errors default to retryable — a safety net matching today's
// behavior, so nothing regresses if an error path is missed. Every chat()
// return site should classify explicitly rather than rely on this default.
func retryable(err error) bool {
	var ce *ChatError
	if errors.As(err, &ce) {
		return ce.Retryable
	}
	return true
}
```

`Retryable` is stored explicitly on the struct (not derived purely from
`Kind` at call time) so a future case — e.g. "429 but the response carried
`Retry-After: 3600`, don't bother" — can override it without adding a new
Kind.

### 2.2 Classification at each site

Every `return Message{}, err` / `return msg, err` in `chat()` becomes a
wrapped `*ChatError`:

```go
body, err := json.Marshal(req)
if err != nil {
	return Message{}, &ChatError{Kind: ErrKindEncode, Retryable: false, Err: err}
}
...
httpReq, err := http.NewRequestWithContext(...)
if err != nil {
	return Message{}, &ChatError{Kind: ErrKindEncode, Retryable: false, Err: err}
}
...
resp, err := httpClient.Do(httpReq)
if err != nil {
	kind, retry := ErrKindConnection, true
	if errors.Is(err, context.DeadlineExceeded) {
		kind = ErrKindTimeout
	}
	return Message{}, &ChatError{Kind: kind, Retryable: retry, Err: err}
}
...
if resp.StatusCode != http.StatusOK {
	... existing body-parsing for e.Error.Message ...
	ce := &ChatError{Status: resp.StatusCode, Err: fmt.Errorf(...)}
	switch {
	case resp.StatusCode == 429:
		ce.Kind, ce.Retryable = ErrKindRateLimit, true
	case resp.StatusCode >= 500:
		ce.Kind, ce.Retryable = ErrKindServer, true
	default: // other 4xx
		ce.Kind, ce.Retryable = ErrKindClient, false
	}
	return Message{}, ce
}
```

`parseSSE`'s two failure returns need to reach `chat()` distinguishably.
Simplest: keep `parseSSE`'s signature returning a plain `error` as today,
but classify at the `chat()` call site using a sentinel wrapper set inside
`parseSSE`:

```go
var errStreamIO = errors.New("stream read error")     // sc.Err()
// the c.Error case already carries the server's message; wrap it distinctly:
var errStreamModel = errors.New("model/server stream error") // used via %w
```

Concretely, in `parseSSE`:
```go
if c.Error != nil {
	return msg, finish, think, fmt.Errorf("%w: %s", errStreamModel, c.Error.Message)
}
...
return msg, finish, think, sc.Err() // nil, or the scanner's I/O error — never errStreamModel
```

Then in `chat()`, after `parseSSE` returns:
```go
if err != nil {
	switch {
	case errors.Is(err, errStreamModel):
		err = &ChatError{Kind: ErrKindModel, Retryable: false, Err: err}
	default:
		err = &ChatError{Kind: ErrKindStream, Retryable: true, Err: err}
	}
	...(existing interrupted-content fallback stays as-is)...
	return msg, err
}
```

This keeps `parseSSE`'s tests (`TestParseSSEReasoning`, etc., in
`features_test.go`) working unchanged — they only check `err != nil`, not
type — while giving `chat()`'s caller a real classification.

### 2.3 `streamChat()` changes (loop.go)

```go
for attempt := 1; attempt <= maxAttempts; attempt++ {
	...
	reply, intr, err = interruptibleChat(baseURL, model, messages, ...)
	...
	if err != nil && !intr && !streamed {
		if !retryable(err) {
			core.EmitError(fmt.Sprintf("  (request failed, not retrying: %v)", err))
			break
		}
		if attempt < maxAttempts {
			core.EmitStatus(fmt.Sprintf("  (request failed: %v — retrying once)", err))
			time.Sleep(time.Second)
			continue
		}
	}
	break
}
```

Net behavior change: fatal errors (bad model, malformed request, HTTP 4xx)
surface **immediately**, with a clearer "not retrying" message, instead of
silently waiting out a pointless 1-second sleep first. Retryable errors keep
today's exact behavior (one retry, 1s sleep). `maxAttempts` stays 2 —
backoff growth (e.g. exponential for repeated 5xx/429) is an easy follow-on
but is explicitly **out of scope** for this pass to keep the change small
and reviewable; noted in §5.

No other call site needs to change:
- `checkpoint.go:155` and `session.go:565` and `session.go:366`
  (`Compact`, commit-message generation, session-title generation) all call
  `streamChat`/`chat` directly for one-shot, non-agentic requests with their
  own `err != nil` handling already — they just get better-typed errors for
  free, no behavior change required of them.
- `RunVerifyLoop` retries at a completely different level (re-running the
  turn after a failed verify command) and is unaffected.

## 3. Test plan

New file `internal/agent/api_test.go` (none exists yet — confirmed via
`glob`), using `httptest.NewServer` (already a proven pattern in this repo,
see `internal/agent/mcp_http_test.go`, `internal/web/dashboard_test.go`):

1. **Classification table test** — table-driven, one fake handler per row of
   §1's table, asserting `chat()`'s returned error `errors.As`s into
   `*ChatError` with the expected `Kind`/`Retryable`/`Status`:
   - 400 → `ErrKindClient`, `Retryable: false`
   - 429 → `ErrKindRateLimit`, `Retryable: true`
   - 500 → `ErrKindServer`, `Retryable: true`
   - connection refused (dial a closed port) → `ErrKindConnection`, `Retryable: true`
   - context timeout (server sleeps past a short ctx deadline) → `ErrKindTimeout`, `Retryable: true`
   - SSE stream with a `data: {"error":{"message":"..."}}` frame → `ErrKindModel`, `Retryable: false`
   - SSE stream that the server hangs up mid-write (close conn without `[DONE]`) → `ErrKindStream`, `Retryable: true`
2. **`retryable()` unit test** — a plain (unwrapped) `errors.New("x")` must
   return `true` (the safety-net default), and a `*ChatError{Retryable:
   false}` must return `false`.
3. **`streamChat()` behavior test** — needs `baseURL` pointed at a fake
   server (existing tests already exercise `streamChat` indirectly via
   `checkpoint.go`/`session.go` callers, but a direct test is cleaner):
   - Fatal case: server always returns 400 → assert `streamChat` returns
     after exactly 1 attempt (no `time.Sleep`, verified via a short test
     timeout or a call counter on the fake handler hit exactly once).
   - Retryable case: server returns 500 once then 200 → assert 2 attempts
     and eventual success (existing pattern implicitly relied on before,
     now made explicit).

## 4. Edge cases / judgment calls to confirm before implementing

1. **429 with `Retry-After`**: not handled specially in this pass — treated
   as a plain retryable 5xx-equivalent with the same flat 1s sleep. Correct
   behavior would honor the header; deferred to keep this change small
   (noted in §5 as a fast follow).
2. **User interrupt vs. retryable**: unchanged — `intr` is checked *before*
   `retryable(err)` in the `if` condition, so a user-cancelled request never
   retries regardless of classification. No change to that guard.
3. **`ErrKindModel` (mid-stream server error) defaults to fatal.** This is a
   judgment call: a "context length exceeded" or "invalid request" error
   object from the server mid-stream is usually a real, permanent problem
   with this request (not a network blip), so treating it as fatal avoids a
   pointless retry of the same too-long prompt. If real-world testing shows
   servers emitting transient errors this way (e.g. a momentary OOM),
   revisit — flagged here so it isn't a silent assumption.
4. **Backward compatibility of the `error` return type**: `chat()`'s
   signature stays `(Message, error)` — `*ChatError` implements `error`, so
   no caller signature changes. Callers that only did `if err != nil`
   continue to compile and behave identically; only `streamChat` needs the
   `errors.As`/`retryable()` check.

## 5. Explicitly out of scope for this pass

- Exponential backoff / honoring `Retry-After` — flat 1s sleep, `maxAttempts
  = 2` stay as-is.
- Structured status updates for internal state transitions (a separate
  audit bullet under "Robustness") — not addressed here; this pass is
  scoped to the retryable/fatal classification only.
- Any change to `RunVerifyLoop`'s retry semantics.

## 6. File-by-file change list

| File | Change |
|---|---|
| `internal/agent/api.go` | Add `ChatErrorKind`, `ChatError`, `retryable()`, `errStreamModel` sentinel; wrap every `chat()` error return site; classify `parseSSE`'s `c.Error` path via the sentinel. |
| `internal/agent/loop.go` | Update `streamChat()`'s retry condition to check `retryable(err)` and break immediately (with `core.EmitError`) on fatal. |
| `internal/agent/api_test.go` (new) | Classification table test, `retryable()` unit test. |
| `internal/agent/loop_test.go` (new, or add to `features_test.go`) | `streamChat()` fatal-vs-retryable attempt-count test. |
| `computah-audit-roadmap` memory note | Mark Phase 2 done, same pattern as Phase 1/1b entries, once merged and verified. |

## 7. Verification before calling it done

- `go build ./...`
- `go vet ./...`
- `go test ./...` (existing `TestParseSSEReasoning`,
  `TestParseSSEAnswerOnlyInReasoning`, and all `checkpoint.go`/`session.go`
  consumers of `streamChat`/`chat` must keep passing unchanged)
- Manual sanity check: point `baseURL` at nothing running (connection
  refused) and confirm one retry + clear final message; point at a server
  returning 400 and confirm zero retries + immediate fatal message.
