package responses

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// twoResponsesEvents is the opening of a Responses stream: the response is
// created and an output item is added, and nothing completes it.
const twoResponsesEvents = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
	"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"

// stallingSSEClient delivers response headers synchronously, then stalls the
// response body. It has no socket/header scheduling race with the idle timer.
func stallingSSEClient(t *testing.T, prefix string) (*http.Client, *httptest.Server) {
	t.Helper()
	r, w := io.Pipe()
	stall := make(chan struct{})
	t.Cleanup(func() { r.Close(); close(stall) })
	client := &http.Client{Transport: idleAttemptRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.Body != nil {
			io.Copy(io.Discard, req.Body)
			req.Body.Close()
		}
		go func() { defer w.Close(); io.WriteString(w, prefix); <-stall }()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: r, Request: req}, nil
	})}
	return client, &httptest.Server{URL: "https://example.invalid"}
}

type idleAttemptRoundTripper func(*http.Request) (*http.Response, error)

func (f idleAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
		client, srv := stallingSSEClient(t, twoResponsesEvents)
		sink := &captureSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_responses_sse_timeout")),
			sink,
		)
		req := userReq("hi")
		req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
		s, err := (&Protocol{Client: client}).Stream(ctx, req, liveRes(srv, openaiCaps))
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
