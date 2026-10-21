package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The repair must happen before dispatch, so `evener serve --resume` and the
// `evener hub` subcommand both start from a usable temp dir.
func TestMainWithDepsDropsDeadTMPDIRBeforeDispatch(t *testing.T) {
	dead := filepath.Join(t.TempDir(), "gone", "tmp")
	t.Setenv("TMPDIR", dead)
	deps := defaultMainDeps()
	seen := dead
	deps.dispatch = func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) {
		seen = os.Getenv("TMPDIR")
		return true, "", nil
	}
	deps.exit = func(int) {}
	deps.args = []string{"serve"}

	mainWithDeps(deps)
	if seen != "" {
		t.Fatalf("dispatch saw TMPDIR %q; a dead value must be removed, not just changed", seen)
	}
}
