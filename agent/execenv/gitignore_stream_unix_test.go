//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"
)

// blockingIgnoreFS hands back one fixed file from Open: the ignore read
// itself is under test, not its discovery.
type blockingIgnoreFS struct{ f fs.File }

func (b blockingIgnoreFS) Open(string) (fs.File, error) { return b.f, nil }

// An ignore file the admitting open handed back as a poll-registered,
// never-ready stream — the shape of a readable .gitignore symlink to
// /proc/kmsg, canonical root-only — must not park the discovery read on the
// runtime poller with nothing bounding the wait (#4574 round 8, HIGH): the
// ignore read gets the same deadline-and-cancellation treatment the search
// reads have. /dev/ptmx stands in for the stream: its read blocks until a
// slave writes, and the cancellation must interrupt it.
func TestIgnoreReadIsInterruptibleOnABlockedStream(t *testing.T) {
	t.Parallel()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open ptmx: %v", err)
	}
	defer func() { _ = master.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fsys := blockingIgnoreFS{f: &grepCtxFile{File: master, ctx: ctx}}

	done := make(chan error, 1)
	go func() {
		_, _, err := readIgnoreFile(ctx, fsys, ".gitignore", newGlobBudget("test"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the blocked ignore read returned before cancellation: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()

	// The read must answer the cancellation promptly, not when a slave
	// writes; the ceiling is a tripwire, and only a blocked read can miss it.
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled blocked ignore read = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("the blocked ignore read did not return within 10s of cancellation")
	}
}
