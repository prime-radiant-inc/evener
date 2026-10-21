package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeGitAdministration lays out the minimum a real .git directory holds for
// looksLikeGitAdministration to classify it as Git administration.
func writeGitAdministration(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(dir, "config"), "[core]\n\trepositoryformatversion = 0\n")
	if err := os.MkdirAll(filepath.Join(dir, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMetadataMigrationMovesOnlyGitEntries(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	cache := m.cacheDir()
	rev := filepath.Join(cache, "acme", "widget", "aaa")
	writeGitAdministration(t, filepath.Join(rev, ".git"))
	writeTestFile(t, filepath.Join(rev, "README.md"), "plugin")
	writeTestFile(t, filepath.Join(rev, "docs", "guide.md"), "guide")
	if err := os.Symlink("docs", filepath.Join(rev, "docs-link")); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	writeTestFile(t, filepath.Join(external, "private.txt"), "user data")
	if err := os.MkdirAll(filepath.Join(rev, "linked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(rev, "linked", ".git")); err != nil {
		t.Fatal(err)
	}

	// A pointer into an ordinary directory: moving that target would relocate
	// content, so the pointer stays and the store remains unverified.
	unsafeRev := filepath.Join(cache, "acme", "widget", "bbb")
	writeTestFile(t, filepath.Join(unsafeRev, ".git"), "gitdir: ../metadata\n")
	writeTestFile(t, filepath.Join(cache, "acme", "widget", "metadata", "object"), "metadata")
	symlinkUnsafeRev := filepath.Join(cache, "acme", "widget", "ccc")
	if err := os.MkdirAll(symlinkUnsafeRev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../metadata", filepath.Join(symlinkUnsafeRev, ".git")); err != nil {
		t.Fatal(err)
	}

	// The inventory covers legacy staging and every bundled slot, not just
	// registry-named revisions.
	writeGitAdministration(t, filepath.Join(cache, "acme", "widget", ".staging.clone", ".git"))
	writeGitAdministration(t, filepath.Join(m.bundledDir(), ".stage-x", ".git"))
	writeGitAdministration(t, filepath.Join(m.bundledDir(), "name-0123456789abcdef.conflict", ".git"))

	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("a store with an ancestor pointer was reported clean")
	}
	if _, err := os.Stat(filepath.Join(rev, ".git")); !os.IsNotExist(err) {
		t.Fatalf("named .git directory survived: %v", err)
	}
	archived, err := os.ReadFile(filepath.Join(root, managedGitArchiveDirName, "cache", "acme", "widget", "aaa", ".git", "HEAD"))
	if err != nil || string(archived) != "ref: refs/heads/main\n" {
		t.Fatalf("archived metadata = %q, %v", archived, err)
	}
	if body, err := os.ReadFile(filepath.Join(external, "private.txt")); err != nil || string(body) != "user data" {
		t.Fatalf("external symlink target was disturbed: %q, %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(rev, "docs", "guide.md")); err != nil || string(body) != "guide" {
		t.Fatalf("ordinary symlink target was disturbed: %q, %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(cache, "acme", "widget", "metadata", "object")); err != nil || string(body) != "metadata" {
		t.Fatalf("pointer target was moved: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(unsafeRev, ".git")); err != nil {
		t.Fatalf("ancestor-pointer .git was moved: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(symlinkUnsafeRev, ".git")); err != nil {
		t.Fatalf("managed-content .git symlink was moved: %v", err)
	}
	for _, gone := range []string{
		filepath.Join(cache, "acme", "widget", ".staging.clone", ".git"),
		filepath.Join(m.bundledDir(), ".stage-x", ".git"),
		filepath.Join(m.bundledDir(), "name-0123456789abcdef.conflict", ".git"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("legacy metadata %s survived: %v", gone, err)
		}
	}

	if err := os.Remove(filepath.Join(unsafeRev, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(symlinkUnsafeRev, ".git")); err != nil {
		t.Fatal(err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("retry after removing the unsafe pointer did not complete the audit")
	}
}

func TestManagedMetadataMigrationRetriesAfterInterruption(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	for _, rev := range []string{"aaa", "bbb"} {
		writeGitAdministration(t, filepath.Join(m.cacheDir(), "acme", "widget", rev, ".git"))
	}
	oldMove := migrationMove
	t.Cleanup(func() { migrationMove = oldMove })
	failed := false
	migrationMove = func(from, to, marker string) error {
		if !failed {
			failed = true
			return errors.New("interrupted")
		}
		return oldMove(from, to, marker)
	}
	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("interrupted migration claimed clean")
	}
	if !failed {
		t.Fatal("migration did not exercise the injected interruption")
	}
	remaining := 0
	for _, rev := range []string{"aaa", "bbb"} {
		if _, err := os.Stat(filepath.Join(m.cacheDir(), "acme", "widget", rev, ".git")); err == nil {
			remaining++
		}
	}
	if remaining == 0 {
		t.Fatal("interrupted migration removed both entries despite the failure")
	}
	if _, err := m.Gc(context.Background()); err != nil {
		t.Fatalf("an ordinary GC did not recover after the transient migration failure: %v", err)
	}
	if !m.managedContentClean() {
		t.Fatal("retry did not complete after the interruption cleared")
	}
	for _, rev := range []string{"aaa", "bbb"} {
		if _, err := os.Stat(filepath.Join(m.cacheDir(), "acme", "widget", rev, ".git")); !os.IsNotExist(err) {
			t.Fatalf("retry left %s metadata: %v", rev, err)
		}
	}
}

func TestManagedMetadataMigrationCleanStoreIsStable(t *testing.T) {
	m := NewManager(t.TempDir())
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("empty store did not complete its audit")
	}
	writeTestFile(t, filepath.Join(m.cacheDir(), "acme", "widget", "aaa", "README.md"), "content")
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("ordinary content invalidated the clean state")
	}
	if _, err := os.Stat(filepath.Join(m.Root, storeFormatFileName)); err != nil {
		t.Fatalf("store format marker missing: %v", err)
	}
}

func TestManagedMetadataMigrationLeavesAnOversizedPointer(t *testing.T) {
	m := NewManager(t.TempDir())
	pointer := filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")
	if err := os.MkdirAll(filepath.Dir(pointer), 0o755); err != nil {
		t.Fatal(err)
	}
	// An external target would otherwise be moved (and the store called clean).
	// The size bound is what leaves it in place here.
	body := []byte("gitdir: /outside/target\n")
	if err := os.WriteFile(pointer, append(body, make([]byte, 1<<20)...), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pointer)
	if err != nil {
		t.Fatal(err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("an oversized pointer was classified as clean")
	}
	after, err := os.Stat(pointer)
	if err != nil || after.Size() != info.Size() {
		t.Fatalf("oversized pointer changed: %v, %d -> %d", err, info.Size(), after.Size())
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("retry after removing the oversized pointer did not complete")
	}
}

func TestManagedMetadataMigrationCanceledLeavesStoreUsable(t *testing.T) {
	m := NewManager(t.TempDir())
	writeGitAdministration(t, filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.ensureManagedContentMigrated(ctx)
	if m.managedContentClean() {
		t.Fatal("canceled migration claimed clean")
	}
	if _, err := os.Stat(filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")); err != nil {
		t.Fatalf("canceled migration moved metadata: %v", err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("retry after cancellation did not complete")
	}
}

func TestManagedMetadataMigrationResolvesPointerThroughASymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "store")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	m := NewManager(linkedRoot)
	metadata := filepath.Join(realRoot, "cache", "acme", "widget", "metadata")
	writeTestFile(t, filepath.Join(metadata, "object"), "metadata")
	pointer := filepath.Join(linkedRoot, "cache", "acme", "widget", "aaa", ".git")
	if err := os.MkdirAll(filepath.Dir(pointer), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "gitdir: " + metadata + "\n"
	if err := os.WriteFile(pointer, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPointer := filepath.Join(linkedRoot, "cache", "acme", "widget", "ccc", ".git")
	if err := os.MkdirAll(filepath.Dir(linkPointer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(metadata, linkPointer); err != nil {
		t.Fatal(err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("a resolved-spelling pointer into mixed managed content was called clean")
	}
	if _, err := os.Lstat(pointer); err != nil {
		t.Fatalf("resolved-spelling pointer was moved: %v", err)
	}
	if _, err := os.Lstat(linkPointer); err != nil {
		t.Fatalf("resolved-spelling .git symlink was moved: %v", err)
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linkPointer); err != nil {
		t.Fatal(err)
	}
	m.ensureManagedContentMigrated(context.Background())
	if !m.managedContentClean() {
		t.Fatal("retry after removing the resolved-spelling pointer did not complete")
	}
}

func TestManagedMetadataMigrationPreservesGitNamespaceDirectories(t *testing.T) {
	m := NewManager(t.TempDir())
	marketplace := filepath.Join(m.cacheDir(), ".git", "acme", "rev")
	plugin := filepath.Join(m.cacheDir(), "acme", ".git", "rev")
	writeTestFile(t, filepath.Join(marketplace, "README.md"), "marketplace content")
	writeTestFile(t, filepath.Join(plugin, "README.md"), "plugin content")

	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("a store with namespace directories named .git was called clean")
	}
	for path, want := range map[string]string{
		filepath.Join(marketplace, "README.md"): "marketplace content",
		filepath.Join(plugin, "README.md"):      "plugin content",
	} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("namespace content %s = %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(m.Root, managedGitArchiveDirName, "cache", ".git")); !os.IsNotExist(err) {
		t.Fatalf("a cache namespace directory was archived: %v", err)
	}
}

func TestManagedMetadataMigrationPreservesNestedGitNamespace(t *testing.T) {
	t.Run("nested legacy namespace", func(t *testing.T) {
		m := NewManager(t.TempDir())
		namespace := filepath.Join(m.cacheDir(), "a", "b", ".git")
		revOne := filepath.Join(namespace, "rev-one")
		revTwo := filepath.Join(namespace, "rev-two")
		writeTestFile(t, filepath.Join(revOne, ".claude-plugin", "plugin.json"), `{"name":"widget"}`)
		writeTestFile(t, filepath.Join(revOne, "companion.bin"), "rev-one companion")
		writeTestFile(t, filepath.Join(revTwo, ".claude-plugin", "plugin.json"), `{"name":"widget","version":"old"}`)
		writeTestFile(t, filepath.Join(revTwo, "companion.bin"), "rev-two companion")
		one := snapshotMigrationTree(t, revOne)
		two := snapshotMigrationTree(t, revTwo)
		if err := SaveRegistry(m.registryPath(), Registry{Plugins: map[string][]InstallEntry{
			"widget@a/b": {{
				InstallPath: revOne,
				Version:     "1.0.0",
				Enabled:     true,
				Source:      Source{Kind: SourceDirectory, Path: "/source/widget"},
			}},
		}}); err != nil {
			t.Fatal(err)
		}
		registryBefore, err := os.ReadFile(m.registryPath())
		if err != nil {
			t.Fatal(err)
		}

		m.ensureManagedContentMigrated(context.Background())

		if _, err := os.Stat(namespace); err != nil {
			t.Fatalf("nested .git namespace was moved: %v", err)
		}
		assertMigrationTree(t, one)
		assertMigrationTree(t, two)
		reg, err := LoadRegistry(m.registryPath())
		if err != nil {
			t.Fatal(err)
		}
		if got := reg.Plugins["widget@a/b"]; len(got) != 1 || got[0].InstallPath != revOne {
			t.Fatalf("migration changed the recorded install path: %+v", reg)
		}
		if after, err := os.ReadFile(m.registryPath()); err != nil || !bytes.Equal(registryBefore, after) {
			t.Fatalf("migration rewrote the registry: %v", err)
		}
		if m.managedContentClean() {
			t.Fatal("a preserved nested namespace was reported clean")
		}
		if _, err := os.Stat(m.managedGitArchiveDir("cache", "a/b/.git")); !os.IsNotExist(err) {
			t.Fatalf("the nested namespace was archived: %v", err)
		}
	})

	t.Run("real git administration", func(t *testing.T) {
		m := NewManager(t.TempDir())
		rev := filepath.Join(m.cacheDir(), "acme", "widget", "admin-rev")
		makeGitRepo(t, rev, "README.md", "x")

		m.ensureManagedContentMigrated(context.Background())

		if _, err := os.Stat(filepath.Join(rev, ".git")); !os.IsNotExist(err) {
			t.Fatalf("real Git administration was preserved: %v", err)
		}
		if _, err := os.Stat(m.managedGitArchiveDir("cache", "acme/widget/admin-rev/.git")); err != nil {
			t.Fatalf("real Git administration was not archived: %v", err)
		}
		if !m.managedContentClean() {
			t.Fatal("a store with only real administration was not cleaned")
		}
	})
}

func TestManagedMetadataMigrationFinishesInterruptedCrossDeviceArchive(t *testing.T) {
	m := NewManager(t.TempDir())
	source := filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")
	// An interrupted old cross-device archive: the copy is complete, but only a
	// partial source tree remains where the removal stopped.
	if err := os.MkdirAll(filepath.Join(source, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git")
	writeGitAdministration(t, base)
	writeTestFile(t, filepath.Join(base, "marker.txt"), "archived bytes")
	pending := m.managedGitJournalPath("cache", "acme/widget/aaa/.git")
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	m.ensureManagedContentMigrated(context.Background())

	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("interrupted source metadata survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(base, "marker.txt")); err != nil || string(got) != "archived bytes" {
		t.Fatalf("archive bytes = %q, %v", got, err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending marker survived: %v", err)
	}
	if !m.managedContentClean() {
		t.Fatal("recovered store was not reported clean")
	}
}

func TestManagedMetadataMigrationDoesNotTrustAPartialArchiveCopy(t *testing.T) {
	m := NewManager(t.TempDir())
	source := filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")
	writeGitAdministration(t, source)
	base := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git")
	// A partial destination from an interrupted old copy is not proof of a
	// complete archive, so the move must still archive to a numbered sibling.
	if err := os.MkdirAll(filepath.Join(base, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}

	m.ensureManagedContentMigrated(context.Background())

	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source metadata survived: %v", err)
	}
	numbered := base + "-1"
	if !looksLikeGitAdministration(numbered) {
		t.Fatalf("numbered archive %s is not administration-shaped", numbered)
	}
	if _, err := os.Stat(filepath.Join(base, "objects", "info")); err != nil {
		t.Fatalf("partial archive destination was disturbed: %v", err)
	}
	if !m.managedContentClean() {
		t.Fatal("store was not reported clean")
	}
}

func TestManagedMetadataMigrationKeepsAPartialSourceWithoutAPendingMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	source := filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")
	if err := os.MkdirAll(filepath.Join(source, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git")
	writeGitAdministration(t, base)

	m.ensureManagedContentMigrated(context.Background())

	if _, err := os.Stat(source); err != nil {
		t.Fatalf("partial source was removed without proof of ownership: %v", err)
	}
	if m.managedContentClean() {
		t.Fatal("a preserved partial source was reported clean")
	}
}

func TestManagedMetadataMigrationRemovesStalePendingMarkers(t *testing.T) {
	m := NewManager(t.TempDir())
	missingSource := filepath.Join(m.cacheDir(), "acme", "widget", "gone", ".git")
	base := m.managedGitArchiveDir("cache", "acme/widget/gone/.git")
	writeGitAdministration(t, base)
	pending := m.managedGitJournalPath("cache", "acme/widget/gone/.git")
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte(missingSource), 0o600); err != nil {
		t.Fatal(err)
	}

	m.ensureManagedContentMigrated(context.Background())

	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("stale pending marker survived: %v", err)
	}
	if !m.managedContentClean() {
		t.Fatal("store with a reclaimed marker was not reported clean")
	}
}

func TestManagedMetadataMigrationFindsANumberedArchiveMarker(t *testing.T) {
	m := NewManager(t.TempDir())
	source := filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git")
	if err := os.MkdirAll(filepath.Join(source, "objects", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git")
	numbered := base + "-1"
	writeGitAdministration(t, numbered)
	pending := m.managedGitJournalPath("cache", "acme/widget/aaa/.git")
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	m.ensureManagedContentMigrated(context.Background())

	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("interrupted source metadata survived: %v", err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("numbered archive marker survived: %v", err)
	}
	if !m.managedContentClean() {
		t.Fatal("store with a recovered numbered archive was not reported clean")
	}
}

// writeArchivedRefs writes refs whose names end in the live journal marker
// suffix and the legacy one. Both record a commit id, not a source path, so a
// journal sweep must never treat them as markers.
func writeArchivedRefs(t *testing.T, archive string) (refs []string, commitID string) {
	t.Helper()
	const id = "0123456789abcdef0123456789abcdef01234567"
	refs = []string{
		filepath.Join(archive, "refs", "heads", "topic.pending"),
		filepath.Join(archive, "refs", "heads", "topic.evener-pending"),
	}
	for _, ref := range refs {
		writeTestFile(t, ref, id)
	}
	return refs, id
}

func assertArchivedRefs(t *testing.T, refs []string, commitID, pass string) {
	t.Helper()
	for _, ref := range refs {
		got, err := os.ReadFile(ref)
		if err != nil || string(got) != commitID {
			t.Fatalf("archived ref %s after %s = %q, %v", filepath.Base(ref), pass, got, err)
		}
	}
}

func TestManagedMetadataMigrationKeepsArchivedRefs(t *testing.T) {
	m := NewManager(t.TempDir())
	archive := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git")
	writeGitAdministration(t, archive)
	refs, commitID := writeArchivedRefs(t, archive)

	stateBytes := func() []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(m.Root, storeFormatFileName))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	m.ensureManagedContentMigrated(context.Background())
	assertArchivedRefs(t, refs, commitID, "the first pass")
	if !m.managedContentClean() {
		t.Fatal("store with archived refs was not reported clean")
	}
	first := stateBytes()

	m.ensureManagedContentMigrated(context.Background())
	assertArchivedRefs(t, refs, commitID, "the second pass")
	if !m.managedContentClean() {
		t.Fatal("store with archived refs was not reported clean after the second pass")
	}
	if second := stateBytes(); !bytes.Equal(first, second) {
		t.Fatalf("store state changed on the second pass: %q then %q", first, second)
	}
}

func TestManagedMetadataMigrationKeepsArchivedRefsInNumberedPayload(t *testing.T) {
	m := NewManager(t.TempDir())
	// A numbered archive copy is a payload, not a journal. Its archived ref name
	// ends in the marker suffix but records a commit id, so no pass may treat it
	// as a stale marker or descend into it.
	archive := m.managedGitArchiveDir("cache", "acme/widget/aaa/.git") + "-1"
	writeGitAdministration(t, archive)
	refs, commitID := writeArchivedRefs(t, archive)

	stateBytes := func() []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(m.Root, storeFormatFileName))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	m.ensureManagedContentMigrated(context.Background())
	assertArchivedRefs(t, refs, commitID, "the first pass")
	if !m.managedContentClean() {
		t.Fatal("store with a numbered archive payload was not reported clean")
	}
	first := stateBytes()

	m.ensureManagedContentMigrated(context.Background())
	assertArchivedRefs(t, refs, commitID, "the second pass")
	if !m.managedContentClean() {
		t.Fatal("store with a numbered archive payload was not reported clean after the second pass")
	}
	if second := stateBytes(); !bytes.Equal(first, second) {
		t.Fatalf("store state changed on the second pass: %q then %q", first, second)
	}
}
