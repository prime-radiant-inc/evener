package agent

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// diffLineKind classifies one line of a git-style unified diff. Only file
// starts and hunk headers are boundaries a digest window may cut at; path
// lines are metadata and body lines are content.
type diffLineKind uint8

const (
	diffLineBody diffLineKind = iota
	diffLineFile
	diffLineHunk
	diffLineOldPath
	diffLineNewPath
)

type diffLine struct {
	start, end int
	kind       diffLineKind
}

// diffWindow is a boundary-aligned head and, when one fits, tail of a unified
// diff, plus the file and hunk that begin the omitted middle. When no diff
// boundary fits in the tail budget, TailStart is a line start (the existing
// trailing peek) or len(full) when that would overlap the head.
type diffWindow struct {
	HeadStart int
	HeadEnd   int
	TailStart int
	File      string
	Hunk      string
}

const (
	// diffMarkerTextBytes bounds each source-derived value (file path, hunk
	// header) quoted in a diff digest marker. With the marker's fixed prose,
	// the largest marker plus both 512-byte windows stays under
	// FuzzArShellInlineDigest's 2*shellDigestHalfBytes + digestMarkerBudget
	// bound.
	diffMarkerTextBytes = 96
)

func scanDiffLines(full string) []diffLine {
	lines := make([]diffLine, 0, strings.Count(full, "\n")+1)
	for start := 0; start < len(full); {
		end := len(full)
		if i := strings.IndexByte(full[start:], '\n'); i >= 0 {
			end = start + i + 1
		}
		lines = append(lines, diffLine{start: start, end: end})
		start = end
	}
	return lines
}

func diffLineText(full string, ln diffLine) string {
	text := strings.TrimSuffix(full[ln.start:ln.end], "\n")
	return strings.TrimSuffix(text, "\r")
}

// isUnifiedHunkHeader reports whether text is a unified-diff hunk header such
// as "@@ -1,8 +1,8 @@ func f()". A bare "@@ " marker is not enough.
func isUnifiedHunkHeader(text string) bool {
	if !strings.HasPrefix(text, "@@ -") {
		return false
	}
	if !strings.Contains(text, " +") {
		return false
	}
	return strings.Contains(text, " @@")
}

// classifyDiffLines assigns each line's kind and returns the first hunk header
// and the first hunk header that follows a "diff --git " file start (-1 when
// none does). inHunk tracks whether a hunk is open so a deleted line rendered
// as "--- " stays body, not a file header. inHeader is set only by evidence of
// a git file-header section (a diff --git line or a git metadata line), so a
// retained prefix that starts inside a hunk never reads its path-like body
// lines as a filename.
func classifyDiffLines(full string, lines []diffLine, startsMidLine bool) (firstHunk, firstHunkAfterFile int) {
	firstFile := -1
	firstHunk, firstHunkAfterFile = -1, -1
	inHunk := false
	inHeader := false
	for i := range lines {
		if i == 0 && startsMidLine {
			// The retention cap cut the first line mid-way. Its remaining bytes
			// may look like metadata or a path, so it cannot establish structure.
			continue
		}
		text := diffLineText(full, lines[i])
		switch {
		case strings.HasPrefix(text, "diff --git "):
			lines[i].kind = diffLineFile
			inHunk = false
			inHeader = true
			if firstFile < 0 {
				firstFile = i
			}
		case isUnifiedHunkHeader(text):
			lines[i].kind = diffLineHunk
			inHunk = true
			inHeader = false
			if firstHunk < 0 {
				firstHunk = i
			}
			if firstFile >= 0 && firstHunkAfterFile < 0 {
				firstHunkAfterFile = i
			}
		case !inHunk && isGitHeaderMetadata(text):
			inHeader = true
		case !inHunk && inHeader && strings.HasPrefix(text, "+++ "):
			lines[i].kind = diffLineNewPath
		case !inHunk && inHeader && strings.HasPrefix(text, "--- "):
			lines[i].kind = diffLineOldPath
		}
	}
	return firstHunk, firstHunkAfterFile
}

// firstCompleteLine returns the first complete line's text, skipping the
// fragment a mid-line retention cut left. ok is false when only the fragment
// is visible.
func firstCompleteLine(full string, startsMidLine bool) (string, bool) {
	lineEnd := strings.IndexByte(full, '\n')
	if !startsMidLine {
		if lineEnd >= 0 {
			return strings.TrimSuffix(full[:lineEnd], "\r"), true
		}
		return strings.TrimSuffix(full, "\r"), true
	}
	if lineEnd < 0 || lineEnd+1 >= len(full) {
		return "", false
	}
	rest := full[lineEnd+1:]
	if nextEnd := strings.IndexByte(rest, '\n'); nextEnd >= 0 {
		rest = rest[:nextEnd]
	}
	return strings.TrimSuffix(rest, "\r"), true
}

func isDiffBoundary(ln diffLine) bool {
	return ln.kind == diffLineFile || ln.kind == diffLineHunk
}

// isDiffContentStart reports whether a retained output's first line can be
// diff content or git metadata after the retention cap evicted a prefix.
func isDiffContentStart(line string) bool {
	if strings.HasPrefix(line, "diff --git ") || isUnifiedHunkHeader(line) {
		return true
	}
	if strings.HasPrefix(line, " ") ||
		strings.HasPrefix(line, "+") ||
		strings.HasPrefix(line, "-") ||
		strings.HasPrefix(line, `\`) {
		return true
	}
	return isGitHeaderMetadata(line)
}

// isGitHeaderMetadata reports whether a line is evidence that a git file-header
// section is open (or was, before the retention cap evicted its diff --git
// line). A body line has an added/removed/context prefix and never matches
// these column-zero prefixes.
func isGitHeaderMetadata(line string) bool {
	for _, prefix := range []string{
		"index ",
		"old mode ",
		"new mode ",
		"deleted file mode ",
		"new file mode ",
		"similarity index ",
		"rename from ",
		"rename to ",
		"copy from ",
		"copy to ",
		"Binary files ",
		"GIT binary patch",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// diffBoundaryIndexAtOrAfter returns the index of the first diff boundary whose
// line starts at or after minStart, or -1.
func diffBoundaryIndexAtOrAfter(lines []diffLine, minStart int) int {
	i := sort.Search(len(lines), func(i int) bool { return lines[i].start >= minStart })
	for ; i < len(lines); i++ {
		if isDiffBoundary(lines[i]) {
			return i
		}
	}
	return -1
}

// lineStartAtOrAfter returns the first line start at or after from, or fullLen
// when no line begins there.
func lineStartAtOrAfter(lines []diffLine, from, fullLen int) int {
	i := sort.Search(len(lines), func(i int) bool { return lines[i].start >= from })
	if i < len(lines) {
		return lines[i].start
	}
	return fullLen
}

// tailByteStart returns the start of a rune-safe byte suffix of at most
// tailBudget bytes. It is the fallback for an unterminated final line longer
// than the tail budget, where no line start fits.
func tailByteStart(full string, tailBudget int) int {
	if tailBudget <= 0 {
		return len(full)
	}
	start := max(len(full)-tailBudget, 0)
	trimmed := runetrim.TrimLeadingPartial([]byte(full[start:]))
	return len(full) - len(trimmed)
}

// lastLineBoundaryAtOrBefore returns the end of the last line that ends at or
// before limit, or 0.
func lastLineBoundaryAtOrBefore(lines []diffLine, limit int) int {
	end := 0
	for i := range lines {
		if lines[i].end > limit {
			break
		}
		end = lines[i].end
	}
	return end
}

// textLineCount counts the lines in a rendered text slice: newline separators
// plus a non-terminated final line.
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

// unifiedDiffWindow selects boundary-aligned windows for a completed shell
// output that is a git-style unified diff. headBudget and tailBudget are byte
// bounds. startsMidStream says the retention cap evicted the output's prefix,
// so a hunk header without a visible file header can still identify a diff.
// lineBudget bounds the rendered digest including its footer (0 = unbounded);
// the optional tail is dropped when it would push the registry past that
// bound, because the registry would otherwise cut the selected hunk again.
func unifiedDiffWindow(full string, headBudget, tailBudget int, startsMidStream, startsMidLine bool, lineBudget int) (diffWindow, bool) {
	if full == "" || headBudget <= 0 || tailBudget <= 0 {
		return diffWindow{}, false
	}
	// Cheap rejection before allocating the line table. A complete diff is
	// anchored at its first line; a retained diff must still start like diff
	// content. Either way, only then is the hunk marker scan worth a pass.
	if startsMidStream {
		first, ok := firstCompleteLine(full, startsMidLine)
		if !ok || !isDiffContentStart(first) {
			return diffWindow{}, false
		}
	} else if !strings.HasPrefix(full, "diff --git ") {
		return diffWindow{}, false
	}
	if !strings.Contains(full, "@@ -") {
		return diffWindow{}, false
	}
	lines := scanDiffLines(full)
	firstHunk, firstHunkAfterFile := classifyDiffLines(full, lines, startsMidLine)
	if startsMidStream {
		// The retention cap may have evicted the file header, but the visible
		// prefix must still look like diff content and carry a hunk header
		// close enough to be the one the head window is about.
		contentIndex := 0
		if startsMidLine {
			contentIndex = 1
		}
		if contentIndex >= len(lines) || !isDiffContentStart(diffLineText(full, lines[contentIndex])) ||
			firstHunk < 0 || lines[firstHunk].start > headBudget {
			return diffWindow{}, false
		}
	} else if !strings.HasPrefix(diffLineText(full, lines[0]), "diff --git ") || firstHunkAfterFile < 0 {
		// Anchor a complete diff at its first line. A log or test failure that
		// merely quotes a diff keeps the existing head-and-tail digest.
		return diffWindow{}, false
	}

	headStart := 0
	if startsMidStream {
		firstIndex := 0
		if startsMidLine {
			firstIndex = 1
		}
		if firstIndex >= len(lines) {
			return diffWindow{}, false
		}
		if !isDiffBoundary(lines[firstIndex]) {
			next := diffBoundaryIndexAtOrAfter(lines, lines[firstIndex].start)
			if next < 0 {
				return diffWindow{}, false
			}
			headStart = lines[next].start
		} else {
			headStart = lines[firstIndex].start
		}
	}

	limit := headStart + headBudget
	startIndex := 0
	for startIndex < len(lines) && lines[startIndex].start < headStart {
		startIndex++
	}
	headAtHunk := startIndex < len(lines) && lines[startIndex].kind == diffLineHunk
	headEnd := headStart
	for i := startIndex + 1; i < len(lines); i++ {
		if lines[i].start > limit {
			break
		}
		if isDiffBoundary(lines[i]) {
			headEnd = lines[i].start
		}
	}
	if headEnd == headStart {
		if headStart > 0 || headAtHunk {
			// No complete hunk or file header fits after the retained start,
			// or the retained start is itself a hunk header whose body is
			// larger than the budget. Show none of it rather than a partial
			// hunk.
			headEnd = headStart
		} else {
			// The leading file header itself is larger than the budget. A line
			// cut here is still outside every hunk body.
			headEnd = lastLineBoundaryAtOrBefore(lines, limit)
		}
	}

	tailIndex := diffBoundaryIndexAtOrAfter(lines, max(len(full)-tailBudget, headEnd+1))
	var tailStart int
	if tailIndex >= 0 {
		tailStart = lines[tailIndex].start
	} else {
		// No complete hunk or file header fits in the tail budget (one huge
		// hunk, or trailing non-diff output). Keep the existing line-aligned
		// peek rather than dropping the last 512 bytes entirely. The result
		// still ends at the natural end of the output.
		tailStart = lineStartAtOrAfter(lines, len(full)-tailBudget, len(full))
		if tailStart == len(full) {
			// An unterminated final line longer than the budget has no line
			// start to snap to; keep the same byte suffix the old digest did,
			// rune-trimmed.
			tailStart = tailByteStart(full, tailBudget)
		}
		if tailStart <= headEnd {
			tailStart = len(full)
		}
	}
	if lineBudget > 0 && tailStart < len(full) {
		// The rendered digest is head lines + marker + tail lines + footer. The
		// head alone is always small enough at the default 512-byte budget; the
		// tail is the optional part, so drop it and advertise the whole
		// remainder as omitted rather than let truncateLines cut its hunks.
		renderedLines := textLineCount(full[headStart:headEnd]) + 1 + textLineCount(full[tailStart:]) + 1
		if renderedLines > lineBudget {
			tailStart = len(full)
		}
	}

	file, hunk := describeDiffCut(full, lines, headEnd, tailStart)
	return diffWindow{HeadStart: headStart, HeadEnd: headEnd, TailStart: tailStart, File: file, Hunk: hunk}, true
}

// describeDiffCut names the file and, when one begins the omitted range, the
// hunk at cut. A hunk at or after limit (the tail start) is visible in the tail
// and must not be described as omitted.
func describeDiffCut(full string, lines []diffLine, cut, limit int) (file, hunk string) {
	next := diffBoundaryIndexAtOrAfter(lines, cut)
	if next < 0 {
		return "", ""
	}
	if lines[next].start == cut {
		return diffPathAroundCut(full, lines, next), firstHunkAtOrAfter(full, lines, next, limit)
	}

	// The cut is inside a section: the omitted range begins in the file that
	// started at the last file boundary at or before it, not in the next one.
	// A following file start cannot begin an omitted hunk in this section.
	if fileIndex := lastDiffFileIndexAtOrBefore(lines, cut); fileIndex >= 0 {
		file = diffPathAroundCut(full, lines, fileIndex)
	}
	if lines[next].kind == diffLineHunk {
		hunk = firstHunkAtOrAfter(full, lines, next, limit)
	}
	return file, hunk
}

// firstHunkAtOrAfter returns the first hunk header at or after from, before
// limit and before the next file section.
func firstHunkAtOrAfter(full string, lines []diffLine, from, limit int) string {
	for i := from; i < len(lines); i++ {
		if lines[i].start >= limit {
			break
		}
		if i > from && lines[i].kind == diffLineFile {
			break
		}
		if lines[i].kind == diffLineHunk {
			return clipDiffText(diffLineText(full, lines[i]))
		}
	}
	return ""
}

// lastDiffFileIndexAtOrBefore returns the last file-start index at or before
// at, or -1.
func lastDiffFileIndexAtOrBefore(lines []diffLine, at int) int {
	i := sort.Search(len(lines), func(i int) bool { return lines[i].start > at }) - 1
	for ; i >= 0; i-- {
		if lines[i].kind == diffLineFile {
			return i
		}
	}
	return -1
}

func diffPathAroundCut(full string, lines []diffLine, cutIndex int) string {
	newPath, oldPath := "", ""
	collect := func(i int) {
		text := diffLineText(full, lines[i])
		switch lines[i].kind {
		case diffLineNewPath:
			if newPath == "" {
				newPath = strings.TrimSpace(strings.TrimPrefix(text, "+++ "))
			}
		case diffLineOldPath:
			if oldPath == "" {
				oldPath = strings.TrimSpace(strings.TrimPrefix(text, "--- "))
			}
		}
	}
	if lines[cutIndex].kind == diffLineFile {
		for i := cutIndex + 1; i < len(lines); i++ {
			if lines[i].kind == diffLineHunk || lines[i].kind == diffLineFile {
				break
			}
			collect(i)
		}
	} else {
		for i := cutIndex - 1; i >= 0; i-- {
			if lines[i].kind == diffLineFile {
				break
			}
			collect(i)
		}
	}
	if newPath != "" && newPath != "/dev/null" {
		return clipDiffText(newPath)
	}
	return clipDiffText(oldPath)
}

func clipDiffText(s string) string {
	return runetrim.Cut(markerSafeText(strings.TrimSpace(s)), diffMarkerTextBytes)
}

// markerSafeText renders s as one valid UTF-8 display token: control bytes
// become visible escapes and invalid UTF-8 bytes become Git-style octal
// escapes, so a path can never add a line break or invalid bytes to the marker.
func markerSafeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '\n':
				b.WriteString(`\n`)
			case c == '\r':
				b.WriteString(`\r`)
			case c == '\t':
				b.WriteString(`\t`)
			case c < 0x20 || c == 0x7f:
				fmt.Fprintf(&b, `\%03o`, c)
			default:
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\%03o`, c)
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}

// renderDiffDigest renders the boundary-aligned head and optional tail around
// a marker stating the omitted lifetime byte range, its file and hunk, and the
// read_transcript continuation offset.
func renderDiffDigest(full string, w diffWindow, total, dropped int64) string {
	head := full[w.HeadStart:w.HeadEnd]
	tail := full[w.TailStart:]
	if total < 0 {
		total = 0
	}
	prefix := max(total-int64(len(full)), 0)
	start := min(prefix+int64(w.HeadEnd), total)
	end := min(prefix+int64(w.TailStart), total)
	elided := end - start

	var b strings.Builder
	b.WriteString(head)
	if len(head) > 0 && head[len(head)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("…[")
	fmt.Fprintf(&b, "%s elided", humanBytes(elided))
	if dropped > 0 {
		fmt.Fprintf(&b, "; %s dropped past the retention cap", humanBytes(dropped))
	}
	fmt.Fprintf(&b, "; omitted [%d,%d)", start, end)
	file, hunk := clipDiffText(w.File), clipDiffText(w.Hunk)
	if file != "" || hunk != "" {
		b.WriteString(" at")
		if file != "" {
			b.WriteByte(' ')
			b.WriteString(file)
		}
		if hunk != "" {
			b.WriteByte(' ')
			b.WriteString(hunk)
		}
	}
	if w.HeadStart > 0 {
		headAt := min(prefix+int64(w.HeadStart), total)
		fmt.Fprintf(&b, "; head starts at byte %d", headAt)
	}
	fmt.Fprintf(&b, "; read more with read_transcript(offset_bytes=%d) via the returned job transcript_ref]…", start)
	if len(tail) > 0 {
		b.WriteByte('\n')
		b.WriteString(tail)
	}
	return b.String()
}
