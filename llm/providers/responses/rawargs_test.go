package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// rawArgsJSON builds a Responses API non-streaming response body whose
// function_call item's arguments field carries the given raw content as a
// JSON string value, preserving raw bytes (including invalid UTF-8).
func rawArgsJSON(t *testing.T, argsContent string) []byte {
	t.Helper()
	argToken := jsonStringToken(argsContent)
	// Build the body using a struct with json.RawMessage for arguments.
	outer := struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Name   string          `json:"name"`
			Args   json.RawMessage `json:"arguments,omitempty"`
		} `json:"output"`
	}{
		ID:     "resp_1",
		Status: "completed",
		Model:  "gpt-5.5",
		Output: []struct {
			ID     string          `json:"id"`
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Name   string          `json:"name"`
			Args   json.RawMessage `json:"arguments,omitempty"`
		}{
			{
				ID:     "fc_1",
				Type:   "function_call",
				CallID: "call_1",
				Name:   "write_file",
				Args:   json.RawMessage(argToken),
			},
		},
	}
	b, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return b
}

// jsonStringToken builds a JSON string token (with surrounding quotes) from
// raw content, escaping only the characters JSON requires for a valid
// string token while preserving raw bytes >= 0x80.
func jsonStringToken(content string) []byte {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, c := range []byte(content) {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.Bytes()
}

// decodeRawArgsComplete runs the responses Complete decode path with a
// scripted response body and returns the tool-call Arguments.
func decodeRawArgsComplete(t *testing.T, body []byte) []byte {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(nil)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: "/responses/input_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	return calls[0].Arguments
}

// TestRawArgs_NonStream_InvalidUTF8 asserts invalid-UTF-8 bytes in
// tool-call arguments survive the responses non-streaming decode.
func TestRawArgs_NonStream_InvalidUTF8(t *testing.T) {
	argsContent := `{"path":"` + "\xff" + `file.txt"}`
	body := rawArgsJSON(t, argsContent)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("non-stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

// TestRawArgs_NonStream_NonCanonicalJSON asserts non-canonical valid JSON
// survives the decode byte-identical.
func TestRawArgs_NonStream_NonCanonicalJSON(t *testing.T) {
	argsContent := `{ "b" : 2 , "a" : 1 }`
	body := rawArgsJSON(t, argsContent)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_NonStream_FallbackOnNull asserts null arguments fall back
// gracefully (degrade, never drop).
func TestRawArgs_NonStream_FallbackOnNull(t *testing.T) {
	body := []byte(`{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"f","arguments":null}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(nil)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: "/responses/input_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("Complete (fallback): %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	if string(calls[0].Arguments) != "" {
		t.Fatalf("fallback Arguments = %q, want empty (null maps to empty string)", calls[0].Arguments)
	}
}

// --- Streaming ---

// rawArgsResponsesSSE builds a Responses API SSE stream where the tool-call
// arguments are delivered via function_call_arguments.delta and
// output_item.done events. The arguments content may contain invalid UTF-8.
func rawArgsResponsesSSE(argsContent string) string {
	argEsc := string(jsonStringToken(argsContent))
	// Strip the surrounding quotes for the delta (delta is the raw fragment).
	// Actually, the delta field in the SSE event is a JSON string value. The
	// arguments arrive as one delta. We deliver the full args content in one
	// delta event.
	deltaEsc := argEsc // includes quotes; the delta field is a JSON string

	return "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + deltaEsc + "}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":" + deltaEsc + "}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\",\"arguments\":" + deltaEsc + "}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\",\"arguments\":" + deltaEsc + "}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"
}

// rawArgumentsDoneSSE builds the gateway fallback shape where the authoritative
// function_call_arguments.done event is the last event carrying arguments:
// output_item.done omits them and response.completed has empty output.
func rawArgumentsDoneSSE(doneArgs string) []byte {
	firstDelta := `{"path":"`
	secondDelta := "\xffdraft.txt\"}"
	doneToken := string(jsonStringToken(doneArgs))
	return []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + string(jsonStringToken(firstDelta)) + "}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + string(jsonStringToken(secondDelta)) + "}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":" + doneToken + "}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n")
}

// rawMultipleArgumentsDoneSSE interleaves two calls' delta fragments, then
// makes each arguments.done event authoritative with distinct raw bytes.
func rawMultipleArgumentsDoneSSE(firstDone, secondDone string) []byte {
	firstPrefix := `{"first":"`
	firstSuffix := "\xfddraft\"}"
	secondPrefix := `{"second":"`
	secondSuffix := "\xfcdraft\"}"
	return []byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"first\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"second\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_2\",\"delta\":" + string(jsonStringToken(secondPrefix)) + "}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + string(jsonStringToken(firstPrefix)) + "}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_2\",\"delta\":" + string(jsonStringToken(secondSuffix)) + "}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + string(jsonStringToken(firstSuffix)) + "}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"arguments\":" + string(jsonStringToken(firstDone)) + "}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"call_id\":\"call_2\",\"item_id\":\"fc_2\",\"arguments\":" + string(jsonStringToken(secondDone)) + "}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"first\"}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"second\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[]}}\n\n")
}

// rawDeltaOnlySSE builds the gateway fallback shape where only argument
// fragments carry the arguments: output_item.done omits them and terminal
// output is empty. field selects the documented delta field or the gateway
// arguments-field fallback accepted by HandleFunctionCallArgumentsDelta.
func rawDeltaOnlySSE(field string, fragments ...string) []byte {
	var b bytes.Buffer
	b.WriteString("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\"}}\n\n")
	for _, fragment := range fragments {
		b.WriteString("event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"")
		b.WriteString(field)
		b.WriteString("\":")
		b.Write(jsonStringToken(fragment))
		b.WriteString("}\n\n")
	}
	b.WriteString("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"write_file\"}}\n\n")
	b.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[]}}\n\n")
	return b.Bytes()
}

// decodeRawArgsStream runs the responses streaming decode with a scripted
// SSE body and returns the tool-call Arguments from the final response.
func decodeRawArgsStream(t *testing.T, sseBody string) []byte {
	t.Helper()
	srv, _ := server(t, 200, sseBody)
	res := liveRes(srv, nil)
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
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
	sseBody := rawArgsResponsesSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

func TestRawArgs_Stream_ArgumentsDonePreservesRawBytes(t *testing.T) {
	want := []byte(`{"path":"` + "\xfe" + `final.txt"}`)
	srv, _ := server(t, http.StatusOK, string(rawArgumentsDoneSSE(string(want))))
	res := liveRes(srv, nil)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatal(err)
	}

	var endArgs, settledArgs []byte
	for ev := range s.Events() {
		switch ev.Type {
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		case llm.StreamEventToolCallEnd:
			endArgs = append([]byte(nil), ev.ToolCall.Arguments...)
		case llm.StreamEventFinish:
			calls := ev.Response.ToolCalls()
			if len(calls) != 1 {
				t.Fatalf("settled ToolCalls() = %d, want 1", len(calls))
			}
			settledArgs = append([]byte(nil), calls[0].Arguments...)
		}
	}
	if !bytes.Equal(endArgs, want) {
		t.Fatalf("ToolCallEnd Arguments = %q (% x), want %q (% x)", endArgs, endArgs, want, want)
	}
	if !bytes.Equal(settledArgs, want) {
		t.Fatalf("settled Arguments = %q (% x), want %q (% x)", settledArgs, settledArgs, want, want)
	}
}

func TestRawArgs_Stream_MultipleArgumentsDonePreserveCallRouting(t *testing.T) {
	want := [][]byte{
		[]byte(`{"first":"` + "\xff" + `final"}`),
		[]byte(`{"second":"` + "\xfe" + `final"}`),
	}
	srv, _ := server(t, http.StatusOK, string(rawMultipleArgumentsDoneSSE(string(want[0]), string(want[1]))))
	res := liveRes(srv, nil)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatal(err)
	}

	endArgs := map[string][]byte{}
	var settledArgs [][]byte
	for ev := range s.Events() {
		switch ev.Type {
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		case llm.StreamEventToolCallEnd:
			if _, exists := endArgs[ev.ToolCall.ID]; exists {
				t.Fatalf("duplicate ToolCallEnd for %q", ev.ToolCall.ID)
			}
			endArgs[ev.ToolCall.ID] = append([]byte(nil), ev.ToolCall.Arguments...)
		case llm.StreamEventFinish:
			settledArgs = responseToolCallArguments(*ev.Response)
		}
	}
	if len(endArgs) != len(want) {
		t.Fatalf("ToolCallEnd count = %d, want %d", len(endArgs), len(want))
	}
	for i, callID := range []string{"call_1", "call_2"} {
		if !bytes.Equal(endArgs[callID], want[i]) {
			t.Fatalf("ToolCallEnd %s Arguments = %q (% x), want %q (% x)", callID, endArgs[callID], endArgs[callID], want[i], want[i])
		}
	}
	assertRawArgsByIndex(t, settledArgs, want)
}

func TestRawArgs_Stream_ArgumentsFieldDeltaPreservesRawBytes(t *testing.T) {
	fragments := []string{`{"path":"`, "\xfffile.txt\"}"}
	want := append([]byte(fragments[0]), []byte(fragments[1])...)
	srv, _ := server(t, http.StatusOK, string(rawDeltaOnlySSE("arguments", fragments...)))
	res := liveRes(srv, nil)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatal(err)
	}

	var deltaArgs, endArgs, settledArgs []byte
	for ev := range s.Events() {
		switch ev.Type {
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		case llm.StreamEventToolCallDelta:
			deltaArgs = append(deltaArgs, ev.ToolCall.Arguments...)
		case llm.StreamEventToolCallEnd:
			endArgs = append([]byte(nil), ev.ToolCall.Arguments...)
		case llm.StreamEventFinish:
			calls := ev.Response.ToolCalls()
			if len(calls) != 1 {
				t.Fatalf("settled ToolCalls() = %d, want 1", len(calls))
			}
			settledArgs = append([]byte(nil), calls[0].Arguments...)
		}
	}
	for _, got := range []struct {
		label string
		args  []byte
	}{
		{label: "ToolCallDelta", args: deltaArgs},
		{label: "ToolCallEnd", args: endArgs},
		{label: "settled", args: settledArgs},
	} {
		if !bytes.Equal(got.args, want) {
			t.Errorf("%s Arguments = %q (% x), want %q (% x)", got.label, got.args, got.args, want, want)
		}
	}
}

// TestRawArgs_Stream_NonCanonicalJSON asserts non-canonical valid JSON in
// streamed tool-call arguments survives the decode byte-identical.
func TestRawArgs_Stream_NonCanonicalJSON(t *testing.T) {
	argsContent := `{ "b" : 2 , "a" : 1 }`
	sseBody := rawArgsResponsesSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_Stream_FallbackOnNull asserts null arguments in the stream
// fall back gracefully (degrade, never drop).
func TestRawArgs_Stream_FallbackOnNull(t *testing.T) {
	sseBody := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"f\"}}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"f\",\"arguments\":null}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"f\",\"arguments\":null}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"
	srv, _ := server(t, 200, sseBody)
	res := liveRes(srv, nil)
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
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
	body := []byte(`{"id":"resp_1","status":"completed","model":"gpt-5.5","output":[` +
		`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"first","arguments":` + string(jsonStringToken(first)) + `},` +
		`{"id":"fc_2","type":"function_call","call_id":"call_2","name":"second","arguments":` + string(jsonStringToken(second)) + `}` +
		`]}`)

	got := decodeRawArgsCompleteMany(t, body)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second)})
}

func TestRawArgs_Stream_MultipleToolCallsPreserveIndex(t *testing.T) {
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	firstToken := string(jsonStringToken(first))
	secondToken := string(jsonStringToken(second))
	sseBody := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"first\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":" + firstToken + "}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"first\",\"arguments\":" + firstToken + "}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"second\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_2\",\"delta\":" + secondToken + "}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"second\",\"arguments\":" + secondToken + "}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"first\",\"arguments\":" + firstToken + "},{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"second\",\"arguments\":" + secondToken + "}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"

	got := decodeRawArgsStreamMany(t, sseBody)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second)})
}

func decodeRawArgsCompleteMany(t *testing.T, body []byte) [][]byte {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(nil)
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/responses", StreamEndpoint: "/responses", ModelsEndpoint: "/models", CountTokensEndpoint: "/responses/input_tokens"}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return responseToolCallArguments(resp)
}

func decodeRawArgsStreamMany(t *testing.T, sseBody string) [][]byte {
	t.Helper()
	srv, _ := server(t, http.StatusOK, sseBody)
	res := liveRes(srv, nil)
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
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
	return responseToolCallArguments(*final)
}

func responseToolCallArguments(resp llm.Response) [][]byte {
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
