package main

import (
	"context"
	"sort"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/internal/plugins"
)

// hubPluginsController manages marketplace and plugin lifecycle CRUD,
// delegating all on-disk state to internal/plugins.Manager (reload -> mutate
// -> atomic write, under the manager's own flock — the marketplace/plugin
// analogue of hubInstancesController's providers.toml handling).
//
// Unlike hubInstancesController, this controller holds no mutex of its own.
// hubInstancesController's mu is load-bearing: providers.toml is read and
// written with no OS-level lock, so an in-process mutex is the only thing
// preventing a lost update between concurrent Create/Edit/Remove/SetDefault
// calls. plugins.Manager is different: every mutating call (and Browse's
// lazy-fetch) takes the manager's own flock on a single per-root lock file
// (m.lockPath()) for the whole read-modify-atomic-write sequence, and flock
// serializes by open-file-description rather than by process, so it
// correctly serializes concurrent in-process goroutines too — Manager itself
// holds no other mutable state a controller-level mutex could protect. A
// controller mutex here would add nothing but contention: it would be held
// across the manager's own blocking (up to 30s) lock acquisition, serializing
// otherwise-independent mutations (e.g. two unrelated marketplaces) behind
// whichever one is slowest.
type hubPluginsController struct {
	mgr *plugins.Manager
}

// newHubPluginsController builds a controller rooted at root, or the default
// (~/.config/serf/plugins, honoring XDG_CONFIG_HOME) when root == "".
func newHubPluginsController(root string) *hubPluginsController {
	return &hubPluginsController{mgr: plugins.NewManager(root)}
}

func marketplaceSourceFromWire(in appwire.MarketplaceSourceInput) plugins.Source {
	return plugins.Source{
		Kind: plugins.SourceKind(in.Kind),
		Repo: in.Repo,
		URL:  in.URL,
		Path: in.Path,
		Ref:  in.Ref,
		Sha:  in.Sha,
	}
}

func marketplaceSourceToWire(src plugins.Source) appwire.MarketplaceSourceInput {
	return appwire.MarketplaceSourceInput{
		Kind: string(src.Kind),
		Repo: src.Repo,
		URL:  src.URL,
		Path: src.Path,
		Ref:  src.Ref,
		Sha:  src.Sha,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Marketplaces
// ─────────────────────────────────────────────────────────────────────────────

// ListMarketplaces returns every registered marketplace, sorted by name: a
// single consistent read off the manager (matching hubInstancesController.List).
func (c *hubPluginsController) ListMarketplaces() (appwire.MarketplaceListResponse, error) {
	return c.listMarketplaces()
}

func (c *hubPluginsController) listMarketplaces() (appwire.MarketplaceListResponse, error) {
	mk, err := c.mgr.ListMarketplaces()
	if err != nil {
		return appwire.MarketplaceListResponse{}, err
	}
	names := make([]string, 0, len(mk))
	for name := range mk {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]appwire.MarketplaceEntry, 0, len(names))
	for _, name := range names {
		ref := mk[name]
		entries = append(entries, appwire.MarketplaceEntry{
			Name:            name,
			Source:          marketplaceSourceToWire(ref.Source),
			InstallLocation: ref.InstallLocation,
			LastUpdated:     hubcore.UnixSeconds(ref.LastUpdated),
		})
	}
	return appwire.MarketplaceListResponse{Marketplaces: entries}, nil
}

// AddMarketplace registers a new marketplace and returns the updated list.
func (c *hubPluginsController) AddMarketplace(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
	if _, err := c.mgr.AddMarketplace(ctx, params.Name, marketplaceSourceFromWire(params.Source)); err != nil {
		return appwire.MarketplaceListResponse{}, err
	}
	return c.listMarketplaces()
}

// RemoveMarketplace unregisters a marketplace and returns the updated list.
func (c *hubPluginsController) RemoveMarketplace(params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
	if err := c.mgr.RemoveMarketplace(params.Name); err != nil {
		return appwire.MarketplaceListResponse{}, err
	}
	return c.listMarketplaces()
}

// RefreshMarketplace pulls a marketplace's latest catalog and returns the
// updated list.
func (c *hubPluginsController) RefreshMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
	if err := c.mgr.RefreshMarketplace(ctx, params.Name); err != nil {
		return appwire.MarketplaceListResponse{}, err
	}
	return c.listMarketplaces()
}

// Browse returns a marketplace's plugin catalog. Like ListMarketplaces, this
// is a read (the manager may lazily fetch an unfetched marketplace pointer,
// but that is serialized by the manager's own flock).
func (c *hubPluginsController) Browse(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
	cat, err := c.mgr.Browse(ctx, params.Name)
	if err != nil {
		return appwire.MarketplaceBrowseResponse{}, err
	}
	entries := make([]appwire.MarketplaceCatalogPlugin, 0, len(cat.Plugins))
	for _, p := range cat.Plugins {
		entries = append(entries, appwire.MarketplaceCatalogPlugin{
			Name:        p.Name,
			Description: p.Description,
			Category:    p.Category,
			Homepage:    p.Homepage,
			Author:      p.Author.Name,
		})
	}
	return appwire.MarketplaceBrowseResponse{Name: cat.Name, Description: cat.Description, Plugins: entries}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Plugins
// ─────────────────────────────────────────────────────────────────────────────

// ListPlugins returns every installed plugin (see ListMarketplaces).
func (c *hubPluginsController) ListPlugins() (appwire.PluginListResponse, error) {
	return c.listPlugins()
}

func (c *hubPluginsController) listPlugins() (appwire.PluginListResponse, error) {
	items, err := c.mgr.List()
	if err != nil {
		return appwire.PluginListResponse{}, err
	}
	entries := make([]appwire.PluginEntry, 0, len(items))
	for _, it := range items {
		entries = append(entries, appwire.PluginEntry{
			Plugin:       it.Plugin,
			Marketplace:  it.Marketplace,
			Version:      it.Version,
			Enabled:      it.Enabled,
			AutoUpgrade:  it.AutoUpgrade,
			Broken:       it.Broken,
			InstallPath:  it.InstallPath,
			GitCommitSha: it.GitCommitSha,
			InstalledAt:  hubcore.UnixSeconds(it.InstalledAt),
			LastUpdated:  hubcore.UnixSeconds(it.LastUpdated),
		})
	}
	return appwire.PluginListResponse{Plugins: entries}, nil
}

// Install installs a plugin from a marketplace's catalog and returns the
// updated list.
func (c *hubPluginsController) Install(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if _, err := c.mgr.Install(ctx, params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Upgrade re-resolves an installed plugin against its marketplace and returns
// the updated list.
func (c *hubPluginsController) Upgrade(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if _, err := c.mgr.Upgrade(ctx, params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Remove deletes an installed plugin's registry entry (and cache dir, if any)
// and returns the updated list.
func (c *hubPluginsController) Remove(params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.Remove(params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Enable flips an installed plugin's enabled flag on and returns the updated
// list.
func (c *hubPluginsController) Enable(params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(params.Plugin, params.Marketplace, true); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Disable flips an installed plugin's enabled flag off and returns the
// updated list.
func (c *hubPluginsController) Disable(params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(params.Plugin, params.Marketplace, false); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// SetAutoUpgrade flips an installed plugin's auto-upgrade flag and returns
// the updated list.
func (c *hubPluginsController) SetAutoUpgrade(params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetAutoUpgrade(params.Plugin, params.Marketplace, params.AutoUpgrade); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}
