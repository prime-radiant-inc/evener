package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"primeradiant.com/serf/llm"
)

// captureRoundTripper is a fake http.RoundTripper: it records the FIRST request
// it sees and replays fuzzer-controlled status+body for every request. It honors
// the RoundTripper contract — it always returns a non-nil *http.Response with a
// non-nil, readable Body and a nil error, so any panic reproduced through it is a
// real adapter bug, never a harness artifact.
type captureRoundTripper struct {
	status int
	body   []byte

	calls     int
	gotMethod string
	gotURL    string
	gotBody   []byte
}

func (rt *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// The transport owns draining and closing the request body.
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	if rt.calls == 0 {
		rt.gotMethod = req.Method
		rt.gotURL = req.URL.String()
		rt.gotBody = reqBody
	}
	rt.calls++

	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
		Request:    req,
	}, nil
}

// fuzzRoundTripRequest assembles an llm.Request from fuzzed scalars. It never
// sets AdapterTimeout: a positive connect timeout would make
// ClientWithConnectTimeout swap our fake transport for a real dialing one, so the
// round-trip harnesses deliberately leave it nil to keep the injected transport
// live.
func fuzzRoundTripRequest(model, sys, user, toolName string, effortSel byte, temp float64) llm.Request {
	req := llm.Request{Model: model}
	if sys != "" {
		req.Messages = append(req.Messages, llm.System(sys))
	}
	if user != "" {
		req.Messages = append(req.Messages, llm.User(user))
	}
	if toolName != "" {
		req.Tools = []llm.ToolDefinition{{
			Name:        toolName,
			Description: "fuzz tool",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}}
	}
	switch effortSel % 4 {
	case 1:
		e := "low"
		req.ReasoningEffort = &e
	case 2:
		e := "medium"
		req.ReasoningEffort = &e
	case 3:
		e := "high"
		req.ReasoningEffort = &e
	}
	if effortSel%2 == 0 {
		req.Temperature = &temp
	}
	return req
}

func fuzzRoundTripStatus(statusSel byte) int {
	if statusSel == 0 {
		return http.StatusOK
	}
	// 200..599 — covers 2xx success, 3xx (no Location -> not followed), and the
	// 4xx/5xx error-mapping branch.
	return 200 + int(statusSel)%400
}

// FuzzOpenAICompleteRoundTrip drives the openai adapter's full non-streaming
// Complete round-trip — request build -> HTTP transport -> response/error decode —
// over a fake http.RoundTripper injected via the exported Adapter.Client field
// (the established stdlib seam Plan 13 WS1 calls for; no new interface). Both the
// llm.Request and the wire response are fuzzed.
//
// Oracles:
//   - Behavior preservation (differential): when the request is buildable, the
//     bytes the transport RECEIVES are byte-identical to json.Marshal of the
//     direct buildRequestBody path, sent as POST to responsesURL() — proving the
//     seam changed nothing about request assembly. When the request is NOT
//     buildable, Complete returns an error before touching the transport.
//   - Decode safety (floor): Complete never panics for any status/body; a non-2xx
//     status always yields a non-nil error; a 2xx status either errors cleanly
//     (undecodable body) or yields a Response stamped with the openai provider.
func FuzzOpenAICompleteRoundTrip(f *testing.F) {
	ok := []byte(`{"id":"resp_1","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
	f.Add("gpt-test", "sys", "hi", "shell", byte(1), 0.5, byte(0), ok)
	f.Add("gpt-test", "", "hello", "", byte(0), 0.0, byte(0), []byte(`{}`))
	f.Add("gpt-test", "s", "u", "t", byte(3), 1.0, byte(44), []byte(`{"error":{"message":"bad"}}`)) // 4xx
	f.Add("gpt-test", "", "", "", byte(2), 0.0, byte(4), []byte(`not json`))
	f.Add("m", "s", "u", "shell", byte(1), 0.0, byte(0), []byte("{\"output\":\xff}"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, effortSel byte, temp float64, statusSel byte, respBody []byte) {
		req := fuzzRoundTripRequest(model, sys, user, toolName, effortSel, temp)
		status := fuzzRoundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{7}, 32))
		a := &Adapter{
			APIKey:              "k",
			BaseURL:             "https://api.openai.test",
			ContinuationHasher:  hasher,
			DisableChatFallback: true, // keep a single responses request for a clean differential
			Client:              &http.Client{Transport: rt},
		}

		// Reference: what the pre-seam path builds for the same request.
		wantBody, buildErr := a.buildRequestBody(req)
		var wantBytes []byte
		var marshalErr error
		if buildErr == nil {
			wantBytes, marshalErr = json.Marshal(wantBody)
		}

		resp, err := a.Complete(context.Background(), req)

		if buildErr != nil || marshalErr != nil {
			// Complete must fail before making an HTTP request.
			if rt.calls != 0 {
				t.Fatalf("unbuildable request reached the transport (%d calls); build err=%v marshal err=%v", rt.calls, buildErr, marshalErr)
			}
			if err == nil {
				t.Fatalf("Complete returned nil error for unbuildable request (build err=%v marshal err=%v)", buildErr, marshalErr)
			}
			return
		}

		// The request was buildable: it must have reached the transport.
		if rt.calls == 0 {
			t.Fatalf("buildable request never reached the transport (err=%v)", err)
		}
		if rt.gotMethod != http.MethodPost {
			t.Fatalf("transport method = %q, want POST", rt.gotMethod)
		}
		if rt.gotURL != a.responsesURL() {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, a.responsesURL())
		}
		if !json.Valid(rt.gotBody) {
			t.Fatalf("transport received non-JSON request body: %q", rt.gotBody)
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("seam differential: transport body != direct buildRequestBody path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
		}

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, respBody)
			}
			return
		}
		if err != nil {
			return // an undecodable 2xx body is an acceptable clean error
		}
		if resp.Provider != "openai" {
			t.Fatalf("Complete on 2xx: provider = %q, want \"openai\" (body %q)", resp.Provider, respBody)
		}
	})
}

// FuzzOpenAIStreamRoundTrip drives the adapter's full Stream round-trip over the
// same fake transport: request build (with stream=true) -> client.Do ->
// decodeStream goroutine -> proxy channel. The fuzzed response body is replayed as
// the SSE payload.
//
// Oracles:
//   - the FIRST request the transport receives is a well-formed POST to
//     responsesURL() whose body is the direct buildRequestBody path plus
//     "stream":true (behavior preservation);
//   - draining the event stream to completion never panics and always terminates
//     (floor). A silent-empty 200 stream may trigger the internal chat-completions
//     fallback (a second request through the same transport) — that is allowed;
//     only the first request is asserted.
func FuzzOpenAIStreamRoundTrip(f *testing.F) {
	sse := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n")
	f.Add("gpt-test", "sys", "hi", "shell", byte(1), 0.0, byte(0), sse)
	f.Add("gpt-test", "", "hello", "", byte(0), 0.0, byte(44), []byte(`{"error":{"message":"bad"}}`))
	f.Add("gpt-test", "", "", "", byte(2), 0.0, byte(0), []byte(``))
	f.Add("m", "s", "u", "t", byte(3), 0.0, byte(0), []byte("garbage\n\ndata: nope\n\n"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, effortSel byte, temp float64, statusSel byte, respBody []byte) {
		req := fuzzRoundTripRequest(model, sys, user, toolName, effortSel, temp)
		status := fuzzRoundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{7}, 32))
		a := &Adapter{
			APIKey:              "k",
			BaseURL:             "https://api.openai.test",
			ContinuationHasher:  hasher,
			DisableChatFallback: true,
			Client:              &http.Client{Transport: rt},
		}

		wantBody, buildErr := a.buildRequestBody(req)

		stream, err := a.Stream(context.Background(), req)
		if buildErr != nil {
			if err == nil {
				t.Fatalf("Stream returned nil error for unbuildable request (build err=%v)", buildErr)
			}
			return
		}
		if err != nil {
			// A hard non-2xx from the Responses API is surfaced here; with fallback
			// disabled that is a clean error, not a panic. The first request must
			// still have been the well-formed responses request.
			assertFirstStreamRequest(t, rt, a, wantBody)
			return
		}

		// Drain to completion — must terminate without panicking.
		for range stream.Events() { //nolint:revive // draining for side effects
		}
		_ = stream.Close()

		assertFirstStreamRequest(t, rt, a, wantBody)
	})
}

func assertFirstStreamRequest(t *testing.T, rt *captureRoundTripper, a *Adapter, wantBody map[string]any) {
	t.Helper()
	if rt.calls == 0 {
		return // request build succeeded but transport not reached (e.g. context) — nothing to assert
	}
	if rt.gotMethod != http.MethodPost {
		t.Fatalf("stream transport method = %q, want POST", rt.gotMethod)
	}
	if rt.gotURL != a.responsesURL() {
		t.Fatalf("stream transport URL = %q, want %q", rt.gotURL, a.responsesURL())
	}
	// The streaming path adds "stream":true to the same built body.
	streamBody := make(map[string]any, len(wantBody)+1)
	for k, v := range wantBody {
		streamBody[k] = v
	}
	streamBody["stream"] = true
	wantBytes, err := json.Marshal(streamBody)
	if err != nil {
		return // unmarshalable body would have failed the build path too
	}
	if !bytes.Equal(rt.gotBody, wantBytes) {
		t.Fatalf("stream seam differential: first request body != buildRequestBody+stream\n got: %s\nwant: %s", rt.gotBody, wantBytes)
	}
}
