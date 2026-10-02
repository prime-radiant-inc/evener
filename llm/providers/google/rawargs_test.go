package google

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"primeradiant.com/evener/llm"
)

// rawArgsJSON builds a Gemini generateContent response body whose
// functionCall args field carries the given raw JSON object, preserving
// raw bytes (including invalid UTF-8) and non-canonical formatting.
func rawArgsJSON(t *testing.T, argsJSON string) []byte {
	t.Helper()
	// Build the body manually so the args field preserves its exact bytes
	// (json.Marshal compacts json.RawMessage, losing non-canonical
	// formatting). The argsJSON is embedded directly as the value of the
	// "args" field — it must be a valid JSON object.
	return []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"write_file","args":` + argsJSON + `}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
}

// decodeRawArgsComplete runs the google Complete decode path with a
// scripted response body and returns the tool-call Arguments.
func decodeRawArgsComplete(t *testing.T, body []byte) []byte {
	t.Helper()
	srv, _ := protoServer(t, 200, string(body))
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	return calls[0].Arguments
}

// TestRawArgs_NonStream_InvalidUTF8 asserts invalid-UTF-8 bytes in the
// functionCall args object survive the non-streaming google decode.
func TestRawArgs_NonStream_InvalidUTF8(t *testing.T) {
	argsJSON := `{"path":"` + "\xff" + `file.txt"}`
	body := rawArgsJSON(t, argsJSON)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("non-stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

// TestRawArgs_NonStream_NonCanonicalJSON asserts non-canonical valid JSON
// (spacing/key-order) in the args object survives byte-identical.
func TestRawArgs_NonStream_NonCanonicalJSON(t *testing.T) {
	argsJSON := `{ "b" : 2 , "a" : 1 }`
	body := rawArgsJSON(t, argsJSON)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_NonStream_FallbackOnNull asserts null args fall back
// gracefully (degrade, never drop).
func TestRawArgs_NonStream_FallbackOnNull(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":null}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)
	srv, _ := protoServer(t, 200, string(body))
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatalf("Complete (fallback): %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
}

// --- Streaming ---

// rawArgsGoogleSSE builds a Gemini SSE stream where the functionCall args
// arrive in one chunk. The args content may contain invalid UTF-8 bytes.
func rawArgsGoogleSSE(argsJSON string) string {
	return "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"write_file\",\"args\":" + argsJSON + `}}]}}]}` + "\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"
}

// decodeRawArgsStream runs the google streaming decode with a scripted
// SSE body and returns the tool-call Arguments from the final response.
func decodeRawArgsStream(t *testing.T, sseBody string) []byte {
	t.Helper()
	srv, _ := protoServer(t, 200, sseBody)
	res := protoLive(srv)
	p := &Protocol{Client: srv.Client()}
	req := protoReq("")
	s, err := p.Stream(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	calls := final.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1: %+v", len(calls), final.Message.Content)
	}
	return calls[0].Arguments
}

// TestRawArgs_Stream_InvalidUTF8 asserts invalid-UTF-8 bytes in streamed
// tool-call arguments survive the decode byte-identical.
func TestRawArgs_Stream_InvalidUTF8(t *testing.T) {
	argsJSON := `{"path":"` + "\xff" + `file.txt"}`
	sseBody := rawArgsGoogleSSE(argsJSON)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

// TestRawArgs_Stream_NonCanonicalJSON asserts non-canonical valid JSON in
// streamed tool-call arguments survives the decode byte-identical.
func TestRawArgs_Stream_NonCanonicalJSON(t *testing.T) {
	argsJSON := `{ "b" : 2 , "a" : 1 }`
	sseBody := rawArgsGoogleSSE(argsJSON)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_Stream_FallbackOnNull asserts null args in the stream fall
// back gracefully (degrade, never drop).
func TestRawArgs_Stream_FallbackOnNull(t *testing.T) {
	sseBody := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"f\",\"args\":null}}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"
	srv, _ := protoServer(t, 200, sseBody)
	res := protoLive(srv)
	p := &Protocol{Client: srv.Client()}
	req := protoReq("")
	s, err := p.Stream(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	calls := final.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
}

func TestRawArgs_NonStream_MultipleToolCallsPreserveIndex(t *testing.T) {
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	body := []byte(`{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"first","args":` + first + `}},` +
		`{"functionCall":{"name":"second","args":` + second + `}}` +
		`]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"totalTokenCount":7}}`)

	got := decodeRawArgsCompleteMany(t, body)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second)})
}

func TestRawArgs_Stream_MultipleToolCallsPreserveIndex(t *testing.T) {
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	sseBody := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[" +
		`{"functionCall":{"name":"first","args":` + first + `}},` +
		`{"functionCall":{"name":"second","args":` + second + `}}` +
		"]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7}}\n\n"

	got := decodeRawArgsStreamMany(t, sseBody)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second)})
}

func decodeRawArgsCompleteMany(t *testing.T, body []byte) [][]byte {
	t.Helper()
	srv, _ := protoServer(t, http.StatusOK, string(body))
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return googleToolCallArguments(resp)
}

func decodeRawArgsStreamMany(t *testing.T, sseBody string) [][]byte {
	t.Helper()
	srv, _ := protoServer(t, http.StatusOK, sseBody)
	res := protoLive(srv)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(protoReq(""), res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	return googleToolCallArguments(*final)
}

func googleToolCallArguments(resp llm.Response) [][]byte {
	calls := resp.ToolCalls()
	args := make([][]byte, len(calls))
	for i := range calls {
		args[i] = calls[i].Arguments
	}
	return args
}

func assertRawArgsByIndex(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tool calls, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("tool call %d Arguments = %q (% x), want %q (% x)", i, got[i], got[i], want[i], want[i])
		}
	}
}
