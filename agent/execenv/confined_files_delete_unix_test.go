//go:build linux || darwin

package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/sandbox"
)

func TestConfinedFileDelete(t *testing.T) {
	t.Parallel()
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	root := env.WorkingDirectory()
	for _, name := range []string{"MEMORY.md", "unrelated"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("opaque-retained-376"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range []string{"", "opaque-ordinary-377"} {
		path := filepath.Join(root, "page")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := env.RemoveConfinedFile("page"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("page survived: %v", err)
		}
	}
	for _, path := range []string{"missing", "missing-parent/page"} {
		if err := env.RemoveConfinedFile(path); err != nil {
			t.Fatalf("absent %s: %v", path, err)
		}
	}
	for _, name := range []string{"MEMORY.md", "unrelated"} {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != "opaque-retained-376" {
			t.Fatalf("%s changed: %q %v", name, got, err)
		}
	}
}

// Not parallel: the allocation measurement must exclude sibling test activity.
func TestConfinedFileDeleteSparseUnreadable(t *testing.T) {
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	path := filepath.Join(env.WorkingDirectory(), "sparse")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if _, err := os.Open(path); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("sparse fixture is not unreadable: %v", err)
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = env.RemoveConfinedFile(path)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("delete allocated %d bytes for 1GiB sparse body", allocated)
	} else {
		t.Logf("1GiB sparse deletion allocated %d bytes, uid=%d", allocated, os.Getuid())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("sparse file survived: %v", err)
	}
}

func TestConfinedFileDeleteTypesAndAuthority(t *testing.T) {
	t.Parallel()
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	root, outside := env.WorkingDirectory(), t.TempDir()
	outsideFile := filepath.Join(outside, "data")
	if err := os.WriteFile(outsideFile, []byte("opaque-outside-378"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"empty-dir", "full-dir", "masked", "denied-parent"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"full-dir/data", "masked/data", "denied-parent/data"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("opaque-preserved-379"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "ancestor")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.sbfs.policy.MaskedPaths = []string{filepath.Join(root, "masked")}
	for _, path := range []string{"empty-dir", "full-dir", "fifo"} {
		if err := env.RemoveConfinedFile(path); !errors.Is(err, errNotRegularFile) {
			t.Fatalf("type %s accepted or wrong error: %v", path, err)
		}
		if _, err := os.Lstat(filepath.Join(root, path)); err != nil {
			t.Fatalf("refused entry %s lost: %v", path, err)
		}
	}
	for _, path := range []string{".", "leaf", "ancestor/data", "masked/data", outsideFile, "../outside"} {
		var denied *sandbox.DeniedError
		if err := env.RemoveConfinedFile(path); !errors.As(err, &denied) {
			t.Fatalf("authority %s accepted or wrong error: %v", path, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "leaf")); err != nil {
		t.Fatalf("refused symlink lost: %v", err)
	}
	if os.Getuid() != 0 {
		parent := filepath.Join(root, "denied-parent")
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		err := env.RemoveConfinedFile("denied-parent/data")
		if restoreErr := os.Chmod(parent, 0o700); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("parent permission error lost: %v", err)
		}
	}
	for _, name := range []string{"full-dir/data", "masked/data", "denied-parent/data"} {
		if got, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(got) != "opaque-preserved-379" {
			t.Fatalf("refused %s changed: %q %v", name, got, err)
		}
	}
	if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "opaque-outside-378" {
		t.Fatalf("outside changed: %q %v", got, err)
	}
}

func TestConfinedFileDeleteCapturedRoot(t *testing.T) {
	t.Parallel()
	env, err := NewConfinedFileEnvironment(t.TempDir(), "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	root, outside := env.WorkingDirectory(), t.TempDir()
	for _, dir := range []string{root, outside} {
		if err := os.WriteFile(filepath.Join(dir, "page"), []byte("opaque-root-380"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if err := env.RemoveConfinedFile("page"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root+"-original", "page")); !os.IsNotExist(err) {
		t.Fatalf("captured page survived: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(outside, "page")); err != nil || string(got) != "opaque-root-380" {
		t.Fatalf("replacement changed: %q %v", got, err)
	}
}
