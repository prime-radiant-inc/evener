package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// The apply methods reach the filesystem through the os package directly, so
// their MkdirAll/WriteFile/Rename error arms are exercised with real
// filesystem shapes (a file where a directory is expected, a read-only file,
// a rename onto a non-empty directory) inside an isolated t.TempDir.

// addFileOp.apply returns the MkdirAll error when a path component is an
// existing regular file (apply_patch.go:48).
func TestW3Sub_ApplyPatch_AddFile_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	patch := "*** Begin Patch\n*** Add File: blocker/child.txt\n+hello\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected MkdirAll error when parent path is a regular file")
	}
}

// addFileOp.apply returns the WriteFile error when the target path is an
// existing directory (apply_patch.go:55).
func TestW3Sub_ApplyPatch_AddFile_WriteFileError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	patch := "*** Begin Patch\n*** Add File: adir\n+hello\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected WriteFile error when target path is a directory")
	}
}

// updateFileOp.apply rejects a source path that escapes the root (safeJoin
// error, apply_patch.go:82).
func TestW3Sub_ApplyPatch_UpdateTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	patch := "*** Begin Patch\n*** Update File: ../escape.txt\n@@\n-one\n+ONE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected traversal update to be rejected")
	}
}

// updateFileOp.apply falls back to searching from pos when the @@ hint
// narrows the search past the actual sequence location (apply_patch.go:113).
func TestW3Sub_ApplyPatch_UpdateHintFallback(t *testing.T) {
	dir := t.TempDir()
	// "apple" sits before the "HINT" line, so the hint narrows searchStart to
	// index 2 (past apple); the fallback re-search from pos=0 finds it.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("apple\nzzz\nHINT\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: f.txt\n@@ HINT\n-apple\n+APPLE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err != nil {
		t.Fatalf("hint-fallback update failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "APPLE\nzzz\nHINT\n" {
		t.Fatalf("hint-fallback result = %q", string(got))
	}
}

// updateFileOp.apply returns the WriteFile error when the target file is
// readable (ReadFile succeeds) but not writable (apply_patch.go:139).
func TestW3Sub_ApplyPatch_UpdateWriteFileError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores file permission bits")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "ro.txt")
	if err := os.WriteFile(p, []byte("apple\n"), 0o444); err != nil {
		t.Fatalf("seed read-only file: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: ro.txt\n@@\n-apple\n+APPLE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected WriteFile error on a read-only file")
	}
}

// updateFileOp.apply rejects a Move-to path that escapes the root (safeJoin
// error for the destination, apply_patch.go:145).
func TestW3Sub_ApplyPatch_MoveToTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("apple\n"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: src.txt\n*** Move to: ../escape.txt\n@@\n-apple\n+APPLE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected Move-to traversal to be rejected")
	}
}

// updateFileOp.apply returns the destination MkdirAll error when a Move-to
// path component is an existing regular file (apply_patch.go:148).
func TestW3Sub_ApplyPatch_MoveToMkdirAllError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("apple\n"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: src.txt\n*** Move to: blocker/dst.txt\n@@\n-apple\n+APPLE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected Move-to MkdirAll error when parent is a regular file")
	}
}

// updateFileOp.apply returns the Rename error when the Move-to destination is
// an existing non-empty directory (apply_patch.go:151).
func TestW3Sub_ApplyPatch_MoveToRenameError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("apple\n"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	dstDir := filepath.Join(dir, "dst")
	if err := os.Mkdir(dstDir, 0o755); err != nil {
		t.Fatalf("seed dst dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "occupied"), []byte("y"), 0o644); err != nil {
		t.Fatalf("seed dst dir child: %v", err)
	}
	patch := "*** Begin Patch\n*** Update File: src.txt\n*** Move to: dst\n@@\n-apple\n+APPLE\n*** End Patch\n"
	if _, err := ApplyPatch(dir, patch); err == nil {
		t.Fatalf("expected Rename error onto a non-empty directory")
	}
}
