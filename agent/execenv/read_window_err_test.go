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

// windowStallFile is a reader that parks until its read deadline is set to now
// or the past, then reports the poller's timeout — what a poll-registered
// procfs regular looks like to a windowed read, and the shape
// armStreamDeadline exists to end. It arms nothing on a future deadline: the
// test drives the firing through the cancellation the deadline watcher
// performs, so no timer or sleep decides when the read ends.
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
	// armStreamDeadline yanks a cancelled read's deadline to now. That is the
	// only firing this handle needs.
	if !t.After(time.Now()) {
		f.once.Do(func() { close(f.fired) })
	}
	return nil
}

func (f *windowStallFile) Close() error { return nil }

// TestReadWindowReportsCancellationNotAStall is the low finding's regression:
// armStreamDeadline ends a blocked read by yanking the handle's deadline to
// now, so a cancelled windowed read of a pollable handle arrives at
// readWindowErr as os.ErrDeadlineExceeded. Without a context check first, that
// cancellation is reported as "stream read stalled" — a stall the caller never
// hit.
func TestReadWindowReportsCancellationNotAStall(t *testing.T) {
	t.Parallel()
	src := newWindowStallFile()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, _, err := readNumberedWindowCtx(ctx, "big.log", "big.log", src, 4<<20, new(1), new(2))
		done <- err
	}()
	// Cancel the call while its read is parked. The deadline watcher inside
	// readNumberedWindowCtx yanks the handle's deadline, which releases the
	// read with the poller's timeout.
	cancel()

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
// fired with a live context is a real stall and must keep the shared stall
// error, or the context check above would have silenced the bound it protects.
// The reader hands back the head and then the poller's timeout, so the error
// arrives on the scan's own read, the same place a stalled scan reports it.
func TestReadWindowStillReportsAGenuineStall(t *testing.T) {
	t.Parallel()
	src := &scriptedReader{steps: []scriptedStep{
		{data: []byte("first line\n")},
		{err: os.ErrDeadlineExceeded},
	}}
	_, _, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(2))
	if !errors.Is(err, errGrepStreamStalled) {
		t.Fatalf("windowed read whose deadline fired with a live context = %v, want the stall error", err)
	}
}

// scriptedStep is one Read result: bytes, an error, or both.
type scriptedStep struct {
	data []byte
	err  error
}

// scriptedReader plays back Read results in order, so a test can put an error
// exactly where it matters. A real file's *os.File never returns data and an
// error together; a wrapped afero handle or another reader can, and the
// whole-file path surfaces such an error through io.ReadAll while the scanner
// used to drop it.
type scriptedReader struct {
	steps []scriptedStep
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
	src := &scriptedReader{steps: []scriptedStep{
		{data: []byte(strings.Repeat("row\n", 8))},
		{data: []byte(strings.Repeat("row\n", 56)), err: boom},
	}}
	_, _, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(100))
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
	src := &scriptedReader{steps: []scriptedStep{
		{data: []byte(body[:12])},
		{data: []byte(body[12:]), err: io.EOF},
	}}
	got, meta, err := readNumberedWindowCtx(context.Background(), "big.log", "big.log", src, 4<<20, new(1), new(2))
	if err != nil {
		t.Fatalf("windowed read of a reader ending with data+EOF: %v", err)
	}
	off, lim := 1, 2
	if want := NumberLines(body, &off, &lim); !strings.HasPrefix(got, want) {
		t.Fatalf("window = %q, want %q", got, want)
	}
	if meta.StartLine != 1 || meta.EndLine != 2 || meta.TotalLines != 0 {
		t.Fatalf("meta = %+v, want StartLine=1 EndLine=2 TotalLines=0", meta)
	}
}
