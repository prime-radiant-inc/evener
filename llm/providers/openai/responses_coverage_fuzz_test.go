package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

type responsesCoverageRoundTripper func(*http.Request) (*http.Response, error)

func (f responsesCoverageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestResponsesCoverageRequestAndTransportBranches(t *testing.T) {
	responsesCoverageRequestAndTransportBranches(t)
}

func responsesCoverageRequestAndTransportBranches(t testing.TB) {
	t.Helper()
	a := &Adapter{}
	_, err := a.buildRequestBody(llm.Request{Model: "gpt-5.6", ProviderOptions: map[string]any{"openai": map[string]any{
		"reasoning": "invalid replacement", "parallel_tool_calls": true,
	}}})
	if err != nil {
		t.Fatal(err)
	}

	badJSON := &Adapter{Client: &http.Client{Transport: responsesCoverageRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("transport reached after marshal failure")
		return nil, nil
	})}}
	_, err = badJSON.streamResponses(context.Background(), llm.Request{Model: "gpt-test", ProviderOptions: map[string]any{"openai": map[string]any{"bad": func() {}}}})
	if err == nil {
		t.Fatal("expected JSON marshal error")
	}

	if _, err = (&Adapter{BaseURL: ":"}).streamResponses(context.Background(), llm.Request{Model: "gpt-test"}); err == nil {
		t.Fatal("expected request URL error")
	}
	network := &Adapter{Client: &http.Client{Transport: responsesCoverageRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}}
	if _, err = network.streamResponses(context.Background(), llm.Request{Model: "gpt-test"}); err == nil {
		t.Fatal("expected transport error")
	}

	var requests int
	success := &Adapter{Client: &http.Client{Transport: responsesCoverageRoundTripper(func(*http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\"}}\n\n"))}, nil
	})}}
	stream, err := success.streamResponses(context.Background(), llm.Request{Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	finishes := 0
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish {
			finishes++
		}
	}
	if requests != 1 || finishes != 1 {
		t.Fatalf("requests=%d finishes=%d, want one decoder and one finish", requests, finishes)
	}
}

func TestResponsesCoverageSchemaInputAndDecodeBranches(t *testing.T) {
	responsesCoverageSchemaInputAndDecodeBranches(t)
}

func responsesCoverageSchemaInputAndDecodeBranches(t testing.TB) {
	t.Helper()
	strictifyJSONSchemaInPlace(nil)
	schema := map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "anyOf": "bad", "oneOf": []any{map[string]any{"type": "object"}, "ignored"}, "allOf": nil}
	strictifyJSONSchemaInPlace(schema)
	if toResponsesResponseFormat(llm.ResponseFormat{Type: "unknown"}) != nil {
		t.Fatal("unknown response format must be ignored")
	}
	if _, err := toResponsesToolChoice(llm.ToolChoice{Mode: "named"}); err == nil {
		t.Fatal("named tool choice without name must fail")
	}

	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: " "}, {Kind: llm.ContentDocument, Document: &llm.DocumentData{}}}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: " "}, {Kind: llm.ContentImage}, {Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("x")}},
			{Kind: llm.ContentDocument}, {Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.invalid/a.pdf"}},
		}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentText}, {Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c", IsError: true, Content: make(chan int)}}}},
		{Role: llm.Role("unknown")},
	}
	if _, _, err := toResponsesInput(msgs, "gpt-test"); err == nil {
		t.Fatal("assistant document must fail")
	}
	msgs[0].Content = nil
	local := filepath.Join(t.TempDir(), "image.unknown")
	if err := os.WriteFile(local, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	msgs[1].Content = append(msgs[1].Content, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: local}})
	if _, _, err := toResponsesInput(msgs, "gpt-test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio}}}}, "gpt-test"); err == nil {
		t.Fatal("user audio must fail")
	}

	got := parseReasoningSummary([]any{"bad", map[string]any{"text": " "}, map[string]any{"text": "kept"}})
	if len(got) != 1 || got[0] != "kept" {
		t.Fatalf("summary=%v", got)
	}
	for _, raw := range []map[string]any{
		{"output": []any{"bad", map[string]any{"type": "message", "phase": "final", "content": []any{"bad", map[string]any{"type": "output_text"}}}}, "status": "completed"},
		{"output": []any{map[string]any{"type": "function_call", "item_id": "i", "call_id": "c", "arguments": "{}"}}, "status": "completed"},
		{"status": "incomplete", "incomplete_details": map[string]any{"reason": "content_filter"}},
		{"status": "incomplete", "incomplete_details": map[string]any{"reason": "new_reason"}},
	} {
		if r := fromResponses(raw, "requested"); r.Finish.Reason == "" {
			t.Fatal("empty finish reason")
		}
	}
}

func TestResponsesCoverageAdversarialStreamBranches(t *testing.T) {
	responsesCoverageAdversarialStreamBranches(t)
}

func responsesCoverageAdversarialStreamBranches(t testing.TB) {
	t.Helper()
	originalRawBodyEnabled := responsesRawBodyEnabled
	responsesRawBodyEnabled = func() bool { return true }
	t.Cleanup(func() { responsesRawBodyEnabled = originalRawBodyEnabled })
	sse := strings.Join([]string{
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"alias-delta\",\"name\":\"tool\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"call_id\":\"missing-delta\",\"item_id\":\"alias-delta\",\"delta\":\"x\"}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"alias-done\",\"name\":\"tool\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.done\",\"call_id\":\"missing-done\",\"item_id\":\"alias-done\",\"arguments\":\"{}\"}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"alias-item\",\"name\":\"tool\"}}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"alias-item\",\"call_id\":\"missing-item\",\"name\":\"tool\",\"arguments\":\"{}\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"id\":\"fallback\",\"name\":\"f\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\"}\n\n",
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item\",\"call_id\":\"call\",\"name\":\"tool\"}}\n\n",
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item\",\"delta\":\"{\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.done\"}\n\n",
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"item\",\"arguments\":\"{}\"}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"output_item\":{\"type\":\"function_call\",\"id\":\"item\",\"name\":\"tool\"}}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"text\":\"x\"}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":null}\n\n",
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"y\"}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"unknown\"}}\n\n",
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"orphan\"}}\n\n",
		"data: {\"type\":\"response.completed\",\"status\":\"completed\"}\n\n",
	}, "")
	resp, sawError := accumulateResponsesSSE(&Adapter{}, []byte(sse), false)
	if sawError || resp == nil {
		t.Fatalf("response=%v sawError=%v", resp, sawError)
	}
}

func FuzzResponsesCoverage(f *testing.F) {
	for _, scenario := range []byte{0, 1, 2} {
		f.Add(scenario)
	}
	f.Fuzz(func(t *testing.T, scenario byte) {
		switch scenario % 3 {
		case 0:
			responsesCoverageRequestAndTransportBranches(t)
		case 1:
			responsesCoverageSchemaInputAndDecodeBranches(t)
		case 2:
			responsesCoverageAdversarialStreamBranches(t)
		}
	})
}
