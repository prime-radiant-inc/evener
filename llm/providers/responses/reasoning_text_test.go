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
// settled text reads like the live view.
func TestResponseContentFromOutputItems_KeepsReasoningTextContent(t *testing.T) {
	t.Parallel()
	var out []any
	if err := json.Unmarshal([]byte(`[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me think."},{"type":"reasoning_text","text":"Then verify."}],"summary":[]},
		{"type":"message","content":[{"type":"output_text","text":"Answer"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out, nil, false)
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

func TestResponsesIncompleteReasoningDoesNotReplay(t *testing.T) {
	t.Parallel()
	out := []any{
		map[string]any{"type": "reasoning", "id": "rs_done", "status": "completed", "content": []any{map[string]any{"type": "reasoning_text", "text": "done"}}},
		map[string]any{"type": "reasoning", "id": "rs_open", "content": []any{map[string]any{"type": "reasoning_text", "text": "half"}}},
		map[string]any{"type": "reasoning", "id": "rs_blob", "encrypted_content": "opaque"},
	}
	msg := fromResponses(map[string]any{"id": "r", "status": "incomplete", "output": out}, "m", nil).Message
	_, items, _ := toResponsesInput([]llm.Message{msg}, "", nil)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "rs_done" {
		t.Fatalf("replayed items = %s, want only the completed reasoning item", jsonOf(t, items))
	}
}
