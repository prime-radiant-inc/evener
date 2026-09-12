package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// plantLegacyMarketplace records a git-backed marketplace under name as an
// older evener would have written it, whatever the store refuses about the
// name today: a clone at <marketplaces>/<name> holding a catalog, plugin
// materialized under cache/<name>/<plugin>/<sha>, and the registry entry
// <plugin>@<name> pointing at it. It returns that install path.
func plantLegacyMarketplace(t *testing.T, m *Manager, name, plugin string) string {
	t.Helper()
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	clone := plantCatalog(t, m.marketplaceDir(name))
	mk[name] = MarketplaceRef{
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/" + plugin + ".git"},
		InstallLocation: clone,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	installPath := m.pluginCacheDir(name, plugin, "sha1")
	writePlugin(t, installPath, plugin, nil)
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg.Plugins[registryKey(plugin, name)] = []InstallEntry{{
		InstallPath: installPath,
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/" + plugin},
	}}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}
	return installPath
}

// plantLegacyDirectoryMarketplace records a directory-source marketplace under
// name as an older evener would have, with plugin referenced in place inside
// it. The install entry's own source is what decides whether a sweep upgrades
// it at all, so the caller gives that.
func plantLegacyDirectoryMarketplace(t *testing.T, m *Manager, name, plugin string, src Source) {
	t.Helper()
	dir := makeDirectoryMarketplace(t, "acme", plugin)
	if err := m.saveMarketplaces(Marketplaces{name: {
		Source:          Source{Kind: SourceDirectory, Path: dir},
		InstallLocation: dir,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.saveRegistry(Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey(plugin, name): {{
			InstallPath: filepath.Join(dir, "plugins", plugin),
			Version:     "1.0.0",
			Enabled:     true,
			AutoUpgrade: true,
			Source:      src,
		}},
	}}); err != nil {
		t.Fatal(err)
	}
}

// plantOneSource records every one of names as naming the source the first of
// them names, which is what makes them aliases of the one marketplace rather
// than separate marketplaces deriving the one pair of directories.
// plantLegacyMarketplace names each source after the plugin it plants, so
// aliases planted with a plugin apiece arrive naming a marketplace apiece.
func plantOneSource(t *testing.T, m *Manager, names ...string) {
	t.Helper()
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names[1:] {
		ref, ok := mk[name]
		if !ok {
			t.Fatalf("%s is not recorded: %v", name, mk)
		}
		ref.Source = mk[names[0]].Source
		mk[name] = ref
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
}

// mustBeTheMigratedStore is what a sweep over a plantLegacyDirectoryMarketplace
// store recorded as "foo@bar" has to leave behind: the marketplace under its
// migrated name, and widget keyed under that.
func mustBeTheMigratedStore(t *testing.T, m *Manager) {
	t.Helper()
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mk["foo-bar"]; !ok || len(mk) != 1 {
		t.Fatalf("%s holds %v, want foo-bar alone", marketplacesFileName, mk)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Plugins[registryKey("widget", "foo-bar")]; !ok || len(reg.Plugins) != 1 {
		t.Fatalf("registry keys = %v, want widget@foo-bar alone", reg.Plugins)
	}
}

// installedAt is the one entry the registry holds under key, or a failure
// naming what it holds instead.
func installedAt(t *testing.T, reg Registry, key string) InstallEntry {
	t.Helper()
	entries, ok := reg.Plugins[key]
	if !ok || len(entries) != 1 {
		t.Fatalf("registry has no single entry under %s: %v", key, reg.Plugins)
	}
	return entries[0]
}

// keyedInstalls is the registry as one sorted picture: every plugin against
// the key it is under and the path it is installed at, marked where that path
// is not on disk. A migration decides key and path together, so a test that
// compares the whole picture says what the store came out as, rather than the
// first thing about it that was wrong.
func keyedInstalls(reg Registry) []string {
	var keyed []string
	for key, entries := range reg.Plugins {
		for _, entry := range entries {
			line := key + " installed at " + entry.InstallPath
			if _, err := os.Stat(entry.InstallPath); err != nil {
				line += ", which is not there"
			}
			keyed = append(keyed, line)
		}
	}
	slices.Sort(keyed)
	return keyed
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is there (stat: %v)", path, err)
	}
}

// renameMarkerFile is where the migration keeps the marker naming the rename
// it has in flight. The tests name the file and write its JSON themselves,
// because it is what one run leaves for the next: the evener that reads it is
// never the one that wrote it.
func renameMarkerFile(m *Manager) string {
	return filepath.Join(m.Root, "marketplace-rename.json")
}

// plantRenameMarker leaves the marker a rename writes before it moves
// anything, which is what a run that exited mid-rename leaves behind.
func plantRenameMarker(t *testing.T, m *Manager, from, to string) {
	t.Helper()
	body := fmt.Sprintf("{\n  \"from\": %q,\n  \"to\": %q\n}\n", from, to)
	if err := os.WriteFile(renameMarkerFile(m), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plantMergeMarker leaves the marker a merge writes before it moves any keys,
// which is what a run that exited mid-merge leaves behind.
func plantMergeMarker(t *testing.T, m *Manager, from, to string) {
	t.Helper()
	body := fmt.Sprintf("{\n  \"from\": %q,\n  \"to\": %q,\n  \"merge\": true\n}\n", from, to)
	if err := os.WriteFile(renameMarkerFile(m), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The name a refused one is renamed to follows one rule, so a user can tell
// from the old name what the new one will be: separators split it and the
// traversal components go, '@' becomes '-', a scratch name loses its dot, and
// what has nothing left falls back to a fixed stand-in. Whatever comes in,
// what comes out is a name the store accepts.
func TestMigratedMarketplaceName(t *testing.T) {
	cases := map[string]string{
		"foo@bar":        "foo-bar",
		"a@b@c":          "a-b-c",
		"@":              "-",
		".staging":       "staging",
		".old":           "old",
		"./.staging":     "staging",
		"../../escape":   "escape",
		"a/b":            "a-b",
		`a\b`:            "a-b",
		"/abs/path":      "abs-path",
		"../foo@bar/..":  "foo-bar",
		"..":             "marketplace",
		".":              "marketplace",
		"":               "marketplace",
		"/":              "marketplace",
		".hidden@corp":   ".hidden-corp",
		"acme.v2/plugin": "acme.v2-plugin",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := migratedMarketplaceName(name)
			if got != want {
				t.Fatalf("migratedMarketplaceName(%q) = %q, want %q", name, got, want)
			}
			if err := validNameComponent("marketplace", got); err != nil {
				t.Fatalf("the derived name %q is itself refused: %v", got, err)
			}
		})
	}
}

// With both foo@bar and bar recorded, the key widget@foo@bar is plugin
// "widget" from foo@bar and equally plugin "widget@foo" from bar. The
// migration settles it once: foo@bar becomes foo-bar, its clone and cache
// follow, and its keys re-key exactly, leaving bar's own where they are.
func TestMarketplaceNameMigration_RenamesAnAtNamedMarketplaceBesideItsSuffix(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	gadget := plantLegacyMarketplace(t, m, "bar", "gadget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want foo-bar and bar", mk)
	}
	ref, ok := mk["foo-bar"]
	if !ok {
		t.Fatalf("foo-bar not registered: %v", mk)
	}
	if _, ok := mk["bar"]; !ok {
		t.Fatalf("bar not registered: %v", mk)
	}
	if want := m.marketplaceDir("foo-bar"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("foo-bar"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir("foo@bar"))

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@foo-bar and gadget@bar", reg.Plugins)
	}
	widget := installedAt(t, reg, registryKey("widget", "foo-bar"))
	if want := m.pluginCacheDir("foo-bar", "widget", "sha1"); widget.InstallPath != want {
		t.Fatalf("widget InstallPath = %q, want %q", widget.InstallPath, want)
	}
	mustExist(t, widget.InstallPath)
	mustNotExist(t, filepath.Join(m.cacheDir(), "foo@bar"))
	if got := installedAt(t, reg, registryKey("gadget", "bar")).InstallPath; got != gadget {
		t.Fatalf("gadget InstallPath = %q, want it untouched at %q", got, gadget)
	}
	mustExist(t, gadget)
}

// The listing reads without the store lock, so what it read is not what it
// returns: a refused name sends it through the lock, and both the map it
// hands back and the file on disk are the migrated ones.
func TestListMarketplaces_MigratesARefusedName(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if _, ok := mk["foo-bar"]; !ok || len(mk) != 1 {
		t.Fatalf("ListMarketplaces = %v, want foo-bar alone", mk)
	}
	onDisk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk["foo-bar"]; !ok || len(onDisk) != 1 {
		t.Fatalf("%s holds %v, want foo-bar alone", marketplacesFileName, onDisk)
	}
}

// The migrating branch waits on the store lock, and the hub reaches it inline
// on the connection's serial worker, so it waits on the caller's context: a
// disconnected client's listing gives up instead of spinning out the full
// lock timeout.
func TestListMarketplaces_ObservesTheCallersCancellation(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := m.ListMarketplaces(ctx); err == nil {
		t.Fatal("ListMarketplaces returned while the store lock was held")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ListMarketplaces took %v after cancellation; want prompt return", elapsed)
	}
}

// The plugin listing reads the registry without the store lock, and in a
// never-migrated store every key it reads splits at the wrong '@':
// widget@foo@bar is plugin "widget" from foo@bar, and splitKey reads it as
// "widget@foo" from "bar". So the listing runs behind the same barrier the
// marketplace listing does, and what it hands back is the migrated store's.
func TestList_MigratesARefusedName(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")

	items, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List = %+v, want widget from foo-bar alone", items)
	}
	if got := items[0]; got.Plugin != "widget" || got.Marketplace != "foo-bar" || got.Broken {
		t.Fatalf("List = %+v, want plugin \"widget\" from marketplace \"foo-bar\", installed and whole", got)
	}
	mustBeTheMigratedStore(t, m)

	// Once the store is migrated there is nothing to migrate, and the listing
	// stays lock-free: it never queues behind the fetch a lock holder runs.
	locks := 0
	orig := installAcquireLock
	installAcquireLock = func(ctx context.Context, lockPath string, timeout time.Duration) (func(), error) {
		locks++
		return orig(ctx, lockPath, timeout)
	}
	t.Cleanup(func() { installAcquireLock = orig })
	if _, err := m.List(context.Background()); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if locks != 0 {
		t.Fatalf("a migrated store's listing took the store lock %d times", locks)
	}
}

// The migrating branch waits on the store lock, and the hub reaches this
// listing inline on the connection's serial worker too: a disconnected
// client's listing gives up instead of spinning out the full lock timeout.
func TestList_ObservesTheCallersCancellation(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	release, err := acquireLock(context.Background(), m.lockPath(), time.Second)
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := m.List(ctx); err == nil {
		t.Fatal("List returned while the store lock was held")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("List took %v after cancellation; want prompt return", elapsed)
	}
}

// A marketplace recorded under a scratch name keeps its clone in the store's
// own scratch directory, which the next fetch clears. The rename is how it
// gets out: the clone moves with the name, and the entry records the move.
func TestMarketplaceNameMigration_MovesAScratchNamedClone(t *testing.T) {
	for _, scratch := range []string{stagingCloneName, asideCloneName} {
		t.Run(scratch, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyMarketplace(t, m, scratch, "widget")
			want := strings.TrimPrefix(scratch, ".")

			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatalf("ListMarketplaces: %v", err)
			}
			ref, ok := mk[want]
			if !ok || len(mk) != 1 {
				t.Fatalf("marketplaces = %v, want %s alone", mk, want)
			}
			if ref.InstallLocation != m.marketplaceDir(want) {
				t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, m.marketplaceDir(want))
			}
			mustExist(t, filepath.Join(m.marketplaceDir(want), ".claude-plugin", "marketplace.json"))
			mustNotExist(t, m.marketplaceDir(scratch))
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			entry := installedAt(t, reg, registryKey("widget", want))
			if entry.InstallPath != m.pluginCacheDir(want, "widget", "sha1") {
				t.Fatalf("InstallPath = %q, want it under cache/%s", entry.InstallPath, want)
			}
			mustExist(t, entry.InstallPath)
		})
	}
}

// A recorded name that is not a path component derives directories outside
// the store — <marketplaces>/../../escape is a sibling of the store root, not
// a clone — so the migration renames the record and its keys and moves
// nothing, leaving whatever the bogus paths name exactly where it was.
func TestMarketplaceNameMigration_MovesNothingForATraversingName(t *testing.T) {
	const recorded = "../../escape"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	outside := filepath.Join(filepath.Dir(m.Root), "escape")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keepme"), []byte("not the store's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.cacheDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{recorded: {
		Source:      Source{Kind: SourceURL, URL: "https://example.invalid/escape.git"},
		LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}
	planted := InstallEntry{
		InstallPath: filepath.Join(outside, "widget"),
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/widget"},
	}
	if err := m.saveRegistry(Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey("widget", recorded): {planted},
	}}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["escape"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want escape alone", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want the unfetched entry left unfetched", ref.InstallLocation)
	}
	mustExist(t, filepath.Join(outside, "keepme"))
	mustNotExist(t, m.marketplaceDir("escape"))
	mustNotExist(t, filepath.Join(m.cacheDir(), "escape"))
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := installedAt(t, reg, registryKey("widget", "escape")); got != planted {
		t.Fatalf("entry = %+v, want it re-keyed and otherwise as planted: %+v", got, planted)
	}
	if _, still := reg.Plugins[registryKey("widget", recorded)]; still {
		t.Fatal("the old registry key survived")
	}
}

// A traversing name derives its clone path outside the store, and an older
// evener that fetched the entry recorded that outside path as its install
// location. Nothing out there is the store's to move, so the migration leaves
// it and drops the location instead: the entry is unfetched, and the next
// fetch clones under the new name, inside the store, rather than pulling and
// swapping over a directory the store never made.
func TestMarketplaceNameMigration_UnfetchesAGitBackedTraversingName(t *testing.T) {
	const recorded = "../../escape"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	outside := plantCatalog(t, m.marketplaceDir(recorded))
	if err := os.WriteFile(filepath.Join(outside, "keepme"), []byte("not the store's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{recorded: {
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/escape.git"},
		InstallLocation: outside,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["escape"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want escape alone", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	mustExist(t, filepath.Join(outside, "keepme"))
	mustNotExist(t, m.marketplaceDir("escape"))

	origClone := marketplaceGitClone
	t.Cleanup(func() { marketplaceGitClone = origClone })
	marketplaceGitClone = func(_ context.Context, _, dest, _, _ string) error {
		plantCatalog(t, dest)
		return nil
	}
	if err := m.RefreshMarketplace(context.Background(), "escape"); err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("escape"), ".claude-plugin", "marketplace.json"))
	mustExist(t, filepath.Join(outside, "keepme"))
}

// A directory-sourced marketplace's install location is the user's own source
// path, and every such path is outside the store. Dropping it would unfetch a
// marketplace the store never fetched, so the migration keeps it whatever the
// recorded name derives.
func TestMarketplaceNameMigration_KeepsADirectorySourcesPathForATraversingName(t *testing.T) {
	const recorded = "../../escape"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	dir := makeDirectoryMarketplace(t, "escape", "widget")
	if err := m.saveMarketplaces(Marketplaces{recorded: {
		Source:          Source{Kind: SourceDirectory, Path: dir},
		InstallLocation: dir,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["escape"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want escape alone", mk)
	}
	if ref.InstallLocation != dir {
		t.Fatalf("InstallLocation = %q, want the source path %q", ref.InstallLocation, dir)
	}
	mustExist(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"))
}

// A name carrying a separator derives directories that really are inside the
// store: <marketplaces>/a/b and cache/a/b are where an older evener put this
// marketplace's clone and its plugins, one level down. They are this
// marketplace's, so the migration moves them under the flat new name exactly
// as a rename does, and the record and the install paths follow.
func TestMarketplaceNameMigration_MovesANestedCloneAndCache(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	nested := plantLegacyMarketplace(t, m, "a/b", "widget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, filepath.Join(m.marketplacesDir(), "a", "b"))
	mustNotExist(t, nested)

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "a-b"))
	if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
		t.Fatalf("InstallPath = %q, want %q", entry.InstallPath, want)
	}
	mustExist(t, entry.InstallPath)
}

// A separator name derives directories inside the store that another recorded
// marketplace already owns: "a/b" beside a valid "a" names a subdirectory of
// a's clone and the cache directory of a's plugin "b". They are inside the
// store but they are a's, so the migration leaves a's clone, a's plugin and
// a's registry entry exactly as they were, and re-keys the record where it
// stands — taking with it only the cache directory of its own plugin.
func TestMarketplaceNameMigration_LeavesAnotherMarketplacesNestedDirs(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	pluginB := plantLegacyMarketplace(t, m, "a", "b")
	plantLegacyMarketplace(t, m, "a/b", "widget")
	marker := filepath.Join(m.marketplaceDir("a"), "b", "keepme")
	if err := os.WriteFile(marker, []byte("marketplace a's"), 0o644); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	mustExist(t, marker)
	mustExist(t, pluginB)
	items, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("List = %+v, want b@a and widget@a-b", items)
	}
	if b := items[0]; b.Plugin != "b" || b.Marketplace != "a" || b.Broken {
		t.Fatalf("b from a lists as %+v, want it installed and whole", b)
	}
	if widget := items[1]; widget.Plugin != "widget" || widget.Marketplace != "a-b" || widget.Broken {
		t.Fatalf("widget from a-b lists as %+v, want it installed and whole", widget)
	}

	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a and a-b", mk)
	}
	if want := m.marketplaceDir("a"); mk["a"].InstallLocation != want {
		t.Fatalf("a's InstallLocation = %q, want %q", mk["a"].InstallLocation, want)
	}
	ref, ok := mk["a-b"]
	if !ok {
		t.Fatalf("a-b not registered: %v", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("a-b's InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	mustNotExist(t, m.marketplaceDir("a-b"))

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := installedAt(t, reg, registryKey("widget", "a-b")).InstallPath, m.pluginCacheDir("a-b", "widget", "sha1"); got != want {
		t.Fatalf("widget's InstallPath = %q, want its own plugin cache at %q", got, want)
	}
	mustNotExist(t, filepath.Join(m.cacheDir(), "a", "b", "widget"))
	if got := installedAt(t, reg, registryKey("b", "a")).InstallPath; got != pluginB {
		t.Fatalf("b's InstallPath = %q, want it untouched at %q", got, pluginB)
	}
}

// The blocked entry keeps the owner's directories but not its own plugins'
// caches: they sit at cache/a/b/<plugin>/<sha>, one level below the
// cache/<marketplace>/<plugin>/<sha> layout Gc reads, so left there the sweep
// the hub runs at startup takes cache/a/b/widget for an unreferenced sha
// directory and reclaims the install the rename just carried over. Each of the
// entry's own plugin directories moves under the new name instead, where Gc
// reads it as the install it is.
func TestMarketplaceNameMigration_MovesTheOwnPluginCachesOfABlockedName(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	pluginP := plantLegacyMarketplace(t, m, "a", "p")
	plantLegacyMarketplace(t, m, "a/b", "widget")

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"p@a installed at " + pluginP,
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	mustNotExist(t, filepath.Join(m.cacheDir(), "a", "b", "widget"))
	// Marketplace a keeps every directory of its own: the clone holding
	// a/b's, and its own plugin's cache beside the one that left.
	mustExist(t, filepath.Join(m.marketplaceDir("a"), "b", ".claude-plugin", "marketplace.json"))
	mustExist(t, pluginP)

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed %v, want every install the migration left kept", removed)
	}
	mustExist(t, m.pluginCacheDir("a-b", "widget", "sha1"))
}

// A name that cleans to another marketplace's own directories derives exactly
// them: "./x" beside a valid "x" joins to x's clone and x's plugin cache.
// Renaming those would leave x recording a clone that is no longer there and
// its plugins pointing at moved paths, so the migration takes a numbered name
// and moves nothing.
func TestMarketplaceNameMigration_LeavesTheDirsADotNameAliases(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	widget := plantLegacyMarketplace(t, m, "x", "widget")
	recorded, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	recorded["./x"] = MarketplaceRef{
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/x.git"},
		InstallLocation: m.marketplaceDir("./x"),
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(recorded); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("x"), ".claude-plugin", "marketplace.json"))
	mustExist(t, widget)
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want x and x-2", mk)
	}
	if want := m.marketplaceDir("x"); mk["x"].InstallLocation != want {
		t.Fatalf("x's InstallLocation = %q, want %q", mk["x"].InstallLocation, want)
	}
	ref, ok := mk["x-2"]
	if !ok {
		t.Fatalf("x-2 not registered: %v", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("x-2's InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	mustNotExist(t, m.marketplaceDir("x-2"))
	mustNotExist(t, filepath.Join(m.cacheDir(), "x-2"))

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := installedAt(t, reg, registryKey("widget", "x")).InstallPath; got != widget {
		t.Fatalf("widget's InstallPath = %q, want it untouched at %q", got, widget)
	}
}

// A name cleaning to another marketplace's own cache ("./x" beside "x")
// derives that marketplace's plugin directories themselves, so one directory
// holds the one plugin both records name. It is the owner's rather than this
// entry's to take with the name — moving it would leave the owner's entry
// pointing where the files no longer are — and it already sits at the depth Gc
// reads, so both keys go on naming it where it is.
func TestMarketplaceNameMigration_LeavesAPluginCacheAnotherRecordInstallsIn(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	widget := plantLegacyMarketplace(t, m, "x", "widget")
	recorded, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	recorded["./x"] = MarketplaceRef{
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/dot-x.git"},
		InstallLocation: m.marketplaceDir("./x"),
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(recorded); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// The one install both records hold: "./x" derives x's own cache, so an
	// older evener materialized its widget at exactly x's widget.
	reg.Plugins[registryKey("widget", "./x")] = []InstallEntry{{
		InstallPath: m.pluginCacheDir("./x", "widget", "sha1"),
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/widget"},
	}}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if reg, err = m.loadRegistry(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"widget@x installed at " + widget,
		"widget@x-2 installed at " + widget,
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	mustNotExist(t, filepath.Join(m.cacheDir(), "x-2"))
}

// A refused name is no owner of the directories it nests around, because it
// is migrating too: "a@b" and "a@b/c" both move, the longer one first, and
// each takes its own clone and cache with it. Deferring to a sibling that is
// itself about to move would leave the longer name re-keyed in place and its
// installs buried under the shorter one's new directories.
func TestMarketplaceNameMigration_MovesBothOfANestedRefusedPair(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a@b", "widget")
	plantLegacyMarketplace(t, m, "a@b/c", "gadget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b and gadget@a-b-c alone", reg.Plugins)
	}
	for _, installed := range []struct{ plugin, marketplace string }{{"widget", "a-b"}, {"gadget", "a-b-c"}} {
		entry := installedAt(t, reg, registryKey(installed.plugin, installed.marketplace))
		mustExist(t, entry.InstallPath)
		if want := m.pluginCacheDir(installed.marketplace, installed.plugin, "sha1"); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want %q", installed.plugin, entry.InstallPath, want)
		}
	}

	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-c", mk)
	}
	for _, name := range []string{"a-b", "a-b-c"} {
		ref, ok := mk[name]
		if !ok {
			t.Fatalf("%s not registered: %v", name, mk)
		}
		if want := m.marketplaceDir(name); ref.InstallLocation != want {
			t.Fatalf("%s's InstallLocation = %q, want %q", name, ref.InstallLocation, want)
		}
		mustExist(t, filepath.Join(m.marketplaceDir(name), ".claude-plugin", "marketplace.json"))
	}
	mustNotExist(t, filepath.Join(m.marketplaceDir("a-b"), "c"))
	mustNotExist(t, filepath.Join(m.marketplacesDir(), "a@b"))
}

// The derived name has to be free, and free means what a rename means by it:
// no recorded marketplace, and none of the residue a rename onto it would
// bury. Each occupant pushes the migration to the next numbered name.
func TestMarketplaceNameMigration_SuffixesADerivedNameThatIsTaken(t *testing.T) {
	occupants := map[string]func(t *testing.T, m *Manager){
		"a recorded marketplace": func(t *testing.T, m *Manager) {
			mk, err := m.loadMarketplaces()
			if err != nil {
				t.Fatal(err)
			}
			dir := makeDirectoryMarketplace(t, "foo-bar", "gadget")
			mk["foo-bar"] = MarketplaceRef{Source: Source{Kind: SourceDirectory, Path: dir}, InstallLocation: dir}
			if err := m.saveMarketplaces(mk); err != nil {
				t.Fatal(err)
			}
		},
		"a marketplace clone": func(t *testing.T, m *Manager) {
			if err := os.MkdirAll(m.marketplaceDir("foo-bar"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a plugin cache": func(t *testing.T, m *Manager) {
			if err := os.MkdirAll(filepath.Join(m.cacheDir(), "foo-bar", "gadget", "deadsha"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"a registry entry": func(t *testing.T, m *Manager) {
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			reg.Plugins[registryKey("gadget", "foo-bar")] = []InstallEntry{{
				InstallPath: filepath.Join(m.cacheDir(), "foo-bar", "gadget", "deadsha"),
				Source:      Source{Kind: SourceGitHub, Repo: "o/gadget"},
			}}
			if err := m.saveRegistry(reg); err != nil {
				t.Fatal(err)
			}
		},
	}
	for what, occupy := range occupants {
		t.Run(what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyMarketplace(t, m, "foo@bar", "widget")
			occupy(t, m)

			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatalf("ListMarketplaces: %v", err)
			}
			if _, ok := mk["foo-bar-2"]; !ok {
				t.Fatalf("marketplaces = %v, want foo-bar-2", mk)
			}
			if _, ok := mk["foo@bar"]; ok {
				t.Fatalf("foo@bar is still recorded: %v", mk)
			}
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			entry := installedAt(t, reg, registryKey("widget", "foo-bar-2"))
			if entry.InstallPath != m.pluginCacheDir("foo-bar-2", "widget", "sha1") {
				t.Fatalf("InstallPath = %q, want it under cache/foo-bar-2", entry.InstallPath)
			}
			mustExist(t, entry.InstallPath)
		})
	}
}

// Every key of z@foo@bar also ends in "@foo@bar", so re-keying foo@bar first
// would carry gizmo@z@foo@bar off to gizmo@z@foo-bar, a key nothing reads.
// Longer names go first: once z@foo@bar's keys end in "@z-foo-bar", the keys
// left ending in "@foo@bar" are foo@bar's own.
func TestMarketplaceNameMigration_MigratesLongerNamesFirst(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	plantLegacyMarketplace(t, m, "z@foo@bar", "gizmo")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	for _, name := range []string{"foo-bar", "z-foo-bar"} {
		if _, ok := mk[name]; !ok {
			t.Fatalf("%s not registered: %v", name, mk)
		}
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@foo-bar and gizmo@z-foo-bar", reg.Plugins)
	}
	for _, mkt := range []struct{ name, plugin string }{{"foo-bar", "widget"}, {"z-foo-bar", "gizmo"}} {
		entry := installedAt(t, reg, registryKey(mkt.plugin, mkt.name))
		if entry.InstallPath != m.pluginCacheDir(mkt.name, mkt.plugin, "sha1") {
			t.Fatalf("%s InstallPath = %q, want it under cache/%s", mkt.plugin, entry.InstallPath, mkt.name)
		}
		mustExist(t, entry.InstallPath)
	}
}

// A name is not as deep as it is long: "a/b/.." is longer than "a/b" and
// resolves to <marketplaces>/a, the directory holding "a/b"'s clone. Migrating
// it first would carry that clone and that plugin cache off under a name
// deriving neither, leaving "a/b"'s record keyed to paths nothing holds. The
// descendant goes before its ancestor, and each record comes out holding the
// directory its own name derived.
func TestMarketplaceNameMigration_MigratesADescendantBeforeItsAncestor(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	plantLegacyMarketplace(t, m, "a/b/..", "gadget")
	marker := filepath.Join(m.marketplacesDir(), "a", "keepme")
	if err := os.WriteFile(marker, []byte("the parent directory's own"), 0o644); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// Every plugin under the record whose cache holds its files: the clone
	// that moved out from under the parent took its own with it, and the
	// parent's plugins stayed in the parent.
	want := []string{
		"gadget@a-b-2 installed at " + m.pluginCacheDir("a-b-2", "gadget", "sha1"),
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}

	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2 alone", mk)
	}
	if got := mk["a-b"].Source.URL; got != "https://example.invalid/widget.git" {
		t.Fatalf("a-b's source = %q, want the marketplace whose own clone moved there", got)
	}
	if got, want := mk["a-b-2"].InstallLocation, m.marketplaceDir("a-b-2"); got != want {
		t.Fatalf("a-b-2's InstallLocation = %q, want the parent directory, moved to %q", got, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("a-b-2"), "keepme"))
	mustNotExist(t, filepath.Join(m.marketplaceDir("a-b-2"), "b"))
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, filepath.Join(m.marketplacesDir(), "a"))
}

// A name whose directories resolve outside the store still holds another's
// keys: every key of "../..@a/b" ends in "@a/b" too, so re-keying "a/b" first
// cuts that suffix off gadget's key and lands it under "a-b", a marketplace it
// never named. The directories say the opposite of what the keys need here —
// the escaping name derives a shallower directory than the nested name it ends
// in, and nothing of its own to move — so the order answers both relations,
// and each record comes out holding its own keys.
func TestMarketplaceNameMigration_ReKeysAnOutOfStoreNameBeforeItsSuffix(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	gadget := plantLegacyMarketplace(t, m, "../..@a/b", "gadget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gadget@..-a-b installed at " + gadget,
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and ..-a-b alone", mk)
	}
	for _, mkt := range []struct{ name, plugin string }{{"a-b", "widget"}, {"..-a-b", "gadget"}} {
		if got, want := mk[mkt.name].Source.URL, "https://example.invalid/"+mkt.plugin+".git"; got != want {
			t.Fatalf("%s's source = %q, want %q, the marketplace whose own name derived it", mkt.name, got, want)
		}
	}
}

// The two relations at once, agreeing on one order: "a/b" holds the directory
// "a/b/.." derives, so it moves first, while every key of "x@a/b" ends in
// "@a/b" too, so it re-keys before "a/b" does. Each record comes out with its
// own keys, its own clone and its own plugin cache.
func TestMarketplaceNameMigration_OrdersADescendantAndAnAtSuffixTogether(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	plantLegacyMarketplace(t, m, "a/b/..", "gadget")
	plantLegacyMarketplace(t, m, "x@a/b", "gizmo")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gadget@a-b-2 installed at " + m.pluginCacheDir("a-b-2", "gadget", "sha1"),
		"gizmo@x-a-b installed at " + m.pluginCacheDir("x-a-b", "gizmo", "sha1"),
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	if len(mk) != 3 {
		t.Fatalf("marketplaces = %v, want a-b, a-b-2 and x-a-b alone", mk)
	}
	for _, mkt := range []struct{ name, plugin string }{{"a-b", "widget"}, {"a-b-2", "gadget"}, {"x-a-b", "gizmo"}} {
		ref := mk[mkt.name]
		if want := "https://example.invalid/" + mkt.plugin + ".git"; ref.Source.URL != want {
			t.Fatalf("%s's source = %q, want %q, the marketplace whose own name derived it", mkt.name, ref.Source.URL, want)
		}
		if want := m.marketplaceDir(mkt.name); ref.InstallLocation != want {
			t.Fatalf("%s's InstallLocation = %q, want its own clone at %q", mkt.name, ref.InstallLocation, want)
		}
		mustExist(t, filepath.Join(m.marketplaceDir(mkt.name), ".claude-plugin", "marketplace.json"))
	}
	mustNotExist(t, filepath.Join(m.marketplacesDir(), "a"))
}

// The shape that reads as a contradiction until the directory relation is
// asked what it has to move: "../..@a/.." is a "@"-superstring of "a/..", so
// the keys want it to re-key first, and it resolves to the store root, the
// directory ancestor of the marketplaces directory "a/.." resolves to, so
// containment alone would want it to move last. Neither name has a directory
// of the store's — the marketplaces directory is not one, and the store root
// is above it — so the directory relation binds nothing here, the keys decide,
// and each record comes out holding its own.
func TestMarketplaceNameMigration_BindsTheDirectoryOrderToDirectoriesTheStoreHolds(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	widget := plantLegacyMarketplace(t, m, "a/..", "widget")
	gadget := plantLegacyMarketplace(t, m, "../..@a/..", "gadget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	want := []string{
		"gadget@..-a installed at " + gadget,
		"widget@a installed at " + widget,
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a and ..-a alone", mk)
	}
	for _, mkt := range []struct{ name, plugin string }{{"a", "widget"}, {"..-a", "gadget"}} {
		if got, want := mk[mkt.name].Source.URL, "https://example.invalid/"+mkt.plugin+".git"; got != want {
			t.Fatalf("%s's source = %q, want %q, the marketplace whose own name derived it", mkt.name, got, want)
		}
	}
}

// Once every recorded name is valid there is nothing to migrate, and a store
// that needs nothing is not written: neither the lockless listing nor a lock
// holder rewrites either file.
func TestMarketplaceNameMigration_ASecondLoadChangesNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	mkBefore := readStoreFile(t, m.marketplacesFile())
	regBefore := readStoreFile(t, m.registryPath())

	writes := 0
	origWrite, origSave := marketplaceAtomicWriteFile, installSaveRegistry
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		writes++
		return origWrite(path, data, perm)
	}
	installSaveRegistry = func(path string, reg Registry) error {
		writes++
		return origSave(path, reg)
	}
	t.Cleanup(func() { marketplaceAtomicWriteFile, installSaveRegistry = origWrite, origSave })

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("second ListMarketplaces: %v", err)
	}
	// Gc takes the store lock and writes nothing of its own.
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if writes != 0 {
		t.Fatalf("a migrated store was written %d times", writes)
	}
	if got := readStoreFile(t, m.marketplacesFile()); got != mkBefore {
		t.Fatalf("%s changed:\n%s", marketplacesFileName, got)
	}
	if got := readStoreFile(t, m.registryPath()); got != regBefore {
		t.Fatalf("%s changed:\n%s", registryFileName, got)
	}
}

// A migration that cannot save is an edit that cannot save: the directories
// go back, the registry as found is what stays on disk, and the operation
// that took the lock fails with an error naming the entry. Once the store can
// be written again the next lock holder finishes the job.
func TestMarketplaceNameMigration_AFailedSaveLeavesTheStoreAsFound(t *testing.T) {
	failures := map[string]func() (restore func()){
		"the marketplaces file": func() func() {
			orig := marketplaceAtomicWriteFile
			marketplaceAtomicWriteFile = func(string, []byte, os.FileMode) error { return errors.New("boom") }
			return func() { marketplaceAtomicWriteFile = orig }
		},
		"the registry": func() func() {
			orig := installSaveRegistry
			installSaveRegistry = func(string, Registry) error { return errors.New("boom") }
			return func() { installSaveRegistry = orig }
		},
	}
	for what, inject := range failures {
		t.Run(what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			widget := plantLegacyMarketplace(t, m, "foo@bar", "widget")
			before := map[string]string{
				m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
				m.registryPath():     readStoreFile(t, m.registryPath()),
			}
			restore := inject()
			t.Cleanup(restore)

			_, err := m.ListMarketplaces(context.Background())
			restore()
			if err == nil {
				t.Fatal("expected the save to fail")
			}
			if !strings.Contains(err.Error(), `"foo@bar"`) {
				t.Fatalf("error = %v, want it to name the entry", err)
			}
			for path, want := range before {
				if got := readStoreFile(t, path); got != want {
					t.Fatalf("%s changed after a failed save:\n%s", path, got)
				}
			}
			mustExist(t, filepath.Join(m.marketplaceDir("foo@bar"), ".claude-plugin", "marketplace.json"))
			mustExist(t, widget)
			mustNotExist(t, m.marketplaceDir("foo-bar"))
			mustNotExist(t, filepath.Join(m.cacheDir(), "foo-bar"))

			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatalf("ListMarketplaces once the store can be written: %v", err)
			}
			if _, ok := mk["foo-bar"]; !ok || len(mk) != 1 {
				t.Fatalf("marketplaces = %v, want foo-bar alone", mk)
			}
		})
	}
}

// Every operation that takes the store lock finds the migrated name: on a
// store nothing has listed, the name the entry was recorded under is gone and
// the one it was renamed to works.
func TestMarketplaceNameMigration_RunsUnderEveryStoreLock(t *testing.T) {
	ctx := context.Background()
	ops := []struct {
		what string
		run  func(*Manager, string) error
	}{
		{"browse", func(m *Manager, name string) error { _, err := m.Browse(ctx, name); return err }},
		{"refresh", func(m *Manager, name string) error { return m.RefreshMarketplace(ctx, name) }},
		{"remove", func(m *Manager, name string) error { return m.RemoveMarketplace(ctx, name) }},
		{"edit", func(m *Manager, name string) error {
			_, err := m.EditMarketplace(ctx, name, "renamed", nil)
			return err
		}},
		{"install", func(m *Manager, name string) error { _, err := m.Install(ctx, "widget", name); return err }},
	}
	for _, op := range ops {
		t.Run(op.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			dir := makeDirectoryMarketplace(t, "acme", "widget")
			if err := m.saveMarketplaces(Marketplaces{stagingCloneName: {
				Source:          Source{Kind: SourceDirectory, Path: dir},
				InstallLocation: dir,
				LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
			}}); err != nil {
				t.Fatal(err)
			}

			if err := op.run(m, "staging"); err != nil {
				t.Fatalf("%s of the migrated name: %v", op.what, err)
			}
			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, still := mk[stagingCloneName]; still {
				t.Fatalf("the recorded name survived %s: %v", op.what, mk)
			}
		})
	}
}

// An upgrade reads the registry before it reads the marketplaces file, so the
// migration has to have run before either read: were it a side effect of
// loading the marketplaces file, the upgrade would carry the registry it
// read first — still keyed under the old name — through the migration and
// save it back over the re-keyed one. The key widget@foo@bar splits at its
// last '@' as "widget@foo" from "bar", which is how an older evener's
// listing would have named this install; asking for it finds nothing because
// the registry the upgrade reads is the migrated one.
func TestUpgrade_ReadsTheRegistryTheMigrationLeft(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	plantLegacyMarketplace(t, m, "bar", "gadget")

	_, err := m.Upgrade(context.Background(), "widget@foo", "bar")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Upgrade = %v, want ErrNotInstalled", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "foo-bar"))
	if entry.InstallPath != m.pluginCacheDir("foo-bar", "widget", "sha1") {
		t.Fatalf("InstallPath = %q, want it under cache/foo-bar", entry.InstallPath)
	}
	if _, still := reg.Plugins[registryKey("widget", "foo@bar")]; still {
		t.Fatal("the old registry key survived the upgrade")
	}
}

// A sweep enumerates the registry to decide what to upgrade, and the store
// lock each per-plugin upgrade takes is what migrates the store: a key read
// before that names a marketplace the migration is about to rename, so the
// sweep asks to upgrade a split of a key nothing is installed under. Taking
// the lock first is what makes the enumeration the migration's — including on
// a store the sweep upgrades nothing in, which it would otherwise never lock
// for at all.
func TestUpdateAll_SweepsTheKeysTheMigrationLeft(t *testing.T) {
	for what, src := range map[string]Source{
		"a git-backed install":       {Kind: SourceGitHub, Repo: "o/widget"},
		"nothing the sweep upgrades": {Kind: SourceDirectory, Path: "in place"},
	} {
		t.Run(what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyDirectoryMarketplace(t, m, "foo@bar", "widget", src)

			if _, err := m.UpdateAll(context.Background()); err != nil {
				t.Fatalf("UpdateAll: %v", err)
			}
			mustBeTheMigratedStore(t, m)
		})
	}
}

// The auto-upgrade sweep enumerates the same way and needs the same thing of
// its keys.
func TestUpdateAutoUpgrade_SweepsTheKeysTheMigrationLeft(t *testing.T) {
	for what, src := range map[string]Source{
		"a git-backed install":       {Kind: SourceGitHub, Repo: "o/widget"},
		"nothing the sweep upgrades": {Kind: SourceDirectory, Path: "in place"},
	} {
		t.Run(what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyDirectoryMarketplace(t, m, "foo@bar", "widget", src)

			if _, err := m.UpdateAutoUpgrade(context.Background()); err != nil {
				t.Fatalf("UpdateAutoUpgrade: %v", err)
			}
			mustBeTheMigratedStore(t, m)
		})
	}
}

// The user learns the new name from one line per rename, in the order the
// renames ran, and hears nothing once there is nothing left to rename.
func TestMarketplaceNameMigration_ReportsEachRename(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	plantLegacyMarketplace(t, m, asideCloneName, "gadget")

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stderr = %q, want one line per rename", stderr.String())
	}
	for i, want := range []struct{ from, to string }{{"foo@bar", "foo-bar"}, {asideCloneName, "old"}} {
		if !strings.Contains(lines[i], `"`+want.from+`"`) || !strings.Contains(lines[i], `"`+want.to+`"`) {
			t.Fatalf("line %d = %q, want it to name %q and %q", i, lines[i], want.from, want.to)
		}
	}

	stderr.Reset()
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("second ListMarketplaces: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a migrated store still warned: %q", stderr.String())
	}
}

// A store whose marketplaces directory is a symlink to another volume is
// still the store: containment is judged under that directory wherever it
// resolves, so a legacy name nesting inside it moves where it lives.
func TestMarketplaceNameMigration_MovesInsideASymlinkedMarketplacesDir(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(root, marketplacesDirName)); err != nil {
		t.Fatal(err)
	}
	m := NewManager(root)
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(elsewhere, "a-b", ".claude-plugin", "marketplace.json"))
	mustNotExist(t, filepath.Join(elsewhere, "a", "b"))

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "a-b"))
	if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
		t.Fatalf("widget InstallPath = %q, want %q", entry.InstallPath, want)
	}
	mustExist(t, entry.InstallPath)
	mustNotExist(t, filepath.Join(m.cacheDir(), "a", "b"))
}

// A rename writes the marker naming it, moves the clone, moves the cache,
// writes the registry and writes the marketplaces file, in that order. A run
// that exits partway through leaves the marker behind, and the next lock
// holder finishes the rename it names: whatever of the two directories is
// still under the old name moves, whatever keys are still under it are
// re-keyed, and the entry comes out recorded under the new name alone, its
// plugin where it says, whichever step the exit fell after.
func TestMarketplaceNameMigration_CompletesTheRenameItsMarkerNames(t *testing.T) {
	const recorded = "a/b"
	moveClone := func(t *testing.T, m *Manager) {
		t.Helper()
		if err := os.Rename(m.marketplaceDir(recorded), m.marketplaceDir("a-b")); err != nil {
			t.Fatal(err)
		}
	}
	moveCache := func(t *testing.T, m *Manager) {
		t.Helper()
		moveClone(t, m)
		if err := os.Rename(filepath.Join(m.cacheDir(), recorded), filepath.Join(m.cacheDir(), "a-b")); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry := func(t *testing.T, m *Manager) {
		t.Helper()
		moveCache(t, m)
		reg, err := m.loadRegistry()
		if err != nil {
			t.Fatal(err)
		}
		entry := installedAt(t, reg, registryKey("widget", recorded))
		entry.InstallPath = m.pluginCacheDir("a-b", "widget", "sha1")
		delete(reg.Plugins, registryKey("widget", recorded))
		reg.Plugins[registryKey("widget", "a-b")] = []InstallEntry{entry}
		if err := m.saveRegistry(reg); err != nil {
			t.Fatal(err)
		}
	}
	writeMarketplaces := func(t *testing.T, m *Manager) {
		t.Helper()
		writeRegistry(t, m)
		mk, err := m.loadMarketplaces()
		if err != nil {
			t.Fatal(err)
		}
		ref := mk[recorded]
		ref.InstallLocation = m.marketplaceDir("a-b")
		delete(mk, recorded)
		mk["a-b"] = ref
		if err := m.saveMarketplaces(mk); err != nil {
			t.Fatal(err)
		}
	}
	points := []struct {
		what  string
		crash func(*testing.T, *Manager)
	}{
		{"after the clone move", moveClone},
		{"after the cache move", moveCache},
		{"after the registry write", writeRegistry},
		{"after the marketplaces write", writeMarketplaces},
	}
	for _, point := range points {
		t.Run(point.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			plantLegacyMarketplace(t, m, recorded, "widget")
			plantRenameMarker(t, m, recorded, "a-b")
			point.crash(t, m)

			// Whichever operation takes the store lock next is what finishes
			// the rename, and a store with nothing refused left in it — the
			// last point below — is not one a listing locks.
			if err := m.migrateStore(context.Background()); err != nil {
				t.Fatalf("migrateStore: %v", err)
			}
			mk, err := m.loadMarketplaces()
			if err != nil {
				t.Fatal(err)
			}
			ref, ok := mk["a-b"]
			if !ok || len(mk) != 1 {
				t.Fatalf("marketplaces = %v, want a-b alone", mk)
			}
			if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
				t.Fatalf("InstallLocation = %q, want the clone the rename moved to %q", ref.InstallLocation, want)
			}
			mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if len(reg.Plugins) != 1 {
				t.Fatalf("registry keys = %v, want widget@a-b alone", reg.Plugins)
			}
			entry := installedAt(t, reg, registryKey("widget", "a-b"))
			if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
				t.Fatalf("widget's InstallPath = %q, want %q", entry.InstallPath, want)
			}
			mustExist(t, entry.InstallPath)
			mustNotExist(t, renameMarkerFile(m))

			mkBefore, regBefore := readStoreFile(t, m.marketplacesFile()), readStoreFile(t, m.registryPath())
			if err := m.migrateStore(context.Background()); err != nil {
				t.Fatalf("second migrateStore: %v", err)
			}
			if got := readStoreFile(t, m.marketplacesFile()); got != mkBefore {
				t.Fatalf("%s changed on the second run:\n%s", marketplacesFileName, got)
			}
			if got := readStoreFile(t, m.registryPath()); got != regBefore {
				t.Fatalf("%s changed on the second run:\n%s", registryFileName, got)
			}
		})
	}
}

// A leftover clone under the derived name is a removed marketplace's unless a
// marker says a rename put it there. An entry the store never fetched has not
// even lost directories to a rename, so the numbered name is what it takes,
// as a rename onto residue always has.
func TestMarketplaceNameMigration_LeavesResidueForANeverFetchedName(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	leftover := plantCatalog(t, m.marketplaceDir("a-b"))
	if err := os.WriteFile(filepath.Join(leftover, "keepme"), []byte("a removed marketplace's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.saveMarketplaces(Marketplaces{"a/b": {
		Source:      Source{Kind: SourceURL, URL: "https://example.invalid/widget.git"},
		LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b-2"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b-2 alone", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	mustExist(t, filepath.Join(leftover, "keepme"))
	mustExist(t, filepath.Join(leftover, ".claude-plugin", "marketplace.json"))
}

// Two refused names can derive the same directories: "a/./b" and "a/b",
// naming the one source, are one marketplace recorded twice. The longer
// migrates first and takes the clone and the cache with it, so the second
// finds its own gone and the new name recorded, and folds into that record —
// one marketplace, one record, every plugin keyed under it and installed
// where it says.
func TestMarketplaceNameMigration_MergesARefusedAliasIntoOneRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	plantLegacyMarketplace(t, m, "a/b", "gadget")
	plantOneSource(t, m, "a/./b", "a/b")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))

	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b and gadget@a-b alone", reg.Plugins)
	}
	for _, plugin := range []string{"widget", "gadget"} {
		entry := installedAt(t, reg, registryKey(plugin, "a-b"))
		if want := m.pluginCacheDir("a-b", plugin, "sha1"); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want %q", plugin, entry.InstallPath, want)
		}
		mustExist(t, entry.InstallPath)
	}
	if !strings.Contains(stderr.String(), "merged") || !strings.Contains(stderr.String(), `"a/b"`) {
		t.Fatalf("warning = %q, want it to say the duplicate %q was merged", stderr.String(), "a/b")
	}

	mkBefore, regBefore := readStoreFile(t, m.marketplacesFile()), readStoreFile(t, m.registryPath())
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("second ListMarketplaces: %v", err)
	}
	if got := readStoreFile(t, m.marketplacesFile()); got != mkBefore {
		t.Fatalf("%s changed on the second run:\n%s", marketplacesFileName, got)
	}
	if got := readStoreFile(t, m.registryPath()); got != regBefore {
		t.Fatalf("%s changed on the second run:\n%s", registryFileName, got)
	}
}

// Both records of the one marketplace can hold the same plugin. The merge
// keeps the entry already under the recorded name — the one the rename that
// moved the directories re-keyed — because that is the install the moved
// cache really holds.
func TestMarketplaceNameMigration_MergeKeepsTheEntryTheFirstRenameLeft(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	mk["a/b"] = MarketplaceRef{
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/widget.git"},
		InstallLocation: m.marketplaceDir("a/b"),
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	duplicate := m.pluginCacheDir("a/b", "widget", "sha2")
	writePlugin(t, duplicate, "widget", nil)
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg.Plugins[registryKey("widget", "a/b")] = []InstallEntry{{
		InstallPath: duplicate,
		Version:     "2.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/widget"},
	}}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 1 {
		t.Fatalf("registry keys = %v, want widget@a-b alone", reg.Plugins)
	}
	entry := installedAt(t, reg, registryKey("widget", "a-b"))
	if entry.Version != "1.0.0" {
		t.Fatalf("widget is version %q, want the 1.0.0 the rename that moved the cache re-keyed", entry.Version)
	}
	if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
		t.Fatalf("widget's InstallPath = %q, want %q", entry.InstallPath, want)
	}
	mustExist(t, entry.InstallPath)
}

// A record under the derived name is an alias's only when this migration made
// it. Nothing on disk tells an "a-b" a rename left from an "a-b" the user
// added — their sources differ either way — so a refused entry whose own
// directories are gone, a store cleared by hand, would otherwise fold into a
// stranger: its source dropped from the marketplaces file and its plugins
// keyed under a marketplace that is not its own. The numbered name it took
// before there was a merge is the safer failure.
func TestMarketplaceNameMigration_DoesNotAbsorbAStrangersRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "a-b", "widget")
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	stranger := mk["a-b"]
	mk["a/b"] = MarketplaceRef{
		Source:          Source{Kind: SourceURL, URL: "https://example.invalid/gadget.git"},
		InstallLocation: m.marketplaceDir("a/b"),
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	installed := installedAt(t, reg, registryKey("widget", "a-b"))

	mk, err = m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2", mk)
	}
	if got := mk["a-b"]; got != stranger {
		t.Fatalf("a-b = %+v, want the record the user made: %+v", got, stranger)
	}
	ref, ok := mk["a-b-2"]
	if !ok {
		t.Fatalf("marketplaces = %v, want the refused entry renamed to a-b-2", mk)
	}
	if ref.Source != (Source{Kind: SourceURL, URL: "https://example.invalid/gadget.git"}) {
		t.Fatalf("a-b-2's source = %+v, want the refused entry's own", ref.Source)
	}
	// The refused entry recorded a clone the hand-cleared store no longer
	// holds, so the rename leaves it unfetched — and, which is what this
	// pins, not pointing at the stranger's.
	if ref.InstallLocation != "" {
		t.Fatalf("InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}

	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 1 {
		t.Fatalf("registry keys = %v, want widget@a-b alone", reg.Plugins)
	}
	if entry := installedAt(t, reg, registryKey("widget", "a-b")); entry != installed {
		t.Fatalf("widget@a-b = %+v, want it as installed: %+v", entry, installed)
	}
	mustExist(t, installed.InstallPath)
	if strings.Contains(stderr.String(), "merged") || !strings.Contains(stderr.String(), `"a-b-2"`) {
		t.Fatalf("warning = %q, want the rename to \"a-b-2\", not a merge", stderr.String())
	}
}

// Two refused names can derive the same new name from different directories:
// "a/./b" clones under <marketplaces>/a/b and "a\b" under <marketplaces>/a\b.
// They are two marketplaces the user added, not one recorded twice, so the
// second keeps its own record and its own source under the numbered name
// rather than folding into the first's.
func TestMarketplaceNameMigration_KeepsAMarketplaceThatOnlySharesTheDerivedName(t *testing.T) {
	const first, second = "a/./b", `a\b`
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, first, "widget")
	plantLegacyMarketplace(t, m, second, "zed")
	// What makes the second look like an alias the first already moved: a
	// store cleared by hand, so its own directories are gone.
	if err := os.RemoveAll(m.marketplaceDir(second)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(m.cacheDir(), second)); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2", mk)
	}
	if got := mk["a-b"].Source.URL; got != "https://example.invalid/widget.git" {
		t.Fatalf("a-b's source = %q, want the marketplace whose directories moved there", got)
	}
	ref, ok := mk["a-b-2"]
	if !ok {
		t.Fatalf("marketplaces = %v, want the second marketplace renamed to a-b-2", mk)
	}
	if got := ref.Source.URL; got != "https://example.invalid/zed.git" {
		t.Fatalf("a-b-2's source = %q, want the second marketplace's own", got)
	}
	if strings.Contains(stderr.String(), "merged") {
		t.Fatalf("warning = %q, want the second marketplace renamed, not merged away", stderr.String())
	}
}

// Deriving the one clone and the one cache is not enough to make two records
// one marketplace: a directory source keeps neither directory in the store,
// so "a/./b" pointing at /x and "a/b" pointing at /y derive the same absent
// pair while naming two marketplaces the user added. Merging them would drop
// the second's source, so each keeps its own record — the second under a
// numbered name — and its plugin where its own source directory holds it.
func TestMarketplaceNameMigration_KeepsTwoSourcesDerivingOnePair(t *testing.T) {
	const first, second = "a/./b", "a/b"
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	x, y := makeDirectoryMarketplace(t, "x", "widget"), makeDirectoryMarketplace(t, "y", "gadget")
	planted := time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := m.saveMarketplaces(Marketplaces{
		first:  {Source: Source{Kind: SourceDirectory, Path: x}, InstallLocation: x, LastUpdated: planted},
		second: {Source: Source{Kind: SourceDirectory, Path: y}, InstallLocation: y, LastUpdated: planted},
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.saveRegistry(Registry{Version: 2, Plugins: map[string][]InstallEntry{
		registryKey("widget", first): {{
			InstallPath: filepath.Join(x, "plugins", "widget"),
			Version:     "1.0.0",
			Enabled:     true,
			Source:      Source{Kind: SourceGitHub, Repo: "o/widget"},
		}},
		registryKey("gadget", second): {{
			InstallPath: filepath.Join(y, "plugins", "gadget"),
			Version:     "1.0.0",
			Enabled:     true,
			Source:      Source{Kind: SourceGitHub, Repo: "o/gadget"},
		}},
	}}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2, one record each", mk)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range []struct{ name, dir, plugin string }{{"a-b", x, "widget"}, {"a-b-2", y, "gadget"}} {
		ref, ok := mk[kept.name]
		if !ok {
			t.Fatalf("%s not registered: %v", kept.name, mk)
		}
		if ref.Source != (Source{Kind: SourceDirectory, Path: kept.dir}) {
			t.Fatalf("%s's source = %+v, want the directory it names, %q", kept.name, ref.Source, kept.dir)
		}
		if ref.InstallLocation != kept.dir {
			t.Fatalf("%s's InstallLocation = %q, want its own source path %q", kept.name, ref.InstallLocation, kept.dir)
		}
		entry := installedAt(t, reg, registryKey(kept.plugin, kept.name))
		if want := filepath.Join(kept.dir, "plugins", kept.plugin); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want %q", kept.plugin, entry.InstallPath, want)
		}
		mustExist(t, entry.InstallPath)
	}
	if strings.Contains(stderr.String(), "merged") {
		t.Fatalf("warning = %q, want the second marketplace renamed, not merged away", stderr.String())
	}
}

// Two records naming the one directory source are that marketplace recorded
// twice, and the second folds into the record the first's rename made: one
// record, both their plugins keyed under it and each referenced where the
// source directory holds it.
func TestMarketplaceNameMigration_MergesTwoAliasesOfOneDirectorySource(t *testing.T) {
	const first, second = "a/./b", "a/b"
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	dir := makeDirectoryMarketplace(t, "acme", "widget")
	writePlugin(t, filepath.Join(dir, "plugins", "gadget"), "gadget", nil)
	source := Source{Kind: SourceDirectory, Path: dir}
	planted := time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)
	if err := m.saveMarketplaces(Marketplaces{
		first:  {Source: source, InstallLocation: dir, LastUpdated: planted},
		second: {Source: source, InstallLocation: dir, LastUpdated: planted},
	}); err != nil {
		t.Fatal(err)
	}
	entries := map[string][]InstallEntry{}
	for _, installed := range []struct{ recorded, plugin string }{{first, "widget"}, {second, "gadget"}} {
		entries[registryKey(installed.plugin, installed.recorded)] = []InstallEntry{{
			InstallPath: filepath.Join(dir, "plugins", installed.plugin),
			Version:     "1.0.0",
			Enabled:     true,
			Source:      Source{Kind: SourceGitHub, Repo: "o/" + installed.plugin},
		}}
	}
	if err := m.saveRegistry(Registry{Version: 2, Plugins: entries}); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if ref.Source != source {
		t.Fatalf("a-b's source = %+v, want the one both records named: %+v", ref.Source, source)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b and gadget@a-b alone", reg.Plugins)
	}
	for _, plugin := range []string{"widget", "gadget"} {
		entry := installedAt(t, reg, registryKey(plugin, "a-b"))
		if want := filepath.Join(dir, "plugins", plugin); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want it still referenced at %q", plugin, entry.InstallPath, want)
		}
		mustExist(t, entry.InstallPath)
	}
	if !strings.Contains(stderr.String(), "merged") || !strings.Contains(stderr.String(), `"a/b"`) {
		t.Fatalf("warning = %q, want it to say the duplicate %q was merged", stderr.String(), second)
	}
}

// A rename that cannot save puts its directories back, and the marker goes
// with them: the rename it named is undone, so there is nothing for the next
// lock holder to finish.
func TestMarketplaceNameMigration_AFailedSaveRemovesTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	widget := plantLegacyMarketplace(t, m, "a/b", "widget")
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}
	orig := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			return errors.New("boom")
		}
		return orig(path, data, perm)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceAtomicWriteFile = orig
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	mustNotExist(t, renameMarkerFile(m))
	for path, want := range before {
		if got := readStoreFile(t, path); got != want {
			t.Fatalf("%s changed after a failed save:\n%s", path, got)
		}
	}
	mustExist(t, widget)
	mustExist(t, filepath.Join(m.marketplaceDir("a/b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir("a-b"))
	mustNotExist(t, filepath.Join(m.cacheDir(), "a-b"))
}

// A move that fails and puts back everything it moved leaves the store at the
// old name, so there is nothing for a marker to resume: it is dropped and the
// failure is reported. A marker left here makes every later lock holder retry
// the same rename, and refuse the whole store once something else takes the
// destination.
func TestMarketplaceNameMigration_AFailedMoveThatRollsBackRemovesTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	widget := plantLegacyMarketplace(t, m, "a/b", "widget")
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if to == m.marketplaceDir("a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the clone move to fail")
	}
	mustNotExist(t, renameMarkerFile(m))
	mustExist(t, widget)
	mustExist(t, filepath.Join(m.marketplaceDir("a/b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir("a-b"))
	mustNotExist(t, filepath.Join(m.cacheDir(), "a-b"))
}

// The same holds when the plugin cache is what fails to move: the clone the
// move already made is put back, so the store is at the old name again and the
// marker goes with the failure.
func TestMarketplaceNameMigration_AFailedCacheMoveThatRollsBackRemovesTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	marketplaceRename = func(from, to string) error {
		if to == filepath.Join(m.cacheDir(), "a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the cache move to fail")
	}
	mustNotExist(t, renameMarkerFile(m))
	mustExist(t, filepath.Join(m.marketplaceDir("a/b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir("a-b"))
	mustExist(t, m.pluginCacheDir("a/b", "widget", "sha1"))
	mustNotExist(t, filepath.Join(m.cacheDir(), "a-b"))
}

// Two refused names can reach the one clone and the one cache through a
// symlink inside the store. They are one marketplace, so the second record
// has to merge into the first rename's record rather than migrate on its own:
// its directories are already gone with the first, and re-keying it to a cache
// path nobody created leaves its installs pointing at nothing.
func TestMarketplaceNameMigration_AliasesThroughASymlinkAreOneMarketplace(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	if err := os.Symlink(m.marketplaceDir("a"), m.marketplaceDir("alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(m.cacheDir(), "a"), filepath.Join(m.cacheDir(), "alias")); err != nil {
		t.Fatal(err)
	}
	plantLegacyMarketplace(t, m, "alias/b", "gadget")
	plantOneSource(t, m, "a/b", "alias/b")

	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore: %v", err)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want the one the alias merged into", mk)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) == 0 {
		t.Fatal("registry has no plugin entries")
	}
	// Every recorded install has to be somewhere: a re-key onto a cache
	// directory nobody created is a broken install.
	for key, entries := range reg.Plugins {
		for _, entry := range entries {
			if _, err := os.Stat(entry.InstallPath); err != nil {
				t.Fatalf("%s points at %s, which does not exist: %v", key, entry.InstallPath, err)
			}
		}
	}
}

// A refused name can derive a component no filesystem will take: longer than one
// name may be, or carrying a byte a name cannot. The migration still has to
// happen — falling back to "marketplace" the way an unrepresentable name does —
// rather than leaving the store failing every operation on the probe.
func TestMarketplaceNameMigration_ADerivedNameTheFilesystemCannotHoldFallsBack(t *testing.T) {
	cases := []struct {
		what string
		name string
	}{
		{"longer than one component", strings.Repeat("a", 300) + "/b"},
		{"a NUL byte", "a\x00/b"},
		{"a control character", "a\x01/b"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			// The store's own two directories, as a real one has them: the
			// probe's error differs when they are missing.
			for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.saveMarketplaces(Marketplaces{tc.name: {
				Source:      Source{Kind: SourceURL, URL: "https://example.invalid/x.git"},
				LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
			}}); err != nil {
				t.Fatal(err)
			}

			if err := m.migrateStore(context.Background()); err != nil {
				t.Fatalf("migrateStore: %v", err)
			}
			mk, err := m.loadMarketplaces()
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := mk["marketplace"]; !ok || len(mk) != 1 {
				t.Fatalf("marketplaces = %v, want marketplace alone", mk)
			}
			// Every later operation must work on the migrated store too.
			if _, err := m.ListMarketplaces(context.Background()); err != nil {
				t.Fatalf("ListMarketplaces: %v", err)
			}
		})
	}
}

// The numbered replacement a taken name appends has to fit as well: a derived
// base can be legal on its own and still overflow once "-2" is added, and the
// probe for that candidate fails the whole migration.
func TestMarketplaceNameMigration_ADerivedNameTooLongOnceNumberedFallsBack(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	base := strings.Repeat("a", 252) + "-b" // 254 bytes, so base+"-2" is 256
	if len(base) != 254 {
		t.Fatalf("base len = %d, want 254 (the case this pins)", len(base))
	}
	long := strings.Repeat("a", 252) + "/b" // derives exactly base
	if got := strings.Repeat("a", 252) + "-b"; got != base {
		t.Fatal("derivation changed; the fixture no longer derives the taken name")
	}
	if err := m.saveMarketplaces(Marketplaces{
		long: {Source: Source{Kind: SourceURL, URL: "https://example.invalid/x.git"},
			LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)},
		base: {Source: Source{Kind: SourceURL, URL: "https://example.invalid/y.git"},
			LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore: %v", err)
	}
	after, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after["marketplace"]; !ok {
		t.Fatalf("marketplaces = %v, want the refused name migrated to marketplace", after)
	}
	if _, ok := after[base]; !ok {
		t.Fatalf("marketplaces = %v, want the recorded %d-byte name untouched", after, len(base))
	}
}

// A legacy name can be long overall while every one of its components is legal,
// and then its directories really are in the store and have to move. Judging the
// whole name by its total length would strand them under a path nothing derives.
func TestMarketplaceNameMigration_ALongNameOfLegalComponentsMovesItsDirectories(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	name := strings.Repeat("c", 200) + "/" + strings.Repeat("d", 200)
	plantLegacyMarketplace(t, m, name, "widget")

	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore: %v", err)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := mk["marketplace"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want marketplace alone", mk)
	}
	if want := m.marketplaceDir("marketplace"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone moved to %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("marketplace"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir(name))
}

// The marker's other half: a move that fails and cannot put back what it moved
// leaves the store between the two names, so the marker stays and the error
// names it, for the next lock holder to finish the rename from.
func TestMarketplaceNameMigration_AnIncompleteMoveRollbackKeepsTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	orig := marketplaceRename
	t.Cleanup(func() { marketplaceRename = orig })
	// The clone moves, the cache move then fails, and the clone cannot be
	// renamed back: the move's own rollback fails, so the store is left
	// between the two names rather than at either.
	marketplaceRename = func(from, to string) error {
		if to == filepath.Join(m.cacheDir(), "a-b") || from == m.marketplaceDir("a-b") {
			return errors.New("boom")
		}
		return orig(from, to)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceRename = orig
	if err == nil {
		t.Fatal("expected the cache move to fail")
	}
	mustExist(t, renameMarkerFile(m))
	if !strings.Contains(err.Error(), renameMarkerFile(m)) {
		t.Fatalf("error = %v, want it to name %s", err, renameMarkerFile(m))
	}
}

// A recovery that cannot finish the rename but puts everything back — the
// registry restored, the directories undone — leaves the store at the old
// name, so the marker goes and the refused name migrates afresh on the next
// lock. A marker left here makes every later lock holder retry the same
// destination, and refuse the whole store once that destination is taken.
func TestMarketplaceNameMigration_ARecoveryThatRollsBackRemovesTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	plantRenameMarker(t, m, "a/b", "a-b")
	orig := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			return errors.New("boom")
		}
		return orig(path, data, perm)
	}

	if err := m.migrateStore(context.Background()); err == nil {
		t.Fatal("expected the recovery's marketplaces write to fail")
	}
	marketplaceAtomicWriteFile = orig
	mustNotExist(t, renameMarkerFile(m))

	// The store is at the old name and the marker is gone, so the next lock
	// holder migrates it afresh instead of retrying a dead destination.
	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore after the rolled-back recovery: %v", err)
	}
	mustNotExist(t, renameMarkerFile(m))
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mk["a-b"]; !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
}

// The other side of the same decision: the marker exists because an earlier run
// stopped mid-rename, so the directories may already be under the destination.
// A recovery then finds nothing under the old name, its undo is trivially empty,
// and dropping the marker would abandon a rename whose directories have really
// moved — the next run would number the record around an occupied destination
// and point its installs at a cache nothing created.
func TestMarketplaceNameMigration_ARecoveryOfAnAlreadyMovedRenameKeepsTheMarker(t *testing.T) {
	const recorded = "a/b"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, recorded, "widget")
	// The earlier run moved both directories before it stopped.
	if err := os.Rename(m.marketplaceDir(recorded), m.marketplaceDir("a-b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(m.cacheDir(), recorded), filepath.Join(m.cacheDir(), "a-b")); err != nil {
		t.Fatal(err)
	}
	plantRenameMarker(t, m, recorded, "a-b")
	orig := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = orig })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			return errors.New("boom")
		}
		return orig(path, data, perm)
	}

	if err := m.migrateStore(context.Background()); err == nil {
		t.Fatal("expected the recovery's marketplaces write to fail")
	}
	marketplaceAtomicWriteFile = orig
	mustExist(t, renameMarkerFile(m))

	// The marker is still the only record that a-b's directories belong to this
	// marketplace, so the next lock holder finishes the rename.
	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore after the kept marker: %v", err)
	}
	mustNotExist(t, renameMarkerFile(m))
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone at %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "a-b"))
	if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
		t.Fatalf("widget's InstallPath = %q, want %q", entry.InstallPath, want)
	}
	mustExist(t, entry.InstallPath)
}

// A save that fails and cannot put the registry back leaves the old record
// beside the new keys, which is the store between the two names and exactly
// what the marker is for: the rollback reached neither state, so the marker
// stays, the error says where it is, and the next lock holder finishes the
// rename from whatever the failure left.
func TestMarketplaceNameMigration_AFailedRestoreKeepsTheMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/b", "widget")
	origWrite, origSave := marketplaceAtomicWriteFile, installSaveRegistry
	t.Cleanup(func() { marketplaceAtomicWriteFile, installSaveRegistry = origWrite, origSave })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if filepath.Base(path) == marketplacesFileName {
			return errors.New("boom")
		}
		return origWrite(path, data, perm)
	}
	// The registry is saved twice: once for the rename, once to put it back.
	// Only the second fails, so what the failure leaves on disk keys this
	// marketplace's plugins under the new name.
	saves := 0
	installSaveRegistry = func(path string, reg Registry) error {
		saves++
		if saves > 1 {
			return errors.New("boom")
		}
		return origSave(path, reg)
	}

	_, err := m.ListMarketplaces(context.Background())
	marketplaceAtomicWriteFile, installSaveRegistry = origWrite, origSave
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	mustExist(t, renameMarkerFile(m))
	if !strings.Contains(err.Error(), renameMarkerFile(m)) {
		t.Fatalf("error = %v, want it to name %s", err, renameMarkerFile(m))
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces once the store can be written: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone the rename moved to %q", ref.InstallLocation, want)
	}
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, renameMarkerFile(m))
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "a-b"))
	if want := m.pluginCacheDir("a-b", "widget", "sha1"); entry.InstallPath != want {
		t.Fatalf("widget's InstallPath = %q, want %q", entry.InstallPath, want)
	}
	mustExist(t, entry.InstallPath)
}

// A never-fetched duplicate has no clone and no cache of its own for an
// alias's rename to have moved, so finding the derived directories gone says
// nothing about it. What it does have is a source of its own, which a merge
// would drop, so it keeps its record under a numbered name and the next fetch
// clones it there.
func TestMarketplaceNameMigration_KeepsANeverFetchedDuplicatesRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	mk["a/b"] = MarketplaceRef{
		Source:      Source{Kind: SourceURL, URL: "https://example.invalid/never-fetched.git"},
		LastUpdated: time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}

	mk, err = m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2", mk)
	}
	if got := mk["a-b"].Source.URL; got != "https://example.invalid/widget.git" {
		t.Fatalf("a-b's source = %q, want the marketplace whose directories moved there", got)
	}
	ref, ok := mk["a-b-2"]
	if !ok {
		t.Fatalf("marketplaces = %v, want the never-fetched duplicate renamed to a-b-2", mk)
	}
	if got := ref.Source.URL; got != "https://example.invalid/never-fetched.git" {
		t.Fatalf("a-b-2's source = %q, want the duplicate's own", got)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("a-b-2's InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	if strings.Contains(stderr.String(), "merged") {
		t.Fatalf("warning = %q, want the duplicate renamed, not merged away", stderr.String())
	}
}

// Every rename writes its marker before it changes anything, the one with no
// directories of its own to move included: it still records itself in two
// files, a run can stop between them, and only a marker tells the next one
// that the keys already under the new name are this rename's rather than a
// removed marketplace's residue.
func TestMarketplaceNameMigration_MarksTheRenameThatMovesNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a", "b")
	plantLegacyMarketplace(t, m, "a/b", "widget")
	var written []string
	origWrite, origSave := marketplaceAtomicWriteFile, installSaveRegistry
	t.Cleanup(func() { marketplaceAtomicWriteFile, installSaveRegistry = origWrite, origSave })
	marketplaceAtomicWriteFile = func(path string, data []byte, perm os.FileMode) error {
		written = append(written, filepath.Base(path))
		return origWrite(path, data, perm)
	}
	installSaveRegistry = func(path string, reg Registry) error {
		written = append(written, filepath.Base(path))
		return origSave(path, reg)
	}

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	marketplaceAtomicWriteFile, installSaveRegistry = origWrite, origSave
	want := strings.Join([]string{renameMarkerFileName, registryFileName, marketplacesFileName}, " ")
	if got := strings.Join(written, " "); got != want {
		t.Fatalf("store writes = %q, want %q", got, want)
	}
}

// A rename whose directories another marketplace owns re-keys the registry
// and records the new name, two writes a run can stop between. The marker
// names it, so the next run finishes that rename — the keys stay where the
// first write put them, the record follows, and the marketplace that owns the
// directories keeps every one of them — instead of reading those keys as
// residue and taking a numbered name that orphans them.
func TestMarketplaceNameMigration_CompletesARenameUnderAnotherMarketplacesDirs(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	pluginB := plantLegacyMarketplace(t, m, "a", "b")
	plantLegacyMarketplace(t, m, "a/b", "widget")
	plantRenameMarker(t, m, "a/b", "a-b")
	// Where the run stopped: widget's own plugin cache moved out of a's and
	// the registry re-keyed onto it, the marketplaces file not written. None
	// of a's own directories moved, because they are a's.
	widget := m.pluginCacheDir("a-b", "widget", "sha1")
	if err := os.MkdirAll(filepath.Join(m.cacheDir(), "a-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(m.cacheDir(), "a", "b", "widget"), filepath.Join(m.cacheDir(), "a-b", "widget")); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry := installedAt(t, reg, registryKey("widget", "a/b"))
	entry.InstallPath = widget
	delete(reg.Plugins, registryKey("widget", "a/b"))
	reg.Plugins[registryKey("widget", "a-b")] = []InstallEntry{entry}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	mustExist(t, pluginB)
	mustExist(t, widget)
	mustExist(t, filepath.Join(m.marketplaceDir("a/b"), ".claude-plugin", "marketplace.json"))
	mustNotExist(t, m.marketplaceDir("a-b"))
	mustNotExist(t, renameMarkerFile(m))
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a and a-b", mk)
	}
	ref, ok := mk["a-b"]
	if !ok {
		t.Fatalf("marketplaces = %v, want the rename the marker names finished", mk)
	}
	if ref.InstallLocation != "" {
		t.Fatalf("a-b's InstallLocation = %q, want the entry left unfetched", ref.InstallLocation)
	}
	if want := m.marketplaceDir("a"); mk["a"].InstallLocation != want {
		t.Fatalf("a's InstallLocation = %q, want %q", mk["a"].InstallLocation, want)
	}

	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b and b@a", reg.Plugins)
	}
	if got := installedAt(t, reg, registryKey("widget", "a-b")).InstallPath; got != widget {
		t.Fatalf("widget's InstallPath = %q, want it where the crash left it at %q", got, widget)
	}
	if got := installedAt(t, reg, registryKey("b", "a")).InstallPath; got != pluginB {
		t.Fatalf("b's InstallPath = %q, want it untouched at %q", got, pluginB)
	}
}

// The plugin caches a blocked entry takes with it ride the marker like the
// directories a rename moves: a run that moved them and stopped before the
// registry write leaves the marker naming the rename, and the next run finds
// each directory already under the new name and records the paths the stopped
// run never wrote.
func TestMarketplaceNameMigration_CompletesThePluginCacheMovesItsMarkerNames(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	pluginP := plantLegacyMarketplace(t, m, "a", "p")
	plantLegacyMarketplace(t, m, "a/b", "widget")
	// Where the run stopped: widget's cache moved under the new name, and
	// neither store file written, so its key and its path are the old ones.
	if err := os.MkdirAll(filepath.Join(m.cacheDir(), "a-b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(m.cacheDir(), "a", "b", "widget"), filepath.Join(m.cacheDir(), "a-b", "widget")); err != nil {
		t.Fatal(err)
	}
	plantRenameMarker(t, m, "a/b", "a-b")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"p@a installed at " + pluginP,
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
	}
	if keyed := keyedInstalls(reg); !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	mustNotExist(t, filepath.Join(m.cacheDir(), "a", "b", "widget"))
	mustNotExist(t, renameMarkerFile(m))
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a and a-b", mk)
	}
	if _, ok := mk["a-b"]; !ok {
		t.Fatalf("marketplaces = %v, want the rename the marker names finished", mk)
	}
}

// A rename that cannot save puts back every plugin cache it lifted out of the
// owner's, and the numbered name a retry would take is not spent on the
// directory it created for them: the store is as it was found, and the next
// lock holder migrates it whole.
func TestMarketplaceNameMigration_AFailedSavePutsThePluginCachesBack(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	pluginP := plantLegacyMarketplace(t, m, "a", "p")
	widget := plantLegacyMarketplace(t, m, "a/b", "widget")
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}
	orig := installSaveRegistry
	installSaveRegistry = func(string, Registry) error { return errors.New("boom") }
	t.Cleanup(func() { installSaveRegistry = orig })

	_, err := m.ListMarketplaces(context.Background())
	installSaveRegistry = orig
	if err == nil {
		t.Fatal("expected the save to fail")
	}
	if !strings.Contains(err.Error(), `"a/b"`) {
		t.Fatalf("error = %v, want it to name the entry", err)
	}
	for path, want := range before {
		if got := readStoreFile(t, path); got != want {
			t.Fatalf("%s changed after a failed save:\n%s", path, got)
		}
	}
	mustExist(t, widget)
	mustExist(t, pluginP)
	mustNotExist(t, filepath.Join(m.cacheDir(), "a-b"))
	mustNotExist(t, renameMarkerFile(m))

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces once the store can be written: %v", err)
	}
	if _, ok := mk["a-b"]; !ok || len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a and a-b", mk)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := installedAt(t, reg, registryKey("widget", "a-b")).InstallPath, m.pluginCacheDir("a-b", "widget", "sha1"); got != want {
		t.Fatalf("widget's InstallPath = %q, want %q", got, want)
	}
}

// Between the crash and the next lock holder a user can record a marketplace
// under the name the unfinished rename was taking. Finishing onto it would
// drop that record and its source, and what sits under the name by then is a
// mix of what the rename moved there and what the user put there, which
// nothing left in the store tells apart. So the acquisition fails instead,
// naming both names and the marker the user can delete to abandon the rename.
func TestMarketplaceNameMigration_RefusesAMarkerWhoseDestinationIsTaken(t *testing.T) {
	const recorded = "a/b"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, recorded, "widget")
	plantRenameMarker(t, m, recorded, "a-b")
	// Where the run stopped: the clone and the cache moved, neither store
	// file written.
	if err := os.Rename(m.marketplaceDir(recorded), m.marketplaceDir("a-b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(m.cacheDir(), recorded), filepath.Join(m.cacheDir(), "a-b")); err != nil {
		t.Fatal(err)
	}
	// What the user added while the store was in that state.
	dir := makeDirectoryMarketplace(t, "acme", "gadget")
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	mk["a-b"] = MarketplaceRef{
		Source:          Source{Kind: SourceDirectory, Path: dir},
		InstallLocation: dir,
		LastUpdated:     time.Date(2031, 4, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}

	err = m.migrateStore(context.Background())
	for path, want := range before {
		if got := readStoreFile(t, path); got != want {
			t.Fatalf("%s changed:\n%s\nwant it as the crash and the user left it:\n%s", path, got, want)
		}
	}
	if err == nil {
		t.Fatal("expected the acquisition to fail on the taken name")
	}
	for _, want := range []string{`"` + recorded + `"`, `"a-b"`, renameMarkerFile(m)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %s", err, want)
		}
	}
	mustExist(t, renameMarkerFile(m))
	mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
	mustExist(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"))
	mustExist(t, m.pluginCacheDir("a-b", "widget", "sha1"))
}

// A marker that cannot be removed names a rename the store already records:
// the rename is made, so there is nothing left to finish from it and nothing
// it can cost a later operation. The run says what it could not remove and
// carries on, rather than failing every store acquisition until the user
// deletes the file by hand.
func TestMarketplaceNameMigration_WarnsAboutAMarkerItCannotRemove(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "a/b", "widget")
	orig := marketplaceRemoveAll
	t.Cleanup(func() { marketplaceRemoveAll = orig })
	marketplaceRemoveAll = func(path string) error {
		if filepath.Base(path) == renameMarkerFileName {
			return errors.New("boom")
		}
		return orig(path)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone", mk)
	}
	if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone the rename moved to %q", ref.InstallLocation, want)
	}
	if got := strings.Count(stderr.String(), renameMarkerFile(m)); got != 1 {
		t.Fatalf("stderr = %q, want one warning naming %s", stderr.String(), renameMarkerFile(m))
	}
	mustExist(t, renameMarkerFile(m))

	// The marker left behind names a rename the marketplaces file records, so
	// the next lock holder finds nothing to finish and drops it.
	marketplaceRemoveAll = orig
	recorded := readStoreFile(t, m.marketplacesFile())
	if err := m.migrateStore(context.Background()); err != nil {
		t.Fatalf("migrateStore: %v", err)
	}
	mustNotExist(t, renameMarkerFile(m))
	if got := readStoreFile(t, m.marketplacesFile()); got != recorded {
		t.Fatalf("%s changed on the second run:\n%s", marketplacesFileName, got)
	}
}

// A marker names the marketplace a rename renames and the name it takes, and
// the migration derives that second name, so it is one the store accepts. A
// marker naming neither, or naming a destination the store refuses, is no
// rename this store left in flight — a hand edit, or some other writer's file
// under the marker's name — and finishing it would move a clone onto whatever
// path that name derives, outside the marketplaces directory for a traversing
// one. It is dropped with a line saying what was wrong, and the entry
// migrates as it would have without it.
func TestMarketplaceNameMigration_DropsAMarkerThatNamesNoRename(t *testing.T) {
	markers := []struct {
		what string
		body string
		says string
	}{
		{"names nothing", `{}`, `names "" and ""`},
		{"names no destination", `{"from": "a/b", "to": ""}`, `names "a/b" and ""`},
		{"names a destination the store refuses", `{"from": "a/b", "to": "../escaped"}`, `"../escaped"`},
	}
	for _, marker := range markers {
		t.Run(marker.what, func(t *testing.T) {
			m := NewManager(t.TempDir())
			var stderr bytes.Buffer
			m.Stderr = &stderr
			plantLegacyMarketplace(t, m, "a/b", "widget")
			if err := os.WriteFile(renameMarkerFile(m), []byte(marker.body+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			mk, err := m.ListMarketplaces(context.Background())
			if err != nil {
				t.Fatalf("ListMarketplaces: %v", err)
			}
			ref, ok := mk["a-b"]
			if !ok || len(mk) != 1 {
				t.Fatalf("marketplaces = %v, want a-b alone", mk)
			}
			if want := m.marketplaceDir("a-b"); ref.InstallLocation != want {
				t.Fatalf("InstallLocation = %q, want the clone the migration moved to %q", ref.InstallLocation, want)
			}
			mustExist(t, filepath.Join(m.marketplaceDir("a-b"), ".claude-plugin", "marketplace.json"))
			mustExist(t, m.pluginCacheDir("a-b", "widget", "sha1"))
			mustNotExist(t, filepath.Join(m.Root, "escaped"))
			mustNotExist(t, renameMarkerFile(m))
			if !strings.Contains(stderr.String(), marker.says) {
				t.Fatalf("stderr = %q, want a line saying the marker %s", stderr.String(), marker.says)
			}
		})
	}
}

// Only a marker names a rename. What else sits under the derived name is
// residue a removed marketplace left — its clone, its plugin cache and its
// registry keys all outlive it (refuseLeftoversUnder) — and it is the same
// picture an interrupted rename leaves, so an entry whose own directories a
// hand-cleared store took away steps over it onto the numbered name rather
// than claiming another marketplace's working tree and installs as its own.
func TestMarketplaceNameMigration_LeavesResidueThatNoMarkerNames(t *testing.T) {
	const recorded = "a/b"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, recorded, "widget")
	if err := os.RemoveAll(m.marketplaceDir(recorded)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(m.cacheDir(), recorded)); err != nil {
		t.Fatal(err)
	}
	residue := plantCatalog(t, m.marketplaceDir("a-b"))
	if err := os.WriteFile(filepath.Join(residue, "keepme"), []byte("a removed marketplace's"), 0o644); err != nil {
		t.Fatal(err)
	}
	ghostPath := m.pluginCacheDir("a-b", "ghost", "sha9")
	writePlugin(t, ghostPath, "ghost", nil)
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ghost := InstallEntry{
		InstallPath: ghostPath,
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/ghost"},
	}
	reg.Plugins[registryKey("ghost", "a-b")] = []InstallEntry{ghost}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b-2"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b-2 alone", mk)
	}
	if ref.InstallLocation == residue {
		t.Fatalf("InstallLocation = %q, want the residue's clone left to the marketplace that made it", ref.InstallLocation)
	}
	mustExist(t, filepath.Join(residue, "keepme"))
	mustExist(t, filepath.Join(residue, ".claude-plugin", "marketplace.json"))

	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want ghost@a-b and widget@a-b-2", reg.Plugins)
	}
	if got := installedAt(t, reg, registryKey("ghost", "a-b")); got != ghost {
		t.Fatalf("ghost@a-b = %+v, want it as planted: %+v", got, ghost)
	}
	mustExist(t, ghostPath)
	if _, ok := reg.Plugins[registryKey("widget", "a-b-2")]; !ok {
		t.Fatalf("registry keys = %v, want the entry's own plugin under a-b-2", reg.Plugins)
	}
}

// A directory-sourced marketplace keeps no clone and no plugin cache of its
// own, so the missing directories that say "an interrupted rename moved them"
// for a git-backed entry say nothing here. Only a marker names a rename, so a
// stale key under the derived name is a removed marketplace's and the entry
// takes the numbered name, still referenced in place.
func TestMarketplaceNameMigration_ADirectorySourceClaimsNoResidue(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyDirectoryMarketplace(t, m, "a/b", "widget", Source{Kind: SourceGitHub, Repo: "o/widget"})
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	source := mk["a/b"].InstallLocation
	ghostPath := m.pluginCacheDir("a-b", "ghost", "sha9")
	writePlugin(t, ghostPath, "ghost", nil)
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	ghost := InstallEntry{
		InstallPath: ghostPath,
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/ghost"},
	}
	reg.Plugins[registryKey("ghost", "a-b")] = []InstallEntry{ghost}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}

	mk, err = m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b-2"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b-2 alone", mk)
	}
	if ref.InstallLocation != source {
		t.Fatalf("InstallLocation = %q, want the directory source's own path %q", ref.InstallLocation, source)
	}

	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want ghost@a-b and widget@a-b-2", reg.Plugins)
	}
	if got := installedAt(t, reg, registryKey("ghost", "a-b")); got != ghost {
		t.Fatalf("ghost@a-b = %+v, want it as planted: %+v", got, ghost)
	}
	if _, ok := reg.Plugins[registryKey("widget", "a-b-2")]; !ok {
		t.Fatalf("registry keys = %v, want the entry's own plugin under a-b-2", reg.Plugins)
	}
}

// The merge is two file writes too, and a run can stop between them: the
// registry write moves the duplicate's keys under the record it folds into,
// and a later run — a new run, which has migrated nothing yet — reads those
// keys as a removed marketplace's residue and gives the duplicate a numbered
// name of its own. So the merge marks itself before that write, saying it is
// a merge and which record it folds into, and drops the marker once the
// marketplaces file records the removal.
func TestMarketplaceNameMigration_MarksTheMergeItMakes(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	plantLegacyMarketplace(t, m, "a/b", "gadget")
	plantOneSource(t, m, "a/./b", "a/b")
	var marked []renameMarker
	orig := installSaveRegistry
	t.Cleanup(func() { installSaveRegistry = orig })
	installSaveRegistry = func(path string, reg Registry) error {
		marker, err := m.loadRenameMarker()
		if err != nil {
			return err
		}
		if marker == nil {
			marker = &renameMarker{}
		}
		marked = append(marked, *marker)
		return orig(path, reg)
	}

	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	installSaveRegistry = orig
	want := []renameMarker{{From: "a/./b", To: "a-b"}, {From: "a/b", To: "a-b", Merge: true}}
	if !slices.Equal(marked, want) {
		t.Fatalf("marker on disk at each registry save = %+v, want %+v", marked, want)
	}
	mustNotExist(t, renameMarkerFile(m))
}

// A merge stopped after its registry write leaves the duplicate's keys under
// the record it folds into and both records in the marketplaces file. The
// marker names that merge, so the next run finishes it — the duplicate's
// record goes, its keys stay where the write put them — rather than reading
// those keys as residue and giving the duplicate a numbered name that orphans
// them.
func TestMarketplaceNameMigration_CompletesAMergeItsMarkerNames(t *testing.T) {
	const duplicate = "a/b"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	plantLegacyMarketplace(t, m, duplicate, "gadget")
	// Where the run stopped: the longer alias renamed to a-b, taking the one
	// clone and the one cache both records derive, and the merge's registry
	// write made.
	if err := os.Rename(m.marketplaceDir(duplicate), m.marketplaceDir("a-b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(m.cacheDir(), duplicate), filepath.Join(m.cacheDir(), "a-b")); err != nil {
		t.Fatal(err)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	ref := mk["a/./b"]
	ref.InstallLocation = m.marketplaceDir("a-b")
	delete(mk, "a/./b")
	mk["a-b"] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, moved := range []struct{ recorded, plugin string }{{"a/./b", "widget"}, {duplicate, "gadget"}} {
		entry := installedAt(t, reg, registryKey(moved.plugin, moved.recorded))
		entry.InstallPath = m.pluginCacheDir("a-b", moved.plugin, "sha1")
		delete(reg.Plugins, registryKey(moved.plugin, moved.recorded))
		reg.Plugins[registryKey(moved.plugin, "a-b")] = []InstallEntry{entry}
	}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}
	plantMergeMarker(t, m, duplicate, "a-b")

	mk, err = m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	got, ok := mk["a-b"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b alone, the duplicate merged away", mk)
	}
	if want := m.marketplaceDir("a-b"); got.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone at %q", got.InstallLocation, want)
	}
	mustNotExist(t, renameMarkerFile(m))
	reg, err = m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b and gadget@a-b alone", reg.Plugins)
	}
	for _, plugin := range []string{"widget", "gadget"} {
		entry := installedAt(t, reg, registryKey(plugin, "a-b"))
		if want := m.pluginCacheDir("a-b", plugin, "sha1"); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want %q", plugin, entry.InstallPath, want)
		}
		mustExist(t, entry.InstallPath)
	}

	mkBefore, regBefore := readStoreFile(t, m.marketplacesFile()), readStoreFile(t, m.registryPath())
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatalf("second ListMarketplaces: %v", err)
	}
	if got := readStoreFile(t, m.marketplacesFile()); got != mkBefore {
		t.Fatalf("%s changed on the second run:\n%s", marketplacesFileName, got)
	}
	if got := readStoreFile(t, m.registryPath()); got != regBefore {
		t.Fatalf("%s changed on the second run:\n%s", registryFileName, got)
	}
}

// A merge folds a duplicate into a record that stands until the one save that
// drops the duplicate, so a marker naming a destination nothing records names
// no merge this store can finish: the record it would fold into is one it
// would have to invent, and inventing it writes a marketplace with no source
// that every later read of the store fails on. The acquisition fails naming
// both names and the marker, and leaves the store as it found it.
func TestMarketplaceNameMigration_RefusesAMergeMarkerWithNoDestination(t *testing.T) {
	const duplicate = "a/b"
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, duplicate, "widget")
	plantMergeMarker(t, m, duplicate, "a-b")
	before := map[string]string{
		m.marketplacesFile(): readStoreFile(t, m.marketplacesFile()),
		m.registryPath():     readStoreFile(t, m.registryPath()),
	}

	err := m.migrateStore(context.Background())
	for path, want := range before {
		if got := readStoreFile(t, path); got != want {
			t.Fatalf("%s changed:\n%s\nwant it as the marker was found beside it:\n%s", path, got, want)
		}
	}
	if err == nil {
		t.Fatal("expected the acquisition to fail on the destination nothing records")
	}
	for _, want := range []string{`"` + duplicate + `"`, `"a-b"`, renameMarkerFile(m)} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to name %s", err, want)
		}
	}
	mustExist(t, renameMarkerFile(m))
}

// Residue under the derived name pushes the first of two aliases onto a
// numbered name, and the second has to follow it there: they are one
// marketplace, so the clone and the cache the first rename moved to a-b-2 are
// the second's too, and merging into the derived name instead would key its
// plugins under a marketplace nothing recorded and leave their install paths
// pointing where the files no longer are. The residue is nobody's and stays
// where it is.
func TestMarketplaceNameMigration_MergesAnAliasIntoTheNameItsRenameTook(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	leftover := plantCatalog(t, m.marketplaceDir("a-b"))
	if err := os.WriteFile(filepath.Join(leftover, "keepme"), []byte("a removed marketplace's"), 0o644); err != nil {
		t.Fatal(err)
	}
	plantLegacyMarketplace(t, m, "a/./b", "widget")
	plantLegacyMarketplace(t, m, "a/b", "gadget")
	plantOneSource(t, m, "a/./b", "a/b")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	ref, ok := mk["a-b-2"]
	if !ok || len(mk) != 1 {
		t.Fatalf("marketplaces = %v, want a-b-2 alone, both aliases in the one record", mk)
	}
	if want := m.marketplaceDir("a-b-2"); ref.InstallLocation != want {
		t.Fatalf("InstallLocation = %q, want the clone the rename moved to %q", ref.InstallLocation, want)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Plugins) != 2 {
		t.Fatalf("registry keys = %v, want widget@a-b-2 and gadget@a-b-2 alone", reg.Plugins)
	}
	for _, plugin := range []string{"widget", "gadget"} {
		entry := installedAt(t, reg, registryKey(plugin, "a-b-2"))
		if want := m.pluginCacheDir("a-b-2", plugin, "sha1"); entry.InstallPath != want {
			t.Fatalf("%s's InstallPath = %q, want %q", plugin, entry.InstallPath, want)
		}
		mustExist(t, entry.InstallPath)
	}
	mustExist(t, filepath.Join(leftover, "keepme"))
	mustExist(t, filepath.Join(leftover, ".claude-plugin", "marketplace.json"))
	if !strings.Contains(stderr.String(), "merged") || !strings.Contains(stderr.String(), `"a-b-2"`) {
		t.Fatalf("warning = %q, want it to say the duplicate was merged into %q", stderr.String(), "a-b-2")
	}
}

// A refused name can derive the same new name from directories of its own and
// migrate in the middle of an alias family: `a\b/` clones under
// <marketplaces>/a\b, as deep as the <marketplaces>/a@b that "./a@b", "a@b/"
// and "a@b" are the one marketplace under, and its name sorts between the
// last two. It is a marketplace of its own and takes a numbered name, and the
// alias behind it still belongs to the family's record — this run holds each
// rename against the directories that rename moved, so no two marketplaces
// share a slot.
func TestMarketplaceNameMigration_MergesAnAliasPastASameBaseStranger(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "./a@b", "widget")
	plantLegacyMarketplace(t, m, "a@b/", "gadget")
	plantLegacyMarketplace(t, m, `a\b/`, "other")
	plantLegacyMarketplace(t, m, "a@b", "zed")
	plantOneSource(t, m, "./a@b", "a@b/", "a@b")

	mk, err := m.ListMarketplaces(context.Background())
	if err != nil {
		t.Fatalf("ListMarketplaces: %v", err)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	// Every plugin under the record whose cache holds its files: the family's
	// three under the name its first rename took, the stranger's under its
	// own. A record of its own for the last alias would key its plugin under
	// a name nothing ever created a cache for.
	var keyed []string
	for key, entries := range reg.Plugins {
		for _, entry := range entries {
			line := key + " installed at " + entry.InstallPath
			if _, err := os.Stat(entry.InstallPath); err != nil {
				line += ", which is not there"
			}
			keyed = append(keyed, line)
		}
	}
	slices.Sort(keyed)
	want := []string{
		"gadget@a-b installed at " + m.pluginCacheDir("a-b", "gadget", "sha1"),
		"other@a-b-2 installed at " + m.pluginCacheDir("a-b-2", "other", "sha1"),
		"widget@a-b installed at " + m.pluginCacheDir("a-b", "widget", "sha1"),
		"zed@a-b installed at " + m.pluginCacheDir("a-b", "zed", "sha1"),
	}
	if !slices.Equal(keyed, want) {
		t.Fatalf("registry:\n%s\nwant:\n%s", strings.Join(keyed, "\n"), strings.Join(want, "\n"))
	}
	if len(mk) != 2 {
		t.Fatalf("marketplaces = %v, want a-b and a-b-2 alone", mk)
	}
	if got := mk["a-b"].Source.URL; got != "https://example.invalid/widget.git" {
		t.Fatalf("a-b's source = %q, want the family whose directories moved there", got)
	}
	if got := mk["a-b-2"].Source.URL; got != "https://example.invalid/other.git" {
		t.Fatalf("a-b-2's source = %q, want the marketplace deriving its own directories", got)
	}
}
