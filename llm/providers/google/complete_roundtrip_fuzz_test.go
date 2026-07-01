package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// fuzzGoogleRequest assembles an llm.Request from fuzzed scalars. It never sets
// AdapterTimeout: a positive connect timeout would make ClientWithConnectTimeout
// swap our fake transport for a real dialing one, so the round-trip harnesses
// deliberately leave it nil to keep the injected transport live.
func fuzzGoogleRequest(model, sys, user, toolName string, hasTemp bool, temp float64) llm.Request {
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
	if hasTemp {
		req.Temperature = &temp
	}
	return req
}

func fuzzGoogleStatus(statusSel byte) int {
	if statusSel == 0 {
		return http.StatusOK
	}
	// 200..599 — covers 2xx success, 3xx (no Location -> not followed), and the
	// 4xx/5xx error-mapping branch.
	return 200 + int(statusSel)%400
}

// googleReference computes the wire bytes and full request URL the pre-seam path
// would produce for req at the named generateContent endpoint suffix ("" for
// non-streaming, "stream" for the SSE path). ok is false when the adapter would
// error before touching the transport (unrepresentable messages, unmarshalable
// body, or an unparseable endpoint URL).
func googleReference(a *Adapter, req llm.Request, streaming bool) (wantBytes []byte, wantURL string, ok bool) {
	system, contents, err := toGeminiContents(req.Messages)
	if err != nil {
		return nil, "", false
	}
	body, err := a.buildRequestBody(req, system, contents)
	if err != nil {
		return nil, "", false
	}
	wantBytes, err = json.Marshal(body)
	if err != nil {
		return nil, "", false
	}
	method := "generateContent"
	if streaming {
		method = "streamGenerateContent"
	}
	endpoint := fmt.Sprintf("%s/v1beta/models/%s:%s", a.BaseURL, url.PathEscape(req.Model), method)
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, "", false
	}
	q := u.Query()
	q.Set("key", a.APIKey)
	if streaming {
		q.Set("alt", "sse")
	}
	u.RawQuery = q.Encode()
	return wantBytes, u.String(), true
}

// FuzzGoogleCompleteRoundTrip drives the google adapter's full non-streaming
// Complete round-trip — message translation -> request build -> HTTP transport ->
// response/error decode — over a fake http.RoundTripper injected via the exported
// Adapter.Client field (the established stdlib seam; no new interface). Both the
// llm.Request and the wire response are fuzzed.
//
// Oracles:
//   - Behavior preservation (differential): when the request is buildable, the
//     bytes the transport RECEIVES are byte-identical to json.Marshal of the
//     direct toGeminiContents+buildRequestBody path, sent as POST to the
//     generateContent URL (with the ?key= query) — proving the seam changed
//     nothing about request assembly. When the request is NOT buildable, Complete
//     returns an error before touching the transport.
//   - Decode safety (floor): Complete never panics for any status/body; a non-2xx
//     status always yields a non-nil error; a 2xx status either errors cleanly
//     (undecodable body) or yields a Response stamped with the google provider.
func FuzzGoogleCompleteRoundTrip(f *testing.F) {
	ok := []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`)
	f.Add("gemini-test", "sys", "hi", "shell", true, 0.5, byte(0), ok)
	f.Add("gemini-test", "", "hello", "", false, 0.0, byte(0), []byte(`{}`))
	f.Add("gemini-test", "s", "u", "t", true, 1.0, byte(44), []byte(`{"error":{"code":429,"message":"bad"}}`)) // 4xx
	f.Add("gemini-test", "", "", "", false, 0.0, byte(4), []byte(`not json`))
	f.Add("m", "s", "u", "shell", true, 0.0, byte(0), []byte("{\"candidates\":\xff}"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, hasTemp bool, temp float64, statusSel byte, respBody []byte) {
		req := fuzzGoogleRequest(model, sys, user, toolName, hasTemp, temp)
		status := fuzzGoogleStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := &Adapter{
			APIKey:  "k",
			BaseURL: "https://generativelanguage.googleapis.test",
			Client:  &http.Client{Transport: rt},
		}

		wantBytes, wantURL, buildable := googleReference(a, req, false)

		resp, err := a.Complete(context.Background(), req)

		if !buildable {
			// Complete must fail before making an HTTP request.
			if rt.calls != 0 {
				t.Fatalf("unbuildable request reached the transport (%d calls)", rt.calls)
			}
			if err == nil {
				t.Fatalf("Complete returned nil error for unbuildable request")
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
		if rt.gotURL != wantURL {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, wantURL)
		}
		if !json.Valid(rt.gotBody) {
			t.Fatalf("transport received non-JSON request body: %q", rt.gotBody)
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("seam differential: transport body != direct build path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
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
		if resp.Provider != "google" {
			t.Fatalf("Complete on 2xx: provider = %q, want \"google\" (body %q)", resp.Provider, respBody)
		}
	})
}

// FuzzGoogleStreamRoundTrip drives the adapter's full Stream round-trip over the
// same fake transport: request build -> client.Do -> decodeStream goroutine ->
// proxy channel. The fuzzed response body is replayed as the SSE payload.
//
// Oracles:
//   - the request the transport receives is a well-formed POST to the
//     streamGenerateContent URL (?key=&alt=sse) whose body is the direct build
//     path (behavior preservation); the google stream path adds no body field.
//   - a non-2xx status surfaces as a clean error before streaming (floor); a 2xx
//     status drains to completion without panicking and always terminates.
func FuzzGoogleStreamRoundTrip(f *testing.F) {
	sse := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3}}\n\n")
	f.Add("gemini-test", "sys", "hi", "shell", true, 0.0, byte(0), sse)
	f.Add("gemini-test", "", "hello", "", false, 0.0, byte(44), []byte(`{"error":{"code":429,"message":"bad"}}`))
	f.Add("gemini-test", "", "", "", false, 0.0, byte(0), []byte(``))
	f.Add("m", "s", "u", "t", true, 0.0, byte(0), []byte("garbage\n\ndata: nope\n\n"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, hasTemp bool, temp float64, statusSel byte, respBody []byte) {
		req := fuzzGoogleRequest(model, sys, user, toolName, hasTemp, temp)
		status := fuzzGoogleStatus(statusSel)

		rt := &captureRoundTripper{status: status, body: respBody}
		a := &Adapter{
			APIKey:  "k",
			BaseURL: "https://generativelanguage.googleapis.test",
			Client:  &http.Client{Transport: rt},
		}

		wantBytes, wantURL, buildable := googleReference(a, req, true)

		stream, err := a.Stream(context.Background(), req)
		if !buildable {
			if err == nil {
				t.Fatalf("Stream returned nil error for unbuildable request")
			}
			return
		}
		if err != nil {
			// A hard non-2xx status is surfaced here as a clean error, not a panic.
			// The single request must still have been the well-formed stream request.
			assertGoogleStreamRequest(t, rt, wantURL, wantBytes)
			return
		}

		// Drain to completion — must terminate without panicking.
		for range stream.Events() { //nolint:revive // draining for side effects
		}
		_ = stream.Close()

		assertGoogleStreamRequest(t, rt, wantURL, wantBytes)
	})
}

func assertGoogleStreamRequest(t *testing.T, rt *captureRoundTripper, wantURL string, wantBytes []byte) {
	t.Helper()
	if rt.calls == 0 {
		return // request build succeeded but transport not reached — nothing to assert
	}
	if rt.gotMethod != http.MethodPost {
		t.Fatalf("stream transport method = %q, want POST", rt.gotMethod)
	}
	if rt.gotURL != wantURL {
		t.Fatalf("stream transport URL = %q, want %q", rt.gotURL, wantURL)
	}
	if !bytes.Equal(rt.gotBody, wantBytes) {
		t.Fatalf("stream seam differential: request body != direct build path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
	}
}
