package agent

import (
	"bytes"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// assembleOutputDigest renders a head+tail line digest: the head slice, an
// elision marker describing what sits between, then the tail slice. total is the
// lifetime output byte count and dropped is the bytes permanently evicted past
// the retention cap, which is also the lifetime offset where the retained output
// starts. The marker states the EXACT elided byte count, the omitted lifetime
// byte range, and the offset_bytes continuation — never a line estimate: the
// store is byte-oriented and does not track total lines, and estimating from the
// shown lines' average length is unreliable (the head/tail sample is biased
// toward short lines, so a guess can exceed the true total).
func assembleOutputDigest(head, tail []byte, total, dropped int64) string {
	total = max(total, 0)
	retainedStart := min(max(dropped, 0), total)
	// The head slice is a prefix of the retained output and the tail slice is a
	// suffix, so these are the lifetime offsets of the first and last bytes the
	// digest omits.
	omitStart := total
	if total-retainedStart >= int64(len(head)) {
		omitStart = retainedStart + int64(len(head))
	}
	omitEnd := max(total-int64(len(tail)), omitStart)
	elidedBytes := omitEnd - omitStart
	var b strings.Builder
	b.Write(head)
	if len(head) > 0 && head[len(head)-1] != '\n' {
		b.WriteByte('\n')
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "…[%s elided; %s permanently dropped past the retention cap; omitted [%d,%d); read more with read_transcript(offset_bytes=%d) via the returned job transcript_ref]…\n",
			humanBytes(elidedBytes), humanBytes(dropped), omitStart, omitEnd, omitStart)
	} else {
		fmt.Fprintf(&b, "…[%s elided; omitted [%d,%d); read more with read_transcript(offset_bytes=%d) via the returned job transcript_ref]…\n",
			humanBytes(elidedBytes), omitStart, omitEnd, omitStart)
	}
	b.Write(tail)
	return b.String()
}

// humanBytes formats a byte count as a short human-readable string. Counts past
// the largest named unit (TB) stay in TB rather than overflowing the unit table,
// so an extreme int64 renders honestly instead of panicking.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	const units = "KMGT"
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < len(units)-1; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), units[exp])
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
	return shellInlineDigestWithin(full, total, dropped, 0, 0)
}

// shellInlineDigestWithin is shellInlineDigest bounded by the registry's
// rendered limits: lineBudget counts the digest plus its one-line footer
// (0 = unbounded), and charBudget bounds the digest's runes (0 = unbounded).
// It first bounds the head and tail previews to lineBudget (reserving one line
// each for the omission marker and job footer), then trims whole lines from the
// inner edges of whichever preview is longer so both the first line and final
// diagnostic survive tight character budgets. Only if each end is down to a
// single oversized line does it fall back to shrinking the longer slice by
// bytes, preserving valid UTF-8. Each candidate computes the omitted lifetime
// range from the slices actually shown.
func shellInlineDigestWithin(full string, total, dropped int64, lineBudget, charBudget int) string {
	headLines, tailLines := shellDigestLines, shellDigestLines
	if lineBudget > 0 {
		// The digest path always renders a job footer line. Keep at least one
		// line at each end when the allowance holds two; otherwise give the one
		// remaining line to the head.
		switch avail := lineBudget - 2; {
		case avail <= 0:
			headLines, tailLines = 0, 0
		case avail == 1:
			headLines, tailLines = 1, 0
		default:
			headLines, tailLines = min(avail/2, shellDigestLines), min(avail-avail/2, shellDigestLines)
		}
	}
	head, headCount, tail, tailCount := shellInlineDigestSlices(full, shellDigestHalfBytes, shellDigestHalfBytes, headLines, tailLines)
	for {
		digest := assembleOutputDigest(head, tail, total, dropped)
		if digestFits(digest, lineBudget, charBudget) {
			return digest
		}
		switch {
		case headCount > 1 && (tailCount <= 1 || len(head) >= len(tail)):
			head, headCount, _ = firstLineBytes(head, headCount-1)
		case tailCount > 1:
			tail, tailCount, _ = lastLineBytes(tail, tailCount-1)
		case len(head) == 0 && len(tail) == 0:
			return digest
		case len(head) >= len(tail):
			head = runetrim.TrimTrailingPartial(head[:len(head)/2])
			if len(head) == 0 {
				headCount = 0
			}
		default:
			tail = runetrim.TrimLeadingPartial(tail[len(tail)-len(tail)/2:])
			if len(tail) == 0 {
				tailCount = 0
			}
		}
	}
}

// digestFits reports whether a rendered digest survives the registry's limits.
// The shell footer is a single line (exit status, windowed-output note, and
// retention details join with " · "), hence the +1.
func digestFits(digest string, lineBudget, charBudget int) bool {
	if lineBudget > 0 && textLineCount(digest)+1 > lineBudget {
		return false
	}
	if charBudget > 0 && len([]rune(digest)) > charBudget {
		return false
	}
	return true
}

// shellInlineDigestSlices selects the initial head and tail byte slices and
// their line counts for one pair of preview byte and line budgets.
func shellInlineDigestSlices(full string, headBudget, tailBudget, headLines, tailLines int) (head []byte, headCount int, tail []byte, tailCount int) {
	b := []byte(full)
	var headRaw []byte
	if headBudget > 0 && headLines > 0 {
		headRaw = b
		if len(headRaw) > headBudget {
			headRaw = headRaw[:headBudget]
			// Drop a trailing partial line so the head ends on a line boundary and no
			// mid-line fragment is shown before the elision marker.
			if i := bytes.LastIndexByte(headRaw, '\n'); i >= 0 {
				headRaw = headRaw[:i+1]
			}
			// A single long line with no newline before the cut leaves the byte slice
			// ending mid-rune; drop the dangling partial rune so the head is valid UTF-8.
			headRaw = runetrim.TrimTrailingPartial(headRaw)
		}
	}
	var tailRaw []byte
	if tailBudget > 0 && tailLines > 0 {
		tailRaw = b
		if len(tailRaw) > tailBudget {
			tailRaw = tailRaw[len(tailRaw)-tailBudget:]
			// Drop a leading partial line so the tail starts on a line boundary.
			if i := bytes.IndexByte(tailRaw, '\n'); i >= 0 {
				tailRaw = tailRaw[i+1:]
			}
			// Likewise drop a dangling partial rune at the cut so the tail is valid UTF-8.
			tailRaw = runetrim.TrimLeadingPartial(tailRaw)
		}
	}
	head, headCount, _ = firstLineBytes(headRaw, headLines)
	tail, tailCount, _ = lastLineBytes(tailRaw, tailLines)
	return head, headCount, tail, tailCount
}

// textLineCount counts rendered lines: newline separators plus a non-terminated
// final line.
func textLineCount(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
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
	for {
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
}
