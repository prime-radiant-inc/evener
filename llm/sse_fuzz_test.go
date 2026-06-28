package llm

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
	"time"
)

// oneByteReader delivers its payload one byte per Read call, then io.EOF. It
// forces ParseSSE through a different read-segmentation than a bytes.Reader,
// which is what makes the chunk-invariance oracle below meaningful.
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

// collectSSE runs ParseSSE over r and returns the events it emitted. Any parse
// error is returned alongside the events collected so far.
func collectSSE(r io.Reader, opts ...SSEOption) ([]SSEEvent, error) {
	var got []SSEEvent
	err := ParseSSE(context.Background(), r, func(ev SSEEvent) error {
		got = append(got, ev)
		return nil
	}, opts...)
	return got, err
}

// FuzzParseSSE hunts panics in the SSE parser and asserts two metamorphic
// invariants that "no panic" alone would miss:
//
//   - Chunk-invariance: parsing the same bytes one byte at a time must yield the
//     same events as parsing them in a single buffer. A parser whose result
//     depends on read boundaries (a bufio/buffering bug) violates this.
//   - Path-agreement: the blocking code path (no timeout) and the
//     timeout-goroutine code path (llm/sse.go) must agree over the same bytes.
//     This guards against the two ParseSSE implementations drifting apart.
func FuzzParseSSE(f *testing.F) {
	seeds := [][]byte{
		[]byte("data: {\"type\":\"x\"}\n\n"),
		[]byte("event: delta\ndata: \n\n: comment\n\n"),
		[]byte("event: message\ndata: line1\ndata: line2\n\n"),
		[]byte(": just a comment\n\n"),
		[]byte("data:[DONE]\n\n"),
		[]byte("data: a\r\nevent: b\r\n\r\n"),
		[]byte("event:no-space\ndata:packed\n\n"),
		[]byte("garbage without newline"),
		[]byte("\n\n\n"),
		[]byte(""),
		[]byte("data: \xff\xfe invalid utf8 \x00\n\n"),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	const longTimeout = time.Hour

	f.Fuzz(func(t *testing.T, raw []byte) {
		whole, errWhole := collectSSE(bytes.NewReader(raw))

		chunked, errChunked := collectSSE(&oneByteReader{data: raw})
		if !sameSSEResult(whole, errWhole, chunked, errChunked) {
			t.Fatalf("SSE chunk-invariance violated for %q:\n whole=%v (err=%v)\n chunked=%v (err=%v)",
				raw, whole, errWhole, chunked, errChunked)
		}

		timed, errTimed := collectSSE(bytes.NewReader(raw), WithStreamReadTimeout(longTimeout))
		if !sameSSEResult(whole, errWhole, timed, errTimed) {
			t.Fatalf("SSE path divergence (blocking vs timeout) for %q:\n blocking=%v (err=%v)\n timeout=%v (err=%v)",
				raw, whole, errWhole, timed, errTimed)
		}
	})
}

// sameSSEResult reports whether two ParseSSE runs produced the same events and
// the same error disposition (both nil or both non-nil).
func sameSSEResult(a []SSEEvent, aerr error, b []SSEEvent, berr error) bool {
	if (aerr == nil) != (berr == nil) {
		return false
	}
	return reflect.DeepEqual(a, b)
}
