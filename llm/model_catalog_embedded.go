package llm

import (
	"embed"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// datedModelSuffix matches an Anthropic-style dated snapshot suffix
// (e.g. "-20251101") plus an optional LiteLLM version tag ("-v1") at the end of
// a model ID. Stripping it yields the bare family ID the Evener overrides key on.
var datedModelSuffix = regexp.MustCompile(`-\d{8}(-v\d+)?$`)

// familyModelID strips a trailing dated-snapshot suffix from a model ID, yielding
// the bare family ID (e.g. "claude-opus-4-5-20251101-v1" → "claude-opus-4-5").
// IDs without a dated suffix are returned unchanged.
func familyModelID(modelID string) string {
	return datedModelSuffix.ReplaceAllString(modelID, "")
}

// claudeCatalogFamilyID returns the bare Anthropic family ID for a catalog key
// whose serving path is encoded in the ID. The returned ID is used only to find
// Evener capability overlays; the catalog entry itself remains the lookup
// result, so serving-path pricing and limits are not replaced with direct-API
// metadata.
//
// LiteLLM uses several provider-specific spellings for the same Anthropic
// family. Keep this allowlist narrow: a model name containing "claude" is not
// enough to prove that it uses the Anthropic Messages request contract.
func claudeCatalogFamilyID(modelID string) (string, bool) {
	id := strings.TrimSuffix(strings.TrimSpace(modelID), "[1m]")

	pathQualified := strings.HasPrefix(id, "vertex_ai/") ||
		strings.HasPrefix(id, "azure_ai/") ||
		strings.HasPrefix(id, "bedrock/") ||
		strings.HasPrefix(id, "anthropic/") ||
		strings.HasPrefix(id, "openrouter/anthropic/") ||
		strings.HasPrefix(id, "perplexity/anthropic/")
	if pathQualified {
		id = id[strings.LastIndex(id, "/")+1:]
	}
	for _, prefix := range []string{
		"anthropic.",
		"us.anthropic.",
		"eu.anthropic.",
		"au.anthropic.",
		"jp.anthropic.",
		"global.anthropic.",
		"apac.anthropic.",
		"us-gov.anthropic.",
	} {
		if stripped, ok := strings.CutPrefix(id, prefix); ok {
			id = stripped
			break
		}
	}

	if i := strings.IndexByte(id, '@'); i >= 0 {
		id = id[:i]
	}
	id = strings.TrimSuffix(id, ":0")
	id = strings.TrimSuffix(id, "-v1")
	if !strings.HasPrefix(id, "claude-") {
		return "", false
	}
	return familyModelID(id), true
}

//go:embed data/litellm_model_catalog.json data/evener_model_catalog_overrides.json
var embeddedCatalogFS embed.FS

var (
	embeddedCatalog     *ModelCatalog
	embeddedCatalogOnce sync.Once
)

// EmbeddedModelCatalog returns the bundled model catalog. The catalog is loaded
// lazily on first access from the embedded LiteLLM data file, with Evener-specific
// overrides merged on top.
// Returns nil if loading fails (should not happen with a valid build).
func EmbeddedModelCatalog() *ModelCatalog {
	embeddedCatalogOnce.Do(func() {
		embeddedCatalog = loadEmbeddedModelCatalog(embeddedCatalogFS.ReadFile, parseLiteLLMCatalog)
	})
	return embeddedCatalog
}

func loadEmbeddedModelCatalog(
	readFile func(string) ([]byte, error),
	parse func([]byte) (*ModelCatalog, error),
) *ModelCatalog {
	data, err := readFile("data/litellm_model_catalog.json")
	if err != nil {
		return nil
	}
	cat, _ := parse(data)
	if cat == nil {
		return nil
	}
	overrideData, err := readFile("data/evener_model_catalog_overrides.json")
	if err != nil {
		return cat // Overrides file missing is not fatal.
	}
	applyOverrides(cat, overrideData)
	return cat
}

// applyOverrides merges Evener-specific model metadata on top of the base catalog.
// An override entry that matches an existing model (by exact ID, dated-family ID
// for snapshots, or a supported provider-qualified Claude family) overlays it.
// A qualified family inherits capability fields from its bare override while
// retaining its own serving metadata. An override entry that matches no model
// and carries base metadata (a context window) materializes a Evener-only
// catalog entry — this is how Evener ships models LiteLLM doesn't cover, e.g.
// kimi-for-coding. Overlay-only entries that match nothing (and the "_comment"
// key) are no-ops.
func applyOverrides(cat *ModelCatalog, data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	applied := make(map[string]bool, len(raw))
	for i := range cat.Models {
		m := &cat.Models[i]
		entry, ok := raw[m.ID]
		includeAliases := ok
		key := m.ID
		if !ok {
			// Dated snapshots (claude-opus-4-5-20251101[-v1]) and supported
			// provider-qualified Claude IDs carry no override of their own; inherit
			// the bare family override so capability metadata applies to those IDs
			// too. An exact match above always wins.
			candidates := make([]string, 0, 2)
			if fam := familyModelID(m.ID); fam != m.ID {
				candidates = append(candidates, fam)
			}
			if fam, qualified := claudeCatalogFamilyID(m.ID); qualified && fam != m.ID {
				if !slices.Contains(candidates, fam) {
					candidates = append(candidates, fam)
				}
			}
			for _, candidate := range candidates {
				entry, ok = raw[candidate]
				if ok {
					key = candidate
					break
				}
			}
			if !ok {
				continue
			}
		}
		ov, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		applyOverlayFields(m, ov, includeAliases)
		applied[key] = true
	}

	// Materialize Evener-only models: override keys that matched no existing model
	// and carry base metadata (a context window).
	for id, entry := range raw {
		if applied[id] {
			continue
		}
		ov, ok := entry.(map[string]any)
		if !ok {
			continue // e.g. the "_comment" string key
		}
		cw := overrideInt(ov["context_window"])
		if cw <= 0 {
			continue // overlay-only entry; nothing to materialize
		}
		m := ModelInfo{ID: id, ContextWindow: cw}
		if s, ok := ov["provider"].(string); ok {
			m.Provider = normalizeCatalogProvider(s)
		}
		if s, ok := ov["display_name"].(string); ok {
			m.DisplayName = s
		}
		if v, ok := ov["supports_reasoning"].(bool); ok {
			m.SupportsReasoning = v
		}
		if v, ok := ov["supports_tools"].(bool); ok {
			m.SupportsTools = v
		}
		applyOverlayFields(&m, ov, true)
		cat.Models = append(cat.Models, m)
	}

	// The byID index is built lazily (and exactly once) on first lookup, after
	// these override mutations land, so there is nothing to invalidate here.
}

// applyOverlayFields applies Evener overlay metadata onto a model. Aliases are
// included only for exact matches and materialized entries so family overlays
// cannot make dated snapshots claim the same alias.
func applyOverlayFields(m *ModelInfo, ov map[string]any, includeAliases bool) {
	if levels, ok := ov["reasoning_effort_levels"].([]any); ok {
		m.ReasoningEffortLevels = nil
		for _, lvl := range levels {
			if s, ok := lvl.(string); ok {
				m.ReasoningEffortLevels = append(m.ReasoningEffortLevels, s)
			}
		}
	}
	if v, ok := ov["supports_adaptive_thinking"].(bool); ok {
		m.SupportsAdaptiveThinking = v
	}
	if v, ok := ov["supports_effort_parameter"].(bool); ok {
		m.SupportsEffortParameter = v
	}
	if v, ok := ov["thinking_always_on"].(bool); ok {
		m.ThinkingAlwaysOn = v
	}
	if v, ok := ov["default_reasoning_effort"].(string); ok {
		m.DefaultReasoningEffort = NormalizeReasoningEffort(v)
	}
	if v, ok := ov["claude5_request_shape"].(bool); ok {
		m.Claude5RequestShape = v
	}
	if v, ok := ov["supports_vision"].(bool); ok {
		m.SupportsVision = v
	}
	if v, ok := ov["supports_reasoning"].(bool); ok {
		m.SupportsReasoning = v
		m.ReasoningAuthoritative = true
	}
	if v, ok := ov["supports_tools"].(bool); ok {
		m.SupportsTools = v
	}
	if v := overrideInt(ov["max_output_tokens"]); v > 0 {
		mo := v
		m.MaxOutputTokens = &mo
	}
	if v := overrideInt(ov["context_window"]); v > 0 {
		// Matched upstream models take the override's window too — the
		// overrides layer always wins, so upstream later adding one of our
		// materialized models can't regress its curated shape.
		m.ContextWindow = v
	}
	if v, ok := ov["supports_web_search"].(bool); ok {
		b := v
		m.SupportsWebSearch = &b
	}
	if v, ok := ov["input_cost_per_million"].(float64); ok {
		c := v
		m.InputCostPerMillion = &c
	}
	if v, ok := ov["output_cost_per_million"].(float64); ok {
		c := v
		m.OutputCostPerMillion = &c
	}
	if v, ok := ov["cache_read_input_cost_per_million"].(float64); ok {
		c := v
		m.CacheReadInputCostPerMillion = &c
	}
	if aliases, ok := ov["aliases"].([]any); ok && includeAliases {
		m.Aliases = nil
		for _, a := range aliases {
			if s, ok := a.(string); ok {
				m.Aliases = append(m.Aliases, s)
			}
		}
	}
}

// overrideInt coerces a JSON-decoded number (float64 from encoding/json) to int.
func overrideInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
