package binresolve

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestResolveSurfacesLookPathErrorWhenNothingFound(t *testing.T) {
	_, err := Resolve("serf-hub", "", filepath.Join(t.TempDir(), "serf-tui"), func(string) (string, error) {
		return "", os.ErrNotExist
	})
	if err == nil {
		t.Fatal("Resolve returned nil error when no candidate exists")
	}
	if !strings.Contains(err.Error(), "serf-hub") {
		t.Fatalf("error %q does not mention binary name", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %q does not wrap os.ErrNotExist", err)
	}
}

func TestResolveResolvesRelativeCurrentExecutable(t *testing.T) {
	// Reproduces the original kata: a binary launched via a relative path
	// must still locate a sibling by an absolute path, otherwise
	// exec.Command will reject it with exec.ErrDot.
	dir := t.TempDir()
	sibling := filepath.Join(dir, "serf-hub")
	writeExecutable(t, sibling)

	// Build a relative currentExecutable path without mutating the process
	// cwd (os.Chdir is process-level state that would be a maintenance trap
	// for any future parallel test). filepath.Rel produces a path relative
	// to the test binary's cwd; filepath.Abs inside SiblingDir expands it
	// back to the correct absolute directory.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rel, err := filepath.Rel(cwd, filepath.Join(dir, "serf-tui"))
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}

	got, err := Resolve("serf-hub", "", rel, func(string) (string, error) {
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

func TestIsExecutable_NonExistent(t *testing.T) {
	// Use the exported function indirectly by testing Resolve with explicit path.
	const explicit = "/nonexistent/path/serf-hub"
	_, err := Resolve("serf-hub", explicit, "", nil)
	if err == nil {
		t.Fatal("expected error for non-existent explicit path")
	}
	// The error must name both the binary and the rejected path so the
	// operator can tell which explicit override failed the check.
	if !strings.Contains(err.Error(), "serf-hub") {
		t.Fatalf("error %q does not mention binary name", err)
	}
	if !strings.Contains(err.Error(), explicit) {
		t.Fatalf("error %q does not mention explicit path %q", err, explicit)
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("error %q does not describe the non-executable rejection", err)
	}
}

func TestIsExecutable_Directory(t *testing.T) {
	dir := t.TempDir()
	// The sibling target "serf-hub" IS a directory: it exists next to the
	// running binary and carries 0o755 (so its execute bits are set), but a
	// directory must never be accepted as an executable. Resolve must
	// reject it and fall through to the PATH lookup hook, which here errors.
	if err := os.Mkdir(filepath.Join(dir, "serf-hub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve("serf-hub", "", filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return "", errors.New("should not reach PATH")
	})
	if err == nil {
		t.Fatal("expected error when sibling is a directory")
	}
}

func TestResolve_SiblingExistsButNotExecutable_FallsThroughToPATH(t *testing.T) {
	dir := t.TempDir()
	// Create a sibling file that exists but is NOT executable.
	sibling := filepath.Join(dir, "serf-hub")
	if err := os.WriteFile(sibling, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a PATH candidate that IS executable.
	pathHub := filepath.Join(t.TempDir(), "serf-hub")
	writeExecutable(t, pathHub)

	got, err := Resolve("serf-hub", "", filepath.Join(dir, "serf-tui"), func(string) (string, error) {
		return pathHub, nil
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != pathHub {
		t.Fatalf("got %q, want %q", got, pathHub)
	}
}

func TestSiblingDir_AbsFailsWithVeryLongPath(t *testing.T) {
	// filepath.Abs on an extremely long path may fail on some platforms.
	// Construct a path that exceeds typical limits (e.g., 4096 bytes on
	// Linux). Whether Abs succeeds is platform-dependent, so we assert the
	// SiblingDir invariant either way: a true ok must yield an absolute
	// directory, and a false ok must yield the empty string.
	//
	// NOTE: on Linux/macOS, filepath.Abs is pure string concatenation and
	// never returns an error, so only the ok=true branch is exercised here.
	// The ok=false invariant (empty string) is covered by
	// TestSiblingDirHandlesEmptyAndMissing.
	veryLong := strings.Repeat("a", 5000)
	dir, ok := SiblingDir(veryLong)
	if ok {
		if !filepath.IsAbs(dir) {
			t.Fatalf("SiblingDir returned ok=true with non-absolute dir %q", dir)
		}
	} else if dir != "" {
		t.Fatalf("SiblingDir returned ok=false with non-empty dir %q", dir)
	}
}

func TestResolveWithNilLookPath(t *testing.T) {
	// Passing nil lookPath triggers the default exec.LookPath branch.
	// Since serf-hub is unlikely to be on the real PATH, this should error.
	const name = "serf-hub-definitely-not-on-path"
	_, err := Resolve(name, "", filepath.Join(t.TempDir(), "serf-tui"), nil)
	if err == nil {
		t.Fatal("expected error when lookPath is nil and no candidate exists")
	}
	// The failure must name the binary and preserve the wrapped
	// exec.LookPath error so callers can still errors.Is against it.
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("error %q does not mention binary name %q", err, name)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("error %q does not wrap exec.ErrNotFound", err)
	}
}
