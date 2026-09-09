package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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

	mk, err := m.ListMarketplaces()
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

	mk, err := m.ListMarketplaces()
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

			mk, err := m.ListMarketplaces()
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

	mk, err := m.ListMarketplaces()
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

			mk, err := m.ListMarketplaces()
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

	mk, err := m.ListMarketplaces()
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

// Once every recorded name is valid there is nothing to migrate, and a store
// that needs nothing is not written: neither the lockless listing nor a lock
// holder rewrites either file.
func TestMarketplaceNameMigration_ASecondLoadChangesNothing(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	if _, err := m.ListMarketplaces(); err != nil {
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

	if _, err := m.ListMarketplaces(); err != nil {
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

			_, err := m.ListMarketplaces()
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

			mk, err := m.ListMarketplaces()
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
			mk, err := m.ListMarketplaces()
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

// The user learns the new name from one line per rename, in the order the
// renames ran, and hears nothing once there is nothing left to rename.
func TestMarketplaceNameMigration_ReportsEachRename(t *testing.T) {
	m := NewManager(t.TempDir())
	var stderr bytes.Buffer
	m.Stderr = &stderr
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
	plantLegacyMarketplace(t, m, asideCloneName, "gadget")

	if _, err := m.ListMarketplaces(); err != nil {
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
	if _, err := m.ListMarketplaces(); err != nil {
		t.Fatalf("second ListMarketplaces: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a migrated store still warned: %q", stderr.String())
	}
}
