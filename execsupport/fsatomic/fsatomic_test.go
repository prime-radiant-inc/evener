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

// Renaming a file over a directory fails after the temp file exists.
func TestWriteRenameFailureLeavesNoTempAndNoPublishedMark(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	err := fsatomic.Write(target, []byte("new"), 0o600)
	if err == nil || errors.Is(err, fsatomic.ErrPublished) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
	if info, statErr := os.Stat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("target = %v, %v; want the original directory", info, statErr)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only the target", entries, readErr)
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

// A failing directory sync is reported as ErrPublished with the new contents
// already visible, and the caller's own sync sees the directory the file lives
// in.
func TestWriteWithDirSyncReportsPublishedWhenTheSyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	var synced string
	err := fsatomic.WriteWithDirSync(path, []byte("new"), 0o600, func(d string) error {
		synced = d
		return syscall.EIO
	})
	if !errors.Is(err, fsatomic.ErrPublished) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("err = %v, want ErrPublished wrapping the sync error", err)
	}
	if synced != dir {
		t.Fatalf("synced %q, want %q", synced, dir)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Fatalf("contents = %q, want the published new contents", got)
	}
}

func TestWriteWithoutSyncPublishesContentsAtTheRequestedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.WriteWithoutSync(path, []byte("new"), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("contents = %q, %v; want new", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v; want 0640", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only the cache file", entries, err)
	}
}

// A symlink planted at the old fixed temp name must not redirect the write.
func TestWriteWithoutSyncDoesNotFollowPlantedTempSymlink(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cache.json")
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := fsatomic.WriteWithoutSync(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "untouched" {
		t.Fatalf("symlink target was written: %q", got)
	}
}

func TestWriteWithoutSyncMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "cache.json")
	if err := fsatomic.WriteWithoutSync(path, []byte("x"), 0o600); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}
