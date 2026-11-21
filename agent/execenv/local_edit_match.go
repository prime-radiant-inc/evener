package execenv

import (
	"fmt"
	"strings"
)

// byteSpan is an exact [start,end) byte range within a file's content.
type byteSpan struct {
	start, end int
}

// maxAmbiguousLines caps the candidate line numbers findFuzzyMatchSpans
// reports for an ambiguous single match.
const maxAmbiguousLines = 10

// findFuzzyMatch returns the file's first whitespace-normalized match for
// oldString, or "" if no match. It delegates to findFuzzyMatchSpans so
// existing direct test callers (line_window_test.go,
// local_edge_program_fuzz_test.go) continue to exercise findFuzzyMatchSpans.
func findFuzzyMatch(content, oldString string) string {
	// Every accepted span is non-empty and non-overlapping, so content cannot
	// hold more than len(content) of them.
	spans, _, _ := findFuzzyMatchSpans(content, oldString, true, len(content)+1)
	if len(spans) == 0 {
		return ""
	}
	return content[spans[0].start:spans[0].end]
}

// findFuzzyMatchSpans scans the file content for exact byte ranges that match
// oldString when whitespace is normalized. replaceAll collects every
// non-overlapping match; otherwise the single match is returned, or its
// candidate 1-based lines in ambigLines when the needle is ambiguous. More
// than maxSpans accepted matches reports limitExceeded and discards the
// spans. The content is scanned segment by segment: a line-heavy file at the
// whole-file read cap would otherwise allocate ~16 bytes of slice header per
// line for every line in it (issue #4556).
func findFuzzyMatchSpans(content, oldString string, replaceAll bool, maxSpans int) (spans []byteSpan, ambigLines []int, limitExceeded bool) {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return nil, nil, false
	}
	wSize := len(splitEditLines(oldString, "\n"))
	var (
		windowSpans []byteSpan
		windowLines []string
		scanOffset  int
		lineNo      int
		firstLine   int
		lastEnd     int
		overflow    bool
	)
	accept := func(span byteSpan, atLine int) (stop bool) {
		if span.end <= span.start {
			return false
		}
		// A candidate that covers bytes a match already claimed is not a
		// distinct occurrence: a matched line or window must not be re-reported
		// by a window sliding over it.
		if len(spans) > 0 && span.start < lastEnd {
			return false
		}
		if len(spans) >= maxSpans {
			overflow = true
			return true
		}
		if replaceAll || len(spans) == 0 {
			if len(spans) == 0 {
				firstLine = atLine
			}
			spans = append(spans, span)
			lastEnd = span.end
			return false
		}
		// A distinct second match in single-match mode: report the candidate
		// lines, the first match included, and keep scanning up to the cap.
		if len(ambigLines) == 0 {
			ambigLines = append(ambigLines, firstLine)
		}
		if len(ambigLines) < maxAmbiguousLines {
			ambigLines = append(ambigLines, atLine)
		}
		return len(ambigLines) >= maxAmbiguousLines
	}

	eachLine(content, func(line string) bool {
		lineStart := scanOffset
		lineEnd := lineStart + len(line)
		scanOffset = lineEnd + 1 // the '\n' eachLine consumed before the next segment
		thisLine := lineNo + 1
		lineNo++

		// A multi-line needle can still match within one line once whitespace
		// is normalized (old "a\nb" against a line "a b"), so the single-line
		// check runs for every needle size.
		if normalizeWS(line) == normOld {
			if accept(byteSpan{start: lineStart, end: lineEnd}, thisLine) {
				return true
			}
		}
		if wSize < 2 {
			return false
		}
		if len(windowSpans) < wSize {
			windowSpans = append(windowSpans, byteSpan{start: lineStart, end: lineEnd})
			windowLines = append(windowLines, line)
		} else {
			copy(windowSpans, windowSpans[1:])
			windowSpans[wSize-1] = byteSpan{start: lineStart, end: lineEnd}
			copy(windowLines, windowLines[1:])
			windowLines[wSize-1] = line
		}
		if len(windowSpans) < wSize || normalizeWS(strings.Join(windowLines, "\n")) != normOld {
			return false
		}
		stop := accept(byteSpan{start: windowSpans[0].start, end: windowSpans[wSize-1].end}, thisLine-wSize+1)
		if replaceAll {
			// Consume the matched window so the next sliding windows cannot
			// re-report bytes this match already covers.
			windowSpans = windowSpans[:0]
			windowLines = windowLines[:0]
		}
		return stop
	})

	if overflow {
		return nil, nil, true
	}
	if len(ambigLines) > 0 {
		return nil, ambigLines, false
	}
	return spans, nil, false
}

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nearestFileRegion returns the file region most similar to oldString's first
// line, verbatim and spanning oldString's line count, so a failed edit_file
// match can show the model the actual bytes at the intended site — surfacing a
// dropped word or an omitted line, the usual causes of a near miss the fuzzy
// matcher cannot rescue. The region is prefixed with the 1-based number of its
// first line (`L<n>:`) so the model can locate it in the file. Returns "" when
// nothing is similar enough to help.
func nearestFileRegion(content, oldString string) string {
	oldLines := splitEditLines(strings.TrimRight(oldString, "\n"), "\n")
	if len(oldLines) == 0 {
		return ""
	}
	target := normalizeWS(oldLines[0])
	if target == "" {
		return ""
	}
	// Score every segment, then re-walk to collect the best window: two
	// streamed passes instead of a whole-file split, which would hold ~16
	// bytes of slice header per line at once (issue #4556).
	best, bestScore := -1, 0.0
	lineNo := 0
	eachLine(content, func(line string) bool {
		if score := lineSimilarity(normalizeWS(line), target); score > bestScore {
			best, bestScore = lineNo, score
		}
		lineNo++
		return false
	})
	if best < 0 || bestScore < 0.5 {
		return ""
	}
	window := make([]string, 0, len(oldLines))
	lineNo = 0
	eachLine(content, func(line string) bool {
		if lineNo >= best && lineNo < best+len(oldLines) {
			window = append(window, line)
			if len(window) == len(oldLines) {
				return true
			}
		}
		lineNo++
		return false
	})
	return fmt.Sprintf("L%d:\n%s", best+1, strings.Join(window, "\n"))
}

// lineSimilarity scores two whitespace-normalized lines in [0,1]: 1.0 when one
// contains the other (the partial-line case), otherwise the Jaccard overlap of
// their space-separated tokens.
func lineSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		return 1
	}
	sa, sb := wordSet(a), wordSet(b)
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// wordSet returns the set of space-separated tokens in s.
func wordSet(s string) map[string]bool {
	m := make(map[string]bool)
	for w := range strings.FieldsSeq(s) {
		m[w] = true
	}
	return m
}
