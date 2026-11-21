package google

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func TestInteractionsStreamSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	t.Parallel()
	sse := "event: step.start\n" +
		`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","name":"lookup","arguments":{"q":"x"}}}` + "\n\n" +
		"event: step.stop\n" +
		`data: {"event_type":"step.stop","index":0}` + "\n\n" +
		"event: interaction.completed\n" +
		`data: {"event_type":"interaction.completed","interaction":{"id":"int_id","status":"requires_action"}}` + "\n\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(sse))}
	s := llm.NewChanStream(cancel)
	go decodeInteractionStream(ctx, cancel, resp, s, llm.Request{Model: "test"}, "gemini-test", "test", "http://test", llm.APILogCredentialMaterial{}, nil, false, nil)
	var got string
	for ev := range s.Events() {
		if ev.ToolCall != nil {
			got = ev.ToolCall.ID
		}
	}
	if err := identifier.ValidateSyntheticCallID(got); err != nil {
		t.Fatalf("stream synthetic ID %q: %v", got, err)
	}
}

func TestInteractionsSyntheticToolCallIDUsesIdentifierDomain(t *testing.T) {
	t.Parallel()
	r, err := fromInteraction(map[string]any{
		"id":     "int_syn",
		"status": "requires_action",
		"steps": []any{map[string]any{
			"type":      "function_call",
			"name":      "lookup",
			"arguments": map[string]any{"q": "x"},
		}},
	}, "test", nil, "google", "test")
	if err != nil {
		t.Fatal(err)
	}
	calls := r.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if err := identifier.ValidateSyntheticCallID(calls[0].ID); err != nil {
		t.Fatalf("synthetic ID %q: %v", calls[0].ID, err)
	}
}
