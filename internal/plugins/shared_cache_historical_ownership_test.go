package plugins

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/gittest"
)

// A superseded migrated revision remains readable after its parent namespace is
// explicitly renamed, even when no current registry selection names that path.
func TestEditMarketplaceAfterUpgradePreservesMigratedRevision(t *testing.T) {
	m, historical, published := historicalSharedCacheAfterUpgrade(t)
	if _, err := m.EditMarketplace(context.Background(), "a", "renamed", nil); err != nil {
		t.Fatal(err)
	}
	assertMigrationTree(t, published)
	reg, err := m.loadRegistry()
	if err != nil || installedAt(t, reg, "widget@a-b").InstallPath != published.path {
		t.Fatalf("foreign selection after parent rename = %+v, %v", reg, err)
	}
	if _, present := reg.Plugins["gadget@a"]; present {
		t.Fatal("old parent registry key survived")
	}
	if installedAt(t, reg, "gadget@renamed").InstallPath != m.pluginCacheDir("a", "gadget", "sha1") {
		t.Fatal("parent install path changed")
	}
	for _, tree := range historical {
		assertMigrationTree(t, tree)
	}
}

// The initial legacy on-disk state is planted as an older writer would have
// recorded it. Migration and publication of B use actual Manager operations and
// a supported local Git marketplace, never a hand-edited final B registry.
func historicalSharedCacheAfterUpgrade(t *testing.T) (*Manager, []migrationTree, migrationTree) {
	t.Helper()
	repo := makeMarketplaceRepoWithPlugin(t, "acme", "widget")
	plugin := filepath.Join(repo, "plugins", "widget")
	for file, data := range map[string]string{
		"skills/probe/SKILL.md":   "B-skill",
		"scripts/hook.sh":         "B-hook",
		"resources/companion.bin": "B-resource",
	} {
		path := filepath.Join(plugin, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gittest.Run(t, repo, "add", "plugins/widget")
	gittest.Run(t, repo, "commit", "-q", "-m", "publish B package")
	wantSHA := gittest.Run(t, repo, "rev-parse", "HEAD:plugins/widget")
	wantPackage := snapshotMigrationTree(t, plugin)

	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	historical := plantMigrationRevisions(t, m, "a/b", "widget")
	plantLegacyMarketplace(t, m, "a", "gadget")
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	legacy := mk["a/b"]
	legacy.Source = Source{Kind: SourceURL, URL: repo}
	legacy.InstallLocation = "" // Supported unfetched local Git marketplace.
	mk["a/b"] = legacy
	if err := m.saveMarketplaces(mk); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mk, err = m.ListMarketplaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, old := mk["a/b"]; old || len(mk) != 2 || mk["a-b"].Source != legacy.Source {
		t.Fatalf("actual logical migration = %+v", mk)
	}
	reg, err := m.loadRegistry()
	if err != nil || installedAt(t, reg, "widget@a-b").InstallPath != historical[2].path {
		t.Fatalf("migrated A selection = %+v, %v", reg, err)
	}
	for _, tree := range historical {
		assertMigrationTree(t, tree)
	}
	entry, err := m.Upgrade(ctx, "widget", "a-b")
	if err != nil {
		t.Fatal(err)
	}
	if entry.GitCommitSha != wantSHA || entry.InstallPath != m.pluginCacheDir("a-b", "widget", wantSHA) || entry.InstallPath == historical[2].path {
		t.Fatalf("real Upgrade did not publish B in canonical namespace: %+v", entry)
	}
	published := snapshotMigrationTree(t, entry.InstallPath)
	if !reflect.DeepEqual(published.files, wantPackage.files) {
		t.Fatal("published B package differs from independent local Git source snapshot")
	}
	reg, err = m.loadRegistry()
	if err != nil || !reflect.DeepEqual(reg.Plugins["widget@a-b"], []InstallEntry{entry}) {
		t.Fatalf("real Upgrade did not select B: %+v, %v", reg, err)
	}
	for _, tree := range historical {
		assertMigrationTree(t, tree)
	}
	return m, historical, published
}

func TestMigratedRevisionsSurviveExplicitOperations(t *testing.T) {
	for _, operation := range []string{"rename", "resource", "removal", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			m, historical, published := historicalSharedCacheAfterUpgrade(t)
			ctx := context.Background()
			var err error
			switch operation {
			case "rename":
				_, err = m.EditMarketplace(ctx, "a-b", "foreign-new", nil)
			case "resource":
				src := Source{Kind: SourceDirectory, Path: makeDirectoryMarketplace(t, "replacement", "widget")}
				_, err = m.EditMarketplace(ctx, "a-b", "", &src)
			case "removal":
				err = m.RemoveMarketplace(ctx, "a-b")
			case "uninstall":
				err = m.Remove(ctx, "widget", "a-b")
			}
			if err != nil {
				t.Fatal(err)
			}
			reg, err := m.loadRegistry()
			if err != nil {
				t.Fatal(err)
			}
			if operation == "uninstall" {
				if _, present := reg.Plugins["widget@a-b"]; present {
					t.Fatal("uninstall left current registry selection")
				}
				if _, err := os.Stat(published.path); !os.IsNotExist(err) {
					t.Fatalf("uninstall retained selected package: %v", err)
				}
			} else {
				key := "widget@a-b"
				if operation == "rename" {
					key = "widget@foreign-new"
				}
				if installedAt(t, reg, key).InstallPath != published.path {
					t.Fatal("explicit operation changed selected path")
				}
				assertMigrationTree(t, published)
			}
			for _, tree := range historical {
				assertMigrationTree(t, tree)
			}
			plantLegacyMarketplace(t, m, "trigger/legacy", "trigger")
			if _, err := m.ListMarketplaces(ctx); err != nil {
				t.Fatal(err)
			}
			for _, tree := range historical {
				assertMigrationTree(t, tree)
			}
			if operation != "uninstall" {
				assertMigrationTree(t, published)
			}
		})
	}
}

func TestMigrationMarkerWriteFailurePreservesPackagesAndRetries(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	historical := plantMigrationRevisions(t, m, "a/b", "widget")
	write := marketplaceAtomicWriteFile
	t.Cleanup(func() { marketplaceAtomicWriteFile = write })
	marketplaceAtomicWriteFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == renameMarkerFileName {
			return os.ErrPermission
		}
		return write(path, data, mode)
	}
	if _, err := m.ListMarketplaces(context.Background()); err == nil {
		t.Fatal("marker write failure did not stop migration")
	}
	mk, err := m.loadMarketplaces()
	if err != nil || len(mk) != 1 {
		t.Fatalf("marketplaces after marker failure = %+v, %v", mk, err)
	}
	if _, old := mk["a/b"]; !old {
		t.Fatal("logical rename committed despite marker failure")
	}
	reg, err := m.loadRegistry()
	if err != nil || len(reg.Plugins) != 1 || installedAt(t, reg, "widget@a/b").InstallPath != historical[2].path {
		t.Fatalf("registry after marker failure = %+v, %v", reg, err)
	}
	marketplaceAtomicWriteFile = write
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg, err = m.loadRegistry()
	if err != nil || len(reg.Plugins) != 1 || installedAt(t, reg, "widget@a-b").InstallPath != historical[2].path {
		t.Fatalf("retry selected registry = %+v, %v", reg, err)
	}
	for _, tree := range historical {
		assertMigrationTree(t, tree)
	}
}

func TestMigrationMarkerRecoveryPreservesPackagesWithoutRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	m.Stderr = io.Discard
	historical := plantMigrationRevisions(t, m, "a/b", "widget")
	// A preexisting marker does not establish that its writer saved the alias
	// family record. Recovery handles such markers without synthesizing it.
	plantRenameMarker(t, m, "a/b", "a-b")
	if _, err := m.ListMarketplaces(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg, err := m.loadRegistry()
	if err != nil || installedAt(t, reg, "widget@a-b").InstallPath != historical[2].path {
		t.Fatalf("recovered logical selection = %+v, %v", reg, err)
	}
	for _, tree := range historical {
		assertMigrationTree(t, tree)
	}
}
