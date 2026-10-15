//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// mkGrepFifoDir lays a real match beside two readerless FIFOs: one a walk
// reaches, one named as a search target. Neither ever gets a writer, so any
// blocking open of either hangs forever — the shape #4574 round 1's HIGH
// finding reproduced under /tmp and /run once a path=/ search took the native
// walk, whose os.DirFS opens block and whose byte cap only applies after a
// successful open.
func mkGrepFifoDir(t *testing.T) (root, walkFifo, namedFifo string) {
	t.Helper()
	root = t.TempDir()
	walkFifo = filepath.Join(root, "pipe")
	namedFifo = filepath.Join(root, "named-pipe")
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, fifo := range []string{walkFifo, namedFifo} {
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatalf("mkfifo %s: %v", fifo, err)
		}
	}
	return root, walkFifo, namedFifo
}

// A reachable readerless FIFO cannot block a search's completion or its
// cancellation: the walk skips it in the unreadable-file class instead of
// blocking at open until a writer appears, and a cancelled search answers
// with the cancellation rather than waiting behind one. The two searches
// only return because the open is nonblocking and type-checked — before that
// fix this test hung until go test's timeout killed it.
func TestGrepSkipsReaderlessFifosWithoutBlocking(t *testing.T) {
	t.Parallel()
	root, _, namedFifo := mkGrepFifoDir(t)
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.lookPath = func(string) (string, error) { return "", errors.New("rg unavailable") }

	got, err := env.Grep(t.Context(), "needle", root, "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if want := "real.txt"; got != want {
		t.Fatalf("walk past a readerless FIFO = %q, want only %q", got, want)
	}

	got, err = env.Grep(t.Context(), "needle", namedFifo, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("readerless FIFO named as the target = %q, want it skipped without a match", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.Grep(ctx, "needle", root, "", false, 100, "files_with_matches"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search over a tree holding a FIFO = %v, want the cancellation itself", err)
	}
}
