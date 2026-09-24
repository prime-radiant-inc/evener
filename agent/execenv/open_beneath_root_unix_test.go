//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenRegularBeneathRoot_RefusesSymlinkedIntermediateDir (FU3 round 13, M2)
// asserts OpenRegularBeneathRoot refuses a symlink at an intermediate directory
// component, not just at the leaf. OpenRegularNoFollow only protects the final
// component with O_NOFOLLOW; intermediate dirs are protected only by the
// symlinkErrorDeep pre-walk (Lstat before open), which leaves a TOCTOU window.
// OpenRegularBeneathRoot walks every component via openat(O_NOFOLLOW), so a
// symlinked intermediate is refused atomically (ELOOP) without following it.
//
// This test calls OpenRegularBeneathRoot directly with a symlinked parent
// component, bypassing any pre-walk — proving the descriptor-relative walk
// itself catches the symlink, not the pre-walk.
func TestOpenRegularBeneathRoot_RefusesSymlinkedIntermediateDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Create a real sessions/ dir with a real file inside.
	realSessions := filepath.Join(root, "realSessions")
	if err := os.MkdirAll(realSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(realSessions, "transcript.jsonl")
	if err := os.WriteFile(realFile, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replace sessions/ with a symlink to the real sessions dir.
	if err := os.Symlink(realSessions, filepath.Join(root, "sessions")); err != nil {
		t.Fatal(err)
	}

	// The path through the symlinked sessions/ dir.
	symlinkedPath := filepath.Join(root, "sessions", "transcript.jsonl")

	// OpenRegularBeneathRoot must refuse: sessions/ is a symlink.
	f, err := OpenRegularBeneathRoot(symlinkedPath, root)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularBeneathRoot followed a symlinked intermediate dir; should refuse")
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", err)
	}
}

// TestOpenRegularBeneathRoot_RefusesSymlinkedLeaf asserts the leaf-level
// O_NOFOLLOW guarantee also holds when root is provided.
func TestOpenRegularBeneathRoot_RefusesSymlinkedLeaf(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Create sessions/ dir with a real file outside the leaf path.
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(root, "real.jsonl")
	if err := os.WriteFile(realFile, []byte(`{"kind":"header"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlink the leaf.
	leafPath := filepath.Join(root, "sessions", "transcript.jsonl")
	if err := os.Symlink(realFile, leafPath); err != nil {
		t.Fatal(err)
	}

	f, err := OpenRegularBeneathRoot(leafPath, root)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularBeneathRoot followed a symlinked leaf; should refuse")
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", err)
	}
}

// TestOpenRegularBeneathRoot_OpensRegularFile asserts the happy path: a
// regular file beneath root opens and reads back its bytes.
func TestOpenRegularBeneathRoot_OpensRegularFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "sessions", "transcript.jsonl")
	if err := os.WriteFile(path, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := OpenRegularBeneathRoot(path, root)
	if err != nil {
		t.Fatalf("OpenRegularBeneathRoot: %v", err)
	}
	defer f.Close()
	got := make([]byte, len("payload"))
	if _, err := f.Read(got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("read = %q, want payload", got)
	}
}
