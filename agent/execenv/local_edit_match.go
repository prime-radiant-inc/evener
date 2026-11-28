package execenv

import (
	"fmt"
	"slices"
	"strings"
)

// byteSpan is an exact [start,end) byte range within a file's content.
type byteSpan struct {
	start, end int
}

// maxAmbiguousLines caps the candidate line numbers findFuzzyMatchSpans
// reports for an ambiguous single match.
const maxAmbiguousLines = 10

// findFuzzyMatchSpans scans the file content for exact byte ranges that match
// oldString when whitespace is normalized. replaceAll collects every
// non-overlapping match; otherwise the single match is returned, or its
// candidate 1-based lines in ambigLines when the needle is ambiguous. An
// accepted match past the maxSpans budget reports limitExceeded and discards
// the spans; ambiguity does not consume the budget, so a distinct second match
// reports ambigLines even at maxSpans == 1. The content is scanned segment by
// segment: a line-heavy file at the whole-file read cap would otherwise
// allocate ~16 bytes of slice header per line for every line in it
// (issue #4556).
func findFuzzyMatchSpans(content, oldString string, replaceAll bool, maxSpans int) (spans []byteSpan, ambigLines []int, limitExceeded, blankTolerant bool) {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return nil, nil, false, false
	}
	oldLines := splitEditLines(oldString, "\n")
	wSize := len(oldLines)
	var (
		windowSpans []byteSpan
		windowLines []string
		nonEmptyWin int
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
		if replaceAll || len(spans) == 0 {
			if len(spans) >= maxSpans {
				overflow = true
				return true
			}
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
		if line != "" {
			nonEmptyWin++
		}
		if len(windowSpans) < wSize {
			windowSpans = append(windowSpans, byteSpan{start: lineStart, end: lineEnd})
			windowLines = append(windowLines, line)
		} else {
			if windowLines[0] != "" {
				nonEmptyWin--
			}
			copy(windowSpans, windowSpans[1:])
			windowSpans[wSize-1] = byteSpan{start: lineStart, end: lineEnd}
			copy(windowLines, windowLines[1:])
			windowLines[wSize-1] = line
		}
		if len(windowSpans) < wSize || nonEmptyWin == 0 || normalizeWS(strings.Join(windowLines, "\n")) != normOld {
			return false
		}
		stop := accept(byteSpan{start: windowSpans[0].start, end: windowSpans[wSize-1].end}, thisLine-wSize+1)
		if replaceAll {
			// Consume the matched window so the next sliding windows cannot
			// re-report bytes this match already covers.
			windowSpans = windowSpans[:0]
			windowLines = windowLines[:0]
			nonEmptyWin = 0
		}
		return stop
	})

	if overflow {
		return nil, nil, true, false
	}
	if len(ambigLines) > 0 {
		return nil, ambigLines, false, false
	}
	if len(spans) == 0 && !replaceAll {
		spans, ambigLines, limitExceeded = findBlankTolerantMatchSpan(content, oldLines, maxSpans)
		return spans, ambigLines, limitExceeded, len(spans) > 0
	}
	return spans, nil, false, false
}

// findBlankTolerantMatchSpan is the last-resort unique-match fallback when
// exact and fixed-window fuzzy matching find nothing. It streams content in
// O(len(patNonBlank)) memory, skipping blank lines between non-blank lines;
// boundary blank lines in oldLines disable it.
func findBlankTolerantMatchSpan(content string, oldLines []string, maxSpans int) (spans []byteSpan, ambigLines []int, limitExceeded bool) {
	if len(oldLines) < 2 || normalizeWS(oldLines[0]) == "" || normalizeWS(oldLines[len(oldLines)-1]) == "" {
		return nil, nil, false
	}
	patNonBlank := make([]string, 0, len(oldLines))
	for _, l := range oldLines {
		if norm := normalizeWS(l); norm != "" {
			patNonBlank = append(patNonBlank, norm)
		}
	}
	type nonBlankLine struct {
		norm               string
		start, end, lineNo int
	}
	m := len(patNonBlank)
	ring := make([]nonBlankLine, m)
	var head, count, scanOffset, lineNo, firstLine int
	eachLine(content, func(line string) bool {
		lineStart, lineEnd := scanOffset, scanOffset+len(line)
		scanOffset = lineEnd + 1
		lineNo++
		norm := normalizeWS(line)
		if norm == "" {
			return false
		}
		ring[(head+count)%m] = nonBlankLine{norm: norm, start: lineStart, end: lineEnd, lineNo: lineNo}
		if count < m {
			if count++; count < m {
				return false
			}
		} else {
			head = (head + 1) % m
		}
		for i, want := range patNonBlank {
			if ring[(head+i)%m].norm != want {
				return false
			}
		}
		first, last := ring[head], ring[(head+m-1)%m]
		if len(spans) == 0 {
			if maxSpans < 1 {
				limitExceeded = true
				return true
			}
			firstLine = first.lineNo
			spans = []byteSpan{{start: first.start, end: last.end}}
			return false
		}
		if len(ambigLines) == 0 {
			ambigLines = append(ambigLines, firstLine)
		}
		ambigLines = append(ambigLines, first.lineNo)
		return len(ambigLines) >= maxAmbiguousLines
	})
	if limitExceeded || len(ambigLines) > 0 {
		return nil, ambigLines, limitExceeded
	}
	return spans, nil, false
}

// reconcileBlankTolerantReplacement preserves unchanged leading/trailing whole
// context lines (and their blank lines) from matched on blank-tolerant hits.
func reconcileBlankTolerantReplacement(matched, oldString, newString string) string {
	matchedLines := splitEditLines(matched, "\n")
	oldLines, newLines := splitEditLines(oldString, "\n"), splitEditLines(newString, "\n")
	matchedIdx := make([]int, 0, len(matchedLines))
	for i, l := range matchedLines {
		if normalizeWS(l) != "" {
			matchedIdx = append(matchedIdx, i)
		}
	}
	p, pNB, s, sNB := 0, 0, 0, 0
	for p < len(oldLines) && p < len(newLines) && oldLines[p] == newLines[p] {
		if normalizeWS(oldLines[p]) != "" {
			pNB++
		}
		p++
	}
	if p == len(oldLines) && p == len(newLines) {
		return matched
	}
	for p+s < len(oldLines) && p+s < len(newLines) && oldLines[len(oldLines)-1-s] == newLines[len(newLines)-1-s] {
		if normalizeWS(oldLines[len(oldLines)-1-s]) != "" {
			sNB++
		}
		s++
	}
	totalNB := len(matchedIdx)
	if (pNB == 0 && sNB == 0) || pNB+sNB > totalNB {
		return newString
	}
	for pNB > 0 && p < len(newLines)-s && normalizeWS(newLines[p]) == "" && p > 0 && normalizeWS(oldLines[p-1]) == "" {
		p--
	}
	for sNB > 0 && p < len(newLines)-s && normalizeWS(newLines[len(newLines)-s-1]) == "" && s > 0 && normalizeWS(oldLines[len(oldLines)-s]) == "" {
		s--
	}
	newMid := newLines[p : len(newLines)-s]
	gap := func(k int) []string { return matchedLines[matchedIdx[k]+1 : matchedIdx[k+1]] }
	var prefix, preGap, postGap, suffix []string
	if pNB > 0 {
		prefix = matchedLines[:matchedIdx[pNB-1]+1]
	}
	if sNB > 0 {
		suffix = matchedLines[matchedIdx[totalNB-sNB]:]
	}
	startNB := len(newMid) > 0 && normalizeWS(newMid[0]) != ""
	endNB := len(newMid) > 0 && normalizeWS(newMid[len(newMid)-1]) != ""
	if pNB+sNB == totalNB {
		if pNB > 0 && sNB > 0 && startNB && endNB {
			if p > 0 && normalizeWS(oldLines[p-1]) == "" {
				preGap = gap(pNB - 1)
			} else {
				postGap = gap(pNB - 1)
			}
		}
	} else {
		if pNB > 0 && (startNB || (len(newMid) == 0 && sNB > 0)) {
			preGap = gap(pNB - 1)
		}
		if sNB > 0 && (endNB || (len(newMid) == 0 && pNB > 0 && len(preGap) == 0)) {
			postGap = gap(totalNB - sNB - 1)
		}
	}
	return strings.Join(slices.Concat(prefix, preGap, newMid, postGap, suffix), "\n")
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
