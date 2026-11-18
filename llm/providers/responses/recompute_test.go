package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
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
	t.Run("terminal selections withdraw earlier calls", func(t *testing.T) {
		const call = `{"type":"function_call","id":"fc1","call_id":"call1","name":"write_fixture","arguments":"{}"}`
		const draft = `{"type":"response.output_text.delta","item_id":"msg1","output_index":1,"content_index":0,"delta":"draft"}`
		const refusal = " \trefusal-17\n"
		type part = map[string]string
		refused := part{"type": "refusal", "refusal": refusal}
		const reference = `{"type":"message","id":"msg1","role":"assistant"}`
		const selected = `{"output":[` + reference + `]}`
		refusalDone := func(index int, value string) string {
			return fmt.Sprintf(`{"type":"response.refusal.done","item_id":"msg1","output_index":1,"content_index":%d,"refusal":%s}`, index, jsonOf(t, value))
		}
		const secondText = `{"type":"response.output_text.done","item_id":"msg1","output_index":1,"content_index":1,"text":"second"}`
		refusalPartDone := `{"type":"response.content_part.done","item_id":"msg1","output_index":1,"content_index":0,"part":` + jsonOf(t, refused) + `}`
		completedRefusal := jsonOf(t, map[string]any{"type": "message", "id": "msg1", "role": "assistant", "content": []part{refused}})
		for _, tc := range []struct {
			name, text, terminal string
			wantCalls            int
			parts                []part
			completed            []string
			frames               []string
		}{
			{name: "refusal", text: refusal, parts: []part{refused}},
			{name: "empty selected refusal", parts: []part{{"type": "refusal", "refusal": ""}}},
			{name: "text and refusal", text: "selected" + refusal, parts: []part{{"type": "output_text", "text": "selected"}, refused}},
			{name: "selected completed refusal", text: refusal, terminal: `{"output":[` + reference + `]}`, parts: []part{refused}, completed: []string{completedRefusal}},
			{name: "metadata-only completion retains selected refusal", text: refusal, terminal: `{"output":[` + reference + `]}`, parts: []part{refused}, completed: []string{completedRefusal, reference}},
			{name: "selected refusal final without an item completion", text: refusal, terminal: selected, parts: []part{refused}, frames: []string{refusalDone(0, refusal)}},
			{name: "first refusal and later text settle in content order", text: refusal + "second", terminal: selected, parts: []part{refused, {"type": "output_text", "text": "second"}}, frames: []string{secondText, refusalDone(0, "earlier"), refusalDone(0, refusal), refusalPartDone}},
			{name: "last refusal follows a streamed text part", text: "draft" + refusal, terminal: selected, parts: []part{{"type": "output_text", "text": "draft"}, refused}, frames: []string{refusalDone(1, refusal)}},
			{name: "empty finalized refusal selects its message", terminal: selected, parts: []part{{"type": "refusal", "refusal": ""}}, frames: []string{refusalDone(0, "")}},
			{name: "completed refusal part selects its message", text: refusal, terminal: selected, parts: []part{refused}, frames: []string{refusalPartDone}},
			{name: "declared refusal inherits the completed message part", text: refusal, terminal: `{"output":[{"type":"message","id":"msg1","content":[{"type":"refusal"}]}]}`, parts: []part{refused}, completed: []string{completedRefusal}},
			{name: "terminal refusal text owns its slot", text: "terminal", parts: []part{{"type": "refusal", "refusal": "terminal"}}, frames: []string{refusalDone(0, refusal)}},
			{name: "explicit final answer withdraws other refusal and call", text: "final", parts: []part{{"type": "output_text", "text": "final"}}, frames: []string{`{"type":"response.refusal.done","item_id":"other","output_index":2,"content_index":0,"refusal":"other"}`}},
			{name: "terminal answer replaces completed refusal", text: "final", parts: []part{{"type": "output_text", "text": "final"}}, completed: []string{completedRefusal}},
			{name: "only a provider file search", terminal: `{"output":[{"type":"file_search_call","id":"fs1","status":"completed","queries":["fixture"]}]}`},
			{name: "empty output recovers the stream", text: "draft", terminal: `{"output":[]}`, wantCalls: 1},
			{name: "empty output recovers finalized message and issued call", text: refusal, terminal: `{"output":[]}`, frames: []string{refusalDone(0, refusal)}, wantCalls: 1},
			{name: "metadata-only terminal recovers the stream", text: "draft", terminal: `{"status":"completed"}`, wantCalls: 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				terminal := tc.terminal
				if terminal == "" {
					terminal = jsonOf(t, map[string]any{"output": []any{map[string]any{"type": "message", "id": "msg1", "role": "assistant", "content": tc.parts}}})
				}
				events := []string{
					`{"type":"response.output_item.added","output_index":0,"item":` + call + `}`,
					`{"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{}"}`,
					draft,
				}
				events = append(events, tc.frames...)
				for _, item := range tc.completed {
					events = append(events, `{"type":"response.output_item.done","output_index":1,"item":`+item+`}`)
				}
				events = append(events, `{"type":"response.completed","response":`+terminal+`}`)
				observed, live := responsesEvents(t, events...)
				if !hasEventType(observed, llm.StreamEventToolCallDelta) {
					t.Fatal("live accumulator never received the tentative tool call")
				}
				results := []llm.Response{*live, recorded(t, "data: "+strings.Join(events, "\n\ndata: ")+"\n\n")}
				if tc.parts != nil {
					full := jsonOf(t, map[string]any{"output": []any{map[string]any{"type": "message", "id": "msg1", "role": "assistant", "content": tc.parts}}})
					results = append(results, recorded(t, full))
				} else if tc.wantCalls == 0 {
					results = append(results, recorded(t, terminal))
				}
				for _, response := range results {
					calls := response.ToolCalls()
					if response.Text() != tc.text || len(calls) != tc.wantCalls {
						t.Errorf("settled text = %q, calls = %d; want %q and %d", response.Text(), len(calls), tc.text, tc.wantCalls)
					}
					if tc.parts == nil {
						if tc.wantCalls != 0 && (len(calls) != 1 || calls[0].ID != "call1" || string(calls[0].Arguments) != "{}") {
							t.Fatal("empty terminal lost the issued call or its arguments")
						}
						continue
					}
					var restored llm.Message
					if err := json.Unmarshal([]byte(jsonOf(t, response.Message)), &restored); err != nil {
						t.Fatal(err)
					}
					warm := nextInput(t, response)
					if cold := nextInput(t, llm.Response{Message: restored}); warm != cold {
						t.Fatal("restoring the message changed its native input")
					}
					want := jsonOf(t, []any{map[string]any{"type": "message", "role": "assistant", "content": tc.parts}})
					if warm != want {
						t.Errorf("next native input = %s, want %s", warm, want)
					}
					ordinary := res
					ordinary.Caps.ReplayReasoningText = new(false)
					if got := jsonOf(t, build(t, llm.Request{Model: res.ModelID, Messages: []llm.Message{restored}}, ordinary)["input"]); got != want {
						t.Errorf("ordinary Responses input = %s, want %s", got, want)
					}
				}
			})
		}
	})
	t.Run("settled reasoning retains distinct streamed summaries", func(t *testing.T) {
		const raw = " λ\t"
		const distinct = " λ\t "
		for _, tc := range []struct {
			name, display, secondSummary, wantDisplay string
			sparse, encrypted                         bool
		}{
			{name: "distinct summary", display: distinct, wantDisplay: distinct},
			{name: "same bytes as one typed content part", display: raw},
			{name: "distinct summary with ciphertext", display: distinct, wantDisplay: distinct, encrypted: true},
			{name: "distinct summary and sparse terminal", display: distinct, wantDisplay: distinct, sparse: true},
			{name: "only a matching summary part is suppressed", display: distinct, secondSummary: "other", wantDisplay: distinct},
			{name: "distinct summary parts keep stream order", display: distinct, secondSummary: "second", wantDisplay: distinct + "\n\nsecond"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				item := map[string]any{"type": "reasoning", "id": "rs", "content": []map[string]string{{"type": "reasoning_text", "text": "other"}, {"type": "reasoning_text", "text": raw}}}
				if tc.encrypted {
					item["encrypted_content"] = "cipher"
				}
				complete := jsonOf(t, map[string]any{"output": []any{item}})
				native := recorded(t, complete)
				wantInput := nextInput(t, native)
				events := []string{
					`{"type":"response.reasoning_summary_part.added","item_id":"rs","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
					`{"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":0,"delta":` + jsonOf(t, tc.display) + `}`,
					`{"type":"response.reasoning_part.added","item_id":"rs","output_index":0,"content_index":1,"part":{"type":"reasoning_text","text":""}}`,
					`{"type":"response.reasoning_text.delta","item_id":"rs","output_index":0,"content_index":1,"delta":` + jsonOf(t, raw) + `}`,
				}
				if tc.secondSummary != "" {
					events = append(events,
						`{"type":"response.reasoning_summary_part.added","item_id":"rs","output_index":0,"summary_index":1,"part":{"type":"summary_text","text":""}}`,
						`{"type":"response.reasoning_summary_text.delta","item_id":"rs","output_index":0,"summary_index":1,"delta":`+jsonOf(t, tc.secondSummary)+`}`)
				}
				terminal := complete
				if tc.sparse {
					events = append(events, `{"type":"response.output_item.done","output_index":0,"item":`+jsonOf(t, item)+`}`)
					terminal = `{"output":[]}`
				}
				events = append(events, `{"type":"response.completed","response":`+terminal+`}`)
				seen, live := responsesEvents(t, events...)
				if !slices.ContainsFunc(seen, func(e llm.StreamEvent) bool { return e.ReasoningDelta == tc.display }) {
					t.Fatal("stream omitted the summary delta")
				}
				saved := recorded(t, "data: "+strings.Join(events, "\n\ndata: ")+"\n\n")
				for _, response := range []llm.Response{*live, saved} {
					var stored llm.Message
					if err := json.Unmarshal([]byte(jsonOf(t, response.Message)), &stored); err != nil {
						t.Fatal(err)
					}
					if !slices.ContainsFunc(stored.Content, func(p llm.ContentPart) bool { return reflect.DeepEqual(p.Thinking, native.Message.Content[0].Thinking) }) {
						t.Error("saved native reasoning differs from the complete response")
					}
					var displays []string
					for _, part := range stored.Content {
						if th := part.Thinking; th != nil && th.ID == "" && th.EncryptedContent == "" && len(th.ResponsesTextParts) == 0 {
							displays = append(displays, th.Text)
						}
					}
					var want []string
					if tc.wantDisplay != "" {
						want = append(want, tc.wantDisplay)
					}
					if !slices.Equal(displays, want) {
						t.Errorf("saved display = %q, want %q", displays, want)
					}
					if nextInput(t, response) != wantInput || nextInput(t, llm.Response{Message: stored}) != wantInput {
						t.Error("streamed summary changed the native reasoning replay")
					}
				}
			})
		}
	})
	t.Run("completed message phase survives an omitted terminal field", func(t *testing.T) {
		const commentary = `"phase":"commentary",`
		const final = `"phase":"final_answer",`
		message := func(phase, first, second string) string {
			return `{"type":"message","id":"m",` + phase + `"content":[{"type":"output_text","text":` + jsonOf(t, first) + `},{"type":"output_text","text":` + jsonOf(t, second) + `}]}`
		}
		const second = " β\t"
		const null = `"phase":null,`
		const empty = `"phase":"",`
		for _, tc := range []struct{ name, completed, terminal, first, second, want string }{
			{"recover phase and missing part", commentary, "", "a", "", commentary},
			{"recover phase with repeated parts", commentary, "", "a", second, commentary},
			{"terminal phase wins", commentary, final, "a", "", final},
			{"explicitly cleared terminal phase", commentary, null, "a", "", ""},
			{"completed null clears announcement", null, "", "", "", ""},
			{"completed empty clears announcement", empty, "", "", "", ""},
			{"terminal replaces cleared completion", null, final, "", "", final},
			{"sparse terminal inherits announcement", "", "", "", "", commentary},
			{"complete terminal owns its unphased message", "", "", "a", second, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				want := nextInput(t, recorded(t, `{"output":[`+message(tc.want, "a", second)+`]}`))
				events := []string{
					`{"type":"response.output_item.added","item":` + message(commentary, "", "") + `}`,
					`{"type":"response.output_item.done","item":` + message(tc.completed, "a", second) + `}`,
					`{"type":"response.output_item.done","item":` + message("", "a", second) + `}`,
					`{"type":"response.completed","response":{"output":[` + message(tc.terminal, tc.first, tc.second) + `]}}`,
				}
				_, live := responsesEvents(t, events...)
				saved := recorded(t, "data: "+strings.Join(events, "\n\ndata: ")+"\n\n")
				for _, result := range []llm.Response{*live, saved} {
					if got := nextInput(t, result); got != want {
						t.Errorf("stream input = %s, complete input = %s", got, want)
					}
				}
			})
		}
	})

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
	reasoning := func(second *string, third string) string {
		part := map[string]string{"type": "reasoning_text"}
		if second != nil {
			part["text"] = *second
		}
		return `{"type":"reasoning","id":"r","content":[{"type":"reasoning_text","text":"owned"},` + jsonOf(t, part) + `,{"type":"reasoning_text","text":` + jsonOf(t, third) + `},{"type":"reasoning_text","text":""}]}`
	}
	finalText := func(identity string, index int, text string) string {
		return fmt.Sprintf(`{"type":"response.reasoning_text.done",%s,"content_index":%d,"text":%s}`, identity, index, jsonOf(t, text))
	}
	const declaredItem = `{"type":"response.output_item.added","output_index":1,"item":{"type":"reasoning","id":"r"}}`
	const summary = `"summary":[{"type":"summary_text","text":" s\t"}]`
	const absentParts = `{"type":"reasoning","id":"r",` + summary + `}`
	const nativeParts = `,"content":[{"type":"reasoning_text","text":" λ\t"},{"type":"reasoning_text","text":""},{"type":"reasoning_text","text":"\r\n<&>"}]`
	const nativeItem = `{"type":"reasoning","id":"r",` + summary + nativeParts + `}`
	const cleared = `{"type":"reasoning","id":"r","content":[],` + summary + `}`
	const encrypted = `{"type":"reasoning","id":"r","encrypted_content":"cipher",` + summary + `}`
	partAdded := func(identity string, index int, kind string) string {
		return fmt.Sprintf(`{"type":"response.reasoning_part.added",%s,"content_index":%d,"part":{"type":%s,"text":""}}`, identity, index, jsonOf(t, kind))
	}
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
		{"substantive terminal retains display", []string{"{" + display + "}", answerDelta, toolDone}, tool + "," + answer, tool + "," + answer},
		{"finalized reasoning fills only its native part", []string{
			"{" + display + "}",
			finalText(`"item_id":"r"`, 2, "\r\n<&>"),
			finalText(`"output_index":1`, 1, " λ\t"),
			finalText(`"item_id":"r","output_index":1`, 0, "superseded"),
			finalText(`"item_id":"other","output_index":0`, 3, "foreign"),
			`{"type":"response.reasoning_text.delta","item_id":"r","content_index":3,"delta":"interim"}`,
			`{"type":"response.reasoning_summary_text.done","item_id":"r","summary_index":3,"text":"summary"}`,
		}, answer + "," + reasoning(nil, "") + "," + tool, answer + "," + reasoning(new(" λ\t"), "\r\n<&>") + "," + tool},
		{"finalized part without a native item keeps answer and call positions", []string{
			"{" + display + "}", finalText(`"item_id":"r","output_index":0`, 0, "unmatched"), answerDelta, toolDone,
		}, "", answer + "," + tool},
		{"completed item owns text before part fallback", []string{
			"{" + display + "}", finalText(`"item_id":"r"`, 0, "earlier"), finalText(`"item_id":"r"`, 2, "\t"),
			`{"type":"response.output_item.done","item":` + reasoning(nil, "") + `}`,
		}, placeholder + "," + answer, reasoning(nil, "\t") + "," + answer},
		{"announced reasoning ID fills an omitted terminal ID", []string{"{" + display + "}", declaredItem},
			answer + `,{"type":"reasoning",` + summary + nativeParts + `},` + tool, answer + "," + nativeItem + "," + tool},
		{"explicit terminal reasoning ID owns its value", []string{"{" + display + "}", declaredItem},
			answer + `,{"type":"reasoning","id":"",` + summary + nativeParts + `}`, answer + `,{"type":"reasoning",` + summary + nativeParts + `}`},
		{"display delta alone cannot supply a native item ID", []string{
			"{" + display + "}", `{"type":"response.reasoning_text.delta","item_id":"display","output_index":1,"delta":"mirror"}`,
		}, answer + `,{"type":"reasoning",` + summary + nativeParts + `}`, answer + `,{"type":"reasoning",` + summary + nativeParts + `}`},
		{"finalized native text identifies its matching declared terminal slot", []string{
			"{" + display + "}", finalText(`"item_id":"r","output_index":1`, 0, " λ\t"),
		}, answer + `,{"type":"reasoning",` + summary + `,"content":[{"type":"reasoning_text"}]}`,
			answer + `,{"type":"reasoning","id":"r",` + summary + `,"content":[{"type":"reasoning_text","text":" λ\t"}]}`},
		{"announced reasoning slots replay their finalized bytes in index order", []string{
			"{" + display + "}", partAdded(`"item_id":"r"`, 2, "reasoning_text"), finalText(`"output_index":1`, 2, "\r\n<&>"),
			partAdded(`"output_index":1`, 0, "reasoning_text"), finalText(`"item_id":"r"`, 0, " λ\t"),
			declaredItem, partAdded(`"item_id":"r"`, 1, "reasoning_text"), finalText(`"item_id":"r"`, 1, ""),
			finalText(`"item_id":"r"`, 3, "no declared slot"),
		}, answer + "," + absentParts + "," + tool, answer + "," + nativeItem + "," + tool},
		{"empty finalized text keeps a native slot", []string{
			"{" + display + "}", partAdded(`"item_id":"r"`, 0, "reasoning_text"), finalText(`"item_id":"r"`, 0, ""),
		}, answer + "," + absentParts, answer + `,{"type":"reasoning","id":"r",` + summary + `,"content":[{"type":"reasoning_text","text":""}]}`},
		{"empty output keeps the announced and finalized slots at the item position", []string{
			"{" + display + "}", strings.Replace(toolDone, `"output_index":1`, `"output_index":2`, 1),
			declaredItem, partAdded(`"item_id":"r"`, 2, "reasoning_text"), finalText(`"output_index":1`, 2, "\r\n<&>"),
			partAdded(`"output_index":1`, 0, "reasoning_text"), finalText(`"item_id":"r"`, 0, " λ\t"),
			partAdded(`"item_id":"r"`, 1, "reasoning_text"), finalText(`"item_id":"r"`, 1, ""),
			partAdded(`"item_id":"r"`, 3, "reasoning_text"), finalText(`"item_id":"other"`, 3, "unmatched"),
			finalText(`"item_id":"r"`, 4, "undeclared"), answerDelta,
		}, "", answer + `,{"type":"reasoning","id":"r"` + nativeParts + `},` + tool},
		{"a named finalized part identifies its sparse item at its output index", []string{
			"{" + display + "}", partAdded(`"item_id":"r","output_index":1`, 0, "reasoning_text"),
			finalText(`"item_id":"r"`, 0, " λ\t"), answerDelta,
		}, "", answer + `,{"type":"reasoning","id":"r","content":[{"type":"reasoning_text","text":" λ\t"}]}`},
		{"typed terminal content owns its declared indexes", []string{
			"{" + display + "}", partAdded(`"item_id":"r"`, 3, "reasoning_text"), finalText(`"item_id":"r"`, 3, "withdrawn"),
		}, answer + "," + nativeItem, answer + "," + nativeItem},
		{"explicit empty terminal content owns its slots", []string{
			"{" + display + "}", declaredItem, partAdded(`"item_id":"r"`, 0, "reasoning_text"), finalText(`"item_id":"r"`, 0, "withdrawn"),
			`{"type":"response.output_item.done","item":` + nativeItem + `}`,
		}, answer + "," + cleared, answer},
		{"clearing completed text preserves ciphertext and the selected summary", []string{
			"{" + display + "}", `{"type":"response.output_item.done","item":{"type":"reasoning","id":"r","encrypted_content":"cipher"` + nativeParts + `}}`,
		}, cleared, encrypted},
		{"empty output keeps an item completion's empty content selection", []string{
			"{" + display + "}", partAdded(`"item_id":"r"`, 0, "reasoning_text"), finalText(`"item_id":"r"`, 0, "withdrawn"),
			`{"type":"response.output_item.done","item":` + cleared + `}`, answerDelta,
		}, "", answer},
		{"unknown announced kind cannot use a reasoning text final", []string{
			"{" + display + "}", partAdded(`"item_id":"r"`, 0, "unknown_text"), finalText(`"item_id":"r"`, 0, "unowned"),
		}, answer + "," + absentParts, answer},
		{"reasoning final belongs to a different item than the slot", []string{
			"{" + display + "}", partAdded(`"item_id":"r","output_index":1`, 0, "reasoning_text"), finalText(`"item_id":"other","output_index":1`, 0, "unowned"),
		}, answer + "," + absentParts, answer},
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
				var stored llm.Message
				if err := json.Unmarshal([]byte(jsonOf(t, result.Message)), &stored); err != nil {
					t.Fatal(err)
				}
				if got := nextInput(t, llm.Response{Message: stored}); got != want {
					t.Errorf("input after reload = %s, complete response = %s", got, want)
				}
				var displays []string
				for _, part := range stored.Content {
					if th := part.Thinking; tc.terminal == cleared && th != nil && th.EncryptedContent != "" && (th.Text != "" || len(th.ResponsesTextParts) != 0) {
						t.Errorf("cleared plaintext survived with the ciphertext: %q", th.Text)
					}
					if th := part.Thinking; th != nil && th.ID == "" && th.EncryptedContent == "" && len(th.ResponsesTextParts) == 0 {
						displays = append(displays, th.Text)
					}
				}
				if !slices.Equal(displays, []string{" D "}) {
					t.Errorf("saved display parts = %q, want the streamed part once", displays)
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
