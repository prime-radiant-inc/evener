package openaicompat

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzFromChatCompletionResponse drives the non-streaming response mapper
// (fromChatCompletionResponse, which calls extractReasoning) directly with
// reasoning-rich Chat Completions JSON. The existing Complete fuzzer reaches this
// mapper through an httptest server but its seed corpus never carries
// reasoning_details/reasoning_content, leaving extractReasoning's join logic
// unfuzzed; this target feeds those branches without a network round-trip.
//
// Oracles beyond no-panic:
//   - a decode that succeeds always stamps the openai-compatible provider;
//   - a thinking content part is present IFF extractReasoning returns non-empty
//     for the choice — a leaked blank thinking part or a dropped reasoning chain
//     reddens it;
//   - tool-call count is preserved: every tool_call in the chosen message becomes
//     exactly one ContentToolCall part;
//   - determinism: the same raw map maps to the same provider/finish/part-kinds.
func FuzzFromChatCompletionResponse(f *testing.F) {
	seeds := []string{
		`{"id":"c1","model":"m","choices":[{"message":{"role":"assistant","content":"hi","reasoning_content":"because"},"finish_reason":"stop"}]}`,
		`{"choices":[{"message":{"role":"assistant","reasoning_details":[{"type":"reasoning.text","text":"a"},{"type":"thinking","thinking":"b"}]}}]}`,
		`{"choices":[{"message":{"role":"assistant","reasoning_details":[{"type":"x"}]}}]}`,
		`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"t1","function":{"name":"shell","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"  "}}]}`,
		`{"choices":[]}`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return
		}

		quirks := QuirksPreset("")
		resp, err := fromChatCompletionResponse(m, quirks)
		if err != nil {
			return // no choices / malformed shape is an honest structured error.
		}

		if resp.Provider != "openai-compatible" {
			t.Fatalf("provider=%q, want openai-compatible (raw=%s)", resp.Provider, raw)
		}

		// Recompute the reasoning the mapper would have seen for choice 0 and check
		// the thinking-part invariant against it. We re-derive the chosen message
		// through the SAME marshal+typed-unmarshal the product uses, so we inherit
		// encoding/json's case-insensitive field matching (a map key lookup here
		// would miss mixed-case keys the product still binds).
		firstMsg, haveChoice := firstChoiceMessage(m)
		var wantReasoning string
		if haveChoice {
			wantReasoning, _ = extractReasoning(firstMsg)
		}
		var thinkingParts, gotToolCalls int
		for _, p := range resp.Message.Content {
			switch p.Kind {
			case llm.ContentThinking:
				thinkingParts++
				if p.Thinking == nil || p.Thinking.Text == "" {
					t.Fatalf("emitted an empty thinking part (raw=%s)", raw)
				}
			case llm.ContentToolCall:
				gotToolCalls++
			}
		}
		if (wantReasoning != "") != (thinkingParts == 1) {
			t.Fatalf("thinking-part presence=%v but extractReasoning non-empty=%v (raw=%s)",
				thinkingParts == 1, wantReasoning != "", raw)
		}

		wantToolCalls := len(firstMsg.ToolCalls)
		if gotToolCalls != wantToolCalls {
			t.Fatalf("tool-call parts=%d, want %d (raw=%s)", gotToolCalls, wantToolCalls, raw)
		}

		again, _ := fromChatCompletionResponse(m, quirks)
		if again.Provider != resp.Provider || again.Finish.Reason != resp.Finish.Reason ||
			len(again.Message.Content) != len(resp.Message.Content) {
			t.Fatalf("fromChatCompletionResponse not deterministic (raw=%s)", raw)
		}
	})
}

// firstChoiceMessage decodes the chosen message exactly as
// fromChatCompletionResponse does — marshal the raw map, then unmarshal into the
// typed chatCompletionResponse — so the oracle inherits the same
// case-insensitive JSON field binding the product relies on.
func firstChoiceMessage(m map[string]any) (chatMessage, bool) {
	b, err := json.Marshal(m)
	if err != nil {
		return chatMessage{}, false
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(b, &parsed); err != nil || len(parsed.Choices) == 0 {
		return chatMessage{}, false
	}
	return parsed.Choices[0].Message, true
}

// FuzzOpenAICompatMultimodalParts drives buildMultimodalParts, the Chat
// Completions request builder for the openai-compatible adapter. It feeds inline
// image data and remote (never local) URLs, so no os.ReadFile is reached.
//
// Oracles beyond no-panic:
//   - every emitted entry re-marshals to valid JSON with a legal type
//     (text/image_url);
//   - a text part always yields exactly one text entry;
//   - an image with inline data OR a non-empty URL always yields exactly one
//     image_url entry whose nested object carries a non-empty "url".
func FuzzOpenAICompatMultimodalParts(f *testing.F) {
	f.Add("hello", []byte{1, 2, 3}, "image/jpeg", "high", "")
	f.Add("", []byte{}, "", "", "https://example.com/a.png")
	f.Add("t", []byte{}, "", "", "")

	f.Fuzz(func(t *testing.T, text string, imgData []byte, imgMedia, imgDetail, imgURL string) {
		// Force any URL to be remote so IsLocalPath is false (no disk read).
		safeURL := imgURL
		if safeURL != "" {
			safeURL = "https://h/" + safeURL
		}

		parts := []llm.ContentPart{
			{Kind: llm.ContentText, Text: text},
			{Kind: llm.ContentImage, Image: &llm.ImageData{
				URL: safeURL, Data: imgData, MediaType: imgMedia, Detail: imgDetail,
			}},
		}

		out := buildMultimodalParts(parts)

		gotText, gotImage := 0, 0
		for i, entry := range out {
			if _, err := json.Marshal(entry); err != nil {
				t.Fatalf("entry[%d] unmarshalable: %v\nentry=%#v", i, err, entry)
			}
			switch entry["type"] {
			case "text":
				gotText++
			case "image_url":
				gotImage++
				urlObj, ok := entry["image_url"].(map[string]any)
				if !ok {
					t.Fatalf("image_url entry missing nested object: %#v", entry)
				}
				if u, _ := urlObj["url"].(string); u == "" {
					t.Fatalf("image_url entry has empty url: %#v", entry)
				}
			default:
				t.Fatalf("entry[%d] illegal type %v", i, entry["type"])
			}
		}
		if gotText != 1 {
			t.Fatalf("text parts emitted=%d, want 1", gotText)
		}
		if (len(imgData) > 0 || safeURL != "") && gotImage != 1 {
			t.Fatalf("image emitted=%d, want 1 (dataLen=%d url=%q)", gotImage, len(imgData), safeURL)
		}
	})
}
