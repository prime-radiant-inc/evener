package execenv

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// rgTailRecords is how many of a stream's last complete records the rg arm's
// capture keeps, so a truncated search still shows how the match stream ends.
const rgTailRecords = 5

// rgTailSlotBytes caps one tail record: a matching line can be as long as the
// per-file read cap, so a slot keeps the record's first rgTailSlotBytes and
// states the size of the rest rather than retaining it.
const rgTailSlotBytes = 16 << 10

// rgTailWindowBytes is the raw tail the capture retains: one record more than it
// presents, so the last rgTailRecords complete records are whole inside the
// window even when every one of them sits at the slot cap. The window may open
// inside a record, whose bytes before the next record boundary are a fragment
// and are never presented.
const rgTailWindowBytes = (rgTailRecords + 1) * rgTailSlotBytes

// rgCaptureDiagnosticBytes bounds the stdout prefix a FAILED search shows beside
// its stderr. That text is a diagnostic to read, not records to parse, and 64
// KiB is far past any real ripgrep diagnostic.
const rgCaptureDiagnosticBytes = 64 << 10

// rgCapture is the rg search arm's stdout sink: it keeps the stream's first
// capBytes byte-exact and, past that cap, a rolling window of the stream's tail.
// Memory is bounded by construction — capBytes, rgTailWindowBytes and, for a
// failed search, rgCaptureDiagnosticBytes — so no capture, emit or parse step
// grows with ripgrep's output.
//
// It bounds the stream; it does not choose records. A stream that fits whole is
// retained whole, and what the cap leaves out is disclosed to the caller rather
// than repaired, so the retained subset of a truncated search carries no
// cross-run guarantee (a documented non-requirement).
//
// One copier owns a capture from Start until the exec joins it, so no method
// needs a lock.
type rgCapture struct {
	capBytes int

	head   []byte // the stream's first capBytes, byte-exact
	window []byte // rolling: the last rgTailWindowBytes bytes written past the cap
	total  int
	over   bool
}

// newRgCapture returns a capture bounded at capBytes. A non-positive cap falls
// back to the production default rather than silently removing the bound.
func newRgCapture(capBytes int) *rgCapture {
	if capBytes <= 0 {
		capBytes = maxCapturedOutputBytes
	}
	return &rgCapture{capBytes: capBytes}
}

// Write consumes the stream, always reporting a full write: a full pipe would
// stall the child until the command timeout.
func (c *rgCapture) Write(p []byte) (int, error) {
	written := len(p)
	c.total += written
	if !c.over {
		if room := c.capBytes - len(c.head); room > 0 {
			n := min(len(p), room)
			c.head = append(c.head, p[:n]...)
			p = p[n:]
		}
		c.over = len(c.head) >= c.capBytes
	}
	if len(p) > 0 && c.over {
		c.window = append(c.window, p...)
		if extra := len(c.window) - rgTailWindowBytes; extra > 0 {
			c.window = append(c.window[:0], c.window[extra:]...)
		}
	}
	return written, nil
}

// heldBytes is what the capture holds, for its own accounting ceiling.
func (c *rgCapture) heldBytes() int { return len(c.head) + len(c.window) }

// diagnostics is the bounded stdout prefix a failed search shows beside its
// stderr.
func (c *rgCapture) diagnostics() string {
	return string(c.head[:min(len(c.head), rgCaptureDiagnosticBytes)])
}

// rgCaptureView is what a completed search reads from its capture: the head cut
// back to its last complete record, the stream's last complete records, the
// SOURCE bytes those two hold, and how many stream bytes neither holds.
type rgCaptureView struct {
	head     string
	tail     []string
	retained int
	omitted  int
}

// view cuts the capture into the bytes a search can present, using the record
// framing ripgrepOutputLines reads. The head ends where the stream was cut, which
// is mid-record, so it is cut back to its last complete record rather than
// presenting a fragment as a result.
func (c *rgCapture) view(dir string, filesOnly bool) rgCaptureView {
	prefix := []byte(strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator))
	head := c.head
	if c.over {
		head = head[:lastRecordEnd(head, prefix, filesOnly)]
	}
	tail, tailBytes := tailRecords(c.window, prefix, filesOnly)
	// A capped slot carries synthetic omission text, which is not stream bytes:
	// counting the slots instead of their records understates the omission, and
	// once the slots' text outweighs what was really dropped the count goes
	// NEGATIVE — which the caller reads as "nothing was left out" and suppresses
	// the disclosure entirely.
	retained := len(head) + tailBytes
	return rgCaptureView{head: string(head), tail: tail, retained: retained, omitted: c.total - retained}
}

// rgRecordEnds is the end offset of every complete record in b, in the framing
// ripgrepOutputLines reads: a path — which may hold newlines — up to the NUL that
// ends it, then the payload line up to its newline (a files-only record ends at
// its NUL); any bytes that do not start a path are a pathless line up to its
// newline. Bytes after the last end are a partial record. A prefix of the
// searched directory is not a pathless line, so a chunk boundary inside the
// prefix waits rather than splitting a record there.
func rgRecordEnds(b, prefix []byte, filesOnly bool) []int {
	var ends []int
	for i := 0; i < len(b); {
		rest := b[i:]
		if bytes.HasPrefix(rest, prefix) || bytes.HasPrefix(prefix, rest) {
			nul := bytes.IndexByte(rest, 0)
			if nul < 0 {
				break // the path has not ended: nothing here is a complete record
			}
			if filesOnly {
				ends, i = append(ends, i+nul+1), i+nul+1
				continue
			}
			nl := bytes.IndexByte(rest[nul+1:], '\n')
			if nl < 0 {
				break // the payload line has not ended
			}
			ends, i = append(ends, i+nul+nl+2), i+nul+nl+2
			continue
		}
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		ends, i = append(ends, i+nl+1), i+nl+1
	}
	return ends
}

// lastRecordEnd is where the last complete record in b ends, or 0 when b holds
// none.
func lastRecordEnd(b, prefix []byte, filesOnly bool) int {
	ends := rgRecordEnds(b, prefix, filesOnly)
	if len(ends) == 0 {
		return 0
	}
	return ends[len(ends)-1]
}

// tailRecords is the last rgTailRecords complete records in b, in stream order,
// each bounded by capTailSlot, with the SOURCE bytes those records occupy. b
// opens wherever the cap tripped, inside a record, so it is resynchronized first:
// the bytes before its first genuine record boundary are a fragment of a record
// that started earlier and are left out.
func tailRecords(b, prefix []byte, filesOnly bool) ([]string, int) {
	b = b[resync(b, filesOnly):]
	ends := rgRecordEnds(b, prefix, filesOnly)
	if len(ends) == 0 {
		return nil, 0
	}
	var recs [][]byte
	for i, end := range ends {
		start := 0
		if i > 0 {
			start = ends[i-1]
		}
		recs = append(recs, b[start:end])
	}
	if len(recs) > rgTailRecords {
		recs = recs[len(recs)-rgTailRecords:]
	}
	out := make([]string, 0, len(recs))
	source := 0
	for _, rec := range recs {
		source += len(rec)
		out = append(out, capTailSlot(rec))
	}
	return out, source
}

// resync drops the bytes of a window that opens inside a record, returning the
// offset of the first complete record for the framing. A window's start is
// wherever the cap tripped, so its leading bytes are a record's tail: a NUL ends
// the path they fell inside (and, when records carry a payload line, the newline
// after that NUL ends that record), while a stream framing no paths at all ends a
// pathless line at its newline. Walking from the window's first byte instead
// reads a pathname's tail as a pathless line — dropping every tail record in
// files_with_matches mode, whose records have no newline at all — and reads a
// newline inside a pathname as a record boundary, presenting a pathname fragment
// as a complete record.
func resync(b []byte, filesOnly bool) int {
	if nul := bytes.IndexByte(b, 0); nul >= 0 {
		if filesOnly {
			return nul + 1
		}
		if nl := bytes.IndexByte(b[nul+1:], '\n'); nl >= 0 {
			return nul + nl + 2
		}
		return len(b) // no record ends inside the window
	}
	if nl := bytes.IndexByte(b, '\n'); nl >= 0 {
		return nl + 1
	}
	return len(b)
}

// capTailSlot bounds one tail record: past the slot cap it keeps the record's
// first rgTailSlotBytes and states the size of the rest, so the slot is bounded
// and says so.
func capTailSlot(rec []byte) string {
	if len(rec) <= rgTailSlotBytes {
		return string(rec)
	}
	return string(rec[:rgTailSlotBytes]) + fmt.Sprintf(outputOmissionMarker, len(rec)-rgTailSlotBytes)
}

// rgTailNote is the disclosure block's tail: the established omission marker with
// the byte count the capture left out, then the last records the stream held, so
// a truncated search shows both ends of the match stream. It rides in the note,
// never among the result rows, so the tool's "at most max_results rows" promise
// holds whatever the cap dropped.
func rgTailNote(view rgCaptureView) string {
	if len(view.tail) == 0 {
		return ""
	}
	marker := strings.Trim(fmt.Sprintf(outputOmissionMarker, view.omitted), "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nthe stream's last %d records:", marker, len(view.tail))
	for _, slot := range view.tail {
		b.WriteString("\n")
		b.WriteString(strings.TrimSuffix(slot, "\n"))
	}
	return b.String()
}
