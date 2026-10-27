package execenv

import "strings"

// findFuzzyMatch scans the file content for a substring that matches
// oldString when whitespace is normalized. Returns the actual substring from
// the file, or "" if no match. The content is scanned segment by segment: a
// line-heavy file at the whole-file read cap would otherwise allocate ~16
// bytes of slice header per line for every line in it (issue #4556).
func findFuzzyMatch(content, oldString string) string {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return ""
	}
	// Scan lines for single-line matches.
	var match string
	eachLine(content, func(line string) bool {
		if normalizeWS(line) == normOld {
			match = line
			return true
		}
		return false
	})
	if match != "" {
		return match
	}
	// Multi-line: try sliding window of the same line count.
	oldLines := strings.Split(oldString, "\n")
	wSize := len(oldLines)
	if wSize < 2 {
		return ""
	}
	window := make([]string, 0, wSize)
	found := false
	eachLine(content, func(line string) bool {
		if len(window) == wSize {
			copy(window, window[1:])
			window[wSize-1] = line
		} else {
			window = append(window, line)
		}
		if len(window) == wSize && normalizeWS(strings.Join(window, "\n")) == normOld {
			found = true
			return true
		}
		return false
	})
	if found {
		return strings.Join(window, "\n")
	}
	return ""
}

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// nearestFileRegion returns the file region most similar to oldString's first
// line, verbatim and spanning oldString's line count, so a failed edit_file
// match can show the model the actual bytes at the intended site — surfacing a
// dropped word or an omitted line, the usual causes of a near miss the fuzzy
// matcher cannot rescue. Returns "" when nothing is similar enough to help.
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
	return strings.Join(window, "\n")
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
