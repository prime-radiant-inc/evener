//go:build serffuzz

package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errInstallCoverage = errors.New("coverage error")

func FuzzPluginsInstallCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("catalog-stage-commit", fuzzInstallHelpers)
		t.Run("install", fuzzInstallBranches)
		t.Run("upgrade", fuzzUpgradeBranches)
		t.Run("registry-operations", fuzzInstallRegistryBranches)
		t.Run("auto-upgrade", fuzzAutoUpgradeBranches)
	})
}

func fuzzUpgradeBranches(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		breakIt func(*Manager)
	}{
		{"load", func(*Manager) {
			installLoadRegistry = func(string) (Registry, error) { return Registry{}, errInstallCoverage }
		}},
		{"catalog", func(*Manager) {
			installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) {
				return MarketplaceRef{}, errInstallCoverage
			}
		}},
		{"stage", func(*Manager) {
			installFetchSource = func(context.Context, Source, string, string) (string, error) { return "", errInstallCoverage }
		}},
		{"commit", func(*Manager) { installRename = func(string, string) error { return errInstallCoverage } }},
		{"manifest", func(*Manager) {
			installManifestFallback = func(string, bool, CatalogPlugin) (string, error) { return "", errInstallCoverage }
		}},
		{"validate", func(*Manager) { installValidateDir = func(string) error { return errInstallCoverage } }},
		{"save", func(*Manager) { installSaveRegistry = func(string, Registry) error { return errInstallCoverage } }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			fakeInstallSuccess(t, m)
			installLoadRegistry = func(string) (Registry, error) {
				return Registry{Plugins: map[string][]InstallEntry{"plugin@market": {{GitCommitSha: "old", AutoUpgrade: true, Source: Source{Kind: SourceGitHub}}}}}, nil
			}
			tc.breakIt(m)
			if _, _, _, err := m.upgradeLocked(ctx, "plugin", "market", false); err == nil {
				t.Fatal("upgrade succeeded")
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		m := NewManager(t.TempDir())
		fakeInstallSuccess(t, m)
		if _, _, _, err := m.upgradeLocked(ctx, "missing", "market", false); err == nil {
			t.Fatal("missing upgraded")
		}
	})
	t.Run("auto-skips", func(t *testing.T) {
		for _, entry := range []InstallEntry{
			{AutoUpgrade: false, Source: Source{Kind: SourceGitHub}},
			{AutoUpgrade: true, Source: Source{Rel: true}},
			{AutoUpgrade: true, Source: Source{Kind: SourceDirectory}},
		} {
			m := NewManager(t.TempDir())
			fakeInstallSuccess(t, m)
			installLoadRegistry = func(string) (Registry, error) {
				return Registry{Plugins: map[string][]InstallEntry{"plugin@market": {entry}}}, nil
			}
			if _, changed, skipped, err := m.upgradeLocked(ctx, "plugin", "market", true); err != nil || changed || !skipped {
				t.Fatalf("skip = %v %v %v", changed, skipped, err)
			}
		}
	})
}

func resetInstallSeams(t *testing.T) {
	t.Helper()
	oldAcquire, oldEnsure, oldParse := installAcquireLock, installEnsureFetched, installParseCatalog
	oldFetch, oldRemove, oldMkdir := installFetchSource, installRemoveAll, installMkdirAll
	oldRename, oldFallback, oldValidate := installRename, installManifestFallback, installValidateDir
	oldVersion, oldLoad, oldSave := installManifestVersion, installLoadRegistry, installSaveRegistry
	t.Cleanup(func() {
		installAcquireLock, installEnsureFetched, installParseCatalog = oldAcquire, oldEnsure, oldParse
		installFetchSource, installRemoveAll, installMkdirAll = oldFetch, oldRemove, oldMkdir
		installRename, installManifestFallback, installValidateDir = oldRename, oldFallback, oldValidate
		installManifestVersion, installLoadRegistry, installSaveRegistry = oldVersion, oldLoad, oldSave
	})
}

func fakeInstallSuccess(t *testing.T, m *Manager) {
	t.Helper()
	resetInstallSeams(t)
	installAcquireLock = func(string, time.Duration) (func(), error) { return func() {}, nil }
	installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) {
		return MarketplaceRef{Source: Source{Kind: SourceGitHub}, InstallLocation: t.TempDir()}, nil
	}
	installParseCatalog = func(string) (Catalog, error) {
		return Catalog{Plugins: []CatalogPlugin{{Name: "plugin", Source: Source{Kind: SourceGitHub, URL: "fake"}}}}, nil
	}
	installFetchSource = func(_ context.Context, _ Source, _ string, dst string) (string, error) {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return "", err
		}
		return "new-sha", nil
	}
	installManifestFallback = func(string, bool, CatalogPlugin) (string, error) { return "note", nil }
	installValidateDir = func(string) error { return nil }
	installManifestVersion = func(string) string { return "1.0.0" }
	installLoadRegistry = func(string) (Registry, error) {
		return Registry{Plugins: map[string][]InstallEntry{}}, nil
	}
	installSaveRegistry = func(string, Registry) error { return nil }
}

func fuzzInstallHelpers(t *testing.T) {
	m := NewManager(t.TempDir())
	resetInstallSeams(t)
	installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) {
		return MarketplaceRef{}, errInstallCoverage
	}
	if _, _, err := m.catalogPlugin(context.Background(), "market", "plugin"); err == nil {
		t.Fatal("ensure error ignored")
	}
	installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) { return MarketplaceRef{}, nil }
	installParseCatalog = func(string) (Catalog, error) { return Catalog{}, errInstallCoverage }
	if _, _, err := m.catalogPlugin(context.Background(), "market", "plugin"); err == nil {
		t.Fatal("parse error ignored")
	}
	installParseCatalog = func(string) (Catalog, error) { return Catalog{}, nil }
	if _, _, err := m.catalogPlugin(context.Background(), "market", "plugin"); err == nil {
		t.Fatal("missing plugin ignored")
	}
	installParseCatalog = func(string) (Catalog, error) { return Catalog{Plugins: []CatalogPlugin{{Name: "plugin"}}}, nil }
	if _, _, err := m.catalogPlugin(context.Background(), "market", "plugin"); err != nil {
		t.Fatal(err)
	}

	ref := MarketplaceRef{Source: Source{Kind: SourceDirectory}, InstallLocation: "/market"}
	if dir, _, staged, _ := m.stagePlugin(context.Background(), "market", "plugin", ref, CatalogPlugin{Source: Source{Rel: true, Path: "rel"}}); dir != filepath.Join("/market", "rel") || staged {
		t.Fatal("relative stage")
	}
	if dir, _, staged, _ := m.stagePlugin(context.Background(), "market", "plugin", ref, CatalogPlugin{Source: Source{Kind: SourceDirectory, Path: "/plugin"}}); dir != "/plugin" || staged {
		t.Fatal("directory stage")
	}
	installFetchSource = func(context.Context, Source, string, string) (string, error) { return "", errInstallCoverage }
	if _, _, _, err := m.stagePlugin(context.Background(), "market", "plugin", MarketplaceRef{}, CatalogPlugin{}); err == nil {
		t.Fatal("fetch error ignored")
	}
	installFetchSource = func(context.Context, Source, string, string) (string, error) { return "sha", nil }
	if _, sha, staged, err := m.stagePlugin(context.Background(), "market", "plugin", MarketplaceRef{}, CatalogPlugin{}); err != nil || sha != "sha" || !staged {
		t.Fatal("stage success")
	}

	staging := t.TempDir()
	installMkdirAll = func(string, os.FileMode) error { return errInstallCoverage }
	if _, err := m.commitStaged("market", "plugin", staging, ""); err == nil {
		t.Fatal("mkdir error ignored")
	}
	installMkdirAll = os.MkdirAll
	installRename = func(string, string) error { return errInstallCoverage }
	if _, err := m.commitStaged("market", "plugin", t.TempDir(), "sha"); err == nil {
		t.Fatal("rename error ignored")
	}
}

func fuzzInstallBranches(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		breakIt func()
	}{
		{"lock", func() {
			installAcquireLock = func(string, time.Duration) (func(), error) { return nil, errInstallCoverage }
		}},
		{"catalog", func() {
			installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) {
				return MarketplaceRef{}, errInstallCoverage
			}
		}},
		{"stage", func() {
			installFetchSource = func(context.Context, Source, string, string) (string, error) { return "", errInstallCoverage }
		}},
		{"commit", func() { installRename = func(string, string) error { return errInstallCoverage } }},
		{"manifest", func() {
			installManifestFallback = func(string, bool, CatalogPlugin) (string, error) { return "", errInstallCoverage }
		}},
		{"validate", func() { installValidateDir = func(string) error { return errInstallCoverage } }},
		{"load", func() { installLoadRegistry = func(string) (Registry, error) { return Registry{}, errInstallCoverage } }},
		{"save", func() { installSaveRegistry = func(string, Registry) error { return errInstallCoverage } }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(t.TempDir())
			fakeInstallSuccess(t, m)
			tc.breakIt()
			if _, err := m.Install(ctx, "plugin", "market"); err == nil {
				t.Fatal("install succeeded")
			}
		})
	}
	m := NewManager(t.TempDir())
	fakeInstallSuccess(t, m)
	if _, err := m.Install(ctx, "plugin", "market"); err != nil {
		t.Fatal(err)
	}
	installFetchSource = func(_ context.Context, _ Source, _ string, dst string) (string, error) {
		_ = os.MkdirAll(dst, 0o755)
		return "old", nil
	}
	installLoadRegistry = func(string) (Registry, error) {
		return Registry{Plugins: map[string][]InstallEntry{"plugin@market": {{GitCommitSha: "old", Source: Source{Kind: SourceGitHub}}}}}, nil
	}
	if _, err := m.Upgrade(ctx, "plugin", "market"); err != nil {
		t.Fatal(err)
	}
}

func fuzzInstallRegistryBranches(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	fakeInstallSuccess(t, m)
	installAcquireLock = func(string, time.Duration) (func(), error) { return nil, errInstallCoverage }
	if _, err := m.Upgrade(ctx, "p", "m"); err == nil {
		t.Fatal("upgrade lock")
	}
	if err := m.SetEnabled("p", "m", true); err == nil {
		t.Fatal("mutate lock")
	}
	if err := m.Remove("p", "m"); err == nil {
		t.Fatal("remove lock")
	}
	installAcquireLock = func(string, time.Duration) (func(), error) { return func() {}, nil }
	installLoadRegistry = func(string) (Registry, error) { return Registry{}, errInstallCoverage }
	if err := m.mutateEntry("p", "m", func(*InstallEntry) {}); err == nil {
		t.Fatal("mutate load")
	}
	if err := m.Remove("p", "m"); err == nil {
		t.Fatal("remove load")
	}
	if _, err := m.List(); err == nil {
		t.Fatal("list load")
	}
	if _, err := m.UpdateAll(ctx); err == nil {
		t.Fatal("update load")
	}

	installLoadRegistry = func(string) (Registry, error) {
		return Registry{Plugins: map[string][]InstallEntry{"p@m": {{InstallPath: filepath.Join(m.cacheDir(), "p"), Source: Source{Kind: SourceGitHub}}}, "empty@m": {}}}, nil
	}
	installSaveRegistry = func(string, Registry) error { return errInstallCoverage }
	if err := m.SetEnabled("p", "m", true); err == nil {
		t.Fatal("mutate save")
	}
	if err := m.Remove("p", "m"); err == nil {
		t.Fatal("remove save")
	}
	installValidateDir = func(string) error { return errInstallCoverage }
	if got, err := m.List(); err != nil || len(got) != 1 || !got[0].Broken {
		t.Fatalf("list %#v %v", got, err)
	}
	installLoadRegistry = func(string) (Registry, error) {
		return Registry{Plugins: map[string][]InstallEntry{
			"z@b": {{}}, "a@z": {{}}, "a@a": {{}},
		}}, nil
	}
	if got, err := m.List(); err != nil || len(got) != 3 || got[0].Marketplace != "a" || got[2].Plugin != "z" {
		t.Fatalf("sorted list %#v %v", got, err)
	}
	installSaveRegistry = func(string, Registry) error { return nil }
	installEnsureFetched = func(*Manager, context.Context, string) (MarketplaceRef, error) {
		return MarketplaceRef{}, errInstallCoverage
	}
	if got, err := m.UpdateAll(ctx); err == nil || len(got) != 0 {
		t.Fatalf("update %#v %v", got, err)
	}
	if p, market := splitKey("p@m"); p != "p" || market != "m" {
		t.Fatal("split")
	}
}

func fuzzAutoUpgradeBranches(t *testing.T) {
	ctx := context.Background()
	m := NewManager(t.TempDir())
	fakeInstallSuccess(t, m)
	installAcquireLock = func(string, time.Duration) (func(), error) { return nil, errInstallCoverage }
	if _, _, _, err := m.upgradeAuto(ctx, "p", "m"); err == nil {
		t.Fatal("auto lock")
	}
	installAcquireLock = func(string, time.Duration) (func(), error) { return func() {}, nil }
	installLoadRegistry = func(string) (Registry, error) { return Registry{}, errInstallCoverage }
	if _, err := m.UpdateAutoUpgrade(ctx); err == nil {
		t.Fatal("auto load")
	}
	installLoadRegistry = func(string) (Registry, error) {
		return Registry{Plugins: map[string][]InstallEntry{"bad@m": {{AutoUpgrade: true, Source: Source{Kind: SourceGitHub}}}, "skip@m": {{AutoUpgrade: false}}}}, nil
	}
	if got, err := m.UpdateAutoUpgrade(ctx); err == nil || len(got) != 0 {
		t.Fatalf("auto %#v %v", got, err)
	}
}
