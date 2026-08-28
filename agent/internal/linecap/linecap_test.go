package linecap

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadLine_TerminatedLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello\nworld\n"))
	line, terminated, consumed, err := ReadLine(context.Background(), reader, 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(line) != "hello" || !terminated || consumed != 6 {
		t.Fatalf("got line=%q terminated=%v consumed=%d, want %q true 6", line, terminated, consumed, "hello")
	}
	line2, terminated2, consumed2, err := ReadLine(context.Background(), reader, 100)
	if err != nil {
		t.Fatalf("second err = %v", err)
	}
	if string(line2) != "world" || !terminated2 || consumed2 != 6 {
		t.Fatalf("got line2=%q terminated2=%v consumed2=%d, want %q true 6", line2, terminated2, consumed2, "world")
	}
}

func TestReadLine_UnterminatedTrailingLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("complete\npartial"))
	if _, _, _, err := ReadLine(context.Background(), reader, 100); err != nil {
		t.Fatalf("first line err = %v", err)
	}
	line, terminated, consumed, err := ReadLine(context.Background(), reader, 100)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(line) != "partial" || terminated || consumed != 7 {
		t.Fatalf("got line=%q terminated=%v consumed=%d, want %q false 7", line, terminated, consumed, "partial")
	}
}

func TestReadLine_CleanEOFBetweenLines(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("one\n"))
	if _, _, _, err := ReadLine(context.Background(), reader, 100); err != nil {
		t.Fatalf("first line err = %v", err)
	}
	_, _, _, err := ReadLine(context.Background(), reader, 100)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestReadLine_EmptyInputIsCleanEOF(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	_, _, _, err := ReadLine(context.Background(), reader, 100)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestReadLine_EmptyLineBetweenTerminators(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n\nx\n"))
	line, terminated, consumed, err := ReadLine(context.Background(), reader, 100)
	if err != nil || len(line) != 0 || !terminated || consumed != 1 {
		t.Fatalf("got line=%q terminated=%v consumed=%d err=%v, want empty true 1 nil", line, terminated, consumed, err)
	}
}

// TestReadLine_OverLimitTerminatedLineIsRefusedAndDrained proves the core
// property: a terminated line longer than maxLineBytes is refused (ErrTooLong),
// its content is discarded (line is nil, not a truncated prefix), and the
// NEXT ReadLine call starts cleanly at the following line -- proving the
// reader was fully drained past the oversized line, not left mid-line.
func TestReadLine_OverLimitTerminatedLineIsRefusedAndDrained(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("toolongvalue\nshort\n"))
	line, _, _, err := ReadLine(context.Background(), reader, 5)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	if line != nil {
		t.Fatalf("line = %q, want nil (discarded, not truncated)", line)
	}
	next, terminated, _, err := ReadLine(context.Background(), reader, 100)
	if err != nil {
		t.Fatalf("next line err = %v", err)
	}
	if string(next) != "short" || !terminated {
		t.Fatalf("next line = %q terminated=%v, want %q true -- reader must be positioned right after the oversized line", next, terminated, "short")
	}
}

// TestReadLine_OverLimitUnterminatedTrailingLineIsRefused covers the EOF-
// while-over-limit path specifically.
func TestReadLine_OverLimitUnterminatedTrailingLineIsRefused(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("toolongtrailingfragment"))
	_, _, _, err := ReadLine(context.Background(), reader, 5)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

// TestReadLine_ExactlyAtLimitIsAccepted is the boundary case: a line whose
// length equals maxLineBytes exactly must NOT be refused.
func TestReadLine_ExactlyAtLimitIsAccepted(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("12345\n"))
	line, terminated, _, err := ReadLine(context.Background(), reader, 5)
	if err != nil {
		t.Fatalf("err = %v, want nil for a line exactly at the cap", err)
	}
	if string(line) != "12345" || !terminated {
		t.Fatalf("line = %q terminated=%v, want %q true", line, terminated, "12345")
	}
}

// TestReadLine_BoundedMemoryAcrossManyReadSliceFragments proves the
// bufio.ErrBufferFull accumulation loop works correctly across a line that
// spans many internal buffer refills, using a bufio.Reader with a
// deliberately tiny internal buffer to force many ErrBufferFull iterations
// without needing a multi-megabyte test fixture.
func TestReadLine_BoundedMemoryAcrossManyReadSliceFragments(t *testing.T) {
	content := strings.Repeat("x", 500) + "\n" + "next\n"
	reader := bufio.NewReaderSize(strings.NewReader(content), 16) // tiny buffer forces many ReadSlice fragments
	line, terminated, consumed, err := ReadLine(context.Background(), reader, 1000)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(line) != 500 || !terminated || consumed != 501 {
		t.Fatalf("got len(line)=%d terminated=%v consumed=%d, want 500 true 501", len(line), terminated, consumed)
	}
	next, _, _, err := ReadLine(context.Background(), reader, 1000)
	if err != nil || string(next) != "next" {
		t.Fatalf("next = %q err = %v, want %q nil", next, err, "next")
	}
}

// TestReadLine_TinyBufferOverLimitStillRefusedAndDrained combines the tiny
// internal buffer (many ErrBufferFull fragments) with a cap that fires
// partway through accumulation, proving the discard-but-keep-draining
// behavior holds across multiple fragments, not just a single ReadSlice
// call.
func TestReadLine_TinyBufferOverLimitStillRefusedAndDrained(t *testing.T) {
	content := strings.Repeat("y", 500) + "\n" + "ok\n"
	reader := bufio.NewReaderSize(strings.NewReader(content), 16)
	// maxLineBytes=300 keeps the post-overflow remainder (500-304=196
	// bytes plus its newline) safely inside the drain cap ReadLine now
	// enforces past the overflow point (see
	// TestReadLine_DrainGivesUpAfterAHardCapAndReturnsErrTooLong) -- this
	// test is about the tiny-buffer fragmentation and drain-and-recover
	// properties, not about exercising that cap itself.
	_, _, _, err := ReadLine(context.Background(), reader, 300)
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	next, terminated, _, err := ReadLine(context.Background(), reader, 300)
	if err != nil || string(next) != "ok" || !terminated {
		t.Fatalf("next = %q terminated=%v err=%v, want %q true nil", next, terminated, err, "ok")
	}
}

// infiniteReader returns an endless stream of non-newline bytes, forcing
// ReadLine's overflow drain to keep looping forever unless something (ctx
// cancellation, or a hard drain cap) stops it -- used to prove both are
// real, not just documented intentions.
type infiniteReader struct {
	reads atomic.Int64
}

func (r *infiniteReader) Read(p []byte) (int, error) {
	r.reads.Add(1)
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// gatedReader is infiniteReader's throttled sibling: each Read blocks until
// the test explicitly permits it via allow, so a test can deterministically
// control exactly how many reads happen before reacting -- with no
// dependence on how fast the reading goroutine happens to be scheduled
// relative to the test goroutine. Wall-clock polling instead of this gate
// is flaky here: an unthrottled infiniteReader can race through its own
// drain cap before a polling loop ever wakes up to observe the
// intermediate state.
type gatedReader struct {
	reads atomic.Int64
	allow chan struct{}
}

func (r *gatedReader) Read(p []byte) (int, error) {
	<-r.allow
	r.reads.Add(1)
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// TestReadLine_DrainObservesCancellationWithoutDrainingIndefinitely proves
// ReadLine's drain-to-the-next-newline loop, once a line is over
// maxLineBytes, must check ctx WHILE draining, not just once before
// ReadLine is called (every existing caller already does that separately):
// without an in-loop check, a canceled request would not be noticed until
// the drain reached a real newline or EOF -- unbounded, since nothing else
// bounds how much of the stream a single pathological line can force this
// loop to consume.
func TestReadLine_DrainObservesCancellationWithoutDrainingIndefinitely(t *testing.T) {
	src := &gatedReader{allow: make(chan struct{})}
	reader := bufio.NewReaderSize(src, 16) // tiny buffer: each Read fully satisfies one ReadSlice call
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// maxLineBytes is deliberately large here: it must stay well
		// clear of both the over-limit threshold AND the drain cap
		// (TestReadLine_DrainGivesUpAfterAHardCapAndReturnsErrTooLong)
		// for the (small, deterministic) number of reads this test
		// permits below -- otherwise the drain cap, not this test's
		// explicit cancel, would be what actually stops the loop,
		// proving the wrong thing.
		_, _, _, err := ReadLine(ctx, reader, 1_000_000)
		done <- err
	}()

	// Deterministically let exactly n reads complete -- many iterations
	// into the drain, proving cancellation is noticed MID-drain, not
	// merely checked once at entry -- before canceling.
	const n = 50
	for i := range n {
		select {
		case src.allow <- struct{}{}:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out feeding read %d -- ReadLine is not pulling from reader as expected", i)
		}
	}
	cancel()
	// ReadLine only checks ctx between reads, not while one is already in
	// flight: the goroutine may already be parked waiting for its NEXT
	// read by the time cancel() runs above, so keep offering permits --
	// harmless once ReadLine has already returned on its own, since
	// nothing will be left to receive them -- until it actually finishes.
	// done is received exactly once, here, not split across two selects:
	// a single-value channel drained by whichever of two separate
	// receives happens to win the race leaves the OTHER one blocked
	// forever waiting for a second value that will never arrive.
	for {
		select {
		case src.allow <- struct{}{}:
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
			return
		case <-time.After(5 * time.Second):
			t.Fatal("ReadLine did not return within 5s of cancellation -- the drain never checks ctx")
		}
	}
}

// TestReadLine_DrainGivesUpAfterAHardCapAndReturnsErrTooLong covers the
// same review finding's other half: even with ctx never canceled, a
// corrupt tail that never yields a newline must still terminate
// deterministically, not drain the rest of the file (or stream)
// unboundedly one bufio refill at a time.
func TestReadLine_DrainGivesUpAfterAHardCapAndReturnsErrTooLong(t *testing.T) {
	src := &infiniteReader{}
	reader := bufio.NewReaderSize(src, 16)
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		_, _, _, err := ReadLine(ctx, reader, 5)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrTooLong) {
			t.Fatalf("err = %v, want ErrTooLong -- a corrupt tail that never yields a newline must give up deterministically via a hard drain cap, treated the same as hitting EOF while over limit", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine never returned -- an unterminated, ever-growing line with no ctx cancellation must still terminate via a hard drain cap")
	}
}
