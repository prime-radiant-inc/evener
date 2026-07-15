package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/identifier"
)

func TestNonProjectHash_Deterministic(t *testing.T) {
	h1 := hexHash("https://github.com/example/repo.git")
	h2 := hexHash("https://github.com/example/repo.git")
	if h1 != h2 {
		t.Fatalf("same input produced different hashes: %q vs %q", h1, h2)
	}
}

func TestNonProjectHash_DifferentInputs(t *testing.T) {
	h1 := hexHash("https://github.com/example/repo.git")
	h2 := hexHash("https://github.com/other/repo.git")
	if h1 == h2 {
		t.Fatalf("different inputs produced same hash: %q", h1)
	}
}

func TestNonProjectHash_Length(t *testing.T) {
	h := hexHash("any-string")
	if len(h) != 16 {
		t.Fatalf("expected 16-char hex hash, got %d chars: %q", len(h), h)
	}
}

func TestRuntimeDir_ProjectIdentity(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)

	workDir := t.TempDir()
	wantProject, err := identifier.ResolveProject(workDir)
	if err != nil {
		t.Fatal(err)
	}
	gotProject, got, err := RuntimeDir(workDir, "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "serf", "projects", wantProject.ID)
	if gotProject != wantProject || got != want {
		t.Fatalf("RuntimeDir project/path = %#v, %q; want project and %q", gotProject, got, want)
	}
}

func TestRuntimeDir_NoGit(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)

	workDir := t.TempDir()
	project, got, err := RuntimeDir(workDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.CanonicalPath == "" || got != filepath.Join(xdg, "serf", "projects", project.ID) {
		t.Fatalf("RuntimeDir project/path = %#v, %q", project, got)
	}
}

func TestRuntimeDir_NonexistentPathReturnsError(t *testing.T) {
	if _, _, err := RuntimeDir(filepath.Join(t.TempDir(), "missing"), ""); err == nil {
		t.Fatal("RuntimeDir(nonexistent) returned nil error")
	}
}

func TestRuntimeDir_Override(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/should/not/use/this")

	override := "/custom/state/dir"
	project, got, err := RuntimeDir(filepath.Join(t.TempDir(), "missing"), override)
	if err != nil || got != override || project != (identifier.Project{}) {
		t.Fatalf("RuntimeDir with override = %#v, %q, %v; want zero project, %q, nil", project, got, err, override)
	}
}

func TestRuntimeDir_XDGDefault(t *testing.T) {
	// Unset XDG_STATE_HOME to test default.
	t.Setenv("XDG_STATE_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	_, got, err := RuntimeDir(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
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
	want := filepath.Join(home, ".cache", "serf")
	if got != want {
		t.Fatalf("CacheDir default:\n  got  %q\n  want %q", got, want)
	}
}
