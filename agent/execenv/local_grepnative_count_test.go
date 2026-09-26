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
// rg --count output. roborev round 3 caught the count branch recording a row
// for every matching file regardless of the cap, so a sandboxed session
// (which always takes the native path) advertising "at most N count entries"
// was over-delivering and then silently hitting the output backstop.
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
	if rows := strings.Split(gotAll, "\n"); len(rows) != 5 {
		t.Fatalf("count mode with cap lifted returned %d rows, want 5:\n%s", len(rows), gotAll)
	}

	got, err := env.grepNative(context.Background(), "hit", root, "", false, 3, "count")
	if err != nil {
		t.Fatalf("grepNative count: %v", err)
	}
	if rows := strings.Split(got, "\n"); len(rows) != 3 {
		t.Fatalf("count mode returned %d rows, want at most 3 (max_results):\n%s", len(rows), got)
	}
}
