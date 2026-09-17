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

func TestAddMarketplaceReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)

	dir := writeTestMarketplaceDir(t, "baz")
	_, changes, err := m.AddMarketplace(context.Background(), "baz", Source{Kind: SourceDirectory, Path: dir})
	if err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true: AddMarketplace's own write and the migration's rename both touch it", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true: the migration re-keyed the legacy name's plugin, independent of AddMarketplace's own operation", changes)
	}
}

func TestRemoveMarketplaceReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)
	plantDirectoryMarketplace(t, m, "baz")

	changes, err := m.RemoveMarketplace(context.Background(), "baz")
	if err != nil {
		t.Fatalf("RemoveMarketplace: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true: the migration re-keyed the legacy name's plugin, independent of RemoveMarketplace's own target", changes)
	}
}

func TestRefreshMarketplaceReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)
	plantDirectoryMarketplace(t, m, "baz")

	changes, err := m.RefreshMarketplace(context.Background(), "baz")
	if err != nil {
		t.Fatalf("RefreshMarketplace: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true: the migration re-keyed the legacy name's plugin, independent of RefreshMarketplace's own target", changes)
	}
}

func TestEditMarketplaceReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)
	plantDirectoryMarketplace(t, m, "baz")

	newDir := writeTestMarketplaceDir(t, "baz2")
	_, changes, err := m.EditMarketplace(context.Background(), "baz", "", &Source{Kind: SourceDirectory, Path: newDir})
	if err != nil {
		t.Fatalf("EditMarketplace: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true: the migration re-keyed the legacy name's plugin, independent of EditMarketplace's own target", changes)
	}
}

func TestPluginRemoveReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)
	plantInstalledPlugin(t, m, "widget2", "baz")

	changes, err := m.Remove(context.Background(), "widget2", "baz")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true: the migration renamed the legacy marketplace, independent of Remove's own target", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true", changes)
	}
}

func TestSetEnabledReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)
	plantInstalledPlugin(t, m, "widget2", "baz")

	changes, err := m.SetEnabled(context.Background(), "widget2", "baz", false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true: the migration renamed the legacy marketplace, independent of SetEnabled's own target - mutateEntry is also SetAutoUpgrade's mechanism", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true", changes)
	}
}

func TestGcReportsAMigrationThatLandsDuringItsOwnLockAcquisition(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	legacyMigrationPending(t, m)

	_, changes, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if !changes.Marketplaces {
		t.Fatalf("changes = %+v, want Marketplaces true: Gc's own sweep touches neither store file, so this is entirely the migration's own signal", changes)
	}
	if !changes.Plugins {
		t.Fatalf("changes = %+v, want Plugins true", changes)
	}
}
