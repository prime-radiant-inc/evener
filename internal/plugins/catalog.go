package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type CatalogOwner struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
}

type CatalogPlugin struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Category    string       `json:"category,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Author      CatalogOwner `json:"author,omitempty"`
	Source      Source       `json:"source"`
}

type Catalog struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Owner       CatalogOwner    `json:"owner,omitempty"`
	Metadata    catalogMetadata `json:"metadata,omitempty"`
	Plugins     []CatalogPlugin `json:"plugins"`
}

type catalogMetadata struct {
	PluginRoot string `json:"pluginRoot,omitempty"`
}

// ParseCatalog reads <marketplaceRoot>/.claude-plugin/marketplace.json.
func ParseCatalog(marketplaceRoot string) (Catalog, error) {
	p := filepath.Join(marketplaceRoot, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return Catalog{}, fmt.Errorf("reading %s: %w", p, err)
	}
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return Catalog{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	return c, nil
}

// Browse returns the parsed catalog of a registered marketplace.
func (m *Manager) Browse(name string) (Catalog, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return Catalog{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return Catalog{}, fmt.Errorf("marketplace %q not found", name)
	}
	return ParseCatalog(ref.InstallLocation)
}
