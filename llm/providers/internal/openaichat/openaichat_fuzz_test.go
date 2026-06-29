package openaichat

import (
	"encoding/json"
	"testing"
)

// FuzzToolArgumentsString drives the real ToolArgumentsString seam, which
// sanitizes an untrusted Chat Completions tool-call arguments blob into a
// provider-safe string. Oracles beyond no-panic:
//
//   - Object/empty invariant: the result is ALWAYS either "{}" or a string that
//     re-parses into a non-nil JSON object. A strict OpenAI-compatible endpoint
//     rejects anything else when replaying tool-call history, so a leak of a
//     non-object (array, scalar, malformed) past this helper is a real bug.
//   - Determinism: the same bytes must map to the same string every time.
func FuzzToolArgumentsString(f *testing.F) {
	seeds := []string{
		`{"status":"in_progress"}`,
		``,
		`null`,
		`["status"]`,
		`{"status": in_progress"}`,
		`  {"a":1}  `,
		`123`,
		`"a string"`,
		`{}`,
		`{"nested":{"x":[1,2,3]}}`,
		"\x00\xff",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		got := ToolArgumentsString(json.RawMessage(raw))

		if again := ToolArgumentsString(json.RawMessage(raw)); again != got {
			t.Fatalf("ToolArgumentsString not deterministic: %q vs %q (input %q)", got, again, raw)
		}

		if got == "{}" {
			return
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(got), &obj); err != nil {
			t.Fatalf("ToolArgumentsString returned non-JSON %q for input %q: %v", got, raw, err)
		}
		if obj == nil {
			t.Fatalf("ToolArgumentsString returned a JSON null/non-object %q for input %q", got, raw)
		}
	})
}

// FuzzParseChatUsage drives ParseChatUsage, which maps an untrusted Chat
// Completions "usage" object onto llm.Usage. The input bytes are decoded into
// the map[string]any shape ParseChatUsage consumes, exercising the real seam.
// Oracles beyond no-panic:
//
//   - Non-negative input: the "InputTokens means new uncached input" invariant
//     clamps prompt-minus-cached to >= 0 (openaichat.go); a negative leak is a bug.
func FuzzParseChatUsage(f *testing.F) {
	seeds := []string{
		`{"prompt_tokens":10,"completion_tokens":5}`,
		`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`,
		`{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":4}}`,
		`{"prompt_tokens":2,"prompt_tokens_details":{"cached_tokens":9}}`,
		`{"completion_tokens_details":{"reasoning_tokens":7}}`,
		`{}`,
		`{"prompt_tokens":"12","completion_tokens":"3"}`,
		`{"prompt_tokens":1.5}`,
		`{"prompt_tokens_details":"not-an-object"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil || m == nil {
			return
		}

		usage := ParseChatUsage(m)

		if usage.InputTokens < 0 {
			t.Fatalf("ParseChatUsage InputTokens negative: %d (input %q)", usage.InputTokens, raw)
		}
	})
}
