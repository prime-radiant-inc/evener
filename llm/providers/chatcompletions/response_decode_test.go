package chatcompletions

import (
	"encoding/json"
	"strings"
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

// TestReasoningDetailItemProgrammaticFallback pins the fallback for an
// item built without its verbatim raw JSON: the known fields still round-trip.
func TestReasoningDetailItemProgrammaticFallback(t *testing.T) {
	t.Parallel()
	item := reasoningDetailItem{Type: "reasoning.encrypted", ID: "i1", Data: "d1"}.wireItem()
	if item["type"] != "reasoning.encrypted" || item["id"] != "i1" || item["data"] != "d1" {
		t.Fatalf("fallback item = %v, want the known fields", item)
	}
}

// TestFromChatCompletionResponse_ThinkingItemsJoin pins the detail-item
// walk: legacy {type:"thinking", thinking:"..."} items contribute
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

// Detail text drives the display. A route that accepts a scalar receives its
// original field and bytes in the next request, including after persistence.
func TestFromChatCompletionResponse_ReplayReasoningDetailsDelta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scalarField, scalar, display string
		details                            []any
	}{
		{
			name:        "details win over scalar reasoning and join multiple pieces",
			scalarField: "reasoning", scalar: " scalar-17\n\t", display: "step one\nstep two",
			details: []any{
				map[string]any{"type": "reasoning.text", "text": "step one"},
				map[string]any{"type": "thinking", "thinking": "step two"},
			},
		},
		{
			name:        "mirrored unsigned text",
			scalarField: "reasoning_text", scalar: "mirror-29", display: "mirror-29",
			details: []any{map[string]any{"type": "reasoning.text", "text": "mirror-29"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]any{tt.scalarField: tt.scalar, "reasoning_details": tt.details}
			nonStream, err := fromChatCompletionResponse(chatResponseRaw(fields, "stop"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			delta, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			body := "data: {\"choices\":[{\"index\":0,\"delta\":" + string(delta) + "}]}\n\n" + "data: [DONE]\n\n"
			stream, err := ExtractRecordedResponse([]byte(body), "m")
			if err != nil {
				t.Fatal(err)
			}
			events, err := chatStreamEvents(t, body, nil)
			if err != nil {
				t.Fatal(err)
			}
			acc := llm.NewStreamAccumulator()
			var liveDisplay strings.Builder
			for _, ev := range events {
				acc.Process(ev)
				liveDisplay.WriteString(ev.ReasoningDelta)
			}
			if acc.Response() == nil || liveDisplay.String() != tt.display {
				t.Fatalf("live reasoning = %q, settled = %t", liveDisplay.String(), acc.Response() != nil)
			}
			for name, resp := range map[string]*llm.Response{"json": &nonStream, "recorded SSE": &stream, "live SSE": acc.Response()} {
				t.Run(name, func(t *testing.T) {
					parts := resp.Message.Content
					if len(parts) != 1 || parts[0].Kind != llm.ContentThinking || parts[0].Thinking == nil || parts[0].Thinking.Text != tt.display || parts[0].Thinking.Signature != "" {
						t.Fatalf("settled detail display = %#v", parts)
					}
					var saved llm.Message
					if err := json.Unmarshal([]byte(jsonOf(t, resp.Message)), &saved); err != nil {
						t.Fatal(err)
					}
					if source := saved.Content[0].Thinking.ChatScalar; tt.scalar == tt.display && source != nil && source.Text != "" {
						t.Fatal("persisted scalar duplicates its display text")
					}
					req := llm.Request{Model: "m", Messages: []llm.Message{saved}}
					for _, route := range []struct {
						caps        registry.Caps
						field, want string
					}{
						{registry.Caps{ReplayReasoningText: new(true)}, tt.scalarField, tt.scalar},
						{registry.Caps{ReplayReasoningText: new(true), ReasoningField: new("reasoning_content")}, "reasoning_content", tt.scalar},
						{registry.Caps{ReplayReasoningText: new(true), ReasoningField: new("reasoning_details")}, "reasoning_details", tt.display},
						{registry.Caps{}, "reasoning_content", tt.display},
						{registry.Caps{Reasoning: new(false), ReplayReasoningText: new(true)}, "content", ""},
						{registry.Caps{ReplayReasoningText: new(true), ThinkingAsText: new(true)}, "content", tt.display},
					} {
						res := resolved(nil)
						res.Caps = route.caps
						msg := build(t, req, res)["messages"].([]map[string]any)[0]
						if route.field == "reasoning_details" {
							details, _ := msg[route.field].([]map[string]any)
							if len(details) != 1 || details[0]["text"] != route.want {
								t.Errorf("unified replay = %#v", msg)
							}
						} else if msg[route.field] != route.want || msg["reasoning_details"] != nil {
							t.Errorf("%s replay = %#v, want %q", route.field, msg, route.want)
						}
						for _, field := range llm.OpenAICompatReasoningFields() {
							if field != route.field && msg[field] != nil {
								t.Errorf("%s replay also includes %s", route.field, field)
							}
						}
					}
				})
			}
		})
	}
	t.Run("unsigned and signed details alongside a scalar", func(t *testing.T) {
		for _, scalar := range []string{"scalar-31", "unsigned-54", "signed-68"} {
			resp, err := fromChatCompletionResponse(chatResponseRaw(map[string]any{
				"reasoning": scalar,
				"reasoning_details": []any{
					map[string]any{"type": "reasoning.text", "text": "unsigned-54", "index": 0},
					map[string]any{"type": "reasoning.text", "text": "signed-68", "index": 1, "signature": "sig-68"},
				},
			}, "stop"), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{ReplayReasoningText: new(true)}, false)
			if err != nil {
				t.Fatal(err)
			}
			details, _ := out[0]["reasoning_details"].([]map[string]any)
			if len(details) != 1 || details[0]["text"] != "signed-68" || details[0]["signature"] != "sig-68" {
				t.Fatalf("continuation = %#v", details)
			}
			want := scalar
			if scalar == "signed-68" {
				want = "" // The signed continuation already carries this scalar.
			}
			if got, _ := out[0]["reasoning"].(string); got != want || out[0]["reasoning_content"] != nil {
				t.Errorf("scalar %q replays as %#v", scalar, out[0])
			}
		}
	})
	for _, tc := range []struct {
		name, scalar, detail, wantScalar string
		streamPrefix                     int
	}{
		{"independent signed text", "scalar-12", "signed-37", "scalar-12", 0},
		{"signature without detail text", "scalar-12", "", "scalar-12", 0},
		{"trailing detail signature", "scalar-12", "signed-37", "scalar-12", len("signed-37")},
		{"signature with final text fragment", "scalar-12", "signed-37", "scalar-12", len("signed-")},
		{"mirrored signed text", "signed-37", "signed-37", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed := map[string]any{"type": "reasoning.text", "signature": "sig-37", "format": "wire-1", "index": 0}
			if tc.detail != "" {
				signed["text"] = tc.detail
			}
			fields := map[string]any{"reasoning": tc.scalar, "reasoning_text": tc.scalar, "reasoning_details": []any{signed}}
			body, err := json.Marshal(chatResponseRaw(fields, "stop"))
			if err != nil {
				t.Fatal(err)
			}
			chunks := []map[string]any{{"reasoning": tc.scalar, "reasoning_text": tc.scalar}}
			if tc.streamPrefix != 0 {
				chunks = append(chunks, map[string]any{"reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": tc.detail[:tc.streamPrefix], "index": 0}}})
				signed["text"] = tc.detail[tc.streamPrefix:]
			}
			chunks = append(chunks, map[string]any{"reasoning_details": []any{signed}})
			var sse strings.Builder
			for _, chunk := range chunks {
				data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": chunk}}})
				if err != nil {
					t.Fatal(err)
				}
				sse.WriteString("data: " + string(data) + "\n\n")
			}
			sse.WriteString("data: [DONE]\n\n")
			for mode, recorded := range map[string][]byte{"json": body, "stream": []byte(sse.String())} {
				t.Run(mode, func(t *testing.T) {
					resp, err := ExtractRecordedResponse(recorded, "m")
					if err != nil {
						t.Fatal(err)
					}
					if got, want := resp.ReasoningText(), strings.Trim(tc.wantScalar+"\n\n"+tc.detail, "\n"); got != want {
						t.Errorf("settled reasoning = %q, want %q", got, want)
					}
					if tc.wantScalar != "" {
						var source string
						for _, part := range resp.Message.Content {
							if part.Thinking != nil && part.Thinking.Text == tc.wantScalar && part.Thinking.EncryptedContent == "" {
								source = part.Thinking.Signature
							}
						}
						if source != "reasoning" {
							t.Errorf("independent scalar source = %q", source)
						}
					}
					for _, field := range []string{"reasoning_text", "reasoning_details"} {
						caps := registry.Caps{ReplayReasoningText: new(true), ReasoningField: &field}
						out, err := toChatMessages([]llm.Message{resp.Message}, caps, field == "reasoning_details")
						if err != nil {
							t.Fatal(err)
						}
						msg := out[0]
						details, _ := msg["reasoning_details"].([]map[string]any)
						if field == "reasoning_details" && tc.wantScalar != "" {
							if len(details) != 2 || details[0]["text"] != tc.wantScalar || details[0]["signature"] != nil {
								t.Errorf("unified independent text = %#v", msg)
								continue
							}
							echo, err := fromChatCompletionResponse(chatResponseRaw(msg, "stop"), nil, nil)
							if err != nil {
								t.Fatal(err)
							}
							if got, n := echo.ReasoningText(), len(echo.Message.Content); got != resp.ReasoningText() || n != len(resp.Message.Content) {
								t.Errorf("unified details decode to %q in %d parts, want %q in %d", got, n, resp.ReasoningText(), len(resp.Message.Content))
							}
							details = details[1:]
						} else if got, _ := msg["reasoning_text"].(string); got != tc.wantScalar {
							t.Errorf("scalar = %q, want %q", got, tc.wantScalar)
						}
						if len(details) != 1 || details[0]["signature"] != "sig-37" || details[0]["format"] != "wire-1" {
							t.Fatalf("signed detail = %#v", details)
						}
						if got, _ := details[0]["text"].(string); got != tc.detail {
							t.Errorf("%s signed text = %q, want %q", field, got, tc.detail)
						}
					}
				})
			}
		})
	}
	t.Run("interleaved detail indexes", func(t *testing.T) {
		sse := `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":2,"text":"first-"},{"type":"reasoning.text","index":7,"text":"second-"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"e3","data":"opaque-3"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":7,"signature":"sig-7","text":"7","metadata":"meta-7"},{"type":"reasoning.text","index":2,"signature":"sig-2","text":"2"}]}}]}

data: [DONE]

`
		resp, err := ExtractRecordedResponse([]byte(sse), "m")
		if err != nil {
			t.Fatal(err)
		}
		out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{ReplayReasoningText: new(true)}, true)
		if err != nil {
			t.Fatal(err)
		}
		details, _ := out[0]["reasoning_details"].([]map[string]any)
		if len(details) != 3 || details[0]["data"] != "opaque-3" ||
			details[1]["signature"] != "sig-7" || details[1]["text"] != "second-7" || details[1]["metadata"] != "meta-7" ||
			details[2]["signature"] != "sig-2" || details[2]["text"] != "first-2" {
			t.Fatalf("interleaved continuations = %#v", details)
		}
	})
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
