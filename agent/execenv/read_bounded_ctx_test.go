package execenv

import (
	"context"
	"errors"
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
