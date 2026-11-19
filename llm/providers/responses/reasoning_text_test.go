package responses

import (
	"context"
	"encoding/json"
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

func TestStreamRecoversTextWhenTerminalOutputIsEmpty(t *testing.T) {
	t.Parallel()
	const delta = `{"type":"response.output_text.delta","delta":"answer"}`
	const call = `{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"probe","arguments":"{ \"n\" : 1 }"}}`
	const empty = `{"type":"response.completed","response":{"output":[]}}`
	const incomplete = `{"type":"response.incomplete","response":{"status":"incomplete","output":[]}}`
	const added = `{"type":"response.output_item.added","output_index":4,"item":{"type":"message","id":"msg_4","phase":"commentary"}}`
	const keyedDelta = `{"type":"response.output_text.delta","output_index":4,"delta":"answer"}`
	for _, tc := range []struct {
		name, text, phase string
		calls             int
		frames            []string
	}{
		{"empty-complete", "answer", "", 1, []string{delta, call, empty}},
		{"empty-incomplete", "answer", "", 1, []string{delta, call, incomplete}},
		{"phase-only-terminal", "answer", "commentary", 1, []string{delta, call, `{"type":"response.completed","response":{"output":[{"type":"message","phase":"commentary","content":[{"type":"output_text","text":""}]}]}}`}},
		{"added-commentary", "answer", "commentary", 0, []string{added, keyedDelta, incomplete}},
		{"completed-commentary", "answer", "commentary", 0, []string{added, keyedDelta,
			`{"type":"response.output_item.done","item":{"type":"message","id":"msg_4","content":[{"type":"output_text","text":"answer"}]}}`, empty,
		}},
		{"completion-phase-wins", "answer", "final_answer", 0, []string{added, keyedDelta,
			`{"type":"response.output_item.done","item":{"type":"message","id":"msg_4","phase":"final_answer","content":[{"type":"output_text","text":"answer"}]}}`, empty,
		}},
		{"late-item-identity", "answer", "", 0, []string{delta,
			`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"answer"}]}}`, empty,
		}},
		{"interleaved-indexes", "earlier later", "", 0, []string{
			`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_0","delta":"earlier "}`,
			`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","delta":"later"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_0","content":[{"type":"output_text","text":"earlier "}]}}`, empty,
		}},
		{"usable-terminal-wins", "other", "", 0, []string{delta, call,
			`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"other"}]}]}}`,
		}},
		{"unfinished-terminal-wins", "", "", 0, []string{delta, call, `{"type":"response.incomplete","response":{"status":"incomplete","output":[{"type":"function_call","id":"fc_1","call_id":"call_1","status":"incomplete"}]}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: " + strings.Join(tc.frames, "\n\ndata: ") + "\n\n"
			streamed, _ := responsesEventsFromSSE(t, body)
			recorded, err := ExtractRecordedResponse([]byte(body), "gpt-5.5")
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range []*llm.Response{streamed[len(streamed)-1].Response, &recorded} {
				var phase string
				for _, p := range r.Message.Content {
					if p.Kind == llm.ContentText && p.Text != "" {
						phase = p.Phase
						break
					}
				}
				calls := r.ToolCalls()
				if r.Text() != tc.text || phase != tc.phase || len(calls) != tc.calls {
					t.Errorf("settled text %q phase %q calls %d", r.Text(), phase, len(calls))
				} else if len(calls) == 1 && string(calls[0].Arguments) != `{ "n" : 1 }` {
					t.Errorf("settled arguments = %q", calls[0].Arguments)
				}
			}
		})
	}
}
