//go:build linux || darwin

package execenv

import (
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
