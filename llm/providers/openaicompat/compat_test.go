package openaicompat

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// A catalog output cap that equals (or exceeds) the model's context window is
// junk data — input and output share the window, so such a cap can't coexist
// with any prompt. LiteLLM's openrouter/anthropic/claude-sonnet-4.5 entry
// claims max_output_tokens == max_input_tokens == 1M, and sending
// max_tokens=1000000 made OpenRouter 400 on a trivial request (live,
// 2026-07-02). The guard leaves DefaultMaxTokens unset so the provider's own
// default governs.
func TestFillFromCatalog_RejectsOutputCapSwallowingWindow(t *testing.T) {
	junkOut := 1_000_000
	saneOut := 64_000
	junk := &llm.ModelInfo{ContextWindow: 1_000_000, MaxOutputTokens: &junkOut}
	sane := &llm.ModelInfo{ContextWindow: 200_000, MaxOutputTokens: &saneOut}

	var mc ModelCompat
	fillFromCatalog(&mc, func(id string) *llm.ModelInfo { return junk }, "openrouter", "anthropic/claude-sonnet-4.5")
	if mc.DefaultMaxTokens != 0 {
		t.Errorf("junk cap adopted: DefaultMaxTokens = %d, want 0", mc.DefaultMaxTokens)
	}

	mc = ModelCompat{}
	fillFromCatalog(&mc, func(id string) *llm.ModelInfo { return sane }, "", "claude-sonnet-4-5")
	if mc.DefaultMaxTokens != 64_000 {
		t.Errorf("sane cap lost: DefaultMaxTokens = %d, want 64000", mc.DefaultMaxTokens)
	}
}
