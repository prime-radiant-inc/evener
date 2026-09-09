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

	list, err := m.ListMarketplaces()
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := list["acme"]; !ok {
		t.Fatalf("marketplace 'acme' not listed: %v", list)
	}

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	list, _ = m.ListMarketplaces()
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
	mk, _ := m.ListMarketplaces()
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
	mk, _ := m.ListMarketplaces()
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
	before, err := m.ListMarketplaces()
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

func TestEditMarketplace_RenameMovesACloneTheEntryDoesNotRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	ctx := context.Background()
	// The shape a failed lazy fetch leaves: a git source recorded without an
	// install location, and a directory at the canonical path anyway, because
	// the fetch cleared and refilled it before it failed.
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
	list, err := m.ListMarketplaces()
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

// A rollback that fails leaves a directory under a name nothing records, and
// the only place that can say which one is the error the edit returns.
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
	if !strings.Contains(err.Error(), "renaming plugin cache") || !strings.Contains(err.Error(), "rename 2 refused") {
		t.Fatalf("error = %v, want the original failure reported", err)
	}
	if !strings.Contains(err.Error(), m.marketplaceDir("beta")) || !strings.Contains(err.Error(), "rename 3 refused") {
		t.Fatalf("error = %v, want the clone the undo could not move back named", err)
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
	list, _ := m.ListMarketplaces()
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
	// Where an os.Rename LinkError said only "directory not empty".
	if want := fmt.Sprintf("plugin cache %s already exists", leftover); !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	if _, err := os.Stat(m.marketplaceDir(name)); err != nil {
		t.Fatalf("the clone was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir(), name)); err != nil {
		t.Fatalf("the plugin cache moved anyway: %v", err)
	}
	list, _ := m.ListMarketplaces()
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
	if want := fmt.Sprintf("plugin cache %s already exists", leftover); !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	list, _ := m.ListMarketplaces()
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
		if !strings.Contains(err.Error(), clone) {
			t.Fatalf("error = %v, want it to name %s", err, clone)
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
		if !strings.Contains(err.Error(), cache) {
			t.Fatalf("error = %v, want it to name %s", err, cache)
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
		if !strings.Contains(err.Error(), leftover) {
			t.Fatalf("error = %v, want it to name %s", err, leftover)
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
		// never have reached.
		if want := "checking " + dest; !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
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
		if _, err := m.EditMarketplace(ctx, name, "", &Source{Kind: SourceDirectory, Path: path}); !errors.Is(err, ErrMarketplaceSourceInStore) {
			t.Fatalf("source %s = %v, want ErrMarketplaceSourceInStore", path, err)
		}
		if _, err := os.Stat(filepath.Join(clone, ".claude-plugin", "marketplace.json")); err != nil {
			t.Fatalf("the clone did not survive the refusal: %v", err)
		}
		if after := readMarketplacesFile(); after != before {
			t.Fatalf("%s changed after a refused edit:\n%s", marketplacesFileName, after)
		}
		items, err := m.List()
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
			mk, err := m.ListMarketplaces()
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
	got := rekeyRegistry(reg, "acme", "beta", oldCache, newCache)
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
	unmoved := rekeyRegistry(reg, "acme", "beta", "", "")
	if p := unmoved.Plugins[registryKey("widget", "beta")][0].InstallPath; p != filepath.Join(oldCache, "widget", "abc") {
		t.Fatalf("InstallPath = %q, want it left under the cache directory that did not move", p)
	}
}
