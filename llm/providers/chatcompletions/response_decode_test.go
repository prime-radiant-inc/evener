package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
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
	t.Parallel()
	raw := chatResponseRaw(map[string]any{
		"role":              "assistant",
		"reasoning_details": []any{map[string]any{"type": float64(123)}},
	}, "stop")
	if _, err := fromChatCompletionResponse(raw, nil, nil); err == nil {
		t.Fatal("a reasoning_details item with a non-string type must fail the decode")
	}
}

// TestFromChatCompletionResponse_EncryptedDetailsRoundTrip pins the
// encrypted-details walk: opaque reasoning.encrypted items are re-encoded
// verbatim onto the thinking part's EncryptedContent.
func TestFromChatCompletionResponse_EncryptedDetailsRoundTrip(t *testing.T) {
	t.Parallel()
	raw := chatResponseRaw(map[string]any{
		"role":              "assistant",
		"reasoning":         "trace-17",
		"reasoning_details": []any{map[string]any{"type": "reasoning.encrypted", "id": "e1", "data": "d1"}},
	}, "stop")
	resp, err := fromChatCompletionResponse(raw, nil, nil)
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
	caps := registry.Caps{ReplayReasoningText: new(true), ReasoningField: new("reasoning_content")}
	out, err := toChatMessages([]llm.Message{resp.Message}, caps, false)
	if err != nil {
		t.Fatal(err)
	}
	details, _ := out[0]["reasoning_details"].([]map[string]any)
	if out[0]["reasoning_content"] != "trace-17" || out[0]["reasoning"] != nil || len(details) != 1 || details[0]["data"] != "d1" {
		t.Fatalf("next assistant input = %#v", out[0])
	}
}

// TestFromChatCompletionResponse_ToolCallThoughtSignature keeps the Gemini
// continuation token carried by OpenAI-compatible gateways such as shisa.
func TestFromChatCompletionResponse_ToolCallThoughtSignature(t *testing.T) {
	t.Parallel()
	raw := chatResponseRaw(map[string]any{
		"role":    "assistant",
		"content": nil,
		"tool_calls": []any{map[string]any{
			"id":   "call_gemini",
			"type": "function",
			"function": map[string]any{
				"name":      "read_file",
				"arguments": `{"path":"x"}`,
			},
			"extra_content": map[string]any{
				"google": map[string]any{"thought_signature": "sig-1"},
			},
		}},
	}, "tool_calls")
	resp, err := fromChatCompletionResponse(raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 || calls[0].ThoughtSignature != "sig-1" {
		t.Fatalf("tool calls = %+v, want one call with the Gemini signature", calls)
	}
}

// TestCollectEncryptedDetailsProgrammaticFallback pins the fallback for an
// item built without its verbatim raw JSON: the known fields still round-trip.
func TestCollectEncryptedDetailsProgrammaticFallback(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	raw := chatResponseRaw(map[string]any{
		"role": "assistant",
		"reasoning_details": []any{
			map[string]any{"type": "thinking", "thinking": "step one"},
			map[string]any{"type": "thinking", "thinking": "step two"},
		},
	}, "stop")
	resp, err := fromChatCompletionResponse(raw, nil, nil)
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

// TestFromChatCompletionResponse_ReplayReasoningDetailsDelta compares both
// decoders for reasoning_details payloads. Details retain their replay field
// contract and join text pieces with newlines, even when scalar reasoning is
// also present.
func TestFromChatCompletionResponse_ReplayReasoningDetailsDelta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		fields map[string]any
		want   string
	}{
		{
			name: "details win over scalar reasoning and join multiple pieces",
			fields: map[string]any{
				"reasoning_content": "scalar reasoning",
				"reasoning_details": []any{
					map[string]any{"type": "reasoning.text", "text": "step one"},
					map[string]any{"type": "thinking", "thinking": "step two"},
				},
			},
			want: "step one\nstep two",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonStream, err := fromChatCompletionResponse(chatResponseRaw(tt.fields, "stop"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var nonStreamThinking *llm.ThinkingData
			for _, p := range nonStream.Message.Content {
				if p.Kind == llm.ContentThinking {
					nonStreamThinking = p.Thinking
				}
			}

			delta, err := json.Marshal(tt.fields)
			if err != nil {
				t.Fatal(err)
			}
			body := "data: {\"choices\":[{\"index\":0,\"delta\":" + string(delta) + "}]}\n\n" + "data: [DONE]\n\n"
			stream, err := ExtractRecordedResponse([]byte(body), "m")
			if err != nil {
				t.Fatal(err)
			}
			var streamThinking *llm.ThinkingData
			for _, p := range stream.Message.Content {
				if p.Kind == llm.ContentThinking {
					streamThinking = p.Thinking
				}
			}
			if nonStreamThinking == nil || streamThinking == nil {
				t.Fatalf("non-stream thinking = %+v, stream thinking = %+v", nonStreamThinking, streamThinking)
			}
			if nonStreamThinking.Text != streamThinking.Text {
				t.Errorf("decoder reasoning differs: non-stream %q, stream %q", nonStreamThinking.Text, streamThinking.Text)
			}
			if nonStreamThinking.Text != tt.want {
				t.Errorf("reasoning = %q, want %q", nonStreamThinking.Text, tt.want)
			}
			if nonStreamThinking.Signature != "" || streamThinking.Signature != "" {
				t.Errorf("details replay signatures = %q and %q, want both empty", nonStreamThinking.Signature, streamThinking.Signature)
			}
		})
	}
}

// TestFromChatCompletionResponse_ShapeFailures pins the decode's own error
// arms: a choices field of the wrong kind fails the parse, an empty choices
// list is "no choices", and an unmarshalable raw value surfaces the marshal
// error instead of panicking.
func TestFromChatCompletionResponse_ShapeFailures(t *testing.T) {
	t.Parallel()
	if _, err := fromChatCompletionResponse(map[string]any{"choices": float64(5)}, nil, nil); err == nil {
		t.Fatal("a non-array choices field must fail the decode")
	}
	if _, err := fromChatCompletionResponse(map[string]any{"choices": []any{}}, nil, nil); err == nil {
		t.Fatal("an empty choices list must fail with no choices")
	}
	if _, err := fromChatCompletionResponse(map[string]any{"bad": make(chan int)}, nil, nil); err == nil {
		t.Fatal("an unmarshalable raw value must surface the marshal error")
	}
}

// TestFromChatCompletionResponse_FinishReasonMap pins that a row's
// FinishReasonMap wins over the raw wire value while keeping it as Raw.
func TestFromChatCompletionResponse_FinishReasonMap(t *testing.T) {
	t.Parallel()
	resp, err := fromChatCompletionResponse(chatResponseRaw(map[string]any{"role": "assistant", "content": "hi"}, "stop"), map[string]string{"stop": "end_turn"}, nil)
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
	t.Parallel()
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
	resp, err := fromChatCompletionResponse(raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 3 || resp.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %+v, want the per-choice numbers", resp.Usage)
	}
}
