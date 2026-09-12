package protocolhttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/internal/transport"
	"primeradiant.com/evener/llm/registry"
)

// fuzzStreamDecoder stands in for a protocol's decodeStream: it honors the
// StreamDecoder contract (close the body and the stream, complete the
// attempt, cancel) without decoding anything, because the bytes under test
// on the streaming path are the ones Stream itself reads — the non-2xx body
// it classifies and json.Unmarshals before any decoder is reached.
func fuzzStreamDecoder(_ context.Context, cancel context.CancelFunc, resp *http.Response, s *llm.ChanStream, r *Result, attempt *transport.APIAttemptCapture) {
	defer func() {
		_ = resp.Body.Close()
		cancel()
		s.CloseSend()
	}()
	body, err := io.ReadAll(resp.Body)
	attempt.Complete(llm.APIAttemptResult{StatusCode: r.StatusCode, ResponseBody: body}, llm.APITimeoutNone, err, nil)
}

// FuzzProtocolHTTPExchange drives the shared HTTP exchange every protocol
// package dispatches through — Complete (exchange.go) and Stream (stream.go)
// — against a local httptest server replaying the fuzz bytes as the response
// body, with the status steered by the first byte and the arm by the second.
// The bytes reach both of the package's own json.Unmarshal sites: the
// response body Do decodes into the raw map a protocol's decoder receives,
// and the non-2xx body Stream decodes for the attempt record's evidence
// beside the classified error.
//
// Oracles:
//   - never panics for arbitrary (malformed, truncated, non-UTF8) bodies at
//     any status (floor);
//   - a non-2xx status always yields a non-nil error, never a half-built
//     result: Complete's decode callback never runs and Stream returns no
//     stream;
//   - a 2xx body that is not a JSON object is an error too, and the caller's
//     decode never sees it;
//   - a 2xx exchange that decodes returns a Response stamped with the
//     instance name, and its callback saw a non-nil raw object;
//   - every exchange completes exactly one API attempt, whose outcome agrees
//     with the status: a non-2xx is a provider rejection, a 2xx is not.
func FuzzProtocolHTTPExchange(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`),
		[]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`),
		[]byte(`{"error":{"message":"context length exceeded","code":"context_length_exceeded"}}`),
		[]byte(`{"id":"resp_1","usage":{"input_tokens":1e400}}`),
		[]byte(`{"id":"resp_1","output":`),
		[]byte(`{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{}}}}}}}}}}}`),
		[]byte(`[1,2,3]`),
		[]byte(`null`),
		[]byte(`"a string body"`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"id\":\xff}"),
	}
	for _, s := range seeds {
		f.Add(byte(0), byte(0), s)
		f.Add(byte(0), byte(1), s)
	}
	f.Add(byte(29), byte(0), []byte(`{"error":{"message":"quota exhausted"}}`)) // 429, Complete
	f.Add(byte(29), byte(1), []byte(`{"error":{"message":"quota exhausted"}}`)) // 429, Stream
	f.Add(byte(200), byte(0), []byte(`{"error":{"type":"invalid_request"}}`))   // 400
	f.Add(byte(44), byte(1), []byte("event: response.created\ndata: {}\n\n"))   // 244
	f.Add(byte(100), byte(0), []byte(`{"error":{"message":"upstream broke"}}`)) // 300
	f.Add(byte(255), byte(1), []byte(`{"id":"resp_1","status":"in_progress"}`)) // 455

	f.Fuzz(func(t *testing.T, statusSel, armSel byte, body []byte) {
		registerTestSchemes()
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		res := testRes(srv.URL, registry.AuthBearer)
		sink := &captureSink{}
		ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_protocolhttp_fuzz")), sink)
		newCall := func(operation string) *Call {
			return &Call{
				Operation: operation, EndpointFamily: "test", Method: http.MethodPost,
				URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"input": "hi"},
				Req: llm.Request{Model: "m"}, Res: res, Client: srv.Client(),
			}
		}
		ok2xx := status >= 200 && status < 300

		if armSel%2 == 0 {
			var decodeCalled bool
			var gotRaw map[string]any
			resp, err := Complete(ctx, newCall("test.complete"), func(raw map[string]any) (llm.Response, error) {
				decodeCalled, gotRaw = true, raw
				return llm.Response{Model: "m"}, nil
			})
			switch {
			case !ok2xx:
				if err == nil {
					t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, body)
				}
				if decodeCalled {
					t.Fatalf("Complete: decode ran for HTTP status %d (body %q)", status, body)
				}
			case err != nil:
				// A 2xx body that is not a JSON object is a decode failure,
				// and the caller's decode must not have seen it.
				if decodeCalled {
					t.Fatalf("Complete: decode ran and the exchange still failed: %v (body %q)", err, body)
				}
			default:
				if !decodeCalled || gotRaw == nil {
					t.Fatalf("Complete: 2xx succeeded without a decoded object (called=%v raw=%v body=%q)", decodeCalled, gotRaw, body)
				}
				if resp.Provider != res.Instance {
					t.Fatalf("Complete on 2xx: provider = %q, want %q (body %q)", resp.Provider, res.Instance, body)
				}
			}
		} else {
			stream, err := Stream(ctx, newCall("test.stream"), fuzzStreamDecoder)
			switch {
			case !ok2xx:
				if err == nil {
					t.Fatalf("Stream: nil error for HTTP status %d (body %q)", status, body)
				}
				if stream != nil {
					t.Fatalf("Stream: stream returned beside the error for HTTP status %d (body %q)", status, body)
				}
			case err != nil:
				t.Fatalf("Stream: error on HTTP 2xx: %v (body %q)", err, body)
			default:
				for range stream.Events() { //nolint:revive // Drain so the decoder finishes.
				}
			}
		}

		llm.WaitForPriorAPIAttempts(ctx)
		attempts := sink.records()
		if len(attempts) != 1 {
			t.Fatalf("attempts = %d, want exactly one completed attempt (status %d, body %q)", len(attempts), status, body)
		}
		if got := attempts[0].Outcome; (got == apilog.AttemptProviderReject) != !ok2xx {
			t.Fatalf("attempt outcome = %q for HTTP status %d (body %q)", got, status, body)
		}
	})
}
