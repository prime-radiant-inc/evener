package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCopyTreeSkipsGitAndRefusesGitAlias(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, ".git", "config"), "token")
	writeTestFile(t, filepath.Join(src, "README.md"), "keep")
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "guide")
	if err := os.Symlink("docs/guide.md", filepath.Join(src, "guide-link")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "published")
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf("copied .git survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "guide-link")); err != nil || string(got) != "guide" {
		t.Fatalf("ordinary link lost its existing dereference behavior: %q, %v", got, err)
	}

	alias := t.TempDir()
	writeTestFile(t, filepath.Join(alias, ".git", "config"), "token")
	if err := os.Symlink(".git/config", filepath.Join(alias, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(alias, filepath.Join(t.TempDir(), "refused")); err == nil {
		t.Fatal("a source symlink reaching .git/config was copied")
	}
}

func TestManagedInstallPublishesWithoutGitMetadata(t *testing.T) {
	f := newPublishedRevisionFixture(t, false)
	if _, err := os.Lstat(filepath.Join(f.entry.InstallPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("published plugin carries .git: %v", err)
	}
	staging := filepath.Join(f.manager.publishingDir("acme", "widget"), ".staging")
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("private staging remains after publication: %v", err)
	}
	if _, err := os.Stat(f.manager.pluginCacheDir("acme", "widget", ".staging")); !os.IsNotExist(err) {
		t.Fatalf("staging was created in the content root: %v", err)
	}
}

func TestEmptySHACopiedInstallPublishesDistinctRevisions(t *testing.T) {
	f := newPublishedRevisionFixture(t, true)
	f.assertIntact(t)
	replacement, err := f.manager.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.InstallPath == f.entry.InstallPath {
		t.Fatal("reinstall reused the first copied revision")
	}
	f.assertIntact(t)
}

func TestAuditBundledPayloadRefusesGit(t *testing.T) {
	payload := t.TempDir()
	writeTestFile(t, filepath.Join(payload, "skill", ".git", "config"), "token")
	if err := auditBundledPayload(payload); err == nil {
		t.Fatal("bundled payload with .git passed its audit")
	}
}

func TestBundledStagingIsOutsideTheGrantedRoot(t *testing.T) {
	m := NewManager(t.TempDir())
	_, staging, _, err := m.prepareBundledStore(context.Background(), "coordinator-workflow", publishBundledStore)
	if err != nil {
		t.Fatal(err)
	}
	if staging == nil {
		t.Fatal("fresh store returned no staging directory")
	}
	defer staging.release()
	defer func() { _ = os.RemoveAll(staging.dir) }()
	if !strings.HasPrefix(staging.dir, m.bundledPublishingDir()+string(os.PathSeparator)) {
		t.Fatalf("bundled staging %q is not under %q", staging.dir, m.bundledPublishingDir())
	}
	if strings.HasPrefix(staging.dir, m.bundledDir()+string(os.PathSeparator)) {
		t.Fatalf("bundled staging %q is inside the granted bundled root", staging.dir)
	}
}

func TestManagedPublicationJourneyPreservesOlderSessionPaths(t *testing.T) {
	f := newPublishedRevisionFixture(t, false)
	oldPath := f.entry.InstallPath
	oldBytes := f.files["resource.txt"]

	advanceGitRepo(t, f.source, "resource.txt", "newer companion resource")
	if _, err := f.manager.Upgrade(context.Background(), "widget", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The upgrade and GC must not remove the path a saved session still names.
	if got, err := os.ReadFile(filepath.Join(oldPath, "resource.txt")); err != nil || string(got) != string(oldBytes) {
		t.Fatalf("older session path lost its bytes: %q, %v", got, err)
	}

	// A legacy revision still carries metadata until the one-time migration.
	if err := os.Remove(filepath.Join(f.manager.Root, storeFormatFileName)); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(oldPath, ".git", "config"), "legacy-token")
	f.manager.ensureManagedContentMigrated(context.Background())
	if !f.manager.managedContentClean() {
		t.Fatal("journey migration did not complete")
	}
	if _, err := os.Lstat(filepath.Join(oldPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("older path still carries .git: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(oldPath, "resource.txt")); err != nil || string(got) != string(oldBytes) {
		t.Fatalf("migration changed older session bytes: %q, %v", got, err)
	}
	// A resumed session reads the same path and the repeated policy resolution
	// keeps the store clean.
	for range 2 {
		f.manager.ensureManagedContentMigrated(context.Background())
		if !f.manager.managedContentClean() {
			t.Fatal("repeated resolution lost the clean state")
		}
	}
}
