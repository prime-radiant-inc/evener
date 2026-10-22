//go:build linux || darwin

package hostlock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStoragePathResolution(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target", "child")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Keep symlink/.. uncleaned, it names target, not root.
	selected := link + "/../missing/fresh"
	probe := func(file *os.File) (string, bool, error) {
		got, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.Stat(filepath.Dir(target))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(got, want) {
			t.Fatalf("inspected wrong ancestor for %q", selected)
		}
		return "fixture-local", false, nil
	}
	if err := CheckStorage(selected, probe); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "target", "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created missing root: %v", err)
	}
	spaced := filepath.Join(root, " space ")
	if err := os.Mkdir(spaced, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckStorage(spaced, nil); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"file", "fifo", "dangling", "loop", "denied"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(root, kind)
			var want error
			switch kind {
			case "file":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				want = syscall.ENOTDIR
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
				want = syscall.ENOTDIR
			case "dangling":
				if err := os.Symlink("no-target", path); err != nil {
					t.Fatal(err)
				}
				want = os.ErrNotExist
			case "loop":
				if err := os.Symlink(path, path); err != nil {
					t.Fatal(err)
				}
				want = syscall.ELOOP
			case "denied":
				if err := os.Mkdir(path, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0700) })
				want = os.ErrPermission
			}
			for _, suffix := range []string{"", "/", "/child"} {
				if err := CheckStorage(path+suffix, nil); !errors.Is(err, want) {
					t.Fatalf("%s: got %v, want %v", path+suffix, err, want)
				}
			}
		})
	}
}

func TestStorageErrorsAndSelectedPath(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "missing", "root")
	err := CheckStorage(selected, func(*os.File) (string, bool, error) { return "fixture-network", true, nil })
	var refusal *StorageError
	if !errors.As(err, &refusal) || !errors.Is(err, ErrNonlocalStorage) || refusal.Path != selected || refusal.Filesystem != "fixture-network" {
		t.Fatalf("refusal=%v", err)
	}
	err = CheckStorage(selected, func(*os.File) (string, bool, error) { return "", false, syscall.EIO })
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("lost OS error: %v", err)
	}
}

func TestStorageDescriptorRecheck(t *testing.T) {
	// Serial: replace only the external OS classification call.
	saved := filesystemInspector
	t.Cleanup(func() { filesystemInspector = saved })
	path := filepath.Join(t.TempDir(), "hub.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	filesystemInspector = func(file *os.File) (string, bool, error) {
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		return "fixture-network", info.Mode().IsRegular(), nil
	}
	if release, err := AcquireLock(path); !errors.Is(err, ErrNonlocalStorage) {
		if release != nil {
			release()
		}
		t.Fatalf("acquire bypassed descriptor check: %v", err)
	}
	if _, err := IsHeld(path); !errors.Is(err, ErrNonlocalStorage) {
		t.Fatalf("inspection bypassed descriptor check: %v", err)
	}
	redirected := path + "-redirected"
	if err := os.Symlink(path, redirected); err != nil {
		t.Fatal(err)
	}
	if release, err := AcquireLock(redirected); !errors.Is(err, ErrNonlocalStorage) {
		if release != nil {
			release()
		}
		t.Fatalf("redirected descriptor bypassed check: %v", err)
	}
	filesystemInspector = func(*os.File) (string, bool, error) { return "fixture-network", true, nil }
	fresh := filepath.Join(filepath.Dir(path), "fresh.lock")
	if release, err := AcquireLock(fresh); !errors.Is(err, ErrNonlocalStorage) {
		if release != nil {
			release()
		}
		t.Fatalf("actual root bypassed check: %v", err)
	}
	if _, err := os.Lstat(fresh); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("root refusal created lock: %v", err)
	}
	// Refusal must leave no ownership and no repair marker.
	filesystemInspector = saved
	release, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if held, err := IsHeld(path); err != nil || held {
		t.Fatalf("recovery: held=%v err=%v", held, err)
	}
}
