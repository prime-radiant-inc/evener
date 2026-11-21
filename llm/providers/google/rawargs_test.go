package google

import (
	"bytes"
	"context"
	"testing"

	"primeradiant.com/evener/llm"
)

func rawArgsJSON(t *testing.T, argsJSON string) []byte {
	t.Helper()
	return []byte(`{"id":"int_raw","model":"gemini-2.5-flash","status":"requires_action","steps":[{"type":"function_call","id":"call_1","name":"write_file","arguments":` + argsJSON + `}],"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}`)
}

func decodeRawArgsComplete(t *testing.T, body []byte) *llm.ToolCallData {
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
	return &calls[0]
}

func TestRawArgs_NonStream_InvalidUTF8(t *testing.T) {
	t.Parallel()
	argsJSON := `{"path":"` + "\xff" + `file.txt"}`
	body := rawArgsJSON(t, argsJSON)
	got := []byte(decodeRawArgsComplete(t, body).WireArguments())
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("non-stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

func TestRawArgs_NonStream_NonCanonicalJSON(t *testing.T) {
	t.Parallel()
	if c := decodeRawArgsComplete(t, rawArgsJSON(t, `{ "b" : 2 , "a" : 1 }`)); string(c.Arguments) != `{"b":2,"a":1}` || c.WireArguments() != `{ "b" : 2 , "a" : 1 }` {
		t.Fatalf("Arguments = %q, WireArguments = %q", c.Arguments, c.WireArguments())
	}
}

func TestRawArgs_NonStream_FallbackOnNull(t *testing.T) {
	t.Parallel()
	body := rawArgsJSON(t, `null`)
	srv, _ := protoServer(t, 200, string(body))
	res := protoLive(srv)
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), res)
	if err != nil {
		t.Fatalf("Complete (fallback): %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || string(calls[0].Arguments) != "{}" {
		t.Fatalf("got %+v, want 1 tool call with {}", calls)
	}
}

func rawArgsInteractionsSSE(argsJSON string) string {
	return "event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"write_file","arguments":{}}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_raw_sse","model":"gemini-2.5-flash","status":"requires_action","steps":[{"type":"function_call","id":"call_1","name":"write_file","arguments":` + argsJSON + `}],"usage":{"total_input_tokens":5,"total_output_tokens":2,"total_tokens":7}}}` + "\n\n"
}

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
	return []byte(calls[0].WireArguments())
}

func TestRawArgs_Stream_InvalidUTF8(t *testing.T) {
	t.Parallel()
	argsJSON := `{"path":"` + "\xff" + `file.txt"}`
	sseBody := rawArgsInteractionsSSE(argsJSON)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

func TestRawArgs_Stream_NonCanonicalJSON(t *testing.T) {
	t.Parallel()
	argsJSON := `{ "b" : 2 , "a" : 1 }`
	sseBody := rawArgsInteractionsSSE(argsJSON)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsJSON)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q, want %q", got, want)
	}
}
