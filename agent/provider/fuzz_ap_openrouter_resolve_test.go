package provider

import (
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzApOpenRouterAnthropicResolve drives the two catalog-precedence resolvers
// for openrouter-anthropic profiles — resolveOpenRouterAnthropicWebSearch and
// resolveOpenRouterAnthropicCtxAndEfforts. Both run the same three-step
// lookup precedence (prefixed "openrouter/<model>" authoritative, bare-direct
// only when the prefixed key misses, bare-upstream-stripped as a last-resort
// fill) over the embedded model catalog. Only fixed-fixture unit tests reached
// them; the precedence arms were otherwise unfuzzed.
//
// The fuzzer owns the model ref and a synthetic three-entry catalog (one entry
// per lookup key the resolvers can query). The lookup closure is pure — no
// catalog file, no network.
//
// Oracles (beyond never-panic):
//   - determinism: each resolver returns identical results on a second call.
//   - web-search precedence: a present prefixed entry with an explicit
//     SupportsWebSearch value is authoritative (result equals it); if no
//     consulted entry sets SupportsWebSearch, the default is returned verbatim.
//   - context precedence: a present prefixed entry with ContextWindow>0 wins;
//     ctx is only ever assigned positive catalog values, so a positive default
//     never degrades to zero.
//   - fresh-copy invariant: the returned efforts slice is a copy, not aliased
//     to internal catalog state (mutating it cannot change a re-resolve).
func FuzzApOpenRouterAnthropicResolve(f *testing.F) {
	// model, prefixed-flags, bare-flags, stripped-flags, and a ctx per key.
	seeds := []struct {
		model            string
		pf, bf, sf       uint8
		pctx, bctx, sctx int32
	}{
		{"anthropic/claude-opus", 0b0000_0011, 0, 0, 200000, 0, 0}, // prefixed present, ws=true
		{"anthropic/claude-opus", 0b0000_0101, 0, 0, 0, 0, 0},      // prefixed present, ws=false
		{"anthropic/claude-opus", 0, 0b0000_0011, 0, 0, 128000, 0}, // bare present only
		{"anthropic/claude-opus", 0, 0, 0b0001_0011, 0, 0, 400000}, // stripped present, efforts=2
		{"claude-sonnet", 0, 0b0000_0001, 0, 0, 0, 0},              // bare present, ws silent
		{"", 0, 0, 0, 0, 0, 0},                                     // empty model, empty catalog
		{"a/b/c", 0b0010_0001, 0b0000_0001, 0b0000_0001, 64000, 32000, 16000},
	}
	for _, s := range seeds {
		f.Add(s.model, s.pf, s.bf, s.sf, s.pctx, s.bctx, s.sctx)
	}

	f.Fuzz(func(t *testing.T, model string, pf, bf, sf uint8, pctx, bctx, sctx int32) {
		// Derive the three keys the resolvers may query.
		prefixedKey := "openrouter/" + model
		bareKey := model
		var strippedKey string
		if _, after, hasSlash := strings.Cut(model, "/"); hasSlash && after != "" {
			strippedKey = after
		}

		catalog := map[string]*llm.ModelInfo{}
		addEntry(catalog, prefixedKey, pf, pctx)
		addEntry(catalog, bareKey, bf, bctx)
		if strippedKey != "" {
			addEntry(catalog, strippedKey, sf, sctx)
		}
		lookup := func(key string) *llm.ModelInfo { return catalog[key] }

		const defaultWS = true
		const defaultCtx = 100000
		defaultEfforts := []string{"low", "high"}

		ws := resolveOpenRouterAnthropicWebSearch(lookup, model, defaultWS)
		if ws2 := resolveOpenRouterAnthropicWebSearch(lookup, model, defaultWS); ws2 != ws {
			t.Fatalf("web-search resolver non-deterministic: %v vs %v", ws, ws2)
		}

		// Prefixed entry with an explicit web-search value is authoritative.
		if pfx := catalog[prefixedKey]; pfx != nil && pfx.SupportsWebSearch != nil {
			if ws != *pfx.SupportsWebSearch {
				t.Fatalf("prefixed ws=%v not authoritative: got %v", *pfx.SupportsWebSearch, ws)
			}
		} else if !anyWebSearchSet(catalog, prefixedKey, bareKey, strippedKey) {
			// No consulted entry sets web search → default returned verbatim.
			if ws != defaultWS {
				t.Fatalf("no ws source but result=%v, want default %v", ws, defaultWS)
			}
		}

		ctx, efforts := resolveOpenRouterAnthropicCtxAndEfforts(lookup, model, defaultCtx, defaultEfforts)
		ctx2, efforts2 := resolveOpenRouterAnthropicCtxAndEfforts(lookup, model, defaultCtx, defaultEfforts)
		if ctx2 != ctx || !equalStrings(efforts, efforts2) {
			t.Fatalf("ctx/efforts resolver non-deterministic")
		}

		// A positive default context window never degrades to zero, since ctx
		// is only ever reassigned to a positive catalog value.
		if ctx <= 0 {
			t.Fatalf("ctx=%d, positive default %d must never degrade to <=0", ctx, defaultCtx)
		}

		// Prefixed entry with ContextWindow>0 wins.
		if pfx := catalog[prefixedKey]; pfx != nil && pfx.ContextWindow > 0 {
			if ctx != pfx.ContextWindow {
				t.Fatalf("prefixed ctx=%d not authoritative: got %d", pfx.ContextWindow, ctx)
			}
		}

		// No-catalog-aliasing invariant: mutating the returned efforts slice must
		// not reach into any catalog entry's ReasoningEffortLevels. (When the
		// default is passed through untouched the caller owns that slice, so we
		// only assert the catalog stays immutable through the return value.)
		if len(efforts) > 0 {
			efforts[0] = "\x00MUTATED"
			for key, mi := range catalog {
				if len(mi.ReasoningEffortLevels) > 0 && mi.ReasoningEffortLevels[0] == "\x00MUTATED" {
					t.Fatalf("returned efforts aliases catalog entry %q — mutation leaked into catalog", key)
				}
			}
		}
	})
}

// addEntry inserts a synthetic ModelInfo into the catalog when the present bit
// (bit0) of flags is set. Flags layout: bit0 present; bits1-2 web-search mode
// (0 silent/nil, 1 true, 2 false); bits3-5 reasoning-effort-level count.
func addEntry(catalog map[string]*llm.ModelInfo, key string, flags uint8, ctx int32) {
	if key == "" || flags&0b1 == 0 {
		return
	}
	mi := &llm.ModelInfo{ID: key, ContextWindow: int(ctx)}
	switch (flags >> 1) & 0b11 {
	case 1:
		v := true
		mi.SupportsWebSearch = &v
	case 2:
		v := false
		mi.SupportsWebSearch = &v
	}
	effN := int((flags >> 3) & 0b111)
	for i := 0; i < effN; i++ {
		mi.ReasoningEffortLevels = append(mi.ReasoningEffortLevels, "e"+string(rune('a'+i)))
	}
	catalog[key] = mi
}

// anyWebSearchSet reports whether any of the named catalog entries has an
// explicit (non-nil) SupportsWebSearch value.
func anyWebSearchSet(catalog map[string]*llm.ModelInfo, keys ...string) bool {
	for _, k := range keys {
		if k == "" {
			continue
		}
		if mi := catalog[k]; mi != nil && mi.SupportsWebSearch != nil {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
