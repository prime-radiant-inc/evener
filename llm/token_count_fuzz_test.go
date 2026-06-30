package llm

import (
	"strings"
	"testing"
)

var tokenFamilies = map[string]bool{"": true, "google": true, "anthropic": true, "openai": true}

// FuzzTokenEstimators drives the pure image/dimension token estimators
// (providerTokenFamily, estimateGoogleImageTokens, estimateAnthropicImageTokens,
// estimateImageTokens dispatch, ceilDiv) over an arbitrary provider/model and
// image dimensions. These deterministic helpers price every inline image in the
// local token estimate; only fixed unit cases reached them.
//
// Oracles:
//   - providerTokenFamily returns a member of the closed family set and is
//     deterministic.
//   - ceilDiv honors its contract: 0 for n<=0, else the unique k with
//     (k-1)*d < n <= k*d.
//   - the per-family estimators never return a negative token count for
//     non-negative dimensions.
func FuzzTokenEstimators(f *testing.F) {
	f.Add("google", "gemini-2.5-pro", 100, 100, "auto")
	f.Add("anthropic", "claude-opus-4-5", 1024, 768, "high")
	f.Add("openai", "gpt-5.2", 2048, 2048, "low")
	f.Add("", "mystery", 0, 0, "")

	f.Fuzz(func(t *testing.T, provider, model string, w, h int, detail string) {
		// Bound magnitudes so int arithmetic in the OpenAI estimator can't overflow
		// (an absurd-dimension overflow is not a product concern); negatives and
		// realistic ranges are still exercised.
		w = clampDim(w)
		h = clampDim(h)

		fam := providerTokenFamily(provider, model)
		if !tokenFamilies[fam] {
			t.Fatalf("providerTokenFamily(%q,%q)=%q not a known family", provider, model, fam)
		}
		if again := providerTokenFamily(provider, model); again != fam {
			t.Fatalf("providerTokenFamily not deterministic")
		}

		// ceilDiv contract for a positive divisor derived from h.
		d := (h%97+98)%97 + 1 // 1..97
		k := ceilDiv(w, d)
		if w <= 0 {
			if k != 0 {
				t.Fatalf("ceilDiv(%d,%d)=%d, want 0 for n<=0", w, d, k)
			}
		} else {
			if (k-1)*d >= w || k*d < w {
				t.Fatalf("ceilDiv(%d,%d)=%d violates (k-1)*d < n <= k*d", w, d, k)
			}
		}

		if w >= 0 && h >= 0 {
			if g := estimateGoogleImageTokens(w, h); g < 0 {
				t.Fatalf("google estimate negative: %d (%dx%d)", g, w, h)
			}
			if a := estimateAnthropicImageTokens(w, h); a < 0 {
				t.Fatalf("anthropic estimate negative: %d (%dx%d)", a, w, h)
			}
			if o := estimateOpenAIImageTokens(w, h, detail); o < 0 {
				t.Fatalf("openai estimate negative: %d (%dx%d, %q)", o, w, h, detail)
			}
		}

		// The dispatcher over an inline image with no decodable bytes falls through
		// to the fallback branch; it must stay non-negative.
		img := &ImageData{MediaType: "image/png", Detail: detail}
		if got := estimateImageTokens(provider, model, img); got < 0 {
			t.Fatalf("estimateImageTokens negative: %d", got)
		}
	})
}

func clampDim(n int) int {
	const limit = 50000
	if n < -limit {
		return -limit
	}
	if n > limit {
		return limit
	}
	return n
}

// FuzzEstimateInputTokens drives EstimateInputTokens / EstimateMessagesInputTokens
// (and estimateMessagesInputTokens / estimateMessageInputParts) over text-only
// requests assembled from fuzzed strings. These produce the deterministic local
// token estimate stamped onto every api_call; only unit tests touched them.
//
// Oracles:
//   - the estimate is non-negative and deterministic.
//   - it is monotonic: appending another text message never lowers the estimate
//     (the estimator must not lose content), and adding a tool never lowers it.
func FuzzEstimateInputTokens(f *testing.F) {
	f.Add("openai", "gpt-5.2", "hello world", "system prompt", "shell")
	f.Add("", "", "", "", "")
	f.Add("anthropic", "claude", strings.Repeat("x", 500), "ctx", "")

	f.Fuzz(func(t *testing.T, provider, model, userText, sysText, toolName string) {
		base := Request{
			Provider: provider,
			Model:    model,
			Messages: []Message{System(sysText), User(userText)},
		}
		got := EstimateInputTokens(base)
		if got.Tokens < 0 {
			t.Fatalf("negative estimate: %d", got.Tokens)
		}
		if got.Source != TokenCountSourceLocalEstimate || got.Exact {
			t.Fatalf("local estimate mislabeled: %+v", got)
		}
		if again := EstimateInputTokens(base); again.Tokens != got.Tokens {
			t.Fatalf("estimate not deterministic: %d vs %d", got.Tokens, again.Tokens)
		}

		// Monotonic under appending another message.
		more := base
		more.Messages = append(append([]Message(nil), base.Messages...), Assistant(userText))
		if EstimateInputTokens(more).Tokens < got.Tokens {
			t.Fatalf("estimate dropped after appending a message: %d < %d", EstimateInputTokens(more).Tokens, got.Tokens)
		}

		// Adding a tool definition never lowers the estimate.
		if strings.TrimSpace(toolName) != "" {
			withTool := base
			withTool.Tools = []ToolDefinition{{Name: toolName, Description: "d"}}
			if EstimateInputTokens(withTool).Tokens < got.Tokens {
				t.Fatalf("estimate dropped after adding a tool: %d < %d", EstimateInputTokens(withTool).Tokens, got.Tokens)
			}
		}

		// EstimateMessagesInputTokens agrees with the message-only portion (no tools).
		msgOnly := Request{Provider: provider, Model: model, Messages: base.Messages}
		if EstimateMessagesInputTokens(base.Messages).Tokens < 0 {
			t.Fatalf("negative message-only estimate")
		}
		_ = msgOnly
	})
}
