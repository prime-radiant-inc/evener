package plugins

import (
	"context"
	"io"
	"testing"
)

// legacyMigrationPending seeds a manager whose marketplaces file still
// records one refused legacy name ("foo@bar") owning a plugin
// (plantLegacyMarketplace's own seed), left un-migrated because seeding
// writes the file directly rather than taking the store lock. The next
// lockStore acquisition - which every mutation below takes for its own
// reason - runs the migration as a side effect: a rename that changes
// marketplaces.json and, because the legacy name owns a plugin, re-keys the
// registry too.
//
// Each test below calls a mutation targeting a DIFFERENT, unrelated
// marketplace or plugin ("baz"/"widget2"), planted the same direct way so it
// is not itself migrated, then asserts the mutation's own returned
// StoreChanges reports BOTH the marketplace rename and the plugin re-key -
// not just whichever store the mutation's own operation happens to touch.
// Before this round every one of these mutations acquired the store lock via
// `release, _, err := m.lockStore(...)`, discarding exactly this signal
// (#1699): a migration landing during the mutation's own lock acquisition
// broadcast only the mutation's own channel, leaving the other listing
// stale.
func legacyMigrationPending(t *testing.T, m *Manager) {
	t.Helper()
	plantLegacyMarketplace(t, m, "foo@bar", "widget")
}

// plantDirectoryMarketplace records a directory-source marketplace directly
// (no store lock), the way legacyMigrationPending's own legacy entry is
// planted, so the migration pending on it is untouched by this call.
func plantDirectoryMarketplace(t *testing.T, m *Manager, name string) {
	t.Helper()
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	dir := writeTestMarketplaceDir(t, name)
	mk[name] = MarketplaceRef{Source: Source{Kind: SourceDirectory, Path: dir}, InstallLocation: dir}
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
}

// plantInstalledPlugin records a registry entry directly (no store lock),
// for tests that need an installed plugin unrelated to legacyMigrationPending's
// own legacy entry.
func plantInstalledPlugin(t *testing.T, m *Manager, plugin, marketplace string) {
	t.Helper()
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reg.Plugins[registryKey(plugin, marketplace)] = []InstallEntry{{
		InstallPath: m.pluginCacheDir(marketplace, plugin, "sha1"),
		Version:     "1.0.0",
		Enabled:     true,
		Source:      Source{Kind: SourceGitHub, Repo: "o/" + plugin},
	}}
	if err := m.saveRegistry(reg); err != nil {
		t.Fatal(err)
	}
}

// TestWriterReportsAMigrationThatLandsDuringItsOwnLockAcquisition is one body
// (legacyMigrationPending, an optional extra plant, the writer's own call,
// then both StoreChanges fields) run per writer: AddMarketplace, RemoveMarketplace,
// RefreshMarketplace, EditMarketplace, plugin Remove, SetEnabled and Gc.
func TestWriterReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, m *Manager)
		call  func(t *testing.T, m *Manager) (StoreChanges, error)
		why   string
	}{
		{
			name: "AddMarketplace",
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				dir := writeTestMarketplaceDir(t, "baz")
				_, changes, err := m.AddMarketplace(context.Background(), "baz", Source{Kind: SourceDirectory, Path: dir})
				return changes, err
			},
			why: "AddMarketplace's own write and the migration's rename both touch Marketplaces; the migration's re-key touches Plugins independent of AddMarketplace's own operation",
		},
		{
			name:  "RemoveMarketplace",
			plant: func(t *testing.T, m *Manager) { plantDirectoryMarketplace(t, m, "baz") },
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				return m.RemoveMarketplace(context.Background(), "baz")
			},
			why: "the migration re-keyed the legacy name's plugin, independent of RemoveMarketplace's own target",
		},
		{
			name:  "RefreshMarketplace",
			plant: func(t *testing.T, m *Manager) { plantDirectoryMarketplace(t, m, "baz") },
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				return m.RefreshMarketplace(context.Background(), "baz")
			},
			why: "the migration re-keyed the legacy name's plugin, independent of RefreshMarketplace's own target",
		},
		{
			name:  "EditMarketplace",
			plant: func(t *testing.T, m *Manager) { plantDirectoryMarketplace(t, m, "baz") },
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				newDir := writeTestMarketplaceDir(t, "baz2")
				_, changes, err := m.EditMarketplace(context.Background(), "baz", "", &Source{Kind: SourceDirectory, Path: newDir})
				return changes, err
			},
			why: "the migration re-keyed the legacy name's plugin, independent of EditMarketplace's own target",
		},
		{
			name:  "Remove",
			plant: func(t *testing.T, m *Manager) { plantInstalledPlugin(t, m, "widget2", "baz") },
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				return m.Remove(context.Background(), "widget2", "baz")
			},
			why: "the migration renamed the legacy marketplace, independent of Remove's own target",
		},
		{
			name:  "SetEnabled",
			plant: func(t *testing.T, m *Manager) { plantInstalledPlugin(t, m, "widget2", "baz") },
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				return m.SetEnabled(context.Background(), "widget2", "baz", false)
			},
			why: "the migration renamed the legacy marketplace, independent of SetEnabled's own target - mutateEntry is also SetAutoUpgrade's mechanism",
		},
		{
			name: "Gc",
			call: func(t *testing.T, m *Manager) (StoreChanges, error) {
				_, changes, err := m.Gc(context.Background())
				return changes, err
			},
			why: "Gc's own sweep touches neither store file, so this is entirely the migration's own signal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			m.Stderr = io.Discard
			legacyMigrationPending(t, m)
			if tc.plant != nil {
				tc.plant(t, m)
			}

			changes, err := tc.call(t, m)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if !changes.Marketplaces {
				t.Fatalf("changes = %+v, want Marketplaces true: %s", changes, tc.why)
			}
			if !changes.Plugins {
				t.Fatalf("changes = %+v, want Plugins true: %s", changes, tc.why)
			}
		})
	}
}
