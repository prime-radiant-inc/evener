package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type chatCoverageRoundTripper struct {
	status int
	body   string
	err    error
}

func (rt chatCoverageRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	if rt.err != nil {
		return nil, rt.err
	}
	return &http.Response{
		StatusCode: rt.status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(rt.body)),
	}, nil
}

// FuzzOpenAIChatCompletionsCoverage replays branch-focused request and stream
// shapes. Every transport is in-memory; local-file cases use the fuzz temp dir.
func FuzzOpenAIChatCompletionsCoverage(f *testing.F) {
	for i := byte(0); i < 19; i++ {
		f.Add(i)
	}

	f.Fuzz(func(t *testing.T, selector byte) {
		tmp := t.TempDir()
		imagePath := tmp + "/image.unknown"
		docPath := tmp + "/document.unknown"
		if err := os.WriteFile(imagePath, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(docPath, []byte("document"), 0o600); err != nil {
			t.Fatal(err)
		}

		temperature, topP := 0.25, 0.75
		maxTokens := 99
		effort := "high"
		format := llm.ResponseFormat{Type: "json_object"}
		req := llm.Request{
			Model: "coverage-model",
			Messages: []llm.Message{
				llm.System("system"), llm.Developer("developer"),
				{Role: llm.RoleUser, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "describe"},
					{Kind: llm.ContentImage, Image: nil},
					{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("png")}},
					{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imagePath, Detail: "high"}},
					{Kind: llm.ContentImage, Image: &llm.ImageData{}},
					{Kind: llm.ContentDocument, Document: nil},
					{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("pdf"), FileName: "a.pdf"}},
					{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: docPath}},
					{Kind: llm.ContentDocument, Document: &llm.DocumentData{}},
				}},
				{Role: llm.RoleAssistant, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "calling"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c", Name: "tool", Arguments: []byte(`{"x":1}`)}},
				}},
				llm.ToolResult("c", map[string]any{"ok": true}, false),
			},
			Tools:           []llm.ToolDefinition{{Name: "tool", Description: "coverage"}},
			Temperature:     &temperature,
			TopP:            &topP,
			MaxTokens:       &maxTokens,
			StopSequences:   []string{"stop"},
			ResponseFormat:  &format,
			ReasoningEffort: &effort,
			Metadata:        map[string]string{"key": "value"},
			WebSearch:       true,
			ProviderOptions: map[string]any{"openai": map[string]any{"seed": 7}},
		}
		// Exercise the plain-message alternatives alongside the rich request.
		_, _ = toChatMessages([]llm.Message{
			llm.User("plain"),
			llm.Assistant("plain"),
			llm.ToolResult("c", "plain", false),
		})

		switch selector % 19 {
		case 0, 1, 2, 3, 4, 5:
			modes := []llm.ToolChoice{
				{}, {Mode: "none"}, {Mode: "required"}, {Mode: "named", Name: "tool"},
				{Mode: "legacy", Name: "tool"}, {Mode: "invalid"},
			}
			req.ToolChoice = &modes[selector%6]
			_, _ = buildChatCompletionsBody(req, selector&1 != 0)
		case 6:
			req.ToolChoice = &llm.ToolChoice{Mode: "named"}
			_, _ = buildChatCompletionsBody(req, true)
		case 7:
			req.Messages[2].Content = append(req.Messages[2].Content, llm.ContentPart{Kind: llm.ContentAudio})
			_, _ = buildChatCompletionsBody(req, false)
		case 8:
			req.Messages = []llm.Message{llm.ToolResult("c", "x", false)}
			req.Messages[0].Content[0].ToolResult.ImageData = []byte("image")
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200}}}
			_, _ = a.streamViaChatCompletions(context.Background(), req)
		case 9:
			req.Messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: tmp + "/missing"}}}}}
			_, _ = buildChatCompletionsBody(req, false)
		case 10:
			req.Messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: tmp + "/missing"}}}}}
			_, _ = buildChatCompletionsBody(req, false)
		case 11:
			req.ProviderOptions = map[string]any{"openai": map[string]any{"bad": func() {}}}
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200}}}
			_, _ = a.streamViaChatCompletions(context.Background(), req)
		case 12:
			a := &Adapter{BaseURL: ":", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200}}}
			_, _ = a.streamViaChatCompletions(context.Background(), req)
		case 13:
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{err: errors.New("transport")}}}
			_, _ = a.streamViaChatCompletions(context.Background(), req)
		case 14:
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 429, body: `{"error":"limited"}`}}}
			_, _ = a.streamViaChatCompletions(context.Background(), req)
		case 15, 16, 17:
			streams := []string{
				"data: {bad}\n\ndata: {\"model\":\"m\",\"choices\":[]}\n\ndata: [DONE]\n\n",
				"data: {\"model\":\"m\",\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5},\"choices\":[{\"finish_reason\":\"stop\",\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n",
				"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"b\",\"function\":{\"name\":\"second\",\"arguments\":\"{}\"}},{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"first\"}}]}}]}\n\ndata: [DONE]\n\n",
			}
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200, body: streams[selector%3]}}}
			stream, err := a.streamViaChatCompletions(context.Background(), req)
			if err == nil {
				for range stream.Events() {
				}
				_ = stream.Close()
			}
		case 18:
			original := chatCompletionsRawBody
			chatCompletionsRawBody = func(*bytes.Buffer) (string, bool) { return "captured", true }
			t.Cleanup(func() { chatCompletionsRawBody = original })
			a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200, body: "data: [DONE]\n\n"}}}
			stream, err := a.streamViaChatCompletions(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for range stream.Events() {
			}
			_ = stream.Close()
		}
	})
}
