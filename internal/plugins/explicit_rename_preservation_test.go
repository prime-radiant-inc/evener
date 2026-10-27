package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExplicitRenamePreservesPublishedPaths(t *testing.T) {
	m := NewManager(t.TempDir())
	trees := plantMigrationRevisions(t, m, "original", "widget")
	before, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		t.Fatal(err)
	}
	clone := snapshotMigrationTree(t, m.marketplaceDir("original"))
	ref, err := m.EditMarketplace(context.Background(), "original", "renamed", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
	clone.path = m.marketplaceDir("renamed")
	assertMigrationTree(t, clone)
	if _, err := os.Stat(m.marketplaceDir("original")); !os.IsNotExist(err) {
		t.Fatalf("old clone still present: %v", err)
	}
	wantRef := mk["original"]
	wantRef.InstallLocation = clone.path
	if ref != wantRef {
		t.Fatalf("rename changed source or freshness: %+v", ref)
	}
	reg, err := m.loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := before
	want.Plugins["widget@renamed"] = want.Plugins["widget@original"]
	delete(want.Plugins, "widget@original")
	if !reflect.DeepEqual(reg, want) {
		t.Fatalf("logical rekey changed entries: %+v, want %+v", reg, want)
	}
	mk, err = m.ListMarketplaces(context.Background())
	if err != nil || len(mk) != 1 || mk["renamed"] != ref {
		t.Fatalf("marketplaces after rename: %+v, %v", mk, err)
	}
}

func TestExplicitRenameBackPreservesBothNamespaces(t *testing.T) {
	f := newPublishedRevisionFixture(t, false)
	ctx := context.Background()
	if _, err := f.manager.EditMarketplace(ctx, "acme", "other", nil); err != nil {
		t.Fatal(err)
	}
	advanceGitRepo(t, f.source, "resource.txt", "new namespace resource")
	newer, err := f.manager.Upgrade(ctx, "widget", "other")
	if err != nil {
		t.Fatal(err)
	}
	if newer.GitCommitSha == f.entry.GitCommitSha || newer.InstallPath == f.entry.InstallPath {
		t.Fatalf("Upgrade did not publish a new revision: %+v", newer)
	}
	tree := snapshotMigrationTree(t, newer.InstallPath)
	if _, err := f.manager.EditMarketplace(ctx, "other", "acme", nil); err != nil {
		t.Fatal(err)
	}
	f.assertIntact(t)
	assertMigrationTree(t, tree)
	f.assertRegistryTarget(t, newer)
	mk, err := f.manager.ListMarketplaces(ctx)
	if err != nil || len(mk) != 1 || mk["acme"].Source.Kind != SourceURL {
		t.Fatalf("rename back registration: %+v, %v", mk, err)
	}
	advanceGitRepo(t, f.source, "resource.txt", "publication after rename back")
	newest, err := f.manager.Upgrade(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if newest.GitCommitSha == newer.GitCommitSha || newest.InstallPath == newer.InstallPath || newest.InstallPath == f.entry.InstallPath {
		t.Fatalf("Upgrade after reuse did not publish fresh revision: %+v", newest)
	}
	f.assertIntact(t)
	assertMigrationTree(t, tree)
	f.assertRegistryTarget(t, newest)
	data, err := os.ReadFile(filepath.Join(newest.InstallPath, "resource.txt"))
	if err != nil || string(data) != "publication after rename back" {
		t.Fatalf("fresh Upgrade bytes = %q, %v", data, err)
	}
}

func TestExplicitRenameIntoCacheOnlyNamePreservesPackages(t *testing.T) {
	m := NewManager(t.TempDir())
	trees := plantMigrationRevisions(t, m, "original", "widget")
	retained := m.pluginCacheDir("reused", "widget", "sha1")
	writePlugin(t, retained, "widget", map[string]string{"resources/old.bin": "independent retained bytes", "scripts/old.sh": "old script"})
	old := snapshotMigrationTree(t, filepath.Join(m.cacheDir(), "reused"))
	if _, err := m.EditMarketplace(context.Background(), "original", "reused", nil); err != nil {
		t.Fatal(err)
	}
	for _, tree := range trees {
		assertMigrationTree(t, tree)
	}
	assertMigrationTree(t, old)
	reg, err := m.loadRegistry()
	if err != nil || len(reg.Plugins) != 1 || installedAt(t, reg, "widget@reused").InstallPath != trees[2].path {
		t.Fatalf("reused name adopted retained package: %+v, %v", reg, err)
	}
}

func TestExplicitNameReusePublishesImmutableInstall(t *testing.T) {
	for _, unversioned := range []bool{false, true} {
		name := "versioned"
		if unversioned {
			name = "unversioned-differing-bytes"
		}
		t.Run(name, func(t *testing.T) {
			f := newPublishedRevisionFixture(t, unversioned)
			ctx := context.Background()
			if _, err := f.manager.EditMarketplace(ctx, "acme", "other", nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.manager.EditMarketplace(ctx, "other", "acme", nil); err != nil {
				t.Fatal(err)
			}
			want := f.tree.files["resource.txt"]
			if unversioned {
				want = []byte("different bytes at the same unknown revision")
				if err := os.WriteFile(filepath.Join(f.source, "resource.txt"), want, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			entry, err := f.manager.Install(ctx, "widget", "acme")
			if err != nil {
				t.Fatal(err)
			}
			if entry.GitCommitSha != f.entry.GitCommitSha || entry.InstallPath == f.entry.InstallPath {
				t.Fatalf("same revision reused published path: %+v", entry)
			}
			f.assertIntact(t)
			got, err := os.ReadFile(filepath.Join(entry.InstallPath, "resource.txt"))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("fresh publication bytes = %q, %v", got, err)
			}
			f.assertRegistryTarget(t, entry)
		})
	}
}

// Catalog fallback metadata can change materialized bytes without changing the
// plugin Git revision. Reusing a name must not adopt that occupied SHA path.
func TestExplicitNameReuseSameSHADifferentBytes(t *testing.T) {
	repo := t.TempDir()
	makeGitRepo(t, repo, "resource.txt", "companion bytes")
	catalogDir := makeDirectoryMarketplace(t, "acme", "widget")
	catalog := Catalog{Name: "acme", Owner: CatalogOwner{Name: "fixture"}, Plugins: []CatalogPlugin{{Name: "widget", Description: "original metadata", Source: Source{Kind: SourceURL, URL: repo}}}}
	writeCatalog := func() {
		t.Helper()
		data, err := json.Marshal(catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(catalogDir, ".claude-plugin", "marketplace.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog()
	m, ctx := newManagerCtx(t)
	if _, err := m.AddMarketplace(ctx, "acme", Source{Kind: SourceDirectory, Path: catalogDir}); err != nil {
		t.Fatal(err)
	}
	first, err := m.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if first.GitCommitSha == "" {
		t.Fatal("expected versioned local Git publication")
	}
	tree := snapshotMigrationTree(t, first.InstallPath)
	if _, err := m.EditMarketplace(ctx, "acme", "other", nil); err != nil {
		t.Fatal(err)
	}
	assertMigrationTree(t, tree)
	if _, err := m.EditMarketplace(ctx, "other", "acme", nil); err != nil {
		t.Fatal(err)
	}
	catalog.Plugins[0].Description = "changed metadata"
	writeCatalog()
	unchanged, err := m.Upgrade(ctx, "widget", "acme")
	if err != nil || !reflect.DeepEqual(unchanged, first) {
		t.Fatalf("same-SHA Upgrade no longer a no-op: %+v, %v", unchanged, err)
	}
	next, err := m.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if next.GitCommitSha != first.GitCommitSha || next.InstallPath == first.InstallPath {
		t.Fatalf("same-SHA Install adopted retained bytes: %+v", next)
	}
	assertMigrationTree(t, tree)
	published := snapshotMigrationTree(t, next.InstallPath)
	if bytes.Equal(published.files[".claude-plugin/plugin.json"], tree.files[".claude-plugin/plugin.json"]) || !bytes.Equal(published.files["resource.txt"], tree.files["resource.txt"]) {
		t.Fatal("fresh same-SHA publication lost catalog change or companion bytes")
	}
	var manifest struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(published.files[".claude-plugin/plugin.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Description != catalog.Plugins[0].Description {
		t.Fatal("fresh publication did not use current catalog metadata")
	}
	reg, err := m.loadRegistry()
	if err != nil || len(reg.Plugins) != 1 || installedAt(t, reg, "widget@acme").InstallPath != next.InstallPath {
		t.Fatalf("published registry selection = %+v, %v", reg, err)
	}
}
