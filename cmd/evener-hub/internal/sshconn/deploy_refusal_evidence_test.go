package sshconn

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The deploy-side refusals here are the pure filesystem arms: a build source
// that is not a real evener checkout, and an embedded web dist that was never
// built. Each fixture below can reach exactly one refusal arm — that is what
// distinguishes them, since these arms are plain errors with no sentinel to
// match. The remote push/install arms of the deploy pipeline are not driven
// here; they need a live remote host's channel.

func TestVerifyBuildSourceRefusesUnusableSources(t *testing.T) {
	t.Parallel()

	if _, err := verifyBuildSource("   "); err == nil {
		t.Fatal("a blank build source was accepted")
	}

	if _, err := verifyBuildSource(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a nonexistent build source was accepted")
	}

	// A directory that exists but declares some other module: EvalSymlinks
	// already succeeded and the module-declaration check is the first one
	// verifyBuildSource runs, so any refusal here is that arm — the foreign
	// go.mod never gets as far as the package checks.
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "go.mod"), []byte("module other.example/mod\n"), 0o644); err != nil {
		t.Fatalf("write foreign go.mod: %v", err)
	}
	if _, err := verifyBuildSource(foreign); err == nil {
		t.Fatal("a build source declaring a foreign module was accepted")
	}

	// A checkout-shaped tree whose cmd/evener is a file: the module
	// declaration passes and Stat succeeds, so the only refusal left before
	// the revision check is the non-directory package-shape arm.
	shaped := t.TempDir()
	if err := os.WriteFile(filepath.Join(shaped, "go.mod"), []byte("module primeradiant.com/evener\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(shaped, "cmd"), 0o755); err != nil {
		t.Fatalf("make cmd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shaped, "cmd", "evener"), nil, 0o644); err != nil {
		t.Fatalf("block the evener package: %v", err)
	}
	if _, err := verifyBuildSource(shaped); err == nil {
		t.Fatal("a build source whose cmd/evener is a file was accepted")
	}
}

func TestEnsureWebBuiltRefusesAnUnbuiltDist(t *testing.T) {
	t.Parallel()

	// A missing dist directory surfaces the ReadDir failure it wraps.
	if err := ensureWebBuilt(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a tree with no dist directory = %v, want the wrapped missing-dist read failure", err)
	}

	// An empty dist directory: no placeholder, no index — the no-index arm.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "cmd", "evener-hub", "frontend", "dist"), 0o755); err != nil {
		t.Fatalf("make dist: %v", err)
	}
	if err := ensureWebBuilt(empty); err == nil {
		t.Fatal("an empty dist directory passed the web-build check")
	}

	// A placeholder-only dist: the directory reads back with exactly the
	// placeholder file, so the only refusal arm left is the placeholder one.
	placeholder := t.TempDir()
	dist := filepath.Join(placeholder, "cmd", "evener-hub", "frontend", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatalf("make dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "PLACEHOLDER"), nil, 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	if err := ensureWebBuilt(placeholder); err == nil {
		t.Fatal("a placeholder-only dist passed the web-build check")
	}

	built := t.TempDir()
	builtDist := filepath.Join(built, "cmd", "evener-hub", "frontend", "dist")
	if err := os.MkdirAll(builtDist, 0o755); err != nil {
		t.Fatalf("make dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(builtDist, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := ensureWebBuilt(built); err != nil {
		t.Fatalf("built dist refused: %v", err)
	}
}
