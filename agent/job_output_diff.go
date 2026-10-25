package agent

import (
	"fmt"
	"sort"
	"strings"

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
func classifyDiffLines(full string, lines []diffLine) (firstHunk, firstHunkAfterFile int) {
	firstFile := -1
	firstHunk, firstHunkAfterFile = -1, -1
	inHunk := false
	inHeader := false
	for i := range lines {
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

// unifiedDiffWindow selects boundary-aligned windows for a completed shell
// output that is a git-style unified diff. headBudget and tailBudget are byte
// bounds. startsMidStream says the retention cap evicted the output's prefix,
// so a hunk header without a visible file header can still identify a diff.
func unifiedDiffWindow(full string, headBudget, tailBudget int, startsMidStream bool) (diffWindow, bool) {
	if full == "" || headBudget <= 0 || tailBudget <= 0 {
		return diffWindow{}, false
	}
	// Cheap rejection before allocating the line table. A complete diff is
	// anchored at its first line; a retained diff must still start like diff
	// content. Either way, only then is the hunk marker scan worth a pass.
	if startsMidStream {
		firstLine := full
		if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
			firstLine = firstLine[:i]
		}
		if !isDiffContentStart(strings.TrimSuffix(firstLine, "\r")) {
			return diffWindow{}, false
		}
	} else if !strings.HasPrefix(full, "diff --git ") {
		return diffWindow{}, false
	}
	if !strings.Contains(full, "@@ -") {
		return diffWindow{}, false
	}
	lines := scanDiffLines(full)
	firstHunk, firstHunkAfterFile := classifyDiffLines(full, lines)
	if startsMidStream {
		// The retention cap may have evicted the file header, but the visible
		// prefix must still look like diff content and carry a hunk header
		// close enough to be the one the head window is about.
		if !isDiffContentStart(diffLineText(full, lines[0])) || firstHunk < 0 || lines[firstHunk].start > headBudget {
			return diffWindow{}, false
		}
	} else {
		// Anchor a complete diff at its first line. A log or test failure that
		// merely quotes a diff keeps the existing head-and-tail digest.
		if !strings.HasPrefix(diffLineText(full, lines[0]), "diff --git ") || firstHunkAfterFile < 0 {
			return diffWindow{}, false
		}
	}

	headStart := 0
	if startsMidStream && !isDiffBoundary(lines[0]) {
		next := diffBoundaryIndexAtOrAfter(lines, 0)
		if next < 0 {
			return diffWindow{}, false
		}
		headStart = lines[next].start
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
	tailStart := len(full)
	if tailIndex >= 0 {
		tailStart = lines[tailIndex].start
	} else {
		// No complete hunk or file header fits in the tail budget (one huge
		// hunk, or trailing non-diff output). Keep the existing line-aligned
		// peek rather than dropping the last 512 bytes entirely. The result
		// still ends at the natural end of the output.
		tailStart = lineStartAtOrAfter(lines, len(full)-tailBudget, len(full))
		if tailStart <= headEnd {
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
	cutIndex := diffBoundaryIndexAtOrAfter(lines, cut)
	if cutIndex < 0 {
		return "", ""
	}
	for i := cutIndex; i < len(lines); i++ {
		if lines[i].start >= limit {
			break
		}
		if i > cutIndex && lines[i].kind == diffLineFile {
			break
		}
		if lines[i].kind == diffLineHunk {
			hunk = clipDiffText(diffLineText(full, lines[i]))
			break
		}
	}
	file = diffPathAroundCut(full, lines, cutIndex)
	return file, hunk
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
		if newPath == "" && oldPath == "" {
			// A mode-only or binary section has no ---/+++ lines; its
			// diff --git header still names the file.
			newPath = diffGitHeaderPath(diffLineText(full, lines[cutIndex]))
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

// diffGitHeaderPath extracts the b-side path from a git file header such as
// "diff --git a/x b/x". It is a display hint for sections with no +++ line.
func diffGitHeaderPath(text string) string {
	rest, ok := strings.CutPrefix(text, "diff --git ")
	if !ok || rest == "" {
		return ""
	}
	if i := strings.LastIndex(rest, " b/"); i >= 0 {
		return strings.TrimSpace(rest[i+1:])
	}
	fields := strings.Fields(rest)
	if len(fields) >= 2 {
		return strings.Trim(fields[len(fields)-1], `"`)
	}
	return ""
}

func clipDiffText(s string) string {
	return runetrim.Cut(strings.TrimSpace(s), diffMarkerTextBytes)
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
	if w.File != "" || w.Hunk != "" {
		b.WriteString(" at")
		if w.File != "" {
			b.WriteByte(' ')
			b.WriteString(w.File)
		}
		if w.Hunk != "" {
			b.WriteByte(' ')
			b.WriteString(w.Hunk)
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
