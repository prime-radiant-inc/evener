package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeMarketplaceRepo builds a git repo containing a .claude-plugin/marketplace.json
// naming one plugin, and returns its path.
func makeMarketplaceRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mkt-"+name)
	mj := `{"name":"` + name + `","owner":{"name":"o"},"plugins":[` +
		`{"name":"widget","description":"a widget","source":"./plugins/widget"}]}`
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, dir, "README.md", "mkt") // also commits marketplace.json via `git add .`
	return dir
}

func TestAddListRemoveMarketplace(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())

	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if ref.InstallLocation == "" {
		t.Fatal("empty InstallLocation")
	}

	list, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := list["acme"]; !ok {
		t.Fatalf("marketplace 'acme' not listed: %v", list)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	list, _ = m.ListMarketplaces(context.Background())
	if _, ok := list["acme"]; ok {
		t.Fatal("marketplace still present after remove")
	}
	if _, err := os.Stat(m.marketplaceDir("acme")); !os.IsNotExist(err) {
		t.Fatal("clone dir not deleted after remove")
	}
}

func TestAddMarketplace_GitSubdirBrowse(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "monorepo")
	if err := os.MkdirAll(filepath.Join(repo, "mkt", ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mkt", ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":"./plugins/widget"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, repo, "README.md", "root") // commits everything incl. mkt/

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceGitSubdir, URL: repo, Path: "mkt"}); err != nil {
		t.Fatalf("AddMarketplace git-subdir: %v", err)
	}
	cat, err := m.Browse(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Browse git-subdir marketplace: %v", err)
	}
	if cat.Name != "acme" || len(cat.Plugins) != 1 {
		t.Fatalf("catalog = %+v", cat)
	}
}

func TestAddMarketplace_RejectsTraversalName(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repo := filepath.Join(t.TempDir(), "evilmkt")
	if err := os.MkdirAll(filepath.Join(repo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	// marketplace.json whose name escapes the store
	if err := os.WriteFile(filepath.Join(repo, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"../../evil","owner":{"name":"o"},"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, repo, "README.md", "x")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: repo}); err == nil {
		t.Fatal("AddMarketplace accepted a traversing marketplace name")
	}
}

// The add path takes its name from the marketplace.json it just read when the
// caller gave none, so an untrusted catalog gets to name itself. A marketplace
// registered as .staging would have its clone swept by the next add as that
// add's own scratch directory, and one carrying an '@' would key its installs
// where no lookup splitting at the last '@' can find them.
func TestAddMarketplace_RefusesTheScratchNamesAndAt(t *testing.T) {
	refused := []struct{ name, want string }{
		{stagingCloneName, "scratch"},
		{asideCloneName, "scratch"},
		{"acme@corp", "'@'"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			dir := makeDirectoryMarketplace(t, tc.name, "widget")
			m := NewManager(t.TempDir())

			_, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir})
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("adding a marketplace named %q = %v, want ErrInvalidName", tc.name, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to name the rule (%q)", err, tc.want)
			}
			if got := readStoreFileIfAny(m.marketplacesFile()); got != "" {
				t.Fatalf("%s was written by a refused add:\n%s", marketplacesFileName, got)
			}
			if _, err := os.Stat(m.marketplaceDir(tc.name)); err == nil {
				t.Fatalf("a directory appeared under %q", tc.name)
			}
		})
	}
}

func TestRemoveMarketplace_DirectorySourceKeepsContents(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	dir := makeMarketplaceRepo(t, "local")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace directory: %v", err)
	}
	if err := m.RemoveMarketplace(context.Background(), "local"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("directory source contents deleted on remove: %v", err)
	}
}

func TestRefreshMarketplace_ClonesUnfetchedSeed(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())
	if err := m.saveMarketplaces(Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: mktRepo}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("refresh unfetched seed: %v", err)
	}
	mk, _ := m.ListMarketplaces(context.Background())
	if mk["acme"].InstallLocation == "" {
		t.Fatal("refresh did not clone the unfetched seed")
	}
}

// TestRefreshMarketplace_RecoversWedgedCloneByStagedReclone pins the
// self-heal path: when git pull fails in the persistent clone (here wedged
// exactly the way a SIGKILLed git wedges it — a stale .git/index.lock),
// refresh falls back to a staged reclone and recovers instead of failing
// forever until the marketplace is removed and re-added.
func TestRefreshMarketplace_RecoversWedgedCloneByStagedReclone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	src := makeMarketplaceRepo(t, "acme")
	m := NewManager(t.TempDir())
	ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: src})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	// Upstream gains a commit, so a refresh must move the index (and would
	// visibly pull new content).
	advanceGitRepo(t, src, "README.md", "new upstream content")
	// Wedge the clone: a stale lock makes every pull fail.
	if err := os.WriteFile(filepath.Join(ref.InstallLocation, ".git", "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := m.RefreshMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("refresh did not self-heal a wedged clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ref.InstallLocation, "README.md")); err != nil || string(b) != "new upstream content" {
		t.Fatalf("recovered clone lacks fresh upstream content: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(ref.InstallLocation, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("stale index.lock survived the reclone: %v", err)
	}
	for _, leftover := range []string{".staging", ".old"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("reclone left %s behind: %v", leftover, err)
		}
	}
}

// TestRefreshMarketplace_FailedRecloneLeavesCloneUntouched pins the safety
// constraint of the staged reclone: if the fresh clone cannot be downloaded,
// the existing (possibly wedged) clone is left exactly as it was — never
// wiped — and the error reports both failures loudly.
func TestRefreshMarketplace_FailedRecloneLeavesCloneUntouched(t *testing.T) {
	m := NewManager(t.TempDir())
	installLoc := m.marketplaceDir("acme")
	if err := os.MkdirAll(installLoc, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(installLoc, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("still here"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := time.Unix(1000, 0).UTC()
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"},
		InstallLocation: installLoc,
		LastUpdated:     before,
	}}); err != nil {
		t.Fatal(err)
	}
	origPull, origClone := marketplaceGitPull, marketplaceGitClone
	t.Cleanup(func() { marketplaceGitPull, marketplaceGitClone = origPull, origClone })
	marketplaceGitPull = func(context.Context, string) error { return errors.New("pull wedged") }
	marketplaceGitClone = func(context.Context, string, string, string, string) error { return errors.New("network down") }

	err := m.RefreshMarketplace(context.Background(), "acme")
	if err == nil {
		t.Fatal("refresh succeeded; want loud error when pull and reclone both fail")
	}
	if !strings.Contains(err.Error(), "pull wedged") || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("err = %v, want both the pull and reclone failures reported", err)
	}
	b, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(b) != "still here" {
		t.Fatalf("old clone touched by failed reclone: %q, %v", b, readErr)
	}
	if _, err := os.Stat(m.marketplaceDir(".staging")); !os.IsNotExist(err) {
		t.Fatalf("failed reclone left .staging behind: %v", err)
	}
	mk, _ := m.ListMarketplaces(context.Background())
	if !mk["acme"].LastUpdated.Equal(before) {
		t.Fatalf("LastUpdated advanced on failed refresh: %v", mk["acme"].LastUpdated)
	}
}

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

	// LastUpdated is a fetch-freshness stamp and a rename fetches nothing; the
	// fixed clock makes any bump unmistakable.
	before, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Now = func() time.Time { return time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC) }

	ref, err := m.EditMarketplace(ctx, name, "acme2", nil)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if !ref.LastUpdated.Equal(before[name].LastUpdated) {
		t.Fatalf("a pure rename advanced LastUpdated: %v → %v", before[name].LastUpdated, ref.LastUpdated)
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
	list, err := m.ListMarketplaces(context.Background())
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
	items, err := m.List(context.Background())
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

func TestEditMarketplace_RenameMovesACloneTheEntryDoesNotRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	// The shape a fetch killed mid-clone leaves: a git source recorded without
	// an install location, and a directory at the canonical path anyway,
	// because the fetch cleared and refilled it before it was killed.
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
	}}); err != nil {
		t.Fatal(err)
	}
	partial := m.marketplaceDir("acme")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "keepme"), []byte("the partial clone's"), 0o644); err != nil {
		t.Fatal(err)
	}

	ref, err := m.EditMarketplace(ctx, "acme", "acme2", nil)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("a directory survived under the old name %s: %v", partial, err)
	}
	if _, err := os.Stat(filepath.Join(m.marketplaceDir("acme2"), "keepme")); err != nil {
		t.Fatalf("the clone did not move with the rename: %v", err)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want an unfetched entry to stay unfetched", ref.InstallLocation)
	}
	list, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	recorded, ok := list["acme2"]
	if !ok {
		t.Fatalf("acme2 is not recorded: %v", list)
	}
	if recorded.InstallLocation != "" {
		t.Fatalf("recorded InstallLocation = %q, want an unfetched entry to stay unfetched", recorded.InstallLocation)
	}
}

// A recorded install location is where the marketplace's clone is, and a
// rename moves the clone it names. With no clone to move — the user deleted
// it — the entry comes away unfetched, so the next fetch clones under the new
// name, rather than recording a path nothing is at.
func TestEditMarketplace_RenameUnfetchesAnEntryWhoseCloneIsGone(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	clone := plantCatalog(t, m.marketplaceDir("acme"))
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
		InstallLocation: clone,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(clone); err != nil {
		t.Fatal(err)
	}

	ref, err := m.EditMarketplace(ctx, "acme", "acme2", nil)
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}

	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		plantCatalog(t, dest)
		return nil
	}
	if err := m.RefreshMarketplace(ctx, "acme2"); err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("acme2"), ".claude-plugin", "marketplace.json"))
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if want := m.marketplaceDir("acme2"); mk["acme2"].InstallLocation != want {
		t.Fatalf("InstallLocation after the refresh = %q, want the clone at %q", mk["acme2"].InstallLocation, want)
	}
}

// A clone that fails partway leaves whatever it downloaded at the canonical
// path, because the fetch clears that directory before it starts. Nothing
// records the directory — the entry comes away unfetched — so the failed fetch
// has to take it away itself, the same discipline AddMarketplace applies to its
// staging directory. Both never-fetched fetch paths owe it: Browse's lazy clone
// and a refresh of a seeded pointer.
func TestFailedFetch_LeavesNoPartialClone(t *testing.T) {
	ctx := context.Background()
	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })

	// A clone that fails the way a broken download does: it puts a directory
	// and a file there, then reports the failure.
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dest, "partial"), []byte("half a clone"), 0o644); err != nil {
			return err
		}
		return errors.New("network down")
	}

	seeded := Marketplaces{"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}}}
	tests := []struct {
		name  string
		fetch func(m *Manager) error
	}{
		{"browse", func(m *Manager) error { _, err := m.Browse(ctx, "acme"); return err }},
		{"refresh", func(m *Manager) error { return m.RefreshMarketplace(ctx, "acme") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			if err := m.saveMarketplaces(seeded); err != nil {
				t.Fatal(err)
			}

			if err := test.fetch(m); err == nil {
				t.Fatal("a failed clone was reported as success")
			}
			mustNotExist(t, m.marketplaceDir("acme"))
			mk, err := m.loadMarketplaces()
			if err != nil {
				t.Fatal(err)
			}
			if got := mk["acme"]; got != seeded["acme"] {
				t.Fatalf("entry = %+v, want it unchanged at %+v", got, seeded["acme"])
			}
		})
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
	stamp := time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)
	m.Now = func() time.Time { return stamp }
	ref, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.Source.URL != repoB {
		t.Fatalf("Source = %+v, want repoB", ref.Source)
	}
	if !ref.LastUpdated.Equal(stamp) {
		t.Fatalf("a re-source did not advance LastUpdated: %v, want %v", ref.LastUpdated, stamp)
	}
	cat, err := m.Browse(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "gadget" {
		t.Fatalf("catalog after re-source = %+v, want gadget", cat.Plugins)
	}
	// The old clone outlives the swap only as long as a failure could still
	// need it back; a committed edit sweeps it with the staging dir.
	for _, leftover := range []string{".staging", ".old"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("%s directory survived a successful re-source", leftover)
		}
	}
}

// That sweep of the aside copy is best-effort, so a failed one strands a
// directory under the fixed .old name the NEXT swap has to move the current
// clone to — and a rename onto a non-empty directory fails, which would leave
// the marketplace unable to re-source until someone deleted it by hand. The
// name is the swap's own scratch, so whatever sits there is residue.
func TestEditMarketplace_ResourceClearsAStaleAsideDirectory(t *testing.T) {
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
	stale := m.marketplaceDir(asideCloneName)
	if err := os.MkdirAll(filepath.Join(stale, "residue"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB}); err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	cat, err := m.Browse(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Plugins) != 1 || cat.Plugins[0].Name != "gadget" {
		t.Fatalf("catalog after re-source = %+v, want gadget", cat.Plugins)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("%s survived the re-source: %v", asideCloneName, err)
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
	list, _ := m.ListMarketplaces(context.Background())
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

// A re-source from git to a directory is the one edit that deletes a directory
// after the files are saved — the clone the directory source makes redundant.
// Renaming in the same call moves that clone first, so the removal has to name
// the new name; naming the old one would leave the clone behind, and naming the
// directory source would destroy the marketplace itself.
func TestEditMarketplace_ResourceToDirectoryDropsTheRenamedClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	dir := makeDirectoryMarketplace(t, "beta", "gadget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if _, err := m.Install(ctx, "widget", name); err != nil {
		t.Fatalf("Install: %v", err)
	}

	ref, err := m.EditMarketplace(ctx, name, "beta", &Source{Kind: SourceDirectory, Path: dir})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if ref.InstallLocation != dir {
		t.Fatalf("InstallLocation = %q, want the directory source %q", ref.InstallLocation, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("the directory source itself was removed: %v", err)
	}
	for _, gone := range []string{name, "beta"} {
		if _, err := os.Stat(m.marketplaceDir(gone)); !os.IsNotExist(err) {
			t.Fatalf("clone directory %s outlived the re-source: %v", m.marketplaceDir(gone), err)
		}
	}
	reg, _ := m.loadRegistry()
	entries, ok := reg.Plugins[registryKey("widget", "beta")]
	if !ok || len(entries) != 1 {
		t.Fatalf("registry not re-keyed: %v", reg.Plugins)
	}
	if _, err := os.Stat(entries[0].InstallPath); err != nil {
		t.Fatalf("the installed plugin lives under the cache, not the clone, but its path is gone: %v", err)
	}
	cat, err := m.Browse(ctx, "beta")
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].Name != "gadget" {
		t.Fatalf("Browse beta = %+v, %v", cat, err)
	}
}

// strandCloneAfterDirectoryResource re-sources the named git marketplace to a
// directory source with the after-save removal of its now-redundant clone made
// to fail, so the clone is left under <marketplaces>/<name> exactly as a failed
// best-effort removal strands it. It returns that stranded path.
func strandCloneAfterDirectoryResource(t *testing.T, m *Manager, name, dir string) string {
	t.Helper()
	clone := m.marketplaceDir(name)
	origRemoveAll := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = origRemoveAll })
	marketplaceRemoveAll = func(path string) error {
		if path == clone {
			return errors.New("injected removal failure")
		}
		return origRemoveAll(path)
	}
	if _, err := m.EditMarketplace(context.Background(), name, "", &Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("EditMarketplace to directory: %v", err)
	}
	marketplaceRemoveAll = origRemoveAll
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("the injected removal failure did not strand the clone at %s: %v", clone, err)
	}
	return clone
}

// A directory source's install location is its own path, never the store's
// clone directory, so a directory under <marketplaces>/<name> can only be a
// stale clone — here the one a git->directory re-source failed to remove.
// RemoveMarketplace must sweep it whatever the recorded kind, or it outlives the
// marketplace and blocks the name for a later rename.
func TestRemoveMarketplace_SweepsAStrandedCloneUnderADirectorySource(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	dir := makeDirectoryMarketplace(t, "local", "gadget")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	clone := strandCloneAfterDirectoryResource(t, m, name, dir)

	if err := m.RemoveMarketplace(context.Background(), name); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the stranded clone %s outlived the marketplace: %v", clone, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("the directory source itself was removed: %v", err)
	}
}

// The same stale clone blocks a rename: leaving it under the old name means
// refuseLeftoversUnder reports it as a removed marketplace's residue, so the
// freed name cannot be reused until someone deletes it by hand. Renaming a
// directory-sourced marketplace moves no clone of its own, but it must clear a
// stale one under the old name — and not park it under the new name instead.
func TestEditMarketplace_RenameSweepsAStrandedCloneUnderADirectorySource(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	dir := makeDirectoryMarketplace(t, "local", "gadget")
	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	clone := strandCloneAfterDirectoryResource(t, m, name, dir)

	ref, err := m.EditMarketplace(context.Background(), name, "beta", nil)
	if err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if ref.Source.Kind != SourceDirectory || ref.InstallLocation != dir {
		t.Fatalf("rename changed the directory source: %+v", ref)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("the stranded clone %s outlived the rename: %v", clone, err)
	}
	if _, err := os.Stat(m.marketplaceDir("beta")); !os.IsNotExist(err) {
		t.Fatalf("the stale clone was parked under the new name: %v", err)
	}
	// The old name is free again, not refused as a removed marketplace's residue.
	if _, err := m.EditMarketplace(context.Background(), "beta", name, nil); err != nil {
		t.Fatalf("renaming back onto the freed name: %v", err)
	}
}

// seedLegacyInStoreDirectorySource writes a directory-source record whose path
// is the marketplace's own canonical clone directory — a store the current
// refuseSourceInStore would never write, but a pre-rule or hand-seeded store
// can hold. It returns a sentinel inside that directory so a caller can tell
// whether the live source survived.
func seedLegacyInStoreDirectorySource(t *testing.T, m *Manager, name string) string {
	t.Helper()
	clone := m.marketplaceDir(name)
	if err := os.MkdirAll(filepath.Join(clone, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(clone, ".claude-plugin", "marketplace.json")
	if err := os.WriteFile(sentinel, []byte(`{"name":"`+name+`","owner":{"name":"o"},"plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{name: {
		Source:          Source{Kind: SourceDirectory, Path: clone},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}
	return sentinel
}

// The store refuses a directory source inside itself today, so a directory at
// <marketplaces>/<name> is normally residue. A record written before that rule
// (or seeded by hand) can still name the clone path as its source, and then the
// directory is the live source: RemoveMarketplace must not delete it.
func TestRemoveMarketplace_SparesADirectorySourceThatIsTheClonePath(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := seedLegacyInStoreDirectorySource(t, m, "acme")

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the live directory source was deleted by removal: %v", err)
	}
}

// The same legacy record on the rename path: the clone path is the live source,
// so a rename must leave it in place rather than sweeping it as stale residue.
func TestEditMarketplace_RenameSparesADirectorySourceThatIsTheClonePath(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := seedLegacyInStoreDirectorySource(t, m, "acme")

	ref, err := m.EditMarketplace(context.Background(), "acme", "beta", nil)
	if err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if ref.Source.Kind != SourceDirectory || ref.InstallLocation == "" {
		t.Fatalf("rename lost the directory source: %+v", ref)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the live directory source was deleted by the rename: %v", err)
	}
}

// seedSweepStore creates the clone directory for name and writes the given
// marketplaces file, so a test can express exactly which record (if any) names
// the clone path, or an ancestor of it, as a directory source.
func seedSweepStore(t *testing.T, m *Manager, name string, mk Marketplaces) string {
	t.Helper()
	clone := m.marketplaceDir(name)
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(clone, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("clone contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	return sentinel
}

// An ancestor directory source contains the clone but is not deleted by
// RemoveAll, so it must not protect the sweep: the stale clone still has to go,
// or the name stays blocked and cannot be reused.
func TestRemoveMarketplace_SweepsWhenADirectorySourceIsAnAncestor(t *testing.T) {
	m := NewManager(t.TempDir())
	ancestor := m.marketplacesDir()
	clone := m.marketplaceDir("acme")
	seedSweepStore(t, m, "acme", Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: ancestor},
		InstallLocation: ancestor,
	}})

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("an ancestor source over-protected the stale clone %s: %v", clone, err)
	}
}

// The rename path shares the guard: an ancestor source must not leave the stale
// clone in place there either.
func TestEditMarketplace_RenameSweepsWhenADirectorySourceIsAnAncestor(t *testing.T) {
	m := NewManager(t.TempDir())
	ancestor := m.marketplacesDir()
	clone := m.marketplaceDir("acme")
	seedSweepStore(t, m, "acme", Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: ancestor},
		InstallLocation: ancestor,
	}})

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("an ancestor source over-protected the stale clone %s: %v", clone, err)
	}
}

// A source recorded beneath the clone is deleted with it even when it is a
// symlink resolving outside: RemoveAll removes the link, leaving the registered
// marketplace pointing at a missing path. The guard must protect the recorded
// path itself, not only its resolved target.
func TestRemoveMarketplace_SparesASourceSymlinkedOutOfTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(clone, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: link},
		InstallLocation: link,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the symlinked source beneath the clone was swept: %v", err)
	}
}

func TestEditMarketplace_RenameSparesASourceSymlinkedOutOfTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(clone, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: link},
		InstallLocation: link,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the symlinked source beneath the clone was swept by the rename: %v", err)
	}
}

// The guard checks every registered marketplace's directory source, not only
// the one being swept: a hand-seeded store can record one marketplace against
// another's clone path, and removing one must not delete the other's source.
func TestRemoveMarketplace_SparesAnotherMarketplacesSourceAtTheClonePath(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	sentinel := seedSweepStore(t, m, "acme", Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	})

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("another marketplace's source at the clone path was deleted: %v", err)
	}
}

func TestEditMarketplace_RenameSparesAnotherMarketplacesSourceAtTheClonePath(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	sentinel := seedSweepStore(t, m, "acme", Marketplaces{
		"acme": {Source: Source{Kind: SourceDirectory, Path: t.TempDir()}, InstallLocation: t.TempDir()},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	})

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("another marketplace's source at the clone path was deleted by the rename: %v", err)
	}
}

// A git marketplace's rename moves its clone with the name, so when another
// marketplace records a directory source at that path, the move would strip the
// source out from under a live record. Refuse rather than do that, and leave the
// store untouched.
func TestEditMarketplace_RenameRefusesToMoveAnotherMarketplacesSource(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	sentinel := seedSweepStore(t, m, "acme", Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	})

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", nil); err == nil {
		t.Fatal("rename moved a clone that another marketplace records as its source")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the other marketplace's source was displaced despite the refusal: %v", err)
	}
	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mk["acme"]; !ok {
		t.Fatalf("failed rename lost the marketplace: %v", mk)
	}
}

// seedPhysicalSourceBehindSymlinkedStore makes the store's marketplaces
// directory a symlink and seeds name with a directory source recorded by its
// physical path — the layout a store on a symlinked root can hold. It returns
// the physical source path and a sentinel inside it.
func seedPhysicalSourceBehindSymlinkedStore(t *testing.T, m *Manager, elsewhere, name string) (physical, sentinel string) {
	t.Helper()
	physical = filepath.Join(elsewhere, name)
	if err := os.MkdirAll(physical, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel = filepath.Join(physical, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{name: {
		Source:          Source{Kind: SourceDirectory, Path: physical},
		InstallLocation: physical,
	}}); err != nil {
		t.Fatal(err)
	}
	return physical, sentinel
}

// The clone path is routinely compared as recorded, but the store's marketplaces
// directory can itself be a symlink, and a record can name the physical path.
// The guard must resolve the clone too, or the sweep walks the symlink and
// deletes the live source behind it.
func TestRemoveMarketplace_SparesAPhysicalSourceUnderASymlinkedStore(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, marketplacesDirName)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root)
	_, sentinel := seedPhysicalSourceBehindSymlinkedStore(t, m, elsewhere, "acme")

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the physical source behind the symlinked store was deleted: %v", err)
	}
}

func TestEditMarketplace_RenameSparesAPhysicalSourceUnderASymlinkedStore(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, marketplacesDirName)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root)
	_, sentinel := seedPhysicalSourceBehindSymlinkedStore(t, m, elsewhere, "acme")

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the physical source behind the symlinked store was deleted by the rename: %v", err)
	}
}

// A source can be reached through a symlink chain that dips into the clone and
// back out: a link inside the clone points elsewhere, and the recorded source
// path is a link outside the clone pointing at that link. The final resolved
// target is outside the clone, but deleting the clone removes the hop and breaks
// the source, so the guard has to walk the links, not just resolve them.
func TestRemoveMarketplace_SparesASourceWhoseLinkPassesThroughTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	hop := filepath.Join(clone, "hop")
	if err := os.Symlink(t.TempDir(), hop); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "entry")
	if err := os.Symlink(hop, entry); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: entry},
		InstallLocation: entry,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Lstat(hop); err != nil {
		t.Fatalf("the link hop inside the clone was swept: %v", err)
	}
}

func TestEditMarketplace_RenameSparesASourceWhoseLinkPassesThroughTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	hop := filepath.Join(clone, "hop")
	if err := os.Symlink(t.TempDir(), hop); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "entry")
	if err := os.Symlink(hop, entry); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: entry},
		InstallLocation: entry,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Lstat(hop); err != nil {
		t.Fatalf("the link hop inside the clone was swept by the rename: %v", err)
	}
}

// The link into the clone can itself be the target of a second link outside it.
// The walk has to follow each link's own target, not only the recorded path's
// components, or the second hop is never inspected and the clone is swept.
func TestRemoveMarketplace_SparesASourceThroughAChainedLinkIntoTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	hop := filepath.Join(clone, "hop")
	if err := os.Symlink(t.TempDir(), hop); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(t.TempDir(), "mid")
	if err := os.Symlink(hop, mid); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "entry")
	if err := os.Symlink(mid, entry); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: entry},
		InstallLocation: entry,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Lstat(hop); err != nil {
		t.Fatalf("the link hop inside the clone was swept through a chained link: %v", err)
	}
}

func TestEditMarketplace_RenameSparesASourceThroughAChainedLinkIntoTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	hop := filepath.Join(clone, "hop")
	if err := os.Symlink(t.TempDir(), hop); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(t.TempDir(), "mid")
	if err := os.Symlink(hop, mid); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(t.TempDir(), "entry")
	if err := os.Symlink(mid, entry); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: entry},
		InstallLocation: entry,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if _, err := os.Lstat(hop); err != nil {
		t.Fatalf("the link hop inside the clone was swept through a chained link by the rename: %v", err)
	}
}

// The sweep is a no-op when the marketplace has no clone, so an unreadable
// directory source recorded by some other marketplace must not fail the whole
// removal: the guard short-circuits on the absent clone before inspecting them.
func TestRemoveMarketplace_SucceedsWithoutACloneDespiteAnUnreadableSource(t *testing.T) {
	m := NewManager(t.TempDir())
	unreadable := filepath.Join(t.TempDir(), "beta-source")
	if err := m.saveMarketplaces(Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}},
		"beta": {Source: Source{Kind: SourceDirectory, Path: unreadable}, InstallLocation: unreadable},
	}); err != nil {
		t.Fatal(err)
	}
	origLstat := marketplaceLstat
	t.Cleanup(func() { marketplaceLstat = origLstat })
	marketplaceLstat = func(path string) (os.FileInfo, error) {
		if path == unreadable {
			return nil, fmt.Errorf("injected lstat failure for %s: %w", path, os.ErrPermission)
		}
		return origLstat(path)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("removing a clone-less marketplace failed on another record's unreadable source: %v", err)
	}
}

// When the clone path itself is a symlink, the sweep removes the link, not its
// target. A source recorded at the target is therefore not deleted and must not
// be protected, or the stale clone link would survive and hold the name.
func TestRemoveMarketplace_SweepsACloneSymlinkWithoutProtectingItsTarget(t *testing.T) {
	m := NewManager(t.TempDir())
	target := t.TempDir()
	sentinel := filepath.Join(target, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, clone); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: target},
		InstallLocation: target,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Lstat(clone); !os.IsNotExist(err) {
		t.Fatalf("the clone symlink survived (its target over-protected): %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the source at the link target was deleted: %v", err)
	}
}

// A clone link whose target cannot be read — here a self-loop — is still an
// entry the sweep must clear. pathPresent stats through the link, so using it
// here would error, over-protect, and leave the link holding the name.
func TestRemoveMarketplace_ClearsAnUnreadableCloneSymlink(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(clone, clone); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
	}}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Lstat(clone); !os.IsNotExist(err) {
		t.Fatalf("the unreadable clone symlink survived removal: %v", err)
	}
}

// The directory-source rename sweeps a stale clone rather than moving it, so it
// must use the entry's own presence: an unreadable link it could not stat
// through must not turn a rename that never touches the clone into a failure.
func TestEditMarketplace_DirectoryRenameClearsAnUnreadableCloneSymlink(t *testing.T) {
	m := NewManager(t.TempDir())
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(clone, clone); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: dir},
		InstallLocation: dir,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("a directory rename failed on an unreadable clone symlink: %v", err)
	}
	if _, err := os.Lstat(clone); !os.IsNotExist(err) {
		t.Fatalf("the stale clone symlink survived the rename: %v", err)
	}
}

// The git->directory re-source without a rename removes the old clone after
// saving, which is a sweep like any other: it must not delete a path another
// marketplace records as a directory source.
func TestEditMarketplace_ResourceToDirectorySparesAnotherMarketplacesSource(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	sentinel := seedSweepStore(t, m, "acme", Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	})
	dir := makeDirectoryMarketplace(t, "local", "widget")

	if _, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("EditMarketplace to directory: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the re-source deleted another marketplace's source at the clone path: %v", err)
	}
}

// The directory->git re-source replaces the destination through swapInClone,
// then deletes the old contents: a destination another marketplace records as a
// directory source must be refused, not overwritten.
func TestEditMarketplace_ResourceToGitRefusesAnotherMarketplacesSource(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(clone, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("beta source"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	if err := m.saveMarketplaces(Marketplaces{
		"acme": {Source: Source{Kind: SourceDirectory, Path: dir}, InstallLocation: dir},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	}); err != nil {
		t.Fatal(err)
	}
	repo := makeMarketplaceRepo(t, "acme")

	if _, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceURL, URL: repo}); err == nil {
		t.Fatal("expected the re-source to refuse a destination another marketplace sources")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the re-source overwrote another marketplace's source: %v", err)
	}
}

// The rename branch moves the clone with the name before the same cleanup runs;
// its displaced-source refusal must hold there too, so a rename cannot take
// another marketplace's source with it.
func TestEditMarketplace_RenameAndResourceToDirectorySparesAnotherMarketplacesSource(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	sentinel := seedSweepStore(t, m, "acme", Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: clone}, InstallLocation: clone},
	})
	dir := makeDirectoryMarketplace(t, "local", "widget")

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", &Source{Kind: SourceDirectory, Path: dir}); err == nil {
		t.Fatal("expected the rename to refuse moving a clone another marketplace sources")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the rename displaced another marketplace's source: %v", err)
	}
}

// The incoming directory source can itself sit inside the clone — a symlink
// there pointing outside the store passes refuseSourceInStore — and the cleanup
// would delete it. The guard has to see the incoming path, not just the
// already-registered ones.
func TestEditMarketplace_ResourceToDirectorySparesTheIncomingSourceBeneathTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := makeDirectoryMarketplace(t, "local", "widget")
	link := filepath.Join(clone, "source")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceDirectory, Path: link}); err != nil {
		t.Fatalf("EditMarketplace to a source inside the clone: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the incoming source beneath the clone was swept: %v", err)
	}
}

// A source written as `<clone>/../outside` needs the clone to exist — the OS
// cannot apply `..` to a component that is gone — even though canonicalizing it
// first makes it look like a plain sibling. The guard must walk it as written.
func TestRemoveMarketplace_SparesASourceThatTraversesTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(m.marketplacesDir(), "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	traversing := clone + string(filepath.Separator) + ".." + string(filepath.Separator) + "beta"
	if err := m.saveMarketplaces(Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: traversing}, InstallLocation: traversing},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("the source that traverses the clone did not protect it: %v", err)
	}
}

// A `..` reached through a symlink into the clone still needs the clone to
// exist: `alias/../beta` where alias points at the clone cannot resolve once
// the clone is gone. The walk must follow the link before applying the `..`.
func TestRemoveMarketplace_SparesASourceWhoseLinkTraversesTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	beta := filepath.Join(m.marketplacesDir(), "beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(clone, alias); err != nil {
		t.Fatal(err)
	}
	traversing := alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "beta"
	if err := m.saveMarketplaces(Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: traversing}, InstallLocation: traversing},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("the source traversing the clone through a link did not protect it: %v", err)
	}
}

// A rename moves the clone, so a directory source that lives inside it moves
// too while Source.Path would still name the old location. The combined edit
// must be refused rather than recorded against a path that is gone.
func TestEditMarketplace_RenameRefusesADirectorySourceInsideTheClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := makeDirectoryMarketplace(t, "local", "widget")
	link := filepath.Join(clone, "source")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", &Source{Kind: SourceDirectory, Path: link}); err == nil {
		t.Fatal("expected a rename with a directory source inside the clone to be refused")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the clone or the in-clone source moved despite the refusal: %v", err)
	}
}

// The record being re-sourced is excluded from the swap guard: its own legacy
// source at the clone path is exactly what the re-source replaces, so refusing
// would leave a state no operation could clear.
func TestEditMarketplace_ResourceToGitReplacesItsOwnLegacyCloneSource(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	m := NewManager(t.TempDir())
	seedLegacyInStoreDirectorySource(t, m, "acme")
	repo := makeMarketplaceRepo(t, "acme")

	ref, err := m.EditMarketplace(context.Background(), "acme", "", &Source{Kind: SourceURL, URL: repo})
	if err != nil {
		t.Fatalf("re-sourcing its own legacy clone-source was refused: %v", err)
	}
	if ref.Source.Kind != SourceURL {
		t.Fatalf("Source = %+v, want the git source", ref.Source)
	}
	if _, err := os.Stat(filepath.Join(ref.InstallLocation, ".claude-plugin", "marketplace.json")); err != nil {
		t.Fatalf("the re-sourced clone is not there: %v", err)
	}
}

// The rename moves the plugin cache as well as the clone, so a registered
// source living under the old cache must refuse the rename rather than be moved
// out from under its record.
func TestEditMarketplace_RenameRefusesASourceUnderTheOldCache(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheEntry := filepath.Join(m.cacheDir(), "acme")
	if err := os.MkdirAll(cacheEntry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{
		"acme": {Source: Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"}, InstallLocation: clone},
		"beta": {Source: Source{Kind: SourceDirectory, Path: cacheEntry}, InstallLocation: cacheEntry},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", nil); err == nil {
		t.Fatal("expected the rename to refuse moving a cache another marketplace sources")
	}
	if _, err := os.Stat(cacheEntry); err != nil {
		t.Fatalf("the cache another marketplace sources was moved: %v", err)
	}
}

// The same cache move must refuse an incoming directory source that sits inside
// the old cache, since the rename would carry it to the new name while
// Source.Path still names the old location.
func TestEditMarketplace_RenameRefusesAnIncomingSourceUnderTheOldCache(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheEntry := filepath.Join(m.cacheDir(), "acme")
	if err := os.MkdirAll(cacheEntry, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := makeDirectoryMarketplace(t, "local", "widget")
	link := filepath.Join(cacheEntry, "source")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/acme.git"},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", &Source{Kind: SourceDirectory, Path: link}); err == nil {
		t.Fatal("expected the rename to refuse an incoming source inside the old cache")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the cache or its incoming source moved despite the refusal: %v", err)
	}
}

// A combined rename and re-source replaces the edited record's own source, so
// that source must not block the cache move: only other records' sources, and
// the incoming source, are data to protect.
func TestEditMarketplace_RenameAndResourceReplacesASourceUnderTheOldCache(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	oldSource := filepath.Join(m.cacheDir(), "acme", "source")
	if err := os.MkdirAll(oldSource, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: oldSource},
		InstallLocation: oldSource,
	}}); err != nil {
		t.Fatal(err)
	}
	dir := makeDirectoryMarketplace(t, "local", "widget")

	ref, err := m.EditMarketplace(context.Background(), "acme", "gamma", &Source{Kind: SourceDirectory, Path: dir})
	if err != nil {
		t.Fatalf("a re-source replacing its own old cache source was refused: %v", err)
	}
	if ref.InstallLocation != dir || ref.Source.Path != dir {
		t.Fatalf("ref = %+v, want the incoming source %q", ref, dir)
	}
}

// The directory-branch sweep is destructive and runs before either store file
// is saved, so it must not delete the edited record's own source ahead of a
// commit that can still fail. A save failure must leave that source readable.
func TestEditMarketplace_FailedRenameAndResourceKeepsASourceUnderTheOldClone(t *testing.T) {
	m := NewManager(t.TempDir())
	oldSource := filepath.Join(m.marketplaceDir("acme"), "source")
	if err := os.MkdirAll(oldSource, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(oldSource, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: oldSource},
		InstallLocation: oldSource,
	}}); err != nil {
		t.Fatal(err)
	}
	dir := makeDirectoryMarketplace(t, "local", "widget")
	origWrite := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, marketplacesFileName) {
			return errors.New("injected save failure")
		}
		return origWrite(path, data, perm)
	}

	if _, err := m.EditMarketplace(context.Background(), "acme", "gamma", &Source{Kind: SourceDirectory, Path: dir}); err == nil {
		t.Fatal("expected the injected save failure to fail the edit")
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("the failed edit deleted a source under the old clone: %v", err)
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
	list, _ := m.ListMarketplaces(context.Background())
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
	list, _ := m.ListMarketplaces(context.Background())
	if _, ok := list[name]; !ok || len(list) != 1 {
		t.Fatalf("list changed after a failed rename: %v", list)
	}
	reg, _ := m.loadRegistry()
	if _, ok := reg.Plugins[registryKey("widget", name)]; !ok {
		t.Fatalf("registry changed after a failed rename: %v", reg.Plugins)
	}
}

// A rollback that fails leaves a directory under a name nothing records, so
// the store is left changed and the edit has to say so. What the caller gets
// is the marketplace whose change could not be rolled back; the absolute
// plugin-store paths the rollback renames carried go to the hub's log, as
// saveFailed's already do (#1854).
func TestEditMarketplace_FailedUndoNamesWhatItCouldNotRestore(t *testing.T) {
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

	// The clone directory renames; the cache directory refuses, and so does
	// the undo that would carry the clone back to its old name.
	orig := marketplaceRename
	calls := 0
	marketplaceRename = func(from, to string) error {
		calls++
		if calls >= 2 {
			return fmt.Errorf("rename %d refused", calls)
		}
		return orig(from, to)
	}
	t.Cleanup(func() { marketplaceRename = orig })

	_, err := m.EditMarketplace(ctx, name, "beta", nil)
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the rename to fail")
	}
	if !errors.Is(err, errRenameRollbackIncomplete) {
		t.Fatalf("error = %v, want errors.Is(err, errRenameRollbackIncomplete)", err)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error = %v, want the marketplace named", err)
	}
	if strings.Contains(err.Error(), m.marketplaceDir("beta")) || strings.Contains(err.Error(), m.marketplaceDir(name)) {
		t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
	}
}

// AddMarketplace has no refusal for an already-registered name, so a second
// call with the same name and a different source re-sources it in place. The
// swap then displaces the clone the marketplaces file still records, and that
// clone is the only copy of what a failed save leaves behind: it has to stay
// aside until the save lands, and go back when it does not. Deleting it at the
// swap would leave the registered marketplace pointing at the new, then removed,
// clone - a marketplace whose install location no longer exists.
func TestAddMarketplace_SaveFailureRestoresTheOldClone(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	repoA := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "acme", Source{Kind: SourceURL, URL: repoA}); err != nil {
		t.Fatalf("first AddMarketplace: %v", err)
	}
	before := readStoreFile(t, m.marketplacesFile())

	origWrite := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })

	if _, err := m.AddMarketplace(ctx, "acme", Source{Kind: SourceURL, URL: repoB}); err == nil {
		t.Fatal("expected the save to fail")
	}
	marketplaceAtomicWriteFile = origWrite

	// The failed save never overwrote the file, so the marketplace is still
	// registered against its old clone, and that clone must still be there.
	if after := readStoreFile(t, m.marketplacesFile()); after != before {
		t.Fatalf("known_marketplaces.json changed after a failed save:\n%s", after)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := mk["acme"]
	if !ok || ref.Source.URL != repoA {
		t.Fatalf("mk[acme] = %+v, %v; want the recorded source", ref, ok)
	}
	if _, err := os.Stat(filepath.Join(ref.InstallLocation, "plugins", "widget")); err != nil {
		t.Fatalf("the registered marketplace's clone is gone: stat(%s) = %v", ref.InstallLocation, err)
	}
	if _, err := os.Stat(filepath.Join(ref.InstallLocation, "plugins", "gadget")); !os.IsNotExist(err) {
		t.Fatalf("the failed re-source's clone outlived the failed save: %v", err)
	}
	for _, leftover := range []string{".old", ".staging"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("the failed save left %s behind: %v", leftover, err)
		}
	}
	// The restored clone is the recorded source's, so a browse serves the old
	// catalog without a refresh having to reclone it.
	cat, err := m.Browse(ctx, "acme")
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("Browse acme = %+v, %v; want the recorded source's catalog", cat, err)
	}
}

// A rename that also re-sources swaps the staged clone into the directory the
// rename has just moved, so its unwind has both halves to put back: the old
// clone the swap set aside goes back under the new name, and the rename undo
// then carries it back to the old one.
func TestEditMarketplace_RegistrySaveFailureRestoresTheOldClone(t *testing.T) {
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

	// The last step before the marketplaces file, and the only one that runs
	// after the swap on a rename.
	origSave := installSaveRegistry
	installSaveRegistry = func(string, Registry) error { return errors.New("boom") }
	t.Cleanup(func() { installSaveRegistry = origSave })

	if _, err := m.EditMarketplace(ctx, "acme", "beta", &Source{Kind: SourceURL, URL: repoB}); err == nil {
		t.Fatal("expected the registry save to fail")
	}
	installSaveRegistry = origSave

	if _, err := os.Stat(filepath.Join(m.marketplaceDir("acme"), "plugins", "widget")); err != nil {
		t.Fatalf("the old clone was not restored: %v", err)
	}
	for _, leftover := range []string{"beta", ".old", ".staging"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("clone directory %s outlived the failed save: %v", m.marketplaceDir(leftover), err)
		}
	}
	list, _ := m.ListMarketplaces(context.Background())
	ref, ok := list["acme"]
	if !ok || len(list) != 1 || ref.Source.URL != repoA {
		t.Fatalf("the failed edit changed the store: %v", list)
	}
	reg, _ := m.loadRegistry()
	if _, ok := reg.Plugins[registryKey("widget", "acme")]; !ok {
		t.Fatalf("registry changed after a failed save: %v", reg.Plugins)
	}
	// The restored clone is the recorded source's, not the source the failed
	// edit had already fetched.
	cat, err := m.Browse(ctx, "acme")
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("Browse acme = %+v, %v; want the recorded source's catalog", cat, err)
	}
}

// The marketplaces file is saved last, so a failure there is the deepest a
// re-source reaches before it has to unwind. The swap renames the old clone
// aside instead of deleting it, so the unwind puts the old source's own files
// back under the install location the surviving file still records — a failed
// edit changes nothing, and no refetch is owed.
func TestEditMarketplace_MarketplacesSaveFailureRestoresTheOldClone(t *testing.T) {
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
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}

	// saveMarketplaces is the only writer through this seam, so the edit gets
	// all the way past the swap before it fails.
	origWrite := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })

	if _, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB}); err == nil {
		t.Fatal("expected the save to fail")
	}
	marketplaceAtomicWriteFile = origWrite

	// Each repo carries only its own plugin directory, so the clone says which
	// source's files are in it.
	if _, err := os.Stat(filepath.Join(m.marketplaceDir("acme"), "plugins", "widget")); err != nil {
		t.Fatalf("the old clone was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.marketplaceDir("acme"), "plugins", "gadget")); !os.IsNotExist(err) {
		t.Fatalf("the new source's clone outlived the failed save: %v", err)
	}
	for _, leftover := range []string{".old", ".staging"} {
		if _, err := os.Stat(m.marketplaceDir(leftover)); !os.IsNotExist(err) {
			t.Fatalf("the failed save left %s behind: %v", leftover, err)
		}
	}
	for path, want := range before {
		if after := readStoreFile(t, path); after != want {
			t.Fatalf("%s changed after a failed save:\n%s", path, after)
		}
	}
	// The restored clone is the recorded source's, so a browse serves the old
	// catalog without a refresh having to reclone it.
	cat, err := m.Browse(ctx, "acme")
	if err != nil || len(cat.Plugins) != 1 || cat.Plugins[0].Name != "widget" {
		t.Fatalf("Browse acme = %+v, %v; want the recorded source's catalog", cat, err)
	}
}

// The marketplaces file is saved last, after the registry has already taken
// the rename. When it fails, that file still names the marketplace — and the
// install location inside it — under the OLD name, so everything the edit
// moved goes back to match it: the directories, and the registry entries the
// re-key moved. Left half-renamed, the directories would be orphaned by the
// very next refresh and the registry would point installed plugins at a cache
// directory the undo has renamed away.
func TestEditMarketplace_MarketplacesSaveFailureRestoresTheStore(t *testing.T) {
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
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}

	// saveMarketplaces is the only writer through this seam, so the rename
	// gets all the way past the registry save before it fails — and the
	// registry restore, which does not go through it, still reaches disk.
	origWrite := marketplaceAtomicWriteFile
	marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
	t.Cleanup(func() { marketplaceAtomicWriteFile = origWrite })

	_, err := m.EditMarketplace(ctx, name, "beta", nil)
	marketplaceAtomicWriteFile = origWrite
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	if !strings.Contains(err.Error(), marketplacesFileName) {
		t.Fatalf("error = %v, want it to name %s", err, marketplacesFileName)
	}
	for _, dir := range []string{m.marketplaceDir(name), filepath.Join(m.cacheDir(), name)} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s was not restored: %v", dir, err)
		}
	}
	for _, dir := range []string{m.marketplaceDir("beta"), filepath.Join(m.cacheDir(), "beta")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s outlived the failed save: %v", dir, err)
		}
	}
	for path, want := range before {
		if after := readStoreFile(t, path); after != want {
			t.Fatalf("%s changed after a failed save:\n%s", path, after)
		}
	}
}

// readStoreFile reads one of the store's JSON files, for the tests that assert
// a failed edit left it byte-identical.
// readStoreFileIfAny reads one of the store's JSON files, or "" when the store
// has not written it yet, for the tests that assert a refused edit left it as
// it was.
func readStoreFileIfAny(path string) string {
	body, _ := os.ReadFile(path)
	return string(body)
}

func readStoreFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestEditMarketplace_RefusesALeftoverPluginCache(t *testing.T) {
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
	// What removing a marketplace named beta leaves behind: RemoveMarketplace
	// drops the clone and the registration, never the plugin cache.
	leftover := filepath.Join(m.cacheDir(), "beta")
	if err := os.MkdirAll(filepath.Join(leftover, "widget", "deadsha"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := m.EditMarketplace(ctx, name, "beta", nil)
	if err == nil {
		t.Fatal("expected the rename to be refused")
	}
	// A name a leftover still occupies is taken, exactly as one the
	// marketplaces file records is: the caller's conflict, not a store failure.
	if !errors.Is(err, ErrMarketplaceExists) {
		t.Fatalf("error = %v, want ErrMarketplaceExists", err)
	}
	// Where an os.Rename LinkError said only "directory not empty" - and
	// without naming the absolute store path the leftover sits at (#1854).
	if want := `plugin cache for "beta" already exists`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), leftover) {
		t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
	}
	if _, err := os.Stat(m.marketplaceDir(name)); err != nil {
		t.Fatalf("the clone was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir(), name)); err != nil {
		t.Fatalf("the plugin cache moved anyway: %v", err)
	}
	list, _ := m.ListMarketplaces(context.Background())
	if _, ok := list[name]; !ok || len(list) != 1 {
		t.Fatalf("list changed after a refused rename: %v", list)
	}
}

// RemoveMarketplace drops a marketplace's clone and its registration but never
// its plugin cache or its registry entries, and a failed edit can strand a
// clone, so any of the three can outlive the marketplace that made it. The
// rename is refused for each of them whether or not the marketplace being
// renamed has anything of its own to move — an install-free one moves neither
// a cache nor a registry entry, and used to walk straight past the check.
func TestEditMarketplace_RenameRefusesEveryLeftoverUnderTheNewName(t *testing.T) {
	leftovers := map[string]func(t *testing.T, m *Manager){
		"plugin cache": func(t *testing.T, m *Manager) {
			if err := os.MkdirAll(filepath.Join(m.cacheDir(), "beta", "widget", "deadsha"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"registry entry": func(t *testing.T, m *Manager) {
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			reg.Plugins[registryKey("widget", "beta")] = []InstallEntry{{
				InstallPath: filepath.Join(m.cacheDir(), "beta", "widget", "deadsha"),
				Source:      Source{Kind: SourceDirectory, Path: filepath.Join(m.cacheDir(), "beta")},
			}}
			if err := m.saveRegistry(reg); err != nil {
				t.Fatal(err)
			}
		},
		"marketplace clone": func(t *testing.T, m *Manager) {
			if err := os.MkdirAll(m.marketplaceDir("beta"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	for what, plant := range leftovers {
		t.Run(what, func(t *testing.T) {
			dir := makeDirectoryMarketplace(t, "acme", "widget")
			m := NewManager(t.TempDir())
			ctx := context.Background()
			if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
				t.Fatalf("AddMarketplace: %v", err)
			}
			plant(t, m)
			regBefore := readStoreFileIfAny(m.registryPath())
			mkBefore := readStoreFileIfAny(m.marketplacesFile())

			_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
			if err == nil {
				t.Fatal("expected the rename to be refused")
			}
			if !errors.Is(err, ErrMarketplaceExists) {
				t.Fatalf("error = %v, want ErrMarketplaceExists", err)
			}
			if want := `before "beta" can be reused`; !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want it to contain %q", err, want)
			}
			if got := readStoreFileIfAny(m.registryPath()); got != regBefore {
				t.Fatalf("%s changed after a refused rename:\n%s", registryFileName, got)
			}
			if got := readStoreFileIfAny(m.marketplacesFile()); got != mkBefore {
				t.Fatalf("%s changed after a refused rename:\n%s", marketplacesFileName, got)
			}
		})
	}
}

// A dangling symlink is there as far as a rename onto it is concerned, but
// os.Stat follows it and reports the missing target, so a check built on Stat
// alone reads the name as free and commits the store files over residue an
// interrupted cleanup left behind.
func TestEditMarketplace_RenameRefusesADanglingLinkUnderTheNewName(t *testing.T) {
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(m.cacheDir(), "beta")
	if err := os.Symlink(filepath.Join(m.cacheDir(), "gone"), leftover); err != nil {
		t.Fatal(err)
	}

	_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
	if !errors.Is(err, ErrMarketplaceExists) {
		t.Fatalf("error = %v, want ErrMarketplaceExists", err)
	}
	if want := `plugin cache for "beta" already exists`; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	if strings.Contains(err.Error(), leftover) {
		t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
	}
	list, _ := m.ListMarketplaces(context.Background())
	if _, ok := list["acme"]; !ok || len(list) != 1 {
		t.Fatalf("list changed after a refused rename: %v", list)
	}
}

// A rename the leftover check is going to refuse is refused before the network
// step, so a doomed edit never pays for a clone.
func TestEditMarketplace_RenameOntoALeftoverIsRefusedBeforeTheFetch(t *testing.T) {
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	m := NewManager(t.TempDir())
	ctx := context.Background()
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(m.cacheDir(), "beta", "widget", "deadsha"), 0o755); err != nil {
		t.Fatal(err)
	}
	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	cloned := false
	marketplaceGitClone = func(context.Context, string, string, string, string) error {
		cloned = true
		return nil
	}

	_, err := m.EditMarketplace(ctx, "acme", "beta", &Source{Kind: SourceURL, URL: "https://example.invalid/repo.git"})
	if !errors.Is(err, ErrMarketplaceExists) {
		t.Fatalf("error = %v, want ErrMarketplaceExists", err)
	}
	if cloned {
		t.Fatal("the new source was fetched for a rename the leftover check refuses")
	}
	if _, err := os.Stat(m.marketplaceDir(stagingCloneName)); !os.IsNotExist(err) {
		t.Fatalf("a staging directory was created: %v", err)
	}
}

// A stat that fails for any reason but "not there" leaves the edit unable to
// say whether a directory is present, and a check that read such an error as
// absence walked straight past what it could not see: the rename skipped a
// clone or a plugin cache and committed both files under the new name anyway,
// stranding the directory under the old one while the store's paths named a
// directory that is not there; the leftover check waved a removed
// marketplace's residue through to be buried under a live name; and the swap
// installed a fresh clone over an install location it had never managed to
// inspect. Each check now refuses the edit and names the path it could not
// read.
//
// A symlink loop is the stat error these tests can plant on one exact path.
// Making a parent directory unreadable instead would fail every check beneath
// it at once — both names' clones share a parent — leaving every check but the
// first untested.
func TestEditMarketplace_TreatsOnlyAMissingPathAsAbsent(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	ctx := context.Background()
	// loopAt replaces path with a symlink to itself, so a stat of it reports a
	// symlink loop rather than a missing path.
	loopAt := func(t *testing.T, path string) {
		t.Helper()
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, path); err != nil {
			t.Fatal(err)
		}
	}
	// installed registers the git marketplace "acme" with one plugin
	// installed, so a rename has both a clone and a plugin cache to move.
	installed := func(t *testing.T) *Manager {
		t.Helper()
		mktRepo, name := makeInstallableMarketplace(t)
		m := NewManager(t.TempDir())
		if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
			t.Fatalf("AddMarketplace: %v", err)
		}
		if _, err := m.Install(ctx, "widget", name); err != nil {
			t.Fatalf("Install: %v", err)
		}
		return m
	}
	// storeUnchanged captures both store files, and the returned check pins
	// them byte for byte across the refused edit.
	storeUnchanged := func(t *testing.T, m *Manager) func() {
		t.Helper()
		mkBefore := readStoreFileIfAny(m.marketplacesFile())
		regBefore := readStoreFileIfAny(m.registryPath())
		return func() {
			t.Helper()
			if got := readStoreFileIfAny(m.marketplacesFile()); got != mkBefore {
				t.Fatalf("%s changed after a refused edit:\n%s", marketplacesFileName, got)
			}
			if got := readStoreFileIfAny(m.registryPath()); got != regBefore {
				t.Fatalf("%s changed after a refused edit:\n%s", registryFileName, got)
			}
		}
	}

	t.Run("marketplace clone", func(t *testing.T) {
		m := installed(t)
		clone := m.marketplaceDir("acme")
		loopAt(t, clone)
		checkStore := storeUnchanged(t, m)

		_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
		if err == nil {
			t.Fatal("expected the rename to be refused")
		}
		if strings.Contains(err.Error(), clone) {
			t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
		}
		if !strings.Contains(err.Error(), "acme") {
			t.Fatalf("error = %v, want the marketplace named", err)
		}
		if _, err := os.Lstat(clone); err != nil {
			t.Fatalf("the clone path changed: %v", err)
		}
		if _, err := os.Stat(m.marketplaceDir("beta")); !os.IsNotExist(err) {
			t.Fatal("a beta clone appeared")
		}
		if _, err := os.Stat(filepath.Join(m.cacheDir(), "acme")); err != nil {
			t.Fatalf("the plugin cache moved anyway: %v", err)
		}
		if _, err := os.Stat(filepath.Join(m.cacheDir(), "beta")); !os.IsNotExist(err) {
			t.Fatal("a beta plugin cache appeared")
		}
		checkStore()
	})

	// The plugin cache is checked after the clone has already been renamed, so
	// this refusal has that rename to carry back.
	t.Run("plugin cache", func(t *testing.T) {
		m := installed(t)
		cache := filepath.Join(m.cacheDir(), "acme")
		loopAt(t, cache)
		checkStore := storeUnchanged(t, m)

		_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
		if err == nil {
			t.Fatal("expected the rename to be refused")
		}
		if strings.Contains(err.Error(), cache) {
			t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
		}
		if !strings.Contains(err.Error(), "acme") {
			t.Fatalf("error = %v, want the marketplace named", err)
		}
		if _, err := os.Stat(m.marketplaceDir("acme")); err != nil {
			t.Fatalf("the clone was not carried back: %v", err)
		}
		if _, err := os.Stat(m.marketplaceDir("beta")); !os.IsNotExist(err) {
			t.Fatal("a beta clone remained")
		}
		if _, err := os.Lstat(cache); err != nil {
			t.Fatalf("the plugin cache path changed: %v", err)
		}
		checkStore()
	})

	// A leftover the check cannot read is not a leftover it can declare
	// absent: this marketplace has no cache of its own to move, so nothing
	// else would have tripped over the residue under the new name.
	t.Run("leftover under the new name", func(t *testing.T) {
		dir := makeDirectoryMarketplace(t, "acme", "widget")
		m := NewManager(t.TempDir())
		if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
			t.Fatalf("AddMarketplace: %v", err)
		}
		if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		leftover := filepath.Join(m.cacheDir(), "beta")
		loopAt(t, leftover)
		checkStore := storeUnchanged(t, m)

		_, err := m.EditMarketplace(ctx, "acme", "beta", nil)
		if err == nil {
			t.Fatal("expected the rename to be refused")
		}
		if strings.Contains(err.Error(), leftover) {
			t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
		}
		if !strings.Contains(err.Error(), "beta") {
			t.Fatalf("error = %v, want the new name named", err)
		}
		if _, err := os.Lstat(leftover); err != nil {
			t.Fatalf("the leftover path changed: %v", err)
		}
		checkStore()
	})

	// The swap renames the install location aside before installing the fresh
	// clone, and the aside path it hands back is the only record that anything
	// was displaced. An install location it could not stat is not one it can
	// install over.
	t.Run("install location of a re-source", func(t *testing.T) {
		m := installed(t)
		dest := m.marketplaceDir("acme")
		loopAt(t, dest)
		checkStore := storeUnchanged(t, m)

		repoB := makeMarketplaceRepoWithPlugin(t, "acme", "gadget")
		_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceURL, URL: repoB})
		if err == nil {
			t.Fatal("expected the re-source to be refused")
		}
		// The swap's own refusal, not the complaint of a rename it should
		// never have reached: a rename would have moved dest away, so the
		// null state below is what pins the refusal to the swap. The message
		// names the marketplace and no store path.
		if strings.Contains(err.Error(), dest) {
			t.Fatalf("error = %v, want no absolute path in the client-facing error", err)
		}
		if !strings.Contains(err.Error(), "acme") {
			t.Fatalf("error = %v, want the marketplace named", err)
		}
		if _, err := os.Lstat(dest); err != nil {
			t.Fatalf("the install location changed: %v", err)
		}
		if _, err := os.Stat(m.marketplaceDir(".staging")); !os.IsNotExist(err) {
			t.Fatal("staging directory survived")
		}
		checkStore()
	})
}

// A directory source at or under the marketplace's own managed clone would be
// swept away by the redundant-clone removal the save runs, leaving the
// marketplace registered against a directory that no longer exists.
func TestEditMarketplace_RefusesADirectorySourceInsideItsOwnClone(t *testing.T) {
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
	readMarketplacesFile := func() string {
		body, err := os.ReadFile(m.marketplacesFile())
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	before := readMarketplacesFile()

	clone := m.marketplaceDir(name)
	for _, path := range []string{clone, filepath.Join(clone, ".claude-plugin")} {
		_, err := m.EditMarketplace(ctx, name, "", &Source{Kind: SourceDirectory, Path: path})
		if !errors.Is(err, ErrMarketplaceSourceInStore) {
			t.Fatalf("source %s = %v, want ErrMarketplaceSourceInStore", path, err)
		}
		if strings.Contains(err.Error(), m.marketplacesDir()) || strings.Contains(err.Error(), m.cacheDir()) {
			t.Fatalf("source %s: err = %v, want no absolute store path in the client-facing error", path, err)
		}
		if _, err := os.Stat(filepath.Join(clone, ".claude-plugin", "marketplace.json")); err != nil {
			t.Fatalf("the clone did not survive the refusal: %v", err)
		}
		if after := readMarketplacesFile(); after != before {
			t.Fatalf("%s changed after a refused edit:\n%s", marketplacesFileName, after)
		}
		items, err := m.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].Plugin != "widget" || items[0].Marketplace != name {
			t.Fatalf("List = %+v after a refused edit", items)
		}
	}
}

// plantCatalog writes a parseable marketplace at dir, so a source the store's
// containment rule let through would really be registered: a refusal the tests
// below assert is the rule's, not a missing marketplace.json's.
func plantCatalog(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	mj := `{"name":"acme","owner":{"name":"o"},"plugins":[]}`
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "marketplace.json"), []byte(mj), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An add is the other way a doomed source gets recorded: the marketplaces dir
// and the cache are the store's to rewrite, so a marketplace added from inside
// them is registered against files the next add, refresh or install destroys.
// Add has no registered marketplace to name — the name it records comes out of
// a catalog it has not fetched yet — so the rule runs on the raw source.
func TestAddMarketplace_RefusesADirectorySourceInsideTheStore(t *testing.T) {
	inStore := []struct {
		what string
		path func(m *Manager) string
	}{
		{"the marketplaces directory itself", func(m *Manager) string { return m.marketplacesDir() }},
		{"a marketplace's clone", func(m *Manager) string { return m.marketplaceDir("beta") }},
		{"the fetch's staging directory", func(m *Manager) string { return m.marketplaceDir(stagingCloneName) }},
		{"a plugin cache directory", func(m *Manager) string { return filepath.Join(m.cacheDir(), "acme", "widget", "deadsha") }},
	}
	for _, tc := range inStore {
		t.Run(tc.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			source := plantCatalog(t, tc.path(m))

			_, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: source})
			if !errors.Is(err, ErrMarketplaceSourceInStore) {
				t.Fatalf("source %s = %v, want ErrMarketplaceSourceInStore", source, err)
			}
			// The refusal comes before the staging directory is cleared, so a
			// source there outlives the call that would have registered it.
			if _, err := os.Stat(filepath.Join(source, ".claude-plugin", "marketplace.json")); err != nil {
				t.Fatalf("the refused source did not survive: %v", err)
			}
			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(mk) != 0 {
				t.Fatalf("marketplaces = %+v after a refused add, want none", mk)
			}
			if got := readStoreFileIfAny(m.marketplacesFile()); got != "" {
				t.Fatalf("a refused add wrote %s:\n%s", marketplacesFileName, got)
			}
		})
	}

	t.Run("an empty path is refused", func(t *testing.T) {
		m := NewManager(t.TempDir())

		if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory}); !errors.Is(err, ErrMarketplaceSourceInStore) {
			t.Fatalf("an empty source path = %v, want ErrMarketplaceSourceInStore", err)
		}
	})

	t.Run("a directory outside the store is accepted", func(t *testing.T) {
		m := NewManager(t.TempDir())
		dir := makeDirectoryMarketplace(t, "acme", "widget")

		ref, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: dir})
		if err != nil {
			t.Fatalf("AddMarketplace: %v", err)
		}
		if ref.Source.Path != dir || ref.InstallLocation != dir {
			t.Fatalf("ref = %+v, want it sourced at %q", ref, dir)
		}
	})
}

// The plugin store's own directories are its to rewrite: a fetch clears
// .staging, a swap deletes .old, the marketplaces dir holds every other
// marketplace's clone, and the cache holds materialized plugins. A directory
// source pointed at any of them registers a marketplace against files the next
// add, refresh or re-source destroys.
func TestEditMarketplace_RefusesADirectorySourceInsideTheStore(t *testing.T) {
	inStore := []struct {
		what string
		path func(m *Manager) string
	}{
		{"the fetch's staging directory", func(m *Manager) string { return m.marketplaceDir(stagingCloneName) }},
		{"the swap's aside directory", func(m *Manager) string { return m.marketplaceDir(asideCloneName) }},
		{"another marketplace's clone", func(m *Manager) string { return m.marketplaceDir("beta") }},
		{"a plugin cache directory", func(m *Manager) string { return filepath.Join(m.cacheDir(), "acme", "widget", "deadsha") }},
	}
	for _, tc := range inStore {
		t.Run(tc.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			ctx := context.Background()
			if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: makeDirectoryMarketplace(t, "acme", "widget")}); err != nil {
				t.Fatalf("AddMarketplace: %v", err)
			}
			before := readStoreFile(t, m.marketplacesFile())
			source := plantCatalog(t, tc.path(m))

			_, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceDirectory, Path: source})
			if !errors.Is(err, ErrMarketplaceSourceInStore) {
				t.Fatalf("source %s = %v, want ErrMarketplaceSourceInStore", source, err)
			}
			// The refusal comes before the staging directory is cleared, so a
			// source there outlives the call that would have registered it.
			if _, err := os.Stat(filepath.Join(source, ".claude-plugin", "marketplace.json")); err != nil {
				t.Fatalf("the refused source did not survive: %v", err)
			}
			if got := readStoreFile(t, m.marketplacesFile()); got != before {
				t.Fatalf("%s changed after a refused edit:\n%s", marketplacesFileName, got)
			}
		})
	}

	// filepath.Abs("") is the process's working directory, which is a store
	// path for nobody and a marketplace for almost nobody.
	t.Run("an empty path is refused", func(t *testing.T) {
		m := NewManager(t.TempDir())
		ctx := context.Background()
		if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: makeDirectoryMarketplace(t, "acme", "widget")}); err != nil {
			t.Fatalf("AddMarketplace: %v", err)
		}

		if _, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceDirectory}); !errors.Is(err, ErrMarketplaceSourceInStore) {
			t.Fatalf("an empty source path = %v, want ErrMarketplaceSourceInStore", err)
		}
	})

	t.Run("a directory outside the store is accepted", func(t *testing.T) {
		m := NewManager(t.TempDir())
		ctx := context.Background()
		if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: makeDirectoryMarketplace(t, "acme", "widget")}); err != nil {
			t.Fatalf("AddMarketplace: %v", err)
		}
		moved := makeDirectoryMarketplace(t, "acme", "widget")

		ref, err := m.EditMarketplace(ctx, "acme", "", &Source{Kind: SourceDirectory, Path: moved})
		if err != nil {
			t.Fatalf("EditMarketplace: %v", err)
		}
		if ref.Source.Path != moved || ref.InstallLocation != moved {
			t.Fatalf("ref = %+v, want it sourced at %q", ref, moved)
		}
	})
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
	if _, err := m.EditMarketplace(ctx, "acme", "../escape", nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("a traversing name = %v, want ErrInvalidName", err)
	}
}

// A rename is refused a name that collides with the edit's own machinery: the
// two fixed scratch directories it renames through, and the '@' that separates
// plugin from marketplace in a registry key. Each is a legal path component, so
// nothing downstream would object — the clone would be renamed onto the
// directory the next re-source stages into, and a plugin installed from a
// marketplace whose name carries an '@' would key an entry no lookup splitting
// at the last '@' can find.
func TestEditMarketplace_RenameRefusesTheScratchNamesAndAt(t *testing.T) {
	refused := []struct{ newName, want string }{
		{".staging", "scratch"},
		{".old", "scratch"},
		{"acme@corp", "'@'"},
	}
	for _, tc := range refused {
		t.Run(tc.newName, func(t *testing.T) {
			dir := makeDirectoryMarketplace(t, "acme", "widget")
			m := NewManager(t.TempDir())
			ctx := context.Background()
			if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceDirectory, Path: dir}); err != nil {
				t.Fatalf("AddMarketplace: %v", err)
			}
			regBefore := readStoreFileIfAny(m.registryPath())
			mkBefore := readStoreFileIfAny(m.marketplacesFile())

			_, err := m.EditMarketplace(ctx, "acme", tc.newName, nil)
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("rename to %q = %v, want ErrInvalidName", tc.newName, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to name the rule (%q)", err, tc.want)
			}
			if _, err := os.Stat(m.marketplaceDir(tc.newName)); err == nil {
				t.Fatalf("a directory appeared under %q", tc.newName)
			}
			if got := readStoreFileIfAny(m.registryPath()); got != regBefore {
				t.Fatalf("%s changed after a refused rename:\n%s", registryFileName, got)
			}
			if got := readStoreFileIfAny(m.marketplacesFile()); got != mkBefore {
				t.Fatalf("%s changed after a refused rename:\n%s", marketplacesFileName, got)
			}
		})
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

// A rename moves exactly the entries keyed "@<oldName>", whatever else the
// key holds: a plugin's own '@' sits before that last one and parses back
// intact. An orphan a removed marketplace left under the target key loses to
// the entry moving onto it, and an install path follows the cache directory
// only when one moved.
func TestRekeyRegistry(t *testing.T) {
	oldCache, newCache := filepath.Join("cache", "acme"), filepath.Join("cache", "beta")
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey("widget", "acme"):    {{InstallPath: filepath.Join(oldCache, "widget", "abc")}},
		registryKey("wid@get", "acme"):   {{InstallPath: filepath.Join(oldCache, "wid@get", "abc")}},
		registryKey("other", "zeta"):     {{InstallPath: filepath.Join("cache", "zeta", "other", "def")}},
		registryKey("elsewhere", "acme"): {{InstallPath: filepath.Join("somewhere", "else")}},
		// An orphan a removed marketplace named beta left behind: removal drops
		// the registration but not the registry entries, so the rename target's
		// key is already taken and the live install has to win it.
		registryKey("widget", "beta"): {{InstallPath: filepath.Join(newCache, "widget", "ghost")}},
	}}
	got := rekeyRegistry(reg, registryKeyOwners(reg, Marketplaces{"acme": {}, "beta": {}, "zeta": {}}), "acme", "beta", oldCache, newCache)
	for _, plugin := range []string{"widget", "wid@get", "elsewhere"} {
		if _, still := got.Plugins[registryKey(plugin, "acme")]; still {
			t.Fatalf("old key %s survived", registryKey(plugin, "acme"))
		}
	}
	if p := got.Plugins[registryKey("widget", "beta")][0].InstallPath; p != filepath.Join(newCache, "widget", "abc") {
		t.Fatalf("InstallPath = %q, want the moved entry to beat the orphan already under that key", p)
	}
	if p := got.Plugins[registryKey("wid@get", "beta")][0].InstallPath; p != filepath.Join(newCache, "wid@get", "abc") {
		t.Fatalf("a plugin whose name carries '@' = %q, want it moved with its path", p)
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

	// With no cache directory moved, the keys move and every path stays.
	unmoved := rekeyRegistry(reg, registryKeyOwners(reg, Marketplaces{"acme": {}, "beta": {}, "zeta": {}}), "acme", "beta", "", "")
	if p := unmoved.Plugins[registryKey("widget", "beta")][0].InstallPath; p != filepath.Join(oldCache, "widget", "abc") {
		t.Fatalf("InstallPath = %q, want it left under the cache directory that did not move", p)
	}
}

// A key can end in more than one recorded name, and the longest is the
// marketplace it belongs to: migrating the shorter name has to leave the longer
// one's entries alone, or the longer marketplace's own migration later finds no
// keys matching it and its installs are silently reassigned.
func TestRekeyRegistry_ALongerRecordedNameKeepsItsKeys(t *testing.T) {
	mk := Marketplaces{"z": {}, "y@z": {}, "x@y@z": {}}
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey("plug", "z"):     {{InstallPath: "cache/z/plug/abc"}},
		registryKey("plug", "y@z"):   {{InstallPath: "cache/y@z/plug/abc"}},
		registryKey("wid@x", "y@z"):  {{InstallPath: "cache/y@z/wid@x/abc"}},
		registryKey("plug", "x@y@z"): {{InstallPath: "cache/x@y@z/plug/abc"}},
	}}

	got := rekeyRegistry(reg, registryKeyOwners(reg, mk), "z", "z-moved", "", "")
	for _, kept := range []string{
		registryKey("plug", "y@z"),
		registryKey("wid@x", "y@z"),
		registryKey("plug", "x@y@z"),
	} {
		if _, still := got.Plugins[kept]; !still {
			t.Fatalf("%s was taken by the migration of the shorter name; keys = %v", kept, got.Plugins)
		}
	}
	if _, moved := got.Plugins[registryKey("plug", "z-moved")]; !moved {
		t.Fatalf("keys = %v, want the shorter name's own key moved", got.Plugins)
	}
}

// Ownership is taken once for a pass, from the store as found. A rename writes
// a key under the name it took, and if ownership were recomputed against the
// names as they stand, a later rename whose name happens to match that key's
// suffix would claim it and assign its plugin and install path to the wrong
// marketplace.
func TestRekeyRegistry_KeepsOwnershipAcrossARenameInTheSamePass(t *testing.T) {
	mk := Marketplaces{"x@y": {}, "get@x-y": {}}
	reg := Registry{Version: 2, Plugins: map[string][]InstallEntry{
		"wid@get@x@y": {{InstallPath: "cache/x@y/wid@get/abc"}},
	}}
	owners := registryKeyOwners(reg, mk)

	// The first rename writes "wid@get@x-y", whose suffix "get@x-y" matches
	// another recorded name: this is the hazard the pass must not walk into.
	after := rekeyRegistry(reg, owners, "x@y", "x-y", "", "")
	if _, ok := after.Plugins["wid@get@x-y"]; !ok {
		t.Fatalf("keys = %v, want the key moved onto the name the rename took", after.Plugins)
	}
	if recomputed, _ := registryKeyOwner("wid@get@x-y", mk); recomputed != "get@x-y" {
		t.Fatalf("fixture no longer models the hazard: recomputing would name %q", recomputed)
	}

	// The second rename must leave it with the name it was keyed under.
	after = rekeyRegistry(after, owners, "get@x-y", "get-x-y", "", "")
	if _, stolen := after.Plugins["wid@get-x-y"]; stolen {
		t.Fatalf("keys = %v, want the key left with the name the first rename wrote", after.Plugins)
	}
	if _, kept := after.Plugins["wid@get@x-y"]; !kept {
		t.Fatalf("keys = %v, want the key kept under the name the first rename wrote", after.Plugins)
	}
}
