package llm

import (
	"embed"
	"encoding/json"
	"sync"
)

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
			continue
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
	}

	// Rebuild index after modifications.
	cat.byID = nil
}
