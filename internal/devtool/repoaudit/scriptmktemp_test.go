package repoaudit

import (
	"os"
	"path/filepath"
	"testing"
)

// writeScriptFixture writes content to path, creating any missing parent
// directories, for checks that read real files at chosen paths.
func writeScriptFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRecursiveDeleteAllowlistDoesNotInheritByBasename is the regression
// test for #183's second defect: a poison file at a DIFFERENT path than an
// allowlisted one, sharing only its basename, must not inherit the
// allowlisted path's exemption. Before the fix, recursiveDeleteAllowedLines
// was keyed by filepath.Base(path), so an unrelated "install.sh" anywhere in
// the tree read the same allowance as the real root install.sh.
func TestRecursiveDeleteAllowlistDoesNotInheritByBasename(t *testing.T) {
	dir := t.TempDir()
	poisonPath := filepath.Join(dir, "unrelated", "install.sh")
	writeScriptFixture(t, poisonPath, "#!/bin/sh\nrm -rf \"$stage\"\n")

	// allowedPath is a DIFFERENT file, never written here, that legitimately
	// earned this allowance under review; poisonPath must not ride on it just
	// because both happen to end in "install.sh".
	allowedPath := filepath.Join(dir, "reviewed", "install.sh")
	allowed := map[string]int{filepath.Base(allowedPath): 1}

	offenders, _, _ := recursiveDeleteOffenders(&audit{}, []string{poisonPath}, allowed)
	if len(offenders) == 0 {
		t.Fatalf("poisonPath (%s) shares allowedPath's basename (install.sh) but is a different, "+
			"unreviewed file; its recursive delete must be flagged rather than silently inherit "+
			"allowedPath's exemption", poisonPath)
	}
}
