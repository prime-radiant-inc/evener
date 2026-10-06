//go:build linux || darwin

package execenv

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestConfinedFileOperations(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := NewConfinedFileEnvironment(root, "memory/projects/fixture-project")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if _, err := os.Stat(filepath.Join(root, "memory", "projects", "fixture-project")); err != nil {
		t.Fatal(err)
	}
	if _, err := env.WriteFile("data.txt", "opaque-data"); err != nil {
		t.Fatal(err)
	}
	data, err := env.ReadFileRaw("data.txt")
	if err != nil || string(data) != "opaque-data" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

// OpenExisting reports an absent tail without creating it, refuses a symlink
// at the tail, and opens the directory once it exists.
func TestConfinedFileRootOpenExisting(t *testing.T) {
	t.Parallel()
	host, outside := t.TempDir(), t.TempDir()
	root, err := NewConfinedFileRoot(host, "memory/sessions/fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if env, err := root.OpenExisting(nil); !errors.Is(err, fs.ErrNotExist) || env != nil {
		t.Fatalf("absent tail env=%v err=%v", env, err)
	}
	if _, err := os.Lstat(filepath.Join(host, "memory")); !os.IsNotExist(err) {
		t.Fatalf("OpenExisting created the tail: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, "memory", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(host, "memory", "sessions", "fixture")); err != nil {
		t.Fatal(err)
	}
	if env, err := root.OpenExisting(nil); err == nil || errors.Is(err, fs.ErrNotExist) {
		if env != nil {
			env.Cleanup()
		}
		t.Fatalf("symlinked tail err=%v", err)
	}
	if err := os.Remove(filepath.Join(host, "memory", "sessions", "fixture")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(host, "memory", "sessions", "fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := root.OpenExisting(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if _, err := env.WriteFile("page.md", "opaque-page"); err != nil {
		t.Fatal(err)
	}
	if again, err := root.OpenExisting(env); err != nil || again != env {
		t.Fatalf("unchanged tail reopened env=%v err=%v", again, err)
	}
}

func TestOpenConfinedFile(t *testing.T) {
	t.Parallel()
	host, outside := t.TempDir(), t.TempDir()
	env, err := NewConfinedFileEnvironment(host, "memory/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	root := filepath.Join(host, "memory", "sessions")
	if err := os.MkdirAll(filepath.Join(root, "scope"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scope", "page.md"), []byte("opaque-page"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := env.OpenConfinedFile("scope/page.md")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(data) != "opaque-page" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("opaque-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "scope", "leaf.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"scope/leaf.md", "linked/secret", "scope"} {
		if f, err := env.OpenConfinedFile(path); err == nil {
			_ = f.Close()
			t.Fatalf("opened %s", path)
		}
	}
	plain := NewLocalExecutionEnvironment(host)
	defer plain.Cleanup()
	if f, err := plain.OpenConfinedFile(filepath.Join(root, "scope", "page.md")); err == nil {
		_ = f.Close()
		t.Fatal("an unconfined environment opened a confined file")
	}
}

// Catches opening a scope root by path after its authority was established.
func TestConfinedFileRootSwap(t *testing.T) {
	t.Parallel()
	host, outside := t.TempDir(), t.TempDir()
	env, err := NewConfinedFileEnvironment(host, "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	root := filepath.Join(host, "memory", "personal")
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := env.WriteFile("sentinel.txt", "opaque-safe"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "sentinel.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root+"-original", "sentinel.txt"))
	if err != nil || string(got) != "opaque-safe" {
		t.Fatalf("original=%q err=%v", got, err)
	}
}

func TestConfinedFileRecoveryCapturedHost(t *testing.T) {
	t.Parallel()
	parent, outside := t.TempDir(), t.TempDir()
	host := filepath.Join(parent, "host")
	root, err := NewConfinedFileRoot(host, "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	env, err := root.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if _, err := env.WriteFile("data", "opaque-original-721"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(host, host+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, host); err != nil {
		t.Fatal(err)
	}
	same, err := root.Open(env)
	if err != nil || same != env {
		t.Fatalf("unchanged captured root=%p want=%p err=%v", same, env, err)
	}
	scope := filepath.Join(host+"-original", "memory", "personal")
	if err := os.Rename(scope, scope+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scope, "data"), []byte("opaque-recovered-722"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := root.Open(env)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Cleanup()
	data, err := recovered.ReadFileRaw("data")
	if err != nil || string(data) != "opaque-recovered-722" || recovered == env {
		t.Fatalf("recovered=%q err=%v same=%t", data, err, recovered == env)
	}
	if _, err := recovered.WriteFile("safe", "opaque-safe-723"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "memory")); !os.IsNotExist(err) {
		t.Fatalf("host replacement gained authority: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(scope, "safe")); err != nil || string(got) != "opaque-safe-723" {
		t.Fatalf("captured host write=%q err=%v", got, err)
	}
	root.Close()
	if _, err := root.Open(recovered); err == nil {
		t.Fatal("closed authority reopened")
	}
}

func TestConfinedFileRetirementDrainsAndPreserves(t *testing.T) {
	t.Parallel()
	host := t.TempDir()
	env, err := NewConfinedFileEnvironment(host, "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.WriteFile("data", "opaque-retained-73"); err != nil {
		t.Fatal(err)
	}
	layer := env.sandbox()
	env.Cleanup()
	env.Cleanup()
	if !layer.retired.Load() || layer.closed.Load() {
		t.Fatal("held layer was not retired safely")
	}
	data, err := layer.readFile("read", filepath.Join(host, "memory", "personal", "data"))
	if err != nil || string(data) != "opaque-retained-73" {
		t.Fatalf("held bytes=%q err=%v", data, err)
	}
	layer.release()
	if !layer.closed.Load() || len(layer.rootFds) != 0 {
		t.Fatal("drained layer kept root fds")
	}
	got, err := os.ReadFile(filepath.Join(host, "memory", "personal", "data"))
	if err != nil || string(got) != "opaque-retained-73" {
		t.Fatalf("preserved=%q err=%v", got, err)
	}
}

func TestConfinedFileAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ host, relative string }{{"", "memory/personal"}, {"relative", "memory/personal"}, {t.TempDir(), ""}, {t.TempDir(), "../escape"}, {t.TempDir(), t.TempDir()}} {
		if env, err := NewConfinedFileEnvironment(tc.host, tc.relative); err == nil {
			env.Cleanup()
			t.Fatalf("accepted host=%q relative=%q", tc.host, tc.relative)
		}
	}
	host, outside := t.TempDir(), t.TempDir()
	env, err := NewConfinedFileEnvironment(host, "memory/personal")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if env.Wrapper != nil || env.SessionScratchDir() != "" {
		t.Fatal("file-only environment has shell/scratch grants")
	}
	if _, err := os.Stat(filepath.Join(host, "memory", "personal", "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("created automatic index: %v", err)
	}
	if _, err := env.WriteFile(filepath.Join(outside, "data"), "bad"); err == nil {
		t.Fatal("outside write accepted")
	}
	if _, err := env.ReadFileRaw(filepath.Join(outside, "data")); err == nil {
		t.Fatal("outside read accepted")
	}
}
