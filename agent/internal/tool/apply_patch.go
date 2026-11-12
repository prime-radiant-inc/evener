package tool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/execenv"
)

// ApplyPatch applies a codex-rs-style apply_patch v4a patch through fm, an
// execenv.FileMutator that owns path containment (and, under a sandbox policy,
// race-safe fd-anchored enforcement). This is a best-effort implementation
// intended for local agent loops.
func ApplyPatch(fm execenv.FileMutator, patch string) (string, error) {
	return ApplyPatchCtx(context.Background(), fm, patch)
}

// ApplyPatchCtx is ApplyPatch with the caller's context threaded to each op's
// read, so a cancelled call ends a read parked on a stream. The result lists
// one line per applied operation, led by its patchChangeKind, so the caller
// can tell a created, moved or deleted file from an updated one.
func ApplyPatchCtx(ctx context.Context, fm execenv.FileMutator, patch string) (string, error) {
	changes, err := applyPatchChanges(ctx, fm, patch)
	if err != nil {
		return "", err
	}
	if len(changes) == 0 {
		return "no changes", nil
	}
	lines := make([]string, len(changes))
	for i, change := range changes {
		lines[i] = change.String()
	}
	return "applied patch:\n" + strings.Join(lines, "\n"), nil
}

// applyPatchChanges parses patch and applies its operations in order,
// returning what each one did. It stops at the first operation that fails.
func applyPatchChanges(ctx context.Context, fm execenv.FileMutator, patch string) ([]patchChange, error) {
	ops, err := parseV4APatch(patch)
	if err != nil {
		return nil, err
	}
	changes := make([]patchChange, 0, len(ops))
	for _, op := range ops {
		change, err := op.apply(ctx, fm)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// patchChangeKind is what one applied operation did to its file. It leads the
// operation's line in apply_patch's result.
type patchChangeKind string

const (
	patchAdded   patchChangeKind = "added"
	patchUpdated patchChangeKind = "updated"
	patchMoved   patchChangeKind = "moved"
	patchDeleted patchChangeKind = "deleted"
)

// patchChange is one applied operation. from is set only for patchMoved: the
// file's path before the move, with path its destination.
type patchChange struct {
	kind patchChangeKind
	path string
	from string
}

// String renders the change as its result line: the kind and the path, or for
// a move the kind and "from -> path".
func (c patchChange) String() string {
	if c.kind == patchMoved {
		return string(c.kind) + " " + c.from + " -> " + c.path
	}
	return string(c.kind) + " " + c.path
}

type patchOp interface {
	apply(ctx context.Context, fm execenv.FileMutator) (patchChange, error)
}

// readFileRawCtx reads path through fm with the caller's context when fm
// carries it, else through the ctx-less entry — the optional-capability
// fallback the other seams use, so a FileMutator without the capability is
// unaffected.
func readFileRawCtx(ctx context.Context, fm execenv.FileMutator, path string) ([]byte, error) {
	if cf, ok := fm.(execenv.ContextualFileMutator); ok {
		return cf.ReadFileRawCtx(ctx, path)
	}
	return fm.ReadFileRaw(path)
}

type addFileOp struct {
	path  string
	lines []string
}

func (o addFileOp) apply(_ context.Context, fm execenv.FileMutator) (patchChange, error) {
	content := strings.Join(o.lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := fm.WriteFileRaw(o.path, []byte(content), 0o644); err != nil {
		return patchChange{}, err
	}
	return patchChange{kind: patchAdded, path: o.path}, nil
}

type deleteFileOp struct {
	path string
}

func (o deleteFileOp) apply(_ context.Context, fm execenv.FileMutator) (patchChange, error) {
	if err := fm.RemovePath(o.path); err != nil {
		return patchChange{}, err
	}
	return patchChange{kind: patchDeleted, path: o.path}, nil
}

type updateFileOp struct {
	path   string
	moveTo string
	hunks  [][]string // diff lines per hunk (may include leading @@ hint line)
}

func (o updateFileOp) apply(ctx context.Context, fm execenv.FileMutator) (patchChange, error) {
	origBytes, err := readFileRawCtx(ctx, fm, o.path)
	if err != nil {
		return patchChange{}, err
	}
	origText := strings.ReplaceAll(string(origBytes), "\r\n", "\n")
	hasFinalNL := strings.HasSuffix(origText, "\n")
	origLines := strings.Split(strings.TrimSuffix(origText, "\n"), "\n")

	out := make([]string, 0, len(origLines))
	pos := 0

	for _, h := range o.hunks {
		entries := replacementEntriesFromHunk(h)
		oldLines, newLines := replacementLinesFromEntries(entries)
		if len(oldLines) == 0 {
			out = append(out, newLines...)
			continue
		}

		hint := hintFromHunk(h)
		searchStart := pos
		if hint != "" {
			if hintIdx := indexOfLine(origLines, hint, pos); hintIdx >= 0 {
				searchStart = hintIdx
			}
		}

		k := seekLineSequence(origLines, oldLines, searchStart)
		if k < 0 && searchStart != pos {
			// Fall back to searching from pos if hint-narrowed search failed.
			k = seekLineSequence(origLines, oldLines, pos)
		}
		if k < 0 {
			hunkLine, diagLine := firstDifferingLine(origLines, oldLines, pos)
			if hunkLine == 0 {
				diagLine = mismatchLineForMissingSequence(origLines, oldLines[0], searchStart)
			}
			return patchChange{}, &PatchMismatchError{
				kind:      "expected lines not found",
				Path:      o.path,
				HunkLine:  hunkLine,
				Want:      oldLines[hunkLine],
				Line:      diagLine,
				Got:       lineAt(origLines, diagLine-1),
				GotEOF:    hunkLine > 0 && diagLine == 0,
				lines:     origLines,
				hunkLines: h,
			}
		}
		out = append(out, origLines[pos:k]...)
		out = append(out, applyReplacementEntries(origLines[k:k+len(oldLines)], entries)...)
		pos = k + len(oldLines)
	}

	out = append(out, origLines[pos:]...)
	newText := strings.Join(out, "\n")
	if hasFinalNL {
		newText += "\n"
	}
	if err := fm.WriteFileRaw(o.path, []byte(newText), 0o644); err != nil {
		return patchChange{}, err
	}
	if strings.TrimSpace(o.moveTo) != "" && o.moveTo != o.path {
		if err := fm.RenamePath(o.path, o.moveTo); err != nil {
			return patchChange{}, err
		}
		return patchChange{kind: patchMoved, path: o.moveTo, from: o.path}, nil
	}
	return patchChange{kind: patchUpdated, path: o.path}, nil
}

func parseV4APatch(patch string) ([]patchOp, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	return parseV4APatchLines(lines)
}

func parseV4APatchLines(lines []string) ([]patchOp, error) {
	i := 0
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "*** Begin Patch" {
		return nil, errors.New("apply_patch: expected '*** Begin Patch'")
	}
	i++

	var ops []patchOp
	for i < len(lines) {
		l := lines[i]
		i++
		if strings.TrimSpace(l) == "*** End Patch" {
			return ops, nil
		}
		if strings.TrimSpace(l) == "" {
			continue
		}
		switch {
		case strings.TrimSpace(l) == "*** End of File":
			continue
		case strings.HasPrefix(l, "*** Add File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Add File: "))
			var content []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "*** ") {
					break
				}
				if !strings.HasPrefix(lines[i], "+") {
					return nil, fmt.Errorf("apply_patch: add file %s: expected '+' line, got %q", path, lines[i])
				}
				content = append(content, strings.TrimPrefix(lines[i], "+"))
				i++
			}
			ops = append(ops, addFileOp{path: path, lines: content})
		case strings.HasPrefix(l, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Delete File: "))
			ops = append(ops, deleteFileOp{path: path})
		case strings.HasPrefix(l, "*** Update File: "):
			path := strings.TrimSpace(strings.TrimPrefix(l, "*** Update File: "))
			moveTo := ""
			if i < len(lines) && strings.HasPrefix(lines[i], "*** Move to: ") {
				moveTo = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to: "))
				i++
			}
			var hunks [][]string
			var cur []string
			for i < len(lines) {
				if strings.HasPrefix(lines[i], "*** ") || strings.TrimSpace(lines[i]) == "*** End Patch" {
					break
				}
				if strings.HasPrefix(lines[i], "@@") && len(cur) > 0 {
					hunks = append(hunks, cur)
					cur = []string{lines[i]}
					i++
					continue
				}
				cur = append(cur, lines[i])
				i++
			}
			if len(cur) > 0 {
				hunks = append(hunks, cur)
			}
			ops = append(ops, updateFileOp{path: path, moveTo: moveTo, hunks: hunks})
		default:
			return nil, fmt.Errorf("apply_patch: unexpected line: %q", l)
		}
	}
	return nil, errors.New("apply_patch: missing '*** End Patch'")
}

// hintFromHunk extracts the positioning hint text after @@ in a hunk.
// The hint (typically a function signature) narrows where we search for the anchor.
func hintFromHunk(hunkLines []string) string {
	for _, l := range hunkLines {
		if rest, ok := strings.CutPrefix(l, "@@"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// PatchMismatchError reports where an update hunk stopped matching the file;
// its message is what the model sees. HunkLine is the 0-based index into the
// hunk's old/context lines (context and '-' lines, in order) of the first one
// that differs from the closest partial match; Want is that hunk line and Got
// the file line at the same position (empty at end of file). Line is the
// 1-based file line of Got.
type PatchMismatchError struct {
	Path     string
	HunkLine int
	Want     string
	Got      string
	Line     int
	// GotEOF is set when the matching prefix ran to the end of the file, so
	// there is no file line at HunkLine (Got is empty, Line is 0).
	GotEOF bool

	kind      string
	lines     []string
	hunkLines []string
}

func (d *PatchMismatchError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "apply_patch: %s in %s", d.kind, d.Path)
	if d.Line > 0 {
		fmt.Fprintf(&b, " at line %d", d.Line)
	}
	fmt.Fprintf(&b, "\nfirst difference at old/context line %d of the hunk", d.HunkLine+1)
	fmt.Fprintf(&b, "\nwanted: %q", d.Want)
	if d.GotEOF {
		fmt.Fprintf(&b, "\ngot:    end of file after %d lines", len(d.lines))
	} else if d.Got != "" || d.Line > 0 {
		fmt.Fprintf(&b, "\ngot:    %q", d.Got)
	}

	contextLine := d.Line
	if d.GotEOF {
		contextLine = len(d.lines)
	}
	if contextLine > 0 {
		fmt.Fprintf(&b, "\n\nFile context around line %d:\n%s", contextLine, formatLineSnippet(d.lines, contextLine, 2))
	}

	if matches := candidateLineIndexes(d.lines, d.Want, d.Line, 3); len(matches) > 0 {
		fmt.Fprintf(&b, "\n\nNearby matches for wanted line:\n")
		for i, idx := range matches {
			if i > 0 {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "candidate at line %d:\n%s", idx+1, formatLineSnippet(d.lines, idx+1, 2))
		}
	}

	oldLines := oldLinesFromHunk(d.hunkLines)
	if len(oldLines) > 0 {
		fmt.Fprintf(&b, "\n\nExpected old/context lines from patch:\n%s", formatExpectedLines(oldLines))
		if matches := candidateSequenceIndexes(d.lines, oldLines, d.Line, 2); len(matches) > 0 {
			fmt.Fprintf(&b, "\n\nPotential locations for old/context block:\n")
			for i, idx := range matches {
				if i > 0 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "candidate at line %d:\n%s", idx+1, formatLineSnippet(d.lines, idx+1, min(6, max(2, len(oldLines)+1))))
			}
		}
	}

	if d.kind == "expected lines not found" {
		fmt.Fprintf(&b, "\n\nthe file has changed since the content this patch was built from; re-read the target region with read_file, then rebuild the patch")
	}
	return b.String()
}

func formatLineSnippet(lines []string, line, radius int) string {
	if len(lines) == 0 {
		return "(file is empty)\n"
	}
	if line < 1 {
		line = 1
	}
	if line > len(lines) {
		line = len(lines)
	}
	start := max(1, line-radius)
	end := min(len(lines), line+radius)
	width := len(strconv.Itoa(end))
	var b strings.Builder
	for i := start; i <= end; i++ {
		marker := " "
		if i == line {
			marker = ">"
		}
		fmt.Fprintf(&b, "%s %*d | %s\n", marker, width, i, lines[i-1])
	}
	return b.String()
}

func candidateLineIndexes(lines []string, want string, failedLine int, limit int) []int {
	var exact, contains []int
	normWant := normalizeUnicode(normalizeWS(want))
	for i, line := range lines {
		if failedLine > 0 && i == failedLine-1 {
			continue
		}
		if fuzzyLineMatch(line, want) {
			exact = append(exact, i)
			continue
		}
		if normWant != "" && strings.Contains(normalizeUnicode(normalizeWS(line)), normWant) {
			contains = append(contains, i)
		}
	}
	return nearestIndexes(append(exact, contains...), failedLine, limit)
}

func candidateSequenceIndexes(lines, pattern []string, failedLine int, limit int) []int {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return nil
	}
	var out []int
	for i := 0; i <= len(lines)-len(pattern); i++ {
		if failedLine > 0 && i <= failedLine-1 && failedLine-1 < i+len(pattern) {
			continue
		}
		ok := true
		for j := range pattern {
			if !fuzzyLineMatch(lines[i+j], pattern[j]) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	if len(out) > 0 {
		return nearestIndexes(out, failedLine, limit)
	}
	return looseCandidateSequenceIndexes(lines, pattern, failedLine, limit)
}

func looseCandidateSequenceIndexes(lines, pattern []string, failedLine int, limit int) []int {
	minScore := min(2, len(pattern))
	window := min(len(pattern)+4, len(lines))
	var out []int
	for i := range lines {
		if !fuzzyLineMatch(lines[i], pattern[0]) {
			continue
		}
		end := min(len(lines), i+window)
		score := 0
		next := 0
		for j := i; j < end && next < len(pattern); j++ {
			if fuzzyLineMatch(lines[j], pattern[next]) {
				score++
				next++
			}
		}
		if score >= minScore {
			out = append(out, i)
		}
	}
	return nearestIndexes(out, failedLine, limit)
}

// firstDifferingLine finds where pattern comes closest to matching lines at or
// after start: the file position whose leading run of fuzzy-matching lines is
// longest. It returns the pattern index of the first line that fails there and
// the 1-based file line at that position (0 past the end of the file). With no
// line matching anywhere it returns pattern index 0 and file line 0.
func firstDifferingLine(lines, pattern []string, start int) (int, int) {
	bestRun, bestAt := 0, -1
	for i := max(start, 0); i < len(lines); i++ {
		run := 0
		for run < len(pattern) && i+run < len(lines) && fuzzyLineMatch(lines[i+run], pattern[run]) {
			run++
		}
		if run > bestRun {
			bestRun, bestAt = run, i
		}
	}
	if bestAt < 0 || bestRun >= len(pattern) {
		return 0, 0
	}
	if bestAt+bestRun >= len(lines) {
		return bestRun, 0
	}
	return bestRun, bestAt + bestRun + 1
}

func mismatchLineForMissingSequence(lines []string, want string, searchStart int) int {
	for i := searchStart; i < len(lines); i++ {
		if fuzzyLineMatch(lines[i], want) {
			return i + 1
		}
	}
	if len(lines) == 0 {
		return 0
	}
	return min(searchStart+1, len(lines))
}

func nearestIndexes(indexes []int, failedLine int, limit int) []int {
	if len(indexes) <= limit {
		return indexes
	}
	if failedLine <= 0 {
		return indexes[:limit]
	}
	type candidate struct {
		index int
		dist  int
	}
	candidates := make([]candidate, 0, len(indexes))
	for _, idx := range indexes {
		dist := idx + 1 - failedLine
		if dist < 0 {
			dist = -dist
		}
		candidates = append(candidates, candidate{index: idx, dist: dist})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist == candidates[j].dist {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].dist < candidates[j].dist
	})
	out := make([]int, 0, limit)
	for i := 0; i < limit && i < len(candidates); i++ {
		out = append(out, candidates[i].index)
	}
	return out
}

func oldLinesFromHunk(hunk []string) []string {
	oldLines, _ := replacementLinesFromEntries(replacementEntriesFromHunk(hunk))
	return oldLines
}

type replacementEntry struct {
	prefix byte
	body   string
}

func replacementEntriesFromHunk(hunk []string) []replacementEntry {
	// Empty lines after the last hunk line are separators before the next
	// hunk or file marker, not context.
	for len(hunk) > 0 && hunk[len(hunk)-1] == "" {
		hunk = hunk[:len(hunk)-1]
	}
	var entries []replacementEntry
	for _, l := range hunk {
		if strings.HasPrefix(l, "@@") {
			continue
		}
		if l == "" {
			// A blank context line written without its leading space.
			entries = append(entries, replacementEntry{prefix: ' '})
			continue
		}
		switch l[0] {
		case ' ', '-', '+':
			entries = append(entries, replacementEntry{prefix: l[0], body: l[1:]})
		}
	}
	return entries
}

func replacementLinesFromEntries(entries []replacementEntry) ([]string, []string) {
	var oldLines []string
	var newLines []string
	for _, entry := range entries {
		switch entry.prefix {
		case ' ':
			oldLines = append(oldLines, entry.body)
			newLines = append(newLines, entry.body)
		case '-':
			oldLines = append(oldLines, entry.body)
		case '+':
			newLines = append(newLines, entry.body)
		}
	}
	return oldLines, newLines
}

func applyReplacementEntries(matchedOldLines []string, entries []replacementEntry) []string {
	out := make([]string, 0, len(entries))
	oldIdx := 0
	for _, entry := range entries {
		switch entry.prefix {
		case ' ':
			if oldIdx < len(matchedOldLines) {
				out = append(out, matchedOldLines[oldIdx])
			} else {
				out = append(out, entry.body)
			}
			oldIdx++
		case '-':
			oldIdx++
		case '+':
			out = append(out, entry.body)
		}
	}
	return out
}

func seekLineSequence(lines, pattern []string, start int) int {
	if len(pattern) == 0 {
		return start
	}
	if start < 0 {
		start = 0
	}
	if len(pattern) > len(lines) || start > len(lines)-len(pattern) {
		return -1
	}

	for i := start; i <= len(lines)-len(pattern); i++ {
		if lineSequenceMatches(lines[i:i+len(pattern)], pattern, matchExact) {
			return i
		}
	}
	for i := start; i <= len(lines)-len(pattern); i++ {
		if lineSequenceMatches(lines[i:i+len(pattern)], pattern, matchTrimRight) {
			return i
		}
	}
	for i := start; i <= len(lines)-len(pattern); i++ {
		if lineSequenceMatches(lines[i:i+len(pattern)], pattern, matchTrimBoth) {
			return i
		}
	}
	for i := start; i <= len(lines)-len(pattern); i++ {
		if lineSequenceMatches(lines[i:i+len(pattern)], pattern, matchUnicodeNormalized) {
			return i
		}
	}
	for i := start; i <= len(lines)-len(pattern); i++ {
		if lineSequenceMatches(lines[i:i+len(pattern)], pattern, matchFuzzyLine) {
			return i
		}
	}
	return -1
}

type lineMatchMode int

const (
	matchExact lineMatchMode = iota
	matchTrimRight
	matchTrimBoth
	matchUnicodeNormalized
	matchFuzzyLine
)

func lineSequenceMatches(lines, pattern []string, mode lineMatchMode) bool {
	for i := range pattern {
		if !lineMatchesMode(lines[i], pattern[i], mode) {
			return false
		}
	}
	return true
}

func lineMatchesMode(a, b string, mode lineMatchMode) bool {
	switch mode {
	case matchExact:
		return a == b
	case matchTrimRight:
		return strings.TrimRight(a, " \t\r") == strings.TrimRight(b, " \t\r")
	case matchTrimBoth:
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	case matchUnicodeNormalized:
		return normalizeUnicode(strings.TrimSpace(a)) == normalizeUnicode(strings.TrimSpace(b))
	case matchFuzzyLine:
		return fuzzyLineMatch(a, b)
	default:
		return false
	}
}

func lineAt(lines []string, idx int) string {
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	return lines[idx]
}

func formatExpectedLines(lines []string) string {
	var b strings.Builder
	limit := min(len(lines), 12)
	for _, line := range lines[:limit] {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if len(lines) > limit {
		fmt.Fprintf(&b, "  ... %d more lines omitted\n", len(lines)-limit)
	}
	return b.String()
}

// normalizeWS collapses all whitespace runs to single spaces and trims ends.
func normalizeWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var unicodePunctReplacer = strings.NewReplacer(
	"\u2018", "'", "\u2019", "'", // left/right single quote
	"\u201C", "\"", "\u201D", "\"", // left/right double quote
	"\u2013", "-", "\u2014", "-", // en-dash, em-dash
	"\u2026", "...", // ellipsis
	"\u00A0", " ", // non-breaking space
)

func normalizeUnicode(s string) string {
	return unicodePunctReplacer.Replace(s)
}

// fuzzyLineMatch returns true if a and b match after whitespace normalization
// and (if needed) Unicode punctuation normalization.
func fuzzyLineMatch(a, b string) bool {
	if a == b {
		return true
	}
	na, nb := normalizeWS(a), normalizeWS(b)
	if na == nb {
		return true
	}
	if na == "" {
		return false
	}
	return normalizeUnicode(na) == normalizeUnicode(nb)
}

func indexOfLine(lines []string, want string, start int) int {
	// Try exact match first.
	for i := start; i < len(lines); i++ {
		if lines[i] == want {
			return i
		}
	}
	// Fuzzy: whitespace-normalized match.
	normWant := normalizeWS(want)
	if normWant == "" {
		return -1
	}
	for i := start; i < len(lines); i++ {
		if normalizeWS(lines[i]) == normWant {
			return i
		}
	}
	// Fuzzy: Unicode punctuation equivalence.
	normUniWant := normalizeUnicode(normWant)
	for i := start; i < len(lines); i++ {
		if normalizeUnicode(normalizeWS(lines[i])) == normUniWant {
			return i
		}
	}
	return -1
}
