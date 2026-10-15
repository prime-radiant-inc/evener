package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// signalingDeadlineReader signals when a read begins and parks until its read
// deadline is yanked to now, so a test can cancel during an ACTIVE read rather
// than on readBounded's entry check.
type signalingDeadlineReader struct {
	reading  chan struct{}
	yanked   chan struct{}
	readOnce sync.Once
	yankOnce sync.Once
}

func newSignalingDeadlineReader() *signalingDeadlineReader {
	return &signalingDeadlineReader{reading: make(chan struct{}), yanked: make(chan struct{})}
}

func (r *signalingDeadlineReader) Read([]byte) (int, error) {
	r.readOnce.Do(func() { close(r.reading) })
	<-r.yanked
	return 0, os.ErrDeadlineExceeded
}

// SetReadDeadline arms on a future deadline and treats a non-future one as the
// cancellation yank, unblocking the parked Read.
func (r *signalingDeadlineReader) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	if !t.After(time.Now()) {
		r.yankOnce.Do(func() { close(r.yanked) })
	}
	return nil
}

// TestReadBoundedCancelsAnActiveRead pins the mid-read yank in armStreamDeadline
// at readBounded, the shared bounded-read constructor both file-tool arms use: a
// ctx cancelled while the read is genuinely parked in Read must end it with the
// cancellation promptly. The reader signals on Read entry, so cancellation
// cannot land on readBounded's entry check (the earlier test's weakness) — the
// read is provably active before cancel is called.
func TestReadBoundedCancelsAnActiveRead(t *testing.T) {
	t.Parallel()
	r := newSignalingDeadlineReader()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := readBounded(ctx, r, r, 1<<20)
		done <- err
	}()
	select {
	case <-r.reading: // the read is active, well past the entry check
	case <-time.After(3 * time.Second):
		t.Fatal("readBounded never entered the read")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a read cancelled while active = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a read cancelled while active did not return: the mid-read yank is broken")
	}
}

// refusingDeadlineSetter models an ordinary regular file: SetReadDeadline is
// refused, so armStreamDeadline arms no watcher and the read never fails on its
// own when the context is cancelled.
type refusingDeadlineSetter struct{}

func (refusingDeadlineSetter) SetReadDeadline(time.Time) error { return os.ErrNoDeadline }

// stagedReader returns one chunk, then parks until the test releases it, so a
// cancellation can land after reading began but before the read finishes.
type stagedReader struct {
	first   chan struct{}
	release chan struct{}
	reads   int
}

func (r *stagedReader) Read(p []byte) (int, error) {
	r.reads++
	switch r.reads {
	case 1:
		close(r.first)
		return copy(p, "first chunk"), nil
	default:
		<-r.release
		return 0, io.EOF
	}
}

// TestReadBoundedStopsOnCancellationWithoutDeadlineSupport is MEDIUM 1: a read
// that cannot take a deadline (an ordinary regular file) never fails for a
// cancellation, and readBounded re-checked ctx only when io.ReadAll failed — so
// a cancelled edit_file/apply_patch read could return successful data and the
// tool would proceed to write. Cancellation must stop the read and suppress the
// successful result.
func TestReadBoundedStopsOnCancellationWithoutDeadlineSupport(t *testing.T) {
	t.Parallel()
	r := &stagedReader{first: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, _, err := readBounded(ctx, r, refusingDeadlineSetter{}, 1<<20)
		done <- result{data, err}
	}()
	select {
	case <-r.first: // the read has begun
	case <-time.After(3 * time.Second):
		t.Fatal("readBounded never began reading")
	}
	cancel()
	close(r.release)
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("readBounded returned %d bytes, nil after cancellation without deadline support; want the cancellation", len(res.data))
		}
		if !errors.Is(res.err, context.Canceled) {
			t.Fatalf("readBounded after cancellation = %v, want context.Canceled", res.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a cancelled read without deadline support did not return")
	}
}
