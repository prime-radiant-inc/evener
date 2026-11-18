package chatcompletions

import (
	"encoding/json"
	"slices"
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
	decode := func(t *testing.T, body, sse string) (map[string]*llm.Response, string) {
		t.Helper()
		responses := map[string]*llm.Response{}
		for mode, recording := range map[string]string{"json": body, "recorded SSE": sse} {
			resp, err := ExtractRecordedResponse([]byte(recording), "m")
			if err != nil {
				t.Fatalf("%s: %v", mode, err)
			}
			responses[mode] = &resp
		}
		events, err := chatStreamEvents(t, sse, nil)
		if err != nil {
			t.Fatal(err)
		}
		acc := llm.NewStreamAccumulator()
		var display strings.Builder
		for _, ev := range events {
			acc.Process(ev)
			display.WriteString(ev.ReasoningDelta)
		}
		if acc.Response() == nil {
			t.Fatal("missing settled stream response")
		}
		responses["live SSE"] = acc.Response()
		return responses, display.String()
	}
	checkScalar := func(t *testing.T, msg map[string]any, field, value string) {
		t.Helper()
		for _, key := range llm.OpenAICompatReasoningFields() {
			got, ok := msg[key]
			if key == field && value != "" {
				if got != value {
					t.Errorf("%s = %#v, want %q", key, got, value)
				}
			} else if ok {
				t.Errorf("unexpected %s in replay %#v", key, msg)
			}
		}
	}
	tests := []struct {
		name, scalarField, scalar, display string
		details                            []any
	}{
		{
			name:        "details win over scalar reasoning and join multiple pieces",
			scalarField: "reasoning", scalar: " scalar-17\n\t", display: "step one\nstep two",
			details: []any{
				map[string]any{"type": "reasoning.text", "text": "step one", "index": 7, "format": "fmt-a", "id": "a"},
				map[string]any{"type": "thinking", "thinking": "step two", "index": 2, "format": "fmt-b", "metadata": map[string]any{"serial": json.Number("9007199254740993")}},
			},
		},
		{
			name:        "mirrored unsigned text",
			scalarField: "reasoning_text", scalar: "mirror-29", display: "mirror-29",
			details: []any{map[string]any{"type": "reasoning.text", "text": "mirror-29"}},
		},
		{
			name: "explicit index overlaps a sibling's array position", scalarField: "reasoning", scalar: "scalar", display: "left\nright",
			details: []any{
				map[string]any{"type": "reasoning.text", "index": 1, "text": "left"},
				map[string]any{"type": "reasoning.text", "text": "right"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := map[string]any{tt.scalarField: tt.scalar, "reasoning_details": tt.details}
			sse := "data: {\"choices\":[{\"index\":0,\"delta\":" + jsonOf(t, fields) + "}]}\n\n" + "data: [DONE]\n\n"
			responses, display := decode(t, jsonOf(t, chatResponseRaw(fields, "stop")), sse)
			if display != tt.display {
				t.Fatalf("live reasoning = %q, want %q", display, tt.display)
			}
			for name, resp := range responses {
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
							if jsonOf(t, details) != jsonOf(t, tt.details) {
								t.Errorf("native array = %s, want %s", jsonOf(t, details), jsonOf(t, tt.details))
							}
						} else if msg[route.field] != route.want || msg["reasoning_details"] != nil {
							t.Errorf("%s replay = %#v, want %q", route.field, msg, route.want)
						}
						checkScalar(t, msg, route.field, route.want)
					}
				})
			}
		})
	}
	t.Run("array conversion when no native detail array was recorded", func(t *testing.T) {
		res := resolved(nil)
		res.Caps.ReplayReasoningText, res.Caps.ReasoningField = new(true), new("reasoning_details")
		scalarOnly, err := ExtractRecordedResponse([]byte(`{"choices":[{"message":{"reasoning":"scalar-only"}}]}`), "m")
		if err != nil {
			t.Fatal(err)
		}
		const signed = `[{"type":"reasoning.text","text":"signed","signature":"sig","index":0}]`
		old := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "old-scalar", Signature: "reasoning"}},
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "signed", EncryptedContent: signed}},
		}}
		for _, tc := range []struct {
			msg  llm.Message
			want []map[string]any
		}{
			{scalarOnly.Message, []map[string]any{{"type": "reasoning.text", "format": "unknown", "index": 0, "text": "scalar-only"}}},
			{old, []map[string]any{{"type": "reasoning.text", "format": "unknown", "index": 1, "text": "old-scalar"}, {"type": "reasoning.text", "index": 0, "text": "signed", "signature": "sig"}}},
		} {
			msg := build(t, llm.Request{Model: "m", Messages: []llm.Message{tc.msg}}, res)["messages"].([]map[string]any)[0]
			if got := jsonOf(t, msg["reasoning_details"]); got != jsonOf(t, tc.want) {
				t.Errorf("array conversion = %s, want %s", got, jsonOf(t, tc.want))
			}
		}
	})
	t.Run("interleaved unsigned and opaque details", func(t *testing.T) {
		for _, signed := range []bool{false, true} {
			blank := map[string]any{"type": "reasoning.text", "id": "blank", "format": "fmt-empty", "index": 4, "text": "", "metadata": json.Number("9007199254740993")}
			middle := map[string]any{"type": "reasoning.encrypted", "id": "opaque", "data": "sealed"}
			tail := map[string]any{"type": "reasoning.text", "index": 3, "text": "tail", "format": "fmt-end"}
			if signed {
				tail["signature"] = "sig-tail"
			}
			want := []map[string]any{
				{"type": "reasoning.text", "id": "a", "format": "fmt-a", "index": 9, "text": "a-complete"},
				blank, middle, tail, {"type": "reasoning.text", "format": "unindexed", "text": ""},
			}
			complete := append(slices.Clone(want), map[string]any{"type": "reasoning.text", "index": 6, "text": nil})
			body := chatResponseRaw(map[string]any{"reasoning": "independent", "reasoning_details": complete}, "stop")
			chunks := []map[string]any{
				{"reasoning": "independent", "reasoning_details": []any{
					map[string]any{"type": "reasoning.text", "id": "a", "format": "fmt-a", "index": 9, "text": "a-"},
					blank, map[string]any{"type": "reasoning.text", "id": "unfinished", "index": 8, "text": ""},
				}},
				{"reasoning_details": []any{middle, tail}},
				{"reasoning_details": []any{map[string]any{"index": 9, "text": "complete"}}},
			}
			var stream strings.Builder
			for _, chunk := range chunks {
				stream.WriteString("data: " + jsonOf(t, map[string]any{"choices": []any{map[string]any{"delta": chunk}}}) + "\n\n")
			}
			stream.WriteString(`data: {"choices":[{"delta":{"reasoning_details":[{"index":4,"text":""},{"type":"reasoning.text","format":"unindexed","text":""}]},"finish_reason":"stop"}]}` + "\n\n")
			stream.WriteString("data: [DONE]\n\n")
			responses, _ := decode(t, jsonOf(t, body), stream.String())
			for source, resp := range responses {
				var cold llm.Message
				if err := json.Unmarshal([]byte(jsonOf(t, resp.Message)), &cold); err != nil {
					t.Fatal(err)
				}
				for _, input := range []llm.Message{resp.Message, cold} {
					res := resolved(nil)
					res.Caps.ReplayReasoningText, res.Caps.ReasoningField = new(true), new("reasoning_details")
					emit := func(m llm.Message) map[string]any {
						return build(t, llm.Request{Model: "m", Messages: []llm.Message{m}}, res)["messages"].([]map[string]any)[0]
					}
					msg := emit(input)
					if got := jsonOf(t, msg["reasoning_details"]); got != jsonOf(t, want) {
						t.Errorf("%s signed=%v native array = %s, want %s", source, signed, got, jsonOf(t, want))
					}
					checkScalar(t, msg, "", "")
					res.Caps.ReasoningField = nil
					msg = emit(input)
					checkScalar(t, msg, "reasoning", "independent")
					opaque := []map[string]any{middle}
					if signed {
						opaque = append(opaque, tail)
					}
					if got := jsonOf(t, msg["reasoning_details"]); got != jsonOf(t, opaque) {
						t.Errorf("%s scalar-route continuation = %s, want %s", source, got, jsonOf(t, opaque))
					}
					res.Caps.ReasoningField = new("reasoning_details")
					msg = emit(llm.TrimThinkingForReplayMessage(input, true))
					if got := jsonOf(t, msg["reasoning_details"]); got != jsonOf(t, opaque) {
						t.Errorf("%s raw-ineligible continuation = %s, want %s", source, got, jsonOf(t, opaque))
					}
				}
			}
		}
	})
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
			if got, _ := out[0]["reasoning"].(string); got != scalar || out[0]["reasoning_content"] != nil {
				t.Errorf("scalar %q replays as %#v", scalar, out[0])
			}
		}
	})
	for _, tc := range []struct {
		name, scalar, detail, independent string
		streamPrefix                      int
	}{
		{"independent signed text", "scalar-12", "signed-37", "scalar-12", 0},
		{"signature without detail text", "scalar-12", "", "scalar-12", 0},
		{"trailing detail signature", "scalar-12", "signed-37", "scalar-12", len("signed-37")},
		{"signature with final text fragment", "scalar-12", "signed-37", "scalar-12", len("signed-")},
		{"mirrored signed text", " \tsigned-37\n ", " \tsigned-37\n ", "", len(" \tsigned-")},
		{"signed detail alone", "", "signed-37", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed := map[string]any{"type": "reasoning.text", "signature": "sig-37", "format": "wire-1", "index": 7}
			if tc.detail != "" {
				signed["text"] = tc.detail
			}
			wantSigned := jsonOf(t, []any{signed})
			fields := map[string]any{"reasoning_details": []any{signed}}
			var chunks []map[string]any
			if tc.scalar != "" {
				fields["reasoning"], fields["reasoning_text"] = tc.scalar, tc.scalar
				chunks = append(chunks, map[string]any{"reasoning": tc.scalar, "reasoning_text": tc.scalar})
			}
			body := jsonOf(t, chatResponseRaw(fields, "stop"))
			if tc.streamPrefix != 0 {
				chunks = append(chunks, map[string]any{"reasoning_details": []any{map[string]any{"type": "reasoning.text", "text": tc.detail[:tc.streamPrefix], "index": 7, "format": "wire-1"}}})
				delete(signed, "index")
				delete(signed, "format")
				signed["text"] = tc.detail[tc.streamPrefix:]
			}
			chunks = append(chunks, map[string]any{"reasoning_details": []any{signed}})
			var sse strings.Builder
			for _, chunk := range chunks {
				data := jsonOf(t, map[string]any{"choices": []any{map[string]any{"delta": chunk}}})
				sse.WriteString("data: " + data + "\n\n")
			}
			sse.WriteString("data: [DONE]\n\n")
			responses, _ := decode(t, body, sse.String())
			for mode, resp := range responses {
				t.Run(mode, func(t *testing.T) {
					if got, want := resp.ReasoningText(), strings.Trim(tc.independent+"\n\n"+tc.detail, "\n"); got != want {
						t.Errorf("settled reasoning = %q, want %q", got, want)
					}
					if tc.independent != "" {
						var source string
						for _, part := range resp.Message.Content {
							if part.Thinking != nil && part.Thinking.Text == tc.independent && part.Thinking.EncryptedContent == "" {
								source = part.Thinking.Signature
							}
						}
						if source != "reasoning" {
							t.Errorf("independent scalar source = %q", source)
						}
					}
					var saved llm.Message
					if err := json.Unmarshal([]byte(jsonOf(t, resp.Message)), &saved); err != nil {
						t.Fatal(err)
					}
					if tc.scalar != "" && tc.scalar == tc.detail {
						if len(saved.Content) != 1 || saved.Content[0].Thinking == nil {
							t.Fatalf("signed mirror settled as %#v", saved.Content)
						}
						if source := saved.Content[0].Thinking.ChatScalar; source == nil || source.Field != "reasoning" || source.Text != "" {
							t.Errorf("signed mirror stores scalar source %#v", source)
						}
					}
					for _, route := range []struct {
						override    *string
						scalarField string
					}{{nil, "reasoning"}, {new("reasoning_text"), "reasoning_text"}, {new("reasoning_details"), ""}} {
						res := resolved(nil)
						res.Caps.ReplayReasoningText, res.Caps.ReasoningField = new(true), route.override
						req := llm.Request{Model: "m", Messages: []llm.Message{resp.Message}}
						out := build(t, req, res)
						req.Messages = []llm.Message{saved}
						if jsonOf(t, build(t, req, res)) != jsonOf(t, out) {
							t.Fatalf("persisted request differs on scalar field %q", route.scalarField)
						}
						msg := out["messages"].([]map[string]any)[0]
						checkScalar(t, msg, route.scalarField, tc.scalar)
						details, _ := msg["reasoning_details"].([]map[string]any)
						if jsonOf(t, details) != wantSigned {
							t.Fatalf("signed detail = %s, want %s", jsonOf(t, details), wantSigned)
						}
						if got, _ := details[0]["text"].(string); got != tc.detail {
							t.Errorf("signed text = %q, want %q", got, tc.detail)
						}
					}
					if tc.scalar != "" && tc.scalar == tc.detail {
						for _, caps := range []registry.Caps{{}, {ReplayReasoningText: new(false)}, {Reasoning: new(false), ReplayReasoningText: new(true)}} {
							out, err := toChatMessages([]llm.Message{saved}, caps, false)
							if err != nil {
								t.Fatal(err)
							}
							checkScalar(t, out[0], "", "")
							details, _ := out[0]["reasoning_details"].([]map[string]any)
							wantDetails := 1
							if caps.ReasoningDisabled() {
								wantDetails = 0
							}
							if len(details) != wantDetails {
								t.Errorf("unenrolled replay = %#v", out[0])
							}
						}
					}
				})
			}
		})
	}
	t.Run("interleaved detail indexes", func(t *testing.T) {
		sse := `data: {"choices":[{"delta":{"reasoning_text":"first-2second-7","reasoning_details":[{"type":"reasoning.text","index":2,"text":"first-"},{"type":"reasoning.text","index":7,"text":"second-"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"e3","data":"opaque-3"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":7,"signature":"sig-7","text":"7","metadata":"meta-7"},{"type":"reasoning.text","index":2,"signature":"sig-2","text":"2"}]}}]}

data: [DONE]

`
		want := []map[string]any{
			{"type": "reasoning.text", "index": 2, "text": "first-2", "signature": "sig-2"},
			{"type": "reasoning.text", "index": 7, "text": "second-7", "signature": "sig-7", "metadata": "meta-7"},
			{"type": "reasoning.encrypted", "id": "e3", "data": "opaque-3"},
		}
		complete := map[string]any{"reasoning_text": "first-2second-7", "reasoning_details": want}
		responses, _ := decode(t, jsonOf(t, chatResponseRaw(complete, "stop")), sse)
		for source, resp := range responses {
			for _, useDetails := range []bool{true, false} {
				out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{ReplayReasoningText: new(true)}, useDetails)
				if err != nil {
					t.Fatal(err)
				}
				if got := jsonOf(t, out[0]["reasoning_details"]); got != jsonOf(t, want) {
					t.Errorf("%s reordered authenticated details: %s", source, got)
				}
				if !useDetails && out[0]["reasoning_text"] != complete["reasoning_text"] {
					t.Errorf("%s aggregate scalar replay = %#v", source, out[0])
				}
			}
		}
	})
	t.Run("unindexed signatures with two open blocks", func(t *testing.T) {
		const signatures = `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","signature":"unassigned"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","id":"b","signature":"b7"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":0,"signature":"a0"}]}}]}

data: [DONE]

`
		const a = `{"type":"reasoning.text","id":"a","index":0,"text":"alpha"}`
		const b = `{"type":"reasoning.text","id":"b","text":"beta"}`
		chunk := func(details string) string {
			return `data: {"choices":[{"delta":{"reasoning_details":[` + details + `]}}]}` + "\n\n"
		}
		for _, prefix := range []string{chunk(a + "," + b), chunk(a) + chunk(b)} {
			resp, err := ExtractRecordedResponse([]byte(prefix+signatures), "m")
			if err != nil {
				t.Fatal(err)
			}
			out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{}, false)
			if err != nil {
				t.Fatal(err)
			}
			details, _ := out[0]["reasoning_details"].([]map[string]any)
			if len(details) != 3 || details[2]["signature"] != "unassigned" || details[2]["text"] != nil ||
				details[1]["signature"] != "b7" || details[1]["text"] != "beta" || details[1]["index"] != nil ||
				details[0]["signature"] != "a0" || details[0]["text"] != "alpha" || details[0]["id"] != "a" {
				t.Fatalf("signatures and authenticated text = %#v", details)
			}
		}
	})
	t.Run("native index arrives after detail ID", func(t *testing.T) {
		const sse = `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","id":"a","text":"alpha "},{"type":"reasoning.text","id":"b","text":"beta "}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","id":"b","index":0,"text":"two "},{"type":"reasoning.text","id":"a","index":7,"text":"one "}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":7,"signature":"sig-a"},{"type":"reasoning.text","index":0,"signature":"sig-b"}]}}]}

data: [DONE]

`
		want := []map[string]any{
			{"type": "reasoning.text", "id": "a", "index": 7, "text": "alpha one ", "signature": "sig-a"},
			{"type": "reasoning.text", "id": "b", "index": 0, "text": "beta two ", "signature": "sig-b"},
		}
		complete := map[string]any{"reasoning_details": want}
		responses, _ := decode(t, jsonOf(t, chatResponseRaw(complete, "stop")), sse)
		for source, resp := range responses {
			out, err := toChatMessages([]llm.Message{resp.Message}, registry.Caps{}, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := jsonOf(t, out[0]["reasoning_details"]); got != jsonOf(t, want) {
				t.Errorf("%s signed details = %s, want %s", source, got, jsonOf(t, want))
			}
		}
	})
	t.Run("a late identity joins a signed detail in arrival order", func(t *testing.T) {
		check := func(t *testing.T, batches [][]map[string]any, want []map[string]any) {
			t.Helper()
			var sse strings.Builder
			for _, batch := range batches {
				sse.WriteString(`data: {"choices":[{"delta":{"reasoning_details":` + jsonOf(t, batch) + `}}]}` + "\n\n")
			}
			sse.WriteString("data: [DONE]\n\n")
			body := chatResponseRaw(map[string]any{"reasoning_details": want}, "stop")
			responses, _ := decode(t, jsonOf(t, body), sse.String())
			for source, resp := range responses {
				var saved llm.Message
				if err := json.Unmarshal([]byte(jsonOf(t, resp.Message)), &saved); err != nil {
					t.Fatal(err)
				}
				for _, input := range []llm.Message{resp.Message, saved} {
					out, err := toChatMessages([]llm.Message{input}, registry.Caps{ReplayReasoningText: new(true)}, true)
					if err != nil {
						t.Fatal(err)
					}
					if got := jsonOf(t, out[0]["reasoning_details"]); got != jsonOf(t, want) {
						t.Errorf("%s native details = %s, want %s", source, got, jsonOf(t, want))
					}
				}
			}
		}
		opaque := map[string]any{"type": "reasoning.encrypted", "id": "other", "data": "sealed"}
		want := []map[string]any{
			{"type": "reasoning.text", "id": "a", "index": 7, "format": "fmt", "text": " α\t<B> C\r\nDΩ", "signature": "sig-a", "hint": "id-last"},
			opaque,
		}
		check(t, [][]map[string]any{
			{{"type": "reasoning.text", "id": "a", "format": "fmt", "text": " α\t", "hint": "id-first"}},
			{opaque},
			{{"index": 7, "text": "<B>", "hint": "index"}},
			{{"id": "a", "text": " C\r\n", "hint": "id-last"}},
			{{"index": 7, "text": "D"}},
			{{"id": "a", "index": 7, "text": "Ω", "signature": "sig-a"}},
		}, want)
		indexed := map[string]any{"type": "reasoning.text", "index": 7, "text": "index"}
		named := map[string]any{"type": "reasoning.text", "id": "a", "text": "name"}
		sig := map[string]any{"type": "reasoning.text", "id": "a", "index": 7, "signature": "sig"}
		t.Run("index precedes ID", func(t *testing.T) {
			other := map[string]any{"type": "reasoning.text", "index": 9, "text": "other"}
			check(t, [][]map[string]any{{indexed, other}, {named}, {sig}}, []map[string]any{
				{"type": "reasoning.text", "id": "a", "index": 7, "text": "indexname", "signature": "sig"}, other,
			})
		})
		t.Run("separate siblings cannot supply the signature text", func(t *testing.T) {
			check(t, [][]map[string]any{{named, indexed}, {sig}}, []map[string]any{named, indexed, sig})
		})
		t.Run("incompatible formats cannot supply the signature text", func(t *testing.T) {
			named := map[string]any{"type": "reasoning.text", "id": "a", "format": "one", "text": "name"}
			indexed := map[string]any{"type": "reasoning.text", "index": 7, "format": "two", "text": "index"}
			check(t, [][]map[string]any{{named}, {indexed}, {sig}}, []map[string]any{named, indexed, sig})
		})
	})
	t.Run("empty indexed openings retain signed detail metadata", func(t *testing.T) {
		const sse = `data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","id":"a","index":7,"format":"fmt-a"},{"type":"reasoning.text","id":"b","index":2},{"type":"reasoning.text","id":"unused","index":9}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"index":2,"format":"fmt-b"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","index":2,"text":"beta"},{"type":"reasoning.text","index":7,"text":"alpha"}]}}]}

data: {"choices":[{"delta":{"reasoning_details":[{"type":null,"id":null,"format":"","index":2,"signature":"sig-b"},{"type":"reasoning.text","index":7,"signature":"sig-a"}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
		want := []map[string]any{
			{"type": "reasoning.text", "id": "a", "index": 7, "format": "fmt-a", "text": "alpha", "signature": "sig-a"},
			{"type": "reasoning.text", "id": "b", "index": 2, "format": "fmt-b", "text": "beta", "signature": "sig-b"},
		}
		complete := append(slices.Clone(want), map[string]any{"type": "reasoning.text", "id": "unused", "index": 9})
		responses, _ := decode(t, jsonOf(t, chatResponseRaw(map[string]any{"reasoning_details": complete}, "stop")), sse)
		res := resolved(nil)
		res.Caps.ReplayReasoningText = new(true)
		for source, resp := range responses {
			if len(resp.Message.Content) != len(want) {
				t.Errorf("%s settled %d parts, want only %d signed details", source, len(resp.Message.Content), len(want))
			}
			var saved llm.Message
			if err := json.Unmarshal([]byte(jsonOf(t, resp.Message)), &saved); err != nil {
				t.Fatal(err)
			}
			msg := build(t, llm.Request{Model: "m", Messages: []llm.Message{saved}}, res)["messages"].([]map[string]any)[0]
			if got := jsonOf(t, msg["reasoning_details"]); got != jsonOf(t, want) {
				t.Errorf("%s native reasoning details = %s, want %s", source, got, jsonOf(t, want))
			}
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
