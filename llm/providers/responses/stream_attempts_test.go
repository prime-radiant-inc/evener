package responses

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// twoResponsesEvents is the opening of a Responses stream: the response is
// created and an output item is added, and nothing completes it.
const twoResponsesEvents = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"

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
	srv, _ := server(t, 200, responseSSE)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_responses_stream_order")),
		sink,
	)
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, userReq("hi"), liveRes(srv, openaiCaps))
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
			if got := len(sink.attempts); got != 1 {
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
	srv := stallingSSEServer(t, twoResponsesEvents)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_responses_sse_timeout")),
		sink,
	)
	req := userReq("hi")
	req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, req, liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatal(err)
	}
	for range s.Events() { //nolint:revive // Drain to the terminal timeout evidence.
	}
	llm.WaitForPriorAPIAttempts(ctx)
	if len(sink.attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(sink.attempts))
	}
	if got := sink.attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
		t.Fatalf("SSE-read timeout outcome = %q, want %q", got, apilog.AttemptProviderTimeout)
	}
}
