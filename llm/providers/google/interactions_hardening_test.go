package google

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// interactionSteps builds the response "steps" array from step maps.
func interactionSteps(steps ...map[string]any) []any {
	out := make([]any, len(steps))
	for i, s := range steps {
		out[i] = s
	}
	return out
}

func interactionResponse(id, status string, steps ...map[string]any) map[string]any {
	return map[string]any{"id": id, "status": status, "steps": interactionSteps(steps...)}
}

// Every status the Interactions API emits settles cleanly: the six that finish a
// foreground call map to a FinishReason, the two pending ones report
// ErrPendingInteractionStatus, and only a status Google does not emit lands in
// the unexpected-status bucket.
func TestInteractionStatusMappingCoversEveryEmittedStatus(t *testing.T) {
	t.Parallel()
	textStep := map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": "hi"}}}

	for _, tc := range []struct {
		status string
		want   llm.FinishReason
	}{
		{status: "completed", want: llm.FinishReason{Reason: llm.FinishReasonStop, Raw: "completed"}},
		{status: "requires_action", want: llm.FinishReason{Reason: llm.FinishReasonToolCalls, Raw: "requires_action"}},
		{status: "incomplete", want: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "incomplete"}},
		{status: "budget_exceeded", want: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "budget_exceeded"}},
		{status: "requires_continuation", want: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "requires_continuation"}},
		{status: "cancelled", want: llm.FinishReason{Reason: llm.FinishReasonOther, Raw: "cancelled"}},
	} {
		resp, err := fromInteraction(interactionResponse("int_"+tc.status, tc.status, textStep), "gemini-x", nil, "gemini-prod", "gemini-x")
		if err != nil {
			t.Errorf("status %q: fromInteraction error = %v, want clean settlement", tc.status, err)
			continue
		}
		if !reflect.DeepEqual(resp.Finish, tc.want) {
			t.Errorf("status %q: Finish = %+v, want %+v", tc.status, resp.Finish, tc.want)
		}
	}

	for _, status := range []string{"queued", "in_progress"} {
		_, err := fromInteraction(interactionResponse("int_"+status, status, textStep), "gemini-x", nil, "gemini-prod", "gemini-x")
		if !errors.Is(err, ErrPendingInteractionStatus) {
			t.Errorf("status %q: error = %v, want ErrPendingInteractionStatus", status, err)
		}
	}

	if _, err := fromInteraction(interactionResponse("int_failed", "failed", textStep), "gemini-x", nil, "gemini-prod", "gemini-x"); err == nil {
		t.Error(`status "failed": fromInteraction error = nil, want the provider failure surfaced`)
	} else if errors.Is(err, ErrUnexpectedInteractionStatus) {
		t.Errorf(`status "failed": error = %v, want a provider failure rather than an unexpected status`, err)
	}

	if _, err := fromInteraction(interactionResponse("int_odd", "teleported", textStep), "gemini-x", nil, "gemini-prod", "gemini-x"); !errors.Is(err, ErrUnexpectedInteractionStatus) {
		t.Errorf("unknown status: error = %v, want ErrUnexpectedInteractionStatus", err)
	}
}

// A thought step whose text is empty still carries the signature the next
// request must send back, so it survives the response and round-trips into the
// outbound thought step with no summary invented for it.
func TestInteractionEmptyTextThoughtSignatureRoundTrip(t *testing.T) {
	t.Parallel()
	raw := interactionResponse("int_thought", "requires_action",
		map[string]any{"type": "thought", "signature": "sig_empty"},
		map[string]any{"type": "function_call", "id": "call_1", "name": "read_file", "arguments": map[string]any{"path": "a.go"}, "signature": "sig_empty"},
	)
	resp, err := fromInteraction(raw, "gemini-x", nil, "gemini-prod", "gemini-x")
	if err != nil {
		t.Fatalf("fromInteraction: %v", err)
	}
	if len(resp.Message.Content) < 2 || resp.Message.Content[0].Kind != llm.ContentThinking || resp.Message.Content[0].Thinking == nil {
		t.Fatalf("content = %+v, want a leading thought part", resp.Message.Content)
	}
	if thought := resp.Message.Content[0].Thinking; thought.Text != "" || thought.Signature != "sig_empty" {
		t.Fatalf("thought = %+v, want an empty text with sig_empty", thought)
	}

	body := protoBuild(t, llm.Request{Model: "gemini-x", Messages: []llm.Message{resp.Message}}, protoRes(nil))
	steps, _ := body["input"].([]any)
	if len(steps) != 2 {
		t.Fatalf("outbound steps = %s, want thought + function_call", hardeningJSON(t, steps))
	}
	thought, _ := steps[0].(map[string]any)
	if thought["type"] != "thought" || thought["signature"] != "sig_empty" {
		t.Fatalf("outbound thought step = %s, want the empty-text signature kept", hardeningJSON(t, thought))
	}
	if _, hasSummary := thought["summary"]; hasSummary {
		t.Fatalf("outbound thought step = %s, want no summary invented for empty text", hardeningJSON(t, thought))
	}
	call, _ := steps[1].(map[string]any)
	if call["type"] != "function_call" || call["signature"] != "sig_empty" {
		t.Fatalf("outbound function_call = %s, want the signature kept", hardeningJSON(t, call))
	}
}

// An unknown SSE event is forward-compatibility surface, not a failure: it is
// published as a passthrough provider event and the stream still settles.
func TestInteractionUnknownSSEEventPassesThrough(t *testing.T) {
	t.Parallel()
	sse := buildSSE(
		[2]string{"interaction.created", `{"event_type":"interaction.created","interaction":{"id":"int_unknown","status":"in_progress"}}`},
		[2]string{"step.revision", `{"event_type":"step.revision","index":0,"revision":{"kind":"future"}}`},
		[2]string{"", `{"event_type":"interaction.completed","interaction":{"id":"int_unknown","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}]}}`},
		[2]string{"", `[DONE]`},
	)
	resp, events, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-x", false)
	if err != nil {
		t.Fatalf("unknown event failed the stream: %v", err)
	}
	if resp.Text() != "hello" {
		t.Fatalf("response text = %q, want hello", resp.Text())
	}
	var passedThrough bool
	for _, ev := range events {
		if ev.Type != llm.StreamEventProviderEvent || ev.Raw == nil {
			continue
		}
		if raw, ok := ev.Raw["revision"].(map[string]any); ok && raw["kind"] == "future" {
			passedThrough = true
		}
	}
	if !passedThrough {
		t.Fatalf("unknown event was not passed through: %+v", events)
	}
}

// A single SSE event larger than the limit is rejected rather than buffered
// without bound, and the rejection names the limit.
func TestInteractionOversizedSSEEventFailsTheStream(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", maxInteractionStreamEventBytes)
	sse := "event: step.revision\ndata: " + huge + "\n\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_huge","status":"completed"}}` + "\n\n"
	_, _, err := runInteractionsSSETest(t, sse, "gemini-prod", "gemini-x", false)
	if !errors.Is(err, llm.ErrSSEEventTooLarge) {
		t.Fatalf("oversized event error = %v, want ErrSSEEventTooLarge", err)
	}
	if !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("oversized event error = %v, want it to name the byte limit", err)
	}
}

func hardeningJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
