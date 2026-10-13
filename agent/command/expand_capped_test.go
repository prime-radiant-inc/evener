package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// @file expansion runs synchronously during argument substitution, so its
// reads carry the whole-file read cap: an over-cap source degrades to the
// existing inline error marker instead of being ingested whole (issue #4556).

func TestReadInlineFileDegradesAnOverCapFileToAMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create oversize: %v", err)
	}
	if err := os.Truncate(path, execenv.MaxWholeFileReadBytes+1); err != nil {
		t.Fatalf("truncate oversize: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)

	got := readInlineFile("huge.log", env)
	if !strings.Contains(got, "error reading") {
		t.Fatalf("readInlineFile(oversize) = %q, want the inline error marker", got)
	}
	if len(got) > maxInlineBytes {
		t.Fatalf("readInlineFile(oversize) returned %d bytes, want a bounded marker", len(got))
	}
}
