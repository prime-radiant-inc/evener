//go:build linux || darwin

package agent

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/execenv"
)

// A FIFO named like a page is not a page: reading it would block.
func TestListMemoryPagesSkipsFIFO(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()
	if err := unix.Mkfifo(filepath.Join(root, "scope", "x.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	pages, err := listMemoryPages(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 0 {
		t.Fatalf("pages=%+v want none", pages)
	}
}
