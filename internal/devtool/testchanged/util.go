package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// command is os/exec's constructor in one place so tests that call run() can
// see the same binary the tool launches.
func command(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// relPath returns target relative to base, both cleaned.
func relPath(base, target string) (string, error) {
	return filepath.Rel(filepath.Clean(base), filepath.Clean(target))
}

// pathExists reports whether path exists at all.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
