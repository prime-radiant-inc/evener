package responses

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
)

// abortingSSEServer writes prefix and then hijacks and closes the connection
// without a terminating chunk, so the client's read fails mid-stream. That is
// evidence about the transport, never about which endpoints the model
// implements.
func abortingSSEServer(t *testing.T, prefix string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = conn.Write([]byte(prefix))
		if tcp, ok := conn.(*net.TCPConn); ok {
			// A reset rather than a clean FIN, so the client reads an error
			// instead of an orderly EOF.
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isUnsupportedEndpoint reports whether err is the endpoint-capability
// sentinel, which is the one terminal error that routes the caller off this
// endpoint entirely.
func isUnsupportedEndpoint(err error) bool {
	_, ok := errors.AsType[*llm.UnsupportedEndpointError](err)
	return ok
}

// streamTerminalError drains a stream and returns its terminal error event.
func streamTerminalError(t *testing.T, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	return streamTerminalErrorWithClient(t, srv.Client(), srv, req)
}
func streamTerminalErrorWithClient(t *testing.T, client *http.Client, srv *httptest.Server, req llm.Request) error {
	t.Helper()
	s, err := (&Protocol{Client: client}).Stream(t.Context(), req, liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var terminal error
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			terminal = ev.Err
		}
	}
	return terminal
}

// TestStreamTerminalErrorsSeparateTheFourEndings pins the classification the
// Responses transport needs and the shared runner's single IncompleteMsg
// cannot express. Only one of the four says anything about endpoint support:
// a clean close with nothing this decoder recognizes. A stall, a broken read
// and a truncated-but-real stream are all evidence about the transport or the
// provider, and misreading any of them as "this model has no /v1/responses"
// sends the request to an endpoint that cannot serve it (#484).
func TestStreamTerminalErrorsSeparateTheFourEndings(t *testing.T) {
	t.Run("stall", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			req := userReq("hi")
			req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
			client, srv := stallingSSEClient(t, twoResponsesEvents)
			err := streamTerminalErrorWithClient(t, client, srv, req)
			if err == nil || !strings.Contains(err.Error(), "responses stream stalled without completion") {
				t.Fatalf("stalled stream error = %v", err)
			}
			if !errors.Is(err, llm.ErrResponseIdleTimeout) {
				t.Fatalf("a stall must carry the response-byte idle timeout as its cause: %v", err)
			}
			if isUnsupportedEndpoint(err) {
				t.Fatalf("silence is not evidence about endpoint support: %v", err)
			}
		})
	})

	t.Run("read failure", func(t *testing.T) {
		err := streamTerminalError(t, abortingSSEServer(t, twoResponsesEvents), userReq("hi"))
		if err == nil || !strings.Contains(err.Error(), "responses stream ended without completion") {
			t.Fatalf("broken stream error = %v", err)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a broken read is not evidence about endpoint support: %v", err)
		}
	})

	t.Run("no responses events", func(t *testing.T) {
		// A clean 200 carrying only events this decoder does not recognize as
		// the Responses API: the model likely does not implement /v1/responses.
		srv, _ := server(t, 200, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
		err := streamTerminalError(t, srv, userReq("hi"))
		if err == nil || !strings.Contains(err.Error(), "responses stream closed with no events") {
			t.Fatalf("empty stream error = %v", err)
		}
		if !isUnsupportedEndpoint(err) {
			t.Fatalf("a clean close with no Responses event is the endpoint sentinel: %v", err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		// Real Responses events, then a clean close with no response.completed.
		srv, _ := server(t, 200, twoResponsesEvents)
		err := streamTerminalError(t, srv, userReq("hi"))
		if err == nil || !strings.Contains(err.Error(), "responses stream closed before response.completed") {
			t.Fatalf("truncated stream error = %v", err)
		}
		if isUnsupportedEndpoint(err) {
			t.Fatalf("a stream that produced Responses events proves the endpoint served it: %v", err)
		}
	})
}
