//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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

// TestSandboxReadFileCtxCancelsADuringRead pins the mid-read yank in
// armStreamDeadline: a ctx cancelled while the read is parked must end it with
// the cancellation promptly, not wait out the stall timeout. A FIFO with a
// writer that never writes stands in for the root-only never-ready regular; the
// stall timeout is left long so only the yank can end the read.
func TestSandboxReadFileCtxCancelsADuringRead(t *testing.T) {
	// Not parallel: swaps admitReadFD, pseudoFsRoots, grepReadStallTimeout.
	s, _, worktree := newSB(t, sandbox.ModeRestricted)
	stalled := filepath.Join(worktree, "kmsg-shaped")
	if err := unix.Mkfifo(stalled, 0o600); err != nil {
		t.Fatal(err)
	}
	// A read side so the writer can open; a write side that never writes so the
	// read parks instead of answering EOF.
	rfd, err := unix.Open(stalled, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(rfd), stalled)
	defer func() { _ = reader.Close() }()
	wfd, err := unix.Open(stalled, unix.O_WRONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer := os.NewFile(uintptr(wfd), stalled)
	defer func() { _ = writer.Close() }()

	admitted := make(chan struct{})
	origAdmit := admitReadFD
	admitReadFD = func(fd int, name string, allowDir, streamed bool) (*os.File, error) {
		f := os.NewFile(uintptr(fd), name)
		if !streamed {
			if flags, ferr := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0); ferr == nil {
				_, _ = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK)
			}
		}
		close(admitted)
		return f, nil
	}
	t.Cleanup(func() { admitReadFD = origAdmit })
	stubPseudoFsRoots(t, worktree)
	origStall := grepReadStallTimeout
	grepReadStallTimeout = 30 * time.Second
	t.Cleanup(func() { grepReadStallTimeout = origStall })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.readFileCtx(ctx, "read_file", stalled)
		done <- err
	}()
	<-admitted // the read is admitted; the next step is the blocking read
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a read cancelled mid-block = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read cancelled mid-block did not return: the mid-read yank is broken")
	}
}
