package plugins

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/internal/bundled"
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

func TestCopiedSourceAllowsOrdinaryLinkUnderGitNamespace(t *testing.T) {
	src := filepath.Join(t.TempDir(), ".git", "acme", "revision")
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "guide")
	if err := os.Symlink("docs/guide.md", filepath.Join(src, "guide-link")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "staging")
	if _, err := fetchPluginSource(context.Background(), Source{Kind: SourceDirectory, Path: src}, "", dest); err != nil {
		t.Fatalf("legitimate .git namespace ancestor rejected: %v", err)
	}
	if err := auditPluginContent(dest); err != nil {
		t.Fatalf("prepared content under a .git namespace failed audit: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "guide-link")); err != nil || string(got) != "guide" {
		t.Fatalf("ordinary link under a .git namespace = %q, %v", got, err)
	}
}

func TestCopiedSourceAllowsLinkUnderSymlinkedGitNamespace(t *testing.T) {
	realParent := filepath.Join(t.TempDir(), ".git", "revision")
	if err := os.MkdirAll(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(linkParent, "plugin")
	writeTestFile(t, filepath.Join(src, "docs", "guide.md"), "guide")
	if err := os.Symlink("docs/guide.md", filepath.Join(src, "guide-link")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "staging")
	if _, err := fetchPluginSource(context.Background(), Source{Kind: SourceDirectory, Path: src}, "", dest); err != nil {
		t.Fatalf("ordinary link under a symlinked .git namespace was rejected: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "guide-link")); err != nil || string(got) != "guide" {
		t.Fatalf("ordinary link under a symlinked .git namespace = %q, %v", got, err)
	}
}

func TestPublicationFallsBackAcrossFilesystems(t *testing.T) {
	m := NewManager(t.TempDir())
	staging := filepath.Join(t.TempDir(), "staging")
	writeTestFile(t, filepath.Join(staging, "README.md"), "content")
	oldRename := installRename
	t.Cleanup(func() { installRename = oldRename })
	calls := 0
	installRename = func(from, to string) error {
		calls++
		if calls == 1 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return oldRename(from, to)
	}
	final, err := m.commitStaged("acme", "widget", staging, "sha")
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("cross-device rename did not fall back: %d calls", calls)
	}
	if got, err := os.ReadFile(filepath.Join(final, "README.md")); err != nil || string(got) != "content" {
		t.Fatalf("cross-device publication = %q, %v", got, err)
	}
}

func TestPublishReclaimsAbandonedCrossFilesystemScratch(t *testing.T) {
	parent := t.TempDir()
	stale := filepath.Join(parent, publishScratchPrefix+"stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := stale + publishScratchMarkerSuffix
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * abandonedPublishScratch)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	young := filepath.Join(parent, publishScratchPrefix+"young")
	if err := os.MkdirAll(young, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(young+publishScratchMarkerSuffix, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimPublishScratch(parent)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("abandoned cross-filesystem copy survived: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("abandoned marker survived: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Fatalf("young in-flight copy was reclaimed: %v", err)
	}
}

func TestMetadataArchiveFallsBackAcrossFilesystems(t *testing.T) {
	src := filepath.Join(t.TempDir(), ".git")
	writeTestFile(t, filepath.Join(src, "config"), "token")
	if err := os.Symlink("config", filepath.Join(src, "alias")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "archive", ".git")
	oldRename := moveRename
	t.Cleanup(func() { moveRename = oldRename })
	calls := 0
	moveRename = func(from, to string) error {
		calls++
		if calls == 1 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return oldRename(from, to)
	}
	marker := filepath.Join(t.TempDir(), managedGitJournalDirName, "cache", "acme", "widget", "aaa", ".git"+managedGitArchivePendingSuffix)
	if err := moveNoReplace(src, dst, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("archived source survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "config")); err != nil || string(got) != "token" {
		t.Fatalf("archive bytes = %q, %v", got, err)
	}
	if link, err := os.Readlink(filepath.Join(dst, "alias")); err != nil || link != "config" {
		t.Fatalf("archive symlink = %q, %v", link, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("journal marker survived the completed archive: %v", err)
	}
}

func TestPublishingGitNamespaceInvalidatesCleanState(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.writeManagedContentState(managedContentClean); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(t.TempDir(), "staging")
	writeTestFile(t, filepath.Join(staging, "README.md"), "content")
	if _, err := m.commitStaged("acme", ".git", staging, "sha"); err != nil {
		t.Fatal(err)
	}
	if m.managedContentClean() {
		t.Fatal("a published .git namespace left the store claimed clean")
	}
}

func TestManagedInstallPublishesWithoutGitMetadata(t *testing.T) {
	f := newPublishedRevisionFixture(t, false)
	if _, err := os.Lstat(filepath.Join(f.entry.InstallPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("published plugin carries .git: %v", err)
	}
	for _, parent := range []string{
		f.manager.publishingDir("acme", "widget"),
		filepath.Join(f.manager.cacheDir(), "acme", "widget"),
	} {
		matches, err := filepath.Glob(filepath.Join(parent, ".staging*"))
		if err != nil {
			t.Fatalf("globbing %s: %v", parent, err)
		}
		if len(matches) > 0 {
			t.Fatalf("staging remains under %s: %v", parent, matches)
		}
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

func TestEmbeddedBundledTreeHasNoGitMetadata(t *testing.T) {
	err := fs.WalkDir(bundled.Plugins(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if isGitName(d.Name()) {
			t.Errorf("embedded bundled tree carries Git metadata at %q", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
	oldBytes := f.tree.files["resource.txt"]

	advanceGitRepo(t, f.source, "resource.txt", "newer companion resource")
	if _, err := f.manager.Upgrade(context.Background(), "widget", "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.Gc(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The upgrade and GC must not remove the path a saved session still names.
	if got, err := os.ReadFile(filepath.Join(oldPath, "resource.txt")); err != nil || !bytes.Equal(got, oldBytes) {
		t.Fatalf("older session path lost its bytes: %q, %v", got, err)
	}

	// A legacy revision still carries metadata until the one-time migration.
	if err := os.Remove(filepath.Join(f.manager.Root, storeFormatFileName)); err != nil {
		t.Fatal(err)
	}
	writeGitAdministration(t, filepath.Join(oldPath, ".git"))
	f.manager.ensureManagedContentMigrated(context.Background())
	if !f.manager.managedContentClean() {
		t.Fatal("journey migration did not complete")
	}
	if _, err := os.Lstat(filepath.Join(oldPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("older path still carries .git: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(oldPath, "resource.txt")); err != nil || !bytes.Equal(got, oldBytes) {
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

func assertMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func TestCopyTreeExactRestoresReadOnlyDirectoryModes(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	nested := filepath.Join(src, "nested")
	writeTestFile(t, filepath.Join(src, "root.txt"), "root bytes")
	writeTestFile(t, filepath.Join(nested, "nested.txt"), "nested bytes")
	if err := os.Chmod(nested, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy")
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(dst, "nested"), 0o755)
		_ = os.Chmod(dst, 0o755)
		_ = os.Chmod(nested, 0o755)
		_ = os.Chmod(src, 0o755)
	})

	if err := copyTreeExact(src, dst); err != nil {
		t.Fatal(err)
	}

	assertMode(t, dst, 0o555)
	assertMode(t, filepath.Join(dst, "nested"), 0o555)
	assertMode(t, filepath.Join(dst, "root.txt"), 0o644)
	assertMode(t, filepath.Join(dst, "nested", "nested.txt"), 0o644)
	if got, err := os.ReadFile(filepath.Join(dst, "root.txt")); err != nil || string(got) != "root bytes" {
		t.Fatalf("root file = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "nested", "nested.txt")); err != nil || string(got) != "nested bytes" {
		t.Fatalf("nested file = %q, %v", got, err)
	}
}

// TestCopyTreeExactRefusesToOverwriteExistingEntry pins the mechanism that
// makes a name collision on the destination fail instead of silently
// replacing content. The source is left untouched on failure.
func TestCopyTreeExactRefusesToOverwriteExistingEntry(t *testing.T) {
	src := filepath.Join(t.TempDir(), "source")
	writeTestFile(t, filepath.Join(src, "root.txt"), "source bytes")
	dst := filepath.Join(t.TempDir(), "copy")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dst, "root.txt"), "destination bytes")

	err := copyTreeExact(src, dst)
	if err == nil {
		t.Fatal("copyTreeExact replaced an existing destination entry")
	}
	if got, readErr := os.ReadFile(filepath.Join(dst, "root.txt")); readErr != nil || string(got) != "destination bytes" {
		t.Fatalf("destination entry = %q, %v", got, readErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(src, "root.txt")); readErr != nil || string(got) != "source bytes" {
		t.Fatalf("source entry = %q, %v", got, readErr)
	}
}

// TestCopyTreeExactRefusesCaseCollidingDestination covers the case-insensitive
// filesystem failure: two distinct source names that the destination equates.
func TestCopyTreeExactRefusesCaseCollidingDestination(t *testing.T) {
	parent := t.TempDir()
	probe := filepath.Join(parent, "CaseProbe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "caseprobe")); err != nil {
		t.Skip("destination filesystem is case-sensitive; case collision not representable")
	}
	src := filepath.Join(parent, "source")
	writeTestFile(t, filepath.Join(src, "Feature"), "A")
	dst := filepath.Join(parent, "copy")
	if err := os.Mkdir(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dst, "feature"), "B")

	err := copyTreeExact(src, dst)
	if err == nil {
		t.Fatal("copyTreeExact collapsed a case-colliding destination entry")
	}
	if got, readErr := os.ReadFile(filepath.Join(dst, "feature")); readErr != nil || string(got) != "B" {
		t.Fatalf("destination entry = %q, %v", got, readErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(src, "Feature")); readErr != nil || string(got) != "A" {
		t.Fatalf("source entry = %q, %v", got, readErr)
	}
}

func TestPublishStagedCrossDeviceRestoresReadOnlyDirectoryModes(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, "staging")
	nested := filepath.Join(staging, "nested")
	writeTestFile(t, filepath.Join(staging, "root.txt"), "root bytes")
	writeTestFile(t, filepath.Join(nested, "nested.txt"), "nested bytes")
	if err := os.Chmod(nested, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging, 0o555); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(t.TempDir(), "published")
	t.Cleanup(func() {
		_ = os.Chmod(filepath.Join(final, "nested"), 0o755)
		_ = os.Chmod(final, 0o755)
		_ = os.Chmod(nested, 0o755)
		_ = os.Chmod(staging, 0o755)
	})
	oldRename := installRename
	t.Cleanup(func() { installRename = oldRename })
	calls := 0
	installRename = func(from, to string) error {
		calls++
		if calls == 1 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
		}
		return oldRename(from, to)
	}

	if err := publishStaged(staging, final, installRename); err != nil {
		t.Fatal(err)
	}

	if calls < 2 {
		t.Fatalf("cross-device publication did not fall back: %d calls", calls)
	}
	assertMode(t, final, 0o555)
	assertMode(t, filepath.Join(final, "nested"), 0o555)
	assertMode(t, filepath.Join(final, "root.txt"), 0o644)
	assertMode(t, filepath.Join(final, "nested", "nested.txt"), 0o644)
	if got, err := os.ReadFile(filepath.Join(final, "root.txt")); err != nil || string(got) != "root bytes" {
		t.Fatalf("root file = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(final, "nested", "nested.txt")); err != nil || string(got) != "nested bytes" {
		t.Fatalf("nested file = %q, %v", got, err)
	}
}

func TestCopyTreeOverwritesExistingFilesInFull(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "f"), []byte("a much longer old body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "f"))
	if err != nil || string(got) != "new" {
		t.Fatalf("overwritten file = %q, %v", got, err)
	}
}
