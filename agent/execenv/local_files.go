package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/tool/repair"
)

// fileTargetMode says which of fileTarget's plain-arm checks an operation
// takes. The sandboxed and granted-scratch arms are the same for every mode
// except that targetWrite also refuses a relative path under a dead sandbox
// root. The modes exist because the arms differ on purpose; file_op_arm_parity_test.go
// pins each operation's outcome on each arm.
type fileTargetMode int

const (
	// targetRead resolves the path as given. A read never checks that its
	// anchor is live, so a vanished root surfaces as the missing file.
	targetRead fileTargetMode = iota
	// targetReadRaw reads through the write path on the plain arm, so an empty
	// path is refused there.
	targetReadRaw
	// targetWrite requires a live anchor (the sandbox root, or the working
	// directory on the plain arm) and resolves through the write path.
	targetWrite
)

// fileTarget is the layer chosen for one path and the absolute path to act on.
// sfs is the sandboxed layer, the granted session-scratch layer, or nil for the
// plain filesystem. The caller must release it.
type fileTarget struct {
	sfs *sandboxFS
	abs string
}

func (t fileTarget) release() { t.sfs.release() }

// fileTarget picks the arm for path: the sandboxed layer when the file tools
// are confined, else the granted scratch layer when path lies in it, else the
// plain filesystem.
func (e *LocalExecutionEnvironment) fileTarget(tool, path string, mode fileTargetMode) (fileTarget, error) {
	if sfs := e.sandbox(); sfs != nil {
		if mode == targetWrite {
			if err := e.requireLiveSandboxRootForRelative(sfs, tool, path); err != nil {
				sfs.release()
				return fileTarget{}, err
			}
		}
		return fileTarget{sfs: sfs, abs: e.resolve(path)}, nil
	}
	abs := e.resolve(path)
	if sfs := e.scratchSandboxFor(abs); sfs != nil {
		return fileTarget{sfs: sfs, abs: abs}, nil
	}
	switch mode {
	case targetReadRaw:
		abs, err := e.resolveWrite(path)
		return fileTarget{abs: abs}, err
	case targetWrite:
		if err := e.requireLiveWorkingDirForRelative(path); err != nil {
			return fileTarget{}, err
		}
		abs, err := e.resolveWrite(path)
		return fileTarget{abs: abs}, err
	}
	return fileTarget{abs: abs}, nil
}

// ReadFile reads the file at path (resolved relative to RootDir) and returns
// its contents. Image and PDF files are returned as base64-encoded data with a
// descriptive header. Files containing a NUL byte are rejected as binary. For
// text files, lines are returned numbered, starting at offsetLine (default 1)
// and limited to limitLines lines (default 2000); CRLF line endings are
// normalized to LF.
func (e *LocalExecutionEnvironment) ReadFile(path string, offsetLine *int, limitLines *int) (string, error) {
	return e.ReadFileCtx(context.Background(), path, offsetLine, limitLines)
}

// ReadFileCtx is ReadFile with the caller's context threaded to the read, so a
// cancelled call ends a read parked on a stream instead of waiting it out.
// A call naming a positive offset or limit reads only the bytes the window
// needs; see read_window.go.
func (e *LocalExecutionEnvironment) ReadFileCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, error) {
	if lineWindowRequested(offsetLine, limitLines) {
		return e.readFileWindowedCtx(ctx, path, offsetLine, limitLines)
	}
	out, _, err := e.ReadFileAndBytesCtx(ctx, path, nil, nil)
	if err != nil {
		return "", readFileLimitHint(err)
	}
	return out, nil
}

// errReadFileCapHint is the "and you can still read a window" note read_file
// adds past the whole-file cap. It is a sentinel so a test can assert the hint
// was added without matching the wording.
var errReadFileCapHint = errors.New("read_file can also read a window of it directly: pass offset and limit (for example offset=1, limit=2000)")

// readFileLimitHint adds read_file's own way past the whole-file read cap to the
// cap's refusal: the shared ReadLimitError text predates the offset/limit
// window and only points at shell tools, so a model that follows it never finds
// the tool capability this change adds. edit_file and apply_patch share
// ReadLimitError and keep the shared text, so the hint belongs here, at the
// read_file entry, rather than in the error type.
func readFileLimitHint(err error) error {
	rle, capped := errors.AsType[*ReadLimitError](err)
	if !capped {
		return err
	}
	return fmt.Errorf("%w; %w", rle, errReadFileCapHint)
}

// ReadFileAndBytesCtx is ReadFileCtx that also returns the whole file's bytes
// as this read loaded them, for a caller that must record exactly what the
// read saw. The bytes are the complete file even when offset and limit select
// only some of its lines — the caller needs the file, not the window — so this
// entry always loads it; ReadFileCtx, which does not need them, reads the
// window alone. On error the bytes are nil.
func (e *LocalExecutionEnvironment) ReadFileAndBytesCtx(ctx context.Context, path string, offsetLine *int, limitLines *int) (string, []byte, error) {
	t, err := e.fileTarget("read_file", path, targetRead)
	if err != nil {
		return "", nil, err
	}
	defer t.release()
	sfs, abs := t.sfs, t.abs
	var b []byte
	if sfs != nil {
		// Sandboxed or explicitly granted scratch: race-safe fd read
		// (symlink-refusing, root/denylist-checked).
		// The image/PDF/binary/line-numbering contract below is applied identically
		// to the returned bytes, so the output is unchanged from the off path.
		b, err = sfs.readFileCtx(ctx, "read_file", abs)
	} else {
		b, err = readFileCappedCtx(ctx, e.filesystem(), abs)
	}
	if err != nil {
		return "", nil, e.mapReadFileErr(sfs, abs, err)
	}
	out, rerr := renderTextRead(path, b, offsetLine, limitLines)
	if rerr != nil {
		return "", nil, rerr
	}
	return out, b, nil
}

// NumberLinesBytes is NumberLines over the file's raw bytes. It exists so a
// caller that already holds the bytes does not pay a whole-file string copy to
// display a small window: the read_file path returns a 20-line window while
// holding an 8 MiB file, and converting the file to a string first copies every
// byte of it. The output is byte-identical to NumberLines(string(text), ...).
func NumberLinesBytes(text []byte, offsetLine, limitLines *int) string {
	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := DefaultReadLines
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	// A trailing newline terminates the last line rather than starting another,
	// so it is not a row of its own. Trimming it first is what makes an offset
	// past a one-line file report "" instead of an empty extra line. A CRLF
	// terminator is removed as a unit, so the CR of a final empty CRLF line
	// does not survive as a stray byte.
	if bytes.HasSuffix(text, []byte("\r\n")) {
		text = text[:len(text)-2]
	} else {
		text = bytes.TrimSuffix(text, []byte{'\n'})
	}
	offset := 0
	for i := 1; i < start; i++ {
		idx := bytes.IndexByte(text[offset:], '\n')
		if idx < 0 {
			return ""
		}
		offset += idx + 1
	}
	window := text[offset:]
	var out strings.Builder
	remaining := limit
	lineNumber := start
	consumed := 0
	for remaining > 0 {
		idx := bytes.IndexByte(window[consumed:], '\n')
		var line []byte
		if idx < 0 {
			line = window[consumed:]
			consumed = len(window)
		} else {
			line = window[consumed : consumed+idx]
			consumed += idx + 1
		}
		// CRLF normalizes to LF per emitted line, so only the window is
		// rewritten and the rest of the file is never touched. Only a line a
		// newline terminated can end in the CR of a CRLF pair; a CR at the end
		// of the final unterminated line is file content and stays.
		if idx >= 0 {
			line = bytes.TrimSuffix(line, []byte{'\r'})
		}
		fmt.Fprintf(&out, "%4d\t%s\n", lineNumber, line)
		lineNumber++
		remaining--
		if idx < 0 {
			break
		}
	}
	return out.String()
}

// eachLine visits the '\n'-separated segments of text in order — including
// the final (possibly empty) segment after a trailing newline, exactly the
// segments strings.Split(text, "\n") yields — without materializing them all:
// a line-heavy file at the whole-file read cap would otherwise hold ~16 bytes
// of slice header per line at once (issue #4556). The walk stops once visit
// returns true.
func eachLine(text string, visit func(line string) bool) {
	for line := range strings.SplitSeq(text, "\n") {
		if visit(line) {
			return
		}
	}
}

// DefaultReadLines is the line count a read returns when the caller gives no
// positive limit.
const DefaultReadLines = 2000

// NumberLines presents text as ReadFile presents a text file: CRLF
// normalized to LF, lines numbered from offsetLine (default 1), at most
// limitLines lines (default 2000), and "" past the end. A trailing newline
// terminates the last line rather than starting another: a file ending in
// "\n" has exactly as many rows as newline characters, matching the line
// count patch context works with, so a row copied from the display cannot
// run past the end of the file (#6202). The window is extracted by scanning
// segments rather than splitting the file, so a line-heavy file at the
// whole-file read cap does not allocate per line for every line in it
// (issue #4556).
func NumberLines(text string, offsetLine, limitLines *int) string {
	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := DefaultReadLines
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	// A trailing newline terminates the last line rather than starting another,
	// so it is not a row of its own. Trimming it first is what makes an offset
	// past a one-line file report "" instead of an empty extra line. A CRLF
	// terminator is removed as a unit, so the CR of a final empty CRLF line
	// does not survive as a stray byte.
	if strings.HasSuffix(text, "\r\n") {
		text = text[:len(text)-2]
	} else {
		text = strings.TrimSuffix(text, "\n")
	}
	// Skip to the window's first line by scanning newlines in the original
	// text. Running out of newlines first means start is past the final
	// segment. The scan does not normalize or copy, so skipping a long prefix
	// costs only the newline scan, not a rewrite of every byte before it.
	offset := 0
	for i := 1; i < start; i++ {
		idx := strings.IndexByte(text[offset:], '\n')
		if idx < 0 {
			return ""
		}
		offset += idx + 1
	}
	windowText := text[offset:]
	var out strings.Builder
	remaining := limit
	lineNumber := start
	consumed := 0
	for remaining > 0 {
		idx := strings.IndexByte(windowText[consumed:], '\n')
		var line string
		if idx < 0 {
			line = windowText[consumed:]
			consumed = len(windowText)
		} else {
			line = windowText[consumed : consumed+idx]
			consumed += idx + 1
		}
		// CRLF normalizes to LF per line, so only the emitted window is
		// rewritten; the rest of the file is never touched. Only a line a
		// newline terminated can end in the CR of a CRLF pair; a CR at the end
		// of the final unterminated line is file content and stays.
		if idx >= 0 {
			line = strings.TrimSuffix(line, "\r")
		}
		fmt.Fprintf(&out, "%4d\t%s\n", lineNumber, line)
		lineNumber++
		remaining--
		if idx < 0 {
			break
		}
	}
	return out.String()
}

// readFileNotFoundSuggestion renders a "did you mean" hint for a read_file
// ENOENT on absPath, or "" if it can't find anything worth suggesting.
//
// It walks up absPath's ancestors (starting at its own parent) until it finds
// one that exists as a directory, then fuzzy-matches absPath's basename
// against that directory's direct children. This single algorithm covers two
// distinct misses with the same code path: an ordinary typo in a file that
// lives right next to where the model looked (the walk terminates
// immediately, since the parent already exists), and a "doubled path
// segment" miss like ".../worktrees/x/worktrees/x/file.go" where the real
// file is at ".../worktrees/x/file.go" (the walk climbs past the fictional
// nested worktrees/x before landing on a real directory whose listing
// contains an exact match).
func readFileNotFoundSuggestion(absPath string) string {
	dir := filepath.Dir(absPath)
	for {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // walked to the filesystem root without finding a real directory
		}
		dir = parent
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	matches := repair.TopMatches(filepath.Base(absPath), names, 3)
	if len(matches) == 0 {
		return ""
	}
	full := make([]string, len(matches))
	for i, m := range matches {
		full[i] = filepath.Join(dir, m)
	}
	if len(full) == 1 {
		return fmt.Sprintf("Did you mean %s?", full[0])
	}
	return fmt.Sprintf("Did you mean one of: %s?", strings.Join(full, ", "))
}

// WriteFile writes content to the file at path, creating any missing parent
// directories within the active sandbox policy. It returns a
// human-readable summary of the bytes written.
func (e *LocalExecutionEnvironment) WriteFile(path string, content string) (string, error) {
	if err := e.writeFileAs("write_file", path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
}

// MaxEditFileSpans caps total matched spans across all entries and match paths
// in a single EditFileBatchWithCtx call. A variable so tests can exercise the
// refusal with a fixture-sized cap.
var MaxEditFileSpans = 100_000

// MaxEditFileOutputBytes caps the spliced output byte size of
// EditFileBatchWithCtx, checked before the output buffer is allocated. A
// variable so tests can exercise the refusal with a fixture-sized cap.
var MaxEditFileOutputBytes int64 = MaxWholeFileReadBytes

// EditFile replaces occurrences of oldString with newString in the file at
// path, subject to the active sandbox policy. If oldString is not
// found exactly, a whitespace-normalized fuzzy match is attempted. Unless
// replaceAll is true, oldString must match exactly once. It returns a summary
// of the number of replacements made.
func (e *LocalExecutionEnvironment) EditFile(path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(context.Background(), path, oldString, newString, replaceAll, nil)
}

// EditFileCtx is EditFile with the caller's context threaded to its read, so a
// cancelled edit ends a read parked on a stream. It is the interface-visible
// entry of ContextualFileReader.
func (e *LocalExecutionEnvironment) EditFileCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool) (string, error) {
	return e.EditFileWithCtx(ctx, path, oldString, newString, replaceAll, nil)
}

// EditFileWithCtx is EditFileCtx, except that finish, when not nil, turns the
// edited bytes into the bytes written, inside the same read and write.
func (e *LocalExecutionEnvironment) EditFileWithCtx(ctx context.Context, path string, oldString string, newString string, replaceAll bool, finish func(edited []byte) []byte) (string, error) {
	return e.EditFileBatchWithCtx(ctx, path, []FileEdit{{
		OldString:  oldString,
		NewString:  newString,
		ReplaceAll: replaceAll,
	}}, finish)
}

// EditFileBatchCtx applies multiple non-overlapping replacements to path
// atomically in a single read and write.
func (e *LocalExecutionEnvironment) EditFileBatchCtx(ctx context.Context, path string, edits []FileEdit) (string, error) {
	return e.EditFileBatchWithCtx(ctx, path, edits, nil)
}

type preparedEdit struct {
	oldLF string
	newLF string
}

type editSpan struct {
	start     int
	end       int
	editIndex int
}

// EditFileBatchWithCtx is EditFileBatchCtx, except that finish, when not nil,
// turns the edited bytes into the bytes written inside the same read and write.
func (e *LocalExecutionEnvironment) EditFileBatchWithCtx(ctx context.Context, path string, edits []FileEdit, finish func(edited []byte) []byte) (string, error) {
	t, err := e.fileTarget("edit_file", path, targetWrite)
	if err != nil {
		return "", err
	}
	defer t.release()
	if t.sfs != nil {
		// Deny an edit in a non-writable location up front (read-only mode, outside
		// the writable roots, or a masked/git-protected surface) before reading, so
		// the model gets a clean write denial rather than a match/not-found result.
		if derr := t.sfs.checkWritable("edit_file", t.abs); derr != nil {
			return "", derr
		}
	}

	if len(edits) == 0 {
		return "", errors.New("edit_file: edits must not be empty")
	}
	editPrefix := func(i int) string {
		if len(edits) == 1 {
			return ""
		}
		return fmt.Sprintf("edits[%d]: ", i)
	}
	var inputErrs []error
	for i, ed := range edits {
		if ed.OldString == "" {
			inputErrs = append(inputErrs, fmt.Errorf("%sold_string must not be empty", editPrefix(i)))
		}
	}
	if len(inputErrs) > 0 {
		return "", errors.Join(inputErrs...)
	}

	var origBytes []byte
	if t.sfs != nil {
		origBytes, err = t.sfs.readFileCtx(ctx, "edit_file", t.abs)
	} else {
		origBytes, err = readFileCappedCtx(ctx, e.filesystem(), t.abs)
	}
	if err != nil {
		return "", err
	}

	bom := ""
	if bytes.HasPrefix(origBytes, []byte{0xEF, 0xBB, 0xBF}) {
		bom = "\xEF\xBB\xBF"
	}
	raw := string(origBytes[len(bom):])
	// Record each newline's original terminator so untouched lines keep theirs
	// even when the file mixes CRLF and LF. crlfLines[i] describes the i-th
	// newline of lfText, in file order.
	crlfLines := make([]bool, 0, strings.Count(raw, "\n"))
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			crlfLines = append(crlfLines, i > 0 && raw[i-1] == '\r')
		}
	}
	// Replacements follow the file's first line ending, as they always have.
	useCRLF := len(crlfLines) > 0 && crlfLines[0]
	lfText := strings.ReplaceAll(raw, "\r\n", "\n")

	prepared := make([]preparedEdit, len(edits))
	for i, ed := range edits {
		oldLF := strings.ReplaceAll(ed.OldString, "\r\n", "\n")
		newLF := strings.ReplaceAll(ed.NewString, "\r\n", "\n")
		if bom != "" {
			if trimmed, ok := strings.CutPrefix(oldLF, bom); ok && trimmed != "" {
				oldLF = trimmed
				newLF = strings.TrimPrefix(newLF, bom)
			}
		}
		prepared[i] = preparedEdit{
			oldLF: oldLF,
			newLF: newLF,
		}
	}

	var (
		spans        []editSpan
		editErrs     []error
		usedFuzzy    bool
		nearestCount int
	)

matchLoop:
	for i, ed := range edits {
		pe := prepared[i]
		idx := strings.Index(lfText, pe.oldLF)
		if idx >= 0 {
			if !ed.ReplaceAll {
				if second := strings.Index(lfText[idx+1:], pe.oldLF); second >= 0 {
					lines := exactAmbiguousLines(lfText, pe.oldLF, idx, second)
					editErrs = append(editErrs, fmt.Errorf("%sold_string not unique in %s (matches at lines %s); use replace_all=true or provide a more specific old_string",
						editPrefix(i), path, formatLineNumbers(lines)))
					continue
				}
				if len(spans)+1 > MaxEditFileSpans {
					editErrs = append(editErrs, fmt.Errorf("%smatched spans exceed the %d-span edit limit", editPrefix(i), MaxEditFileSpans))
					break matchLoop
				}
				spans = append(spans, editSpan{
					start:     idx,
					end:       idx + len(pe.oldLF),
					editIndex: i,
				})
				continue
			}
			pos := idx
			for {
				if len(spans)+1 > MaxEditFileSpans {
					editErrs = append(editErrs, fmt.Errorf("%smatched spans exceed the %d-span edit limit", editPrefix(i), MaxEditFileSpans))
					break matchLoop
				}
				spans = append(spans, editSpan{
					start:     pos,
					end:       pos + len(pe.oldLF),
					editIndex: i,
				})
				nextStart := pos + len(pe.oldLF)
				next := strings.Index(lfText[nextStart:], pe.oldLF)
				if next < 0 {
					break
				}
				pos = nextStart + next
			}
			continue
		}

		remaining := max(MaxEditFileSpans-len(spans), 0)
		fuzzySpans, ambigLines, limitExceeded := findFuzzyMatchSpans(lfText, pe.oldLF, ed.ReplaceAll, remaining)
		if limitExceeded {
			editErrs = append(editErrs, fmt.Errorf("%smatched spans exceed the %d-span edit limit", editPrefix(i), MaxEditFileSpans))
			break matchLoop
		}
		if len(ambigLines) > 0 {
			editErrs = append(editErrs, fmt.Errorf("%sold_string not unique in %s (ambiguous whitespace-normalized matches at lines %s); use replace_all=true or provide more context",
				editPrefix(i), path, formatLineNumbers(ambigLines)))
			continue
		}
		if len(fuzzySpans) == 0 {
			var near string
			if nearestCount < 3 {
				near = nearestFileRegion(lfText, pe.oldLF)
				if near != "" {
					nearestCount++
				}
			}
			if near != "" {
				editErrs = append(editErrs, fmt.Errorf("%sold_string not found in %s (exact and whitespace-normalized matching found no match)\nnearest text in the file (copy it exactly — your old_string may be a partial line or omit a line):\n%s",
					editPrefix(i), path, near))
			} else {
				editErrs = append(editErrs, fmt.Errorf("%sold_string not found in %s (exact and whitespace-normalized matching found no match)",
					editPrefix(i), path))
			}
			continue
		}
		if !ed.ReplaceAll {
			sp := fuzzySpans[0]
			matched := lfText[sp.start:sp.end]
			prevIdx := strings.Index(lfText[:sp.start], matched)
			nextIdx := strings.Index(lfText[sp.end:], matched)
			if prevIdx >= 0 || nextIdx >= 0 {
				var lines []int
				if prevIdx >= 0 {
					lines = append(lines, 1+strings.Count(lfText[:prevIdx], "\n"))
				}
				lines = append(lines, 1+strings.Count(lfText[:sp.start], "\n"))
				if nextIdx >= 0 {
					lines = append(lines, 1+strings.Count(lfText[:sp.end+nextIdx], "\n"))
				}
				editErrs = append(editErrs, fmt.Errorf("%sold_string not unique in %s (fuzzy match also appears as a substring at lines %s); provide more context",
					editPrefix(i), path, formatLineNumbers(lines)))
				continue
			}
		}
		usedFuzzy = true
		for _, fs := range fuzzySpans {
			spans = append(spans, editSpan{
				start:     fs.start,
				end:       fs.end,
				editIndex: i,
			})
		}
	}

	if len(editErrs) > 0 {
		return "", errors.Join(editErrs...)
	}

	slices.SortFunc(spans, func(a, b editSpan) int {
		if a.start != b.start {
			return a.start - b.start
		}
		if a.end != b.end {
			return a.end - b.end
		}
		return a.editIndex - b.editIndex
	})

	for k := 1; k < len(spans); k++ {
		if spans[k-1].end > spans[k].start {
			return "", fmt.Errorf("edits[%d] and edits[%d] match overlapping text; merge overlapping or nearby edits into a single edit",
				spans[k-1].editIndex, spans[k].editIndex)
		}
	}

	// Compute the exact restored length before allocating the output, so an
	// edit that only exceeds the cap once CRLF terminators are restored is
	// rejected without building it. Untouched text keeps each newline's
	// original terminator; replacement text gets the file's first ending.
	restoredLen := int64(len(bom)) + int64(len(lfText))
	nlIdx := 0
	pos := 0
	for _, sp := range spans {
		rep := prepared[sp.editIndex].newLF
		if bom != "" && sp.start == 0 {
			rep = strings.TrimPrefix(rep, bom)
		}
		restoredLen += int64(len(rep)) - int64(sp.end-sp.start)
		for n := strings.Count(lfText[pos:sp.start], "\n"); n > 0; n-- {
			if crlfLines[nlIdx] {
				restoredLen++
			}
			nlIdx++
		}
		nlIdx += strings.Count(lfText[sp.start:sp.end], "\n")
		if useCRLF {
			restoredLen += int64(strings.Count(rep, "\n"))
		}
		pos = sp.end
	}
	for ; nlIdx < len(crlfLines); nlIdx++ {
		if crlfLines[nlIdx] {
			restoredLen++
		}
	}
	if restoredLen > MaxEditFileOutputBytes {
		return "", fmt.Errorf("edited output for %s would be %d bytes, exceeding the %d-byte edit limit", path, restoredLen, MaxEditFileOutputBytes)
	}

	var out strings.Builder
	out.Grow(int(restoredLen))
	cursor := 0
	nlIdx = 0
	// writeOrig emits untouched text with each newline's original terminator.
	writeOrig := func(seg string) {
		for {
			before, after, ok := strings.Cut(seg, "\n")
			if !ok {
				out.WriteString(seg)
				return
			}
			out.WriteString(before)
			if crlfLines[nlIdx] {
				out.WriteString("\r\n")
			} else {
				out.WriteByte('\n')
			}
			nlIdx++
			seg = after
		}
	}
	for _, sp := range spans {
		writeOrig(lfText[cursor:sp.start])
		rep := prepared[sp.editIndex].newLF
		if bom != "" && sp.start == 0 {
			rep = strings.TrimPrefix(rep, bom)
		}
		if useCRLF {
			rep = strings.ReplaceAll(rep, "\n", "\r\n")
		}
		out.WriteString(rep)
		nlIdx += strings.Count(lfText[sp.start:sp.end], "\n")
		cursor = sp.end
	}
	writeOrig(lfText[cursor:])

	restored := out.String()
	edited := append([]byte(bom), restored...)

	if bytes.Equal(edited, origBytes) {
		return "", fmt.Errorf("no changes made to %s: replacement produces identical file content", path)
	}

	if finish != nil {
		edited = finish(edited)
	}
	if t.sfs != nil {
		if werr := t.sfs.writeFile("edit_file", t.abs, edited, 0o644); werr != nil {
			return "", werr
		}
	} else if werr := afero.WriteFile(e.filesystem(), t.abs, edited, 0o644); werr != nil {
		return "", werr
	}

	n := len(spans)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	fuzzyNote := ""
	if usedFuzzy {
		fuzzyNote = " [NOTE: Matched with whitespace normalization]"
	}
	if len(edits) == 1 {
		return fmt.Sprintf("edited %s: %d replacement%s%s", path, n, plural, fuzzyNote), nil
	}
	return fmt.Sprintf("edited %s: %d replacement%s across %d edits%s", path, n, plural, len(edits), fuzzyNote), nil
}

func exactAmbiguousLines(lfText, oldLF string, firstIdx, secondRel int) []int {
	lines := make([]int, 0, maxAmbiguousLines)
	line := 1 + strings.Count(lfText[:firstIdx], "\n")
	lines = append(lines, line)
	pos := firstIdx + 1 + secondRel
	prev := firstIdx
	for len(lines) < maxAmbiguousLines {
		line += strings.Count(lfText[prev:pos], "\n")
		lines = append(lines, line)
		prev = pos
		if pos+1 >= len(lfText) {
			break
		}
		next := strings.Index(lfText[pos+1:], oldLF)
		if next < 0 {
			break
		}
		pos = pos + 1 + next
	}
	return lines
}

func formatLineNumbers(lines []int) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(ln))
	}
	return b.String()
}

// detectImageFormat checks file extension and magic bytes to identify image files.
// Returns the format name (e.g. "png", "jpeg") or "" if not an image.
func detectImageFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	imageExts := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".tif": "tiff", ".tiff": "tiff",
	}
	if format, ok := imageExts[ext]; ok {
		return format
	}
	// Check magic bytes for images without recognized extension.
	if len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}) {
		return "png"
	}
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}
	if len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a") {
		return "gif"
	}
	if len(data) >= 2 && string(data[:2]) == "BM" {
		return "bmp"
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "webp"
	}
	if len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*") {
		return "tiff"
	}
	return ""
}

// detectDocumentFormat checks file extension and magic bytes to identify document files
// that can be processed natively by the model (e.g. PDFs). Returns the format name or "".
func detectDocumentFormat(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".pdf" {
		return "pdf"
	}
	// Check magic bytes: PDF files start with %PDF-
	if len(data) >= 5 && string(data[:5]) == "%PDF-" {
		return "pdf"
	}
	return ""
}

// FileExists reports whether a file or directory exists at path (resolved
// relative to RootDir).
func (e *LocalExecutionEnvironment) FileExists(path string) bool {
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		return sfs.exists("file_exists", e.resolve(path))
	}
	_, err := os.Stat(e.resolve(path))
	return err == nil
}
