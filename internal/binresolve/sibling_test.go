package binresolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrefersExplicitPath(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "custom-hub")
	writeExecutable(t, explicit)
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)

	got, err := Resolve("serf-hub", explicit, filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return "", errors.New("should not search PATH")
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != explicit {
		t.Fatalf("got %q, want %q", got, explicit)
	}
}

func TestResolveRejectsNonExecutableExplicit(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(explicit, []byte("noop"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("serf-hub", explicit, "", nil)
	if err == nil {
		t.Fatal("Resolve returned nil error for non-executable explicit path")
	}
}

func TestResolvePrefersSiblingBeforePath(t *testing.T) {
	dir := t.TempDir()
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)
	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)

	got, err := Resolve("serf-hub", "", filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != sibling {
		t.Fatalf("got %q, want %q", got, sibling)
	}
}

func TestResolveFallsBackToPath(t *testing.T) {
	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)

	got, err := Resolve("serf-hub", "", filepath.Join(t.TempDir(), "serf-tui"), func(string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != pathHub {
		t.Fatalf("got %q, want %q", got, pathHub)
	}
}

func TestResolveSurfacesLookPathErrorWhenNothingFound(t *testing.T) {
	_, err := Resolve("serf-hub", "", filepath.Join(t.TempDir(), "serf-tui"), func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if err == nil {
		t.Fatal("Resolve returned nil error when no candidate exists")
	}
}

func TestResolveResolvesRelativeCurrentExecutable(t *testing.T) {
	// Reproduces the original kata: a binary launched as "./serf-tui"
	// must still locate a sibling by an absolute path, otherwise
	// exec.Command will reject it with exec.ErrDot.
	dir := t.TempDir()
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prevWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	got, err := Resolve("serf-hub", "", "./serf-tui", func(string) (string, error) {
		return "", errors.New("should not search PATH")
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("got %q, want absolute path", got)
	}
	if got != sibling {
		// EvalSymlinks may canonicalise the temp dir
		// (e.g. /var -> /private/var on macOS).
		gotResolved, _ := filepath.EvalSymlinks(got)
		siblingResolved, _ := filepath.EvalSymlinks(sibling)
		if gotResolved != siblingResolved {
			t.Fatalf("got %q, want %q (resolved %q vs %q)", got, sibling, gotResolved, siblingResolved)
		}
	}
}

func TestResolveFollowsSymlinkedExecutable(t *testing.T) {
	// /usr/local/bin/serf-tui -> /opt/serf/serf-tui style layout: the
	// sibling lives next to the real binary, not the symlink.
	realDir := t.TempDir()
	realTUI := filepath.Join(realDir, "serf-tui")
	writeExecutable(t, realTUI)
	sibling := filepath.Join(realDir, "serf-hub")
	writeExecutable(t, sibling)

	linkDir := t.TempDir()
	linkTUI := filepath.Join(linkDir, "serf-tui")
	if err := os.Symlink(realTUI, linkTUI); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got, err := Resolve("serf-hub", "", linkTUI, func(string) (string, error) {
		return "", errors.New("should not search PATH")
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	gotResolved, _ := filepath.EvalSymlinks(got)
	siblingResolved, _ := filepath.EvalSymlinks(sibling)
	if gotResolved != siblingResolved {
		t.Fatalf("got %q (resolved %q), want sibling at %q (resolved %q)", got, gotResolved, sibling, siblingResolved)
	}
}

func TestResolveMissingSiblingFallsThroughToPath(t *testing.T) {
	// No sibling exists alongside currentExecutable; Resolve should
	// fall through to the PATH lookup hook.
	dir := t.TempDir()
	tuiPath := filepath.Join(dir, "serf-tui")
	writeExecutable(t, tuiPath)
	// Deliberately do NOT create a sibling "serf-hub" next to it.

	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)
	got, err := Resolve("serf-hub", "", tuiPath, func(name string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != pathHub {
		t.Fatalf("got %q, want %q", got, pathHub)
	}
}

func TestSiblingDirHandlesEmptyAndMissing(t *testing.T) {
	if _, ok := SiblingDir(""); ok {
		t.Fatal("SiblingDir(\"\") returned ok=true")
	}
	if _, ok := SiblingDir("   "); ok {
		t.Fatal("SiblingDir whitespace returned ok=true")
	}
	// A path that does not exist still yields a usable directory (Abs
	// succeeds; EvalSymlinks is best-effort).
	dir, ok := SiblingDir(filepath.Join(t.TempDir(), "nonexistent", "serf-tui"))
	if !ok {
		t.Fatal("SiblingDir(missing) returned ok=false")
	}
	if !filepath.IsAbs(dir) {
		t.Fatalf("SiblingDir returned non-absolute %q", dir)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
