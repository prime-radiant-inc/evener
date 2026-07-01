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

// countRoundTripper is a fake http.RoundTripper that replays a fuzzer-controlled
// status+body and records the first request. It honors the RoundTripper contract
// (drains and closes the request body; always returns a non-nil *http.Response
// with a readable Body and a nil error) so any panic reproduced through it is a
// real adapter bug, never a harness artifact.
type countRoundTripper struct {
	status int
	body   []byte

	calls     int
	gotMethod string
	gotURL    string
	gotBody   []byte
}

func (rt *countRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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

func countFuzzStatus(sel byte) int {
	if sel == 0 {
		return http.StatusOK
	}
	return 200 + int(sel)%400 // 200..599
}

// FuzzAnthropicCountInputTokensRoundTrip drives the anthropic adapter's
// CountInputTokens round-trip — request build, sampling-field stripping, HTTP
// transport, and response/error decode (json.Unmarshal, intFromAny,
// ErrorFromHTTPStatus) — over a fake http.RoundTripper injected via the exported
// Adapter.Client field. Both the llm.Request and the wire response bytes are
// fuzzed. No real network is touched.
//
// Oracles:
//   - Behavior preservation: when the request is buildable, the transport
//     receives exactly one well-formed POST to the count_tokens endpoint with a
//     JSON body matching buildRequestBody minus the sampling-only fields.
//   - Decode safety: CountInputTokens never panics; a non-2xx status always
//     yields a non-nil error; a 2xx status either errors cleanly or returns an
//     exact, provider-sourced count stamped with the provider and request model.
//   - Determinism: the same request+response yields an identical count.
func FuzzAnthropicCountInputTokensRoundTrip(f *testing.F) {
	ok := []byte(`{"input_tokens":123}`)
	f.Add("claude-opus-4-5", "sys", "hello", "lookup", byte(0), ok)
	f.Add("claude-opus-4-5", "", "hi", "", byte(0), []byte(`{"input_tokens":"77"}`))
	f.Add("claude-opus-4-5", "s", "u", "t", byte(44), []byte(`{"error":{"type":"overloaded_error"}}`))
	f.Add("claude-opus-4-5", "", "", "", byte(4), []byte(`not json`))
	f.Add("m", "s", "u", "", byte(0), []byte("{\"input_tokens\":\xff}"))
	f.Add("m", "s", "u", "", byte(0), []byte(`{"input_tokens":-9}`))

	f.Fuzz(func(t *testing.T, model, sys, user, toolName string, statusSel byte, respBody []byte) {
		req := llm.Request{Model: model}
		if sys != "" {
			req.Messages = append(req.Messages, llm.System(sys))
		}
		if user != "" {
			req.Messages = append(req.Messages, llm.User(user))
		}
		if toolName != "" {
			req.Tools = []llm.ToolDefinition{{
				Name:       toolName,
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
			}}
		}
		status := countFuzzStatus(statusSel)

		newAdapter := func() (*Adapter, *countRoundTripper) {
			rt := &countRoundTripper{status: status, body: respBody}
			return &Adapter{
				APIKey:  "k",
				BaseURL: "https://api.anthropic.test",
				Client:  &http.Client{Transport: rt},
			}, rt
		}

		a, rt := newAdapter()

		// Reference: buildRequestBody minus the sampling-only fields the counter
		// strips before sending.
		wantBody, buildErr := a.buildRequestBody(req)
		var wantBytes []byte
		var marshalErr error
		if buildErr == nil {
			for _, k := range []string{"max_tokens", "temperature", "top_p", "stop_sequences", "service_tier", "cache_control"} {
				delete(wantBody, k)
			}
			wantBytes, marshalErr = json.Marshal(wantBody)
		}

		got, err := a.CountInputTokens(context.Background(), req)

		if buildErr != nil || marshalErr != nil {
			if rt.calls != 0 {
				t.Fatalf("unbuildable request reached the transport (%d calls)", rt.calls)
			}
			if err == nil {
				t.Fatalf("nil error for unbuildable request (build=%v marshal=%v)", buildErr, marshalErr)
			}
			return
		}

		if rt.calls == 0 {
			t.Fatalf("buildable request never reached the transport (err=%v)", err)
		}
		if rt.gotMethod != http.MethodPost {
			t.Fatalf("transport method = %q, want POST", rt.gotMethod)
		}
		wantURL := a.BaseURL + "/v1/messages/count_tokens"
		if rt.gotURL != wantURL {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, wantURL)
		}
		if !json.Valid(rt.gotBody) {
			t.Fatalf("transport received non-JSON body: %q", rt.gotBody)
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("seam differential: transport body != stripped buildRequestBody\n got: %s\nwant: %s", rt.gotBody, wantBytes)
		}

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("nil error for HTTP status %d (body %q)", status, respBody)
			}
			return
		}
		if err != nil {
			return // an undecodable 2xx body producing a clean error is acceptable
		}
		if !got.Exact || got.Source != llm.TokenCountSourceProvider {
			t.Fatalf("2xx count not exact/provider-sourced: %+v (body %q)", got, respBody)
		}
		if got.Provider != a.Name() {
			t.Fatalf("2xx count provider = %q, want %q", got.Provider, a.Name())
		}
		if got.Model != model {
			t.Fatalf("2xx count model = %q, want %q", got.Model, model)
		}

		a2, _ := newAdapter()
		got2, err2 := a2.CountInputTokens(context.Background(), req)
		if err2 != nil || got2.Tokens != got.Tokens {
			t.Fatalf("count not deterministic: %d/%v vs %d", got.Tokens, err2, got2.Tokens)
		}
	})
}
