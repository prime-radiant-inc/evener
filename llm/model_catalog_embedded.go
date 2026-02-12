package llm

import (
	"embed"
	"sync"
)

//go:embed data/litellm_model_catalog.json
var embeddedCatalogFS embed.FS

var (
	embeddedCatalog     *ModelCatalog
	embeddedCatalogOnce sync.Once
)

// EmbeddedModelCatalog returns the bundled model catalog. The catalog is loaded
// lazily on first access from the embedded LiteLLM data file.
// Returns nil if loading fails (should not happen with a valid build).
func EmbeddedModelCatalog() *ModelCatalog {
	embeddedCatalogOnce.Do(func() {
		data, err := embeddedCatalogFS.ReadFile("data/litellm_model_catalog.json")
		if err != nil {
			return
		}
		embeddedCatalog, _ = parseLiteLLMCatalog(data)
	})
	return embeddedCatalog
}
