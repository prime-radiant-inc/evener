package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
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

// Browse returns the parsed catalog of a registered marketplace, lazily
// cloning it first if it was only seeded as an unfetched pointer.
func (m *Manager) Browse(ctx context.Context, name string) (Catalog, error) {
	release, err := acquireLock(m.lockPath(), 30*time.Second)
	if err != nil {
		return Catalog{}, err
	}
	defer release()
	ref, err := m.ensureFetched(ctx, name)
	if err != nil {
		return Catalog{}, err
	}
	return ParseCatalog(m.catalogRoot(ref))
}
