package responses

import (
	"context"
	"encoding/json"
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
	var out []any
	if err := json.Unmarshal([]byte(`[
		{"id":"rs_1","type":"reasoning","content":[{"type":"reasoning_text","text":"Let me think."},{"type":"reasoning_text","text":"Then verify."}],"summary":[]},
		{"type":"message","content":[{"type":"output_text","text":"Answer"}]}
	]`), &out); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	content := responseContentFromOutputItems(out)
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
