package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// These snapshots are independent of registry rekeying: superseded revisions
// have no registry entry at all, as with a saved session's old selection.
type migrationTree struct {
	path     string
	identity os.FileInfo
	files    map[string][]byte
}

func snapshotMigrationTree(t *testing.T, path string) migrationTree {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tree := migrationTree{path: path, identity: info, files: map[string][]byte{}}
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		tree.files[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func assertMigrationTree(t *testing.T, tree migrationTree) {
	t.Helper()
	got := snapshotMigrationTree(t, tree.path)
	if !os.SameFile(tree.identity, got.identity) {
		t.Fatalf("published directory identity changed: %s", tree.path)
	}
	if !reflect.DeepEqual(tree.files, got.files) {
		t.Fatalf("published bytes changed: %s", tree.path)
	}
}

func plantMigrationRevisions(t *testing.T, m *Manager, name, plugin string) []migrationTree {
	t.Helper()
	current := plantLegacyMarketplace(t, m, name, plugin)
	var trees []migrationTree
	for _, revision := range []string{"superseded-a", "superseded-b", "sha1"} {
		dir := filepath.Join(filepath.Dir(current), revision)
		writePlugin(t, dir, plugin, nil)
		for file, data := range map[string]string{"skills/probe/SKILL.md": revision + "-skill", "scripts/hook.sh": revision + "-hook", "resources/companion.bin": revision + "-resource"} {
			path := filepath.Join(dir, file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		trees = append(trees, snapshotMigrationTree(t, dir))
	}
	return trees
}

func assertMigrationIdempotent(t *testing.T, m *Manager) {
	t.Helper()
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if refused := refusedMarketplaceNames(mk); len(refused) != 0 {
		t.Fatalf("migration left refused logical names: %v", refused)
	}
	beforeMK, beforeReg := readStoreFile(t, m.marketplacesFile()), readStoreFile(t, m.registryPath())
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readStoreFile(t, m.marketplacesFile()); after != beforeMK {
		t.Fatal("second acquisition changed marketplaces")
	}
	if after := readStoreFile(t, m.registryPath()); after != beforeReg {
		t.Fatal("second acquisition changed registry")
	}
}

func TestAutomaticMigrationPreservesPublishedPaths(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	trees := plantMigrationRevisions(t, m, "old@market", "widget")
	// Gc really acquires lockStore, which runs automatic migration before GC.
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 1 || installedAt(t, reg, "widget@old-market").InstallPath != trees[2].path {
		t.Fatalf("rekeyed registry = %+v", reg)
	}
	assertMigrationIdempotent(t, m)
}

func TestAutomaticMigrationAliasPreservesPublishedPaths(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	trees := plantMigrationRevisions(t, m, "a/./b", "widget")
	plantLegacyMarketplace(t, m, "a/b", "gadget")
	plantOneSource(t, m, "a/./b", "a/b")
	stranger := plantLegacyMarketplace(t, m, "a-b", "stranger")
	strangerTree := snapshotMigrationTree(t, stranger)
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
	assertMigrationTree(t, strangerTree)
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(mk) != 2 {
		t.Fatalf("same-source aliases not merged: %+v", mk)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 3 || installedAt(t, reg, "widget@a-b-2").InstallPath != trees[2].path {
		t.Fatalf("rekeyed registry = %+v", reg)
	}
	if installedAt(t, reg, "stranger@a-b").InstallPath != stranger {
		t.Fatal("different-source collision changed")
	}
	assertMigrationIdempotent(t, m)
	// A later alias must use the durable source/family record, even though
	// superseded revisions still occupy the original cache.
	mk["a//b"] = MarketplaceRef{Source: mk["a-b-2"].Source, InstallLocation: m.marketplaceDir("a//b")}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	mk, err = m.loadMarketplaces()
	if err != nil || len(mk) != 2 {
		t.Fatalf("later source-family alias = %+v, %v", mk, err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
}

func TestAutomaticMigrationRecoveryPreservesPublishedPaths(t *testing.T) {
	for _, stage := range []string{"marker", "clone-moved", "cache-moved", "registry-written", "surviving-old", "both-paths", "merge", "merge-moved", "merge-registry-written"} {
		t.Run(stage, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			trees := plantMigrationRevisions(t, m, "a/b", "widget")
			oldPath := trees[2].path
			plantRenameMarker(t, m, "a/b", "a-b")
			moved := stage == "cache-moved" || stage == "surviving-old" || stage == "merge-moved" || stage == "merge-registry-written"
			if stage != "marker" && stage != "registry-written" {
				if err := os.Rename(m.marketplaceDir("a/b"), m.marketplaceDir("a-b")); err != nil {
					t.Fatal(err)
				}
			}
			if moved {
				if err := os.Rename(filepath.Join(m.cacheDir(), "a/b"), filepath.Join(m.cacheDir(), "a-b")); err != nil {
					t.Fatal(err)
				}
				for i := range trees {
					trees[i].path = m.pluginCacheDir("a-b", "widget", filepath.Base(trees[i].path))
				}
			}
			if stage == "surviving-old" {
				// An original superseded tree survives at the old path, while
				// the current revision already moved. Keep both identities.
				dir := filepath.Join(filepath.Dir(oldPath), "superseded-a")
				if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(trees[0].path, dir); err != nil {
					t.Fatal(err)
				}
				trees[0].path = dir
			}
			if stage == "both-paths" {
				// A valid old InstallPath takes precedence over an occupied
				// destination. Migration must neither overwrite nor adopt it.
				dir := m.pluginCacheDir("a-b", "widget", "sha1")
				writePlugin(t, dir, "destination", nil)
				trees = append(trees, snapshotMigrationTree(t, dir))
			}
			if stage == "registry-written" || stage == "merge-registry-written" {
				reg, err := m.loadRegistry()
				if err != nil {
					t.Fatal(err)
				}
				reg.Plugins["widget@a-b"] = reg.Plugins["widget@a/b"]
				delete(reg.Plugins, "widget@a/b")
				if err := m.saveRegistry(reg); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "merge" || stage == "merge-moved" || stage == "merge-registry-written" {
				mk, err := m.loadMarketplaces()
				if err != nil {
					t.Fatal(err)
				}
				mk["a-b"] = MarketplaceRef{Source: mk["a/b"].Source, InstallLocation: m.marketplaceDir("a-b")}
				if err := m.saveMarketplaces(mk); err != nil {
					t.Fatal(err)
				}
				plantMergeMarker(t, m, "a/b", "a-b")
			}
			if _, err := m.Gc(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				assertMigrationTree(t, tree)
			}
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			want := oldPath
			if moved {
				want = m.pluginCacheDir("a-b", "widget", "sha1")
			}
			if got := installedAt(t, reg, "widget@a-b").InstallPath; got != want {
				t.Fatalf("recovered path = %s, want %s", got, want)
			}
			mustNotExist(t, renameMarkerFile(m))
			assertMigrationIdempotent(t, m)
		})
	}
}

func TestAutomaticMigrationWriteInterruptionPreservesPublishedPaths(t *testing.T) {
	for _, merge := range []bool{false, true} {
		t.Run(map[bool]string{false: "rename", true: "merge"}[merge], func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			trees := plantMigrationRevisions(t, m, "a/./b", "widget")
			if merge {
				plantLegacyMarketplace(t, m, "a/b", "gadget")
				plantOneSource(t, m, "a/./b", "a/b")
			}
			write, save := marketplaceAtomicWriteFile, installSaveRegistry
			t.Cleanup(func() { marketplaceAtomicWriteFile, installSaveRegistry = write, save })
			failed := false
			marketplaceAtomicWriteFile = func(path string, data []byte, mode os.FileMode) error {
				if filepath.Base(path) == marketplacesFileName {
					marker, err := m.loadRenameMarker()
					if err != nil {
						t.Fatal(err)
					}
					if marker != nil && marker.Merge == merge {
						failed = true
						return os.ErrPermission
					}
				}
				return write(path, data, mode)
			}
			installSaveRegistry = func(path string, reg Registry) error {
				if failed {
					return os.ErrPermission // fail rollback too, like a stopped writer
				}
				return save(path, reg)
			}
			if _, err := m.Gc(context.Background()); !errors.Is(err, errStoreBetweenNames) {
				t.Fatalf("interrupted save = %v, want between-names state", err)
			}
			marketplaceAtomicWriteFile, installSaveRegistry = write, save
			mustExist(t, renameMarkerFile(m))
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if installedAt(t, reg, "widget@a-b").InstallPath != trees[2].path {
				t.Fatal("first registry write changed the physical install path")
			}
			for _, tree := range trees {
				assertMigrationTree(t, tree)
			}
			// Restart the manager, and recover under the real next store lock.
			m = NewManager(m.Root)
			m.Stderr = io.Discard
			if _, err := m.Gc(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				assertMigrationTree(t, tree)
			}
			mustNotExist(t, renameMarkerFile(m))
			assertMigrationIdempotent(t, m)
		})
	}
}

func TestAutomaticMigrationMergeRefusesDifferentSource(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	trees := plantMigrationRevisions(t, m, "a/b", "widget")
	stranger := plantLegacyMarketplace(t, m, "a-b", "stranger")
	strangerTree := snapshotMigrationTree(t, stranger)
	plantMergeMarker(t, m, "a/b", "a-b")
	before := readStoreFile(t, m.registryPath())
	if _, err := m.Gc(context.Background()); !errors.Is(err, ErrMarketplaceExists) {
		t.Fatalf("different-source merge = %v", err)
	}
	if readStoreFile(t, m.registryPath()) != before {
		t.Fatal("different-source merge changed registry")
	}
	for _, tree := range append(trees, strangerTree) {
		assertMigrationTree(t, tree)
	}
}

func TestAutomaticMigrationRecoveryRejectsUnownedDestination(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	old := plantLegacyMarketplace(t, m, "a/b", "widget")
	// The old writer moved the real tree somewhere unrelated. A matching
	// destination filename that links outside the cache proves no exact move.
	outside := filepath.Join(t.TempDir(), "published")
	if err := os.Rename(old, outside); err != nil {
		t.Fatal(err)
	}
	before := snapshotMigrationTree(t, outside)
	dest := m.pluginCacheDir("a-b", "widget", "sha1")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dest); err != nil {
		t.Fatal(err)
	}
	plantRenameMarker(t, m, "a/b", "a-b")
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if installedAt(t, reg, "widget@a-b").InstallPath != old {
		t.Fatal("marker adopted a destination outside its physical ownership")
	}
	assertMigrationTree(t, before)
}

// An unreadable old path is not an absent path that can be reconciled to a
// destination. The filesystem seam is the same one used by pathPresent.
func TestAutomaticMigrationUnreadablePath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	trees := plantMigrationRevisions(t, m, "a/b", "widget")
	plantRenameMarker(t, m, "a/b", "a-b")
	original := marketplaceStat
	t.Cleanup(func() { marketplaceStat = original })
	marketplaceStat = func(path string) (os.FileInfo, error) {
		if path == trees[2].path {
			return nil, os.ErrPermission
		}
		return original(path)
	}
	before := []byte(readStoreFile(t, m.registryPath()))
	if _, err := m.Gc(context.Background()); err == nil {
		t.Fatal("unreadable published path authorized migration")
	}
	if !bytes.Equal(before, []byte(readStoreFile(t, m.registryPath()))) {
		t.Fatal("registry changed after unreadable path")
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
}
