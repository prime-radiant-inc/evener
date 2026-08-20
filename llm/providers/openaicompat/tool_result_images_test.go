package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestRequestWithoutToolResultImagesCopiesOnlyWireImageFields(t *testing.T) {
	imageData := []byte{0x89, 0x50, 0x4e, 0x47}
	req := llm.Request{
		Messages: []llm.Message{llm.ToolResultNamed("call_img", "read_file", "screenshot", false)},
	}
	req.Messages[0].Content[0].ToolResult.ImageData = imageData
	req.Messages[0].Content[0].ToolResult.ImageMediaType = "image/png"

	sanitized := requestWithoutToolResultImages(req)
	got := sanitized.Messages[0].Content[0].ToolResult
	if got.ImageData != nil || got.ImageMediaType != "" {
		t.Fatalf("sanitized image fields = %v/%q, want nil/empty", got.ImageData, got.ImageMediaType)
	}
	if got.Content != "screenshot" || got.ToolCallID != "call_img" || got.Name != "read_file" {
		t.Fatalf("sanitized metadata = %+v", got)
	}
	original := req.Messages[0].Content[0].ToolResult
	if !bytes.Equal(original.ImageData, imageData) || original.ImageMediaType != "image/png" {
		t.Fatalf("input was mutated: %+v", original)
	}
}

func TestAdapter_Complete_ToolResultImage_ChatDispatchesTextOnly(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-image","model":"compat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[0].Content[0].ToolResult.ImageData...)
	adapter := &Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := resp.Text(); got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
	tool := toolMessageFromBody(t, gotBody)
	if tool["content"] != "screenshot" {
		t.Fatalf("tool content = %#v, want screenshot", tool["content"])
	}
	if req.Messages[0].Content[0].ToolResult.ImageMediaType != "image/png" ||
		!bytes.Equal(req.Messages[0].Content[0].ToolResult.ImageData, original) {
		t.Fatal("Complete mutated the original request")
	}
}

func TestAdapter_Stream_ToolResultImage_ChatDispatchesTextOnly(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-image\",\"model\":\"compat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	stream, err := (&Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client()}).Stream(t.Context(), toolResultImageRequest(t))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	finished := false
	for event := range stream.Events() {
		if event.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", event.Err)
		}
		if event.Type == llm.StreamEventFinish {
			finished = true
		}
	}
	if !finished {
		t.Fatal("stream did not finish")
	}
	tool := toolMessageFromBody(t, gotBody)
	if tool["content"] != "screenshot" {
		t.Fatalf("tool content = %#v, want screenshot", tool["content"])
	}
}

func TestAdapter_AdaptiveComplete_ToolResultImage_SanitizesChatFallback(t *testing.T) {
	var responsesBody, chatBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/responses":
			if err := json.NewDecoder(r.Body).Decode(&responsesBody); err != nil {
				t.Errorf("decode Responses request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"responses unavailable"}}`)
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&chatBody); err != nil {
				t.Errorf("decode Chat request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-image","model":"compat","choices":[{"index":0,"message":{"role":"assistant","content":"fallback ok"},"finish_reason":"stop"}]}`)
		default:
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[0].Content[0].ToolResult.ImageData...)
	adapter := &Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client(), Adaptive: true}
	resp, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "fallback ok" {
		t.Fatalf("response text = %q, want fallback ok", resp.Text())
	}
	if responsesBody == nil {
		t.Fatal("adaptive Responses attempt was not made")
	}
	tool := toolMessageFromBody(t, chatBody)
	if tool["content"] != "screenshot" {
		t.Fatalf("fallback tool content = %#v, want screenshot", tool["content"])
	}
	if req.Messages[0].Content[0].ToolResult.ImageMediaType != "image/png" ||
		!bytes.Equal(req.Messages[0].Content[0].ToolResult.ImageData, original) {
		t.Fatal("fallback mutated the original request")
	}
}

func TestAdapter_AdaptiveComplete_ToolResultImage_PreservesResponsesImage(t *testing.T) {
	var responsesBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&responsesBody); err != nil {
			t.Errorf("decode Responses request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp-image","model":"compat","output":[{"type":"message","content":[{"type":"output_text","text":"responses ok"}]}]}`)
	}))
	t.Cleanup(srv.Close)

	resp, err := (&Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client(), Adaptive: true}).Complete(t.Context(), toolResultImageRequest(t))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "responses ok" {
		t.Fatalf("response text = %q, want responses ok", resp.Text())
	}
	if !responsesBodyHasInputImage(responsesBody) {
		t.Fatalf("Responses request lost input_image: %#v", responsesBody)
	}
}

func toolResultImageRequest(t *testing.T) llm.Request {
	t.Helper()
	return llm.Request{
		Model: "compat",
		Messages: []llm.Message{
			llm.User("inspect this"),
			func() llm.Message {
				message := llm.ToolResultNamed("call_img", "read_file", "screenshot", false)
				result := message.Content[0].ToolResult
				result.ImageData = testPNG(t)
				result.ImageMediaType = "image/png"
				return message
			}(),
		},
	}
}

func toolMessageFromBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	messages, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages = %#v", body["messages"])
	}
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] == "tool" {
			return message
		}
	}
	t.Fatalf("no tool message in %#v", messages)
	return nil
}

func responsesBodyHasInputImage(body map[string]any) bool {
	items, _ := body["input"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["type"] != "function_call_output" {
			continue
		}
		parts, _ := item["output"].([]any)
		for _, partRaw := range parts {
			part, _ := partRaw.(map[string]any)
			if part["type"] == "input_image" {
				return true
			}
		}
	}
	return false
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}
