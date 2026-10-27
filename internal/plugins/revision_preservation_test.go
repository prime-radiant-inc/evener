package plugins

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/gittest"
)

// publishedRevisionFixture keeps the original install's files and directory
// identity independent of the registry and of the source used for a revisit.
type publishedRevisionFixture struct {
	manager *Manager
	source  string
	entry   InstallEntry
	tree    migrationTree
}

func newPublishedRevisionFixture(t *testing.T, unversioned bool) publishedRevisionFixture {
	t.Helper()
	skipWithoutGit(t)
	root := t.TempDir()
	pluginRepo := filepath.Join(root, "plugin")
	writePlugin(t, pluginRepo, "widget", map[string]string{
		"hooks/hooks.json": `{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"echo old"}]}]}`,
		"scripts/start.sh": "#!/bin/sh\nprintf 'old\\n'\n",
		"resource.txt":     "old companion resource",
	})
	if !unversioned {
		makeGitRepo(t, pluginRepo, "README.md", "first")
	}
	marketRepo := filepath.Join(root, "market")
	if err := os.MkdirAll(filepath.Join(marketRepo, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := `{"source":"url","url":"` + pluginRepo + `"}`
	if unversioned {
		source = `{"source":"directory","path":"` + pluginRepo + `"}`
	}
	catalog := `{"name":"acme","owner":{"name":"o"},"plugins":[{"name":"widget","source":` + source + `}]}`
	if err := os.WriteFile(filepath.Join(marketRepo, ".claude-plugin", "marketplace.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	makeGitRepo(t, marketRepo, "README.md", "market")
	m, ctx := newManagerCtx(t)
	if _, err := m.AddMarketplace(ctx, "", Source{Kind: SourceURL, URL: marketRepo}); err != nil {
		t.Fatal(err)
	}
	entry, err := m.Install(ctx, "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	return publishedRevisionFixture{manager: m, source: pluginRepo, entry: entry, tree: snapshotMigrationTree(t, entry.InstallPath)}
}

func (f publishedRevisionFixture) assertIntact(t *testing.T) {
	t.Helper()
	assertMigrationTree(t, f.tree)
}

func (f publishedRevisionFixture) assertRegistryTarget(t *testing.T, target InstallEntry) {
	t.Helper()
	reg, err := LoadRegistry(f.manager.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	entries := reg.Plugins[registryKey("widget", "acme")]
	if len(entries) != 1 || entries[0].InstallPath != target.InstallPath || entries[0].GitCommitSha != target.GitCommitSha {
		t.Fatalf("registry target changed on failure: %+v", entries)
	}
}

func TestPublishedRevisionSurvivesRevisit(t *testing.T) {
	f := newPublishedRevisionFixture(t, false)
	oldSHA := f.entry.GitCommitSha
	if oldSHA == "" || f.entry.InstallPath != f.manager.pluginCacheDir("acme", "widget", oldSHA) {
		t.Fatalf("first revision must use its SHA: %+v", f.entry)
	}
	advanceGitRepo(t, f.source, "resource.txt", "new companion resource")
	middle, err := f.manager.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if middle.InstallPath == f.entry.InstallPath || middle.GitCommitSha == oldSHA {
		t.Fatalf("upgrade did not advance: %+v", middle)
	}
	middleTree := snapshotMigrationTree(t, middle.InstallPath)
	gittest.Run(t, f.source, "reset", "--hard", oldSHA)
	revisit, err := f.manager.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if revisit.GitCommitSha != oldSHA || revisit.InstallPath == f.entry.InstallPath || revisit.InstallPath == middle.InstallPath {
		t.Fatalf("revisit reused a published destination: %+v", revisit)
	}
	f.assertIntact(t)
	assertMigrationTree(t, middleTree)
	newBytes, err := os.ReadFile(filepath.Join(revisit.InstallPath, "resource.txt"))
	if err != nil || !bytes.Equal(newBytes, f.tree.files["resource.txt"]) {
		t.Fatalf("revisit did not publish original resource: %v, %q", err, newBytes)
	}
	reg, err := LoadRegistry(f.manager.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Plugins[registryKey("widget", "acme")][0].InstallPath; got != revisit.InstallPath {
		t.Fatalf("registry points at %q, want %q", got, revisit.InstallPath)
	}
}

func TestPublishedRevisionSurvivesUnversionedInstall(t *testing.T) {
	f := newPublishedRevisionFixture(t, true)
	if f.entry.GitCommitSha != "" {
		t.Fatalf("directory plugin source unexpectedly has SHA: %+v", f.entry)
	}
	if f.entry.InstallPath == f.manager.pluginCacheDir("acme", "widget", "unknown") {
		t.Fatal("unversioned install claimed the reusable unknown destination")
	}
	if err := os.WriteFile(filepath.Join(f.source, "resource.txt"), []byte("new companion resource"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.manager.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.InstallPath == f.entry.InstallPath {
		t.Fatal("publication reused a protected destination")
	}
	f.assertIntact(t)
	got, err := os.ReadFile(filepath.Join(replacement.InstallPath, "resource.txt"))
	if err != nil || string(got) != "new companion resource" {
		t.Fatalf("replacement resource = %q, %v", got, err)
	}
	reg, err := LoadRegistry(f.manager.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	if entries := reg.Plugins[registryKey("widget", "acme")]; len(entries) != 1 || entries[0].InstallPath != replacement.InstallPath {
		t.Fatalf("replacement not registered: %+v", entries)
	}
}

func TestPublishedRevisionSurvivesPublicationFailure(t *testing.T) {
	faults := []struct {
		name   string
		inject func(t *testing.T, f publishedRevisionFixture)
	}{
		{"manifest permission", func(t *testing.T, f publishedRevisionFixture) {
			old := installManifestFallback
			t.Cleanup(func() { installManifestFallback = old })
			installManifestFallback = func(string, bool, CatalogPlugin) (string, error) {
				return "", &fs.PathError{Op: "write", Err: fs.ErrPermission}
			}
		}},
		{"validation permission", func(t *testing.T, f publishedRevisionFixture) {
			old := installValidateDir
			t.Cleanup(func() { installValidateDir = old })
			installValidateDir = func(string) error { return &fs.PathError{Op: "read", Err: fs.ErrPermission} }
		}},
		{"mkdir ENOSPC", func(t *testing.T, f publishedRevisionFixture) {
			old := installMkdirAll
			t.Cleanup(func() { installMkdirAll = old })
			installMkdirAll = func(string, os.FileMode) error { return &fs.PathError{Op: "mkdir", Err: syscall.ENOSPC} }
		}},
		{"rename permission", func(t *testing.T, f publishedRevisionFixture) {
			old := installRename
			t.Cleanup(func() { installRename = old })
			installRename = func(string, string) error { return &fs.PathError{Op: "rename", Err: fs.ErrPermission} }
		}},
		{"registry ENOSPC", func(t *testing.T, f publishedRevisionFixture) {
			old := installSaveRegistry
			t.Cleanup(func() { installSaveRegistry = old })
			f.manager.Stderr = io.Discard
			installSaveRegistry = func(string, Registry) error { return &fs.PathError{Op: "write", Err: syscall.ENOSPC} }
		}},
	}
	for _, mode := range []string{"install", "upgrade", "revisit"} {
		for _, fault := range faults {
			t.Run(mode+"/"+fault.name, func(t *testing.T) {
				f := newPublishedRevisionFixture(t, false)
				target := f.entry
				if mode == "upgrade" || mode == "revisit" {
					advanceGitRepo(t, f.source, "resource.txt", "new companion resource")
				}
				if mode == "revisit" {
					var err error
					target, err = f.manager.Upgrade(context.Background(), "widget", "acme")
					if err != nil {
						t.Fatal(err)
					}
					gittest.Run(t, f.source, "reset", "--hard", f.entry.GitCommitSha)
				}
				fault.inject(t, f) // package-wide seams: these subtests must stay serial.
				var err error
				if mode == "install" {
					_, err = f.manager.Install(context.Background(), "widget", "acme")
				} else {
					_, err = f.manager.Upgrade(context.Background(), "widget", "acme")
				}
				if err == nil {
					t.Fatal("faulted publication succeeded")
				}
				f.assertIntact(t)
				f.assertRegistryTarget(t, target)
				for _, parent := range []string{
					filepath.Dir(f.entry.InstallPath),
					f.manager.publishingDir("acme", "widget"),
				} {
					staging, globErr := filepath.Glob(filepath.Join(parent, ".staging-*"))
					if globErr != nil || len(staging) != 0 {
						t.Fatalf("owned staging remains after failed publication in %s: %v, %v", parent, staging, globErr)
					}
				}
				if fault.name != "registry ENOSPC" {
					dirs, readErr := os.ReadDir(filepath.Dir(f.entry.InstallPath))
					if readErr != nil {
						t.Fatal(readErr)
					}
					want := 1
					if mode == "revisit" {
						want = 2
					}
					if len(dirs) != want {
						t.Fatalf("failed publication left %d cache entries, want %d: %+v", len(dirs), want, dirs)
					}
				}
			})
		}
	}
}

func TestPublishedRevisionNestedLegacyStagingSurvives(t *testing.T) {
	for _, operation := range []string{"install", "upgrade", "unchanged-upgrade"} {
		t.Run(operation, func(t *testing.T) {
			f := newPublishedRevisionFixture(t, false)
			f.manager.Stderr = io.Discard
			trees := plantMigrationRevisions(t, f.manager, "acme/widget/.staging", "gadget")
			unregistered := f.manager.pluginCacheDir("acme/widget/.staging", "unregistered", "old")
			writePlugin(t, unregistered, "unregistered", map[string]string{"companion.bin": "unregistered old bytes"})
			trees = append(trees, snapshotMigrationTree(t, unregistered))
			if _, err := f.manager.ListMarketplaces(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				assertMigrationTree(t, tree)
			}
			if operation != "unchanged-upgrade" {
				advanceGitRepo(t, f.source, "resource.txt", "new companion resource")
			}
			var err error
			if operation == "install" {
				_, err = f.manager.Install(context.Background(), "widget", "acme")
			} else {
				_, err = f.manager.Upgrade(context.Background(), "widget", "acme")
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, tree := range trees {
				assertMigrationTree(t, tree)
			}
			f.assertIntact(t)
		})
	}
}
