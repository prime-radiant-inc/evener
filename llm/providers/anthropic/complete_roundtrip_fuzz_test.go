package anthropic

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
// non-nil, readable Body and a nil error, and it drains and closes the request
// body — so any panic reproduced through it is a real adapter bug, never a
// harness artifact.
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

// FuzzAnthropicCompleteRoundTrip drives the anthropic adapter's full
// non-streaming Complete round-trip — request build -> HTTP transport ->
// response/error decode — over a fake http.RoundTripper injected via the exported
// Adapter.Client field (the established stdlib seam; no new interface). Both the
// llm.Request and the wire response are fuzzed.
//
// Oracles:
//   - Behavior preservation (differential): when the request is buildable, the
//     bytes the transport RECEIVES are byte-identical to json.Marshal of the
//     direct buildRequestBody path, sent as POST to BaseURL+"/v1/messages" —
//     proving the transport seam changed nothing about request assembly. When the
//     request is NOT buildable, Complete returns an error before touching the
//     transport.
//   - Decode safety (floor): Complete never panics for any status/body; a non-2xx
//     status always yields a non-nil error; a 2xx status either errors cleanly
//     (undecodable body) or yields a Response stamped with the anthropic provider.
func FuzzAnthropicCompleteRoundTrip(f *testing.F) {
	ok := []byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
	f.Add("claude-test", "sys", "hi", "shell", byte(1), 0.5, byte(0), ok)
	f.Add("claude-test", "", "hello", "", byte(0), 0.0, byte(0), []byte(`{}`))
	f.Add("claude-test", "s", "u", "t", byte(3), 1.0, byte(44), []byte(`{"error":{"message":"bad"}}`)) // 4xx
	f.Add("claude-test", "", "", "", byte(2), 0.0, byte(4), []byte(`not json`))
	f.Add("m", "s", "u", "shell", byte(1), 0.0, byte(0), []byte("{\"content\":\xff}"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, effortSel byte, temp float64, statusSel byte, respBody []byte) {
		req := fuzzRoundTripRequest(model, sys, user, toolName, effortSel, temp)
		status := fuzzRoundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := &Adapter{
			APIKey:  "k",
			BaseURL: "https://api.anthropic.test",
			Client:  &http.Client{Transport: rt},
		}

		// Reference: what the direct path builds for the same request.
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
		wantURL := a.BaseURL + "/v1/messages"
		if rt.gotURL != wantURL {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, wantURL)
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
		if resp.Provider != "anthropic" {
			t.Fatalf("Complete on 2xx: provider = %q, want \"anthropic\" (body %q)", resp.Provider, respBody)
		}
	})
}

// FuzzAnthropicStreamRoundTrip drives the adapter's full Stream round-trip over
// the same fake transport: request build (with stream=true) -> client.Do ->
// decodeStream goroutine -> proxy channel. The fuzzed response body is replayed
// as the SSE payload.
//
// Oracles:
//   - the request the transport receives is a well-formed POST to
//     BaseURL+"/v1/messages" whose body is the direct buildRequestBody path plus
//     "stream":true (behavior preservation);
//   - draining the event stream to completion never panics and always terminates
//     (floor); a non-2xx status yields a clean error (no stream) rather than a
//     panic.
func FuzzAnthropicStreamRoundTrip(f *testing.F) {
	sse := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n")
	f.Add("claude-test", "sys", "hi", "shell", byte(1), 0.0, byte(0), sse)
	f.Add("claude-test", "", "hello", "", byte(0), 0.0, byte(44), []byte(`{"error":{"message":"bad"}}`))
	f.Add("claude-test", "", "", "", byte(2), 0.0, byte(0), []byte(``))
	f.Add("m", "s", "u", "t", byte(3), 0.0, byte(0), []byte("garbage\n\ndata: nope\n\n"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, effortSel byte, temp float64, statusSel byte, respBody []byte) {
		req := fuzzRoundTripRequest(model, sys, user, toolName, effortSel, temp)
		status := fuzzRoundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := &Adapter{
			APIKey:  "k",
			BaseURL: "https://api.anthropic.test",
			Client:  &http.Client{Transport: rt},
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
			// A hard non-2xx from the Messages API is surfaced here as a clean
			// error, not a panic. The request must still have been the well-formed
			// streaming request.
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
		return // request build succeeded but transport not reached — nothing to assert
	}
	if rt.gotMethod != http.MethodPost {
		t.Fatalf("stream transport method = %q, want POST", rt.gotMethod)
	}
	wantURL := a.BaseURL + "/v1/messages"
	if rt.gotURL != wantURL {
		t.Fatalf("stream transport URL = %q, want %q", rt.gotURL, wantURL)
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
		t.Fatalf("stream seam differential: request body != buildRequestBody+stream\n got: %s\nwant: %s", rt.gotBody, wantBytes)
	}
}
