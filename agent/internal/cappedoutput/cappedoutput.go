// Package cappedoutput bounds the memory a child process's captured stdout or
// stderr can hold. A firehose like `yes` fills an unbounded buffer at pipe
// speed well inside any command timeout, which a container memory cap turns
// into a SIGKILL (#4557, #4561). Every capture of a child's output that is not
// otherwise bounded writes through a Buffer.
package cappedoutput

import (
	"fmt"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// OmissionMarker joins the retained head and tail of a stream whose capture
// tripped the cap, in the same family as the registry truncator's head+tail
// "[... N lines omitted ...]" marker: the reader keeps the beginning and the
// end of the output plus an explicit count of what is missing.
const OmissionMarker = "\n[... %d bytes omitted ...]\n"

// DefaultCapBytes is the per-stream bound New falls back to for a non-positive
// cap. 8 MiB matches execenv's grep retained-output budget and the streamed
// jobs path's retention budget, so every capture regime costs the same order of
// memory.
const DefaultCapBytes = 8 << 20

// Buffer is a stdout/stderr capture bounded at a fixed number of bytes. It
// keeps the exact stream up to cap bytes; past the cap it retains the first
// cap/2 and the last cap-cap/2 bytes around an omission marker, so a firehose
// cannot grow the capture with the stream. Write always consumes: a full pipe
// would block the child until its timeout, and the copier must keep draining
// for the tail window to track the stream's real end. This bound is about
// memory, not presentation; callers still bound the text they show.
//
// One copier goroutine owns a buffer from Start until Wait joins it, so no
// method needs a lock.
type Buffer struct {
	capBytes int
	total    int
	buf      []byte // exact stream while total <= cap; released once the cap trips
	head     []byte // first cap/2 bytes, frozen when the cap trips
	tail     []byte // rolling window of the last cap-cap/2 bytes, in ring order
	tailW    int    // ring index the next written byte overwrites
	over     bool   // whether the cap tripped
}

// New returns a capture bounded at capBytes per stream. A non-positive cap
// falls back to DefaultCapBytes rather than silently removing the bound.
func New(capBytes int) *Buffer {
	if capBytes <= 0 {
		capBytes = DefaultCapBytes
	}
	return &Buffer{capBytes: capBytes}
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.over {
		b.writeTail(p)
		return len(p), nil
	}
	if b.total <= b.capBytes {
		b.buf = append(b.buf, p...)
		return len(p), nil
	}
	// The cap just tripped: freeze the head, seed the tail window with the
	// newest bytes, and release the under-cap buffer in one step.
	combined := b.buf
	b.buf = nil
	combined = append(combined, p...)
	headLen := b.capBytes / 2
	b.head = append([]byte(nil), combined[:headLen]...)
	b.tail = make([]byte, b.capBytes-headLen)
	b.writeTail(combined[headLen:])
	b.over = true
	return len(p), nil
}

// writeTail feeds p into the rolling tail window, evicting the oldest bytes;
// p larger than the window keeps only its newest len(tail) bytes.
func (b *Buffer) writeTail(p []byte) {
	if len(b.tail) == 0 {
		return
	}
	if len(p) >= len(b.tail) {
		copy(b.tail, p[len(p)-len(b.tail):])
		b.tailW = 0
		return
	}
	n := copy(b.tail[b.tailW:], p)
	if n < len(p) {
		copy(b.tail, p[n:])
	}
	b.tailW = (b.tailW + len(p)) % len(b.tail)
}

// String returns the capture: the exact stream at or under the cap, else the
// retained head and tail around an omission marker counting every byte not
// present. The cut edges are rune-trimmed so a multibyte character straddling a
// cut is dropped rather than rendered as a replacement character.
func (b *Buffer) String() string {
	if !b.over {
		return string(b.buf)
	}
	head := runetrim.TrimTrailingPartial(b.head)
	tail := runetrim.TrimLeadingPartial(b.TailView())
	omitted := b.total - len(head) - len(tail)
	return string(head) + fmt.Sprintf(OmissionMarker, omitted) + string(tail)
}

// Truncated reports whether the cap tripped: String() is then the retained
// head and tail around an omission marker, not the exact stream.
func (b *Buffer) Truncated() bool { return b.over }

// TailView unwinds the rolling tail window into stream order. It is the
// authoritative last bytes of the stream once the cap tripped, for a caller
// that must not parse the rendered marker back out of String().
func (b *Buffer) TailView() []byte {
	view := make([]byte, 0, len(b.tail))
	view = append(view, b.tail[b.tailW:]...)
	view = append(view, b.tail[:b.tailW]...)
	return view
}
