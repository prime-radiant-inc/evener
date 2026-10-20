package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedMetadataMigrationMovesOnlyGitEntries(t *testing.T) {
	root := t.TempDir()
	m := NewManager(root)
	cache := m.cacheDir()
	rev := filepath.Join(cache, "acme", "widget", "aaa")
	writeTestFile(t, filepath.Join(rev, ".git", "config"), "git-config-bytes")
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
	writeTestFile(t, filepath.Join(cache, "acme", "widget", ".staging.clone", ".git", "config"), "legacy")
	writeTestFile(t, filepath.Join(m.bundledDir(), ".stage-x", ".git", "config"), "bundled stage")
	writeTestFile(t, filepath.Join(m.bundledDir(), "name-0123456789abcdef.conflict", ".git", "config"), "conflict")

	m.ensureManagedContentMigrated(context.Background())
	if m.managedContentClean() {
		t.Fatal("a store with an ancestor pointer was reported clean")
	}
	if _, err := os.Stat(filepath.Join(rev, ".git")); !os.IsNotExist(err) {
		t.Fatalf("named .git directory survived: %v", err)
	}
	archived, err := os.ReadFile(filepath.Join(root, managedGitArchiveDirName, "cache", "acme", "widget", "aaa", ".git", "config"))
	if err != nil || string(archived) != "git-config-bytes" {
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
		writeTestFile(t, filepath.Join(m.cacheDir(), "acme", "widget", rev, ".git", "config"), rev)
	}
	oldMove := migrationMove
	t.Cleanup(func() { migrationMove = oldMove })
	failed := false
	migrationMove = func(from, to string) error {
		if !failed {
			failed = true
			return errors.New("interrupted")
		}
		return oldMove(from, to)
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
	m.ensureManagedContentMigrated(context.Background())
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
	body := []byte("gitdir: ../metadata\n")
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
	writeTestFile(t, filepath.Join(m.cacheDir(), "acme", "widget", "aaa", ".git", "config"), "token")
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
