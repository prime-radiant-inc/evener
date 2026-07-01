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

// FuzzOpenAICountInputTokensRoundTrip drives the openai adapter's
// CountInputTokens round-trip — request build, field stripping
// (stripOpenAIInputTokenCountOutputFields), HTTP transport, and response/error
// decode (json.NewDecoder+UseNumber, IntFromAny, ErrorFromHTTPStatus) — over a
// fake http.RoundTripper injected via the exported Adapter.Client field. Both the
// llm.Request and the wire response bytes are fuzzed. No real network is touched.
//
// Oracles:
//   - Behavior preservation: when the request is buildable, the transport
//     receives exactly one well-formed POST to inputTokensURL() with a JSON body
//     that matches the direct buildInputTokenCountBody path, and that stripped
//     body carries none of the output-only fields.
//   - Decode safety: CountInputTokens never panics for any status/body; a non-2xx
//     status always yields a non-nil error; a 2xx status either errors cleanly or
//     returns an exact, provider-sourced count stamped with the openai provider
//     and request model.
//   - Determinism: the same request+response yields an identical count.
func FuzzOpenAICountInputTokensRoundTrip(f *testing.F) {
	ok := []byte(`{"object":"response.input_tokens","input_tokens":456}`)
	f.Add("gpt-5.2", "sys", "hello", "lookup", byte(0), ok)
	f.Add("gpt-5.2", "", "hi", "", byte(0), []byte(`{"input_tokens":"77"}`))
	f.Add("gpt-5.2", "s", "u", "t", byte(44), []byte(`{"error":{"message":"bad"}}`)) // 4xx-ish
	f.Add("gpt-5.2", "", "", "", byte(4), []byte(`not json`))
	f.Add("m", "s", "u", "", byte(0), []byte("{\"input_tokens\":\xff}"))
	f.Add("m", "s", "u", "", byte(0), []byte(`{"input_tokens":-5}`))

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
				BaseURL: "https://api.openai.test",
				Client:  &http.Client{Transport: rt},
			}, rt
		}

		a, rt := newAdapter()

		// Reference: what the direct build path produces for the same request.
		wantBody, buildErr := a.buildInputTokenCountBody(req)
		var wantBytes []byte
		var marshalErr error
		if buildErr == nil {
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
		if rt.gotURL != a.inputTokensURL() {
			t.Fatalf("transport URL = %q, want %q", rt.gotURL, a.inputTokensURL())
		}
		if !json.Valid(rt.gotBody) {
			t.Fatalf("transport received non-JSON body: %q", rt.gotBody)
		}
		if !bytes.Equal(rt.gotBody, wantBytes) {
			t.Fatalf("seam differential: transport body != buildInputTokenCountBody\n got: %s\nwant: %s", rt.gotBody, wantBytes)
		}
		// The stripped body must carry no output-only fields.
		var sent map[string]any
		if json.Unmarshal(rt.gotBody, &sent) == nil {
			for _, k := range []string{"max_output_tokens", "temperature", "top_p", "store", "stop", "service_tier"} {
				if _, present := sent[k]; present {
					t.Fatalf("stripped body still carries output-only field %q: %s", k, rt.gotBody)
				}
			}
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

		// Determinism: a fresh adapter with identical inputs agrees on the count.
		a2, _ := newAdapter()
		got2, err2 := a2.CountInputTokens(context.Background(), req)
		if err2 != nil || got2.Tokens != got.Tokens {
			t.Fatalf("count not deterministic: %d/%v vs %d", got.Tokens, err2, got2.Tokens)
		}
	})
}
