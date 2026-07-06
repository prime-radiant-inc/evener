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

	// The following mirror Claude Code's marketplace-entry manifest fields
	// (https://code.claude.com/docs/en/plugin-marketplaces#plugin-entries):
	// same names and JSON shapes as agent/plugin.Manifest's same-named
	// fields, so an entry can be dropped straight into a Manifest value (see
	// ensureManifestFallback, manifest_fallback.go). They are used only as a
	// fallback when the plugin's own source has no plugin.json; a plugin
	// that ships its own manifest is unchanged and these are ignored.
	Commands   json.RawMessage `json:"commands,omitempty"`
	Agents     json.RawMessage `json:"agents,omitempty"`
	Hooks      json.RawMessage `json:"hooks,omitempty"`
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
	// Skills is parsed for schema completeness but NOT currently honored:
	// agent/plugin.Manifest has no Skills override field (a plugin's
	// skills/ directory is always scanned by default, manifest or not), so
	// a marketplace entry's custom skill paths are not applied by the v1
	// fallback — only the plugin's own default skills/ directory, if it has
	// one, is picked up. See the plan's Global Constraints for why.
	Skills json.RawMessage `json:"skills,omitempty"`
	// Strict mirrors Claude Code's `strict` marketplace-entry field
	// (https://code.claude.com/docs/en/plugin-marketplaces#strict-mode):
	// default true means plugin.json is the authority and the entry only
	// supplements it; false means the entry is the plugin's entire
	// definition (and a co-existing plugin.json's components would
	// conflict). v1's fallback triggers purely on "no plugin.json exists"
	// and does not read Strict — see the plan's Global Constraints for the
	// full rationale. Captured here for round-trip and future
	// strict:false-conflict/merge work.
	Strict *bool `json:"strict,omitempty"`
}

// HasManifestFields reports whether the marketplace entry declares at least
// one manifest-fallback component (commands/agents/hooks/mcpServers) —
// ensureManifestFallback's signal for whether a manifest-less plugin has
// anything usable to synthesize a plugin.json from. Skills is deliberately
// excluded: the fallback does not honor it (see the Skills field's doc), so
// a skills-only entry has nothing this mechanism can act on.
func (cp CatalogPlugin) HasManifestFields() bool {
	return len(cp.Commands) > 0 || len(cp.Agents) > 0 || len(cp.Hooks) > 0 || len(cp.MCPServers) > 0
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
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
	SkippedPlugins []string `json:"skippedPlugins,omitempty"`
}

type catalogMetadata struct {
	// serf:naming-ignore: matches Claude Code plugin/marketplace JSON schema
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
