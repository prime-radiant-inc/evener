//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestSandboxReadFileCtxObservesCancellation pins the ctx threading to the
// file-tool read's fd-anchored arm: a cancelled call answers before the read,
// rather than parking on a stream it would otherwise have to wait out.
func TestSandboxReadFileCtxObservesCancellation(t *testing.T) {
	t.Parallel()
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	path := filepath.Join(worktree, "file.txt")
	if err := os.WriteFile(path, []byte("data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.readFileCtx(ctx, "read_file", path); !errors.Is(err, context.Canceled) {
		t.Fatalf("readFileCtx(cancelled) = %v, want context.Canceled", err)
	}
}

// TestReadFileCtxObservesCancellation pins the ctx threading through the
// exported off-arm entry: a cancelled call reaches the read and answers with
// the cancellation.
func TestReadFileCtxObservesCancellation(t *testing.T) {
	t.Parallel()
	env := NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	path := filepath.Join(env.RootDir, "file.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.ReadFileCtx(ctx, "file.txt", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadFileCtx(cancelled) = %v, want context.Canceled", err)
	}
}
