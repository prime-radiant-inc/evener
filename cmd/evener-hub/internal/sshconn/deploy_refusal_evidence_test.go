package sshconn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The deploy-side refusals here are the pure filesystem arms: a build source
// that is not a real evener checkout, and an embedded web dist that was never
// built. The remote push/install arms of the deploy pipeline are not driven
// here; they need a live remote host's channel.

func TestVerifyBuildSourceRefusesUnusableSources(t *testing.T) {
	t.Parallel()

	if _, err := verifyBuildSource("   "); err == nil {
		t.Fatal("a blank build source was accepted")
	}

	if _, err := verifyBuildSource(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a nonexistent build source was accepted")
	}

	// A directory that exists but declares some other module never gets as far
	// as the package checks.
	foreign := t.TempDir()
	if err := os.WriteFile(filepath.Join(foreign, "go.mod"), []byte("module other.example/mod\n"), 0o644); err != nil {
		t.Fatalf("write foreign go.mod: %v", err)
	}
	if _, err := verifyBuildSource(foreign); err == nil || !strings.Contains(err.Error(), "not the evener checkout") {
		t.Fatalf("foreign module build source = %v, want the checkout refusal", err)
	}

	// A checkout-shaped tree whose cmd/evener is a file is refused before any
	// revision check runs.
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
	if _, err := verifyBuildSource(shaped); err == nil || !strings.Contains(err.Error(), "non-directory cmd/evener") {
		t.Fatalf("file-backed cmd/evener = %v, want the package-shape refusal", err)
	}
}

func TestEnsureWebBuiltRefusesAnUnbuiltDist(t *testing.T) {
	t.Parallel()

	if err := ensureWebBuilt(t.TempDir()); err == nil {
		t.Fatal("a tree with no dist directory passed the web-build check")
	}

	placeholder := t.TempDir()
	dist := filepath.Join(placeholder, "cmd", "evener-hub", "frontend", "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatalf("make dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "PLACEHOLDER"), nil, 0o644); err != nil {
		t.Fatalf("write placeholder: %v", err)
	}
	err := ensureWebBuilt(placeholder)
	if err == nil || !strings.Contains(err.Error(), "holds only the placeholder") {
		t.Fatalf("placeholder-only dist = %v, want the placeholder refusal", err)
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
