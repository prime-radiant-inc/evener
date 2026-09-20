package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
// nothing records it. Only Gc's cache sweep existed, so nothing reclaimed it:
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

// The store's two scratch names are not leftovers: a fetch stages into
// .staging, and .old holds the clone a failed swap could not put back, which
// can be the only local copy.
func TestGc_KeepsTheStoresScratchCloneDirs(t *testing.T) {
	m := NewManager(t.TempDir())
	for _, name := range []string{asideCloneName, stagingCloneName} {
		if err := os.MkdirAll(m.marketplaceDir(name), 0o755); err != nil {
			t.Fatal(err)
		}
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

// A failed cleanup can leave a symlink where the clone was, and the link
// occupies the marketplace name just as a directory does — it blocks the rename
// the remove path's error tells the user to clear with gc. Gc must clear it
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

// A failure to read the marketplaces directory is returned, after the cache
// removals already made, rather than reported as a success that skipped the
// clone sweep entirely.
func TestGc_ReportsAMarketplacesReadFailureAfterReclaimingTheCache(t *testing.T) {
	m := NewManager(t.TempDir())
	sha := m.pluginCacheDir("acme", "widget", "deadbeef")
	if err := os.MkdirAll(sha, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file where the marketplaces directory belongs: ReadDir fails with
	// ENOTDIR, not ErrNotExist.
	if err := os.WriteFile(m.marketplacesDir(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := m.Gc(context.Background())
	if err == nil {
		t.Fatal("Gc reported success while skipping the clone sweep")
	}
	found := false
	for _, p := range removed {
		if p == sha {
			found = true
		}
	}
	if !found {
		t.Fatalf("Gc removed = %v, want the already-reclaimed cache dir %s reported", removed, sha)
	}
	if _, err := os.Stat(sha); !os.IsNotExist(err) {
		t.Fatalf("cache dir was not reclaimed: %v", err)
	}
}

// The store's marketplaces directory can be a symlink, and a record can name
// the physical path. Ownership has to compare in the same resolved form, or a
// registered marketplace's live clone is swept as residue and the next refresh
// has to reclone it.
func TestGc_KeepsARegisteredCloneUnderASymlinkedMarketplacesDir(t *testing.T) {
	root, physical := t.TempDir(), t.TempDir()
	if err := os.Symlink(physical, filepath.Join(root, marketplacesDirName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	m := NewManager(root)
	clone := filepath.Join(physical, "acme")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	// The record names the physical path; the clone reached through the
	// symlinked marketplaces directory is that same directory.
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
		t.Fatalf("Gc removed = %v, want none: the clone is recorded under its physical path", removed)
	}
	if _, err := os.Stat(clone); err != nil {
		t.Fatalf("Gc removed a registered marketplace's clone: %v", err)
	}
}
