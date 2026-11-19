package responses

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// Some Responses-API gateways (lunaroute fronting GLM, observed live) stream
// reasoning as raw reasoning_text events on the reasoning item's content
// rather than as OpenAI's reasoning_summary events. The decoder must surface
// those as reasoning deltas or the UI shows nothing for minutes at a time
// while the model thinks. Two parts are sent so the part separator is
// asserted too.
func TestStreamEmitsReasoningTextDeltas(t *testing.T) {
	t.Parallel()
	var sse strings.Builder
	for _, ev := range []string{
		`{"type":"response.reasoning_part.added","item_id":"rs_1","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"Let me "}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":0,"delta":"think."}`,
		`{"type":"response.reasoning_part.added","item_id":"rs_1","output_index":0,"content_index":1,"part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.reasoning_text.delta","item_id":"rs_1","output_index":0,"content_index":1,"delta":"Then verify."}`,
		`{"type":"response.output_text.delta","delta":"Answer"}`,
		`{"type":"response.completed","response":{"id":"resp_1","model":"glm-5.3","output":[{"type":"message","content":[{"type":"output_text","text":"Answer"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
	} {
		sse.WriteString("data: " + ev + "\n\n")
	}
	srv, _ := server(t, 200, sse.String())
	stream, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), userReq("hi"), liveRes(srv, openaiCaps))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var reasoning strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventReasoningDelta {
			reasoning.WriteString(ev.ReasoningDelta)
		}
	}
	if got := reasoning.String(); got != "Let me think.\n\nThen verify." {
		t.Fatalf("reasoning stream = %q, want %q", got, "Let me think.\n\nThen verify.")
	}
}

// A reasoning item that carries plain reasoning_text content (no
// encrypted_content) must still land in the settled message as a thinking
// part, or the transcript loses everything the model thought. Parts are
// joined with the same blank line the stream emits between them, so the
// settled text reads like the live view. Replay is unaffected:
// toResponsesInput sends a reasoning item back only when it has
// encrypted_content, so the raw text never returns to this API.
func TestResponseContentFromOutputItems_KeepsReasoningTextContent(t *testing.T) {
	t.Parallel()
	var out []any
	if err := json.Unmarshal([]byte(`[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me think."},{"type":"reasoning_text","text":"Then verify."}],"summary":[]},
		{"type":"message","content":[{"type":"output_text","text":"Answer"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out, nil)
	if len(content) != 2 {
		t.Fatalf("content parts = %d, want 2 (thinking + text): %#v", len(content), content)
	}
	th := content[0]
	if th.Kind != llm.ContentThinking || th.Thinking == nil {
		t.Fatalf("content[0] = %#v, want thinking part", th)
	}
	if th.Thinking.Text != "Let me think.\n\nThen verify." {
		t.Fatalf("thinking text = %q, want %q", th.Thinking.Text, "Let me think.\n\nThen verify.")
	}
	if th.Thinking.ID != "rs_1" {
		t.Fatalf("thinking id = %q, want rs_1", th.Thinking.ID)
	}
	if th.Thinking.EncryptedContent != "" {
		t.Fatalf("encrypted content = %q, want empty", th.Thinking.EncryptedContent)
	}
}

func TestStreamTerminalTypedReasoningPreservesAccumulatedAnswer(t *testing.T) {
	t.Parallel()
	_, final := responsesEvents(t,
		`{"type":"response.reasoning_summary_part.added"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"summary"}`,
		`{"type":"response.reasoning_summary_part.added"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"streamed only"}`,
		`{"type":"response.output_text.delta","delta":"answer"}`,
		`{"type":"response.completed","response":{"id":"resp_1","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"summary"},{"type":"summary_text","text":"terminal only"}]}]}}`,
	)
	if final == nil || final.Text() != "answer" || len(final.Message.Content) != 3 {
		t.Fatalf("settled response = %+v, want accumulated answer", final)
	}
	want := []llm.ResponsesReasoningPart{{Type: "summary_text", Text: "summary"}, {Type: "summary_text", Text: "terminal only"}}
	if th := final.Message.Content[0].Thinking; th == nil ||
		!slices.Equal(th.ResponsesSummaryParts, want) || !slices.Equal(th.Summary, []string{"terminal only"}) {
		t.Fatalf("settled reasoning = %+v, want %v with only the terminal-only display summary", th, want)
	}
	if got := final.ReasoningText(); got != "summary\n\nstreamed only" {
		t.Fatalf("settled display reasoning = %q, want both streamed parts", got)
	}
}

func TestStreamSettlesOutputWhenTerminalReasoningHasNoReplayableContent(t *testing.T) {
	t.Parallel()
	events := []string{
		`{"type":"response.reasoning_summary_text.delta","delta":"streamed thought"}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_streamed","encrypted_content":"opaque continuation","summary":[{"type":"summary_text","text":"settled summary"}]}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"comm"}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_commentary","delta":"entary "}`,
		`{"type":"response.output_item.done","item":{"type":"message","id":"msg_commentary","phase":"commentary","content":[{"type":"output_text","text":"commentary "}]}}`,
		`{"type":"response.output_text.delta","delta":"detail "}`,
		`{"type":"response.output_item.done","item":{"type":"message","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","delta":"draft "}`,
		`{"type":"response.function_call_arguments.delta","call_id":"call_unfinished","name":"probe","delta":"{}"}`,
		`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"draft "}]}}`,
		`{"type":"response.output_text.delta","item_id":"msg_answer","delta":"answer"}`,
		`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{ \"n\" : 1 }"}}`,
	}
	const summaryOutput = `[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"summary"}]}]`
	for _, tc := range []struct{ event, status, finish, output string }{
		{"response.completed", "completed", llm.FinishReasonToolCalls, summaryOutput},
		{"response.completed", "incomplete", llm.FinishReasonLength, summaryOutput},
		{"response.incomplete", "incomplete", llm.FinishReasonLength, summaryOutput},
		{"response.incomplete", "incomplete", llm.FinishReasonLength, `[]`},
	} {
		events := append(events, `{"type":"`+tc.event+`","response":{"id":"resp_1","status":"`+tc.status+`","output":`+tc.output+`}}`)
		body := "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
		if tc.status == "completed" {
			body = strings.NewReplacer(`"output_index":1,`, "", `"item_id":"msg_commentary",`, "", `"id":"msg_commentary",`, "", `"item_id":"msg_answer",`, "").Replace(body)
		}
		recorded, err := ExtractRecordedResponse([]byte(body), "gpt-5.5")
		if err != nil {
			t.Error(err)
		}
		streamed, _ := responsesEventsFromSSE(t, body)
		if len(streamed) == 0 || streamed[len(streamed)-1].Type != llm.StreamEventFinish {
			t.Fatalf("stream did not emit a finish event: %+v", streamed)
		}
		for _, response := range []*llm.Response{streamed[len(streamed)-1].Response, &recorded} {
			if response == nil || response.Text() != "commentary detail draft answer" || response.ReasoningText() != "streamed thought" || response.Finish.Reason != tc.finish || (tc.status == "incomplete" && response.Finish.Raw != tc.status) {
				t.Errorf("settled response = %+v, want streamed answer and finish %q", response, tc.finish)
				continue
			}
			if calls := response.ToolCalls(); len(calls) != 1 || calls[0].ID != "call_1" || string(calls[0].Arguments) != `{ "n" : 1 }` {
				t.Errorf("settled tool calls = %+v, want call_1 with original arguments", calls)
			}
			i := 1
			if tc.output == summaryOutput {
				i++
			}
			if th := response.Message.Content[i].Thinking; th == nil || th.ID != "rs_streamed" || th.EncryptedContent != "opaque continuation" || len(th.Summary) != 1 || th.Summary[0] != "settled summary" {
				t.Errorf("settled response omitted the completed encrypted reasoning")
			}
		}
		if tc.status == "completed" && !t.Failed() {
			srv, captured := server(t, 200, responseJSON)
			req := llm.Request{Model: "gpt-5.5", Messages: []llm.Message{llm.User("hi"), recorded.Message, llm.ToolResultNamed("call_1", "lookup", "ok", false)}}
			if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, liveRes(srv, openaiCaps)); err != nil {
				t.Fatal(err)
			}
			items, _ := captured.body["input"].([]any)
			want := []any{
				map[string]any{"type": "reasoning", "id": "rs_streamed", "encrypted_content": "opaque continuation", "summary": []any{map[string]any{"type": "summary_text", "text": "settled summary"}}},
				map[string]any{"type": "message", "role": "assistant", "phase": "commentary", "content": []any{map[string]any{"type": "output_text", "text": "commentary "}, map[string]any{"type": "output_text", "text": "detail "}}},
				map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "draft "}, map[string]any{"type": "output_text", "text": "answer"}}},
				map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{ "n" : 1 }`},
				map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			}
			if len(items) != len(want)+1 || !reflect.DeepEqual(items[1:], want) {
				t.Errorf("replayed input after user = %+v, want %+v", items, want)
			}
		}
	}
}
