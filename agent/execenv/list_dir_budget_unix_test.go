//go:build unix

package execenv

// Unix-gated regressions for the list_dir walk's root open, round 4 of the
// #2375 budget work: the off-sandbox walk opens the requested path itself, so
// a readerless FIFO as the target must fail the open promptly instead of
// blocking it until a writer appears, an already-cancelled context must be
// answered before any open, and a symlink to a directory must keep
// traversing. The sandboxed arm opens through openat with O_DIRECTORY already,
// so its FIFO parity is pinned here too.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/sandbox"
)

// mkListDirFIFO makes a readerless named pipe at dir/pipe and returns its
// path. No writer is ever opened for it, so an unfenced open for reading
// blocks at the open itself.
func mkListDirFIFO(t *testing.T, dir string) string {
	t.Helper()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	return fifo
}

// TestListDirBudgetDoesNotBlockOnAReaderlessFIFO is round 4's medium: opening
// a FIFO for reading blocks at the open until a writer appears, and the walk's
// root open had no fence in front of it — the cancellation-aware chunked
// reader behind the open was never reached, so a list_dir on a FIFO target
// hung past any context's help. The open must fail a FIFO promptly in the
// not-a-directory class, naming the requested path.
func TestListDirBudgetDoesNotBlockOnAReaderlessFIFO(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	fifo := mkListDirFIFO(t, dir)

	type listing struct {
		entries []DirEntry
		err     error
	}
	done := make(chan listing, 1)
	go func() {
		entries, err := env.ListDirectoryWithBudget(t.Context(), fifo, 1, NewDirListBudget(0, 1000, 0))
		done <- listing{entries, err}
	}()
	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("listing a FIFO target succeeded: %v", got.entries)
		}
		if !errors.Is(got.err, syscall.ENOTDIR) {
			t.Fatalf("FIFO target = %q, want the not-a-directory class (ENOTDIR), not a block", got.err)
		}
		var perr *fs.PathError
		if !errors.As(got.err, &perr) || perr.Path != fifo {
			t.Fatalf("FIFO target error = %q, want a *fs.PathError naming the requested path %q", got.err, fifo)
		}
	case <-time.After(5 * time.Second):
		t.Error("list_dir on a readerless FIFO blocked past 5s: the root open must be directory-fenced so a FIFO fails ENOTDIR promptly, not wait for a writer")
	}
}

// TestListDirBudgetAnswersACancelledContextBeforeTheOpen pins the context
// half of the medium: cancellation must be checked before the root open, not
// only between the chunks of a listing that already started — with a FIFO at
// the requested path there is no such listing to cancel, so a walk that only
// checked ctx inside the reader would still block at the open. An
// already-cancelled call must answer context.Canceled promptly without
// opening anything.
func TestListDirBudgetAnswersACancelledContextBeforeTheOpen(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	fifo := mkListDirFIFO(t, dir)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := env.ListDirectoryWithBudget(ctx, fifo, 1, NewDirListBudget(0, 1000, 0))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled list_dir over a FIFO root = %q, want context.Canceled answered before any open", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("a cancelled list_dir blocked on a FIFO root: ctx must be checked before the open, where no chunked read can ever land the cancellation")
	}
}

// TestSandboxedListDirBudgetAnswersACancelledContextBeforeTheOpen is the
// confined twin of TestListDirBudgetAnswersACancelledContextBeforeTheOpen:
// the root open resolves the requested path component by component and is
// the arm's one filesystem touch before the walk's own checks — a resolution
// over an unavailable mount is exactly the unbounded wait the budget exists
// to prevent — so an already-cancelled call must answer before it. The
// missing target proves the point: a walk that opened would answer the
// filesystem's ENOENT, and only a walk that never touched the filesystem
// answers the cancellation.
func TestSandboxedListDirBudgetAnswersACancelledContextBeforeTheOpen(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	missing := filepath.Join(worktree, "definitely-not-here-xyz")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	entries, err := env.ListDirectoryWithBudget(ctx, missing, 1, NewDirListBudget(0, 1000, 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled confined list_dir = (%v, %q), want context.Canceled answered before the root open", entries, err)
	}
}

// TestSandboxedListDirBudgetKeepsSandboxDenialsUnwrapped pins the round-5
// redaction finding: a typed sandbox denial is its own disclosure contract —
// Error() reduces the requested path to a basename, and a sensitive denial
// omits the path entirely — so the root-open wrap must return it unchanged:
// the fs.PathError shell prints the FULL requested path around whatever the
// denial chose to disclose, which for the sensitive denials darwin's
// canonical-path recheck can produce is exactly the leak the masking policy
// exists to prevent. The Linux-reachable denial here is the symlink
// component refusal, whose own diagnostic deliberately names the offending
// component and its target — disclosure chosen by the denial, not by the
// wrap — so the pinned property is the shell's absence, which holds for
// every typed denial on both arms.
func TestSandboxedListDirBudgetKeepsSandboxDenialsUnwrapped(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	link := filepath.Join(worktree, "link")
	if err := os.Symlink(worktree, link); err != nil {
		t.Fatal(err)
	}

	_, err := env.ListDirectoryWithBudget(t.Context(), link, 1, NewDirListBudget(0, 1000, 0))
	if _, ok := errors.AsType[*sandbox.DeniedError](err); !ok {
		t.Fatalf("a symlink root must refuse through the typed sandbox denial, got %q", err)
	}
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		t.Fatalf("the typed denial must return unchanged: the fs.PathError shell prints the full requested path %q around the denial's own disclosure contract: %q", link, err)
	}
}

// TestListDirBudgetListsThroughASymlinkToADirectory pins the fence's edge:
// O_DIRECTORY constrains the target's type after the link resolves, so a
// symlink pointing at a directory must keep traversing exactly as the legacy
// walk's followed open did — same entries, same names.
func TestListDirBudgetListsThroughASymlinkToADirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "leaf.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(root)

	legacy, err := env.ListDirectory(link, 2)
	if err != nil {
		t.Fatalf("legacy walk through a symlinked root: %v", err)
	}
	got, err := env.ListDirectoryWithBudget(t.Context(), link, 2, NewDirListBudget(0, 1000, 0))
	if err != nil {
		t.Fatalf("budgeted walk through a symlinked root: %v", err)
	}
	if !slices.Equal(got, legacy) || len(got) != 1 || got[0].Name != "leaf.txt" {
		t.Fatalf("budgeted walk through a symlinked root = %v, want exactly the legacy listing %v", got, legacy)
	}
}

// TestSandboxedListDirBudgetRejectsAFIFOTargetPromptly pins the confined
// arm's side of the round-4 medium: its root open is an openat with
// O_DIRECTORY, so a FIFO target has always failed promptly there. Prompt
// completion is the contract and holds on every platform; the CLASS of the
// refusal is the resolver's to choose — Linux's openat2 surfaces the raw
// ENOTDIR, while darwin's component walk classifies a non-directory final
// component as a traversal refusal (errSymlinkComponent), which mapOpenErr
// answers with the typed symlink denial, a refusal that does not preserve
// ENOTDIR. Both are prompt, structured refusals of the same target.
func TestSandboxedListDirBudgetRejectsAFIFOTargetPromptly(t *testing.T) {
	env, _, worktree := sandboxedEnv(t, sandbox.ModeReadOnly)
	fifo := mkListDirFIFO(t, worktree)

	done := make(chan error, 1)
	go func() {
		_, err := env.ListDirectoryWithBudget(t.Context(), fifo, 1, NewDirListBudget(0, 1000, 0))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("confined listing of a FIFO target succeeded")
		}
		if runtime.GOOS == "darwin" {
			if _, ok := errors.AsType[*sandbox.DeniedError](err); !ok {
				t.Fatalf("confined FIFO target on darwin = %q, want the resolver's typed symlink denial", err)
			}
		} else if !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("confined FIFO target = %q, want the not-a-directory class (ENOTDIR)", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("confined list_dir blocked on a FIFO root: the openat with O_DIRECTORY must fail a FIFO promptly")
	}
}
