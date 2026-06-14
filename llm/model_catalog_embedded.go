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
func applyOverrides(cat *ModelCatalog, data []byte) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}

	for i := range cat.Models {
		m := &cat.Models[i]
		entry, ok := raw[m.ID]
		if !ok {
			// Dated snapshots (claude-opus-4-5-20251101[-v1]) carry no override of
			// their own; inherit the bare family override so effort metadata applies
			// to dated IDs too. An exact match above always wins.
			if fam := familyModelID(m.ID); fam != m.ID {
				entry, ok = raw[fam]
			}
			if !ok {
				continue
			}
		}
		ov, ok := entry.(map[string]any)
		if !ok {
			continue
		}
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
		if v, ok := ov["supports_web_search"].(bool); ok {
			b := v
			m.SupportsWebSearch = &b
		}
	}

	// Rebuild index after modifications.
	cat.byID = nil
}
