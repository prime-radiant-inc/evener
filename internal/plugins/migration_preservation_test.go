package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// These snapshots are independent of registry rekeying: superseded revisions
// have no registry entry at all, as with a saved session's old selection.
type migrationTree struct {
	path     string
	identity os.FileInfo
	files    map[string][]byte
	nodes    map[string]os.FileInfo
}

func snapshotMigrationTree(t *testing.T, path string) migrationTree {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tree := migrationTree{path: path, identity: info, files: map[string][]byte{}, nodes: map[string]os.FileInfo{}}
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		tree.nodes[rel] = info
		if d.IsDir() {
			return nil
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
	if len(tree.nodes) != len(got.nodes) {
		t.Fatalf("published tree size changed: %s", tree.path)
	}
	for rel, before := range tree.nodes {
		after, present := got.nodes[rel]
		if !present || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			t.Fatalf("published node identity or mode changed: %s", filepath.Join(tree.path, rel))
		}
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

func TestEditMarketplaceAfterMigrationPreservesSharedCache(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	foreign := plantMigrationRevisions(t, m, "a/b", "widget")
	own := plantMigrationRevisions(t, m, "a", "gadget")
	ctx := context.Background()

	// Run the actual logical migration, not just its final registry shape.
	mk, err := m.ListMarketplaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, old := mk["a/b"]; old || len(mk) != 2 {
		t.Fatalf("marketplaces after migration = %+v", mk)
	}
	if _, migrated := mk["a-b"]; !migrated {
		t.Fatal("nested marketplace was not migrated")
	}
	before, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	foreignEntry := installedAt(t, before, "widget@a-b")
	ownEntry := installedAt(t, before, "gadget@a")
	if foreignEntry.InstallPath != foreign[2].path || ownEntry.InstallPath != own[2].path {
		t.Fatalf("migration changed original selections: %+v", before)
	}
	// The whole shared namespace must stay, including unregistered superseded
	// packages, hook scripts, companion resources and their file identities.
	shared := snapshotMigrationTree(t, filepath.Join(m.cacheDir(), "a"))
	if _, err := m.EditMarketplace(ctx, "a", "renamed", nil); err != nil {
		t.Fatal(err)
	}
	assertMigrationTree(t, shared)
	for _, tree := range append(foreign, own...) {
		assertMigrationTree(t, tree)
	}
	want := Registry{Version: before.Version, Plugins: map[string][]InstallEntry{
		"widget@a-b":     {foreignEntry},
		"gadget@renamed": {ownEntry},
	}}
	after, err := m.loadRegistry()
	if err != nil || !reflect.DeepEqual(after, want) {
		t.Fatalf("registry after explicit rename = %+v, %v, want %+v", after, err, want)
	}
	items, err := m.List(ctx)
	if err != nil || len(items) != 2 {
		t.Fatalf("fresh selections = %+v, %v", items, err)
	}
	for _, item := range items {
		entry := installedAt(t, want, registryKey(item.Plugin, item.Marketplace))
		if item.Broken || !item.Enabled || item.InstallPath != entry.InstallPath {
			t.Fatalf("fresh selection no longer names original package: %+v", item)
		}
		assertMigrationTree(t, snapshotForPath(t, append(foreign, own...), item.InstallPath))
	}
	mustNotExist(t, filepath.Join(m.cacheDir(), "renamed"))
}

func snapshotForPath(t *testing.T, trees []migrationTree, path string) migrationTree {
	t.Helper()
	for _, tree := range trees {
		if tree.path == path {
			return tree
		}
	}
	t.Fatalf("no original package snapshot for %s", path)
	return migrationTree{}
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
	scenarios := []struct {
		name                                                                string
		moveClone, moveCache, writeRegistry, merge, survivingOld, bothPaths bool
	}{
		{name: "marker"},
		{name: "clone-moved", moveClone: true},
		{name: "cache-moved", moveClone: true, moveCache: true},
		{name: "registry-written", writeRegistry: true},
		{name: "surviving-old", moveClone: true, moveCache: true, survivingOld: true},
		{name: "both-paths", moveClone: true, bothPaths: true},
		{name: "merge", moveClone: true, merge: true},
		{name: "merge-moved", moveClone: true, moveCache: true, merge: true},
		{name: "merge-registry-written", moveClone: true, moveCache: true, writeRegistry: true, merge: true},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			trees := plantMigrationRevisions(t, m, "a/b", "widget")
			oldPath := trees[2].path
			plantRenameMarker(t, m, "a/b", "a-b")
			if scenario.moveClone {
				if err := os.Rename(m.marketplaceDir("a/b"), m.marketplaceDir("a-b")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.moveCache {
				if err := os.Rename(filepath.Join(m.cacheDir(), "a", "b"), filepath.Join(m.cacheDir(), "a-b")); err != nil {
					t.Fatal(err)
				}
				for i := range trees {
					trees[i].path = m.pluginCacheDir("a-b", "widget", filepath.Base(trees[i].path))
				}
			}
			if scenario.survivingOld {
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
			if scenario.bothPaths {
				// A valid old InstallPath takes precedence over an occupied
				// destination. Migration must neither overwrite nor adopt it.
				dir := m.pluginCacheDir("a-b", "widget", "sha1")
				writePlugin(t, dir, "destination", nil)
				trees = append(trees, snapshotMigrationTree(t, dir))
			}
			if scenario.writeRegistry {
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
			if scenario.merge {
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
			if scenario.moveCache {
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
					var next Marketplaces
					if err := json.Unmarshal(data, &next); err != nil {
						t.Fatal(err)
					}
					from := "a/./b"
					if merge {
						from = "a/b"
					}
					if _, remains := next[from]; !remains {
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
			if merge {
				mustNotExist(t, renameMarkerFile(m))
				mk, err := m.loadMarketplaces()
				if err != nil {
					t.Fatal(err)
				}
				if _, remains := mk["a/b"]; !remains {
					t.Fatal("interrupted logical merge lost its retry source")
				}
			} else {
				mustExist(t, renameMarkerFile(m))
			}
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

// A fresh logical alias merge must never leave a physical-move marker, even
// after a failed first write or an interruption between its two store writes.
func TestAutomaticMigrationFreshAliasKeepsMissingPath(t *testing.T) {
	for _, boundary := range []string{"success", "first-registry-failure", "split-write"} {
		t.Run(boundary, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyMarketplace(t, m, "a/./b", "widget")
			if _, err := m.Gc(context.Background()); err != nil {
				t.Fatal(err)
			}
			mk, err := m.loadMarketplaces()
			if err != nil {
				t.Fatal(err)
			}
			alias := mk["a-b"]
			alias.InstallLocation = m.marketplaceDir("a/b")
			mk["a/b"] = alias
			if err := m.saveMarketplaces(mk); err != nil {
				t.Fatal(err)
			}
			missing := m.pluginCacheDir("a/b", "gadget", "sha1")
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			reg.Plugins["gadget@a/b"] = []InstallEntry{{InstallPath: missing, Version: "1.0.0", Enabled: true, Source: Source{Kind: SourceGitHub, Repo: "o/gadget"}}}
			if err := m.saveRegistry(reg); err != nil {
				t.Fatal(err)
			}
			// This directory was independently published, never moved from the old
			// namespace. Its matching suffix cannot authorize recovery of missing.
			destination := m.pluginCacheDir("a-b", "gadget", "sha1")
			writePlugin(t, destination, "gadget", nil)
			tree := snapshotMigrationTree(t, destination)
			mustNotExist(t, missing)
			mustNotExist(t, renameMarkerFile(m))
			write, save := marketplaceAtomicWriteFile, installSaveRegistry
			t.Cleanup(func() { marketplaceAtomicWriteFile, installSaveRegistry = write, save })
			interrupted := false
			crash := new(int)
			installSaveRegistry = func(path string, reg Registry) error {
				if boundary == "first-registry-failure" && !interrupted {
					interrupted = true
					return os.ErrPermission
				}
				return save(path, reg)
			}
			marketplaceAtomicWriteFile = func(path string, data []byte, mode os.FileMode) error {
				if filepath.Base(path) == marketplacesFileName && boundary == "split-write" {
					interrupted = true
					panic(crash) // registry persisted, marketplaces not written, no rollback
				}
				return write(path, data, mode)
			}
			func() {
				defer func() {
					if got := recover(); got != nil && got != crash {
						panic(got)
					}
				}()
				release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer release() // model process exit releasing its lock after panic
				err = m.migrateMarketplaceNames()
				if boundary == "first-registry-failure" {
					if err == nil {
						t.Fatalf("first write = %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}()
			marketplaceAtomicWriteFile, installSaveRegistry = write, save
			if boundary != "success" && !interrupted {
				t.Fatal("fault boundary was not reached")
			}
			beforeRetry, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			key := "gadget@a-b"
			if boundary == "first-registry-failure" {
				key = "gadget@a/b"
			}
			if got := installedAt(t, beforeRetry, key).InstallPath; got != missing {
				t.Fatalf("first attempt changed recorded path: %s", got)
			}
			if boundary != "success" {
				mk, err := m.loadMarketplaces()
				if err != nil {
					t.Fatal(err)
				}
				if _, remains := mk["a/b"]; !remains {
					t.Fatal("retry alias disappeared before marketplaces commit")
				}
			}
			mustNotExist(t, renameMarkerFile(m))
			assertMigrationTree(t, tree)
			m = NewManager(m.Root)
			m.Stderr = io.Discard
			if _, err := m.Gc(context.Background()); err != nil {
				t.Fatal(err)
			}
			reg, err = m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if got := installedAt(t, reg, "gadget@a-b").InstallPath; got != missing {
				t.Fatalf("fresh alias adopted independently published destination: got %s, want missing recorded path %s", got, missing)
			}
			assertMigrationTree(t, tree)
			assertMigrationIdempotent(t, m)
		})
	}
}

func TestAutomaticMigrationRecoveryRetainsUnreadableOwnership(t *testing.T) {
	for _, failure := range []string{"destination", "directory-source"} {
		t.Run(failure, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			old := plantLegacyMarketplace(t, m, "a/b", "widget")
			destination := m.pluginCacheDir("a-b", "widget", "sha1")
			if err := os.Rename(m.marketplaceDir("a/b"), m.marketplaceDir("a-b")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(m.cacheDir(), "a", "b"), filepath.Join(m.cacheDir(), "a-b")); err != nil {
				t.Fatal(err)
			}
			mustNotExist(t, old)
			tree := snapshotMigrationTree(t, destination)
			plantRenameMarker(t, m, "a/b", "a-b")
			unreadable := filepath.Join(m.cacheDir(), "a-b")
			if failure == "directory-source" {
				unreadable = t.TempDir()
				mk, err := m.loadMarketplaces()
				if err != nil {
					t.Fatal(err)
				}
				mk["external"] = MarketplaceRef{Source: Source{Kind: SourceDirectory, Path: unreadable}, InstallLocation: unreadable}
				if err := m.saveMarketplaces(mk); err != nil {
					t.Fatal(err)
				}
			}
			beforeReg := readStoreFile(t, m.registryPath())
			beforeMarker := readStoreFile(t, renameMarkerFile(m))
			original := marketplaceLstat
			t.Cleanup(func() { marketplaceLstat = original })
			failures := 0
			marketplaceLstat = func(path string) (os.FileInfo, error) {
				if path == unreadable {
					failures++
					return nil, os.ErrPermission
				}
				return original(path)
			}
			_, err := m.Gc(context.Background())
			marketplaceLstat = original
			if failures == 0 {
				t.Fatal("ownership inspection did not reach injected failure")
			}
			if err == nil {
				t.Error("unreadable ownership authorized successful recovery")
			}
			if got := readStoreFile(t, m.registryPath()); got != beforeReg {
				t.Error("unreadable ownership changed registry bytes")
			}
			if got, err := os.ReadFile(renameMarkerFile(m)); err != nil || string(got) != beforeMarker {
				t.Errorf("unreadable ownership lost or changed recovery marker: %v", err)
			}
			assertMigrationTree(t, tree)
			// Once inspection works again, the retained marker completes the
			// exact old move rather than leaving the store permanently blocked.
			if _, err := m.Gc(context.Background()); err != nil {
				t.Fatal(err)
			}
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if installedAt(t, reg, "widget@a-b").InstallPath != destination {
				t.Fatal("recovered ownership did not reconcile the proven move")
			}
			mustNotExist(t, renameMarkerFile(m))
			assertMigrationTree(t, tree)
			assertMigrationIdempotent(t, m)
		})
	}
}

func TestAutomaticMigrationFreshRenameSplitWriteKeepsMissingPath(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	missing := plantLegacyMarketplace(t, m, "a/b", "widget")
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	write := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = write })
	crash := new(int)
	destination := m.pluginCacheDir("a-b", "widget", "sha1")
	var tree migrationTree
	interrupted := false
	marketplaceAtomicWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			writePlugin(t, destination, "widget", map[string]string{"companion.bin": "independent bytes"})
			tree = snapshotMigrationTree(t, destination)
			interrupted = true
			panic(crash)
		}
		return write(path, data, mode)
	}
	func() {
		defer func() {
			if got := recover(); got != nil && got != crash {
				panic(got)
			}
		}()
		release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer release() // model process exit releasing its lock after panic
		if err := m.migrateMarketplaceNames(); err != nil {
			t.Fatal(err)
		}
	}()
	marketplaceAtomicWriteFile = write
	if !interrupted {
		t.Fatal("split-write boundary was not reached")
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := installedAt(t, reg, "widget@a-b").InstallPath; got != missing {
		t.Fatalf("first write retargeted path: %s", got)
	}
	m = NewManager(m.Root)
	m.Stderr = io.Discard
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := installedAt(t, reg, "widget@a-b").InstallPath; got != missing {
		t.Fatalf("retry adopted independently published bytes: %s", got)
	}
	assertMigrationTree(t, tree)
	assertMigrationIdempotent(t, m)
}
