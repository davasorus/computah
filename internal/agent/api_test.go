package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestChatErrorClassification drives chat() against fake servers for each
// row of the failure table and asserts the returned error classifies
// correctly — the exact regression this pass guards against: every failure
// used to collapse into a flat, unclassified error.
func TestChatErrorClassification(t *testing.T) {
	cases := []struct {
		name      string
		handler   http.HandlerFunc
		wantKind  ChatErrorKind
		wantRetry bool
	}{
		{
			name: "400 client error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
			},
			wantKind:  ErrKindClient,
			wantRetry: false,
		},
		{
			name: "429 rate limited",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			},
			wantKind:  ErrKindRateLimit,
			wantRetry: true,
		},
		{
			name: "500 server error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
			},
			wantKind:  ErrKindServer,
			wantRetry: true,
		},
		{
			name: "mid-stream model error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				_, _ = w.Write([]byte(`data: {"error":{"message":"context length exceeded"}}` + "\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
			},
			wantKind:  ErrKindModel,
			wantRetry: false,
		},
		{
			name: "mid-stream connection drop",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)
				_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"))
				if flusher != nil {
					flusher.Flush()
				}
				// Hang up without [DONE] by closing the underlying connection.
				hj, ok := w.(http.Hijacker)
				if !ok {
					return
				}
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			},
			wantKind:  ErrKindStream,
			wantRetry: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()
			_, err := chat(context.Background(), ts.URL, "test-model", []Message{{Role: "user", Content: "hi"}}, nil)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			var ce *ChatError
			if !errors.As(err, &ce) {
				t.Fatalf("error is not a *ChatError: %v (%T)", err, err)
			}
			if ce.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", ce.Kind, tc.wantKind)
			}
			if ce.Retryable != tc.wantRetry {
				t.Errorf("Retryable = %v, want %v", ce.Retryable, tc.wantRetry)
			}
			if got := retryable(err); got != tc.wantRetry {
				t.Errorf("retryable(err) = %v, want %v", got, tc.wantRetry)
			}
		})
	}
}

// TestChatErrorConnectionRefused hits a closed port — no server listening —
// so httpClient.Do fails at dial time rather than after a response.
func TestChatErrorConnectionRefused(t *testing.T) {
	// Bind and immediately close to get a port nothing listens on.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	_, err = chat(context.Background(), "http://"+addr, "test-model", []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var ce *ChatError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a *ChatError: %v (%T)", err, err)
	}
	if ce.Kind != ErrKindConnection {
		t.Errorf("Kind = %v, want ErrKindConnection", ce.Kind)
	}
	if !ce.Retryable {
		t.Error("connection refused must be retryable")
	}
}

// TestChatErrorTimeout uses a very short context deadline against a server
// that deliberately stalls past it.
func TestChatErrorTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := chat(ctx, ts.URL, "test-model", []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var ce *ChatError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not a *ChatError: %v (%T)", err, err)
	}
	if ce.Kind != ErrKindTimeout {
		t.Errorf("Kind = %v, want ErrKindTimeout", ce.Kind)
	}
	if !ce.Retryable {
		t.Error("timeout must be retryable")
	}
}

// TestRetryableDefault confirms the safety-net default: a plain, unwrapped
// error is treated as retryable (matching the pre-classification behavior),
// while an explicit *ChatError{Retryable: false} is honored.
func TestRetryableDefault(t *testing.T) {
	if !retryable(errors.New("plain error")) {
		t.Error("an unclassified error must default to retryable")
	}
	if retryable(&ChatError{Retryable: false, Err: errors.New("fatal")}) {
		t.Error("a ChatError with Retryable=false must not be retryable")
	}
	if !retryable(&ChatError{Retryable: true, Err: errors.New("transient")}) {
		t.Error("a ChatError with Retryable=true must be retryable")
	}
}
