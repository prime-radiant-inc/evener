package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

// chatResponseRaw builds the map fromChatCompletionResponse expects for one
// assistant message carrying the given message-level fields.
func chatResponseRaw(message map[string]any, finish string) map[string]any {
	return map[string]any{
		"id":    "resp_1",
		"model": "m-wire",
		"choices": []any{map[string]any{
			"index":         float64(0),
			"finish_reason": finish,
			"message":       message,
		}},
	}
}

// TestFromChatCompletionResponse_DetailItemDecodeError pins the reasoning
// detail item's own unmarshal error surfacing as a decode failure: a detail
// whose typed field holds the wrong JSON kind must fail the response, not be
// silently dropped.
func TestFromChatCompletionResponse_DetailItemDecodeError(t *testing.T) {
	raw := chatResponseRaw(map[string]any{
		"role":              "assistant",
		"reasoning_details": []any{map[string]any{"type": float64(123)}},
	}, "stop")
	if _, err := fromChatCompletionResponse(raw, nil); err == nil {
		t.Fatal("a reasoning_details item with a non-string type must fail the decode")
	}
}

// TestFromChatCompletionResponse_EncryptedDetailsRoundTrip pins the
// encrypted-details walk: opaque reasoning.encrypted items are re-encoded
// verbatim onto the thinking part's EncryptedContent.
func TestFromChatCompletionResponse_EncryptedDetailsRoundTrip(t *testing.T) {
	raw := chatResponseRaw(map[string]any{
		"role":              "assistant",
		"reasoning_details": []any{map[string]any{"type": "reasoning.encrypted", "id": "e1", "data": "d1"}},
	}, "stop")
	resp, err := fromChatCompletionResponse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var thinking *llm.ThinkingData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking {
			thinking = p.Thinking
		}
	}
	if thinking == nil {
		t.Fatalf("content = %+v, want a thinking part carrying the encrypted items", resp.Message.Content)
	}
	var want []map[string]any
	if err := json.Unmarshal([]byte(thinking.EncryptedContent), &want); err != nil || len(want) != 1 {
		t.Fatalf("EncryptedContent = %q (%v), want the one verbatim item", thinking.EncryptedContent, err)
	}
	if want[0]["type"] != "reasoning.encrypted" || want[0]["id"] != "e1" || want[0]["data"] != "d1" {
		t.Fatalf("encrypted item = %v, want every original field preserved", want[0])
	}
}

// TestCollectEncryptedDetailsProgrammaticFallback pins the fallback for an
// item built without its verbatim raw JSON: the known fields still round-trip.
func TestCollectEncryptedDetailsProgrammaticFallback(t *testing.T) {
	enc := collectEncryptedDetails([]reasoningDetailItem{
		{Type: "reasoning.encrypted", ID: "i1", Data: "d1"},
	})
	if len(enc) != 1 {
		t.Fatalf("collected = %+v, want the one continuation-bearing item", enc)
	}
	if enc[0]["type"] != "reasoning.encrypted" || enc[0]["id"] != "i1" || enc[0]["data"] != "d1" {
		t.Fatalf("fallback item = %v, want the known fields", enc[0])
	}
}

// TestFromChatCompletionResponse_ThinkingItemsJoin pins extractReasoning's
// detail-item walk: legacy {type:"thinking", thinking:"..."} items contribute
// their text, joined with a newline when there are several, and report no
// replay field.
func TestFromChatCompletionResponse_ThinkingItemsJoin(t *testing.T) {
	raw := chatResponseRaw(map[string]any{
		"role": "assistant",
		"reasoning_details": []any{
			map[string]any{"type": "thinking", "thinking": "step one"},
			map[string]any{"type": "thinking", "thinking": "step two"},
		},
	}, "stop")
	resp, err := fromChatCompletionResponse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var thinking *llm.ThinkingData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking {
			thinking = p.Thinking
		}
	}
	if thinking == nil {
		t.Fatalf("content = %+v, want a thinking part", resp.Message.Content)
	}
	if thinking.Text != "step one\nstep two" {
		t.Fatalf("thinking text = %q, want the items joined by a newline", thinking.Text)
	}
	if thinking.Signature != "" {
		t.Fatalf("signature = %q, want no replay field for detail items", thinking.Signature)
	}
}

// TestFromChatCompletionResponse_ReplayReasoningDetailsDelta pins the
// streaming decoder's reasoning_details accumulation through the recorded
// replay: text items and thinking-fallback items concatenate with no
// separator (matching the live delta stream), and the settled thinking part
// carries the joined text.
func TestFromChatCompletionResponse_ReplayReasoningDetailsDelta(t *testing.T) {
	body := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[" +
		`{"type":"reasoning.text","text":"th"},{"type":"thinking","thinking":"tk"}]}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	resp, err := ExtractRecordedResponse([]byte(body), "m")
	if err != nil {
		t.Fatal(err)
	}
	var thinking *llm.ThinkingData
	for _, p := range resp.Message.Content {
		if p.Kind == llm.ContentThinking {
			thinking = p.Thinking
		}
	}
	if thinking == nil || thinking.Text != "thtk" {
		t.Fatalf("thinking = %+v, want the concatenated delta text %q", thinking, "thtk")
	}
}

// TestFromChatCompletionResponse_ShapeFailures pins the decode's own error
// arms: a choices field of the wrong kind fails the parse, an empty choices
// list is "no choices", and an unmarshalable raw value surfaces the marshal
// error instead of panicking.
func TestFromChatCompletionResponse_ShapeFailures(t *testing.T) {
	if _, err := fromChatCompletionResponse(map[string]any{"choices": float64(5)}, nil); err == nil {
		t.Fatal("a non-array choices field must fail the decode")
	}
	if _, err := fromChatCompletionResponse(map[string]any{"choices": []any{}}, nil); err == nil {
		t.Fatal("an empty choices list must fail with no choices")
	}
	if _, err := fromChatCompletionResponse(map[string]any{"bad": make(chan int)}, nil); err == nil {
		t.Fatal("an unmarshalable raw value must surface the marshal error")
	}
}

// TestFromChatCompletionResponse_FinishReasonMap pins that a row's
// FinishReasonMap wins over the raw wire value while keeping it as Raw.
func TestFromChatCompletionResponse_FinishReasonMap(t *testing.T) {
	resp, err := fromChatCompletionResponse(chatResponseRaw(map[string]any{"role": "assistant", "content": "hi"}, "stop"), map[string]string{"stop": "end_turn"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Finish.Reason != "end_turn" || resp.Finish.Raw != "stop" {
		t.Fatalf("finish = %+v, want the mapped reason with the raw value kept", resp.Finish)
	}
}

// TestFromChatCompletionResponse_ChoiceLevelUsage pins the Moonshot/Kimi
// fallback: with no top-level usage, choices[0].usage becomes the response
// usage.
func TestFromChatCompletionResponse_ChoiceLevelUsage(t *testing.T) {
	raw := map[string]any{
		"id":    "resp_u",
		"model": "m-wire",
		"choices": []any{map[string]any{
			"index":         float64(0),
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": "hi"},
			"usage":         map[string]any{"prompt_tokens": float64(7), "completion_tokens": float64(3), "total_tokens": float64(10)},
		}},
	}
	resp, err := fromChatCompletionResponse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v, want the per-choice numbers", resp.Usage)
	}
}
