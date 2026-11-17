package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSSEReadTimeoutErrorMatchesSentinelWithoutLosingItsDuration(t *testing.T) {
	t.Parallel()
	err := error(&sseReadTimeoutError{timeout: 90 * time.Second})

	// Callers classify this with errors.Is against the sentinel, so Is is what
	// makes the concrete type reachable at all; without it the wrapper reads as
	// an unrelated error and the SSE stall is misclassified as a transport fault.
	if !errors.Is(err, ErrSSEReadTimeout) {
		t.Fatalf("errors.Is(%v, ErrSSEReadTimeout) = false, want true", err)
	}
	if errors.Is(err, io.EOF) {
		t.Fatal("the SSE read timeout must not match an unrelated sentinel")
	}
	// The duration is the diagnostic half: a bare sentinel cannot say how long
	// the stream was silent.
	if got := err.Error(); !strings.Contains(got, "1m30s") {
		t.Fatalf("Error() = %q, want it to name the 1m30s timeout", got)
	}
}

func TestParseSSE_BasicEvent(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	input := "data: hello\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if string(events[0].Data) != "hello" {
		t.Errorf("expected data 'hello', got %q", string(events[0].Data))
	}
}

func TestParseSSE_MultipleEvents(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	input := "data: one\n\ndata: two\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if string(events[0].Data) != "one" {
		t.Errorf("expected 'one', got %q", events[0].Data)
	}
	if string(events[1].Data) != "two" {
		t.Errorf("expected 'two', got %q", events[1].Data)
	}
}

func TestParseSSE_WithEventType(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	input := "event: message\ndata: payload\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Event != "message" {
		t.Errorf("expected event type 'message', got %q", events[0].Event)
	}
}

func TestParseSSE_ContextCancellation_WithTimeout(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// The blocking path cannot detect context cancellation while stuck on
	// ReadString. When a StreamReadTimeout is configured, context cancellation
	// is handled via select, so it works reliably.
	pr, pw := io.Pipe()
	defer pw.Close() //nolint:errcheck
	go func() {
		_, _ = pw.Write([]byte("data: hello\n\n"))
		// Stall forever.
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(time.Second))
	// Context fires before the stream-read timeout.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestParseSSE_StreamReadTimeout_FiresOnStall(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Create a reader that sends one event then stalls.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("data: hello\n\n"))
		// Stall forever (don't write anything else, don't close).
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(50*time.Millisecond))

	if err == nil {
		t.Fatal("expected error from stream read timeout, got nil")
	}
	if !errors.Is(err, ErrSSEReadTimeout) {
		t.Errorf("errors.Is(%v, ErrSSEReadTimeout) = false, want true", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event before timeout, got %d", len(events))
	}
	_ = pw.Close()
}

func TestParseSSE_StreamReadTimeout_ResetsOnData(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Verify that the timeout resets after each line of data, not just events.
	pr, pw := io.Pipe()
	go func() {
		// Write lines slowly but well within the timeout window (10x margin).
		_, _ = pw.Write([]byte("data: first\n\n"))
		time.Sleep(20 * time.Millisecond)
		_, _ = pw.Write([]byte("data: second\n\n"))
		time.Sleep(20 * time.Millisecond)
		_, _ = pw.Write([]byte("data: third\n\n"))
		time.Sleep(20 * time.Millisecond)
		_ = pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var events []SSEEvent
	err := ParseSSE(ctx, pr, func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(200*time.Millisecond))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
}

func TestParseSSE_StreamReadTimeout_ZeroMeansNoTimeout(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Zero timeout should behave like the original (no per-read timeout).
	input := "data: hello\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	}, WithStreamReadTimeout(0))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestParseSSE_NoOptions_PreservesExistingBehavior(t *testing.T) {
	// Not parallel: FuzzRootScenarioReplay calls this test directly with a shared t.
	// Verify that calling ParseSSE without options still works identically.
	input := "event: delta\ndata: content\n\nevent: done\ndata: [DONE]\n\n"
	var events []SSEEvent
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Event != "delta" {
		t.Errorf("expected event type 'delta', got %q", events[0].Event)
	}
	if string(events[1].Data) != "[DONE]" {
		t.Errorf("expected '[DONE]', got %q", events[1].Data)
	}
}

func TestAPITimeoutSourceForSSERecognizesItsReadTimeout(t *testing.T) {
	t.Parallel()
	if got := APITimeoutSourceForSSE(&sseReadTimeoutError{timeout: 5e9}); got != APITimeoutSSERead {
		t.Fatalf("APITimeoutSourceForSSE(sseReadTimeoutError) = %q, want %q", got, APITimeoutSSERead)
	}
}

// TestParseSSELineFraming pins how a body's bytes become events: which lines
// contribute to the payload, how they are joined, and what a body that stops
// part way through an event delivers. The mid-event cases are deliberate
// contract, not accidents: the parser flushes whatever it has accumulated when
// the body ends, so a caller that signs off on a dropped connection sees the
// chunks that did arrive, in order. Changing the loop that scans these lines
// must not change any row here.
func TestParseSSELineFraming(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		want  []SSEEvent
	}{
		{
			name:  "multi-line data joins with a newline",
			input: "data: line1\ndata: line2\n\n",
			want:  []SSEEvent{{Data: []byte("line1\nline2")}},
		},
		{
			name:  "crlf terminators are stripped",
			input: "data: a\r\n\r\n",
			want:  []SSEEvent{{Data: []byte("a")}},
		},
		{
			// The stream ends right after the CR, so this pins the trailing-CR
			// strip: the CR ends the line and is not delivered as data.
			name:  "a trailing carriage return at end of stream is stripped",
			input: "data: a\r",
			want:  []SSEEvent{{Data: []byte("a")}},
		},
		{
			// SSE lists CR, LF and CRLF as line terminators. One read carries
			// both lines, so the scan has to split between them; reading the CR
			// as data merges them into one payload with the CR left in it.
			name:  "a bare CR ends a line",
			input: "data: a\rdata: b\n\n",
			want:  []SSEEvent{{Data: []byte("a\nb")}},
		},
		{
			// A body framed only with CR, its blank lines included: both events
			// have to come out of it.
			name:  "cr-only framing separates events",
			input: "data: a\r\rdata: b\r\r",
			want:  []SSEEvent{{Data: []byte("a")}, {Data: []byte("b")}},
		},
		{
			// CRLF is one terminator. Split at the CR and then again at the LF,
			// the blank line between them would flush the event before its data
			// line arrived.
			name:  "a CRLF pair is one line ending",
			input: "event: delta\r\ndata: a\r\n\r\n",
			want:  []SSEEvent{{Event: "delta", Data: []byte("a")}},
		},
		{
			// The CR is the last byte of the reader's 4096-byte buffer, so only
			// the next fragment can say whether it is a bare CR or half of a
			// CRLF.
			name:  "a CR at the read buffer boundary still ends the line",
			input: "data: " + strings.Repeat("a", 4089) + "\r\n\r\n",
			want:  []SSEEvent{{Data: []byte(strings.Repeat("a", 4089))}},
		},
		{
			name:  "data prefix needs no space",
			input: "data:x\n\n",
			want:  []SSEEvent{{Data: []byte("x")}},
		},
		{
			name:  "every leading space after the prefix is dropped",
			input: "data:   x\n\n",
			want:  []SSEEvent{{Data: []byte("x")}},
		},
		{
			name:  "comments and unknown fields contribute nothing",
			input: ": keepalive\nid: 7\nretry: 10\ndata: a\n\n",
			want:  []SSEEvent{{Data: []byte("a")}},
		},
		{
			name:  "blank lines alone emit no event",
			input: "\n\n\n",
			want:  nil,
		},
		{
			name:  "a blank line ends the event",
			input: "data: a\n\ndata: b\n\n",
			want:  []SSEEvent{{Data: []byte("a")}, {Data: []byte("b")}},
		},
		{
			// The drop lands between two data lines: the parser cannot know
			// another line was coming, so the event still flushes at EOF.
			name:  "drop between data lines delivers the partial event",
			input: "data: a\ndata: b",
			want:  []SSEEvent{{Data: []byte("a\nb")}},
		},
		{
			// No newline at all: the fragment is still the event's payload.
			name:  "drop mid data line delivers the fragment",
			input: `data: {"x":`,
			want:  []SSEEvent{{Data: []byte(`{"x":`)}},
		},
		{
			// An event name with no data flushes with an empty, non-nil Data.
			name:  "drop after an event line delivers the named empty event",
			input: "event: delta\n",
			want:  []SSEEvent{{Event: "delta", Data: []byte{}}},
		},
		{
			name:  "an event name is trimmed and carried to the flush",
			input: "event:  message \ndata: payload\n\n",
			want:  []SSEEvent{{Event: "message", Data: []byte("payload")}},
		},
		{
			// Longer than bufio's 4096-byte buffer: the scan has to join
			// fragments before it can see the terminator.
			name:  "a line longer than the read buffer is delivered whole",
			input: "data: " + strings.Repeat("x", 20000) + "\n\ndata: short\n\n",
			want: []SSEEvent{
				{Data: []byte(strings.Repeat("x", 20000))},
				{Data: []byte("short")},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectSSE(strings.NewReader(tc.input))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("events = %d %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i].Event != tc.want[i].Event {
					t.Errorf("event %d name = %q, want %q", i, got[i].Event, tc.want[i].Event)
				}
				if !bytes.Equal(got[i].Data, tc.want[i].Data) {
					t.Errorf("event %d data = %q, want %q", i, got[i].Data, tc.want[i].Data)
				}
				if tc.want[i].Data != nil && got[i].Data == nil {
					t.Errorf("event %d data is nil, want an empty non-nil slice", i)
				}
			}
		})
	}
}

// TestParseSSEPreservesUTF8SplitAcrossReads pins that the parser is byte
// oriented, not rune oriented: a multi-byte rune delivered in two pieces must
// arrive whole. The buffer-boundary row is the one a reused read buffer can
// break, because the rune straddles the fill that ends the buffer.
func TestParseSSEPreservesUTF8SplitAcrossReads(t *testing.T) {
	t.Parallel()
	// 6 bytes of "data: " plus 4089 padding puts the first byte of "é" at
	// offset 4095, so the rune straddles a 4096-byte read.
	const pad = 4089
	want := strings.Repeat("a", pad) + "é"
	input := "data: " + want + "\n\n"

	for _, tc := range []struct {
		name string
		r    io.Reader
	}{
		{name: "one byte per read", r: &oneByteReader{data: []byte(input)}},
		{name: "rune straddling the read buffer", r: strings.NewReader(input)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectSSE(tc.r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("events = %d, want 1", len(got))
			}
			if !bytes.Equal(got[0].Data, []byte(want)) {
				t.Fatalf("data of %d bytes did not survive the split reads intact", len(got[0].Data))
			}
		})
	}
}

// TestParseSSETreatsWrappedEOFAsEndOfStream pins the stream-end test on both
// read paths. End of stream is decided with errors.Is, so a reader that reports
// the end of its body through a wrapper still ends the stream normally, and the
// accumulated event still flushes; an unrelated read error is still returned to
// the caller unchanged.
func TestParseSSETreatsWrappedEOFAsEndOfStream(t *testing.T) {
	t.Parallel()
	wrappedEOF := fmt.Errorf("body closed after the last byte: %w", io.EOF)
	for _, tc := range []struct {
		name string
		opts []SSEOption
	}{
		{name: "blocking path"},
		{name: "read-timeout path", opts: []SSEOption{WithStreamReadTimeout(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collectSSE(sseErrorReader{wrappedEOF}, tc.opts...)
			if err != nil {
				t.Fatalf("a wrapped EOF must end the stream, not fail it: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("events = %v, want none from a body that carried none", got)
			}

			boom := errors.New("connection reset")
			if _, err := collectSSE(sseErrorReader{boom}, tc.opts...); !errors.Is(err, boom) {
				t.Fatalf("unrelated read error = %v, want %v", err, boom)
			}
		})
	}
}

// TestParseSSEDataIsOwnedByTheCaller pins the handoff: the slice a callback
// receives stays valid after the callback returns and after later reads have
// reused the line scan's buffer. The scan reads into one buffer it refills for
// every line, so the payload a callback keeps must not alias those bytes.
func TestParseSSEDataIsOwnedByTheCaller(t *testing.T) {
	t.Parallel()
	var kept [][]byte
	input := "data: AAAA\n\ndata: BBBB\n\ndata: CCCC\n\n"
	err := ParseSSE(context.Background(), strings.NewReader(input), func(ev SSEEvent) error {
		kept = append(kept, ev.Data)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"AAAA", "BBBB", "CCCC"}
	if len(kept) != len(want) {
		t.Fatalf("events = %d, want %d", len(kept), len(want))
	}
	for i := range want {
		if string(kept[i]) != want[i] {
			t.Fatalf("event %d data = %q after the parse, want %q: the payload must not alias a reused read buffer", i, kept[i], want[i])
		}
	}
}

// TestParseSSEPerLineAllocationsDoNotScaleWithLineCount pins the reader's
// per-line cost. A comment line contributes nothing to an event, so every
// allocation measured here belongs to reading the line itself: the scan must
// reuse one buffer rather than allocate a string per line. Both read paths are
// covered, because each has its own line loop.
func TestParseSSEPerLineAllocationsDoNotScaleWithLineCount(t *testing.T) {
	// Not parallel: testing.AllocsPerRun must not run beside other tests.
	measure := func(t *testing.T, lines int, sep string, opts ...SSEOption) float64 {
		t.Helper()
		var body bytes.Buffer
		for range lines {
			body.WriteString(": keepalive" + sep)
		}
		raw := body.Bytes()
		var parseErr error
		allocs := testing.AllocsPerRun(20, func() {
			parseErr = ParseSSE(context.Background(), bytes.NewReader(raw), func(SSEEvent) error { return nil }, opts...)
		})
		if parseErr != nil {
			t.Fatalf("parse %d lines: %v", lines, parseErr)
		}
		return allocs
	}
	for _, tc := range []struct {
		name string
		sep  string
		opts []SSEOption
	}{
		{name: "blocking path", sep: "\n"},
		{name: "read-timeout path", sep: "\n", opts: []SSEOption{WithStreamReadTimeout(time.Hour)}},
		// CR-framed lines reach the scan several to a read, so it has to serve
		// them out of its buffer without taking an allocation per line.
		{name: "blocking path with CR line endings", sep: "\r"},
		{name: "read-timeout path with CR line endings", sep: "\r", opts: []SSEOption{WithStreamReadTimeout(time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			small, large := measure(t, 32, tc.sep, tc.opts...), measure(t, 128, tc.sep, tc.opts...)
			if large > small {
				t.Fatalf("allocations scale with line count: %v for 32 lines, %v for 128 lines; each line must reuse the read buffer", small, large)
			}
		})
	}
}

// BenchmarkParseSSELines measures the SSE reader over a synthetic multi-KB
// stream: scanning lines and flushing events, with a callback that only reads
// the payload, as the protocol adapters do.
func BenchmarkParseSSELines(b *testing.B) {
	var body bytes.Buffer
	payload := `{"id":"c1","model":"m-wire","choices":[{"index":0,"delta":{"content":"` +
		strings.Repeat("abcdefgh", 25) + `"}}]}`
	for range 256 {
		body.WriteString("data: " + payload + "\n\n")
	}
	body.WriteString("data: [DONE]\n\n")
	raw := body.Bytes()

	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var n int
		if err := ParseSSE(context.Background(), bytes.NewReader(raw), func(ev SSEEvent) error {
			n += len(ev.Data)
			return nil
		}); err != nil {
			b.Fatalf("parse: %v", err)
		}
		if n == 0 {
			b.Fatal("no data reached the callback")
		}
	}
}
