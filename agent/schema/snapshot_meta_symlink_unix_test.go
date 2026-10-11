//go:build unix

package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// TestLoadSessionMetaFS_SymlinkedIntermediateDirRefused (FU3 round 12, M3)
// asserts loadSessionMetaFS refuses to read session metadata when an
// intermediate directory on the meta path (sessions/) is a symlink pointing
// elsewhere. Pre-fix, loadSessionMetaFS only Lstat'd the leaf .meta.json;
// os.Lstat follows symlinks in intermediate components, so a symlinked
// sessions/ was invisible to the leaf-only check, and afero.ReadFile followed
// it to read metadata from an untrusted location. Post-fix, metaComponentWalk
// Lstats each intermediate between the bucket dir and the leaf, rejecting
// any symlink before the leaf is opened.
func TestLoadSessionMetaFS_SymlinkedIntermediateDirRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"

	// Create a real sessions directory with a valid meta file.
	realSessions := filepath.Join(dir, "realSessions")
	if err := os.MkdirAll(realSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(realSessions, id+".meta.json")
	if err := os.WriteFile(metaPath, []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace sessions/ with a symlink to the real sessions directory. Both
	// the symlink and its target stay inside dir (the bucket root), so nothing
	// traverses above it.
	if err := os.Symlink(realSessions, filepath.Join(dir, sessionsSubdir)); err != nil {
		t.Fatal(err)
	}

	// loadSessionMetaFS must refuse: the sessions/ intermediate is a symlink.
	_, err := loadSessionMetaFS(afero.NewOsFs(), dir, id)
	if err == nil {
		t.Fatal("loadSessionMetaFS followed a symlinked sessions/ intermediate dir; should refuse")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error about symlinked intermediate, got: %v", err)
	}
}

// TestLoadSessionMetaFS_SymlinkedLeafRefusedByNoFollow (FU3 round 12, L6)
// asserts loadSessionMetaFS opens the meta file through a single O_NOFOLLOW
// descriptor that atomically refuses a symlink at the leaf, rather than
// Lstat-then-ReadFile which leaves a TOCTOU window. A symlinked .meta.json
// leaf is rejected by both the pre-fix Lstat check and the post-fix O_NOFOLLOW
// open, but the mechanisms produce distinct error messages: the pre-fix Lstat
// path returns "is a symlink", while the post-fix O_NOFOLLOW path returns
// "refusing to follow it" (ELOOP from the kernel). This test asserts the
// O_NOFOLLOW message, so it fails on the pre-fix Lstat-then-ReadFile path.
func TestLoadSessionMetaFS_SymlinkedLeafRefusedByNoFollow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"

	// Create a real sessions dir and a real meta file outside the leaf path.
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realMeta := filepath.Join(dir, "real.meta.json")
	if err := os.WriteFile(realMeta, []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the meta leaf with a symlink to the real file. The symlink stays
	// inside dir (the bucket root), so nothing traverses above it.
	leafPath := filepath.Join(sessDir, id+".meta.json")
	if err := os.Symlink(realMeta, leafPath); err != nil {
		t.Fatal(err)
	}

	// loadSessionMetaFS must refuse via O_NOFOLLOW (ELOOP), producing the
	// "refusing to follow it" message — not the pre-fix Lstat "is a symlink"
	// message.
	_, err := loadSessionMetaFS(afero.NewOsFs(), dir, id)
	if err == nil {
		t.Fatal("loadSessionMetaFS followed a symlinked meta leaf; should refuse via O_NOFOLLOW")
	}
	if !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("expected O_NOFOLLOW refusal ('refusing to follow it'), got: %v", err)
	}
}

// TestLoadSessionMetaFS_ReadOnlyFs_SymlinkedLeafRefused (FU3 round 13, M1)
// asserts readMetaFile's no-follow open fires when the afero.Fs is a ReadOnlyFs
// wrapping OsFs, not just bare *afero.OsFs. Pre-fix, readMetaFile matched only
// *afero.OsFs; a ReadOnlyFs over OsFs fell through to afero.ReadFile, which
// follows a symlinked leaf — bypassing the O_NOFOLLOW guard. Post-fix, the type
// switch unwraps ReadOnlyFs (identity path mapping) and opens through
// readFileNoFollowOS, refusing the symlink with ELOOP.
func TestLoadSessionMetaFS_ReadOnlyFs_SymlinkedLeafRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "02wMz5Txv1C3Hut0M8GCeB"

	// Create a real sessions dir and a real meta file outside the leaf path.
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realMeta := filepath.Join(dir, "real.meta.json")
	if err := os.WriteFile(realMeta, []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace the meta leaf with a symlink to the real file.
	leafPath := filepath.Join(sessDir, id+".meta.json")
	if err := os.Symlink(realMeta, leafPath); err != nil {
		t.Fatal(err)
	}

	// ReadOnlyFs wrapping OsFs: readMetaFile must still refuse via O_NOFOLLOW.
	fs := afero.NewReadOnlyFs(afero.NewOsFs())
	_, err := loadSessionMetaFS(fs, dir, id)
	if err == nil {
		t.Fatal("readMetaFile followed a symlinked meta leaf via ReadOnlyFs; should refuse via O_NOFOLLOW")
	}
	if !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("expected O_NOFOLLOW refusal ('refusing to follow it'), got: %v", err)
	}
}
