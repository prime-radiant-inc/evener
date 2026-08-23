package fakellm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleModels covers the /v1/models handler.
func TestHandleModels(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	srv.handleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("models status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ModelID) {
		t.Fatalf("models body missing model ID: %s", rec.Body.String())
	}
}

// TestHandleChatDecodeError covers the JSON decode error path.
func TestHandleChatDecodeError(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("not json"))
	srv.handleChat(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("decode error status = %d, want 400", rec.Code)
	}
}

// TestHandleChatNoTools covers the no-tools (namer) path.
func TestHandleChatNoTools(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	body := `{"model":"test","messages":[{"role":"user","content":"name this"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("no-tools status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Fake Session") {
		t.Fatalf("no-tools body should contain auto name: %s", rec.Body.String())
	}
}

// TestHandleChatStreamNoTools covers the streaming no-tools path.
func TestHandleChatStreamNoTools(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	body := `{"model":"test","stream":true,"messages":[{"role":"user","content":"name this"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	srv.handleChat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stream no-tools status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("stream body should contain SSE data: %s", rec.Body.String())
	}
}

// TestHandleUnexpectedPath covers the catch-all 404 handler.
func TestHandleUnexpectedPath(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/unexpected", nil)
	srv.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unexpected path status = %d, want 404", rec.Code)
	}
}

// TestNonStreamBodyTextReply covers the text (non-tool) reply path.
func TestNonStreamBodyTextReply(t *testing.T) {
	out := nonStreamBody(reply{text: "hello"})
	if !strings.Contains(string(out), `"content":"hello"`) {
		t.Fatalf("nonStreamBody text reply = %s, want content hello", out)
	}
	if !strings.Contains(string(out), `"finish_reason":"stop"`) {
		t.Fatalf("nonStreamBody text reply = %s, want finish_reason stop", out)
	}
}

// TestNonStreamBodyToolReply covers the tool reply path.
func TestNonStreamBodyToolReply(t *testing.T) {
	out := nonStreamBody(reply{toolName: "read_file", toolArgs: map[string]any{"path": "x"}, toolID: "call-1"})
	if !strings.Contains(string(out), `"finish_reason":"tool_calls"`) {
		t.Fatalf("nonStreamBody tool reply = %s, want finish_reason tool_calls", out)
	}
	if !strings.Contains(string(out), `"read_file"`) {
		t.Fatalf("nonStreamBody tool reply = %s, want tool name", out)
	}
}

// TestPreviousToolCallIDNonMapCall covers the non-map tool_call entry path.
func TestPreviousToolCallIDNonMapCall(t *testing.T) {
	call := &Call{
		Body: map[string]any{
			"messages": []any{
				map[string]any{"role": "assistant", "tool_calls": []any{
					"not a map",
					map[string]any{"id": "call-z"},
				}},
			},
		},
	}
	if got := call.PreviousToolCallID(); got != "call-z" {
		t.Fatalf("PreviousToolCallID() = %q, want call-z", got)
	}
}

// TestNewOnBadAddr covers the listen error path.
func TestNewOnBadAddr(t *testing.T) {
	_, err := NewOn("invalid:addr:format:extra")
	if err == nil {
		t.Fatalf("NewOn with bad addr should error")
	}
}

// TestServerAddrAndBaseURL covers the Addr and BaseURL methods.
func TestServerAddrAndBaseURL(t *testing.T) {
	srv, err := NewOn("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if srv.Addr() == "" {
		t.Fatalf("Addr should not be empty")
	}
	if !strings.HasPrefix(srv.BaseURL(), "http://") {
		t.Fatalf("BaseURL = %q, want http:// prefix", srv.BaseURL())
	}
}
