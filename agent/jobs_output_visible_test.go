package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A closed output file can still hold bytes older than the retention cap;
// the closed-file readers take the first visible lifetime offset and must
// never return anything before it.
func TestClosedOutputFileReadersStartAtTheVisibleStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, []byte("old-a\nold-b\nnew-1\nnew-2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The file starts at lifetime offset 100; readers see from "new-1".
	total := int64(100 + len("old-a\nold-b\nnew-1\nnew-2\n"))
	visibleStart := int64(100 + len("old-a\nold-b\n"))

	tail, gotTotal, truncated, err := tailOutputFile(path, 1024, total, visibleStart)
	if err != nil || tail != "new-1\nnew-2\n" || gotTotal != total || !truncated {
		t.Fatalf("tail = %q, %d, %v, %v; want the visible bytes, truncated", tail, gotTotal, truncated, err)
	}
	head, _, truncated, err := headOutputFile(path, 6, total, visibleStart)
	if err != nil || head != "new-1\n" || !truncated {
		t.Fatalf("head = %q, %v, %v; want the first visible line, truncated", head, truncated, err)
	}
}
