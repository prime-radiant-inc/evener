package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ApplyPatch applies a codex-rs-style apply_patch v4a patch to files under rootDir.
// This is a best-effort implementation intended for local agent loops.
func ApplyPatch(rootDir string, patch string) (string, error) {
	ops, err := parseV4APatch(patch)
	if err != nil {
		return "", err
	}
	var touched []string
	for _, op := range ops {
		paths, err := op.apply(rootDir)
		if err != nil {
			return "", err
		}
		touched = append(touched, paths...)
	}
	if len(touched) == 0 {
		return "no changes", nil
	}
	return "applied patch to:\n" + strings.Join(touched, "\n"), nil
}

type patchOp interface {
	apply(rootDir string) ([]string, error)
}

type addFileOp struct {
	path  string
	lines []string
}

func (o addFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	content := strings.Join(o.lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return []string{o.path}, nil
}

type deleteFileOp struct {
	path string
}

func (o deleteFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(p)
	return []string{o.path}, nil
}

type updateFileOp struct {
	path   string
	moveTo string
	hunks  [][]string // diff lines per hunk (may include leading @@ hint line)
}

func (o updateFileOp) apply(rootDir string) ([]string, error) {
	p, err := safeJoin(rootDir, o.path)
	if err != nil {
		return nil, err
	}
	origBytes, err := os.ReadFile(p)
	if err != nil {
		return nil, err
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
			diagLine := mismatchLineForMissingSequence(origLines, oldLines[0], searchStart)
			return nil, fmt.Errorf("%s", formatPatchMismatchError(patchMismatchDiagnostic{
				kind:      "expected lines not found",
				path:      o.path,
				want:      oldLines[0],
				line:      diagLine,
				got:       lineAt(origLines, diagLine-1),
				lines:     origLines,
				hunkLines: h,
			}))
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
	if err := os.WriteFile(p, []byte(newText), 0o644); err != nil {
		return nil, err
	}
	paths := []string{o.path}
	if strings.TrimSpace(o.moveTo) != "" && o.moveTo != o.path {
		dst, err := safeJoin(rootDir, o.moveTo)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(p, dst); err != nil {
			return nil, err
		}
		paths = append(paths, o.moveTo)
	}
	return paths, nil
}

func parseV4APatch(patch string) ([]patchOp, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	return parseV4APatchLines(lines)
}

func parseV4APatchLines(lines []string) ([]patchOp, error) {
	i := 0
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "*** Begin Patch" {
		return nil, fmt.Errorf("apply_patch: expected '*** Begin Patch'")
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
				if strings.TrimSpace(lines[i]) == "*** End Patch" {
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
	return nil, fmt.Errorf("apply_patch: missing '*** End Patch'")
}

func safeJoin(rootDir, rel string) (string, error) {
	r := strings.TrimSpace(rel)
	if r == "" {
		return "", fmt.Errorf("empty path")
	}
	// Allow absolute paths that fall under rootDir by stripping the prefix.
	if filepath.IsAbs(r) {
		cleanRoot := filepath.Clean(rootDir) + string(filepath.Separator)
		cleanR := filepath.Clean(r)
		if strings.HasPrefix(cleanR, cleanRoot) {
			r = cleanR[len(cleanRoot):]
		} else if cleanR == filepath.Clean(rootDir) {
			return "", fmt.Errorf("path is rootDir itself: %s", rel)
		} else {
			return "", fmt.Errorf("absolute path outside working directory: %s", rel)
		}
	}
	clean := filepath.Clean(r)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal not allowed: %s", rel)
	}
	return filepath.Join(rootDir, clean), nil
}

// hintFromHunk extracts the positioning hint text after @@ in a hunk.
// The hint (typically a function signature) narrows where we search for the anchor.
func hintFromHunk(hunkLines []string) string {
	for _, l := range hunkLines {
		if strings.HasPrefix(l, "@@") {
			return strings.TrimSpace(strings.TrimPrefix(l, "@@"))
		}
	}
	return ""
}

type patchMismatchDiagnostic struct {
	kind      string
	path      string
	want      string
	got       string
	line      int
	gotEOF    bool
	lines     []string
	hunkLines []string
}

func formatPatchMismatchError(d patchMismatchDiagnostic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apply_patch: %s in %s", d.kind, d.path)
	if d.line > 0 {
		fmt.Fprintf(&b, " at line %d", d.line)
	}
	fmt.Fprintf(&b, "\nwanted: %q", d.want)
	if d.gotEOF {
		fmt.Fprintf(&b, "\ngot:    end of file after %d lines", len(d.lines))
	} else if d.got != "" || d.line > 0 {
		fmt.Fprintf(&b, "\ngot:    %q", d.got)
	}

	if d.line > 0 {
		fmt.Fprintf(&b, "\n\nFile context around line %d:\n%s", d.line, formatLineSnippet(d.lines, d.line, 2))
	}

	if matches := candidateLineIndexes(d.lines, d.want, d.line, 3); len(matches) > 0 {
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
		if matches := candidateSequenceIndexes(d.lines, oldLines, d.line, 2); len(matches) > 0 {
			fmt.Fprintf(&b, "\n\nPotential locations for old/context block:\n")
			for i, idx := range matches {
				if i > 0 {
					b.WriteByte('\n')
				}
				fmt.Fprintf(&b, "candidate at line %d:\n%s", idx+1, formatLineSnippet(d.lines, idx+1, minInt(6, maxInt(2, len(oldLines)+1))))
			}
		}
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
	start := maxInt(1, line-radius)
	end := minInt(len(lines), line+radius)
	width := len(fmt.Sprint(end))
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
	minScore := minInt(2, len(pattern))
	window := len(pattern) + 4
	if window > len(lines) {
		window = len(lines)
	}
	var out []int
	for i := 0; i < len(lines); i++ {
		if !fuzzyLineMatch(lines[i], pattern[0]) {
			continue
		}
		end := minInt(len(lines), i+window)
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

func mismatchLineForMissingSequence(lines []string, want string, searchStart int) int {
	for i := searchStart; i < len(lines); i++ {
		if fuzzyLineMatch(lines[i], want) {
			return i + 1
		}
	}
	if len(lines) == 0 {
		return 0
	}
	return minInt(searchStart+1, len(lines))
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
	var entries []replacementEntry
	for _, l := range hunk {
		if l == "" || strings.HasPrefix(l, "@@") {
			continue
		}
		switch l[0] {
		case ' ', '-', '+':
			body := ""
			if len(l) > 1 {
				body = l[1:]
			}
			entries = append(entries, replacementEntry{prefix: l[0], body: body})
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
	limit := minInt(len(lines), 12)
	for _, line := range lines[:limit] {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if len(lines) > limit {
		fmt.Fprintf(&b, "  ... %d more lines omitted\n", len(lines)-limit)
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
