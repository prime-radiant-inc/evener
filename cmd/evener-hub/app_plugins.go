package hub

import (
	"context"
	"errors"
	"fmt"
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
// goroutines too. Manager does hold one other piece of mutable state — the
// current lock session's accumulated StoreChanged (store_changed.go) — but
// that already guards itself with its own mutex (storeChangedMu, paths.go),
// so a controller mutex here would add nothing but contention: it would be
// held across the manager's own blocking (up to 30s) lock acquisition,
// serializing otherwise-independent mutations (e.g. two unrelated
// marketplaces) behind whichever one is slowest.
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
func (c *hubPluginsController) ListMarketplaces(ctx context.Context) (appwire.MarketplaceListResponse, error) {
	return c.listMarketplaces(ctx)
}

func (c *hubPluginsController) listMarketplaces(ctx context.Context) (appwire.MarketplaceListResponse, error) {
	mk, err := c.mgr.ListMarketplaces(ctx)
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
		errors.Is(err, plugins.ErrMarketplaceSourceInStore),
		errors.Is(err, plugins.ErrMarketplaceSourceUnsupported):
		return appwire.InvalidParams(err.Error())
	}
	return err
}

// AddMarketplace registers a new marketplace and returns the updated list.
// Its refusals — a source inside the store's own directories, and a fetched
// catalog whose name the store cannot carry — are classified like the edit
// path's identical ones, by marketplaceRefusalToWire.
func (c *hubPluginsController) AddMarketplace(ctx context.Context, params appwire.MarketplaceAddParams) (appwire.MarketplaceListResponse, error) {
	if _, err := c.mgr.AddMarketplace(ctx, params.Name, marketplaceSourceFromWire(params.Source)); err != nil {
		return appwire.MarketplaceListResponse{}, marketplaceRefusalToWire(err)
	}
	return c.listMarketplaces(ctx)
}

// hubPluginsReconcileAfterCloneLitter runs immediately before RemoveMarketplace's
// litter path re-lists the marketplaces to build its WireError's Data.Applied -
// a no-op in production, and a seam for a test to break that specific read
// (e.g. a permission change) without touching RemoveMarketplace's own
// already-successful unregister-then-clone-cleanup, matching
// credentialWriteBetween's and the navigation service's between-step hooks.
var hubPluginsReconcileAfterCloneLitter = func() {}

// hubPluginsReconcileAfterAppliedRemove runs immediately before
// RemoveMarketplace's success path re-lists the marketplaces to build its
// response - a no-op in production, and a seam for a test to break that
// specific read (e.g. a permission change) without touching
// RemoveMarketplace's own already-successful unregister-then-clone-cleanup,
// matching hubPluginsReconcileAfterCloneLitter's shape.
var hubPluginsReconcileAfterAppliedRemove = func() {}

// RemoveMarketplace unregisters a marketplace and returns the updated list.
// Its one refusal, an unknown name, is classified by marketplaceRefusalToWire.
// A clone-removal failure after the unregister has already landed is not
// that refusal: the marketplace is already gone, so folding it into the same
// plain-error path would read as "removal failed" when it applied, and a
// retry would then land on ErrMarketplaceNotFound instead of ever surfacing
// the litter. Its own WireError carries the updated list in Data.Applied
// instead - the shape ErrorKeybindingsPostRename established for an
// applied-then-a-durable-step-fails outcome - so the caller reconciles from
// Applied instead of retrying. Re-listing to build Applied is itself a fresh
// read that can fail on its own account (a fault landing in the narrow window
// after RemoveMarketplace already returned), unrelated to whether the
// removal applied; that must never drop the typed outcome back to a plain
// error indistinguishable from an ordinary failure, so it sets
// Data.AppliedUnavailable instead and keeps the same WireError shape. The
// discarded read's own text is logged to the hub's stderr - the same
// server-side diagnostic the success path's failed re-list uses (#1951) - so
// the secondary failure leaves a trace. The success path's own re-list can
// fail the same way, after an unregister and a
// clone cleanup that both completed; it too must never read as "removal
// failed" (a retry would land on ErrMarketplaceNotFound), so it answers with
// its own post-apply outcome, ErrorMarketplaceRemoveApplied - the same
// reconcile-don't-retry rule minus the litter warning, which had no cause
// here. Binding that outcome in a consumer is deliberately deferred to the
// marketplace reconciliation successors (#1954 SDK, #1960 web); until one
// lands, a client treats the unclassified error as a failed removal.
func (c *hubPluginsController) RemoveMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
	if err := c.mgr.RemoveMarketplace(ctx, params.Name); err != nil {
		if errors.Is(err, plugins.ErrMarketplaceUnregisteredCloneRemains) {
			data := appwire.MarketplaceUnregisteredCloneRemainsData{
				EvenerErrorInfo: appwire.ErrorMarketplaceUnregisteredCloneRemains,
			}
			hubPluginsReconcileAfterCloneLitter()
			if applied, listErr := c.listMarketplaces(ctx); listErr != nil {
				// The read failure must leave a diagnostic behind: the wire
				// error deliberately keeps only the path-scrubbed
				// clone-litter text, so without this line the discarded
				// listErr would vanish - the same server-side log the
				// success path's failed re-list already uses.
				fmt.Fprintf(os.Stderr, "[hub] marketplace %q: reading the updated list after unregistration failed: %v\n", params.Name, listErr)
				data.AppliedUnavailable = true
			} else {
				data.Applied = applied
			}
			return appwire.MarketplaceListResponse{}, appwire.WireError{
				Code:    appwire.CodeInternalError,
				Message: err.Error(),
				Data:    data,
			}
		}
		return appwire.MarketplaceListResponse{}, marketplaceRefusalToWire(err)
	}
	// The unregister save landed and the clone cleanup that follows it
	// completed, so the removal APPLIED. Reading the updated list is a fresh
	// read that can fail on its own account (a fault landing in the narrow
	// window after RemoveMarketplace already returned), unrelated to whether
	// the removal applied; folding it into the plain-error path would read as
	// "removal failed" when it applied, and a retry would then land on
	// ErrMarketplaceNotFound instead of ever surfacing this read failure. Its
	// own WireError carries the same post-apply distinction as the litter
	// branch's, but as its own discriminator - nothing is left on disk here,
	// so a client must not warn about leftover clone files. The read failure's
	// own text can carry this machine's absolute plugin-store path
	// (marketplaceReadFile's *fs.PathError names it), so it goes to the hub's
	// log instead of the client-facing message, the split cloneRemovalFailed
	// keeps for the litter branch's own cause.
	hubPluginsReconcileAfterAppliedRemove()
	applied, listErr := c.listMarketplaces(ctx)
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "[hub] marketplace %q: reading the updated list after removal failed: %v\n", params.Name, listErr)
		return appwire.MarketplaceListResponse{}, appwire.WireError{
			Code:    appwire.CodeInternalError,
			Message: fmt.Sprintf("marketplace %q: removed, but the updated list could not be read", params.Name),
			Data: appwire.MarketplaceRemoveAppliedData{
				EvenerErrorInfo:    appwire.ErrorMarketplaceRemoveApplied,
				AppliedUnavailable: true,
			},
		}
	}
	return applied, nil
}

// RefreshMarketplace pulls a marketplace's latest catalog and returns the
// updated list. Its refusals are classified like RemoveMarketplace's; a fetch
// that failed is not a refusal and stays the hub's plain error.
func (c *hubPluginsController) RefreshMarketplace(ctx context.Context, params appwire.MarketplaceNameParams) (appwire.MarketplaceListResponse, error) {
	if err := c.mgr.RefreshMarketplace(ctx, params.Name); err != nil {
		return appwire.MarketplaceListResponse{}, marketplaceRefusalToWire(err)
	}
	return c.listMarketplaces(ctx)
}

// EditMarketplace renames a marketplace and/or replaces its source and
// returns the updated list. Its refusals are classified by
// marketplaceRefusalToWire.
func (c *hubPluginsController) EditMarketplace(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, error) {
	var src *plugins.Source
	if params.Source != nil {
		converted := marketplaceSourceFromWire(*params.Source)
		src = &converted
	}
	if _, err := c.mgr.EditMarketplace(ctx, params.Name, params.NewName, src); err != nil {
		return appwire.MarketplaceListResponse{}, marketplaceRefusalToWire(err)
	}
	return c.listMarketplaces(ctx)
}

// Browse returns a marketplace's plugin catalog. Like ListMarketplaces, this
// is a read (the manager may lazily fetch an unfetched marketplace pointer,
// but that is serialized by the manager's own flock). Its one refusal, an
// unknown name, is classified like RemoveMarketplace's, by
// marketplaceRefusalToWire; a fetch that failed is not a refusal.
func (c *hubPluginsController) Browse(ctx context.Context, params appwire.MarketplaceBrowseParams) (appwire.MarketplaceBrowseResponse, error) {
	cat, err := c.mgr.Browse(ctx, params.Name)
	if err != nil {
		return appwire.MarketplaceBrowseResponse{}, marketplaceRefusalToWire(err)
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
func (c *hubPluginsController) ListPlugins(ctx context.Context) (appwire.PluginListResponse, error) {
	return c.listPlugins(ctx)
}

func (c *hubPluginsController) listPlugins(ctx context.Context) (appwire.PluginListResponse, error) {
	items, err := c.mgr.List(ctx)
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
	return c.listPlugins(ctx)
}

// Upgrade re-resolves an installed plugin against its marketplace and returns
// the updated list.
func (c *hubPluginsController) Upgrade(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if _, err := c.mgr.Upgrade(ctx, params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins(ctx)
}

// Remove deletes an installed plugin's registry entry (and cache dir, if any)
// and returns the updated list.
func (c *hubPluginsController) Remove(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.Remove(ctx, params.Plugin, params.Marketplace); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins(ctx)
}

// Enable flips an installed plugin's enabled flag on and returns the updated
// list.
func (c *hubPluginsController) Enable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, true); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins(ctx)
}

// Disable flips an installed plugin's enabled flag off and returns the
// updated list.
func (c *hubPluginsController) Disable(ctx context.Context, params appwire.PluginRefParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetEnabled(ctx, params.Plugin, params.Marketplace, false); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins(ctx)
}

// SetAutoUpgrade flips an installed plugin's auto-upgrade flag and returns
// the updated list.
func (c *hubPluginsController) SetAutoUpgrade(ctx context.Context, params appwire.PluginSetAutoUpgradeParams) (appwire.PluginListResponse, error) {
	if err := c.mgr.SetAutoUpgrade(ctx, params.Plugin, params.Marketplace, params.AutoUpgrade); err != nil {
		return appwire.PluginListResponse{}, err
	}
	return c.listPlugins(ctx)
}
