package responses

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// syntheticResponsesSSE builds a raw Responses-API SSE body mirroring the
// affected wire shape: a function_call and a text message item arrive via
// response.output_item.done events, but the terminal response.completed
// payload's "output" is empty. This is the exact stored-body shape apilog
// --recompute must re-extract counts from.
func syntheticResponsesSSE() string {
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.created", `{"type":"response.created","response":{"id":"resp_1"}}`)
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\":\"x\"}"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hello"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Hello"}]}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	return b.String()
}

func TestExtractRecordedResponse_ResponsesSSE_TerminalWinsWhenNonEmpty(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Different terminal text"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Different terminal text" {
		t.Fatalf("Text() = %q, want terminal payload's text", resp.Text())
	}
	if calls := resp.ToolCalls(); len(calls) != 0 {
		t.Fatalf("ToolCalls() = %d, want 0: %+v", len(calls), calls)
	}

	res := resolved(openaiCaps)
	res.Caps.ReplayReasoningText = new(true)
	recorded := func(t *testing.T, body string) llm.Response {
		t.Helper()
		got, err := ExtractRecordedResponse([]byte(body), res.ModelID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	nextInput := func(t *testing.T, response llm.Response) string {
		t.Helper()
		return jsonOf(t, build(t, llm.Request{Model: res.ModelID, Messages: []llm.Message{response.Message}}, res)["input"])
	}
	t.Run("independently repeated fields replay identical input", func(t *testing.T) {
		fields := [4]string{"cipher", " λ\t", "\r\n<&>", " \t" + `{ "arg": 1.0 }` + "\n"}
		terminal := func(f [4]string) string {
			return `{"output":[` +
				`{"type":"reasoning","id":"rs","encrypted_content":` + jsonOf(t, f[0]) + `,"content":[{"type":"reasoning_text","text":"r"}],"summary":[]},` +
				`{"type":"message","id":"msg","content":[{"type":"output_text","text":` + jsonOf(t, f[1]) + `},{"type":"output_text","text":` + jsonOf(t, f[2]) + `}]},` +
				`{"type":"function_call","id":"fc","call_id":"call","name":"f","arguments":` + jsonOf(t, f[3]) + `}]}`
		}
		events := strings.Join([]string{
			`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs","encrypted_content":` + jsonOf(t, fields[0]) + `}}`,
			`data: {"type":"response.output_text.delta","item_id":"msg","content_index":1,"delta":` + jsonOf(t, fields[2]) + `}`,
			`data: {"type":"response.output_text.delta","item_id":"msg","content_index":0,"delta":` + jsonOf(t, fields[1]) + `}`,
			`data: {"type":"response.function_call_arguments.done","item_id":"fc","call_id":"call","arguments":` + jsonOf(t, fields[3]) + `}`,
		}, "\n\n") + "\n\n"
		want := nextInput(t, recorded(t, terminal(fields)))
		for omitted := range 1 << len(fields) {
			t.Run(fmt.Sprintf("omitted_%04b", omitted), func(t *testing.T) {
				repeated := fields
				for i := range repeated {
					if omitted&(1<<i) != 0 {
						repeated[i] = ""
					}
				}
				stream := events + `data: {"type":"response.completed","response":` + terminal(repeated) + "}\n\n"
				if got := nextInput(t, recorded(t, stream)); got != want {
					t.Errorf("input from stream = %s, complete response = %s", got, want)
				}
			})
		}
	})

	const display = `"type":"response.reasoning_text.delta","delta":" D "`
	const answer = `{"type":"message","id":"m","content":[{"type":"output_text","text":" A "}]}`
	const tool = `{"type":"function_call","call_id":"c","name":"f","arguments":" { } "}`
	const placeholder = `{"type":"reasoning","id":"r","content":[{"type":"reasoning_text","text":""}]}`
	const answerDelta = `{"type":"response.output_text.delta","item_id":"m","delta":" A "}`
	const toolDone = `{"type":"response.output_item.done","output_index":1,"item":` + tool + `}`
	for _, tc := range []struct {
		name     string
		events   []string
		terminal string
		complete string
	}{
		{"anonymous display before unindexed answer", []string{"{" + display + "}", answerDelta, toolDone}, "", answer + "," + tool},
		{"indexed display before unindexed answer", []string{`{"output_index":0,` + display + "}", answerDelta, toolDone}, "", answer + "," + tool},
		{"native call claims its index after display", []string{`{"output_index":1,` + display + "}", toolDone, answerDelta}, "", answer + "," + tool},
		{"sparse terminal answer after display", []string{"{" + display + "}", answerDelta, toolDone}, `{"type":"message","id":"m","content":[{"type":"output_text","text":""}]}`, answer + "," + tool},
		{"terminal reasoning owns its native slot", []string{`{"item_id":"r","output_index":0,` + display + "}", answerDelta, toolDone}, placeholder, placeholder + "," + tool + "," + answer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := nextInput(t, recorded(t, `{"output":[`+tc.complete+`]}`))
			events := slices.Concat(tc.events, []string{`{"type":"response.completed","response":{"output":[` + tc.terminal + `]}}`})
			_, live := responsesEvents(t, events...)
			saved := recorded(t, "data: "+strings.Join(events, "\n\ndata: ")+"\n\n")
			for _, result := range []llm.Response{*live, saved} {
				if got := nextInput(t, result); got != want {
					t.Errorf("input from stream = %s, complete response = %s", got, want)
				}
				var displays int
				for _, part := range result.Message.Content {
					if th := part.Thinking; th != nil && th.Text == " D " && th.ID == "" && len(th.ResponsesTextParts) == 0 {
						displays++
					}
				}
				if displays != 1 {
					t.Errorf("settled display parts = %d, want 1", displays)
				}
			}
		})
	}
}

func TestExtractRecordedResponse_ResponsesSSE_TerminalRawArguments(t *testing.T) {
	t.Parallel()
	args := `{ "path" : "` + "\xff" + `" }`
	payload := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","status":"completed","output":[{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":`)
	payload = append(payload, jsonStringToken(args)...)
	payload = append(payload, []byte(`}]}}`)...)
	body := append([]byte("event: response.completed\ndata: "), payload...)
	body = append(body, '\n', '\n')

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Arguments, []byte(args)) {
		t.Fatalf("Arguments = %q (% x), want %q (% x)", calls[0].Arguments, calls[0].Arguments, args, []byte(args))
	}
}

func TestExtractRecordedResponse_ResponsesJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"resp_1","model":"gpt-5.2","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi there"}]}]}`)

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "hi there" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "hi there")
	}
}

func TestExtractRecordedResponse_RejectsChatCompletionsJSON(t *testing.T) {
	t.Parallel()
	body := []byte(`{"id":"chatcmpl_1","model":"gpt-5.2","choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"call_1","function":{"name":"write_file","arguments":"{\"path\":\"x\"}"}}]}}]}`)

	if _, err := ExtractRecordedResponse(body, "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedResponse accepted a Chat Completions JSON body; want an error directing callers elsewhere")
	}
}

func TestExtractRecordedResponse_RejectsChatCompletionsSSE(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	write := func(data string) {
		b.WriteString("data: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write(`{"model":"gpt-5.2","choices":[{"delta":{"content":"Hello"}}]}`)
	write(`[DONE]`)

	if _, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2"); err == nil {
		t.Fatal("ExtractRecordedResponse accepted a Chat Completions SSE body; want an error directing callers elsewhere")
	}
}

func TestExtractRecordedResponse_ResponsesSSE_SynthesizesFromAccumulatedItems(t *testing.T) {
	t.Parallel()
	body := []byte(syntheticResponsesSSE())

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("Text() = %q, want %q", resp.Text(), "Hello")
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1 (%+v)", len(calls), calls)
	}
	if calls[0].Name != "write_file" {
		t.Fatalf("tool call name = %q, want write_file", calls[0].Name)
	}
}

func TestExtractRecordedResponse_ResponsesSSE_AccumulatedRawArguments(t *testing.T) {
	t.Parallel()
	args := `{ "path" : "` + "\xfe" + `" }`
	done := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":`)
	done = append(done, jsonStringToken(args)...)
	done = append(done, []byte(`}}`)...)
	body := append([]byte("event: response.output_item.done\ndata: "), done...)
	body = append(body, []byte("\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2\",\"status\":\"completed\",\"output\":[]}}\n\n")...)

	resp, err := ExtractRecordedResponse(body, "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Arguments, []byte(args)) {
		t.Fatalf("Arguments = %q (% x), want %q (% x)", calls[0].Arguments, calls[0].Arguments, args, []byte(args))
	}
}

func TestExtractRecordedResponse_ResponsesSSE_ArgumentsDoneRawArguments(t *testing.T) {
	t.Parallel()
	want := []byte(`{"path":"` + "\xfe" + `final.txt"}`)
	resp, err := ExtractRecordedResponse(rawArgumentsDoneSSE(string(want)), "gpt-5.5")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Arguments, want) {
		t.Fatalf("Arguments = %q (% x), want %q (% x)", calls[0].Arguments, calls[0].Arguments, want, want)
	}
}

func TestExtractRecordedResponse_ResponsesSSE_MultipleArgumentsDonePreserveCallRouting(t *testing.T) {
	t.Parallel()
	want := [][]byte{
		[]byte(`{"first":"` + "\xff" + `final"}`),
		[]byte(`{"second":"` + "\xfe" + `final"}`),
	}
	resp, err := ExtractRecordedResponse(rawMultipleArgumentsDoneSSE(string(want[0]), string(want[1])), "gpt-5.5")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	assertRawArgsByIndex(t, responseToolCallArguments(resp), want)
}

func TestExtractRecordedResponse_ResponsesSSE_DeltaOnlyRawArguments(t *testing.T) {
	t.Parallel()
	fragments := []string{`{"path":"`, "\xfffile.txt\"}"}
	want := append([]byte(fragments[0]), []byte(fragments[1])...)
	resp, err := ExtractRecordedResponse(rawDeltaOnlySSE("delta", fragments...), "gpt-5.5")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	assertRawArgsByIndex(t, responseToolCallArguments(resp), [][]byte{want})
}

func TestExtractRecordedResponse_ResponsesSSE_ArgumentsFieldDeltaPreservesRawBytes(t *testing.T) {
	t.Parallel()
	fragments := []string{`{"path":"`, "\xfffile.txt\"}"}
	want := append([]byte(fragments[0]), []byte(fragments[1])...)
	resp, err := ExtractRecordedResponse(rawDeltaOnlySSE("arguments", fragments...), "gpt-5.5")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	assertRawArgsByIndex(t, responseToolCallArguments(resp), [][]byte{want})
}

// TestExtractRecordedResponse_EmptyBody covers the empty body error path.
func TestExtractRecordedResponse_EmptyBody(t *testing.T) {
	t.Parallel()
	if _, err := ExtractRecordedResponse([]byte("   "), "gpt-5.2"); err == nil {
		t.Fatal("empty body should error")
	}
}

// TestExtractRecordedResponse_InvalidJSON covers the JSON decode error path.
func TestExtractRecordedResponse_InvalidJSON(t *testing.T) {
	t.Parallel()
	if _, err := ExtractRecordedResponse([]byte("{invalid"), "gpt-5.2"); err == nil {
		t.Fatal("invalid JSON should error")
	}
}

// TestIsChatCompletionsSSE_EmptyData covers the empty data path in the SSE
// scanner.
func TestIsChatCompletionsSSE_EmptyData(t *testing.T) {
	t.Parallel()
	// An SSE body with only empty data events should return false.
	body := []byte("data: \n\ndata: \n\n")
	if isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with only empty data should return false")
	}
}

// TestIsChatCompletionsSSE_DoneMarker covers the [DONE] marker path.
func TestIsChatCompletionsSSE_DoneMarker(t *testing.T) {
	t.Parallel()
	// An SSE body with [DONE] should return true.
	body := []byte("data: [DONE]\n\n")
	if !isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with [DONE] should return true")
	}
}

// TestIsChatCompletionsSSE_ChoicesInData covers the choices detection path.
func TestIsChatCompletionsSSE_ChoicesInData(t *testing.T) {
	t.Parallel()
	body := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	if !isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with choices in data should return true")
	}
}

// TestIsChatCompletionsSSE_NonChoicesSSE returns false for a Responses-API SSE.
func TestIsChatCompletionsSSE_NonChoicesSSE(t *testing.T) {
	t.Parallel()
	body := []byte("data: {\"type\":\"response.created\"}\n\n")
	if isChatCompletionsSSE(body) {
		t.Fatal("isChatCompletionsSSE with Responses API event should return false")
	}
}

// TestExtractResponsesFromSSE_EmptyDataEvent covers the len(ev.Data)==0 path.
func TestExtractResponsesFromSSE_EmptyDataEvent(t *testing.T) {
	t.Parallel()
	// SSE with an empty data event followed by a valid response.completed.
	body := "data: \n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_InvalidJSONPayload covers the decodeSSEPayload
// false return path.
func TestExtractResponsesFromSSE_InvalidJSONPayload(t *testing.T) {
	t.Parallel()
	// An invalid JSON payload should be skipped, not cause an error.
	body := "data: {not json}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_FallbackToEventName covers the typ=="" path
// where the event name is used as the type.
func TestExtractResponsesFromSSE_FallbackToEventName(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("event: response.completed\n")
	b.WriteString("data: {\"response\":{\"id\":\"r1\",\"model\":\"m\",\"output\":[]}}\n\n")
	resp, err := ExtractRecordedResponse([]byte(b.String()), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_FunctionCallArgumentsDone covers the
// response.function_call_arguments.done event.
func TestExtractResponsesFromSSE_FunctionCallArgumentsDone(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	write := func(event, data string) {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteString("\ndata: ")
		b.WriteString(data)
		b.WriteString("\n\n")
	}
	write("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file"}}`)
	write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","delta":"{\"path\""}`)
	write("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","call_id":"call_1","arguments":"{\"path\":\"x\"}"}`)
	write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","id":"item_1","name":"write_file","arguments":"{\"path\":\"x\"}"}}`)
	write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)

	resp, err := ExtractRecordedResponse([]byte(b.String()), "gpt-5.2")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("ToolCalls() = %d, want 1", len(calls))
	}
}

// TestExtractResponsesFromSSE_ResponseCompletedNoResponseField covers the
// rawResp == nil fallback.
func TestExtractResponsesFromSSE_ResponseCompletedNoResponseField(t *testing.T) {
	t.Parallel()
	// response.completed with no "response" key — the payload itself is used.
	body := "data: {\"type\":\"response.completed\",\"id\":\"r1\",\"model\":\"m\",\"output\":[]}\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("ID = %q, want r1", resp.ID)
	}
}

// TestExtractResponsesFromSSE_NoResponseCompleted covers the missing
// response.completed event error.
func TestExtractResponsesFromSSE_NoResponseCompleted(t *testing.T) {
	t.Parallel()
	body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n"
	if _, err := ExtractRecordedResponse([]byte(body), "m"); err == nil {
		t.Fatal("SSE without response.completed should error")
	}
}

// TestDecodeJSONObject_Invalid covers the JSON decode error.
func TestDecodeJSONObject_Invalid(t *testing.T) {
	t.Parallel()
	if _, err := decodeJSONObject([]byte("{invalid")); err == nil {
		t.Fatal("decodeJSONObject with invalid JSON should error")
	}
}

// TestDecodeSSEPayload_Invalid covers the JSON decode error.
func TestDecodeSSEPayload_Invalid(t *testing.T) {
	t.Parallel()
	if _, ok := decodeSSEPayload([]byte("{invalid")); ok {
		t.Fatal("decodeSSEPayload with invalid JSON should return false")
	}
}
