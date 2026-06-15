package agent

import (
	"bytes"
	"fmt"
	"strings"
)

// assembleOutputDigest renders a head+tail line digest: the head slice, an
// elision marker describing what sits between, then the tail slice. headLines /
// tailLines are the line counts already sliced into head / tail; total is the
// lifetime output byte count and dropped is the bytes permanently evicted past
// the retention cap. The marker estimates the elided line count from the shown
// lines' average length (exact line totals are not tracked) and always states
// exact elided bytes plus the recovery call.
func assembleOutputDigest(head []byte, headLines int, tail []byte, tailLines int, total, dropped int64) string {
	elidedBytes := total - int64(len(head)) - int64(len(tail))
	if elidedBytes < 0 {
		elidedBytes = 0
	}
	var b strings.Builder
	b.Write(head)
	if len(head) > 0 && head[len(head)-1] != '\n' {
		b.WriteByte('\n')
	}
	estLines := estimateElidedLines(elidedBytes, len(head)+len(tail), headLines+tailLines)
	if dropped > 0 {
		fmt.Fprintf(&b, "…[~%d lines / %s elided, %s of them permanently dropped past the retention cap — recover the retained middle with job_read_output(head_lines=… / tail_lines=… / grep=…)]…\n",
			estLines, humanBytes(elidedBytes), humanBytes(dropped))
	} else {
		fmt.Fprintf(&b, "…[~%d lines / %s elided — read more with job_read_output(head_lines=… / tail_lines=… / grep=…)]…\n",
			estLines, humanBytes(elidedBytes))
	}
	b.Write(tail)
	return b.String()
}

// estimateElidedLines approximates how many lines `elidedBytes` covers from the
// average line length of the shown content. Returns 0 when nothing is elided.
func estimateElidedLines(elidedBytes int64, shownBytes, shownLines int) int64 {
	if elidedBytes <= 0 {
		return 0
	}
	if shownLines <= 0 || shownBytes <= 0 {
		return 0
	}
	avg := int64(shownBytes) / int64(shownLines)
	if avg < 1 {
		avg = 1
	}
	return elidedBytes / avg
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
// OutputStore, reachable via job_read_output.
func shellInlineDigest(full string, total, dropped int64) string {
	b := []byte(full)
	headRaw := b
	if len(headRaw) > shellDigestHalfBytes {
		headRaw = headRaw[:shellDigestHalfBytes]
	}
	tailRaw := b
	if len(tailRaw) > shellDigestHalfBytes {
		tailRaw = tailRaw[len(tailRaw)-shellDigestHalfBytes:]
	}
	head, hN, _ := firstLineBytes(headRaw, shellDigestLines)
	tail, tN, _ := lastLineBytes(tailRaw, shellDigestLines)
	return assembleOutputDigest(head, hN, tail, tN, total, dropped)
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
		head, hN, more := firstLineBytes(content, headLines)
		if !more {
			return headSnap, nil
		}
		tail, tN, _ := lastLineBytes(content, tailLines)
		if len(head)+len(tail) >= len(content) {
			return headSnap, nil
		}
		headSnap.Content = assembleOutputDigest(head, hN, tail, tN, headSnap.TotalBytes, headSnap.DroppedBytes)
		headSnap.Truncated = true
		return headSnap, nil
	}
	tailSnap, err := readWindow(jobLineReadBudget, false)
	if err != nil {
		return jobReadOutputSnapshot{}, err
	}
	head, hN, _ := firstLineBytes([]byte(headSnap.Content), headLines)
	tail, tN, _ := lastLineBytes([]byte(tailSnap.Content), tailLines)
	tailSnap.Content = assembleOutputDigest(head, hN, tail, tN, tailSnap.TotalBytes, tailSnap.DroppedBytes)
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
	return b[end-end:], lines, true
}
