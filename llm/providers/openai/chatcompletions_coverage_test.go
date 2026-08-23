package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/openaichat"
)

// TestStreamViaChatCompletionsJSONMarshalError covers the json.Marshal error
// path (lines 39-41) by using ProviderOptions with a function value.
func TestStreamViaChatCompletionsJSONMarshalError(t *testing.T) {
	a := &Adapter{BaseURL: "https://example.test", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200}}}
	req := llm.Request{
		Model:    "test-model",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{"openai": map[string]any{"bad": func() {}}},
	}
	_, err := a.streamViaChatCompletions(context.Background(), req)
	if err == nil {
		t.Fatal("streamViaChatCompletions with unmarshalable body should error")
	}
}

// TestStreamViaChatCompletionsNewRequestError covers the http.NewRequestWithContext
// error path (lines 45-47) by using an invalid BaseURL.
func TestStreamViaChatCompletionsNewRequestError(t *testing.T) {
	a := &Adapter{BaseURL: ":", Client: &http.Client{Transport: chatCoverageRoundTripper{status: 200}}}
	req := llm.Request{
		Model:    "test-model",
		Messages: []llm.Message{llm.User("hi")},
	}
	_, err := a.streamViaChatCompletions(context.Background(), req)
	if err == nil {
		t.Fatal("streamViaChatCompletions with invalid URL should error")
	}
}

// TestStreamViaChatCompletionsReadErrOnErrorResponse covers the readErr branch
// (line 84-85) by returning a response body that errors on read.
func TestStreamViaChatCompletionsReadErrOnErrorResponse(t *testing.T) {
	a := &Adapter{
		BaseURL: "https://example.test",
		Client: &http.Client{Transport: errorBodyRoundTripper{
			status: 429,
		}},
	}
	req := llm.Request{
		Model:    "test-model",
		Messages: []llm.Message{llm.User("hi")},
	}
	_, err := a.streamViaChatCompletions(context.Background(), req)
	if err == nil {
		t.Fatal("streamViaChatCompletions with read error should return error")
	}
}

// errorBodyRoundTripper returns a response whose Body errors on Read.
type errorBodyRoundTripper struct {
	status int
}

func (e errorBodyRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: e.status,
		Header:     make(http.Header),
		Body:       errorReadCloser{},
	}, nil
}

type errorReadCloser struct{}

func (errorReadCloser) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (errorReadCloser) Close() error             { return nil }

// TestInbandStreamErrorEmptyMessage covers the empty-message fallback in
// inbandStreamError (line 138-139).
func TestInbandStreamErrorEmptyMessage(t *testing.T) {
	e := &openaichat.InbandError{Code: json.RawMessage(`"rate_limit_exceeded"`)}
	err := inbandStreamError("chat.completions(stream)", e, nil)
	if err == nil {
		t.Fatal("inbandStreamError should return an error")
	}
	if !strings.Contains(err.Error(), "provider reported an in-band stream error") {
		t.Fatalf("error = %q, want fallback message", err.Error())
	}
}

// TestToChatCompletionsToolChoiceNamedWithoutName covers the named-without-name
// error path (line 639-640).
func TestToChatCompletionsToolChoiceNamedWithoutName(t *testing.T) {
	_, err := toChatCompletionsToolChoice(llm.ToolChoice{Mode: "named"})
	if err == nil {
		t.Fatal("toChatCompletionsToolChoice named without name should error")
	}
}

// TestToChatCompletionsToolChoiceUnknownWithName covers the unknown-mode-with-name
// forced function path (lines 649-653).
func TestToChatCompletionsToolChoiceUnknownWithName(t *testing.T) {
	got, err := toChatCompletionsToolChoice(llm.ToolChoice{Mode: "legacy", Name: "my_tool"})
	if err != nil {
		t.Fatalf("toChatCompletionsToolChoice unknown with name: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	fn, ok := m["function"].(map[string]any)
	if !ok || fn["name"] != "my_tool" {
		t.Fatalf("expected function.name=my_tool, got %v", m)
	}
}

// TestToChatCompletionsToolChoiceUnknownWithoutName covers the unknown-mode-without-name
// error path (line 654).
func TestToChatCompletionsToolChoiceUnknownWithoutName(t *testing.T) {
	_, err := toChatCompletionsToolChoice(llm.ToolChoice{Mode: "invalid"})
	if err == nil {
		t.Fatal("toChatCompletionsToolChoice invalid without name should error")
	}
}

// TestToChatCompletionsMessagesNonStringToolResult covers the default case
// in tool result content serialization (lines 502-503).
func TestToChatCompletionsMessagesNonStringToolResult(t *testing.T) {
	msgs := []llm.Message{
		llm.ToolResult("call-1", map[string]any{"ok": true}, false),
	}
	out, err := toChatMessages(msgs)
	if err != nil {
		t.Fatalf("toChatCompletionsMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("messages = %d, want 1", len(out))
	}
	content, ok := out[0]["content"].(string)
	if !ok || !strings.Contains(content, "ok") {
		t.Fatalf("content = %v, want JSON with ok", out[0]["content"])
	}
}
