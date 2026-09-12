# Marketplace Sheet Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Marketplace rows open a sheet that edits the marketplace in place (rename, change source) and carries Refresh and Remove, backed by a new `evener/marketplace/edit` method that renames and re-sources a registered marketplace safely.

**Architecture:** `internal/plugins.Manager.EditMarketplace` does the on-disk work under the store lock in a fetch-first order with directory-rename rollback: fetch a changed source into staging, rename the clone and cache directories and re-key `installed_plugins.json`, swap the staged clone in, then save the registry and the marketplaces file. The hub exposes it as one additive v4 method and maps the manager's sentinels to wire error classes. On the frontend, `MarketplacesSection` rows become single tappable buttons, a new `MarketplaceSheet` holds the form plus Refresh and Remove, and `marketplacesPlugins/index.tsx` owns the selection the same way it owns the plugin sheet's.

**Tech Stack:** Go (internal/plugins, appwire, cmd/evener-hub), TypeScript + React + zustand + vitest, `make generate`.

**Spec:** `docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md` §3 (plus §5, §6). This plan is slice 3 of 3; it does not depend on slices 1 or 2, but it follows the design-system rule slice 2 rewrote (a detail sheet is the item's editor).

## Global Constraints

- Repo root for every command: the git worktree you were started in. Frontend commands run from `cmd/evener-hub/frontend`.
- Protocol version stays `evener-appwire-v4`; the new method is additive. Empty `newName` and absent `source` mean unchanged.
- A marketplace name must pass `validNameComponent` (a single non-traversing path component). A taken name is `plugins.ErrMarketplaceExists` → `appwire.Conflict`; an unknown name is `plugins.ErrMarketplaceNotFound` → `appwire.InvalidParams`.
- The manager's order is fixed: fetch new source → rename directories and re-key the registry → swap the clone in → save the registry, then the marketplaces file. Any failure after a directory rename renames back before returning.
- The source picker offers the same three kinds the Add form offers: Git URL (`url`), owner/repo (`github`), Local path (`directory`). A `git-subdir` entry renders its URL under Git URL and is saved back unchanged unless edited.
- No `git add -A`. Commit after every task with the exact message given.
- Frontend: `npx biome check --write <touched files under src/>` before every commit that touches frontend files; gate is `make test-web` from the repo root.
- Go plugin tests that need git call `gitAvailable()` and skip otherwise, like their neighbours; tests that stub a package-level seam (`marketplaceRename`) restore it in `t.Cleanup` and must not call `t.Parallel()`.
- Follow each file's comment style: explain why, never narrate what changed.

---

### Task 1: The manager edits a marketplace

**Files:**
- Modify: `internal/plugins/errors.go` (add `ErrMarketplaceExists`)
- Modify: `internal/plugins/marketplaces.go` (add `EditMarketplace`, `rekeyRegistry`; add `"strings"` to the imports)
- Test: `internal/plugins/marketplaces_test.go`

**Interfaces:**
- Consumes: existing `acquireStoreLock`, `loadMarketplaces`/`saveMarketplaces`, `loadRegistry`/`saveRegistry`, `fetchMarketplaceContainer`, `ParseCatalog`, `swapInClone`, `marketplaceDir`, `cacheDir`, `marketplaceStat`, `marketplaceRename`, `marketplaceRemoveAll`, `registryKey`, `splitKey`, `validNameComponent`, `m.now()`.
- Produces: `plugins.ErrMarketplaceExists`; `(*Manager).EditMarketplace(ctx context.Context, name, newName string, src *Source) (MarketplaceRef, error)`; `rekeyRegistry(reg Registry, oldName, newName, oldCache, newCache string) Registry`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/plugins/marketplaces_test.go`:

```go
// makeMarketplaceRepoWithPlugin builds a git repo whose marketplace.json is
// named name and lists one installable plugin at ./plugins/<plugin>.
func makeMarketplaceRepoWithPlugin(t *testing.T, name, plugin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name+"-"+plugin)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[{"name":"` + plugin + `","source":"./plugins/` + plugin + `"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(dir, "plugins", plugin), plugin, nil)
	makeGitRepo(t, dir, "README.md", "mkt")
	return dir
}

// makeDirectoryMarketplace builds a directory-source marketplace (no git)
// named name with one installable plugin.
func makeDirectoryMarketplace(t *testing.T, name, plugin string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dir-"+name)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[{"name":"` + plugin + `","source":"./plugins/` + plugin + `"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, filepath.Join(dir, "plugins", plugin), plugin, nil)
	return dir
}

func TestEditMarketplace_RenameMovesCloneCacheAndRegistry(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}

	ref, err := m.EditMarketplace(ctx, name, "acme2", nil)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.InstallLocation != m.marketplaceDir("acme2") {
		t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, m.marketplaceDir("acme2"))
	}
	if _, err := os.Stat(m.marketplaceDir("acme2")); err != nil {
		t.Fatalf("renamed clone missing: %v", err)
	}
	if _, err := os.Stat(m.marketplaceDir(name)); !os.IsNotExist(err) {
		t.Fatal("old clone directory survived the rename")
	}
	list, err := m.ListMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list["acme2"]; !ok {
		t.Fatalf("acme2 not registered: %v", list)
	}
	if _, ok := list[name]; ok {
		t.Fatal("the old name is still registered")
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entries, ok := reg.Plugins[registryKey("widget", "acme2")]
	if !ok || len(entries) != 1 {
		t.Fatalf("registry not re-keyed: %v", reg.Plugins)
	}
	if _, still := reg.Plugins[registryKey("widget", name)]; still {
		t.Fatal("the old registry key survived")
	}
	wantPrefix := filepath.Join(m.cacheDir(), "acme2") + string(filepath.Separator)
	if !strings.HasPrefix(entries[0].InstallPath, wantPrefix) {
		t.Fatalf("InstallPath = %q, want it under %q", entries[0].InstallPath, wantPrefix)
	}
	if _, err := os.Stat(entries[0].InstallPath); err != nil {
		t.Fatalf("the re-keyed install path does not exist: %v", err)
	}
	items, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Plugin != "widget" || items[0].Marketplace != "acme2" {
		t.Fatalf("List = %+v", items)
	}
	cat, err := m.Browse(ctx, "acme2")
	if err != nil || len(cat.Plugins) != 1 {
		t.Fatalf("Browse acme2 = %+v, %v", cat, err)
	}
}

func TestEditMarketplace_DirectorySourceRenameKeepsThePath(t *testing.T) {
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	ref, err := m.EditMarketplace(ctx, "acme", "beta", nil)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.InstallLocation != dir {
		t.Fatalf("a directory source is referenced in place; InstallLocation = %q", ref.InstallLocation)
	}
	reg, _ := m.loadRegistry()
	entries, ok := reg.Plugins[registryKey("widget", "beta")]
	if !ok || len(entries) != 1 {
		t.Fatalf("registry not re-keyed: %v", reg.Plugins)
	}
	if _, err := os.Stat(entries[0].InstallPath); err != nil {
		t.Fatalf("install path after rename: %v", err)
	}
}

func TestEditMarketplace_ResourceSwapsTheClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	ref, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.Source.URL != repoB {
		t.Fatalf("Source = %+v, want repoB", ref.Source)
	}
	cat, err := m.Browse(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "gadget" {
		t.Fatalf("catalog after re-source = %+v, want gadget", cat.Plugins)
	}
	if _, err := os.Stat(m.marketplaceDir(".staging")); !os.IsNotExist(err) {
		t.Fatal("staging directory survived")
	}
}

func TestEditMarketplace_RenameAndResourceTogether(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", "acme"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	ref, err := m.EditMarketplace(ctx, "acme", "beta", &Source{Kind: SourceURL, URL: repoB})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.InstallLocation != m.marketplaceDir("beta") || ref.Source.URL != repoB {
		t.Fatalf("ref = %+v", ref)
	}
	list, _ := m.ListMarketplaces()
	if _, ok := list["beta"]; !ok || len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	cat, err := m.Browse(ctx, "beta")
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].Name != "gadget" {
		t.Fatalf("Browse beta = %+v, %v", cat, err)
	}
	reg, _ := m.loadRegistry()
	entries, ok := reg.Plugins[registryKey("widget", "beta")]
	if !ok || len(entries) != 1 {
		t.Fatalf("registry = %v", reg.Plugins)
	}
	if _, err := os.Stat(entries[0].InstallPath); err != nil {
		t.Fatalf("the installed plugin is unaffected by a re-source, but its path is gone: %v", err)
	}
}

func TestEditMarketplace_FetchFailureChangesNothing(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}
	bad := &Source{Kind: SourceURL, URL: filepath.Join(t.TempDir(), "does-not-exist")}
	if _, err := m.EditMarketplace(ctx, name, "beta", bad); err == nil {
		t.Fatal("expected the fetch to fail")
	}
	list, _ := m.ListMarketplaces()
	if _, ok := list[name]; !ok || len(list) != 1 {
		t.Fatalf("list changed after a failed fetch: %v", list)
	}
	if _, err := os.Stat(m.marketplaceDir(name)); err != nil {
		t.Fatalf("the clone moved after a failed fetch: %v", err)
	}
	if _, err := os.Stat(m.marketplaceDir("beta")); !os.IsNotExist(err) {
		t.Fatal("a beta directory appeared")
	}
	reg, _ := m.loadRegistry()
	if _, ok := reg.Plugins[registryKey("widget", name)]; !ok {
		t.Fatalf("registry changed after a failed fetch: %v", reg.Plugins)
	}
	if _, err := os.Stat(m.marketplaceDir(".staging")); !os.IsNotExist(err) {
		t.Fatal("staging directory survived")
	}
}

func TestEditMarketplace_RestoresDirectoriesWhenARenameStepFails(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}

	// The clone directory renames; the cache directory refuses. The undo
	// then runs through the same seam, so only the second call fails.
	orig := marketplaceRename
	calls := 0
	marketplaceRename = func(from, to string) error {
		calls++
		if calls == 2 {
			return errors.New("boom")
		}
		return orig(from, to)
	}
	t.Cleanup(func() { marketplaceRename = orig })

	if _, err := m.EditMarketplace(ctx, name, "beta", nil); err == nil {
		t.Fatal("expected the rename to fail")
	}
	if _, err := os.Stat(m.marketplaceDir(name)); err != nil {
		t.Fatalf("the clone was not restored: %v", err)
	}
	if _, err := os.Stat(m.marketplaceDir("beta")); !os.IsNotExist(err) {
		t.Fatal("a beta clone remained")
	}
	list, _ := m.ListMarketplaces()
	if _, ok := list[name]; !ok || len(list) != 1 {
		t.Fatalf("list changed after a failed rename: %v", list)
	}
	reg, _ := m.loadRegistry()
	if _, ok := reg.Plugins[registryKey("widget", name)]; !ok {
		t.Fatalf("registry changed after a failed rename: %v", reg.Plugins)
	}
}

func TestEditMarketplace_Refusals(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	for _, name := range []string{"acme", "beta"} {
		dir := makeDirectoryMarketplace(t, name, "widget")
		if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
			t.Fatalf("AddMarketplace %s: %v", name, err)
		}
	}
	if _, err := m.EditMarketplace(ctx, "acme", "beta", nil); !errors.Is(err, ErrMarketplaceExists) {
		t.Fatalf("rename onto a taken name = %v, want ErrMarketplaceExists", err)
	}
	if _, err := m.EditMarketplace(ctx, "nope", "x", nil); !errors.Is(err, ErrMarketplaceNotFound) {
		t.Fatalf("unknown marketplace = %v, want ErrMarketplaceNotFound", err)
	}
	if _, err := m.EditMarketplace(ctx, "acme", "../escape", nil); err == nil {
		t.Fatal("a traversing name must be refused")
	}
}

func TestEditMarketplace_NoOpReturnsTheCurrentRef(t *testing.T) {
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	before, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	same := Source{Kind: SourceDirectory, Path: dir}
	after, err := m.EditMarketplace(ctx, "acme", "acme", &same)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if after != before {
		t.Fatalf("a no-op edit changed the ref: %+v → %+v", before, after)
	}
}

func TestRekeyRegistry(t *testing.T) {
	oldCache, newCache := filepath.Join("cache", "acme"), filepath.Join("cache", "beta")
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey("widget", "acme"): {{InstallPath: filepath.Join(oldCache, "widget", "abc")}},
		registryKey("other", "zeta"):  {{InstallPath: filepath.Join("cache", "zeta", "other", "def")}},
		registryKey("elsewhere", "acme"): {{InstallPath: filepath.Join("somewhere", "else")}},
	}}
	got := rekeyRegistry(reg, "acme", "beta", oldCache, newCache)
	if _, still := got.Plugins[registryKey("widget", "acme")]; still {
		t.Fatal("old key survived")
	}
	if p := got.Plugins[registryKey("widget", "beta")][0].InstallPath; p != filepath.Join(newCache, "widget", "abc") {
		t.Fatalf("InstallPath = %q", p)
	}
	if p := got.Plugins[registryKey("other", "zeta")][0].InstallPath; p != filepath.Join("cache", "zeta", "other", "def") {
		t.Fatalf("an unrelated entry changed: %q", p)
	}
	if p := got.Plugins[registryKey("elsewhere", "beta")][0].InstallPath; p != filepath.Join("somewhere", "else") {
		t.Fatalf("a path outside the cache changed: %q", p)
	}
	if got.Version != 2 {
		t.Fatalf("Version = %d", got.Version)
	}
}
```

`strings` and `errors` are already imported by this test file.

Two corrections to the block above, from pre-flight verification:

- **Run the pasted block through gofmt before committing.** `TestRekeyRegistry`'s
  map literal is aligned to only two of its three keys; gofmt realigns all three
  to the longest (`registryKey("elsewhere", "acme"):`), and `make lint-gofmt`
  walks every tracked `.go` file.
- **Do not add a third copy of an existing fixture.**
  `makeInstallableMarketplace` (`internal/plugins/install_test.go:15`) already
  builds exactly `makeMarketplaceRepoWithPlugin(t, "acme", "widget")`. Add the new
  helper, then have `makeInstallableMarketplace` delegate to it and return
  `(dir, "acme")` — but only after checking that its callers depend on nothing
  the delegation changes (the fixture's parent temp-dir path shape in
  particular). If delegating would change any existing test's behavior, leave
  both helpers and say so in your report. `makeMarketplaceRepoDirNoGit`
  (`doctor_test.go:617`) is deliberately plugin-less; leave it alone.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/plugins -run 'TestEditMarketplace|TestRekeyRegistry' 2>&1 | head -8`
Expected: compile errors, `undefined: ErrMarketplaceExists`, `m.EditMarketplace undefined`, `undefined: rekeyRegistry`.

- [ ] **Step 3: Implement**

In `internal/plugins/errors.go`, add to the `var (...)` block:

```go
	ErrMarketplaceExists   = errors.New("marketplace already registered")
```

In `internal/plugins/marketplaces.go`, add `"strings"` to the import block, and after `RemoveMarketplace` add:

```go
// EditMarketplace renames a registered marketplace and/or replaces its
// source (spec 2026-09-07 §3). The order is chosen so the one step that can
// take a long time or fail for reasons outside the store - fetching the new
// source - happens before anything on disk moves, and a failure at any later
// step, the marketplaces file's own save included, puts back everything the
// edit moved: the renamed directories, the re-keyed registry file, and the
// contents of a clone the new source was swapped into - the swap sets the old
// contents aside and they stay there until both files are saved.
//
//  1. fetch a changed source into staging and parse its catalog (Add's own
//     staging discipline: a bad source never half-registers);
//  2. rename the clone directory and the plugin cache directory, and re-key
//     every <plugin>@old registry entry (its install path lives under the
//     renamed cache);
//  3. swap the staged clone into the (possibly renamed) install location, or
//     point a directory source at its path;
//  4. save the installed registry, then the marketplaces file.
//
// A same-name, same-source call is a no-op that returns the current ref.
// A git-backed marketplace's plugins are materialized under the cache; a
// directory-source marketplace's relative plugins are referenced in place
// inside it. A re-source moves neither, beyond the re-key a rename implies.
func (m *Manager) EditMarketplace(ctx context.Context, name, newName string, src *Source) (MarketplaceRef, error) {
	release, err := m.acquireStoreLock(ctx, marketplaceAcquireLock, m.lockPath(), 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	renaming := newName != "" && newName != name
	resourcing := src != nil && *src != ref.Source
	if !renaming && !resourcing {
		return ref, nil
	}
	if renaming {
		if err := validNameComponent("marketplace", newName); err != nil {
			return MarketplaceRef{}, err
		}
		if _, taken := mk[newName]; taken {
			return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", newName, ErrMarketplaceExists)
		}
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return MarketplaceRef{}, err
	}

	// 1. The network step, before anything on disk moves.
	staging := m.marketplaceDir(".staging")
	if resourcing {
		_ = marketplaceRemoveAll(staging)
		root, err := m.fetchMarketplaceContainer(ctx, *src, staging)
		if err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, err
		}
		if _, err := ParseCatalog(root); err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, fmt.Errorf("reading marketplace.json: %w", err)
		}
	}
	// From here until the files are saved, a failure runs undo in reverse
	// and sweeps staging.
	var undo []func()
	fail := func(err error) (MarketplaceRef, error) {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, err
	}

	// 2. Rename on disk and in the registry.
	target := name
	if renaming {
		target = newName
		if ref.Source.Kind != SourceDirectory && ref.InstallLocation != "" {
			oldDir, newDir := m.marketplaceDir(name), m.marketplaceDir(newName)
			if _, err := marketplaceStat(oldDir); err == nil {
				if err := marketplaceRename(oldDir, newDir); err != nil {
					return fail(fmt.Errorf("renaming marketplace clone: %w", err))
				}
				undo = append(undo, func() { _ = marketplaceRename(newDir, oldDir) })
			}
			ref.InstallLocation = newDir
		}
		oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), newName)
		if _, err := marketplaceStat(oldCache); err == nil {
			if err := marketplaceRename(oldCache, newCache); err != nil {
				return fail(fmt.Errorf("renaming plugin cache: %w", err))
			}
			undo = append(undo, func() { _ = marketplaceRename(newCache, oldCache) })
		}
		reg = rekeyRegistry(reg, name, newName, oldCache, newCache)
	}

	// 3. Apply the new source into the install location. An old clone that a
	// directory source makes redundant goes only after the files say so.
	var afterSave []func()
	if resourcing {
		if src.Kind == SourceDirectory {
			if ref.Source.Kind != SourceDirectory {
				clone := m.marketplaceDir(target)
				afterSave = append(afterSave, func() { _ = marketplaceRemoveAll(clone) })
			}
			_ = marketplaceRemoveAll(staging)
			ref.InstallLocation = src.Path
		} else {
			dest := m.marketplaceDir(target)
			if err := m.swapInClone(staging, dest); err != nil {
				return fail(err)
			}
			ref.InstallLocation = dest
		}
		ref.Source = *src
	}
	ref.LastUpdated = m.now().UTC()

	// 4. The registry first: a marketplaces file naming a marketplace whose
	// plugins are still keyed under the old name is the worse of the two
	// half-states, and evener-doctor reports the other one.
	if renaming {
		if err := m.saveRegistry(reg); err != nil {
			return fail(err)
		}
		delete(mk, name)
	}
	mk[target] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if renaming {
			return MarketplaceRef{}, fmt.Errorf("marketplace %q renamed in %s but not in %s: %w", name, registryFileName, marketplacesFileName, err)
		}
		return MarketplaceRef{}, err
	}
	for _, fn := range afterSave {
		fn()
	}
	return ref, nil
}

// rekeyRegistry moves every <plugin>@oldName entry to <plugin>@newName and
// rewrites the install paths that lived under the renamed cache directory;
// entries for other marketplaces, and paths outside the cache, are untouched.
func rekeyRegistry(reg Registry, oldName, newName, oldCache, newCache string) Registry {
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		plugin, marketplace := splitKey(key)
		if marketplace != oldName {
			out.Plugins[key] = entries
			continue
		}
		moved := make([]InstallEntry, 0, len(entries))
		for _, e := range entries {
			rel, err := filepath.Rel(oldCache, e.InstallPath)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				e.InstallPath = filepath.Join(newCache, rel)
			}
			moved = append(moved, e)
		}
		out.Plugins[registryKey(plugin, newName)] = moved
	}
	return out
}
```

If `marketplaceStat` is not a package-level var in this file (check `grep -n marketplaceStat internal/plugins/marketplaces.go`), use `os.Stat` in its place.

- [ ] **Step 4: Run the plugin tests**

Run: `go test ./internal/plugins 2>&1 | tail -4`
Expected: ok.

Then run `go vet -tags evenerfuzz ./internal/plugins 2>&1 | tail -5`. The only
other test that stubs `marketplaceRename` is `fuzzMarketplacesCoverage` in
`internal/plugins/coverage_marketplaces_fuzz_test.go`, which is behind
`//go:build evenerfuzz` and so is invisible to plain `go test` — but CI's
tagged lanes compile it, so the tagged build must stay green.

- [ ] **Step 5: Commit**

```bash
git add internal/plugins/errors.go internal/plugins/marketplaces.go internal/plugins/marketplaces_test.go
git commit -m "feat(plugins): rename and re-source a registered marketplace"
```

---

### Task 2: Wire method, hub controller, and handler

**Files:**
- Modify: `appwire/types.go` (constant beside `MethodEvenerMarketplaceRefresh`; `MarketplaceEditParams` after `MarketplaceAddParams`)
- Modify: `appwire/protocol.go` (catalog entry after the Refresh entry; the two notification doc strings)
- Modify: `cmd/evener-hub/app_plugins.go` (`EditMarketplace`)
- Modify: `cmd/evener-hub/app_rpc.go` (handler after the Refresh handler)
- Test: `cmd/evener-hub/app_plugins_test.go`, `appwire/protocol_test.go`,
  `cmd/evener-hub/app_rpc_test.go` (the registered-handler lock, see Step 4)
- Regenerate: `cmd/evener-hub/frontend/src/protocol/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Produces: `appwire.MethodEvenerMarketplaceEdit = "evener/marketplace/edit"`; `appwire.MarketplaceEditParams{ Name string; NewName string omitempty; Source *MarketplaceSourceInput omitempty }`; `(*hubPluginsController).EditMarketplace(ctx, params) (appwire.MarketplaceListResponse, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `appwire/protocol_test.go`:

```go
func TestMarketplaceEditCatalog(t *testing.T) {
	for _, method := range Methods {
		if method.Name != MethodEvenerMarketplaceEdit {
			continue
		}
		if method.Scope != ScopeHub {
			t.Errorf("scope = %q, want %q", method.Scope, ScopeHub)
		}
		if reflect.TypeOf(method.Params) != reflect.TypeFor[MarketplaceEditParams]() {
			t.Errorf("params type = %T", method.Params)
		}
		if reflect.TypeOf(method.Result) != reflect.TypeFor[MarketplaceListResponse]() {
			t.Errorf("result type = %T", method.Result)
		}
		return
	}
	t.Fatalf("method catalog missing %s", MethodEvenerMarketplaceEdit)
}
```

Append to `cmd/evener-hub/app_plugins_test.go`:

```go
func TestPlugins_Marketplace_EditRenamesAndReturnsTheList(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)

	resp, err := ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "acme2"})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if len(resp.Marketplaces) != 1 || resp.Marketplaces[0].Name != "acme2" {
		t.Fatalf("EditMarketplace response = %+v, want one entry named acme2", resp.Marketplaces)
	}
	if resp.Marketplaces[0].Source.Kind != "directory" || resp.Marketplaces[0].Source.Path != dir {
		t.Fatalf("Source = %+v", resp.Marketplaces[0].Source)
	}
}

func TestPlugins_Marketplace_EditRefusalsAreWireErrors(t *testing.T) {
	ctl := newTestPluginsController(t)
	dir := t.TempDir()
	writeTestMarketplace(t, dir)
	addTestMarketplace(t, ctl, dir)
	other := t.TempDir()
	writeTestMarketplaceManifest(t, other, "beta", "[]")
	if _, err := ctl.AddMarketplace(context.Background(), appwire.MarketplaceAddParams{
		Source: appwire.MarketplaceSourceInput{Kind: "directory", Path: other},
	}); err != nil {
		t.Fatalf("AddMarketplace beta: %v", err)
	}

	_, err := ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "acme", NewName: "beta"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("rename onto a taken name = %v, want a Conflict wire error", err)
	}
	_, err = ctl.EditMarketplace(context.Background(), appwire.MarketplaceEditParams{Name: "nope", NewName: "x"})
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unknown marketplace = %v, want an InvalidParams wire error", err)
	}
}
```

Add `"errors"` to that test file's imports if it is not there.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./appwire -run TestMarketplaceEditCatalog 2>&1 | head -4; go test ./cmd/evener-hub -run 'TestPlugins_Marketplace_Edit' 2>&1 | head -4`
Expected: compile errors on the undefined constant, type, and method.

- [ ] **Step 3: Implement the wire shape and catalog**

In `appwire/types.go`, after `MethodEvenerMarketplaceRefresh`, add:

```go
	MethodEvenerMarketplaceEdit       = "evener/marketplace/edit"
```

After `MarketplaceAddParams`, add:

```go
// MarketplaceEditParams is the params for evener/marketplace/edit (spec
// 2026-09-07 §3). NewName renames the registered marketplace (empty means
// unchanged); Source replaces its source and re-fetches it (absent means
// unchanged). Installed plugins are unaffected beyond being re-keyed under
// the new name.
type MarketplaceEditParams struct {
	Name    string                  `json:"name"`
	NewName string                  `json:"newName,omitempty"`
	Source  *MarketplaceSourceInput `json:"source,omitempty"`
}
```

In `appwire/protocol.go`, after the `MethodEvenerMarketplaceRefresh` catalog line, add:

```go
	{MethodEvenerMarketplaceEdit, MarketplaceEditParams{}, MarketplaceListResponse{}, ScopeHub, "Renames a registered marketplace and/or replaces its source, re-fetching it; returns the updated list and broadcasts evener/marketplace/updated and evener/plugin/updated."},
```

Change the two notification doc strings `(add/remove/refresh)` → `(add/edit/remove/refresh)` and `(install/upgrade/remove/enable/disable/setAutoUpgrade)` → `(install/upgrade/remove/enable/disable/setAutoUpgrade, or a marketplace rename re-keying installs)`.

Those same two enumerations are duplicated outside `protocol.go`. Update every
occurrence under `appwire/` — `grep -rn 'add/remove/refresh\|setAutoUpgrade)' appwire/`
finds them, including `appwire/types.go` around the doc for
`MarketplaceListResponse`, which is this method's own result type. There are
also copies in `cmd/evener-tui/internal/launchconfig/plugins_client.go`: read
them, and update them only if they enumerate which methods *broadcast* the
notification (then they are now wrong); leave them if they enumerate that
client's own calls (then they are still right). Say which you found in your
report.

No `*Client` wrapper: nothing in the tree requires hub methods to have one, and
this method's only callers are the TypeScript in Tasks 3-5, so a wrapper would
be dead code.

- [ ] **Step 4: Implement the controller and handler**

In `cmd/evener-hub/app_plugins.go`, after `RefreshMarketplace`, add:

```go
// EditMarketplace renames a marketplace and/or replaces its source and
// returns the updated list. The manager's sentinels become the wire's own
// refusal classes - a taken name is the caller's Conflict, an unknown name
// their InvalidParams - while a fetch failure or a rename the filesystem
// refused stays the hub's plain error.
func (c *hubPluginsController) EditMarketplace(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, error) {
	var src *plugins.Source
	if params.Source != nil {
		converted := marketplaceSourceFromWire(*params.Source)
		src = &converted
	}
	if _, err := c.mgr.EditMarketplace(ctx, params.Name, params.NewName, src); err != nil {
		switch {
		case errors.Is(err, plugins.ErrMarketplaceExists):
			return appwire.MarketplaceListResponse{}, appwire.Conflict(err.Error())
		case errors.Is(err, plugins.ErrMarketplaceNotFound):
			return appwire.MarketplaceListResponse{}, appwire.InvalidParams(err.Error())
		}
		return appwire.MarketplaceListResponse{}, err
	}
	return c.listMarketplaces()
}
```

In `cmd/evener-hub/app_rpc.go`, after the `MethodEvenerMarketplaceRefresh` handler, add:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerMarketplaceEdit, func(ctx context.Context, params appwire.MarketplaceEditParams) (appwire.MarketplaceListResponse, error) {
		resp, err := pluginsController.EditMarketplace(ctx, params)
		if err == nil {
			// A rename re-keys installed plugins, so both lists refresh.
			notifyMarketplaceUpdated(server)
			notifyPluginUpdated(server)
		}
		return resp, err
	})
```

Then register the method with the two tests that lock the handler set.
`cmd/evener-hub/app_rpc_test.go`'s `TestHubRPCRegistersExpectedHandlerSet` holds
a hard-coded `expected` list — "The list is a lock, not a sample: nothing may be
registered that it does not name" — and a `notDispatched` map that keeps the
marketplace methods from being dispatched against a real plugin root. Add
`appwire.MethodEvenerMarketplaceEdit` to BOTH, each beside its
`MethodEvenerMarketplaceRefresh` neighbour. (`TestHubRouterMatchesCatalog` in
`cmd/evener-hub/appwire_catalog_test.go` is the parity test and needs no edit.)

- [ ] **Step 5: Regenerate and run the tests**

Run: `make generate && go test ./appwire ./internal/appwirets ./cmd/evener-hub 2>&1 | tail -6`
Expected: ok for all three. `git status --short` shows `types.gen.ts` and `docs/appwire-protocol.md` modified; confirm `grep -n "marketplace/edit" cmd/evener-hub/frontend/src/protocol/types.gen.ts` and that the generated params read `newName?: string` and `source?: MarketplaceSourceInput` (optional, never nullable — Tasks 3-5 omit those keys rather than passing null).

- [ ] **Step 6: Commit**

```bash
git add appwire/types.go appwire/protocol.go appwire/protocol_test.go cmd/evener-hub/app_plugins.go cmd/evener-hub/app_plugins_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_rpc_test.go cmd/evener-hub/frontend/src/protocol/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): serve evener/marketplace/edit"
```

(Add `cmd/evener-tui/internal/launchconfig/plugins_client.go` only if its comments needed the enumeration fix.)

---

### Task 3: Store mutation and the form model

**Files:**
- Modify: `cmd/evener-hub/frontend/src/stores/extensions.ts`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.ts`
- Test: `cmd/evener-hub/frontend/src/stores/extensions.test.ts`, `.../marketplacesPlugins/marketplaceEdit.test.ts`

**Interfaces:**
- Produces: `extensionsStore.editMarketplace(params: MarketplaceEditParams): Promise<void>`; `MarketplaceDraft { name: string; kind: "url" | "github" | "directory"; url: string; repo: string; path: string }`; `marketplaceDraftFor(entry: MarketplaceEntry): MarketplaceDraft`; `marketplaceEditParams(entry, draft): MarketplaceEditParams | null`; `MARKETPLACE_SOURCE_OPTIONS`.

- [ ] **Step 1: Write the failing tests**

Append to `src/stores/extensions.test.ts`, after the `refreshMarketplace` describe:

```ts
describe("editMarketplace", () => {
  test("calls evener/marketplace/edit, applies the response, and drops the browse cache for both names", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/browse", () => ({ name: "acme-plugins", description: "", plugins: [] }));
    await extensionsStore.getState().browseMarketplace("acme-plugins");
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(true);

    fake.on("evener/marketplace/edit", (params) => {
      expect(params).toEqual({ name: "acme-plugins", newName: "acme2", source: { kind: "url", url: "https://x/y.git" } });
      return { marketplaces: [{ ...MARKETPLACE_A, name: "acme2", source: { kind: "url", url: "https://x/y.git" } }] };
    });
    await extensionsStore.getState().editMarketplace({
      name: "acme-plugins",
      newName: "acme2",
      source: { kind: "url", url: "https://x/y.git" },
    });
    expect(extensionsStore.getState().marketplaces?.map((m) => m.name)).toEqual(["acme2"]);
    expect(extensionsStore.getState().browseCatalogs.has("acme-plugins")).toBe(false);
    expect(extensionsStore.getState().browseCatalogs.has("acme2")).toBe(false);
  });

  test("a rejection propagates", async () => {
    const fake = connectFakeClient();
    fake.on("evener/marketplace/edit", () => {
      throw new Error("edit failed");
    });
    await expect(extensionsStore.getState().editMarketplace({ name: "acme-plugins", newName: "x" })).rejects.toThrow(
      "edit failed",
    );
  });
});
```

Create `marketplaceEdit.test.ts`:

```ts
// @vitest-environment node
import { describe, expect, test } from "vitest";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import { MARKETPLACE_SOURCE_OPTIONS, marketplaceDraftFor, marketplaceEditParams } from "./marketplaceEdit";

const GITHUB: MarketplaceEntry = { name: "acme", source: { kind: "github", repo: "acme/plugins" }, lastUpdated: 1 };
const URL: MarketplaceEntry = { name: "acme", source: { kind: "url", url: "https://x/y.git" }, lastUpdated: 1 };
const DIR: MarketplaceEntry = { name: "acme", source: { kind: "directory", path: "/srv/mkt" }, lastUpdated: 1 };
const SUBDIR: MarketplaceEntry = {
  name: "acme",
  source: { kind: "git-subdir", url: "https://x/mono.git", path: "mkt" },
  lastUpdated: 1,
};

describe("marketplaceDraftFor", () => {
  test("seeds the kind and the matching field, blanking the others", () => {
    expect(marketplaceDraftFor(GITHUB)).toEqual({ name: "acme", kind: "github", url: "", repo: "acme/plugins", path: "" });
    expect(marketplaceDraftFor(URL)).toEqual({ name: "acme", kind: "url", url: "https://x/y.git", repo: "", path: "" });
    expect(marketplaceDraftFor(DIR)).toEqual({ name: "acme", kind: "directory", url: "", repo: "", path: "/srv/mkt" });
  });

  test("a git-subdir source shows its URL under Git URL", () => {
    expect(marketplaceDraftFor(SUBDIR)).toEqual({ name: "acme", kind: "url", url: "https://x/mono.git", repo: "", path: "" });
  });
});

describe("marketplaceEditParams", () => {
  test("null when nothing changed, whitespace included", () => {
    expect(marketplaceEditParams(GITHUB, marketplaceDraftFor(GITHUB))).toBeNull();
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: " acme ", repo: "acme/plugins " })).toBeNull();
  });

  test("a changed name is a rename", () => {
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), name: "beta" })).toEqual({ name: "acme", newName: "beta" });
  });

  test("a changed field or kind sends the whole new source", () => {
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), repo: "acme/other" })).toEqual({
      name: "acme",
      source: { kind: "github", repo: "acme/other" },
    });
    expect(marketplaceEditParams(GITHUB, { ...marketplaceDraftFor(GITHUB), kind: "directory", path: "/srv/x" })).toEqual({
      name: "acme",
      source: { kind: "directory", path: "/srv/x" },
    });
  });

  test("an untouched git-subdir source is not re-sent; an edited URL becomes a plain url source", () => {
    expect(marketplaceEditParams(SUBDIR, marketplaceDraftFor(SUBDIR))).toBeNull();
    expect(marketplaceEditParams(SUBDIR, { ...marketplaceDraftFor(SUBDIR), url: "https://x/other.git" })).toEqual({
      name: "acme",
      source: { kind: "url", url: "https://x/other.git" },
    });
  });

  test("rename and re-source ride one request", () => {
    expect(marketplaceEditParams(URL, { ...marketplaceDraftFor(URL), name: "beta", url: "https://x/z.git" })).toEqual({
      name: "acme",
      newName: "beta",
      source: { kind: "url", url: "https://x/z.git" },
    });
  });
});

test("the source options are the add form's three kinds", () => {
  expect(MARKETPLACE_SOURCE_OPTIONS.map((o) => o.value)).toEqual(["url", "github", "directory"]);
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npx vitest run src/stores/extensions.test.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.test.ts 2>&1 | tail -8`
Expected: the store tests fail (`editMarketplace is not a function`); the model file cannot be resolved.

- [ ] **Step 3: Implement the store mutation**

In `src/stores/extensions.ts`:
- Add `MarketplaceEditParams` to the `import type { ... } from "../protocol/types.gen"` list.
- In `ExtensionsStoreState`, after `refreshMarketplace(name: string): Promise<void>;`, add:

```ts
  /** Rename and/or re-source a marketplace (spec 2026-09-07 §3). The browse
   * cache for the old AND new names is dropped: a re-source changes the
   * catalog, and a renamed entry's catalog is keyed by its new name. */
  editMarketplace(params: MarketplaceEditParams): Promise<void>;
```

- After the `refreshMarketplace` implementation, add:

```ts
  async editMarketplace(params) {
    const client = requireClient();
    const resp = await client.request("evener/marketplace/edit", params);
    set((s) => {
      const nextCatalogs = new Map(s.browseCatalogs);
      nextCatalogs.delete(params.name);
      if (params.newName) nextCatalogs.delete(params.newName);
      return { marketplaces: resp.marketplaces, browseCatalogs: nextCatalogs };
    });
  },
```

- [ ] **Step 4: Implement the form model**

Create `marketplaceEdit.ts`:

```ts
// marketplaceEdit.ts: the marketplace sheet's form model - the draft, how it
// is seeded from a MarketplaceEntry, and how a dirty draft becomes the
// smallest evener/marketplace/edit request. Pure, so the rules (the kind's
// own field is the only one that counts, a git-subdir entry is left alone
// unless its URL is edited) are pinned without rendering anything.
import type { MarketplaceEditParams, MarketplaceEntry, MarketplaceSourceInput } from "../../../../protocol/types.gen";

export type MarketplaceSourceKind = "url" | "github" | "directory";

/** The same three kinds, in the same order, the add form offers. git-subdir
 * is not offered: the picker would need a second field, and the add form
 * never had one. */
export const MARKETPLACE_SOURCE_OPTIONS: { value: MarketplaceSourceKind; label: string }[] = [
  { value: "url", label: "Git URL" },
  { value: "github", label: "owner/repo" },
  { value: "directory", label: "Local path" },
];

export interface MarketplaceDraft {
  name: string;
  kind: MarketplaceSourceKind;
  url: string;
  repo: string;
  path: string;
}

export function marketplaceDraftFor(entry: MarketplaceEntry): MarketplaceDraft {
  const { source } = entry;
  const kind: MarketplaceSourceKind =
    source.kind === "github" ? "github" : source.kind === "directory" ? "directory" : "url";
  return {
    name: entry.name,
    kind,
    url: kind === "url" ? (source.url ?? "") : "",
    repo: kind === "github" ? (source.repo ?? "") : "",
    path: kind === "directory" ? (source.path ?? "") : "",
  };
}

function sourceFromDraft(draft: MarketplaceDraft): MarketplaceSourceInput {
  if (draft.kind === "github") return { kind: "github", repo: draft.repo.trim() };
  if (draft.kind === "directory") return { kind: "directory", path: draft.path.trim() };
  return { kind: "url", url: draft.url.trim() };
}

/** Whether the draft still describes the entry's own source. A git-subdir
 * entry renders as its URL under the url kind, so it is unchanged exactly
 * when that URL is untouched. */
function sourceUnchanged(entry: MarketplaceEntry, draft: MarketplaceDraft): boolean {
  const { source } = entry;
  if (source.kind === "git-subdir") return draft.kind === "url" && draft.url.trim() === (source.url ?? "");
  const next = sourceFromDraft(draft);
  if (next.kind !== source.kind) return false;
  if (next.kind === "github") return next.repo === (source.repo ?? "");
  if (next.kind === "directory") return next.path === (source.path ?? "");
  return next.url === (source.url ?? "");
}

/** The request carrying exactly what changed, or null when nothing did. */
export function marketplaceEditParams(entry: MarketplaceEntry, draft: MarketplaceDraft): MarketplaceEditParams | null {
  const params: MarketplaceEditParams = { name: entry.name };
  let changed = false;
  const name = draft.name.trim();
  if (name !== entry.name) {
    params.newName = name;
    changed = true;
  }
  if (!sourceUnchanged(entry, draft)) {
    params.source = sourceFromDraft(draft);
    changed = true;
  }
  return changed ? params : null;
}
```

- [ ] **Step 5: Run the tests**

Run: `npx biome check --write src/stores/extensions.ts src/stores/extensions.test.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.test.ts && npx vitest run src/stores/extensions.test.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.test.ts 2>&1 | tail -6`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add src/stores/extensions.ts src/stores/extensions.test.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.ts src/panes/settings/sections/marketplacesPlugins/marketplaceEdit.test.ts
git commit -m "feat(web): marketplace edit mutation and form model"
```

---

### Task 4: MarketplaceSheet, tappable rows, and the page's selection

**Files:**
- Create: `marketplacesPlugins/MarketplaceSheet.tsx`
- Modify: `marketplacesPlugins/MarketplacesSection.tsx` (rows become buttons; Refresh/Remove and their state move out; new `onSelect` prop)
- Modify: `marketplacesPlugins/index.tsx` (`selectedMarketplace`; renders the sheet)
- Modify: `marketplacesPlugins/marketplacesPlugins.module.css` (add `.sheetForm`, `.sheetNote`, `.sheetError`, `.sheetActions`, `.sheetDivider` — all five; `src/styles/requireclass-contract.test.ts` statically asserts every `styles.x` the components reference resolves to a real class)
- Modify: `marketplacesPlugins/PluginDetailSheet.tsx` (its header comment and the comment at its close-on-vanish effect still call it an "inspector"; the sheet-is-the-editor rule makes that stale — carried over from slice 2)
- Modify: `src/styles/token-contract.test.ts` (`SEMANTIC_PATH_EXCEPTIONS`, see Step 4)
- Test: `marketplacesPlugins/MarketplaceSheet.test.tsx` (new), `marketplacesPlugins/MarketplacesSection.test.tsx` (rows + moved tests), `marketplacesPlugins/index.test.tsx` (segment switch closes the sheet)

**Interfaces:**
- Consumes: Task 3's model and store mutation; widgets `Button`, `ConfirmDialog`, `FormRow`, `Input`, `PathField`, `RadioGroup`, `Sheet`, `useToasts`; `directoryActions`, `extensionsStore`, `useExtensionsStore`; `sourceLabel`; `useIsMobile`.
- Produces: `MarketplaceSheet({ name: string | null; onClose(); onRenamed(newName: string); expandedMarketplaces: Set<string> })`; `MarketplacesSection({ onSelect(name: string) })` (the `expandedMarketplaces` prop moves to the sheet).

- [ ] **Step 1: Write the failing sheet tests**

Create `MarketplaceSheet.test.tsx`:

```tsx
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../../../protocol/testing/fakeClient";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import { connectionStore } from "../../../../stores/connection";
import { extensionsStore, resetExtensionsStoreForTests } from "../../../../stores/extensions";
import { Toast } from "../../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../../widgets/toast/store";
import { MarketplaceSheet } from "./MarketplaceSheet";

const ACME: MarketplaceEntry = {
  name: "acme",
  source: { kind: "github", repo: "acme/plugins" },
  installLocation: "/home/u/.config/evener/plugins/marketplaces/acme",
  lastUpdated: 1_700_000_000,
};

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

function renderSheet(entry: MarketplaceEntry | null, expanded: Set<string> = new Set()) {
  const onClose = vi.fn();
  const onRenamed = vi.fn();
  extensionsStore.setState({ marketplaces: entry === null ? [] : [entry] });
  render(
    <>
      <Toast />
      <MarketplaceSheet name={entry?.name ?? null} onClose={onClose} onRenamed={onRenamed} expandedMarketplaces={expanded} />
    </>,
  );
  return { onClose, onRenamed };
}

function field(label: string): HTMLInputElement {
  return screen.getByLabelText(label) as HTMLInputElement;
}
function saveButton(): HTMLButtonElement {
  return screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
}

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetToastStoreForTests();
  connectFakeClient();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("renders nothing when name is null", () => {
  renderSheet(null);
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("prefills the name, the source kind, and its field; shows install location and last updated", () => {
  renderSheet(ACME);
  expect(screen.getByRole("dialog", { name: "acme" })).toBeTruthy();
  expect(field("Name").value).toBe("acme");
  // RadioGroup renders <button role="radio" aria-checked>, not an <input>, so
  // the checked state reads off the attribute (the idiom in index.test.tsx).
  expect(screen.getByRole("radio", { name: "owner/repo" }).getAttribute("aria-checked")).toBe("true");
  // The kind's input shares its label text with the radio, so query it by
  // placeholder - the same idiom MarketplacesSection.test.tsx uses.
  expect((screen.getByPlaceholderText("owner/repo") as HTMLInputElement).value).toBe("acme/plugins");
  expect(screen.queryByPlaceholderText("https://github.com/owner/repo.git")).toBeNull();
  expect(screen.getByText("/home/u/.config/evener/plugins/marketplaces/acme")).toBeTruthy();
  expect(screen.getByText("Last updated")).toBeTruthy();
});

test("Save is disabled until something changes", async () => {
  renderSheet(ACME);
  expect(saveButton().disabled).toBe(true);
  const user = userEvent.setup();
  await user.type(field("Name"), "2");
  expect(saveButton().disabled).toBe(false);
});

test("switching the kind shows only that kind's field and the re-fetch note", async () => {
  renderSheet(ACME);
  const user = userEvent.setup();
  await user.click(screen.getByRole("radio", { name: "Git URL" }));
  expect(screen.getByPlaceholderText("https://github.com/owner/repo.git")).toBeTruthy();
  expect(screen.queryByPlaceholderText("owner/repo")).toBeNull();
  expect(screen.getByText(/Saving re-fetches the marketplace/)).toBeTruthy();
});

test("Save sends a rename and the section re-selects the new name without the sheet closing", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("evener/marketplace/edit", (params) => {
    expect(params).toEqual({ name: "acme", newName: "acme2" });
    return { marketplaces: [{ ...ACME, name: "acme2" }] };
  });
  const { onClose, onRenamed } = renderSheet(ACME);
  const user = userEvent.setup();
  await user.type(field("Name"), "2");
  await user.click(saveButton());
  await waitFor(() => expect(onRenamed).toHaveBeenCalledWith("acme2"));
  expect(getToasts().some((t) => t.text === "Saved acme2")).toBe(true);
  expect(onClose).not.toHaveBeenCalled();
});

test("Save sends a changed source and reseeds the form from the refreshed entry", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("evener/marketplace/edit", (params) => {
    expect(params).toEqual({ name: "acme", source: { kind: "url", url: "https://x/y.git" } });
    return { marketplaces: [{ ...ACME, source: { kind: "url", url: "https://x/y.git" } }] };
  });
  renderSheet(ACME);
  const user = userEvent.setup();
  await user.click(screen.getByRole("radio", { name: "Git URL" }));
  await user.type(screen.getByPlaceholderText("https://github.com/owner/repo.git"), "https://x/y.git");
  await user.click(saveButton());
  await waitFor(() => expect(getToasts().some((t) => t.text === "Saved acme")).toBe(true));
  expect(saveButton().disabled).toBe(true);
  expect((screen.getByPlaceholderText("https://github.com/owner/repo.git") as HTMLInputElement).value).toBe(
    "https://x/y.git",
  );
});

test("a failed save shows the error inline and toasts", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("evener/marketplace/edit", () => {
    throw new Error("clone failed");
  });
  renderSheet(ACME);
  const user = userEvent.setup();
  await user.type(field("Name"), "2");
  await user.click(saveButton());
  await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("clone failed"));
  expect(getToasts().some((t) => t.text.startsWith("Save failed"))).toBe(true);
  expect(field("Name").value).toBe("acme2");
});

test("Refresh calls refreshMarketplace, is busy in flight, toasts, and re-browses an expanded marketplace", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  let release: (() => void) | undefined;
  fake.on(
    "evener/marketplace/refresh",
    () =>
      new Promise((resolve) => {
        release = () => resolve({ marketplaces: [ACME] });
      }),
  );
  fake.on("evener/marketplace/browse", () => ({ name: "acme", description: "", plugins: [] }));
  renderSheet(ACME, new Set(["acme"]));
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Refresh" }));
  expect((screen.getByRole("button", { name: "Refresh" }) as HTMLButtonElement).disabled).toBe(true);
  act(() => release?.());
  await waitFor(() => expect(getToasts().some((t) => t.text === "Refreshed acme")).toBe(true));
  expect((screen.getByRole("button", { name: "Refresh" }) as HTMLButtonElement).disabled).toBe(false);
  expect(fake.calls.some((c) => c.method === "evener/marketplace/browse")).toBe(true);
});

test("a failed Refresh toasts and re-enables the button", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("evener/marketplace/refresh", () => {
    throw new Error("fetch failed");
  });
  renderSheet(ACME);
  await userEvent.setup().click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(getToasts().some((t) => t.text === "Refresh failed: fetch failed")).toBe(true));
  expect((screen.getByRole("button", { name: "Refresh" }) as HTMLButtonElement).disabled).toBe(false);
});

test("Remove opens a confirm; confirming removes, toasts, and the sheet closes when the entry vanishes", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  fake.on("evener/marketplace/remove", () => ({ marketplaces: [] }));
  const { onClose } = renderSheet(ACME);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const confirm = screen.getByRole("dialog", { name: "Remove marketplace" });
  expect(within(confirm).getByText(/Installed plugins from it are unaffected/)).toBeTruthy();
  await user.click(within(confirm).getByRole("button", { name: "Remove" }));
  await waitFor(() => expect(getToasts().some((t) => t.text === "Removed marketplace acme")).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});

test("cancelling the remove confirm calls nothing", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  renderSheet(ACME);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  await user.click(within(screen.getByRole("dialog", { name: "Remove marketplace" })).getByRole("button", { name: "Cancel" }));
  expect(fake.calls.some((c) => c.method === "evener/marketplace/remove")).toBe(false);
});

test("the confirm dialog's buttons disable while removal is in flight, and it stays open until it resolves", async () => {
  const fake = connectionStore.getState().client as FakeClient;
  let release: (() => void) | undefined;
  fake.on(
    "evener/marketplace/remove",
    () =>
      new Promise((resolve) => {
        release = () => resolve({ marketplaces: [] });
      }),
  );
  renderSheet(ACME);
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Remove" }));
  const confirm = screen.getByRole("dialog", { name: "Remove marketplace" });
  await user.click(within(confirm).getByRole("button", { name: "Remove" }));
  await waitFor(() =>
    expect((within(confirm).getByRole("button", { name: "Remove" }) as HTMLButtonElement).disabled).toBe(true),
  );
  expect((within(confirm).getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.getByRole("dialog", { name: "Remove marketplace" })).toBeTruthy();
  act(() => release?.());
  await waitFor(() => expect(getToasts().some((t) => t.text === "Removed marketplace acme")).toBe(true));
});

test("closes itself when the entry disappears from the store", async () => {
  const { onClose } = renderSheet(ACME);
  act(() => extensionsStore.setState({ marketplaces: [] }));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});
```

- [ ] **Step 2: Update the section and page tests**

In `MarketplacesSection.test.tsx`:
- Change the first test to:

```tsx
test("renders each marketplace as one tappable row carrying name, kind, and source; tapping selects it", async () => {
  connectFakeClient();
  extensionsStore.setState({ marketplaces: [MARKETPLACE_A] });
  const onSelect = vi.fn();
  render(<MarketplacesSection onSelect={onSelect} />);
  const row = screen.getByRole("button", { name: /acme-plugins/ });
  expect(within(row).getByText("github")).toBeTruthy();
  expect(within(row).getByText("github: acme/plugins")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
  await userEvent.setup().click(row);
  expect(onSelect).toHaveBeenCalledWith("acme-plugins");
});
```

  Carry over verbatim, into this same test, the two assertions the original
  first test made and this replacement drops — the `queryByRole("heading")`
  and `queryByText("1 entry")` lines that pin "the segment label owns the
  heading and the count". They still hold, and nothing else covers them.

- Every remaining render of `<MarketplacesSection expandedMarketplaces={...} />` becomes `<MarketplacesSection onSelect={vi.fn()} />` (eight call sites).
- Delete the tests from `"Refresh calls refreshMarketplace and toasts success"` through `"the confirm dialog's buttons disable while removal is in flight, and it stays open until it resolves"` (they now live in `MarketplaceSheet.test.tsx`).

In `index.test.tsx`, append:

```tsx
test("switching segments while the marketplace sheet is open closes it", async () => {
  connectSeededClient();
  render(<MarketplacesPluginsSection />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: "Marketplaces (1)" }));
  await user.click(await screen.findByRole("button", { name: /acme-plugins/ }));
  expect(screen.getByRole("dialog", { name: "acme-plugins" })).toBeTruthy();
  await user.click(screen.getByRole("radio", { name: "Browse" }));
  await waitFor(() => expect(screen.queryByRole("dialog", { name: "acme-plugins" })).toBeNull());
});
```

(`connectSeededClient` and the segment radio names are the file's own; the existing "switching segments while the detail sheet is open closes it" test is the model.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `npx vitest run src/panes/settings/sections/marketplacesPlugins 2>&1 | tail -10`
Expected: the sheet test file cannot resolve `./MarketplaceSheet`; the section tests fail on the new prop and missing row button.

- [ ] **Step 4: Add the stylesheet rules**

Append to `marketplacesPlugins.module.css`:

```css
/* MarketplaceSheet body (the sheet is the editor, spec 2026-09-07 §3): the
 * form above the meta table, then Refresh, then the danger zone under a
 * divider - the same shape as the provider sheet. */
.sheetForm {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}

.sheetNote {
  margin: 0;
  color: var(--ink-mid);
  font-size: var(--font-size-caption);
}

.sheetError {
  margin: 0;
  color: var(--danger-ink);
  font-size: var(--font-size-caption);
}

.sheetActions {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.sheetActions > button {
  justify-content: flex-start;
  width: 100%;
}

.sheetDivider {
  margin: var(--space-3) 0;
  border: none;
  border-top: 1px solid var(--edge);
}
```

`--danger-ink` is the repo's token for inline error text (`keybindings.module.css`
and `delegateStatus.module.css` both use it for exactly this). It is a semantic
token, so `src/styles/token-contract.test.ts`'s `SEMANTIC_VAR_RE` will flag it
unless this stylesheet is listed in `SEMANTIC_PATH_EXCEPTIONS` — add
`panes/settings/sections/marketplacesPlugins/marketplacesPlugins.module.css`
there, beside its neighbours. First read that list's own comment: if it
documents a rule this stylesheet does not meet, keep `var(--ink-hi)` instead,
add nothing to the list, and report the conflict as a concern.

- [ ] **Step 5: Implement the sheet**

Create `MarketplaceSheet.tsx`:

```tsx
// MarketplaceSheet: the marketplace's editor (spec 2026-09-07 §3). Opens
// from a MarketplacesSection row tap and IS the edit surface: Name and the
// source (the add form's own three-way picker) are prefilled form fields,
// Save in the footer lights up when either differs, and a changed source
// says it will re-fetch. Refresh and Remove - the two actions the row used
// to carry inline - sit below the meta table, Remove behind its
// ConfirmDialog. A wide right Sheet on desktop, a bottom Sheet on mobile.
//
// The entry is read from the store by name so cross-client changes land
// live; the draft reseeds when a different marketplace opens or this
// sheet's own save lands, never on an unrelated refresh. The sheet closes
// itself when its entry vanishes - except across its own rename, where the
// page re-selects the new name (onRenamed).
import { useEffect, useId, useRef, useState } from "react";
import { errorText } from "../../../../protocol/errors";
import type { MarketplaceEntry } from "../../../../protocol/types.gen";
import { useIsMobile } from "../../../../shell/useIsMobile";
import { directoryActions, extensionsStore, useExtensionsStore } from "../../../../stores/extensions";
import { Button, ConfirmDialog, FormRow, Input, PathField, RadioGroup, Sheet, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import {
  MARKETPLACE_SOURCE_OPTIONS,
  type MarketplaceDraft,
  type MarketplaceSourceKind,
  marketplaceDraftFor,
  marketplaceEditParams,
} from "./marketplaceEdit";
import styles from "./marketplacesPlugins.module.css";

const CLASS = {
  sheetForm: requireClass(styles.sheetForm, "marketplacesPlugins.module.css", "sheetForm"),
  sheetNote: requireClass(styles.sheetNote, "marketplacesPlugins.module.css", "sheetNote"),
  sheetError: requireClass(styles.sheetError, "marketplacesPlugins.module.css", "sheetError"),
  sheetActions: requireClass(styles.sheetActions, "marketplacesPlugins.module.css", "sheetActions"),
  sheetDivider: requireClass(styles.sheetDivider, "marketplacesPlugins.module.css", "sheetDivider"),
  metaList: requireClass(styles.metaList, "marketplacesPlugins.module.css", "metaList"),
  metaRow: requireClass(styles.metaRow, "marketplacesPlugins.module.css", "metaRow"),
  metaLabel: requireClass(styles.metaLabel, "marketplacesPlugins.module.css", "metaLabel"),
  metaValue: requireClass(styles.metaValue, "marketplacesPlugins.module.css", "metaValue"),
  rowMeta: requireClass(styles.rowMeta, "marketplacesPlugins.module.css", "rowMeta"),
};

export interface MarketplaceSheetProps {
  name: string | null;
  onClose: () => void;
  /** After a successful rename, with the new name: the page re-selects it
   * so the sheet stays open on the same marketplace. */
  onRenamed: (newName: string) => void;
  /** Read-only: a Refresh on a marketplace BrowseSection currently has
   * expanded re-browses it at once, so the tree never shows a catalog the
   * refresh just invalidated (see index.tsx's own comment on lifting it). */
  expandedMarketplaces: Set<string>;
}

function lastUpdatedText(seconds: number): string {
  return seconds > 0 ? new Date(seconds * 1000).toLocaleString() : "never";
}

export function MarketplaceSheet({ name, onClose, onRenamed, expandedMarketplaces }: MarketplaceSheetProps) {
  const marketplaces = useExtensionsStore((s) => s.marketplaces);
  const isMobile = useIsMobile();
  const toasts = useToasts();
  const ids = useId();

  const entry = name === null || marketplaces === null ? undefined : marketplaces.find((m) => m.name === name);

  const [draft, setDraft] = useState<MarketplaceDraft | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [pendingRemove, setPendingRemove] = useState(false);
  const [removeBusy, setRemoveBusy] = useState(false);
  // Set for the span of a rename request: the old name vanishes from the
  // store when the response lands, and that vanish must not close the sheet.
  const pendingRename = useRef<string | null>(null);

  function seed(current: MarketplaceEntry): void {
    setDraft(marketplaceDraftFor(current));
    setFormError(null);
  }

  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when a different marketplace opens; a refresh of the same one must not clobber in-progress edits
  useEffect(() => {
    if (entry === undefined) {
      setDraft(null);
      setFormError(null);
      setPendingRemove(false);
      return;
    }
    seed(entry);
  }, [entry?.name]);

  useEffect(() => {
    if (name !== null && marketplaces !== null && entry === undefined && pendingRename.current === null) onClose();
  }, [name, marketplaces, entry, onClose]);
  useEffect(() => {
    pendingRename.current = null;
  }, [name]);

  const open = name !== null && entry !== undefined;
  const params = entry !== undefined && draft !== null ? marketplaceEditParams(entry, draft) : null;
  const dirty = params !== null;
  const sourceDirty = params?.source !== undefined;

  function update(patch: Partial<MarketplaceDraft>): void {
    setDraft((current) => (current === null ? current : { ...current, ...patch }));
  }

  async function handleSave(): Promise<void> {
    if (entry === undefined || params === null) return;
    setFormError(null);
    setSaving(true);
    if (params.newName !== undefined) pendingRename.current = params.newName;
    try {
      await extensionsStore.getState().editMarketplace(params);
      toasts.push("success", `Saved ${params.newName ?? entry.name}`);
      if (params.newName !== undefined) {
        onRenamed(params.newName);
      } else {
        const refreshed = extensionsStore.getState().marketplaces?.find((m) => m.name === entry.name);
        if (refreshed !== undefined) seed(refreshed);
      }
    } catch (err) {
      pendingRename.current = null;
      const message = errorText(err);
      setFormError(message);
      toasts.push("error", `Save failed: ${message}`);
    } finally {
      setSaving(false);
    }
  }

  async function handleRefresh(): Promise<void> {
    if (entry === undefined) return;
    setRefreshing(true);
    try {
      await extensionsStore.getState().refreshMarketplace(entry.name);
      if (expandedMarketplaces.has(entry.name)) void extensionsStore.getState().browseMarketplace(entry.name);
      toasts.push("success", `Refreshed ${entry.name}`);
    } catch (err) {
      toasts.push("error", `Refresh failed: ${errorText(err)}`);
    } finally {
      setRefreshing(false);
    }
  }

  async function handleConfirmRemove(): Promise<void> {
    if (entry === undefined) return;
    setRemoveBusy(true);
    try {
      await extensionsStore.getState().removeMarketplace(entry.name);
      toasts.push("success", `Removed marketplace ${entry.name}`);
      setPendingRemove(false);
      // onClose fires via the entry-vanished effect once the store's updated
      // list lands - no explicit close here.
    } catch (err) {
      toasts.push("error", `Remove marketplace failed: ${errorText(err)}`);
    } finally {
      setRemoveBusy(false);
    }
  }

  return (
    <>
      <Sheet
        open={open}
        onClose={onClose}
        title={entry?.name ?? ""}
        side={isMobile ? "bottom" : "right"}
        size="wide"
        footer={
          entry !== undefined && (
            <Button onClick={() => void handleSave()} disabled={!dirty || saving}>
              Save
            </Button>
          )
        }
      >
        {entry !== undefined && draft !== null && (
          <>
            <form
              className={CLASS.sheetForm}
              aria-label={`Edit ${entry.name}`}
              onSubmit={(event) => {
                event.preventDefault();
                void handleSave();
              }}
            >
              <FormRow label="Name" htmlFor={`${ids}-name`}>
                <Input
                  id={`${ids}-name`}
                  value={draft.name}
                  onChange={(event) => update({ name: event.target.value })}
                  disabled={saving}
                />
              </FormRow>
              <RadioGroup
                label="Source"
                value={draft.kind}
                onChange={(value) => update({ kind: value as MarketplaceSourceKind })}
                options={MARKETPLACE_SOURCE_OPTIONS}
                disabled={saving}
              />
              {draft.kind === "url" && (
                <FormRow label="Git URL" htmlFor={`${ids}-url`}>
                  <Input
                    id={`${ids}-url`}
                    value={draft.url}
                    onChange={(event) => update({ url: event.target.value })}
                    placeholder="https://github.com/owner/repo.git"
                    disabled={saving}
                  />
                </FormRow>
              )}
              {draft.kind === "github" && (
                <FormRow label="owner/repo" htmlFor={`${ids}-repo`}>
                  <Input
                    id={`${ids}-repo`}
                    value={draft.repo}
                    onChange={(event) => update({ repo: event.target.value })}
                    placeholder="owner/repo"
                    disabled={saving}
                  />
                </FormRow>
              )}
              {draft.kind === "directory" && (
                <FormRow label="Local path" htmlFor={`${ids}-path`}>
                  <PathField
                    ariaLabel="Local path"
                    directory={directoryActions}
                    id={`${ids}-path`}
                    value={draft.path}
                    onChange={(value) => update({ path: value })}
                    kind="dir"
                    complete={(prefix, includeFiles) => extensionsStore.getState().completePaths(prefix, includeFiles)}
                    placeholder="/absolute/path"
                  />
                </FormRow>
              )}
              {sourceDirty && (
                <p className={CLASS.sheetNote} role="status">
                  Saving re-fetches the marketplace. Installed plugins are unaffected.
                </p>
              )}
              {formError !== null && (
                <p className={CLASS.sheetError} role="alert">
                  {formError}
                </p>
              )}
            </form>
            <div className={CLASS.metaList}>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Install location</span>
                <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>{entry.installLocation || "not fetched yet"}</span>
              </div>
              <div className={CLASS.metaRow}>
                <span className={CLASS.metaLabel}>Last updated</span>
                <span className={`${CLASS.metaValue} ${CLASS.rowMeta}`}>{lastUpdatedText(entry.lastUpdated)}</span>
              </div>
            </div>
            <div className={CLASS.sheetActions}>
              <Button variant="quiet" onClick={() => void handleRefresh()} disabled={refreshing}>
                Refresh
              </Button>
            </div>
            <hr className={CLASS.sheetDivider} />
            <div className={CLASS.sheetActions}>
              <Button variant="danger" onClick={() => setPendingRemove(true)}>
                Remove
              </Button>
            </div>
          </>
        )}
      </Sheet>
      <ConfirmDialog
        open={pendingRemove && entry !== undefined}
        title="Remove marketplace"
        confirmLabel="Remove"
        busy={removeBusy}
        onConfirm={() => void handleConfirmRemove()}
        onCancel={() => setPendingRemove(false)}
      >
        {entry !== undefined ? `Remove marketplace "${entry.name}"? Installed plugins from it are unaffected.` : ""}
      </ConfirmDialog>
    </>
  );
}
```

Two things pre-flight settled, so do not re-litigate them: `RadioGroup`'s
`options` element type is `{ value: string; label: string; accessibleLabel?:
string; disabled?: boolean }`, so `MARKETPLACE_SOURCE_OPTIONS` is assignable
with no cast; and `PathField` with `kind="dir"` renders a trigger button that
opens the shared `DirectoryPicker` rather than a text input, so its `onChange`
fires once on commit — the draft updates then, which is all this form needs.

Because that field is a button and not an input, wrap it exactly the way
`MarketplacesSection.tsx`'s add form already wraps its own `PathField`: copy
that wrapping (whether or not it uses a `FormRow`, and whichever of `label`
/`htmlFor`/`ariaLabel` it sets) instead of the `FormRow label="Local path"` +
`ariaLabel="Local path"` pair sketched above, which would give the control two
labels with the same text.

- [ ] **Step 6: Make the rows tappable and lift the selection**

Rewrite `MarketplacesSection.tsx`'s list and props:
- Props become `export interface MarketplacesSectionProps { onSelect: (name: string) => void; }`.
- Delete `pendingRemove`, `removeBusy`, `refreshBusy`, `handleRefresh`, `handleConfirmRemove`, the `ConfirmDialog`, and the `rowActions` markup; drop `ConfirmDialog` from the widgets import and remove `rowActions` AND `row` from `CLASS` (the `<li>` loses `.row` when it becomes a button, so both entries go dead; the classes themselves stay in the stylesheet because `BrowseSection.tsx` still uses them); add `rowButton` and `rowChevron` to `CLASS` (both already exist in the stylesheet) and import `Chevron` from widgets.
- Delete the section's own `SourceKind` type and `SOURCE_OPTIONS` constant and import `type MarketplaceSourceKind` and `MARKETPLACE_SOURCE_OPTIONS` from `./marketplaceEdit` instead — Task 3 introduces the same three kinds in the same order, and its own comment says the sheet's picker must match the add form's. Do this only if the two option arrays are identical (same values, same labels, same order); if a label differs, keep the section's constant, leave the add form's visible labels alone, and report the divergence.
- Each list item renders:

```tsx
            <li key={m.name}>
              <button type="button" className={CLASS.rowButton} onClick={() => onSelect(m.name)}>
                <div className={CLASS.rowMain}>
                  <div className={CLASS.rowText}>
                    {m.name} <span className={CLASS.rowKind}>{m.source.kind}</span>
                  </div>
                  <div className={CLASS.rowMeta}>{sourceLabel(m.source)}</div>
                </div>
                <span className={CLASS.rowChevron} aria-hidden="true">
                  <Chevron direction="right" />
                </span>
              </button>
            </li>
```

- Update the header comment: the section is the list plus the add form; every per-marketplace action lives in `MarketplaceSheet`, which is why `expandedMarketplaces` now flows to the sheet instead of here.

In `index.tsx`:
- Add `const [selectedMarketplace, setSelectedMarketplace] = useState<string | null>(null);`.
- In `handleSegmentChange` also `setSelectedMarketplace(null);` and extend its comment: both sheets belong to their segments.
- Render `<MarketplacesSection onSelect={setSelectedMarketplace} />` and, beside `PluginDetailSheet`:

```tsx
      <MarketplaceSheet
        name={selectedMarketplace}
        onClose={() => setSelectedMarketplace(null)}
        onRenamed={setSelectedMarketplace}
        expandedMarketplaces={expandedMarketplaces}
      />
```

with `import { MarketplaceSheet } from "./MarketplaceSheet";`.

Finally, retire the stale "inspector" framing in this directory (spec 2026-09-07
§4 makes a detail sheet the item's editor; slice 2 deferred this to whichever
slice next touched these files). Reword, comments only, no behavior change:
`PluginDetailSheet.tsx:1` ("the plugin inspector for the Segmented Workspace")
and `PluginDetailSheet.tsx:53` ("An inspector for a thing that no longer
exists…"), plus the same word in the `index.tsx` comment you are already
editing. `PluginDetailSheet` itself gets no functional change (spec §4).

- [ ] **Step 7: Run the marketplaces tests and the whole settings suite**

Run: `npx biome check --write src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.test.tsx src/panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx src/panes/settings/sections/marketplacesPlugins/MarketplacesSection.test.tsx src/panes/settings/sections/marketplacesPlugins/index.tsx src/panes/settings/sections/marketplacesPlugins/index.test.tsx src/panes/settings/sections/marketplacesPlugins/marketplacesPlugins.module.css && npx vitest run src/panes/settings 2>&1 | tail -10`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx src/panes/settings/sections/marketplacesPlugins/MarketplaceSheet.test.tsx src/panes/settings/sections/marketplacesPlugins/MarketplacesSection.tsx src/panes/settings/sections/marketplacesPlugins/MarketplacesSection.test.tsx src/panes/settings/sections/marketplacesPlugins/index.tsx src/panes/settings/sections/marketplacesPlugins/index.test.tsx src/panes/settings/sections/marketplacesPlugins/PluginDetailSheet.tsx src/panes/settings/sections/marketplacesPlugins/marketplacesPlugins.module.css src/styles/token-contract.test.ts
git commit -m "feat(web): marketplace rows open an editor sheet"
```

---

### Task 5: Docs, gates, browser pass, and the pull request

**Files:**
- Modify: `docs/web-ui/design-system.md` (§10)

- [ ] **Step 1: Name the sheet as the reference editor**

Pre-flight settled what this step assumed: slice 2 has NOT merged, so §10 has no
sheet-as-editor sentence and names no reference `.tsx` file. It still says "The
sheet is the item's inspector" (around line 639), and the rows-are-single-
tappable-targets paragraph ends around line 645 with "…closing the pane out
from under an open overlay."

So, the fallback path, kept as small as it can be:

- Append one sentence to that paragraph: a detail sheet is the item's **editor**,
  with editable facts rendered as prefilled form fields and a dirty-gated Save in
  the sheet footer, and name `panes/settings/sections/marketplacesPlugins/MarketplaceSheet.tsx`
  as the reference implementation.
- Reword only the "inspector" framing in §10 that the new sentence would
  otherwise contradict outright (the two spots pre-flight found, around lines 639
  and 652).

Do NOT do the rest of slice 2's work here: no full §10 rewrite, and no
`docs/web-ui/decisions.md` entry — spec §4 and §6 assign both to slice 2, whose
PR will expand this sentence rather than correct it.

- [ ] **Step 2: Run every gate from the repo root, reading each for failures**

```bash
make test-web 2>&1 | tail -15
make test-web-browser 2>&1 | tail -10
make lint 2>&1 | tail -20
make vet 2>&1 | tail -5
make test 2>&1 | tail -20
```

Expected: all green. If `make test` reports one of the known flaky tests (drain-abandon cleanup race, execenv, procgroup, drain-continue), re-run that package once and say which it was.

- [ ] **Step 3: Live check against a throwaway hub**

Follow the WebUI screenshot recipe in memory with `XDG_CONFIG_HOME` pointed at a temp dir. Create a local directory marketplace (a dir with `.claude-plugin/marketplace.json` naming one plugin at `./plugins/widget` with its own `.claude-plugin/plugin.json`), add it from Settings → Marketplaces & Plugins → Marketplaces as a Local path, install the plugin from Browse, then open the marketplace row: rename it, Save, and confirm the sheet stays open under the new name, `known_marketplaces.json` and `installed_plugins.json` under `$XDG_CONFIG_HOME/evener/plugins` carry the new name, and the Installed segment still lists the plugin under it. Take a desktop and a phone-width screenshot of the sheet for the PR.

- [ ] **Step 4: Commit, push, open the PR**

```bash
git add docs/web-ui/design-system.md
git commit -m "docs(web-ui): name the marketplace sheet as a reference editor"
git push -u origin HEAD
gh pr create --repo prime-radiant-inc/evener --base main --title "Marketplace rows open an editor sheet; marketplace/edit renames and re-sources" --body-file - <<'EOF'
Slice 3 of docs/superpowers/specs/2026-09-07-settings-sheet-editors-and-agents-doc-design.md.

- `internal/plugins.Manager.EditMarketplace`: fetch-first, directory-rename rollback, re-keys installed_plugins.json and install paths on rename, swaps the staged clone on re-source.
- New additive `evener/marketplace/edit` (v4); taken name → Conflict, unknown → InvalidParams; broadcasts marketplace-updated and plugin-updated.
- Marketplace rows are single tappable targets; the new `MarketplaceSheet` edits name and source in place with a dirty-gated Save and carries Refresh and the confirm-gated Remove.

Gates: make lint, make vet, make test, make test-web, make test-web-browser. Screenshots attached.
EOF
```

Then watch CI and roborev; findings that recur on every push get fixed in code.
