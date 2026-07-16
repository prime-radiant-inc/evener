//go:build serffuzz

package google

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

type googleSeedRoundTripper func(*http.Request) (*http.Response, error)

func (f googleSeedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func googleSeedClient(status int, body string, headers http.Header) *http.Client {
	return &http.Client{Transport: googleSeedRoundTripper(func(*http.Request) (*http.Response, error) {
		if headers == nil {
			headers = make(http.Header)
		}
		return &http.Response{StatusCode: status, Header: headers.Clone(), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func googleSeedErrorClient(err error) *http.Client {
	return &http.Client{Transport: googleSeedRoundTripper(func(*http.Request) (*http.Response, error) { return nil, err })}
}

func googleSeedRequest() llm.Request {
	temp := 0.25
	max := 17
	effort := "medium"
	return llm.Request{
		Model: "gemini/seed model", Temperature: &temp, MaxTokens: &max,
		ReasoningEffort: &effort, WebSearch: true,
		ResponseFormat: &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{
			"type": []any{"object", "null"}, "additionalProperties": false,
			"properties": map[string]any{"items": map[string]any{"type": []string{"string", "null"}}},
		}},
		Tools: []llm.ToolDefinition{{Name: "tool", Description: "seed", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string"}},
		}}},
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: " system "}}},
			{Role: llm.RoleDeveloper, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "developer"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "user"}, {Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png"), MediaType: "image/png"}}}},
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "prior"}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-1", Name: "tool", Arguments: json.RawMessage(`{"v":1}`), ThoughtSignature: " sig "}}}},
			{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-1", Content: "ok", IsError: true, ImageData: []byte("image")}}}},
		},
		ProviderOptions: map[string]any{"google": map[string]any{"seedGoogle": true}, "gemini": map[string]any{"seedGemini": true}},
	}
}

func drainGoogleSeedStream(s llm.Stream) {
	if s == nil {
		return
	}
	for range s.Events() {
	}
}

// runGoogleCoverageSeedUnion is the deterministic, offline branch seed bank
// replayed by FuzzGoogleComplete's distinguished committed seed.
func runGoogleCoverageSeedUnion(t *testing.T) {
	ctx := context.Background()
	req := googleSeedRequest()

	_, _ = NewForInstance(GoogleInstanceParams{})
	a, _ := NewForInstance(GoogleInstanceParams{Name: "seed", APIKey: " key ", BaseURL: "http://seed.invalid/", Headers: map[string]string{"X-Seed": "factory"}})
	_, _ = NewForInstance(GoogleInstanceParams{Name: "default", APIKey: "key"})
	_ = a.Name()
	_ = (&Adapter{}).Name()

	t.Setenv(envvars.GeminiAPIKey.Name, "")
	t.Setenv(envvars.GoogleAPIKey.Name, "")
	_, _ = llm.NewFromEnv()
	t.Setenv(envvars.GoogleAPIKey.Name, "alias")
	t.Setenv(envvars.GeminiBaseURL.Name, "http://env.invalid/")
	_, _ = NewFromEnv()
	t.Setenv(envvars.GeminiAPIKey.Name, "gemini")
	_, _ = NewFromEnv()
	_, _ = llm.NewFromEnv()
	oldNewFromEnv := newGoogleFromEnv
	newGoogleFromEnv = func() (*Adapter, error) { return nil, errors.New("seed factory") }
	_, _ = llm.NewFromEnv()
	newGoogleFromEnv = oldNewFromEnv

	_, _ = llm.NewFromProviders(providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: "factory-google", Type: "google", APIKey: "key"}, {Name: "factory-gemini", Type: "gemini", APIKey: "key"}}})

	for _, mode := range []string{"", "auto", "none", "required", "named", "bad"} {
		r := req
		r.ToolChoice = &llm.ToolChoice{Mode: mode, Name: "tool"}
		_, _ = a.buildRequestBody(r, "system", []map[string]any{{"role": "user"}})
	}
	r := req
	r.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, _ = a.buildRequestBody(r, "", nil)
	for _, format := range []string{"text", "json"} {
		r := req
		r.ResponseFormat = &llm.ResponseFormat{Type: format}
		r.Tools = nil
		_, _ = a.buildRequestBody(r, "", nil)
	}

	for _, v := range []any{[]any{"string", "null"}, []string{"null", "number"}, []any{"string"}, []any{"string", 1}, []string{"", "null"}, []string{"a", "b"}, []string{"null", "null"}, "string"} {
		_, _, _ = geminiNullableType(v)
	}
	_ = sanitizeGeminiSchema([]any{map[string]any{"type": []any{"null", "string"}}, "x"})

	imageFile := t.TempDir() + "/seed.png"
	if err := os.WriteFile(imageFile, []byte("file-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, image := range []*llm.ImageData{{Data: []byte("x")}, {URL: imageFile}, {URL: "https://example.invalid/image"}, {}} {
		_, _ = geminiImagePart(llm.ContentPart{Kind: llm.ContentImage, Image: image})
	}
	_, _ = geminiImagePart(llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imageFile + ".missing"}})

	badMessages := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio}}}}
	_, _, _ = toGeminiContents(badMessages)
	badMessages[0].Role = llm.RoleAssistant
	_, _, _ = toGeminiContents(badMessages)
	_, _, _ = toGeminiContents([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imageFile + ".missing"}}}}})
	_, _, _ = toGeminiContents([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imageFile + ".missing"}}}}})
	_, _, _ = toGeminiContents([]llm.Message{
		{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: " "}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: " "}, {Kind: llm.ContentImage}, {Kind: llm.ContentImage, Image: &llm.ImageData{}}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: " "}, {Kind: llm.ContentImage}, {Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.invalid/a"}}, {Kind: llm.ContentToolCall}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{}}, {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{Arguments: json.RawMessage("null")}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentText}, {Kind: llm.ContentToolResult}}},
		{Role: "unknown"},
	})
	_, _, _ = toGeminiContents([]llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "unknown", Content: map[string]any{"ok": true}}}, {Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "named", Content: 3, ImageData: []byte("x"), ImageMediaType: "image/jpeg"}}}}})
	_ = toolNameFromCallID(req.Messages, "")
	_ = toolNameFromCallID(req.Messages, "missing")

	for _, v := range []any{1, int64(2), float64(3), json.Number("4"), json.Number("bad"), "5"} {
		_ = tokenCountInt(v)
	}
	for _, v := range []any{map[string]any{"n": json.Number("1")}, []any{json.Number("1.5"), json.Number("bad")}, "x"} {
		_ = normalizeJSONNumbers(v)
	}
	for _, pair := range [][2]map[string]any{{{"thoughtSignature": " a "}, {}}, {{"thought_signature": "b"}, {}}, {{}, {"thoughtSignature": "c"}}, {{}, {"thought_signature": "d"}}, {{}, {}}} {
		_ = geminiThoughtSignature(pair[0], pair[1])
	}

	fullResponse := `{"candidates":[{"content":{"parts":[1,{"thought":true,"text":"think"},{"thought":true,"text":""},{"text":"answer"},{"functionCall":{"name":"tool","args":{"i":1},"thought_signature":"sig"}}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["q",1]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3,"cachedContentTokenCount":4,"thoughtsTokenCount":5}}`
	for _, raw := range []map[string]any{{}, {"candidates": []any{1}}, {"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"functionCall": map[string]any{"name": "tool", "args": nil}}}}}}}} {
		_ = fromGeminiResponse(raw, "seed")
	}

	completeHeaders := http.Header{"X-Ratelimit-Limit": {"10"}}
	for _, tc := range []struct {
		status  int
		body    string
		headers http.Header
	}{{200, fullResponse, completeHeaders}, {204, "", nil}, {400, `{"error":{"message":"quota","status":"RESOURCE_EXHAUSTED"}}`, nil}, {422, `{"error":{"message":"late","status":"DEADLINE_EXCEEDED"}}`, nil}, {400, `{"error":{"message":"down","status":"UNAVAILABLE"}}`, nil}, {400, `{"error":{"message":"other","status":"OTHER"}}`, nil}, {400, "not-json", nil}, {429, `{}`, http.Header{"Retry-After": {"1"}}}} {
		a.Client = googleSeedClient(tc.status, tc.body, tc.headers)
		_, _ = a.Complete(ctx, req)
	}
	a.Client = nil
	badReq := req
	badReq.Messages = badMessages
	_, _ = a.Complete(ctx, badReq)
	badReq = req
	badReq.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, _ = a.Complete(ctx, badReq)
	cyclic := map[string]any{}
	cyclic["cycle"] = cyclic
	badReq = req
	badReq.ProviderOptions = map[string]any{"google": cyclic}
	_, _ = a.Complete(ctx, badReq)
	a.BaseURL = "%"
	_, _ = a.Complete(ctx, req)
	a.BaseURL = "http://seed.invalid"
	a.Client = googleSeedErrorClient(context.Canceled)
	_, _ = a.Complete(ctx, req)
	_, _ = a.Complete(nil, req)

	for _, tc := range []struct {
		status int
		body   string
	}{{200, `{"totalTokens":7}`}, {200, `{"totalTokens":"bad"}`}, {400, `{"error":{"message":"internal","status":"INTERNAL"}}`}, {503, `{}`}} {
		a.Client = googleSeedClient(tc.status, tc.body, nil)
		_, _ = a.CountInputTokens(ctx, req)
	}
	a.Client = nil
	badReq = req
	badReq.Messages = badMessages
	_, _ = a.CountInputTokens(ctx, badReq)
	badReq = req
	badReq.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, _ = a.CountInputTokens(ctx, badReq)
	badReq = req
	badReq.ProviderOptions = map[string]any{"google": cyclic}
	_, _ = a.CountInputTokens(ctx, badReq)
	a.BaseURL = "%"
	_, _ = a.CountInputTokens(ctx, req)
	a.BaseURL = "http://seed.invalid"
	a.Client = googleSeedErrorClient(context.DeadlineExceeded)
	_, _ = a.CountInputTokens(ctx, req)
	_, _ = a.CountInputTokens(nil, req)

	streamBody := "data: " + fullResponse + "\n\n"
	for _, tc := range []struct {
		status int
		body   string
	}{{200, streamBody}, {200, "data: {}\n\n"}, {400, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`}} {
		a.Client = googleSeedClient(tc.status, tc.body, nil)
		s, _ := a.Stream(ctx, req)
		drainGoogleSeedStream(s)
	}
	a.Client = nil
	badReq = req
	badReq.Messages = badMessages
	_, _ = a.Stream(ctx, badReq)
	badReq = req
	badReq.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, _ = a.Stream(ctx, badReq)
	badReq = req
	badReq.ProviderOptions = map[string]any{"google": cyclic}
	_, _ = a.Stream(ctx, badReq)
	a.BaseURL = "%"
	_, _ = a.Stream(ctx, req)
	a.BaseURL = "http://seed.invalid"
	a.Client = googleSeedErrorClient(errors.New("seed transport"))
	_, _ = a.Stream(ctx, req)
	oldStreamRequest := newGoogleStreamRequest
	newGoogleStreamRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("seed request")
	}
	_, _ = a.Stream(ctx, req)
	newGoogleStreamRequest = oldStreamRequest

	for _, tc := range []struct {
		status int
		body   string
	}{{200, `{"models":[{"name":"models/z","supportedGenerationMethods":["generateContent"]},{"name":"models/a","supportedGenerationMethods":["generateContent"]},{"name":"models/skip","supportedGenerationMethods":["other"]}]}`}, {200, "not-json"}, {204, ""}, {400, `{}`}} {
		a.Client = googleSeedClient(tc.status, tc.body, nil)
		_, _ = a.ListModels(ctx)
	}
	a.BaseURL = "%"
	a.Client = nil
	_, _ = a.ListModels(ctx)
	a.BaseURL = "http://seed.invalid"
	_, _ = a.ListModels(nil)
	a.BaseURL = "http://seed.invalid"
	a.Client = googleSeedErrorClient(errors.New("list transport"))
	_, _ = a.ListModels(ctx)

	d := time.Second
	for _, status := range []int{200, 400, 422} {
		_ = classifyGeminiError(status, []byte(`{"error":{"message":"x","status":"OTHER"}}`), &d, errors.New("http"))
	}
}
