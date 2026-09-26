package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepNativeCountModeCapsRowsAtMaxResults pins count mode to the same
// result cap the other modes honor: at most maxResults count rows (one per
// file with matches), mirroring the ripgrep path's first-N truncation of
// rg --count output. Sandboxed sessions always take the native path, so the
// cap the schema advertises must hold here, not just where ripgrep truncates.
func TestGrepNativeCountModeCapsRowsAtMaxResults(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("hit\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	env := NewLocalExecutionEnvironment(root)

	// Control: with the cap lifted, all five files get a count row.
	gotAll, err := env.grepNative(context.Background(), "hit", root, "", false, 100, "count")
	if err != nil {
		t.Fatalf("grepNative count (cap lifted): %v", err)
	}
	if rows := countOutputRows(gotAll); rows != 5 {
		t.Fatalf("count mode with cap lifted returned %d rows, want all 5 matching files:\n%s", rows, gotAll)
	}

	got, err := env.grepNative(context.Background(), "hit", root, "", false, 3, "count")
	if err != nil {
		t.Fatalf("grepNative count: %v", err)
	}
	if rows := countOutputRows(got); rows != 3 {
		t.Fatalf("count mode returned %d rows, want exactly 3 (max_results caps rows at 3; 5 files match):\n%s", rows, got)
	}
}

// countOutputRows counts a grep output's non-empty rows, tolerant of a
// trailing newline or empty output, so the row-count assertions cannot be
// shifted by rendering whitespace.
func countOutputRows(out string) int {
	rows := 0
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) != "" {
			rows++
		}
	}
	return rows
}
