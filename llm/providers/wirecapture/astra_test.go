package wirecapture

import (
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
)

// A catalog refresh must not be required to select Astra, and every request
// must use the Codex Lite route while retaining tools, instructions, and images.
func TestCodexAstraRequests(t *testing.T) {
	h := newHarness(t)
	for _, effort := range []string{"", "high"} {
		t.Run("effort="+effort, func(t *testing.T) {
			req := toolsRequest(effort)
			req.Temperature, req.TopP = new(0.3), new(0.7)
			req.Messages[1].Content = append(req.Messages[1].Content, llm.ContentPart{
				Kind:  llm.ContentImage,
				Image: &llm.ImageData{URL: "https://example.com/image.png"},
			})
			got := h.run(t, wireCase{"codex-astra", "openai-codex/gpt-6-astra", true, req})
			body := bodyOf(t, got)
			if got.Headers["X-Openai-Internal-Codex-Responses-Lite"] != "true" {
				t.Fatalf("Lite routing header missing: %v", got.Headers)
			}
			if body["model"] != "gpt-6-astra" || body["instructions"] != "" || body["tools"] != nil || body["parallel_tool_calls"] != false {
				t.Fatalf("incorrect Astra request framing: %s", got.Body)
			}
			input := body["input"].([]any)
			tools := input[0].(map[string]any)
			if tools["type"] != "additional_tools" || len(tools["tools"].([]any)) != 1 || input[1].(map[string]any)["role"] != "developer" {
				t.Fatalf("tools/instructions missing from Lite input: %v", input)
			}
			user := input[2].(map[string]any)["content"].([]any)
			if user[1].(map[string]any)["detail"] != "original" {
				t.Fatalf("Astra image detail = %v", user[1])
			}
			reasoning, ok := body["reasoning"].(map[string]any)
			if !ok || reasoning["context"] != "all_turns" || body["include"] == nil {
				t.Fatalf("reasoning replay disabled: %v", body)
			}
			if effort != "" && reasoning["effort"] != effort {
				t.Fatalf("wire effort = %v, want %s", reasoning["effort"], effort)
			}
			for _, field := range []string{"temperature", "top_p", "max_output_tokens", "prompt_cache_retention"} {
				if body[field] != nil {
					t.Errorf("unsupported Codex field %s sent", field)
				}
			}
		})
	}
}

func TestCodexAstraReplaysReasoningAndToolResult(t *testing.T) {
	h := newHarness(t)
	req := toolsRequest("max")
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_astra", EncryptedContent: "opaque-reasoning"}},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_astra", Name: "weather", Arguments: []byte(`{"city":"Oslo"}`)}},
	}}, llm.ToolResultNamed("call_astra", "weather", "Sunny", false))
	body := bodyOf(t, h.run(t, wireCase{"codex-astra-replay", "openai-codex/gpt-6-astra", true, req}))
	var reasoning, call, result map[string]any
	for _, raw := range body["input"].([]any) {
		item := raw.(map[string]any)
		switch item["type"] {
		case "reasoning":
			reasoning = item
		case "function_call":
			call = item
		case "function_call_output":
			result = item
		}
	}
	if reasoning["id"] != "rs_astra" || reasoning["encrypted_content"] != "opaque-reasoning" {
		t.Fatalf("reasoning lost on replay: %v", reasoning)
	}
	if call["call_id"] != "call_astra" || call["name"] != "weather" || result["call_id"] != "call_astra" || result["output"] != "Sunny" {
		t.Fatalf("tool round trip lost on replay: call=%v result=%v", call, result)
	}
	if !slices.Contains(body["include"].([]any), any("reasoning.encrypted_content")) {
		t.Fatalf("encrypted reasoning not requested: %v", body["include"])
	}
}
