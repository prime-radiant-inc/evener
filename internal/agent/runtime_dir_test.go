package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectHash_Deterministic(t *testing.T) {
	h1 := projectHash("https://github.com/example/repo.git")
	h2 := projectHash("https://github.com/example/repo.git")
	if h1 != h2 {
		t.Fatalf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

func TestProjectHash_DifferentInputs(t *testing.T) {
	h1 := projectHash("https://github.com/example/repo.git")
	h2 := projectHash("https://github.com/other/repo.git")
	if h1 == h2 {
		t.Fatalf("different inputs produced same hash: %q", h1)
	}
}

func TestProjectHash_Length(t *testing.T) {
	h := projectHash("any-string")
	if len(h) != 16 {
		t.Fatalf("expected 16-char hex hash, got %d chars: %q", len(h), h)
	}
}

func TestRuntimeDir_GitOrigin(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)

	origin := "https://github.com/example/repo.git"
	got := RuntimeDir(origin, "/some/work/dir", "")
	want := filepath.Join(xdg, "serf", "projects", projectHash(origin))
	if got != want {
		t.Fatalf("RuntimeDir with origin:\n  got  %q\n  want %q", got, want)
	}
}

func TestRuntimeDir_NoGit(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)

	workDir := "/home/user/my-project"
	got := RuntimeDir("", workDir, "")
	want := filepath.Join(xdg, "serf", "projects", projectHash(workDir))
	if got != want {
		t.Fatalf("RuntimeDir without origin:\n  got  %q\n  want %q", got, want)
	}
}

func TestRuntimeDir_Override(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/should/not/use/this")

	override := "/custom/state/dir"
	got := RuntimeDir("https://github.com/example/repo.git", "/some/dir", override)
	if got != override {
		t.Fatalf("RuntimeDir with override:\n  got  %q\n  want %q", got, override)
	}
}

func TestRuntimeDir_XDGDefault(t *testing.T) {
	// Unset XDG_STATE_HOME to test default.
	t.Setenv("XDG_STATE_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	got := RuntimeDir("", "/some/dir", "")
	wantPrefix := filepath.Join(home, ".local", "state", "serf", "projects")
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("RuntimeDir XDG default:\n  got  %q\n  want prefix %q", got, wantPrefix)
	}
}

func TestCacheDir_WithXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)

	got := CacheDir()
	want := filepath.Join(xdg, "serf")
	if got != want {
		t.Fatalf("CacheDir with XDG:\n  got  %q\n  want %q", got, want)
	}
}

func TestCacheDir_Default(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	got := CacheDir()
	want := filepath.Join(home, ".local", "cache", "serf")
	if got != want {
		t.Fatalf("CacheDir default:\n  got  %q\n  want %q", got, want)
	}
}
