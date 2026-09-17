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
// lazy-fetch) takes one of the manager's own flocks for the whole
// read-modify-atomic-write sequence — the store lock for the registry, the
// marketplaces and the plugin cache, and the bundled cache's own lock for
// readying <Root>/bundled — and flock serializes by open-file-description
// rather than by process, so it correctly serializes concurrent in-process
// goroutines too — Manager itself holds no other mutable state a
// controller-level mutex could protect. A controller mutex here would add
// nothing but contention: it would be held across the manager's own blocking
// (up to 30s) lock acquisition, serializing otherwise-independent mutations
// (e.g. two unrelated marketplaces) behind whichever one is slowest.
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
// It reads manifests and registry state, and for a requested bundled plugin it
// prepares the store the way a launch does: it creates <Root>/bundled when that
// is missing, stages a marked copy there, reads it, and removes it before
// returning. It publishes nothing and reclaims nothing. Plugin hooks, MCP
// commands, and session state are never touched, and it fails the way a launch
// would on a store neither can write.
func (c *hubPluginsController) Preview(ctx context.Context, params appwire.PluginPreviewParams) (appwire.PluginPreviewResponse, error) {
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
	resolution, err := c.mgr.PreviewForLaunch(ctx, resolved.Effective.PluginDirs, resolved.Effective.EnabledPlugins)
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
//
// The second return is the manager's own StoreChanges: a pure read can still
// persist a legacy-name migration behind the scenes (lockStore runs it on
// every acquisition), which changes both stores, so the caller broadcasts
// whichever of evener/marketplace/updated and evener/plugin/updated it names
// even though this call asked for neither change.
func (c *hubPluginsController) ListMarketplaces(ctx context.Context) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	return c.listMarketplaces(ctx)
}

func (c *hubPluginsController) listMarketplaces(ctx context.Context) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	mk, changes, err := c.mgr.ListMarketplaces(ctx)
	if err != nil {
		return appwire.MarketplaceListResponse{}, changes, err
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
	return appwire.MarketplaceListResponse{Marketplaces: entries}, changes, nil
}

// marketplaceRefusalToWire turns the manager's marketplace sentinels into the
// wire's own refusal classes, so the same refusal reads the same way whichever
// mutation raised it — a taken name is the caller's Conflict, whether another
// marketplace holds it or a removed one's leftovers still occupy it, and an
// unknown name, a name the store cannot carry, and a source inside the plugin
// store's own directories their InvalidParams. Anything else — a fetch
// failure, a rename the filesystem refused — stays the hub's plain error.
func marketplaceRefusalToWire(err error) error {
	switch {
	case errors.Is(err, plugins.ErrMarketplaceExists):
		return appwire.Conflict(err.Error())
	case errors.Is(err, plugins.ErrMarketplaceNotFound), errors.Is(err, plugins.ErrInvalidName),
		errors.Is(err, plugins.ErrMarketplaceSourceInStore):
		return appwire.InvalidParams(err.Error())
	}
	return err
}

// marketplaceWrite runs a marketplace-mutating manager call and answers with
// the refreshed listing - the marketplace analogue of instanceWrite in
// app_rpc.go. A refusal reports its wire class and changes nothing; a write
// that applied (or one whose listing failed) answers the way
// marketplaceListAfterWrite already does, wrapped in hubcore.ErrWriteApplied
// - marketplaceRefusalToWire only reclassifies the manager's refusal
// sentinels, so that survives it. changes is apply's own answer: every
// caller's underlying manager call takes the store lock itself, so a
// migration landing during THIS call's own acquisition is covered here, not
// just by ListAfterWrite's separate (and separately discarded) read.
func (c *hubPluginsController) marketplaceWrite(ctx context.Context, apply func() (plugins.StoreChanges, error)) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	changes, err := apply()
	if err != nil {
		return appwire.MarketplaceListResponse{}, changes, marketplaceRefusalToWire(err)
	}
	resp, err := c.marketplaceListAfterWrite(ctx)
	return resp, changes, err
}

// AddMarketplace registers a new marketplace and returns the updated list.
// Its refusals — a source inside the store's own directories, and a fetched
// catalog whose name the store cannot carry — are classified like the edit
// path's identical ones, by marketplaceRefusalToWire.
func (c *hubPluginsController) AddMarketplace(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	return c.marketplaceWrite(ctx, func() (plugins.StoreChanges, error) {
		_, changes, err := c.mgr.AddMarketplace(ctx, params.Name, marketplaceSourceFromWire(params.Source))
		return changes, err
	})
}

// RemoveMarketplace unregisters a marketplace and returns the updated list.
// Its one refusal, an unknown name, is classified by marketplaceRefusalToWire.
func (c *hubPluginsController) RemoveMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	return c.marketplaceWrite(ctx, func() (plugins.StoreChanges, error) { return c.mgr.RemoveMarketplace(ctx, params.Name) })
}

// RefreshMarketplace pulls a marketplace's latest catalog and returns the
// updated list. Its refusals are classified like RemoveMarketplace's; a fetch
// that failed is not a refusal and stays the hub's plain error.
func (c *hubPluginsController) RefreshMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	return c.marketplaceWrite(ctx, func() (plugins.StoreChanges, error) { return c.mgr.RefreshMarketplace(ctx, params.Name) })
}

// EditMarketplace renames a marketplace and/or replaces its source and
// returns the updated list. Its refusals are classified by
// marketplaceRefusalToWire.
func (c *hubPluginsController) EditMarketplace(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, plugins.StoreChanges, error) {
	var src *plugins.Source
	if params.Source != nil {
		converted := marketplaceSourceFromWire(*params.Source)
		src = &converted
	}
	return c.marketplaceWrite(ctx, func() (plugins.StoreChanges, error) {
		_, changes, err := c.mgr.EditMarketplace(ctx, params.Name, params.NewName, src)
		return changes, err
	})
}

// Browse returns a marketplace's plugin catalog. Like ListMarketplaces, this
// is a read (the manager may lazily fetch an unfetched marketplace pointer,
// but that is serialized by the manager's own flock). Its one refusal, an
// unknown name, is classified like RemoveMarketplace's, by
// marketplaceRefusalToWire; a fetch that failed is not a refusal.
//
// The second return is the manager's own StoreChanges, including on the
// rollback failure a save can hit after a lazy fetch: the clone still landed
// on disk, so the caller broadcasts whichever store it names, independent of
// this call's own error.
func (c *hubPluginsController) Browse(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, plugins.StoreChanges, error) {
	cat, changes, err := c.mgr.Browse(ctx, params.Name)
	if err != nil {
		return appwire.MarketplaceBrowseResponse{}, changes, marketplaceRefusalToWire(err)
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
	return appwire.MarketplaceBrowseResponse{Name: cat.Name, Description: cat.Description, Plugins: entries}, changes, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Plugins
// ─────────────────────────────────────────────────────────────────────────────

// ListPlugins returns every installed plugin. The second return is the
// manager's own StoreChanges - see ListMarketplaces for what a caller owes
// when it names a change.
func (c *hubPluginsController) ListPlugins(ctx context.Context) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	return c.listPlugins(ctx)
}

func (c *hubPluginsController) listPlugins(ctx context.Context) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	items, changes, err := c.mgr.List(ctx)
	if err != nil {
		return appwire.PluginListResponse{}, changes, err
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
	return appwire.PluginListResponse{Plugins: entries}, changes, nil
}

// pluginWriteBetween runs after a plugin or marketplace write has applied and
// before the listing that answers it. It is the one point a test can reach to
// break that listing: a manager whose write succeeds and whose next read fails
// cannot be produced from outside the call. Mirrors credentialWriteBetween.
var pluginWriteBetween = func() {}

// pluginListAfterWrite and marketplaceListAfterWrite answer a write that has
// applied. The listing is a read of what the write just did, so its failure
// does not unapply anything: the caller is told what failed, and the error
// says the write stands so the handler still broadcasts (#1572).
func (c *hubPluginsController) pluginListAfterWrite(ctx context.Context) (appwire.PluginListResponse, error) {
	pluginWriteBetween()
	// changes is discarded: the write this answers already owns its own
	// applied-write broadcast below, which refetches the same listing a
	// concurrent migration would also have changed.
	resp, _, err := c.listPlugins(ctx)
	return resp, writeApplied(err)
}

func (c *hubPluginsController) marketplaceListAfterWrite(ctx context.Context) (appwire.MarketplaceListResponse, error) {
	pluginWriteBetween()
	resp, _, err := c.listMarketplaces(ctx)
	return resp, writeApplied(err)
}

// pluginWrite is marketplaceWrite's plugin-listing sibling, for the
// evener/plugin/* mutations. changes is apply's own answer, for the same
// reason marketplaceWrite's is: every caller's underlying manager call takes
// the store lock itself, so a migration landing during THIS call's own
// acquisition is covered here.
func (c *hubPluginsController) pluginWrite(ctx context.Context, apply func() (plugins.StoreChanges, error)) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	changes, err := apply()
	if err != nil {
		return appwire.PluginListResponse{}, changes, marketplaceRefusalToWire(err)
	}
	resp, err := c.pluginListAfterWrite(ctx)
	return resp, changes, err
}

// Install installs a plugin from a marketplace's catalog and returns the
// updated list, plus the manager's own StoreChanges (a lazy fetch's backfill,
// or lockStore's migration, persisted during this call) - a different shape
// than pluginWrite's other callers (a second return, not just a listing and
// an error), so it stays inline. The caller broadcasts whichever store it
// names, regardless of this call's own error.
func (c *hubPluginsController) Install(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	_, changes, err := c.mgr.Install(ctx, params.Plugin, params.Marketplace)
	if err != nil {
		return appwire.PluginListResponse{}, changes, marketplaceRefusalToWire(err)
	}
	resp, err := c.pluginListAfterWrite(ctx)
	return resp, changes, err
}

// Upgrade re-resolves an installed plugin against its marketplace and returns
// the updated list. See Install for the second return.
func (c *hubPluginsController) Upgrade(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	_, changes, err := c.mgr.Upgrade(ctx, params.Plugin, params.Marketplace)
	if err != nil {
		return appwire.PluginListResponse{}, changes, marketplaceRefusalToWire(err)
	}
	resp, err := c.pluginListAfterWrite(ctx)
	return resp, changes, err
}

// Remove deletes an installed plugin's registry entry (and cache dir, if any)
// and returns the updated list.
func (c *hubPluginsController) Remove(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	return c.pluginWrite(ctx, func() (plugins.StoreChanges, error) { return c.mgr.Remove(ctx, params.Plugin, params.Marketplace) })
}

// Enable flips an installed plugin's enabled flag on and returns the updated
// list.
func (c *hubPluginsController) Enable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	return c.pluginWrite(ctx, func() (plugins.StoreChanges, error) {
		return c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, true)
	})
}

// Disable flips an installed plugin's enabled flag off and returns the
// updated list.
func (c *hubPluginsController) Disable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	return c.pluginWrite(ctx, func() (plugins.StoreChanges, error) {
		return c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, false)
	})
}

// SetAutoUpgrade flips an installed plugin's auto-upgrade flag and returns
// the updated list.
func (c *hubPluginsController) SetAutoUpgrade(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, plugins.StoreChanges, error) {
	return c.pluginWrite(ctx, func() (plugins.StoreChanges, error) {
		return c.mgr.SetAutoUpgrade(ctx, params.Plugin, params.Marketplace, params.AutoUpgrade)
	})
}
