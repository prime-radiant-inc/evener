package llm

import (
	"embed"
	"encoding/json"
	"regexp"
	"sync"
)

// datedModelSuffix matches an Anthropic-style dated snapshot suffix
// (e.g. "-20251101") plus an optional LiteLLM version tag ("-v1") at the end of
// a model ID. Stripping it yields the bare family ID the Serf overrides key on.
var datedModelSuffix = regexp.MustCompile(`-\d{8}(-v\d+)?$`)

// familyModelID strips a trailing dated-snapshot suffix from a model ID, yielding
// the bare family ID (e.g. "claude-opus-4-5-20251101-v1" → "claude-opus-4-5").
// IDs without a dated suffix are returned unchanged.
func familyModelID(modelID string) string {
	return datedModelSuffix.ReplaceAllString(modelID, "")
}

//go:embed data/litellm_model_catalog.json data/serf_model_catalog_overrides.json
var embeddedCatalogFS embed.FS

var (
	embeddedCatalog     *ModelCatalog
	embeddedCatalogOnce sync.Once
)

// EmbeddedModelCatalog returns the bundled model catalog. The catalog is loaded
// lazily on first access from the embedded LiteLLM data file, with Serf-specific
// overrides merged on top.
// Returns nil if loading fails (should not happen with a valid build).
func EmbeddedModelCatalog() *ModelCatalog {
	embeddedCatalogOnce.Do(func() {
		data, err := embeddedCatalogFS.ReadFile("data/litellm_model_catalog.json")
		if err != nil {
			return
		}
		embeddedCatalog, _ = parseLiteLLMCatalog(data)
		if embeddedCatalog == nil {
			return
		}

		// Merge Serf-specific overrides.
		overrideData, err := embeddedCatalogFS.ReadFile("data/serf_model_catalog_overrides.json")
		if err != nil {
			return // Overrides file missing is not fatal.
		}
		applyOverrides(embeddedCatalog, overrideData)
	})
	return embeddedCatalog
}

// applyOverrides merges Serf-specific model metadata on top of the base catalog.
// An override entry that matches an existing model (by exact ID, or by dated-family
// ID for dated snapshots) overlays it. An override entry that matches no model and
// carries base metadata (a context window) materializes a Serf-only catalog entry —
// this is how Serf ships models LiteLLM doesn't cover, e.g. kimi-for-coding.
// Overlay-only entries that match nothing (and the "_comment" key) are no-ops.
func applyOverrides(cat *ModelCatalog, data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	applied := make(map[string]bool, len(raw))
	for i := range cat.Models {
		m := &cat.Models[i]
		entry, ok := raw[m.ID]
		key := m.ID
		if !ok {
			// Dated snapshots (claude-opus-4-5-20251101[-v1]) carry no override of
			// their own; inherit the bare family override so effort metadata applies
			// to dated IDs too. An exact match above always wins.
			if fam := familyModelID(m.ID); fam != m.ID {
				entry, ok = raw[fam]
				key = fam
			}
			if !ok {
				continue
			}
		}
		ov, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		applyOverlayFields(m, ov)
		applied[key] = true
	}

	// Materialize Serf-only models: override keys that matched no existing model
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
		applyOverlayFields(&m, ov)
		cat.Models = append(cat.Models, m)
	}

	// The byID index is built lazily (and exactly once) on first lookup, after
	// these override mutations land, so there is nothing to invalidate here.
}

// applyOverlayFields applies the Serf overlay fields (effort levels, capability
// flags) from an override entry onto a model.
func applyOverlayFields(m *ModelInfo, ov map[string]any) {
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
	if v := overrideInt(ov["max_output_tokens"]); v > 0 {
		mo := v
		m.MaxOutputTokens = &mo
	}
	if v, ok := ov["supports_web_search"].(bool); ok {
		b := v
		m.SupportsWebSearch = &b
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
