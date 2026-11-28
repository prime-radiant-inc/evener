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

// PatchEffect records one file mutation committed by ApplyPatchTracked.
type PatchEffect struct {
	Path    string
	Deleted bool
}

// ApplyPatch applies a codex-rs-style apply_patch v4a patch through fm, an
// execenv.FileMutator that owns path containment (and, under a sandbox policy,
// race-safe fd-anchored enforcement). It threads ctx to each op's read so a
// cancelled call ends a read parked on a stream. This is a best-effort
// implementation intended for local agent loops.
func ApplyPatch(ctx context.Context, fm execenv.FileMutator, patch string) (string, error) {
	out, _, err := ApplyPatchTracked(ctx, fm, patch)
	return out, err
}

// ApplyPatchTracked behaves like ApplyPatch and returns the committed effects
// in order, including earlier sections that landed before an error.
func ApplyPatchTracked(ctx context.Context, fm execenv.FileMutator, patch string) (string, []PatchEffect, error) {
	ops, err := parseV4APatch(patch)
	if err != nil {
		return "", nil, err
	}
	var touched []string
	var effects []PatchEffect
	for i := 0; i < len(ops); {
		// Consecutive *** Update File: sections naming one file share one read
		// and one write of it, instead of each one rewriting the whole file
		// (issue #6430).
		end := coalescedUpdateRun(ops, i)
		var paths []string
		if end-i > 1 {
			paths, err = applyUpdateRun(ctx, fm, ops[i:end])
			if len(paths) > 0 {
				effects = append(effects, PatchEffect{Path: ops[i].(updateFileOp).path})
			}
		} else if paths, err = ops[i].apply(ctx, fm); err == nil {
			switch o := ops[i].(type) {
			case addFileOp:
				effects = append(effects, PatchEffect{Path: o.path})
			case deleteFileOp:
				effects = append(effects, PatchEffect{Path: o.path, Deleted: true})
			case updateFileOp:
				if moveTo := o.moveTarget(); moveTo != "" && moveTo != o.path {
					effects = append(effects, PatchEffect{Path: o.path, Deleted: true}, PatchEffect{Path: moveTo})
				} else {
					effects = append(effects, PatchEffect{Path: o.path})
				}
			}
		} else if u, ok := ops[i].(updateFileOp); ok && len(paths) > 0 {
			effects = append(effects, PatchEffect{Path: u.path})
		}
		if err != nil {
			return "", effects, err
		}
		touched = append(touched, paths...)
		i = end
	}
	if len(touched) == 0 {
		return "no changes", nil, nil
	}
	return "applied patch to:\n" + strings.Join(touched, "\n"), effects, nil
}

type patchOp interface {
	apply(ctx context.Context, fm execenv.FileMutator) ([]string, error)
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

func (o addFileOp) apply(_ context.Context, fm execenv.FileMutator) ([]string, error) {
	content := strings.Join(o.lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := fm.WriteFileRaw(o.path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return []string{"A " + o.path}, nil
}

type deleteFileOp struct {
	path string
}

func (o deleteFileOp) apply(_ context.Context, fm execenv.FileMutator) ([]string, error) {
	if err := fm.RemovePath(o.path); err != nil {
		return nil, err
	}
	return []string{"D " + o.path}, nil
}

type updateFileOp struct {
	path   string
	moveTo string
	hunks  [][]string // diff lines per hunk (may include leading @@ hint line)
}

// coalescedUpdateRun returns the end (exclusive) of the run of update
// operations at start that can share one read and one write of their file: the
// maximal run of consecutive *** Update File: sections naming one path and
// carrying a *** Move to:. A section carrying a *** Move to: runs alone, so its
// rename lands before the next section reads the path — and, when it starts the
// run, so its rename is not dropped by being folded into a run.
func coalescedUpdateRun(ops []patchOp, start int) int {
	first, ok := ops[start].(updateFileOp)
	if !ok || first.moveTarget() != "" {
		return start + 1
	}
	end := start + 1
	for end < len(ops) {
		next, ok := ops[end].(updateFileOp)
		if !ok || next.path != first.path || next.moveTarget() != "" {
			break
		}
		end++
	}
	return end
}

// moveTarget is o's *** Move to: path, empty when it carries none.
func (o updateFileOp) moveTarget() string { return strings.TrimSpace(o.moveTo) }

// patchFileState is one file as the applier holds it: the lines a whole-file
// read normalizes to, and whether the file ended with a newline. It is what a
// read of the file plus one update section's rewrite produces, so a run of
// sections can carry it between them instead of rewriting and re-reading the
// file after each.
type patchFileState struct {
	lines   []string
	finalNL bool
}

// loadPatchFileState reads path into the state an update section starts from.
func loadPatchFileState(ctx context.Context, fm execenv.FileMutator, path string) (patchFileState, error) {
	origBytes, err := readFileRawCtx(ctx, fm, path)
	if err != nil {
		return patchFileState{}, err
	}
	return patchStateFromText(strings.ReplaceAll(string(origBytes), "\r\n", "\n")), nil
}

// patchStateFromText is the state a read of CRLF-normalized text yields.
func patchStateFromText(text string) patchFileState {
	return patchFileState{
		lines:   strings.Split(strings.TrimSuffix(text, "\n"), "\n"),
		finalNL: strings.HasSuffix(text, "\n"),
	}
}

// text is the file's bytes as the applier writes them.
func (s patchFileState) text() string {
	text := strings.Join(s.lines, "\n")
	if s.finalNL {
		text += "\n"
	}
	return text
}

// reread returns the state a read of the written bytes yields — the state the
// next section started from when each section wrote and re-read the file
// itself. It replaces the written form of an empty last line with the final
// newline a read sees, and strips a trailing CR from a line the write
// terminates, because the read normalizes CRLF. Both forms write the same
// bytes, so canonicalizing never changes what lands on disk.
func (s patchFileState) reread() patchFileState {
	// No lines reads as one empty line.
	if len(s.lines) == 0 {
		return patchFileState{lines: []string{""}, finalNL: s.finalNL}
	}
	// One empty line with no final newline writes no bytes at all.
	if len(s.lines) == 1 && s.lines[0] == "" && !s.finalNL {
		return patchFileState{lines: []string{""}}
	}
	// A line is terminated by a newline in the written text unless it is the
	// last line of a file with no final newline.
	lastTerminated := s.finalNL
	lines := s.lines
	var stripped []string
	for i, line := range lines {
		terminated := i < len(lines)-1 || lastTerminated
		if terminated && strings.HasSuffix(line, "\r") {
			if stripped == nil {
				stripped = append([]string(nil), lines...)
			}
			stripped[i] = strings.TrimSuffix(line, "\r")
		}
	}
	if stripped != nil {
		lines = stripped
	}
	if s.finalNL || lines[len(lines)-1] != "" {
		return patchFileState{lines: lines, finalNL: s.finalNL}
	}
	// The empty last line is now the final newline instead.
	return patchFileState{lines: lines[:len(lines)-1], finalNL: true}
}

// writePatchFileState writes state to path at the permission every patch write
// uses.
func writePatchFileState(fm execenv.FileMutator, path string, state patchFileState) error {
	return fm.WriteFileRaw(path, []byte(state.text()), 0o644)
}

// applyUpdateRun applies overlapping sections of one file in order, over one
// in-memory copy of it, and writes the file once. Each section matches its
// hunks against the lines a per-section apply would have re-read after the
// sections before it, so a hunk that cannot match fails at its own section with
// the same diagnostic. The bytes written are the last section's own output — a
// re-read only ever feeds the next section's matching, never the write.
func applyUpdateRun(ctx context.Context, fm execenv.FileMutator, ops []patchOp) ([]string, error) {
	path := ops[0].(updateFileOp).path
	// at is the state a read of the file yields; written is what the last
	// completed section produced, which is what lands on disk.
	at, err := loadPatchFileState(ctx, fm, path)
	if err != nil {
		return nil, err
	}
	written := at
	var touched []string
	for i, op := range ops {
		// A section after the first applied on its own re-read the file before
		// it ran, and that read reports a cancelled context. The run's one read
		// already happened, so the cancellation is reported here instead.
		if len(touched) > 0 {
			if err := ctx.Err(); err != nil {
				return failUpdateRun(fm, path, written, touched, err)
			}
		}
		next, err := op.(updateFileOp).update(at)
		if err != nil {
			return failUpdateRun(fm, path, written, touched, err)
		}
		written = next
		// Only a section that follows this one matches against the re-read.
		if i < len(ops)-1 {
			at = next.reread()
		}
		touched = append(touched, "M "+path)
	}
	if err := writePatchFileState(fm, path, written); err != nil {
		return nil, err
	}
	return touched, nil
}

// failUpdateRun reports err, after writing the sections that completed: applied
// one at a time, each section wrote itself before the next one ran, so those
// bytes are on disk ahead of the failure. A failure to write them is reported
// in their place.
func failUpdateRun(fm execenv.FileMutator, path string, written patchFileState, touched []string, err error) ([]string, error) {
	if len(touched) > 0 {
		if writeErr := writePatchFileState(fm, path, written); writeErr != nil {
			return nil, writeErr
		}
		return touched, err
	}
	return nil, err
}

func (o updateFileOp) apply(ctx context.Context, fm execenv.FileMutator) ([]string, error) {
	// A lone section is a run of one, so it matches, writes, and fails exactly
	// as the same section does inside a longer run.
	if _, err := applyUpdateRun(ctx, fm, []patchOp{o}); err != nil {
		return nil, err
	}
	moveTo := o.moveTarget()
	if moveTo != "" && moveTo != o.path {
		if err := fm.RenamePath(o.path, moveTo); err != nil {
			return []string{"M " + o.path}, err
		}
		return []string{fmt.Sprintf("R %s -> %s", o.path, moveTo)}, nil
	}
	return []string{"M " + o.path}, nil
}

// update applies o's hunks to state and returns the resulting state without
// touching fm.
func (o updateFileOp) update(state patchFileState) (patchFileState, error) {
	origLines := state.lines

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
			return patchFileState{}, &PatchMismatchError{
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
	return patchFileState{lines: out, finalNL: state.finalNL}, nil
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

// PatchFileState reports whether the target file changed since the session's
// last read or write.
type PatchFileState int

const (
	PatchFileStateUnknown PatchFileState = iota
	PatchFileStateUnchanged
	PatchFileStateChanged
)

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
	// FileState records whether the file changed since the session's last read or write.
	FileState PatchFileState

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
		switch d.FileState {
		case PatchFileStateUnchanged:
			fmt.Fprintf(&b, "\n\nthe file has not changed since your last read or write; inspect the mismatch and snippet above, then rebuild the patch")
		case PatchFileStateChanged:
			fmt.Fprintf(&b, "\n\nthe file has changed since your last read or write; re-read the target region with read_file, then rebuild the patch")
		default:
			fmt.Fprintf(&b, "\n\nre-read the target region with read_file, then rebuild the patch")
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
