package cmdutil

import (
	"os"
	"path/filepath"
	"testing"
)

// A TMPDIR that no longer names a directory is a dead value: every
// os.MkdirTemp("") below a process that inherits it fails. Only an empty value
// defers to the platform default.
func TestTempDirExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{name: "empty means the platform default", path: "", want: true},
		{name: "existing directory", path: dir, want: true},
		{name: "missing directory", path: filepath.Join(dir, "gone"), want: false},
		{name: "regular file", path: file, want: false},
		{name: "blank names no directory", path: " ", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := TempDirExists(tc.path); got != tc.want {
				t.Fatalf("TempDirExists(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
