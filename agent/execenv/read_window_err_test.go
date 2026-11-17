package execenv

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// windowStallFile is a reader that never produces a byte until its read
// deadline fires, then reports the poller's timeout — the shape
// armStreamDeadline exists to end, and what a poll-registered procfs regular
// looks like to a windowed read.
type windowStallFile struct {
	fired chan struct{}
	once  sync.Once
}

func newWindowStallFile() *windowStallFile { return &windowStallFile{fired: make(chan struct{})} }

func (f *windowStallFile) Read([]byte) (int, error) {
	<-f.fired
	return 0, os.ErrDeadlineExceeded
}

func (f *windowStallFile) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	go func() {
		if d > 0 {
			time.Sleep(d)
		}
		f.once.Do(func() { close(f.fired) })
	}()
	return nil
}

func (f *windowStallFile) Close() error { return nil }

// TestReadWindowReportsCancellationNotAStall is the low finding's regression:
// armStreamDeadline ends a blocked read by yanking the handle's deadline to now,
// so a cancelled windowed read of a pollable handle arrives at readWindowErr as
// os.ErrDeadlineExceeded. Without a context check first, that cancellation is
// reported as "stream read stalled" — a 30-second stall the caller never hit.
func TestReadWindowReportsCancellationNotAStall(t *testing.T) {
	t.Parallel()
	src := newWindowStallFile()
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after the read parks, the way a tool call's context ends.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := readNumberedWindowCtx(ctx, "big.log", "big.log", src, 4<<20, new(1), new(2))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled windowed read = %v, want context.Canceled", err)
		}
		if errors.Is(err, errGrepStreamStalled) {
			t.Fatalf("cancelled windowed read reported a stall: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled windowed read never returned")
	}
}

// TestReadWindowStillReportsAGenuineStall pins the other half: a deadline that
// fires with a live context is a real stall and must keep the shared stall
// error, or the context check above would have silenced the bound it protects.
func TestReadWindowStillReportsAGenuineStall(t *testing.T) {
	// Not parallel: shrinks the package-wide grepReadStallTimeout.
	orig := grepReadStallTimeout
	grepReadStallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { grepReadStallTimeout = orig })

	src := newWindowStallFile()
	done := make(chan error, 1)
	go func() {
		_, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(2))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errGrepStreamStalled) {
			t.Fatalf("stalled windowed read = %v, want the stall error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled windowed read never returned: the stall bound did not fire with a live context")
	}
}

// scriptedReader plays back Read results in order, so a test can put an error
// exactly where it matters. A real file's *os.File never returns data and an
// error together; a wrapped afero handle or another reader can, and the
// whole-file path surfaces such an error through io.ReadAll while the scanner
// used to drop it.
type scriptedReader struct {
	steps []struct {
		data []byte
		err  error
	}
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if len(r.steps) == 0 {
		return 0, io.EOF
	}
	step := r.steps[0]
	r.steps = r.steps[1:]
	return copy(p, step.data), step.err
}

// TestReadWindowKeepsAnErrorReturnedWithData is the other low finding's
// regression: a Read that returns n > 0 with a non-EOF error must surface that
// error rather than report a clean EOF over a window cut short by it.
func TestReadWindowKeepsAnErrorReturnedWithData(t *testing.T) {
	t.Parallel()
	boom := errors.New("backing store failed mid-read")
	// The head read consumes the first step cleanly, so the failing read is one
	// the scanner's own refill performs: the last bytes of the file arrive with
	// a non-EOF error, and the error must not be dropped.
	src := &scriptedReader{steps: []struct {
		data []byte
		err  error
	}{
		{data: []byte(strings.Repeat("row\n", 8))},
		{data: []byte(strings.Repeat("row\n", 56)), err: boom},
	}}
	_, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(100))
	if !errors.Is(err, boom) {
		t.Fatalf("windowed read over a reader that fails with its data = %v, want %v", err, boom)
	}
}

// TestReadWindowEndsCleanlyOnDataWithEOF pins the case the fix must not break: a
// reader that returns its final bytes together with io.EOF still ends the window
// cleanly, as io.ReadAll's callers rely on.
func TestReadWindowEndsCleanlyOnDataWithEOF(t *testing.T) {
	t.Parallel()
	body := "first\nsecond\nthird\n"
	src := &scriptedReader{steps: []struct {
		data []byte
		err  error
	}{
		{data: []byte(body[:8])},
		{data: []byte(body[8:]), err: io.EOF},
	}}
	got, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(2))
	if err != nil {
		t.Fatalf("windowed read of a reader ending with data+EOF: %v", err)
	}
	off, lim := 1, 2
	if want := NumberLinesBytes([]byte(body), &off, &lim); got != want {
		t.Fatalf("window = %q, want %q", got, want)
	}
}
