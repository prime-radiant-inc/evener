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

// swapInFIFOAfterCheck replays the substitution #4847 describes without a
// timing race: the pre-open check reports a regular file while the path itself
// is a writerless FIFO by the time the open happens. Only the metadata check is
// substituted; the open and every later step are the real ones.
func swapInFIFOAfterCheck(t *testing.T) {
	t.Helper()
	decoy := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(decoy, []byte("gitdir: ../elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	decoyInfo, err := os.Lstat(decoy)
	if err != nil {
		t.Fatal(err)
	}
	restore := entryLstat
	entryLstat = func(string) (os.FileInfo, error) { return decoyInfo, nil }
	t.Cleanup(func() { entryLstat = restore })
}

// TestCopyEntryExactRefusesFIFOSwappedInAfterLstat pins the archive copy used
// by cross-filesystem metadata moves: the Lstat says regular, the path is a
// FIFO, and the copy must refuse it instead of blocking on the open.
func TestCopyEntryExactRefusesFIFOSwappedInAfterLstat(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "config")
	mkfifoAt(t, fifo)
	swapInFIFOAfterCheck(t)
	dst := filepath.Join(t.TempDir(), "archived")

	done := make(chan error, 1)
	go func() { done <- copyEntryExact(fifo, dst) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO swapped in after the regular-file check was archived")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("copyEntryExact did not return (a FIFO was opened with a blocking open)")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("the refused archive entry left %s behind: %v", dst, err)
	}
}

// TestUnsafeGitEntryRefusesFIFOSwappedInAfterLstat pins the migration's .git
// pointer read: a FIFO swapped in after the regular-file check must leave the
// store unverified rather than hang the migration.
func TestUnsafeGitEntryRefusesFIFOSwappedInAfterLstat(t *testing.T) {
	entry := filepath.Join(t.TempDir(), ".git")
	mkfifoAt(t, entry)
	swapInFIFOAfterCheck(t)
	m := NewManager(t.TempDir())

	done := make(chan bool, 1)
	go func() { done <- m.unsafeGitEntry(entry) }()
	select {
	case unsafe := <-done:
		if !unsafe {
			t.Fatal("a FIFO named .git was reported as a clean metadata pointer")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("unsafeGitEntry did not return (a FIFO was opened with a blocking open)")
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

	done := make(chan error, 1)
	go func() { done <- copyTree(src, dst) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a plugin source FIFO was copied as content")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("copyTree did not return (a source FIFO was opened with a blocking open)")
	}
}

// TestCopyTreeRefusesSymlinkToFIFOWithoutBlocking pins the dereferencing source
// copy: a companion-file link that resolves to a named pipe must be refused,
// because os.Open on the link would otherwise block on the pipe.
func TestCopyTreeRefusesSymlinkToFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	mkfifoAt(t, filepath.Join(src, "pipe"))
	if err := os.Symlink("pipe", filepath.Join(src, "companion.txt")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copied")

	done := make(chan error, 1)
	go func() { done <- copyTree(src, dst) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a companion link resolving to a FIFO was copied as content")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("copyTree did not return (a linked FIFO was opened with a blocking open)")
	}
	if _, err := os.Stat(filepath.Join(dst, "companion.txt")); !os.IsNotExist(err) {
		t.Fatalf("the refused companion link was materialized: %v", err)
	}
}

// TestReadPrefixRefusesFIFOWithoutBlocking pins the Git-administration probe
// used by source selection: a writerless FIFO named HEAD must make the probe
// decline instead of blocking the install before any copy starts.
func TestReadPrefixRefusesFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mkfifoAt(t, filepath.Join(dir, "HEAD"))

	type read struct{ err error }
	done := make(chan read, 1)
	go func() {
		_, err := readPrefix(filepath.Join(dir, "HEAD"), 4<<10)
		done <- read{err: err}
	}()
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("readPrefix read a FIFO")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("readPrefix did not return (a FIFO was opened with a blocking open)")
	}

	adminDone := make(chan bool, 1)
	go func() { adminDone <- looksLikeGitAdministration(dir) }()
	select {
	case looks := <-adminDone:
		if looks {
			t.Fatal("a FIFO HEAD looked like Git administration")
		}
	case <-time.After(fifoDeadline):
		t.Fatal("looksLikeGitAdministration did not return (a FIFO HEAD was opened with a blocking open)")
	}
}
