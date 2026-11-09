package responses

// Regression tests for #4941 at the built-in decoder: the Responses adapter
// classifies the passthrough frames it knows carry model output, and leaves
// lifecycle/control frames unclassified so an empty stream can still retry.

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// providerEventsWithType returns every passthrough event whose payload names
// the given Responses event type.
func providerEventsWithType(events []llm.StreamEvent, typ string) []llm.StreamEvent {
	var out []llm.StreamEvent
	for _, ev := range events {
		if ev.Type != llm.StreamEventProviderEvent {
			continue
		}
		if got, _ := ev.Raw["type"].(string); got == typ {
			out = append(out, ev)
		}
	}
	return out
}

// TestResponsesStream_RefusalDeltaIsContentBearing pins the reported frame:
// response.refusal.delta falls through the decoder as a passthrough and must
// carry its content classification, because the caller renders it.
func TestResponsesStream_RefusalDeltaIsContentBearing(t *testing.T) {
	events, _ := responsesEvents(t,
		`{"type":"response.created","response":{"id":"resp_commit","status":"in_progress"}}`,
		`{"type":"response.refusal.delta","delta":"I cannot help with that."}`,
		`{"type":"response.completed","response":{"id":"resp_commit","status":"completed","output":[]}}`,
	)

	refusals := providerEventsWithType(events, "response.refusal.delta")
	if len(refusals) != 1 {
		t.Fatalf("response.refusal.delta passthroughs = %d, want 1", len(refusals))
	}
	if !refusals[0].ContentBearing {
		t.Fatal("response.refusal.delta passthrough is not marked content-bearing; a retry after it can duplicate the refusal")
	}
}

// TestResponsesStream_ControlEventsAreNotContentBearing covers the other half
// of the boundary: a lifecycle-only stream must keep its retry recovery.
func TestResponsesStream_ControlEventsAreNotContentBearing(t *testing.T) {
	events, _ := responsesEvents(t,
		`{"type":"response.created","response":{"id":"resp_ctrl","status":"in_progress"}}`,
		`{"type":"response.in_progress","response":{"id":"resp_ctrl","status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"resp_ctrl","status":"completed","output":[]}}`,
	)

	for _, ev := range events {
		if ev.Type == llm.StreamEventProviderEvent && ev.ContentBearing {
			t.Fatalf("control passthrough %v marked content-bearing; an empty stream must still retry", ev.Raw["type"])
		}
	}
}

// TestResponsesStream_RefusalDoneIsContentBearing pins the .done sibling: a
// gateway that sends the refusal in one frame still commits the attempt.
func TestResponsesStream_RefusalDoneIsContentBearing(t *testing.T) {
	events, _ := responsesEvents(t,
		`{"type":"response.refusal.delta","delta":"I cannot help with that."}`,
		`{"type":"response.refusal.done","refusal":"I cannot help with that."}`,
		`{"type":"response.completed","response":{"id":"resp_done","status":"completed","output":[]}}`,
	)
	dones := providerEventsWithType(events, "response.refusal.done")
	if len(dones) != 1 {
		t.Fatalf("response.refusal.done passthroughs = %d, want 1", len(dones))
	}
	if !dones[0].ContentBearing {
		t.Fatal("response.refusal.done passthrough is not marked content-bearing")
	}
}

// TestResponsesStream_UnmappableToolCallFragmentIsContentBearing pins the
// adapter's explicit mark on a tool-call fragment the accumulator cannot map:
// replaying its arguments would duplicate them.
func TestResponsesStream_UnmappableToolCallFragmentIsContentBearing(t *testing.T) {
	events, _ := responsesEvents(t,
		`{"type":"response.function_call_arguments.delta","delta":"{\"x\":"}`,
		`{"type":"response.completed","response":{"id":"resp_frag","status":"completed","output":[]}}`,
	)
	frags := providerEventsWithType(events, "response.function_call_arguments.delta")
	if len(frags) != 1 {
		t.Fatalf("unmappable function-call passthroughs = %d, want 1", len(frags))
	}
	if !frags[0].ContentBearing {
		t.Fatal("unmappable tool-call fragment is not marked content-bearing")
	}
}
