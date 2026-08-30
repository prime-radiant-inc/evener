package chatcompletions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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

func (s *captureSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.attempts)
}

// twoChatChunks is the opening of a Chat Completions stream: two content
// deltas, with neither a finish_reason chunk nor the [DONE] that ends it.
const twoChatChunks = "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hel\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n"

// stallingSSEServer streams prefix, flushes it, and then holds the response
// open until the test ends: the only way out of such a stream is the
// StreamRead idle timeout.
func stallingSSEServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	stall := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(prefix))
		w.(http.Flusher).Flush()
		<-stall
	}))
	// Cleanups run last-registered-first: release the handler before Close
	// waits for it.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stall) })
	return srv
}

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
			if got := sink.count(); got != 1 {
				t.Fatalf("attempts visible at finish = %d, want 1", got)
			}
		}
	}
	if !sawFinish {
		t.Fatal("stream ended without a finish event")
	}
	if got := sink.attempts[0].Outcome; got != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want success", got)
	}
}

// TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout pins that a stream
// that stalls mid-body is recorded as a provider timeout — the request
// reached the provider and the response headers arrived, so neither a
// connect nor a request-deadline classification would be honest.
func TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout(t *testing.T) {
	srv := stallingSSEServer(t, twoChatChunks)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_chat_sse_timeout")),
		sink,
	)
	req := userReq("hi")
	req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, req, liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() { //nolint:revive // Drain to the terminal timeout evidence.
	}
	llm.WaitForPriorAPIAttempts(ctx)
	if sink.count() != 1 {
		t.Fatalf("attempts = %d, want 1", sink.count())
	}
	if got := sink.attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
		t.Fatalf("SSE-read timeout outcome = %q, want %q", got, apilog.AttemptProviderTimeout)
	}
}
