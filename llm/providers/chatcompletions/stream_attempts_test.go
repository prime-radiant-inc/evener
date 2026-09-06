package chatcompletions

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// captureSink collects the api-log attempt records a call emits.
type captureSink struct {
	mu       sync.Mutex
	attempts []apilog.APIAttemptRecord
}

func (s *captureSink) AppendAttempt(_ context.Context, r apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, r)
	return nil
}

func (s *captureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

// records returns a copy of the attempts appended so far. Every read goes
// through it: the producer appends from its own goroutine, so an unguarded
// read is only accidentally safe.
func (s *captureSink) records() []apilog.APIAttemptRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]apilog.APIAttemptRecord(nil), s.attempts...)
}

// twoChatChunks is the opening of a Chat Completions stream: two content
// deltas, with neither a finish_reason chunk nor the [DONE] that ends it.
const twoChatChunks = "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n"

// TestStreamAppendsTheAttemptBeforeTheTerminalEvent pins the ordering the
// adapter's wire captures pinned before the protocols replaced them: the
// canonical attempt record is appended before the consumer sees the
// stream's terminal event, so a caller that reacts to FINISH always finds
// the attempt already in the log.
func TestStreamAppendsTheAttemptBeforeTheTerminalEvent(t *testing.T) {
	srv, _ := server(t, 200, chatSSE)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_chat_stream_order")),
		sink,
	)
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, userReq("hi"), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	sawFinish := false
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			sawFinish = true
			if got := len(sink.records()); got != 1 {
				t.Fatalf("attempts visible at finish = %d, want 1", got)
			}
		}
	}
	if !sawFinish {
		t.Fatal("stream ended without a finish event")
	}
	if got := sink.records()[0].Outcome; got != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want success", got)
	}
}

// TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout pins that a stream
// that stalls mid-body is recorded as a provider timeout — the request
// reached the provider and the response headers arrived, so neither a
// connect nor a request-deadline classification would be honest.
func TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		stall := make(chan struct{})
		defer close(stall)
		defer r.Close()
		client := &http.Client{Transport: idleAttemptRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Body != nil {
				io.Copy(io.Discard, req.Body)
				req.Body.Close()
			}
			go func() { defer w.Close(); io.WriteString(w, twoChatChunks); <-stall }()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: r, Request: req}, nil
		})}
		srv := &httptest.Server{URL: "https://example.invalid"}
		sink := &captureSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_chat_sse_timeout")),
			sink,
		)
		req := userReq("hi")
		req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
		s, err := (&Protocol{Client: client}).Stream(ctx, req, liveRes(srv, nil))
		if err != nil {
			t.Fatal(err)
		}
		for range s.Events() { //nolint:revive // Drain to the terminal timeout evidence.
		}
		llm.WaitForPriorAPIAttempts(ctx)
		attempts := sink.records()
		if len(attempts) != 1 {
			t.Fatalf("attempts = %d, want 1", len(attempts))
		}
		if got := attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
			t.Fatalf("SSE-read timeout outcome = %q, want %q", got, apilog.AttemptProviderTimeout)
		}
	})
}

type idleAttemptRoundTripper func(*http.Request) (*http.Response, error)

func (f idleAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
