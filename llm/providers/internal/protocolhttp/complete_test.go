package protocolhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// completeResult bundles what a TestComplete case needs to assert: the
// resolved record the call ran against (for Instance/Endpoint), Complete's
// outcome, the raw body the decode callback saw, and the attempt-logging
// context/sink so a case can check the completed attempt record.
type completeResult struct {
	res  registry.Resolved
	resp llm.Response
	err  error
	raw  map[string]any
	ctx  context.Context
	sink *captureSink
}

// TestComplete pins the contract of Complete (exchange.go), the shared
// non-streaming completion exchange the four protocol packages call
// (responses, chatcompletions, anthropic, google): the decode callback
// receives the response body and runs only on a 2xx JSON object; a
// successful Response comes back with Provider and the endpoint URL
// stamped the way every Do finish callback is stamped; a non-2xx status
// is classified instead of decoded; and a decode error propagates
// unchanged — neither wrapped nor reprovidered.
//
// Complete itself carries no branch on RequiresStreamingComplete (spec
// §9.5) — every call site checks that before ever calling Complete
// (responses/complete.go, chatcompletions/complete.go,
// anthropic/protocol_transport.go, google/protocol_transport.go). The
// last case proves Complete runs its ordinary POST-and-decode exchange
// even for a scheme that requires streaming: that routing is the
// caller's job, not Complete's.
func TestComplete(t *testing.T) {
	registerTestSchemes()
	// wantDecodeErr already implements llm.Error with its own Provider; the
	// status and message it carries are arbitrary fixture data — only
	// whether Complete leaves Provider() alone is under test.
	wantDecodeErr := llm.ErrorFromHTTPStatus("decode-provider", 500, "malformed content", nil, nil)

	cases := []struct {
		name           string
		auth           string // "" defaults to registry.AuthBearer
		handler        http.HandlerFunc
		decodeErr      error
		wantDecodeCall bool
		check          func(t *testing.T, r completeResult)
	}{
		{
			name: "success stamps provider, endpoint URL, and rate limit",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("x-ratelimit-remaining-requests", "7")
				_, _ = w.Write([]byte(`{"id":"r1"}`))
			},
			wantDecodeCall: true,
			check: func(t *testing.T, r completeResult) {
				if r.err != nil {
					t.Fatalf("err = %v", r.err)
				}
				if r.raw["id"] != "r1" {
					t.Fatalf("decode saw raw = %+v", r.raw)
				}
				if r.resp.Provider != r.res.Instance {
					t.Fatalf("provider = %q, want %q", r.resp.Provider, r.res.Instance)
				}
				epURL, _ := r.resp.Raw["endpoint_url"].(string)
				if !strings.HasSuffix(epURL, "/responses") {
					t.Fatalf("endpoint_url = %v", r.resp.Raw["endpoint_url"])
				}
				if r.resp.RateLimit == nil || r.resp.RateLimit.RequestsRemaining == nil || *r.resp.RateLimit.RequestsRemaining != 7 {
					t.Fatalf("rate limit = %+v", r.resp.RateLimit)
				}
				llm.WaitForPriorAPIAttempts(r.ctx)
				if len(r.sink.attempts) != 1 || r.sink.attempts[0].Response == nil || r.sink.attempts[0].Response.StatusCode == nil || *r.sink.attempts[0].Response.StatusCode != 200 {
					t.Fatalf("attempts = %+v", r.sink.attempts)
				}
			},
		},
		{
			name: "429 is classified and decode is not invoked",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
			},
			wantDecodeCall: false,
			check: func(t *testing.T, r completeResult) {
				var le llm.Error
				if !errors.As(r.err, &le) {
					t.Fatalf("err = %v, not an llm.Error", r.err)
				}
				if le.StatusCode() != http.StatusTooManyRequests || le.Provider() != r.res.Instance || llm.Kind(r.err) != llm.KindRateLimit {
					t.Fatalf("status = %d provider = %q kind = %v", le.StatusCode(), le.Provider(), llm.Kind(r.err))
				}
				if r.resp.Provider != "" {
					t.Fatalf("resp = %+v, want zero value on a classified error", r.resp)
				}
				assertOneAttemptCompleted(r.ctx, t, r.sink, http.StatusTooManyRequests, r.err)
			},
		},
		{
			name: "decode error propagates unwrapped and unstamped",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"r1"}`))
			},
			decodeErr:      wantDecodeErr,
			wantDecodeCall: true,
			check: func(t *testing.T, r completeResult) {
				if r.raw["id"] != "r1" {
					t.Fatalf("decode saw raw = %+v", r.raw)
				}
				var le llm.Error
				if !errors.Is(r.err, wantDecodeErr) || !errors.As(r.err, &le) || le.Provider() != "decode-provider" {
					t.Fatalf("err = %v, want the decode error unchanged (Provider must stay %q, not %q)", r.err, "decode-provider", r.res.Instance)
				}
				if r.resp.Provider != "" {
					t.Fatalf("resp = %+v, want zero value on a decode error", r.resp)
				}
				assertOneAttemptCompleted(r.ctx, t, r.sink, http.StatusOK, r.err)
			},
		},
		{
			name: "RequiresStreamingComplete has no effect on Complete itself",
			auth: "test-runner-preparer",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"id":"r1"}`))
			},
			wantDecodeCall: true,
			check: func(t *testing.T, r completeResult) {
				if !RequiresStreamingComplete(r.res) {
					t.Fatal("fixture must require streaming completion, or this case proves nothing")
				}
				if r.err != nil {
					t.Fatalf("err = %v; Complete must still run its ordinary POST-and-decode exchange", r.err)
				}
				if r.raw["id"] != "r1" {
					t.Fatalf("decode saw raw = %+v", r.raw)
				}
				if r.resp.Provider != r.res.Instance {
					t.Fatalf("provider = %q, want %q", r.resp.Provider, r.res.Instance)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			auth := tc.auth
			if auth == "" {
				auth = registry.AuthBearer
			}
			res := testRes(srv.URL, auth)
			sink := &captureSink{}
			ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_complete")), sink)
			call := &Call{Operation: "test.complete", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"input": "hi"}, Req: llm.Request{Model: "m"}, Res: res, Client: srv.Client()}
			var decodeCalled bool
			var gotRaw map[string]any
			resp, err := Complete(ctx, call, func(raw map[string]any) (llm.Response, error) {
				decodeCalled = true
				gotRaw = raw
				if tc.decodeErr != nil {
					return llm.Response{}, tc.decodeErr
				}
				return llm.Response{Model: "m"}, nil
			})
			if decodeCalled != tc.wantDecodeCall {
				t.Fatalf("decode called = %v, want %v", decodeCalled, tc.wantDecodeCall)
			}
			tc.check(t, completeResult{res: res, resp: resp, err: err, raw: gotRaw, ctx: ctx, sink: sink})
		})
	}
}
