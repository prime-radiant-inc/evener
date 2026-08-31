package chatcompletions

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

// The two bodies below are the verbatim wire shapes captured on 2026-08-31
// from live completions that answered 200 with no reply text (issue #715):
// openrouter/minimax/minimax-m2.7 and lunaroute/glm-5.2-vision. Every key the
// provider sent is kept — that is the point of the fixture, since it shows
// there is no further text-bearing field the decoder could be missing — and
// only the chain-of-thought prose is shortened, to a string that still breaks
// off mid-sentence exactly as the captured one did.
//
// Both requests capped output at 64 tokens. Both models spent all 64 on
// reasoning and were cut off (`finish_reason: "length"`) before emitting a
// single visible token, so `content` came back null. An empty Text() is the
// correct reading of these bodies, not a folding gap: nothing in them is
// dropped, as the thinking assertions below show.

const truncatedReasoningOnlyMiniMax = `{
  "id": "gen-fixture",
  "object": "chat.completion",
  "created": 0,
  "model": "minimax/minimax-m2.7",
  "provider": "Minimax",
  "system_fingerprint": null,
  "service_tier": null,
  "choices": [
    {
      "index": 0,
      "finish_reason": "length",
      "native_finish_reason": "length",
      "logprobs": null,
      "message": {
        "role": "assistant",
        "content": null,
        "refusal": null,
        "reasoning": "The user wants one word. Should I add punctuation? I will stick to just \"pong",
        "reasoning_details": [
          {
            "type": "reasoning.text",
            "text": "The user wants one word. Should I add punctuation? I will stick to just \"pong",
            "format": "unknown",
            "index": 0
          }
        ]
      }
    }
  ],
  "usage": {
    "prompt_tokens": 48,
    "completion_tokens": 64,
    "total_tokens": 112,
    "completion_tokens_details": {"reasoning_tokens": 64, "image_tokens": 0, "audio_tokens": 0},
    "prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0, "audio_tokens": 0, "video_tokens": 0}
  }
}`

const truncatedReasoningOnlyGLM = `{
  "id": "chatcmpl-fixture",
  "object": "chat.completion",
  "created": 0,
  "model": "glm-5.2-vision",
  "service_tier": null,
  "prompt_logprobs": null,
  "prompt_token_ids": null,
  "prompt_text": null,
  "kv_transfer_params": null,
  "ec_transfer_params": null,
  "metrics": null,
  "choices": [
    {
      "index": 0,
      "finish_reason": "length",
      "stop_reason": null,
      "logprobs": null,
      "token_ids": null,
      "routed_experts": null,
      "message": {
        "role": "assistant",
        "content": null,
        "refusal": null,
        "annotations": null,
        "audio": null,
        "function_call": null,
        "reasoning": "The user wants one word. Should I add punctuation? I will stick to just \"pong"
      }
    }
  ],
  "usage": {
    "prompt_tokens": 19,
    "completion_tokens": 64,
    "total_tokens": 83,
    "prompt_tokens_details": {"cached_tokens": 0, "created_cache_tokens": 0, "multimodal_tokens": null}
  }
}`

// TestTruncatedReasoningOnlyResponse pins the reading of a 200 whose message
// carries reasoning and a null content because the output cap cut the model
// off mid-thought. Text() is empty and that is correct; what must never
// regress is that the reasoning still arrives as a thinking part and the
// truncation is still legible as finish_reason "length", so a caller can tell
// "the model was cut off" from "we dropped the reply".
func TestTruncatedReasoningOnlyResponse(t *testing.T) {
	tests := []struct {
		name string
		body string
		// field is the wire field the reasoning arrived on, as reported on
		// ThinkingData.Signature. reasoning_details reports none, because
		// it replays through the reasoning_details path instead.
		field string
	}{
		{name: "openrouter/minimax/minimax-m2.7", body: truncatedReasoningOnlyMiniMax, field: ""},
		{name: "lunaroute/glm-5.2-vision", body: truncatedReasoningOnlyGLM, field: "reasoning"},
	}
	const wantReasoning = `The user wants one word. Should I add punctuation? I will stick to just "pong`

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(tt.body), &raw); err != nil {
				t.Fatalf("fixture is not JSON: %v", err)
			}
			resp, err := fromChatCompletionResponse(raw, nil)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if resp.Text() != "" {
				t.Fatalf("Text() = %q, want empty: the body's content is null", resp.Text())
			}
			if len(resp.Message.Content) != 1 {
				t.Fatalf("Content = %+v, want exactly the thinking part", resp.Message.Content)
			}
			part := resp.Message.Content[0]
			if part.Kind != llm.ContentThinking || part.Thinking == nil {
				t.Fatalf("part 0 = %+v, want a thinking part", part)
			}
			if part.Thinking.Text != wantReasoning {
				t.Fatalf("thinking text = %q, want the whole reasoning field", part.Thinking.Text)
			}
			if part.Thinking.Signature != tt.field {
				t.Fatalf("thinking arrived on field %q, want %q", part.Thinking.Signature, tt.field)
			}
			if resp.Finish.Reason != "length" {
				t.Fatalf("Finish.Reason = %q, want %q — the truncation is the whole explanation for the empty text",
					resp.Finish.Reason, "length")
			}
		})
	}
}
