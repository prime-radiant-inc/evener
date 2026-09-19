package plugins

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// ghostDirEntry is a ReadDir entry whose path does not exist. It stands in for
// an entry removed between the ReadDir and the sweep's check, where only the
// readdir result remains.
type ghostDirEntry struct{ name string }

func (e ghostDirEntry) Name() string               { return e.name }
func (e ghostDirEntry) IsDir() bool                { return true }
func (e ghostDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (e ghostDirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

func TestGc_NoCacheDirYet(t *testing.T) {
	m := NewManager(t.TempDir())
	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc on empty store: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none", removed)
	}
}

func TestGc_RemovesOrphanedKeepsReferenced(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin's upstream HEAD and upgrade — Upgrade never deletes,
	// so the first sha-dir is left orphaned once the registry repoints.
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")
	second, err := m.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if first.InstallPath == second.InstallPath {
		t.Fatal("test setup: upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(first.InstallPath); err != nil {
		t.Fatalf("test setup: old sha-dir missing before gc: %v", err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 1 || removed[0] != first.InstallPath {
		t.Fatalf("Gc removed = %v, want [%s]", removed, first.InstallPath)
	}
	if _, err := os.Stat(first.InstallPath); !os.IsNotExist(err) {
		t.Fatal("orphaned sha-dir not removed by Gc")
	}
	if _, err := os.Stat(second.InstallPath); err != nil {
		t.Fatalf("referenced sha-dir removed by Gc: %v", err)
	}

	// A second sweep with nothing new to reclaim is a no-op.
	removed, err = m.Gc(context.Background())
	if err != nil {
		t.Fatalf("second Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("second Gc removed = %v, want none", removed)
	}
}

func TestGc_DirectorySourceInstallPathNeverConsidered(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t) // "widget" is a "./plugins/widget" relative source
	pluginDir := filepath.Join(mktRepo, "plugins", "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if entry.InstallPath != pluginDir {
		t.Fatalf("InstallPath = %q, want %q (referenced in place)", entry.InstallPath, pluginDir)
	}

	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("Gc touched a directory-source install path: %v", err)
	}
}

// A marketplace removal that lands the marketplaces-file save but fails to
// remove the clone leaves the clone under the marketplaces directory while
// nothing records it. Before this, nothing swept that directory — Gc walked
// only the plugin cache, and Doctor only iterated registered marketplaces — so
// the residue outlived every command and blocked a later rename onto the name
// with "it must be deleted", naming no way to delete it.
func TestGc_RemovesALeftoverMarketplaceClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	// acme is not recorded: its removal landed and its clone cleanup did not.
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 1 || removed[0] != clone {
		t.Fatalf("Gc removed = %v, want [%s]", removed, clone)
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatal("leftover marketplace clone outlived Gc")
	}
}

// The sweep must keep the clone of a marketplace that is still registered —
// its install location is exactly the directory the walk finds.
func TestGc_KeepsARegisteredMarketplaceClone(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceGitHub, Repo: "acme/widgets"},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: the clone is recorded", removed)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("Gc removed a registered marketplace's clone: %v", err)
	}
}

// The store's two scratch names are not leftovers. .old can be the only local
// copy of a clone a failed swap could not put back, so the sweep must leave it
// and .staging alone.
func TestGc_KeepsTheStoresScratchCloneDirs(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := os.MkdirAll(m.marketplaceDir(asideCloneName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.marketplaceDir(stagingCloneName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: the scratch names are the store's own", removed)
	}
	for _, name := range []string{asideCloneName, stagingCloneName} {
		if _, err := os.Stat(m.marketplaceDir(name)); err != nil {
			t.Fatalf("Gc removed the store's %s scratch directory: %v", name, err)
		}
	}
}

// A recorded directory source that sits inside a leftover clone is live data:
// removing the clone would delete it, exactly as RemoveMarketplace's own sweep
// refuses to. The leftover-clone sweep has to refuse too.
func TestGc_KeepsALeftoverCloneADirectorySourceSitsIn(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	source := filepath.Join(clone, "sub")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	// A hand-seeded store can hold this: refuseSourceInStore guards new
	// operations, not a record written directly.
	if err := m.saveMarketplaces(Marketplaces{"other": {
		Source:          Source{Kind: SourceDirectory, Path: source},
		InstallLocation: source,
	}}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: a directory source sits inside the clone", removed)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("Gc removed a live directory source: %v", err)
	}
}

// A failed cleanup can leave a symlink where the clone was, and the link
// occupies the marketplace name just as a directory does. Gc must clear it
// without following it: RemoveAll removes the link, never its target.
func TestGc_RemovesALeftoverMarketplaceCloneSymlink(t *testing.T) {
	m := NewManager(t.TempDir())
	target := filepath.Join(t.TempDir(), "live-copy")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := m.marketplaceDir("acme")
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 1 || removed[0] != link {
		t.Fatalf("Gc removed = %v, want [%s]", removed, link)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("leftover marketplace clone symlink outlived Gc")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("Gc followed the symlink and removed its target: %v", err)
	}
}

// A leftover link is compared as the name it occupies, not as its target: a
// link at an unregistered name must be swept even when it points at a live
// marketplace's clone, which is what following the final symlink would call it.
func TestGc_RemovesALeftoverSymlinkThatPointsAtALiveClone(t *testing.T) {
	m := NewManager(t.TempDir())
	beta := m.marketplaceDir("beta")
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	link := m.marketplaceDir("acme")
	if err := os.Symlink(beta, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := m.saveMarketplaces(Marketplaces{"beta": {
		Source:          Source{Kind: SourceGitHub, Repo: "acme/widgets"},
		InstallLocation: beta,
	}}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 1 || removed[0] != link {
		t.Fatalf("Gc removed = %v, want [%s]", removed, link)
	}
	if _, err := os.Stat(beta); err != nil {
		t.Fatalf("Gc removed the live clone the link pointed at: %v", err)
	}
}

// Gc reads both store files before either sweep mutates disk, so a corrupt
// marketplaces file aborts with nothing removed rather than after the cache
// sweep has already deleted directories.
func TestGc_DoesNotSweepCacheWhenTheMarketplacesFileIsCorrupt(t *testing.T) {
	m := NewManager(t.TempDir())
	sha := m.pluginCacheDir("acme", "widget", "deadbeef")
	if err := os.MkdirAll(sha, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.marketplacesFile(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Gc(context.Background()); err == nil {
		t.Fatal("Gc with a corrupt marketplaces file succeeded")
	}
	if _, err := os.Stat(sha); err != nil {
		t.Fatalf("Gc deleted a cache dir before it read the marketplaces file: %v", err)
	}
}

// A clone removal that fails partway still reports the clones already removed,
// as the cache sweep reports the dirs already removed when one removal fails.
func TestGc_ReportsClonesRemovedBeforeACloneRemovalFails(t *testing.T) {
	m := NewManager(t.TempDir())
	first, second := m.marketplaceDir("aaa"), m.marketplaceDir("bbb")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}
	origRemoveAll := gcRemoveAll
	t.Cleanup(func() { gcRemoveAll = origRemoveAll })
	gcRemoveAll = func(path string) error {
		if path == second {
			return errors.New("boom")
		}
		return origRemoveAll(path)
	}

	removed, err := m.Gc(context.Background())
	if err == nil {
		t.Fatal("Gc succeeded despite a clone removal failure")
	}
	if len(removed) != 1 || removed[0] != first {
		t.Fatalf("Gc removed = %v, want [%s]", removed, first)
	}
}

// RemoveMarketplace drops a marketplace's registration but deliberately leaves
// its plugins' registry entries behind. A legacy record can source from its own
// clone, so those install paths lie inside the clone; reclaiming it would break
// a still-recorded install, so the sweep has to keep it.
func TestGc_KeepsAStrandedCloneARegistryInstallLivesIn(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	pluginDir := filepath.Join(clone, "plugins", "widget")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A legacy record sources from its own clone; its plugin installs inside it.
	if err := m.saveMarketplaces(Marketplaces{"acme": {
		Source:          Source{Kind: SourceDirectory, Path: clone},
		InstallLocation: clone,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: pluginDir, Source: Source{Kind: SourceDirectory, Path: pluginDir}}},
	}}); err != nil {
		t.Fatal(err)
	}

	// RemoveMarketplace protects the clone — the removed record's own source —
	// and leaves the registry entry naming an install inside it.
	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("test setup: RemoveMarketplace removed the clone: %v", err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: a registry install lives inside the clone", removed)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("Gc removed a registered plugin's install: %v", err)
	}
}

// Remove, then Gc: the two verbs agree on a legacy in-store directory source.
// Removal spares the directory because a recorded source sits inside it, and
// carries that spare into the store's exception record, so the next Gc — which
// the hub runs on every start — does not reclaim it as an unreferenced clone.
// Before, removal spared it and Gc deleted it silently.
func TestGc_AfterRemovingALegacyInStoreDirectorySource(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := seedLegacyInStoreDirectorySource(t, m, "acme")

	if err := m.RemoveMarketplace(context.Background(), "acme"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("RemoveMarketplace deleted the live in-store directory source: %v", err)
	}
	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: the removal spared and retained the directory", removed)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("Gc reclaimed a directory a removal deliberately spared: %v", err)
	}
}

// A clone removed between the directory read and the sweep's check is nothing
// to sweep: RemoveAll on a missing path would report a directory as removed
// that never went away.
func TestGc_SkipsACloneGoneBeforeTheCheck(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := os.MkdirAll(m.marketplacesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}
	origReadDir := gcReadDir
	t.Cleanup(func() { gcReadDir = origReadDir })
	gcReadDir = func(path string) ([]os.DirEntry, error) {
		if path == m.marketplacesDir() {
			return []os.DirEntry{ghostDirEntry{name: "ghost"}}, nil
		}
		return origReadDir(path)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: the entry does not exist", removed)
	}
}

// A rename of a legacy in-store directory source leaves the record sourcing the
// path it had before, not the clone the new name derives. Removing the renamed
// record must carry that path into the exception record, or the next Gc
// reclaims it as an orphan and deletes the live source with no CLI path back
// (refuseSourceInStore rejects re-adding it).
func TestGc_AfterRenamingAndRemovingALegacyInStoreDirectorySource(t *testing.T) {
	m := NewManager(t.TempDir())
	sentinel := seedLegacyInStoreDirectorySource(t, m, "acme")

	if _, err := m.EditMarketplace(context.Background(), "acme", "beta", nil); err != nil {
		t.Fatalf("EditMarketplace rename: %v", err)
	}
	if err := m.RemoveMarketplace(context.Background(), "beta"); err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: a removed record sourced this path", removed)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("Gc reclaimed a path a renamed-then-removed record sourced: %v", err)
	}
}

// The registry InstallPath loop is the only safeguard when there is neither a
// marketplace record nor a retained entry: a leftover clone a still-recorded
// install lives in must survive Gc.
func TestGc_KeepsALeftoverCloneARegistryInstallLivesInWithoutARecord(t *testing.T) {
	m := NewManager(t.TempDir())
	clone := m.marketplaceDir("acme")
	pluginDir := filepath.Join(clone, "plugins", "widget")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
		"widget@acme": {{InstallPath: pluginDir, Source: Source{Kind: SourceDirectory, Path: pluginDir}}},
	}}); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: a registry install lives inside the clone", removed)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("Gc removed a registered plugin's install: %v", err)
	}
}

// A directory source can be a symlink under the marketplaces directory that
// points outside the store. The clone sweep removes the link, not its target,
// so retaining the source has to see the lexical path, not only the resolved
// one — otherwise Gc deletes the link and the source path with it.
func TestGc_AfterRemovingARecordSourcingASymlinkElsewhereInTheStore(t *testing.T) {
	m := NewManager(t.TempDir())
	source := m.marketplaceDir("other")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
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
	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none: a removed record sourced this path", removed)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("Gc removed a symlinked source path: %v", err)
	}
}
