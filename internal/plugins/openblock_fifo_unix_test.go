//go:build unix

package plugins

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fifoDeadline bounds every probe below. The hazards pinned here all live in
// the gap between a metadata check and the open that follows it (#4847). A
// regression that opens the path with a blocking open parks the operation on a
// writerless FIFO forever, so each probe runs in a goroutine and fails this
// tripwire instead of hanging the suite. The shape matches the existing FIFO
// tripwires in agent/command and agent/skill.
const fifoDeadline = 5 * time.Second

// withinFIFODeadline runs call in its own goroutine and fails the test if it
// has not returned within fifoDeadline, naming what blocked.
func withinFIFODeadline[T any](t *testing.T, what string, call func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- call() }()
	select {
	case result := <-done:
		return result
	case <-time.After(fifoDeadline):
		t.Fatalf("%s did not return (a FIFO was opened with a blocking open)", what)
		var zero T
		return zero
	}
}

// mkfifoAt creates a writerless named pipe at path.
func mkfifoAt(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}

// TestCopyRegularFileRefusesFIFOWithoutBlocking pins the one open every archive
// and payload copy funnels through: the caller's metadata check has passed, and
// the path is a writerless FIFO. The copy must refuse it instead of parking on
// the open.
func TestCopyRegularFileRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "config")
	mkfifoAt(t, fifo)
	dst := filepath.Join(t.TempDir(), "copied")

	err := withinFIFODeadline(t, "copyRegularFile(FIFO)", func() error {
		return copyRegularFile(fifo, dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	})
	if err == nil {
		t.Fatal("a copy read a FIFO whose metadata check had said regular file")
	}
	if _, statErr := os.Lstat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("the refused copy left %s behind: %v", dst, statErr)
	}
}

// TestUnsafeGitEntryRefusesFIFOSwappedInAfterLstat pins the migration's .git
// pointer read. The metadata check reported a regular pointer file and the
// entry on disk is a writerless FIFO: the migration must leave the store
// unverified instead of hanging.
func TestUnsafeGitEntryRefusesFIFOSwappedInAfterLstat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	entry := filepath.Join(dir, ".git")
	mkfifoAt(t, entry)
	pointer := filepath.Join(dir, "pointer")
	if err := os.WriteFile(pointer, []byte("gitdir: ../elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checked, err := os.Lstat(pointer)
	if err != nil {
		t.Fatal(err)
	}

	m := NewManager(t.TempDir())
	unsafe := withinFIFODeadline(t, "unsafeGitEntry(FIFO)", func() bool {
		return m.unsafeGitEntryInfo(entry, checked)
	})
	if !unsafe {
		t.Fatal("a FIFO named .git was reported as a clean metadata pointer")
	}
}

// TestCopyTreeRefusesFIFOWithoutBlocking pins the plugin source copy: a source
// directory holding a named pipe must fail the copy, not park the install on
// the pipe's open.
func TestCopyTreeRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "README.md"), "content")
	mkfifoAt(t, filepath.Join(src, "pipe"))
	dst := filepath.Join(t.TempDir(), "copied")

	err := withinFIFODeadline(t, "copyTree(FIFO)", func() error { return copyTree(src, dst) })
	if err == nil {
		t.Fatal("a plugin source FIFO was copied as content")
	}
}

// TestCopyTreeRefusesSymlinkToFIFOWithoutBlocking pins the dereferencing source
// copy: a companion-file link that resolves to a named pipe must be refused,
// because opening the link would otherwise block on the pipe.
func TestCopyTreeRefusesSymlinkToFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	mkfifoAt(t, filepath.Join(src, "pipe"))
	if err := os.Symlink("pipe", filepath.Join(src, "companion.txt")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copied")

	err := withinFIFODeadline(t, "copyTree(linked FIFO)", func() error { return copyTree(src, dst) })
	if err == nil {
		t.Fatal("a companion link resolving to a FIFO was copied as content")
	}
	if _, statErr := os.Stat(filepath.Join(dst, "companion.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused companion link was materialized: %v", statErr)
	}
}

// TestReadPrefixRefusesFIFOWithoutBlocking pins the Git-administration probe
// used by source selection: a writerless FIFO named HEAD must make the probe
// decline instead of blocking the install before any copy starts.
func TestReadPrefixRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	head := filepath.Join(dir, "HEAD")
	mkfifoAt(t, head)

	var data []byte
	err := withinFIFODeadline(t, "readPrefix(FIFO)", func() error {
		var readErr error
		data, readErr = readPrefix(head, 4<<10)
		return readErr
	})
	if err == nil {
		t.Fatalf("readPrefix read a FIFO: %q", data)
	}
	if withinFIFODeadline(t, "looksLikeGitAdministration(FIFO HEAD)", func() bool {
		return looksLikeGitAdministration(dir)
	}) {
		t.Fatal("a FIFO HEAD looked like Git administration")
	}
}

// TestParseCatalogRefusesFIFOWithoutBlocking pins the catalog read. A
// directory-kind marketplace is read in place, so a writerless FIFO at
// .claude-plugin/marketplace.json must fail the parse instead of parking it.
func TestParseCatalogRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mkfifoAt(t, filepath.Join(root, ".claude-plugin", "marketplace.json"))

	err := withinFIFODeadline(t, "ParseCatalog(FIFO)", func() error {
		_, parseErr := ParseCatalog(root)
		return parseErr
	})
	if err == nil {
		t.Fatal("a FIFO marketplace.json parsed as a catalog")
	}
}

// TestAddMarketplaceRefusesFIFOCatalogWithoutBlocking pins the wedge the FIFO
// causes at its widest: AddMarketplace parses the catalog while holding the
// store lock, so a blocking read there parks every later plugin operation.
func TestAddMarketplaceRefusesFIFOCatalogWithoutBlocking(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	mkfifoAt(t, filepath.Join(src, ".claude-plugin", "marketplace.json"))
	m, ctx := newManagerCtx(t)

	err := withinFIFODeadline(t, "AddMarketplace(FIFO catalog)", func() error {
		_, addErr := m.AddMarketplace(ctx, "acme", Source{Kind: SourceDirectory, Path: src})
		return addErr
	})
	if err == nil {
		t.Fatal("a marketplace with a FIFO catalog was registered")
	}
}
