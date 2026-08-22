package openaicompat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	assertToolResultImage(t, req, imageData)
}

func TestAdapter_Complete_ToolResultImage_ChatDispatchesTextOnly(t *testing.T) {
	srv := newImageTestServer(t, map[string]imageTestResponse{
		"/chat/completions": {contentType: "application/json", body: `{"id":"chatcmpl-image","model":"compat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`},
	})

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[1].Content[0].ToolResult.ImageData...)
	adapter := &Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client()}
	resp, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := resp.Text(); got != "ok" {
		t.Fatalf("response text = %q, want ok", got)
	}
	assertChatToolMessage(t, srv.bodies["/chat/completions"])
	assertToolResultImage(t, req, original)
}

func TestAdapter_Stream_ToolResultImage_ChatDispatchesTextOnly(t *testing.T) {
	srv := newImageTestServer(t, map[string]imageTestResponse{
		"/chat/completions": {contentType: "text/event-stream", body: "data: {\"id\":\"chatcmpl-image\",\"model\":\"compat\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	})

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[1].Content[0].ToolResult.ImageData...)
	stream, err := (&Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client()}).Stream(t.Context(), req)
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
	assertChatToolMessage(t, srv.bodies["/chat/completions"])
	assertToolResultImage(t, req, original)
}

func TestAdapter_AdaptiveComplete_ToolResultImage_SanitizesChatFallback(t *testing.T) {
	srv := newImageTestServer(t, map[string]imageTestResponse{
		"/responses":        {status: http.StatusNotFound, contentType: "application/json", body: `{"error":{"message":"responses unavailable"}}`},
		"/chat/completions": {contentType: "application/json", body: `{"id":"chatcmpl-image","model":"compat","choices":[{"index":0,"message":{"role":"assistant","content":"fallback ok"},"finish_reason":"stop"}]}`},
	})

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[1].Content[0].ToolResult.ImageData...)
	adapter := &Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client(), Adaptive: true}
	resp, err := adapter.Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "fallback ok" {
		t.Fatalf("response text = %q, want fallback ok", resp.Text())
	}
	if srv.bodies["/responses"] == nil {
		t.Fatal("adaptive Responses attempt was not made")
	}
	assertChatToolMessage(t, srv.bodies["/chat/completions"])
	assertToolResultImage(t, req, original)
}

func TestAdapter_AdaptiveComplete_ToolResultImage_PreservesResponsesImage(t *testing.T) {
	srv := newImageTestServer(t, map[string]imageTestResponse{
		"/responses": {contentType: "application/json", body: `{"id":"resp-image","model":"compat","output":[{"type":"message","content":[{"type":"output_text","text":"responses ok"}]}]}`},
	})

	req := toolResultImageRequest(t)
	original := append([]byte(nil), req.Messages[1].Content[0].ToolResult.ImageData...)
	resp, err := (&Adapter{APIKey: "key", BaseURL: srv.URL, Client: srv.Client(), Adaptive: true}).Complete(t.Context(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text() != "responses ok" {
		t.Fatalf("response text = %q, want responses ok", resp.Text())
	}
	assertResponsesInputImage(t, srv.bodies["/responses"], original)
	assertToolResultImage(t, req, original)
}

type imageTestResponse struct {
	status      int
	contentType string
	body        string
}

type imageTestServer struct {
	*httptest.Server
	bodies map[string]map[string]any
}

func newImageTestServer(t *testing.T, routes map[string]imageTestResponse) *imageTestServer {
	t.Helper()
	srv := &imageTestServer{bodies: make(map[string]map[string]any)}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		srv.bodies[r.URL.Path] = decodeImageTestRequest(t, r)
		writeImageTestResponse(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func decodeImageTestRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("decode %s request: %v", r.URL.Path, err)
	}
	return body
}

func writeImageTestResponse(w http.ResponseWriter, response imageTestResponse) {
	w.Header().Set("Content-Type", response.contentType)
	if response.status == 0 {
		response.status = http.StatusOK
	}
	w.WriteHeader(response.status)
	_, _ = io.WriteString(w, response.body)
}

func assertChatToolMessage(t *testing.T, body map[string]any) {
	t.Helper()
	tool := toolMessageFromBody(t, body)
	if tool["content"] != "screenshot" {
		t.Fatalf("tool content = %#v, want screenshot", tool["content"])
	}
	for _, field := range []string{"image_url", "input_image", "ImageData", "ImageMediaType"} {
		if _, ok := tool[field]; ok {
			t.Errorf("tool message contains %q: %#v", field, tool)
		}
	}
}

func assertToolResultImage(t *testing.T, req llm.Request, expected []byte) {
	t.Helper()
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult == nil {
				continue
			}
			result := part.ToolResult
			if result.ImageMediaType != "image/png" || !bytes.Equal(result.ImageData, expected) {
				t.Fatalf("source tool result image = %q/%x, want image/png/%x", result.ImageMediaType, result.ImageData, expected)
			}
			return
		}
	}
	t.Fatal("source request has no tool result image")
}

func toolResultImageRequest(t *testing.T) llm.Request {
	t.Helper()
	message := llm.ToolResultNamed("call_img", "read_file", "screenshot", false)
	result := message.Content[0].ToolResult
	result.ImageData = testPNG(t)
	result.ImageMediaType = "image/png"
	return llm.Request{
		Model: "compat",
		Messages: []llm.Message{
			llm.User("inspect this"),
			message,
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

func assertResponsesInputImage(t *testing.T, body map[string]any, expected []byte) {
	t.Helper()
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v", body["input"])
	}
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] != "function_call_output" {
			continue
		}
		output, _ := item["output"].([]any)
		for _, partRaw := range output {
			part, _ := partRaw.(map[string]any)
			if part["type"] != "input_image" {
				continue
			}
			imageURL, ok := part["image_url"].(string)
			const prefix = "data:image/png;base64,"
			if !ok || !strings.HasPrefix(imageURL, prefix) {
				t.Fatalf("input image URL = %#v, want %s...", part["image_url"], prefix)
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(imageURL, prefix))
			if err != nil {
				t.Fatalf("decode input image URL: %v", err)
			}
			if !bytes.Equal(decoded, expected) {
				t.Fatalf("input image payload = %x, want %x", decoded, expected)
			}
			return
		}
	}
	t.Fatalf("no image-bearing function_call_output in %#v", input)
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}
