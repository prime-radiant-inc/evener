package provider

// White-box unit tests for the unexported catalog-resolution helpers
// (resolveOpenAICompatCatalogModel, resolveOpenRouterAnthropicWebSearch).

import (
	"testing"

	"primeradiant.com/serf/llm"
)

// TestResolveOpenAICompatCatalogModel exercises the lookup precedence
// using a fake catalog so each branch can be observed directly. The
// embedded catalog ships every ollama/llama3* variant with the same
// 8192 context window, so a real-data test cannot distinguish the
// exact-tagged path from the tag-stripped fallback.
func TestResolveOpenAICompatCatalogModel(t *testing.T) {
	fake := func(entries map[string]int) func(string) *llm.ModelInfo {
		return func(key string) *llm.ModelInfo {
			if ctx, ok := entries[key]; ok {
				return &llm.ModelInfo{ID: key, ContextWindow: ctx}
			}
			return nil
		}
	}

	t.Run("prefixed key wins when both exist (openrouter overlap)", func(t *testing.T) {
		// Real-world case: catalog has both "deepseek/deepseek-r1"
		// (the deepseek provider's entry) and
		// "openrouter/deepseek/deepseek-r1" (OpenRouter's entry,
		// possibly with a different context window). Asking for
		// openrouter/deepseek-r1 must hit the OpenRouter entry, not
		// the deepseek one.
		lookup := fake(map[string]int{
			"deepseek/deepseek-r1":            65536, // wrong provider's entry
			"openrouter/deepseek/deepseek-r1": 65336, // correct match
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "openrouter", "deepseek/deepseek-r1")
		if mi == nil {
			t.Fatal("got nil, want openrouter-prefixed match")
		}
		if mi.ContextWindow != 65336 {
			t.Fatalf("ContextWindow = %d, want 65336 (openrouter-prefixed); got %d means the bare lookup fired before the prefixed match",
				mi.ContextWindow, mi.ContextWindow)
		}
	})

	t.Run("bare key wins when prefixed misses (kimi/glm style)", func(t *testing.T) {
		// kimi and glm catalog keys are unprefixed — the prefixed
		// lookup misses, so the bare lookup is the actual match.
		lookup := fake(map[string]int{"kimi-k2.5": 100})
		mi := resolveOpenAICompatCatalogModel(lookup, "kimi", "kimi-k2.5")
		if mi == nil || mi.ContextWindow != 100 {
			t.Fatalf("got %+v, want bare match", mi)
		}
	})

	t.Run("prefixed key matches when only it exists", func(t *testing.T) {
		lookup := fake(map[string]int{"ollama/llama3.1": 200})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3.1")
		if mi == nil || mi.ContextWindow != 200 {
			t.Fatalf("got %+v, want prefixed match", mi)
		}
	})

	t.Run("exact tagged prefixed key wins over tag-stripped fallback", func(t *testing.T) {
		// Both keys exist with DIFFERENT context windows. The exact tagged
		// key must be selected, NOT the tag-stripped one. If the lookup
		// regresses and the third (stripped) branch fires before the
		// second (exact prefixed) branch, this test catches it.
		lookup := fake(map[string]int{
			"ollama/llama3":    111, // tag-stripped fallback target
			"ollama/llama3:8b": 222, // exact tagged target — should win
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3:8b")
		if mi == nil {
			t.Fatal("got nil, want exact tagged match")
		}
		if mi.ContextWindow != 222 {
			t.Fatalf("ContextWindow = %d, want 222 (exact tagged); got %d means the tag-stripped fallback fired before the exact prefixed match", mi.ContextWindow, mi.ContextWindow)
		}
	})

	t.Run("tag-stripped prefixed key when exact tagged misses", func(t *testing.T) {
		// Only the untagged base exists in the catalog. A user-supplied
		// "llama3.1:8b" must fall back to "ollama/llama3.1".
		lookup := fake(map[string]int{"ollama/llama3.1": 333})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "llama3.1:8b")
		if mi == nil || mi.ContextWindow != 333 {
			t.Fatalf("got %+v, want tag-stripped fallback to ollama/llama3.1", mi)
		}
	})

	t.Run("returns nil when nothing matches", func(t *testing.T) {
		lookup := fake(map[string]int{"unrelated/model": 1})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "nope:9999b")
		if mi != nil {
			t.Fatalf("got %+v, want nil", mi)
		}
	})

	t.Run("model without colon does not attempt tag-stripped lookup", func(t *testing.T) {
		// Sanity: an untagged miss must not fall through to a fictional
		// stripped form. We use a bare-key catalog that would otherwise
		// match the tag-stripped key if the third branch fired.
		lookup := fake(map[string]int{"ollama/": 999})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "nonexistent")
		if mi != nil {
			t.Fatalf("got %+v, want nil — there is no tag to strip", mi)
		}
	})

	t.Run("ollama does not bare-fall-back to unrelated provider entries", func(t *testing.T) {
		// Real-world hazard: the catalog has bare anthropic entries
		// ("claude-3-haiku-20240307": 200000). Asking for that name
		// under ollama must NOT pick up Anthropic's metadata — the
		// 200K window would silently mask Ollama context truncation.
		lookup := fake(map[string]int{
			"claude-3-haiku-20240307": 200000, // Anthropic's bare entry
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "ollama", "claude-3-haiku-20240307")
		if mi != nil {
			t.Fatalf("got %+v, want nil — bare-key fallback must be disabled for ollama", mi)
		}
	})

	t.Run("openrouter still uses bare-key fallback for upstream models", func(t *testing.T) {
		// OpenRouter routes to upstreams whose catalog entries are
		// often only stored under their bare upstream key (no
		// "openrouter/..." prefix). For example, requesting
		// "openrouter + minimax/minimax-m2.7" must hit the bare
		// "minimax/minimax-m2.7" entry. The prefixed-first precedence
		// still protects against the overlap case (covered separately
		// by the deepseek subtest above).
		lookup := fake(map[string]int{
			"minimax/minimax-m2.7": 204800,
		})
		mi := resolveOpenAICompatCatalogModel(lookup, "openrouter", "minimax/minimax-m2.7")
		if mi == nil {
			t.Fatal("got nil, want bare upstream match")
		}
		if mi.ContextWindow != 204800 {
			t.Fatalf("ContextWindow = %d, want 204800 (upstream bare entry)", mi.ContextWindow)
		}
	})

	t.Run("kimi still uses bare-key fallback", func(t *testing.T) {
		// Sanity: providers whose catalog keys are unprefixed (kimi,
		// glm) must still hit the bare lookup.
		lookup := fake(map[string]int{"kimi-k2.5": 100})
		mi := resolveOpenAICompatCatalogModel(lookup, "kimi", "kimi-k2.5")
		if mi == nil || mi.ContextWindow != 100 {
			t.Fatalf("got %+v, want kimi-k2.5 bare match", mi)
		}
	})
}

// TestResolveOpenRouterAnthropicWebSearch verifies the three-step
// resolution precedence used by newOpenRouterAnthropicProfile.
// Step 1 (openrouter-prefixed) and step 2 (bare-direct, only when step
// 1 misses) are authoritative; step 3 (bare-upstream-stripped) is a
// fallback that only fills when no earlier step resolved the field.
//
// Particularly important: step 3 must NOT overwrite an authoritative
// step 2 result, even if step 2's matched entry happened to omit the
// field. Built against a fake catalog so all branches can be exercised
// directly — the real catalog doesn't currently contain a model where
// every relevant key exists with diverging values.
func TestResolveOpenRouterAnthropicWebSearch(t *testing.T) {
	tt := func(t *testing.T, name string, entries map[string]*bool, presentEntries map[string]bool, model string, wantWS bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			lookup := func(key string) *llm.ModelInfo {
				if _, present := presentEntries[key]; !present {
					return nil
				}
				ws := entries[key]
				return &llm.ModelInfo{ID: key, SupportsWebSearch: ws}
			}
			got := resolveOpenRouterAnthropicWebSearch(lookup, model, true)
			if got != wantWS {
				t.Fatalf("got %v, want %v", got, wantWS)
			}
		})
	}

	bTrue, bFalse := true, false

	// Step 1 wins when the openrouter-prefixed entry has an explicit value.
	tt(t, "step 1 explicit false wins over later steps",
		map[string]*bool{"openrouter/anthropic/m": &bFalse, "anthropic/m": &bTrue, "m": &bTrue},
		map[string]bool{"openrouter/anthropic/m": true, "anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 2 wins when no openrouter-prefixed entry exists but a
	// bare-direct entry does. Step 3 stripped upstream must NOT
	// overwrite step 2's authoritative answer — this was the bug.
	tt(t, "step 2 explicit false wins over step 3 explicit true",
		map[string]*bool{"anthropic/m": &bFalse, "m": &bTrue},
		map[string]bool{"anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 3 fills when steps 1 and 2 are silent (no entries match).
	tt(t, "step 3 fills when steps 1 and 2 silent",
		map[string]*bool{"m": &bTrue},
		map[string]bool{"m": true},
		"anthropic/m", true)

	// Step 3 fills when step 2 matched but its entry has no field —
	// useful for picking up serf overrides on bare upstream IDs.
	tt(t, "step 3 fills when step 2 matched but field absent",
		map[string]*bool{"anthropic/m": nil, "m": &bFalse},
		map[string]bool{"anthropic/m": true, "m": true},
		"anthropic/m", false)

	// Step 3 fills when step 1's prefixed entry matched but has no field.
	tt(t, "step 3 fills when step 1 matched but field absent",
		map[string]*bool{"openrouter/anthropic/m": nil, "m": &bFalse},
		map[string]bool{"openrouter/anthropic/m": true, "m": true},
		"anthropic/m", false)

	// All silent → caller default wins.
	tt(t, "default wins when nothing matches",
		map[string]*bool{}, map[string]bool{},
		"anthropic/m", true)
}
