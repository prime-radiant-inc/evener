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
	// SkippedPlugins names catalog entries that failed to parse — most
	// commonly an unsupported/unknown Source.Kind, e.g. an npm source or
	// another real Claude Code source type this Source doesn't implement —
	// and were therefore dropped rather than failing the whole catalog. A
	// skipped plugin is simply absent from Plugins (and so not installable);
	// this field exists so Browse/CLI/callers can surface a warning about it.
	SkippedPlugins []string `json:"skippedPlugins,omitempty"`
}

type catalogMetadata struct {
	PluginRoot string `json:"pluginRoot,omitempty"`
}

// catalogHeader decodes everything in marketplace.json except the plugin
// list, which ParseCatalog decodes one entry at a time so a single
// unparseable plugin can be skipped without failing the rest.
type catalogHeader struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Owner       CatalogOwner      `json:"owner,omitempty"`
	Metadata    catalogMetadata   `json:"metadata,omitempty"`
	Plugins     []json.RawMessage `json:"plugins"`
}

// ParseCatalog reads <marketplaceRoot>/.claude-plugin/marketplace.json.
//
// The top-level fields and the plugin list itself must be well-formed JSON —
// a malformed marketplace.json still fails outright. But each element of
// "plugins" is decoded independently: a plugin whose source is an
// unsupported/unknown kind (Source.UnmarshalJSON's only error path) is
// skipped and its name recorded in Catalog.SkippedPlugins instead of failing
// ParseCatalog for the whole marketplace (design spec §7) — every other
// plugin in the marketplace remains browsable and installable.
func ParseCatalog(marketplaceRoot string) (Catalog, error) {
	p := filepath.Join(marketplaceRoot, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return Catalog{}, fmt.Errorf("reading %s: %w", p, err)
	}
	var h catalogHeader
	if err := json.Unmarshal(data, &h); err != nil {
		return Catalog{}, fmt.Errorf("parsing %s: %w", p, err)
	}
	c := Catalog{Name: h.Name, Description: h.Description, Owner: h.Owner, Metadata: h.Metadata}
	for _, raw := range h.Plugins {
		var cp CatalogPlugin
		if err := json.Unmarshal(raw, &cp); err != nil {
			c.SkippedPlugins = append(c.SkippedPlugins, catalogPluginName(raw))
			continue
		}
		c.Plugins = append(c.Plugins, cp)
	}
	return c, nil
}

// catalogPluginName best-effort extracts a plugin catalog entry's "name" so a
// skipped entry can still be named in Catalog.SkippedPlugins even though its
// Source failed to decode. Falls back to "(unknown)" if even that fails.
func catalogPluginName(raw json.RawMessage) string {
	var probe struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Name != "" {
		return probe.Name
	}
	return "(unknown)"
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
