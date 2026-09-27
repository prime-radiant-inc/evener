package anthropic

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
)

// rawInputJSON builds an Anthropic Messages response body whose tool_use
// content block's input field carries the given raw JSON object, preserving
// raw bytes (including invalid UTF-8) and non-canonical formatting.
func rawInputJSON(t *testing.T, inputJSON string) []byte {
	t.Helper()
	// Build the body manually via string concatenation so the input field
	// preserves its exact bytes (json.Marshal compacts json.RawMessage,
	// losing non-canonical formatting). The inputJSON is embedded directly
	// as the value of the "input" field — it must be a valid JSON value
	// (object, array, etc.) since it's not string-wrapped.
	return []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-x-wire","content":[{"type":"tool_use","id":"toolu_1","name":"write_file","input":` + inputJSON + `}],"stop_reason":"tool_use"}`)
}

// decodeRawArgsComplete runs the anthropic Complete decode path with a
// scripted response body and returns the tool-call Arguments.
func decodeRawArgsComplete(t *testing.T, body []byte) []byte {
	t.Helper()
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, string(body) })
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
// tool_use input object survive the non-streaming anthropic decode.
func TestRawArgs_NonStream_InvalidUTF8(t *testing.T) {
	// input is a JSON object containing a raw 0xff byte in a string value.
	inputJSON := `{"path":"` + "\xff" + `file.txt"}`
	body := rawInputJSON(t, inputJSON)
	got := decodeRawArgsComplete(t, body)
	want := []byte(inputJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("non-stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

// TestRawArgs_NonStream_NonCanonicalJSON asserts non-canonical valid JSON
// (spacing/key-order) in the input object survives byte-identical — not
// re-marshaled to canonical form.
func TestRawArgs_NonStream_NonCanonicalJSON(t *testing.T) {
	inputJSON := `{ "b" : 2 , "a" : 1 }`
	body := rawInputJSON(t, inputJSON)
	got := decodeRawArgsComplete(t, body)
	want := []byte(inputJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_NonStream_FallbackOnNull asserts null input falls back
// gracefully (degrade, never drop).
func TestRawArgs_NonStream_FallbackOnNull(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_1","name":"f","input":null}],"stop_reason":"tool_use"}`)
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, string(body) })
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

// rawArgsAnthropicSSE builds an Anthropic Messages SSE stream where the
// tool_use arguments arrive via input_json_delta events. The partial_json
// content may contain invalid UTF-8 bytes.
func rawArgsAnthropicSSE(argsContent string) string {
	// Escape the argsContent for JSON string transport.
	var esc strings.Builder
	for _, b := range []byte(argsContent) {
		switch b {
		case '"':
			esc.WriteString(`\"`)
		case '\\':
			esc.WriteString(`\\`)
		case '\n':
			esc.WriteString(`\n`)
		case '\r':
			esc.WriteString(`\r`)
		case '\t':
			esc.WriteString(`\t`)
		case '\b':
			esc.WriteString(`\b`)
		case '\f':
			esc.WriteString(`\f`)
		default:
			if b < 0x20 {
				esc.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				esc.WriteByte(hex[b>>4])
				esc.WriteByte(hex[b&0xf])
			} else {
				esc.WriteByte(b)
			}
		}
	}
	argEsc := esc.String()
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"write_file\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"" + argEsc + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// decodeRawArgsStream runs the anthropic streaming decode with a scripted
// SSE body and returns the tool-call Arguments from the final response.
func decodeRawArgsStream(t *testing.T, sseBody string) []byte {
	t.Helper()
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, sseBody })
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
	argsContent := `{"path":"` + "\xff" + `file.txt"}`
	sseBody := rawArgsAnthropicSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
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
	argsContent := `{ "b" : 2 , "a" : 1 }`
	sseBody := rawArgsAnthropicSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_Stream_FallbackOnEmpty asserts a tool_use with no
// input_json_delta (empty args) still completes (degrade, never drop).
func TestRawArgs_Stream_FallbackOnEmpty(t *testing.T) {
	sseBody := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-x-wire\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"f\",\"input\":{}}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	srv, _ := protoServer(t, func(*http.Request) (int, string) { return 200, sseBody })
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

// Ensure imports are used.
var _ = protocolhttp.Result{}
