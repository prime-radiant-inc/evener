package agenttest

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// CheckGolden pins got to the golden file at path, relative to the calling
// test's package: update rewrites the file, and otherwise any drift fails with
// both versions and hint, which says how to regenerate.
func CheckGolden(t *testing.T, path string, got []byte, update bool, hint string) {
	t.Helper()
	if update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v. %s", path, err, hint)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s drifted.\n got: %s\nwant: %s\n%s", path, got, want, hint)
	}
}
