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
// rgFraming is where the rolling window's first byte sits in ripgrep's record
// stream. Content alone cannot tell a payload tail from a pathname tail — a
// pathname may hold a newline, and what follows that newline can look exactly
// like the next record's start — so the capture, which saw every byte, tracks
// this as the window advances and the view resumes from it.
type rgFraming int

const (
	// frameRecordStart: the window's first byte begins a record.
	frameRecordStart rgFraming = iota
	// framePath: inside a record's pathname, before the NUL that ends it.
	framePath
	// framePayload: past the path's NUL, before the line that ends the record.
	framePayload
	// framePathless: inside a line that frames no path.
	framePathless
)

type rgCapture struct {
	capBytes  int
	filesOnly bool   // a files_with_matches record ends at its NUL, with no line
	prefix    []byte // the searched directory's path-separator-terminated prefix

	head   []byte // the stream's first capBytes, byte-exact
	window []byte // rolling: the last rgTailWindowBytes bytes written past the cap
	total  int
	over   bool

	// frame is the framing at c.window[0]: the capture tracks it over every byte
	// the window's start has passed, so the view knows where it opens instead of
	// guessing from the window's content.
	frame rgFraming
	// unitHead is the current unit's opening bytes, enough to tell a record whose
	// pathname it is from a line that frames no path; no longer than the prefix.
	unitHead []byte
}

// newRgCapture returns a capture bounded at capBytes. A non-positive cap falls
// back to the production default rather than silently removing the bound.
func newRgCapture(dir string, filesOnly bool, capBytes int) *rgCapture {
	if capBytes <= 0 {
		capBytes = maxCapturedOutputBytes
	}
	return &rgCapture{
		capBytes:  capBytes,
		filesOnly: filesOnly,
		prefix:    []byte(strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)),
	}
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
			// The tracker follows the head to the cap, so when the window opens
			// it already describes that first byte.
			c.trackFraming(p[:n])
			p = p[n:]
		}
		c.over = len(c.head) >= c.capBytes
	}
	if len(p) > 0 && c.over {
		c.window = append(c.window, p...)
		if extra := len(c.window) - rgTailWindowBytes; extra > 0 {
			// The bytes the trim discards are exactly what the new window start
			// has passed, so the state stays in sync with one bounded scan.
			c.trackFraming(c.window[:extra])
			c.window = append(c.window[:0], c.window[extra:]...)
		}
	}
	return written, nil
}

// trackFraming advances the framing state over bytes the window's start has
// passed. A record is its pathname up to a NUL and then its line; a pathname may
// hold newlines, which is why a newline inside one does not end it, and a line
// that opens without the directory prefix frames no path at all.
func (c *rgCapture) trackFraming(p []byte) {
	for _, b := range p {
		switch c.frame {
		case frameRecordStart:
			c.unitHead = append(c.unitHead[:0], b)
			switch {
			case b == 0:
				c.frame = framePayload // no path at all before this NUL
			case !c.unitHeadIsPath():
				c.frame = framePathless
			default:
				c.frame = framePath
			}
		case framePath:
			if b == 0 {
				// A files-only record is its path and NUL: the NUL ends it, so the
				// state moves straight to the next record's start.
				if c.filesOnly {
					c.frame = frameRecordStart
				} else {
					c.frame = framePayload
				}
				continue
			}
			if len(c.unitHead) < len(c.prefix) {
				c.unitHead = append(c.unitHead, b)
				if !c.unitHeadIsPath() {
					c.frame = framePathless
				}
			}
		case framePayload, framePathless:
			if b == '\n' {
				c.frame = frameRecordStart
			}
		}
	}
}

// unitHeadIsPath reports whether the current unit still opens like the searched
// directory, so it can be a pathname rather than a pathless line.
func (c *rgCapture) unitHeadIsPath() bool {
	return bytes.HasPrefix(c.prefix, c.unitHead)
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
	// cut is how many tail records are longer than one slot and are presented cut
	// to it. It is not the same as an omission: a cut records the record's own
	// tail, while omitted counts stream bytes neither the head nor the records
	// hold.
	cut int
}

// wholeStream is the head and the tail as one stream when the capture holds every
// byte of it and no slot was cut, else "". Nothing was left out, so a caller
// parses it as a complete result: the tail's records are the stream's own final
// ones, and there is no omission to disclose.
func (v rgCaptureView) wholeStream() string {
	if v.omitted != 0 || v.cut > 0 {
		return ""
	}
	return v.head + strings.Join(v.tail, "")
}

// view cuts the capture into the bytes a search can present, using the record
// framing ripgrepOutputLines reads. The head ends where the stream was cut, which
// is mid-record, so it is cut back to its last complete record rather than
// presenting a fragment as a result.
func (c *rgCapture) view(filesOnly bool) rgCaptureView {
	head := c.head
	if c.over {
		head = head[:lastRecordEnd(head, c.prefix, filesOnly)]
	}
	// Where the window opens is the tracker's to say (see rgFraming): the state
	// it carries is exactly that position's, however the window has moved.
	tail, tailBytes, cut := tailRecords(c.window, c.prefix, filesOnly, c.frame)
	// A capped slot carries synthetic omission text, which is not stream bytes:
	// counting the slots instead of their records understates the omission, and
	// once the slots' text outweighs what was really dropped the count goes
	// NEGATIVE — which the caller reads as "nothing was left out" and suppresses
	// the disclosure entirely.
	retained := len(head) + tailBytes
	return rgCaptureView{
		head:     string(head),
		tail:     tail,
		retained: retained,
		omitted:  c.total - retained,
		cut:      cut,
	}
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
// each bounded by capTailSlot, with the SOURCE bytes those records occupy and
// whether any record was cut to its slot. b opens wherever the cap tripped, at
// the framing state frame describes, so it is resynchronized to the first
// complete record there: the bytes before that boundary are a fragment of a
// record that started earlier and are left out.
func tailRecords(b, prefix []byte, filesOnly bool, frame rgFraming) (slots []string, source, cut int) {
	b = b[resync(b, filesOnly, frame):]
	ends := rgRecordEnds(b, prefix, filesOnly)
	if len(ends) == 0 {
		return nil, 0, 0
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
	slots = make([]string, 0, len(recs))
	for _, rec := range recs {
		source += len(rec)
		// The record's own size decides: comparing the slot's length against the
		// record's misses a record whose dropped part is exactly as wide as the
		// marker that replaces it (rgTailSlotBytes+28 gets a 28-byte marker).
		if len(rec) > rgTailSlotBytes {
			cut++
		}
		slots = append(slots, capTailSlot(rec))
	}
	return slots, source, cut
}

// resync drops the bytes of a window that opens inside a record, returning the
// offset of the first complete record. The framing state says where the window
// opens — the one fact its content cannot supply, since a pathname may hold a
// newline and what follows it can look exactly like the next record's start.
// A window opening at a record start keeps everything; a pathname tail is
// dropped through the NUL that ends it and the line after that NUL; a payload
// tail ends at its own newline, which comes before the window's first NUL (that
// NUL belongs to the next record); a pathless line ends at its newline.
func resync(b []byte, filesOnly bool, frame rgFraming) int {
	switch frame {
	case frameRecordStart:
		return 0
	case framePayload, framePathless:
		if nl := bytes.IndexByte(b, '\n'); nl >= 0 {
			return nl + 1
		}
		return len(b)
	default: // framePath
		if nul := bytes.IndexByte(b, 0); nul >= 0 {
			if filesOnly {
				return nul + 1
			}
			if nl := bytes.IndexByte(b[nul+1:], '\n'); nl >= 0 {
				return nul + nl + 2
			}
		}
		return len(b) // no record ends inside the window
	}
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

// rgTailNote is the disclosure block's tail: what the cap did to the stream, then
// the last records it held, so a truncated search shows both ends of the match
// stream. What it states depends on what happened — an omission marker claims
// bytes were left out, so a capture that omitted none states its cut slots
// instead. It rides in the note, never among the result rows, so the tool's "at
// most max_results rows" promise holds whatever the cap dropped.
func rgTailNote(view rgCaptureView) string {
	if len(view.tail) == 0 {
		return ""
	}
	var b strings.Builder
	if view.omitted > 0 {
		// The established omission marker, with the bytes the cap left out.
		fmt.Fprintf(&b, "%s\n", strings.Trim(fmt.Sprintf(outputOmissionMarker, view.omitted), "\n"))
	} else {
		fmt.Fprintf(&b,
			"the byte cap tripped and left nothing out; %d tail record(s) longer than the %d-byte slot are cut at the slot:\n",
			view.cut, rgTailSlotBytes)
	}
	fmt.Fprintf(&b, "the stream's last %d records:", len(view.tail))
	for _, slot := range view.tail {
		b.WriteString("\n")
		b.WriteString(strings.TrimSuffix(slot, "\n"))
	}
	return b.String()
}
