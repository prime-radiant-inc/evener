package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
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

// roundTripStatus maps a fuzzer byte onto an HTTP status: 0 -> 200 (the only
// success status the Chat Completions path recognizes), otherwise a spread across
// 200..599 to exercise the non-200 error-mapping branch.
func roundTripStatus(statusSel byte) int {
	if statusSel == 0 {
		return http.StatusOK
	}
	return 200 + int(statusSel)%400
}

// newRoundTripAdapter builds an openaicompat Adapter wired to the fake transport
// through the exported Client field. Adaptive is left false so Complete/Stream go
// straight to the Chat Completions path — a single request against a stable URL,
// which keeps the request-body differential clean (no Responses-API probe first).
func newRoundTripAdapter(rt *captureRoundTripper) *Adapter {
	return &Adapter{
		name:     "compat",
		APIKey:   "k",
		BaseURL:  "https://api.compat.test/v1",
		Client:   &http.Client{Transport: rt},
		Adaptive: false,
	}
}

// FuzzOpenaicompatCompleteRoundTrip drives the openaicompat adapter's full
// non-streaming Complete round-trip — request build -> HTTP transport ->
// response/error decode — over a fake http.RoundTripper injected via the exported
// Adapter.Client field (the established stdlib seam; no new production interface).
// Both the llm.Request and the wire response are fuzzed. req.AdapterTimeout is
// never set, so ClientWithConnectTimeout keeps the injected transport live rather
// than swapping in a real dialing one.
//
// Oracles:
//   - Behavior preservation (differential): when the request is buildable, the
//     bytes the transport RECEIVES are byte-identical to json.Marshal of the
//     direct buildRequestBody(req, false) path, sent as POST to the
//     chat/completions URL — proving the seam changed nothing about request
//     assembly. When the request is NOT buildable, Complete errors before touching
//     the transport.
//   - Decode safety (floor): Complete never panics for any status/body; a non-200
//     status always yields a non-nil error; a 200 status either errors cleanly
//     (undecodable / choiceless body) or yields a Response stamped with the
//     openai-compatible provider.
func FuzzOpenaicompatCompleteRoundTrip(f *testing.F) {
	ok := []byte(`{"id":"c1","model":"m","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	f.Add("compat-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0), ok)
	f.Add("glm-4", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3), byte(0), []byte(`{"choices":[]}`))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(44), []byte(`{"error":{"message":"bad"}}`)) // 4xx
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(20), byte(4), []byte(`not json`))
	f.Add("m", "s", "u", []byte(`{"a":1}`), []byte(`{}`), byte(1), byte(0), []byte("{\"choices\":\xff}"))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, statusSel byte, respBody []byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		status := roundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := newRoundTripAdapter(rt)

		// Reference: the body the direct (pre-seam) build path produces for the
		// same request. buildRequestBody is exactly what completeViaChatCompletions
		// marshals before the HTTP call.
		wantBody, buildErr := buildRequestBody(req, false, a.compatFor(req.Model))
		var wantBytes []byte
		var marshalErr error
		if buildErr == nil {
			wantBytes, marshalErr = json.Marshal(wantBody)
		}

		resp, err := a.Complete(context.Background(), req)

		if buildErr != nil || marshalErr != nil {
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
		wantURL := a.BaseURL + "/chat/completions"
		if rt.gotURL != wantURL {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, wantURL)
		}
		if !json.Valid(rt.gotBody) {
			t.Fatalf("transport received non-JSON request body: %q", rt.gotBody)
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("seam differential: transport body != direct buildRequestBody path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
		}

		// Only HTTP 200 is treated as success; every other status maps to an error.
		if status != http.StatusOK {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, respBody)
			}
			return
		}
		if err != nil {
			return // an undecodable / choiceless 200 body is an acceptable clean error
		}
		if resp.Provider != "openai-compatible" {
			t.Fatalf("Complete on 200: provider = %q, want \"openai-compatible\" (body %q)", resp.Provider, respBody)
		}
	})
}

// FuzzOpenaicompatStreamRoundTrip drives the adapter's full Stream round-trip over
// the same fake transport: request build (with stream=true) -> client.Do ->
// decodeStream goroutine -> ChanStream. The fuzzed response body is replayed as
// the SSE payload. Adaptive is false, so streamViaChatCompletions runs directly.
//
// Oracles:
//   - Behavior preservation (differential): the request the transport receives is
//     a well-formed POST to the chat/completions URL whose body is byte-identical
//     to json.Marshal of the direct buildRequestBody(req, true) path.
//   - Floor: on a non-200 status the Stream call returns a clean error (no panic);
//     on 200, draining the event stream to completion never panics and always
//     terminates.
func FuzzOpenaicompatStreamRoundTrip(f *testing.F) {
	sse := []byte(`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n")
	f.Add("compat-test", "be terse", "hi", []byte(`{"city":"paris"}`), []byte(`{"type":"object"}`), byte(0), byte(0), sse)
	f.Add("glm-4", "", "", []byte(`{}`), []byte(`null`), byte(3), byte(44), []byte(`{"error":{"message":"bad"}}`)) // 4xx
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(0), []byte(``))
	f.Add("", "s", "u", []byte(``), []byte(`{}`), byte(20), byte(0), []byte("garbage\n\ndata: nope\n\n"))
	f.Add("m", "s", "u", []byte(`{"a":1}`), []byte(`{}`), byte(1), byte(0), []byte("data: {\"choices\":\xff}\n\n"))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, statusSel byte, respBody []byte) {
		req := buildFuzzRequest(model, system, user, toolArgs, toolParams, sel)
		status := roundTripStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := newRoundTripAdapter(rt)

		// The streaming build path marshals buildRequestBody(req, true) directly as
		// the wire body — including "stream":true and stream_options.
		wantBody, buildErr := buildRequestBody(req, true, a.compatFor(req.Model))

		stream, err := a.Stream(context.Background(), req)
		if buildErr != nil {
			if rt.calls != 0 {
				t.Fatalf("unbuildable stream request reached the transport (%d calls); build err=%v", rt.calls, buildErr)
			}
			if err == nil {
				t.Fatalf("Stream returned nil error for unbuildable request (build err=%v)", buildErr)
			}
			return
		}
		if err != nil {
			// A non-200 status is surfaced as a clean error here; the request must
			// still have been the well-formed chat/completions request.
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
		t.Fatalf("buildable stream request never reached the transport")
	}
	if rt.gotMethod != http.MethodPost {
		t.Fatalf("stream transport method = %q, want POST", rt.gotMethod)
	}
	wantURL := a.BaseURL + "/chat/completions"
	if rt.gotURL != wantURL {
		t.Fatalf("stream transport URL = %q, want %q", rt.gotURL, wantURL)
	}
	wantBytes, err := json.Marshal(wantBody)
	if err != nil {
		return // an unmarshalable body would have failed the build path too
	}
	if !bytes.Equal(rt.gotBody, wantBytes) {
		t.Fatalf("stream seam differential: transport body != direct buildRequestBody(stream) path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
	}
}
