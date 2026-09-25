//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
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

// openBeneathRootOrFailFast runs OpenRegularBeneathRoot and returns its result,
// failing the test (with a clear message) if it does not return within a few
// seconds. This guards the suite against an unbounded hang: openat(O_RDONLY) on
// a FIFO at an intermediate component blocks indefinitely waiting for a writer,
// so a regression that drops O_NONBLOCK from the intermediate openat surfaces
// as a visible timeout rather than a stuck suite.
func openBeneathRootOrFailFast(t *testing.T, path, root string) (*os.File, error) {
	t.Helper()
	type result struct {
		f   *os.File
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		f, err := OpenRegularBeneathRoot(path, root)
		resCh <- result{f, err}
	}()
	select {
	case res := <-resCh:
		return res.f, res.err
	case <-time.After(5 * time.Second):
		t.Fatalf("OpenRegularBeneathRoot hung on %q beneath %q (an intermediate openat blocked, likely a FIFO without O_NONBLOCK)", path, root)
		return nil, nil // unreachable
	}
}

// TestOpenRegularBeneathRoot_RefusesFIFOIntermediate (FU3 round 14, M-FIFO)
// asserts the descriptor walk opens every intermediate component with
// O_NONBLOCK so a FIFO planted at an intermediate directory component fails
// fast instead of hanging the read. openat(O_RDONLY) on a FIFO blocks
// indefinitely waiting for a writer, so without O_NONBLOCK the fstat "is it a
// directory?" check never runs and the whole read hangs — a regression of the
// pre-fix full-path open, which rejected a FIFO intermediate immediately
// (ENOTDIR during path resolution). Post-fix the FIFO opens nonblocking and
// fstat rejects it as "not a directory" promptly. The goroutine +
// select-timeout makes a regression fail as a visible timeout instead of
// hanging the suite.
func TestOpenRegularBeneathRoot_RefusesFIFOIntermediate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Plant a FIFO at the first intermediate component (sessions/).
	if err := unix.Mkfifo(filepath.Join(root, "sessions"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The leaf need not exist: the walk must reject the FIFO at the
	// intermediate before ever reaching it.
	target := filepath.Join(root, "sessions", "transcript.jsonl")

	f, err := openBeneathRootOrFailFast(t, target, root)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularBeneathRoot returned a file through a FIFO intermediate; should refuse")
	}
	if err == nil {
		t.Fatal("OpenRegularBeneathRoot returned no error through a FIFO intermediate; should refuse")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected prompt not-a-directory error for FIFO intermediate, got: %v", err)
	}
}

// TestOpenRegularBeneathRoot_RefusesFIFOIntermediateNested asserts the same
// O_NONBLOCK guarantee holds at an intermediate component beyond the first one,
// proving every iteration of the walk's openat loop carries O_NONBLOCK — not
// just the root-relative open at index 0.
func TestOpenRegularBeneathRoot_RefusesFIFOIntermediateNested(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// A real directory at the first component, a FIFO at the second.
	if err := os.MkdirAll(filepath.Join(root, "level1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "level1", "sessions"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "level1", "sessions", "transcript.jsonl")

	f, err := openBeneathRootOrFailFast(t, target, root)
	if f != nil {
		_ = f.Close()
		t.Fatal("OpenRegularBeneathRoot returned a file through a nested FIFO intermediate; should refuse")
	}
	if err == nil {
		t.Fatal("OpenRegularBeneathRoot returned no error through a nested FIFO intermediate; should refuse")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected prompt not-a-directory error for nested FIFO intermediate, got: %v", err)
	}
}
