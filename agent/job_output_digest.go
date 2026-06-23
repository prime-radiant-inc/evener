package agent

import (
	"bytes"
	"fmt"
	"strings"
)

// assembleOutputDigest renders a head+tail line digest: the head slice, an
// elision marker describing what sits between, then the tail slice. total is the
// lifetime output byte count and dropped is the bytes permanently evicted past
// the retention cap. The marker states the EXACT elided byte count plus the
// transcript-read hint — never a line estimate: the store is byte-oriented and does not
// track total lines, and estimating from the shown lines' average length is
// unreliable (the head/tail sample is biased toward short lines, so a guess can
// exceed the true total).
func assembleOutputDigest(head, tail []byte, total, dropped int64) string {
	elidedBytes := total - int64(len(head)) - int64(len(tail))
	if elidedBytes < 0 {
		elidedBytes = 0
	}
	var b strings.Builder
	b.Write(head)
	if len(head) > 0 && head[len(head)-1] != '\n' {
		b.WriteByte('\n')
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "…[%s elided, %s of them permanently dropped past the retention cap — inspect retained output with read_transcript using the returned job transcript_ref]…\n",
			humanBytes(elidedBytes), humanBytes(dropped))
	} else {
		fmt.Fprintf(&b, "…[%s elided — read more with read_transcript using the returned job transcript_ref]…\n",
			humanBytes(elidedBytes))
	}
	b.Write(tail)
	return b.String()
}

// humanBytes formats a byte count as a short human-readable string.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// shellDigestHalfBytes bounds the bytes taken from each end for the inline shell
// digest; shellDigestLines caps the lines per end (the byte budget usually binds
// first). Net inline footprint stays ~1 KiB, split head/tail.
const (
	shellDigestHalfBytes = 512
	shellDigestLines     = 200
)

// shellInlineDigest renders a compact head+tail digest of a completed command's
// full output for the inline shell result: whole lines from the first and last
// ~shellDigestHalfBytes with the middle elided. The full output stays in the
// OutputStore, reachable via the job transcript_ref.
func shellInlineDigest(full string, total, dropped int64) string {
	b := []byte(full)
	headRaw := b
	if len(headRaw) > shellDigestHalfBytes {
		headRaw = headRaw[:shellDigestHalfBytes]
		// Drop a trailing partial line so the head ends on a line boundary and no
		// mid-line fragment is shown before the elision marker.
		if i := bytes.LastIndexByte(headRaw, '\n'); i >= 0 {
			headRaw = headRaw[:i+1]
		}
	}
	tailRaw := b
	if len(tailRaw) > shellDigestHalfBytes {
		tailRaw = tailRaw[len(tailRaw)-shellDigestHalfBytes:]
		// Drop a leading partial line so the tail starts on a line boundary.
		if i := bytes.IndexByte(tailRaw, '\n'); i >= 0 {
			tailRaw = tailRaw[i+1:]
		}
	}
	head, _, _ := firstLineBytes(headRaw, shellDigestLines)
	tail, _, _ := lastLineBytes(tailRaw, shellDigestLines)
	return assembleOutputDigest(head, tail, total, dropped)
}

// readJobOutputDigest builds the default head+tail digest snapshot using the
// supplied window reader. It reads the head first; when the whole retained output
// fits in the read budget it returns it whole (≤ digestHeadLines lines or
// head+tail overlap) or a single-buffer digest; otherwise it reads the tail
// separately and stitches a head + elision-marker + tail digest.
func readJobOutputDigest(readWindow func(budget int, fromHead bool) (jobReadOutputSnapshot, error), headLines, tailLines int) (jobReadOutputSnapshot, error) {
	headSnap, err := readWindow(jobLineReadBudget, true)
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	if !headSnap.Truncated && headSnap.DroppedBytes == 0 {
		// The entire retained output fits in the head read.
		content := []byte(headSnap.Content)
		head, _, more := firstLineBytes(content, headLines)
		if !more {
			return headSnap, nil
		}
		tail, _, _ := lastLineBytes(content, tailLines)
		if len(head)+len(tail) >= len(content) {
			return headSnap, nil
		}
		headSnap.Content = assembleOutputDigest(head, tail, headSnap.TotalBytes, headSnap.DroppedBytes)
		headSnap.Truncated = true
		return headSnap, nil
	}
	tailSnap, err := readWindow(jobLineReadBudget, false)
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	head, _, _ := firstLineBytes([]byte(headSnap.Content), headLines)
	tail, _, _ := lastLineBytes([]byte(tailSnap.Content), tailLines)
	tailSnap.Content = assembleOutputDigest(head, tail, tailSnap.TotalBytes, tailSnap.DroppedBytes)
	tailSnap.Truncated = true
	return tailSnap, nil
}

// midLineBytes returns the lines [fromLine, fromLine+count) of b (1-based,
// inclusive of trailing newlines), the count returned, and whether lines exist
// before the window (before) or after it (after) within b.
func midLineBytes(b []byte, fromLine, count int) (slice []byte, lines int, before, after bool) {
	if fromLine < 1 {
		fromLine = 1
	}
	if count < 1 || len(b) == 0 {
		return nil, 0, fromLine > 1 && len(b) > 0, false
	}
	start, end, idx, pos := -1, -1, 0, 0
	for pos < len(b) {
		lineStart := pos
		nl := bytes.IndexByte(b[pos:], '\n')
		lineEnd := len(b)
		if nl >= 0 {
			lineEnd = pos + nl + 1
		}
		idx++ // 1-based line number
		switch {
		case idx < fromLine:
			before = true
		case idx < fromLine+count:
			if start < 0 {
				start = lineStart
			}
			end = lineEnd
			lines++
		default:
			after = true
		}
		if after {
			break
		}
		pos = lineEnd
	}
	if start < 0 {
		return nil, 0, before, after
	}
	return b[start:end], lines, before, after
}

// firstLineBytes returns the first n lines of b (each line including its trailing
// newline; a final line without a newline still counts), the number of lines
// returned, and whether b held more lines than were returned.
func firstLineBytes(b []byte, n int) (slice []byte, lines int, more bool) {
	if n <= 0 || len(b) == 0 {
		return nil, 0, len(b) > 0
	}
	end := 0
	for lines < n {
		nl := bytes.IndexByte(b[end:], '\n')
		if nl < 0 {
			// Trailing partial line (no newline) — the last line.
			return b, lines + 1, false
		}
		end += nl + 1
		lines++
		if end >= len(b) {
			return b[:end], lines, false
		}
	}
	return b[:end], lines, true
}

// lastLineBytes returns the last n lines of b, the number returned, and whether
// b held more lines before them.
func lastLineBytes(b []byte, n int) (slice []byte, lines int, more bool) {
	if n <= 0 || len(b) == 0 {
		return nil, 0, len(b) > 0
	}
	// Walk backward counting line starts. A trailing newline terminates the last
	// line; ignore it when locating boundaries.
	end := len(b)
	scan := end
	if b[scan-1] == '\n' {
		scan--
	}
	for lines < n {
		nl := bytes.LastIndexByte(b[:scan], '\n')
		lines++
		if nl < 0 {
			// Reached the start: b begins on a line boundary.
			return b, lines, false
		}
		scan = nl
		if lines == n {
			return b[nl+1:], lines, true
		}
	}
	return b, lines, true
}
