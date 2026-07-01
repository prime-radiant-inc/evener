package google

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// miscStreamRoundTripper is a fake http.RoundTripper honoring the RoundTripper
// contract: it drains + closes the request body, records the first request it
// sees, and replays a fuzzer-controlled status + body with a non-nil readable
// Body and nil error. Any panic reproduced through it is a real adapter bug.
type miscStreamRoundTripper struct {
	status int
	body   []byte

	calls     int
	gotMethod string
	gotURL    string
	gotBody   []byte
}

func (rt *miscStreamRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: rt.status,
		Status:     http.StatusText(rt.status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(rt.body)),
		Request:    req,
	}, nil
}

func miscStreamStatus(sel byte) int {
	if sel == 0 {
		return http.StatusOK
	}
	return 200 + int(sel)%400
}

func miscGoogleRequest(model, sys, user, toolName string) llm.Request {
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
	return req
}

// FuzzMiscGoogleStreamRoundTrip drives the Gemini adapter's full Stream
// round-trip — request build -> HTTP transport -> decodeStream goroutine ->
// proxy channel — over a fake http.RoundTripper injected via the exported
// Adapter.Client field. Both the llm.Request and the wire SSE response are
// fuzzed. This exercises Stream's own request-assembly, URL/query construction,
// and status-branching, which the direct decodeStream fuzzer bypasses.
//
// Oracles:
//   - request-shape differential: when the request is buildable, the bytes the
//     transport RECEIVES are byte-identical to json.Marshal of the direct
//     buildRequestBody path, sent as POST to the streamGenerateContent endpoint
//     with key + alt=sse query params. When it is NOT buildable, Stream returns
//     an error before touching the transport.
//   - status contract: a non-2xx status always yields a non-nil error; a 2xx
//     status yields a drainable stream that terminates without panicking.
func FuzzMiscGoogleStreamRoundTrip(f *testing.F) {
	sse := []byte(`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}` + "\n\n")
	f.Add("gemini-2.0", "sys", "hi", "shell", byte(0), sse)
	f.Add("gemini-2.0", "", "hello", "", byte(200), []byte(`{"error":{"message":"bad"}}`)) // -> HTTP 400, error branch
	f.Add("gemini-2.0", "s", "u", "", byte(229), []byte(`{"error":{"message":"rate"}}`))   // -> HTTP 429
	f.Add("m", "s", "u", "t", byte(0), []byte(""))
	f.Add("m/weird:name", "s", "u", "", byte(0), []byte("data: not-json\n\n"))
	f.Add("gemini", "", "", "", byte(0), []byte("garbage"))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, statusSel byte, respBody []byte) {
		req := miscGoogleRequest(model, sys, user, toolName)
		status := miscStreamStatus(statusSel)

		rt := &miscStreamRoundTripper{status: status, body: respBody}
		a := &Adapter{
			name:    "google",
			APIKey:  "k",
			BaseURL: "https://gen.googleapis.test",
			Client:  &http.Client{Transport: rt},
		}

		// Reference: what the pre-transport build path produces for this request.
		system, contents, contentsErr := toGeminiContents(req.Messages)
		var wantBytes []byte
		var buildErr error
		if contentsErr == nil {
			var wantBody map[string]any
			wantBody, buildErr = a.buildRequestBody(req, system, contents)
			if buildErr == nil {
				wantBytes, buildErr = json.Marshal(wantBody)
			}
		}
		unbuildable := contentsErr != nil || buildErr != nil

		stream, err := a.Stream(context.Background(), req)

		if unbuildable {
			if rt.calls != 0 {
				t.Fatalf("unbuildable request reached the transport (%d calls)", rt.calls)
			}
			if err == nil {
				t.Fatalf("Stream returned nil error for unbuildable request (contents err=%v build err=%v)", contentsErr, buildErr)
			}
			return
		}

		// Buildable: it must have reached the transport as a well-formed POST.
		if rt.calls == 0 {
			t.Fatalf("buildable request never reached the transport (err=%v)", err)
		}
		if rt.gotMethod != http.MethodPost {
			t.Fatalf("transport method = %q, want POST", rt.gotMethod)
		}
		gotURL, parseErr := url.Parse(rt.gotURL)
		if parseErr != nil {
			t.Fatalf("transport URL not parseable: %q: %v", rt.gotURL, parseErr)
		}
		if !strings.HasSuffix(gotURL.Path, ":streamGenerateContent") {
			t.Fatalf("transport path = %q, want a :streamGenerateContent suffix", gotURL.Path)
		}
		q := gotURL.Query()
		if q.Get("key") != a.APIKey {
			t.Fatalf("transport query key = %q, want %q", q.Get("key"), a.APIKey)
		}
		if q.Get("alt") != "sse" {
			t.Fatalf("transport query alt = %q, want sse", q.Get("alt"))
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("stream seam differential: transport body != direct buildRequestBody path\n got: %s\nwant: %s", rt.gotBody, wantBytes)
		}

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("Stream: nil error for HTTP status %d", status)
			}
			return
		}
		if err != nil {
			t.Fatalf("Stream: unexpected error on 2xx status: %v", err)
		}

		// Drain to completion — must terminate without panicking.
		for range stream.Events() { //nolint:revive // draining for side effects
		}
		_ = stream.Close()
	})
}
