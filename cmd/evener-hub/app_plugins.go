package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/plugins"
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
	mgr              *plugins.Manager
	launchConfigRoot string
}

// newHubPluginsController builds a controller rooted at root, or the default
// (~/.config/evener/plugins, honoring XDG_CONFIG_HOME) when root == "".
func newHubPluginsController(root string, launchConfigRoots ...string) *hubPluginsController {
	launchConfigRoot := ""
	if len(launchConfigRoots) > 0 {
		launchConfigRoot = launchConfigRoots[0]
	}
	return &hubPluginsController{mgr: plugins.NewManager(root), launchConfigRoot: launchConfigRoot}
}

// Preview resolves the same launch plugin inventory used by session startup.
// It only reads manifests and registry state; plugin hooks, MCP commands, and
// session state are never touched.
func (c *hubPluginsController) Preview(ctx context.Context, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
	_ = ctx // retained in the controller API for parity with other RPC reads
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	var resolved launchconfig.Resolved
	if strings.TrimSpace(params.CWD) == "" {
		// No launch directory chosen yet: the user-level inventory (global
		// layer + per-launch overrides) is all that exists. Repo and project
		// layers resolve once a directory is picked, and clients re-preview
		// then.
		userResolved, err := launchconfig.ResolveUserOnly(c.launchConfigRoot, overrides)
		if err != nil {
			return appwire.PluginPreviewResponse{}, err
		}
		resolved = userResolved
	} else {
		cwd, project, cleanup, err := pluginPreviewCWD(params.CWD)
		if err != nil {
			return appwire.PluginPreviewResponse{}, err
		}
		defer cleanup()
		fullResolved, err := launchconfig.ResolveWithProject(c.launchConfigRoot, cwd, project, overrides)
		if err != nil {
			return appwire.PluginPreviewResponse{}, err
		}
		resolved = fullResolved
	}
	resolution, err := c.mgr.PreviewForLaunch(resolved.Effective.PluginDirs, resolved.Effective.EnabledPlugins)
	if err != nil {
		return appwire.PluginPreviewResponse{}, err
	}
	resp := appwire.PluginPreviewResponse{
		Plugins:         make([]appwire.PluginLaunchCandidate, 0, len(resolution.Candidates)),
		Diagnostics:     make([]appwire.PluginDiagnostic, 0, len(resolution.Diagnostics)),
		SelectionErrors: make([]appwire.PluginSelectionError, 0, len(resolution.SelectionErrors)),
	}
	for _, candidate := range resolution.Candidates {
		resp.Plugins = append(resp.Plugins, appwire.PluginLaunchCandidate{
			Name: candidate.Name, Version: candidate.Version, Description: candidate.Description,
			Source: string(candidate.Source), Marketplace: candidate.Marketplace, Path: candidate.Path,
			Selected: candidate.Selected, SkillCount: candidate.SkillCount, AgentCount: candidate.AgentCount,
			CommandCount: candidate.CommandCount, HookCount: candidate.HookCount, MCPCount: candidate.MCPCount,
		})
	}
	for _, diagnostic := range resolution.Diagnostics {
		resp.Diagnostics = append(resp.Diagnostics, appwire.PluginDiagnostic{
			Name: diagnostic.Name, Path: diagnostic.Path, Source: string(diagnostic.Source), Message: diagnostic.Message,
		})
	}
	for _, selectionErr := range resolution.SelectionErrors {
		resp.SelectionErrors = append(resp.SelectionErrors, appwire.PluginSelectionError{Name: selectionErr.Name, Reason: selectionErr.Reason})
	}
	return resp, nil
}

func pluginPreviewCWD(path string) (string, identifier.Project, func(), error) {
	cwd, err := fspaths.CanonicalizeDir(path)
	if err == nil {
		project, projectErr := identifier.ResolveProject(cwd)
		if projectErr != nil {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + projectErr.Error())
		}
		return cwd, project, func() {}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + err.Error())
	}

	// launchconfig.Resolve needs an existing directory to derive project
	// identity. Put the temporary resolver directory under the nearest existing
	// ancestor so project identity follows the eventual target, while the
	// target itself and its ancestor's local files remain untouched.
	requested := filepath.Clean(strings.TrimSpace(path))
	ancestor := requested
	for {
		info, statErr := os.Stat(ancestor)
		if statErr == nil {
			if !info.IsDir() {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: nearest existing path is not a directory")
			}
			existingAncestor := ancestor
			ancestor, statErr = fspaths.CanonicalizeDir(ancestor)
			if statErr != nil {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + statErr.Error())
			}
			missingSuffix, relErr := filepath.Rel(existingAncestor, requested)
			if relErr != nil {
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + relErr.Error())
			}
			requestedCanonical := filepath.Join(ancestor, missingSuffix)
			previewDir, mkdirErr := os.MkdirTemp(ancestor, "evener-plugin-preview-")
			if mkdirErr != nil {
				return "", identifier.Project{}, nil, mkdirErr
			}
			probeProject, projectErr := identifier.ResolveProject(previewDir)
			if projectErr != nil {
				_ = os.RemoveAll(previewDir)
				return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + projectErr.Error())
			}
			project := probeProject
			if probeProject.CanonicalPath == previewDir {
				project = identifier.ProjectFromCanonicalPath(requestedCanonical)
			}
			return previewDir, project, func() { _ = os.RemoveAll(previewDir) }, nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: " + statErr.Error())
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", identifier.Project{}, nil, appwire.InvalidParams("cwd: no existing ancestor")
		}
		ancestor = parent
	}
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
func (c *hubPluginsController) RemoveMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
	if err := c.mgr.RemoveMarketplace(ctx, params.Name); err != nil {
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
func (c *hubPluginsController) Remove(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.Remove(ctx, params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Enable flips an installed plugin's enabled flag on and returns the updated
// list.
func (c *hubPluginsController) Enable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, true); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// Disable flips an installed plugin's enabled flag off and returns the
// updated list.
func (c *hubPluginsController) Disable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, false); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}

// SetAutoUpgrade flips an installed plugin's auto-upgrade flag and returns
// the updated list.
func (c *hubPluginsController) SetAutoUpgrade(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetAutoUpgrade(ctx, params.Plugin, params.Marketplace, params.AutoUpgrade); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins()
}
