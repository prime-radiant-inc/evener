package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// command is os/exec's constructor in one place, so the cancellation a wedged
// git or `go list` needs lives here rather than at each call site.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// relPath returns target relative to base, both cleaned.
func relPath(base, target string) (string, error) {
	return filepath.Rel(filepath.Clean(base), filepath.Clean(target))
}

// pathExists reports whether path exists at all.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
