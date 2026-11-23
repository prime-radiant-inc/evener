package anthropic

import (
	"testing"

	"primeradiant.com/evener/llm"
)

func TestToAnthropicMessages_SkipsGoogleInteractionThinking(t *testing.T) {
	t.Parallel()
	msgs := []llm.Message{
		llm.User("hi"),
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						GoogleInteraction: true,
						GoogleInstance:    "google",
						GoogleModel:       "gemini-4-neon",
						Text:              "google reasoning summary",
						Signature:         "sig_google",
						Summary:           []string{"google reasoning summary"},
					},
				},
				{
					Kind: llm.ContentThinking,
					Thinking: &llm.ThinkingData{
						Text:      "native anthropic reasoning",
						Signature: "sig_anthropic",
					},
				},
				{Kind: llm.ContentText, Text: "answer"},
			},
		},
	}

	_, out, err := toAnthropicMessages(msgs, false)
	if err != nil {
		t.Fatalf("toAnthropicMessages: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("messages = %d, want 2", len(out))
	}
	blocks, ok := out[1]["content"].([]map[string]any)
	if !ok {
		t.Fatalf("assistant content = %T, want []map[string]any", out[1]["content"])
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want 2 (native thinking + text; Google thinking skipped)", blocks)
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "native anthropic reasoning" || blocks[0]["signature"] != "sig_anthropic" {
		t.Fatalf("blocks[0] = %#v, want native anthropic thinking", blocks[0])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != "answer" {
		t.Fatalf("blocks[1] = %#v, want text 'answer'", blocks[1])
	}
}
