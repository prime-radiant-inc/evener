package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// twoAnthropicEvents is the opening of a Messages stream: enough for the
// decoder to start a message and a text block, and nothing that finishes it.
const twoAnthropicEvents = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"

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
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, messagesSSE })
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_anthropic_stream_order")),
		sink,
	)
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, protoReq(""), protoLive(srv))
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
	srv := stallingSSEServer(t, twoAnthropicEvents)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_anthropic_sse_timeout")),
		sink,
	)
	req := protoReq("")
	req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, req, protoLive(srv))
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
}
