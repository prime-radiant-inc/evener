package fsatomic_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/execsupport/fsatomic"
)

func TestWritePublishesContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("contents = %q, %v; want new", got, err)
	}
}

// A symlink planted where a fixed "<name>.tmp" would have been must not
// redirect the write.
func TestWriteDoesNotFollowPlantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	for _, name := range []string{"state.json.tmp", "state.json.tmp-0"} {
		if err := os.Symlink(victim, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := fsatomic.Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "untouched" {
		t.Fatalf("symlink target was written: %q", got)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("contents = %q, want new", got)
	}
}

func TestWriteFailureLeavesOldFileAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Renaming a file over a directory fails after the temp file exists.
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.Write(target, []byte("new"), 0o600); err == nil {
		t.Fatal("expected rename over a directory to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("directory has %d entries, want only state.json and target", len(entries))
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("unrelated file changed: %q", got)
	}
}

func TestWriteMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.json")
	if err := fsatomic.Write(path, []byte("x"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestSyncUnsupported(t *testing.T) {
	for _, errno := range []error{syscall.ENOSYS, syscall.ENOTSUP, syscall.EINVAL} {
		if !fsatomic.SyncUnsupported(&os.PathError{Op: "sync", Err: errno}) {
			t.Errorf("%v should be unsupported", errno)
		}
	}
	if fsatomic.SyncUnsupported(syscall.EIO) || fsatomic.SyncUnsupported(nil) {
		t.Error("EIO and nil are not unsupported")
	}
}

func TestRemoveDeletesFileAndToleratesMissingOnes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	if err := fsatomic.Remove(path); err != nil {
		t.Fatalf("removing a missing file: %v", err)
	}
	if err := fsatomic.Remove(filepath.Join(dir, "missing-dir", "state.json")); err != nil {
		t.Fatalf("removing under a missing directory: %v", err)
	}
}
