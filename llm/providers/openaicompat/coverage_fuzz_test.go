package openaicompat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type coverageRoundTripper func(*http.Request) (*http.Response, error)

func (f coverageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type coverageErrorReader struct{ data string }

func (r *coverageErrorReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, errors.New("scripted read failure")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func (r *coverageErrorReader) Close() error { return nil }

func coverageAdapter(base string, rt http.RoundTripper) *Adapter {
	return &Adapter{BaseURL: base, Client: &http.Client{Transport: rt}}
}

func FuzzOpenAICompatCoverageUnion(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		t.Run("environment", func(t *testing.T) {
			t.Setenv(envvars.OpenAICompatibleBaseURL.Name, "")
			if _, err := NewFromEnv(); err == nil {
				t.Fatal("NewFromEnv accepted missing base URL")
			}
			if a, ok, err := newOpenAICompatEnvAdapter(llm.EnvConfig{}); a != nil || ok || err != nil {
				t.Fatalf("unset env factory = (%v, %v, %v)", a, ok, err)
			}
			t.Setenv(envvars.OpenAICompatibleBaseURL.Name, "https://example.invalid/v1")
			a, ok, err := newOpenAICompatEnvAdapter(llm.EnvConfig{})
			if a == nil || !ok || err != nil {
				t.Fatalf("set env factory = (%v, %v, %v)", a, ok, err)
			}
			if got := (&Adapter{}).Name(); got != "openai-compatible" {
				t.Fatalf("default name = %q", got)
			}
		})

		t.Run("configuration", func(t *testing.T) {
			v := true
			one := 1
			q := ApplyCompatConfig(ProviderQuirks{}, &providercfg.CompatConfig{
				SupportsStrictMode: &v, ChatTemplateKwargs: map[string]any{"x": true},
				LockTopP: &v, LockFrequencyPenalty: &v, LockPresencePenalty: &v,
				ToolChoiceAutoOnly: &v, MaxStopSequences: &one, StripEmptyContent: &v,
				NoJSONSchema: &v,
			})
			if q.SupportsStrictMode == nil || !q.LockTopP || !q.LockFrequencyPenalty || !q.LockPresencePenalty || !q.ToolChoiceAutoOnly || q.MaxStopSequences != 1 || !q.StripEmptyContent || !q.NoJSONSchema {
				t.Fatalf("overlay not applied: %#v", q)
			}
			mc := ModelCompat{ThinkingLevels: map[string]string{"low": "l"}}
			if got := mc.wireEffort("turbo"); got != "turbo" {
				t.Fatalf("wire effort = %q", got)
			}
			if got := (ProviderQuirks{}).mapFinishReason("custom"); got != "custom" {
				t.Fatalf("finish = %q", got)
			}
			if got := (ProviderQuirks{FinishReasonMap: map[string]string{}}).mapFinishReason("custom"); got != "custom" {
				t.Fatalf("unmapped finish = %q", got)
			}
		})

		t.Run("cache content", func(t *testing.T) {
			cc := map[string]any{"type": "ephemeral"}
			for _, tc := range []struct {
				content any
				want    bool
			}{
				{"", false},
				{[]map[string]any{{"type": "image_url"}, {"type": "text", "text": "x"}}, true},
				{[]map[string]any{{"type": "image_url"}}, false},
				{42, false},
			} {
				msg := map[string]any{"content": tc.content}
				if got := addCacheControlToTextContent(msg, cc); got != tc.want {
					t.Fatalf("content %#v = %v", tc.content, got)
				}
			}
		})

		t.Run("invalid request and response", func(t *testing.T) {
			a := &Adapter{BaseURL: "://bad"}
			if _, err := a.ChatCompletionsBody(llm.Request{}, false); err != nil {
				t.Fatalf("ChatCompletionsBody: %v", err)
			}
			if _, err := a.Complete(context.Background(), llm.Request{}); err == nil {
				t.Fatal("Complete accepted invalid URL")
			}
			if _, err := a.Stream(context.Background(), llm.Request{}); err == nil {
				t.Fatal("Stream accepted invalid URL")
			}
			if _, err := a.ListModels(context.Background()); err == nil {
				t.Fatal("ListModels accepted invalid URL")
			}
			if _, err := fromChatCompletionResponse(map[string]any{"bad": func() {}}, ProviderQuirks{}); err == nil {
				t.Fatal("response marshal succeeded")
			}
			if _, err := fromChatCompletionResponse(map[string]any{"choices": "wrong type"}, ProviderQuirks{}); err == nil {
				t.Fatal("response type mismatch succeeded")
			}
			var detail reasoningDetailItem
			if err := detail.UnmarshalJSON([]byte("{")); err == nil {
				t.Fatal("detail unmarshal succeeded")
			}
			if got := encodeEncryptedDetails([]reasoningDetailItem{{Type: "reasoning.encrypted", ID: "id", Data: "x", Raw: []byte("{")}}); got == "" {
				t.Fatal("missing fallback encrypted details")
			}
		})

		t.Run("builder edges", func(t *testing.T) {
			badChoice := llm.ToolChoice{Mode: "invalid"}
			req := llm.Request{ToolChoice: &badChoice}
			if _, err := (&Adapter{}).Complete(context.Background(), req); err == nil {
				t.Fatal("Complete accepted invalid tool choice")
			}
			if _, err := (&Adapter{}).Stream(context.Background(), req); err == nil {
				t.Fatal("Stream accepted invalid tool choice")
			}
			marshalReq := llm.Request{ProviderOptions: map[string]any{"openai-compatible": map[string]any{"bad": func() {}}}}
			if _, err := coverageAdapter("https://example.invalid", nil).Complete(context.Background(), marshalReq); err == nil {
				t.Fatal("Complete marshaled function")
			}
			if _, err := coverageAdapter("https://example.invalid", nil).Stream(context.Background(), marshalReq); err == nil {
				t.Fatal("Stream marshaled function")
			}
			if _, _, _, _, _, err := (&Adapter{}).doHTTP(context.Background(), llm.Request{}, map[string]any{"bad": func() {}}); err == nil {
				t.Fatal("doHTTP marshaled function")
			}
			parts := []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "a"}}, {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "b"}}}
			if got := thinkingFromParts(parts); got != "a\nb" {
				t.Fatalf("thinking = %q", got)
			}
			images := []llm.ContentPart{{Kind: llm.ContentImage}, {Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("x")}}, {Kind: llm.ContentImage, Image: &llm.ImageData{}}}
			if got := buildMultimodalParts(images); len(got) != 1 {
				t.Fatalf("images = %#v", got)
			}
		})

		t.Run("adaptive helpers", func(t *testing.T) {
			if canFallbackFromResponses(llm.Request{}, nil) {
				t.Fatal("nil error falls back")
			}
			if canFallbackFromResponses(llm.Request{}, errors.New("plain")) {
				t.Fatal("plain error falls back")
			}
			httpErr := llm.ErrorFromHTTPStatus("openai-compatible", http.StatusNotFound, "missing", nil, nil)
			if !canFallbackFromResponses(llm.Request{}, httpErr) {
				t.Fatal("404 does not fall back")
			}
			if canFallbackFromResponses(llm.Request{PreviousResponseID: "p"}, httpErr) {
				t.Fatal("continuation falls back")
			}
			adaptive := &Adapter{Adaptive: true, BaseURL: "://bad"}
			if _, err := adaptive.Complete(context.Background(), llm.Request{}); err == nil {
				t.Fatal("adaptive Complete accepted invalid URL")
			}
			if _, err := adaptive.Stream(context.Background(), llm.Request{}); err == nil {
				t.Fatal("adaptive Stream accepted invalid URL")
			}
			_ = (&Adapter{}).responsesAdapter()
			inner := llm.NewChanStream(nil)
			proxy := restampResponsesStream(inner)
			inner.Send(llm.StreamEvent{Err: httpErr})
			inner.CloseSend()
			for range proxy.Events() {
			}
			msgs, _ := toChatMessages([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought"}}}}}, ModelCompat{Quirks: ProviderQuirks{ThinkingAsText: true}}, false)
			if msgs[0]["content"] != "thought" {
				t.Fatalf("thinking-only content = %#v", msgs)
			}
		})

		t.Run("complete transport failures", func(t *testing.T) {
			transportErr := coverageRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial") })
			if _, err := coverageAdapter("https://example.invalid", transportErr).Complete(context.Background(), llm.Request{}); err == nil {
				t.Fatal("missing transport error")
			}
			readErr := coverageRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &coverageErrorReader{data: "{"}}, nil
			})
			if _, err := coverageAdapter("https://example.invalid", readErr).Complete(context.Background(), llm.Request{}); err == nil {
				t.Fatal("missing read error")
			}
			badJSON := coverageRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not-json"))}, nil
			})
			if _, err := coverageAdapter("https://example.invalid", badJSON).Complete(context.Background(), llm.Request{}); err == nil {
				t.Fatal("missing no-choices error")
			}
			streamErr := coverageRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("stream dial") })
			if _, err := coverageAdapter("https://example.invalid", streamErr).Stream(context.Background(), llm.Request{}); err == nil {
				t.Fatal("missing stream transport error")
			}
		})

		t.Run("models transport paths", func(t *testing.T) {
			transportErr := coverageRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial") })
			if _, err := coverageAdapter("https://example.invalid", transportErr).ListModels(context.Background()); err == nil {
				t.Fatal("missing models transport error")
			}
			badJSON := coverageRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("X-Test") != "yes" {
					t.Fatalf("missing default header: %v", r.Header)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{"))}, nil
			})
			a := coverageAdapter("https://example.invalid", badJSON)
			a.DefaultHeaders = map[string]string{"X-Test": "yes"}
			if _, err := a.ListModels(context.Background()); err == nil {
				t.Fatal("missing models decode error")
			}
			if _, err := (&Adapter{BaseURL: "://bad"}).ListModels(context.Background()); err == nil {
				t.Fatal("nil-client models URL succeeded")
			}
		})

		t.Run("rescue edges", func(t *testing.T) {
			for _, raw := range []string{`{"n":1,"s":"x"}`, `{"n":1,"s":"<parameter name=\"x\">v</parameter>"}`, `{"s":"v<parameter name=\"\">x</parameter>"}`} {
				_ = rescueClaudeXMLArgs(raw)
			}
		})
	})
}
