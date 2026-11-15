package dev

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/internal/devtool/gatesurface"
)

// TestGateSurfaceSubcommandWritesTheShellLib pins the generator behind make
// generate: the subcommand writes ShellLib to the path the shell gate sources,
// creating the directory if needed.
func TestGateSurfaceSubcommandWritesTheShellLib(t *testing.T) {
	root := t.TempDir()
	if code := gateSurfaceMain([]string{"-root", root}); code != 0 {
		t.Fatalf("gate-surface exit = %d, want 0", code)
	}
	got, err := os.ReadFile(filepath.Join(root, "scripts", "lib", "gate-surface-lib.sh"))
	if err != nil {
		t.Fatalf("reading generated lib: %v", err)
	}
	if string(got) != gatesurface.ShellLib() {
		t.Error("the generated file does not match gatesurface.ShellLib()")
	}
}
