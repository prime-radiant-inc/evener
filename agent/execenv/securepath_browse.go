package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/evener/agent/searchresult"
)

// This file holds the platform-independent browse logic (the writability
// pre-check and the shared grep matching/formatting core) and builds on every
// platform. The fd-anchored glob and native-grep walks live in
// securepath_browse_fdops_unix.go (linux/darwin); securepath_other.go supplies
// fail-closed stand-ins elsewhere.

// checkWritable reports whether a write to abs would be permitted by the policy
// (writable-root membership, not masked, not a protected git surface) WITHOUT
// opening or creating anything. It is a textual pre-check used to deny an edit up
// front (e.g. in read-only mode) before reading the file; the authoritative,
// fd-based check still runs in writeFile, so this can only be more permissive and
// never wrongly allows a write.
func (s *sandboxFS) checkWritable(tool, abs string) error {
	abs = filepath.Clean(abs)
	if len(s.policy.FileTool.WriteRoots) == 0 {
		return s.deny(tool, abs, denyReasonWriteDenied)
	}
	if _, _, ok := containingRoot(s.policy.FileTool.WriteRoots, abs); !ok {
		return s.deny(tool, abs, denyReasonOutsideWrite)
	}
	if s.underMaskedForWrite(abs) {
		return s.deny(tool, abs, denyReasonMasked)
	}
	if s.underProtected(abs) {
		return s.deny(tool, abs, denyReasonProtected)
	}
	return nil
}

// grepAccum accumulates native-grep results across files. It is the shared core of
// the off-mode and sandboxed native grep so their output modes stay identical.
type grepAccum struct {
	re           *regexp.Regexp
	outputMode   string
	maxResults   int
	contextLines int
	results      []string
	// truncated records that the walk found a result past maxResults, so
	// finish adds grepTruncationNote as a note.
	truncated bool
	// retained is the byte size of the results held so far; emit stops at
	// grepRetainedBytes.
	retained int
	// retainedTruncated records that a result line crossed the
	// retained-byte budget, which emit leaves out with everything after it,
	// so finishNotes adds the byte-truncation note.
	retainedTruncated bool
	// overlongSkipped counts lines the named-file streaming path left out
	// because a line longer than grepMaxFileBytes cannot be searched without
	// holding it whole; the walk never streams, so it stays zero there.
	// finishNotes turns it into the incompleteness note.
	overlongSkipped int
}

// compileGrepPattern compiles a grep pattern as the native search reads it.
func compileGrepPattern(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	flags := ""
	if caseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}

// CheckGrepArgs reports the error a grep would give for its pattern or its
// glob filter's brace syntax before searching anything, so a caller that
// searches nothing can still refuse bad arguments.
func CheckGrepArgs(pattern, globFilter string, caseInsensitive bool) error {
	if _, err := expandGrepFilter(globFilter); err != nil {
		return err
	}
	_, err := compileGrepPattern(pattern, caseInsensitive)
	return err
}

// newGrepAccum compiles the pattern (with optional case-insensitivity) and
// initializes the accumulator; maxResults defaults to 100 when non-positive.
// contextLines (0-10, validated by the caller) adds that many lines of
// surrounding context around each match in "content"/"" output mode; it has no
// effect on "files_with_matches" or "count", which report per-file, not
// per-line.
func newGrepAccum(pattern string, caseInsensitive bool, maxResults int, outputMode string, contextLines int) (*grepAccum, error) {
	re, err := compileGrepPattern(pattern, caseInsensitive)
	if err != nil {
		return nil, err
	}
	if maxResults <= 0 {
		maxResults = DefaultGrepMaxResults
	}
	if contextLines < 0 {
		contextLines = 0
	}
	return &grepAccum{
		re:           re,
		outputMode:   outputMode,
		maxResults:   maxResults,
		contextLines: contextLines,
	}, nil
}

// feed scans one file's lines and records matches per output mode; it returns true
// when it found a result past maxResults, which it leaves out, and the walk
// should stop. Stopping at the result past the cap rather than at the cap
// itself tells a result cut short from one that held exactly maxResults.
//
// A relPath of "." means the search target was the file itself (the walk root),
// not a file found under a directory. Ripgrep omits the filename entirely when
// given a single explicit file argument, so content and count output do the
// same here; otherwise the tool's output would differ between environments with
// and without rg on PATH. A path is written as OneLinePath gives it. A
// binary file (one holding a NUL byte) is skipped.
func (a *grepAccum) feed(relPath string, data []byte) (stop bool) {
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	singleFile := relPath == "."
	name := OneLinePath(relPath)
	lines := fileLines(string(data))
	switch a.outputMode {
	case "files_with_matches":
		if !slices.ContainsFunc(lines, a.re.MatchString) {
			return false
		}
		return a.emit(name)
	case "count":
		n := 0
		for _, line := range lines {
			if a.re.MatchString(line) {
				n++
			}
		}
		if n == 0 {
			return false
		}
		// One row per file in walk order, capped like files_with_matches —
		// the same first-N truncation the ripgrep path applies to rg
		// --count. A named file's row is the bare count, as rg prints it.
		if singleFile {
			return a.emit(strconv.Itoa(n))
		}
		return a.emit(name + ":" + strconv.Itoa(n))
	default:
		return a.feedContent(name, singleFile, lines)
	}
}

// fileLines is a file's lines as rg counts them: none in an empty file, and
// no empty line after a final newline, which ends the last line rather than
// starting another.
func fileLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// feedContent records a file's matching lines, and with contextLines the
// lines around each, the way rg -C prints them: windows that overlap or touch
// join into one group, a "--" goes between groups (across files too), and a
// match line takes ":" where a context line takes "-". The cap counts output
// lines, separators and context included, as the ripgrep arm's cap does and
// the grep tool's max_results promises for content.
func (a *grepAccum) feedContent(name string, singleFile bool, lines []string) (stop bool) {
	// last is the last line written from this file; afterEnd is the last line
	// the latest match's after-context reaches.
	last, afterEnd := -1, -1
	// context writes lines lo through hi as context, up to the last line.
	context := func(lo, hi int) (stop bool) {
		for k := lo; k <= min(hi, len(lines)-1); k++ {
			if a.emitLine(name, singleFile, k, "-", lines[k]) {
				return true
			}
			last = k
		}
		return false
	}
	for i, line := range lines {
		if !a.re.MatchString(line) {
			continue
		}
		if context(last+1, min(i-1, afterEnd)) {
			return true
		}
		start := max(last+1, i-a.contextLines)
		if a.contextLines > 0 && len(a.results) > 0 && (last < 0 || start > last+1) && a.emit("--") {
			return true
		}
		if context(start, i-1) || a.emitLine(name, singleFile, i, ":", line) {
			return true
		}
		last, afterEnd = i, i+a.contextLines
	}
	return context(last+1, afterEnd)
}

// emitLine writes line index k of a file as a content line: "12:text" for a
// named file, else "path:12:text", with sep in place of ":" on a context line.
func (a *grepAccum) emitLine(name string, singleFile bool, k int, sep, text string) (stop bool) {
	if singleFile {
		return a.emit(fmt.Sprintf("%d%s%s", k+1, sep, text))
	}
	return a.emit(fmt.Sprintf("%s%s%d%s%s", name, sep, k+1, sep, text))
}

// emit adds one content output line, or reports the cap cut it off.
func (a *grepAccum) emit(line string) (stop bool) {
	if len(a.results) >= a.maxResults {
		return a.cutAtCap()
	}
	if a.retained+len(line) > grepRetainedBytes {
		return a.cutAtRetainedCap()
	}
	a.results = append(a.results, line)
	a.retained += len(line)
	return false
}

// cutAtCap records that feed found a result past maxResults, which it leaves
// out, and returns feed's stop.
func (a *grepAccum) cutAtCap() (stop bool) {
	a.truncated = true
	return true
}

// cutAtRetainedCap records that a result line crossed grepRetainedBytes,
// which emit leaves out with everything after it, and returns feed's stop.
func (a *grepAccum) cutAtRetainedCap() (stop bool) {
	a.retainedTruncated = true
	return true
}

// grepStream applies the whole-file feed's rules to one file as its lines
// arrive in bounded windows, so a file larger than grepMaxFileBytes can be
// searched without ever holding it whole. The output order is the feed's:
// a match's before-context is known at the match, and its after-context
// lines are emitted the moment each arrives — the same lines, in the same
// order, the whole-file feed prints in its deferred blocks.
type grepStream struct {
	a          *grepAccum
	name       string
	singleFile bool
	// recent holds the last contextLines non-matching lines, for a match's
	// before-context.
	recent []streamLine
	// last is the last line written from this file, afterEnd the last line
	// the latest match's after-context reaches; both -1 when none yet.
	last, afterEnd int
	// count is the matches seen so far (count mode).
	count int
	// done marks a files_with_matches row already emitted; no later line
	// of this file can add output.
	done bool
}

type streamLine struct {
	idx  int
	text string
}

// line applies the feed's rules to line index i (0-based); stop is the
// feed's: the results cap cut the output off.
func (s *grepStream) line(i int, text string) (stop bool) {
	a := s.a
	switch a.outputMode {
	case "files_with_matches":
		if s.done || !a.re.MatchString(text) {
			return false
		}
		s.done = true
		return a.emit(s.name)
	case "count":
		if a.re.MatchString(text) {
			s.count++
		}
		return false
	}
	if !a.re.MatchString(text) {
		if i > s.afterEnd {
			s.remember(i, text)
			return false
		}
		// Inside the latest match's after-context: emit the context line
		// the moment it arrives, as the whole-file feed emits it in its
		// next block or the trailing one.
		if a.emitLine(s.name, s.singleFile, i, "-", text) {
			return true
		}
		s.last = i
		return false
	}
	start := max(s.last+1, i-a.contextLines)
	if a.contextLines > 0 && len(a.results) > 0 && (s.last < 0 || start > s.last+1) && a.emit("--") {
		return true
	}
	for _, held := range s.recent {
		if held.idx < start || held.idx > i-1 {
			continue
		}
		if a.emitLine(s.name, s.singleFile, held.idx, "-", held.text) {
			return true
		}
		s.last = held.idx
	}
	if a.emitLine(s.name, s.singleFile, i, ":", text) {
		return true
	}
	s.last, s.afterEnd = i, i+a.contextLines
	return false
}

// remember holds a non-matching line for a later match's before-context.
func (s *grepStream) remember(i int, text string) {
	if s.a.contextLines <= 0 {
		return
	}
	share := grepRetainedBytes / (s.a.contextLines + 1)
	s.recent = append(s.recent, streamLine{idx: i, text: truncateContextLine(text, share)})
	if len(s.recent) > s.a.contextLines {
		s.recent = s.recent[len(s.recent)-s.a.contextLines:]
	}
}

// truncateContextLine keeps the tail of a before-context line longer than
// keep — the tail is the end adjacent to the match the context orients —
// headed by a marker disclosing how much was left out, so the cut shows in
// the output itself. The share a caller passes keeps the whole ring within
// the retained budget while the match it exists to orient keeps room of its
// own.
func truncateContextLine(text string, keep int) string {
	const format = "[... %d bytes omitted ...]"
	if len(text) <= keep {
		return text
	}
	marker := fmt.Sprintf(format, len(text)-keep+len(fmt.Sprintf(format, 0)))
	if len(marker) >= keep {
		// No room for any of the line: the marker alone, disclosing all of it.
		return fmt.Sprintf(format, len(text))
	}
	return marker + text[len(text)-keep+len(marker):]
}

// eof finishes the file: count mode emits its one row, exactly as the
// whole-file feed does after scanning the last line.
func (s *grepStream) eof() (stop bool) {
	if s.a.outputMode == "count" && s.count > 0 {
		if s.singleFile {
			return s.a.emit(strconv.Itoa(s.count))
		}
		return s.a.emit(s.name + ":" + strconv.Itoa(s.count))
	}
	return false
}

// grepFileSelected reports whether grep searches the file named name, at
// slash path rel, once dotfile and ignore rules have let it through: a file
// skip names (when skip is not nil) or outside the glob filters is left out.
// The error is a malformed glob filter's.
func grepFileSelected(name, rel string, globFilters []string, skip func(rel string) bool) (bool, error) {
	if skip != nil && skip(rel) {
		return false, nil
	}
	if len(globFilters) == 0 {
		return true, nil
	}
	return matchesAnyGrepFilter(name, globFilters)
}

// finish renders the accumulated results in the requested output mode, then
// grepTruncationNote as a note (searchresult.WithNotes) when the cap left
// results out.
func (a *grepAccum) finish() string {
	out, notes := a.finishNotes()
	if len(notes) > 0 {
		return searchresult.WithNotes(out, notes...)
	}
	return out
}

// finishNotes renders the accumulated results and the notes the search owes
// about them: the truncation notes when a cap — results or retained bytes —
// left some out.
func (a *grepAccum) finishNotes() (out string, notes []string) {
	out = strings.Join(a.results, "\n")
	if a.truncated {
		notes = append(notes, grepTruncationNote(a.maxResults))
	}
	if a.retainedTruncated {
		notes = append(notes, grepRetainedTruncationNote())
	}
	if a.overlongSkipped > 0 {
		notes = append(notes, grepOverlongNote(a.overlongSkipped))
	}
	return out, notes
}

// feedLargeFile searches the over-cap regular file behind r in windows of
// grepStreamWindowBytes, applying the feed's rules line by line, so a file
// named as the search target — a deliberate request the capped read refused
// (#4574 round 4, MEDIUM) — still yields its matches without any read
// holding it whole. f is rewound first: the capped read leaves it partway
// in. The scan ends at the first window holding a NUL byte, the content the
// whole-file feed treats as binary, with matches from earlier windows
// standing; a single line longer than grepMaxFileBytes is skipped — its
// number spent, the scan resuming after it, its length alone deciding,
// never where a window happens to fall, and the omission disclosed in the
// finish note — rather than held whole. A
// cancelled context — checked between windows, because an ordinary regular
// file takes no read deadline and a growing one can outlast any scan — or
// a stalled read ends the scan. stop is the feed's: the results cap cut the
// output off.
func (a *grepAccum) feedLargeFile(ctx context.Context, r io.ReadSeeker) (stop bool, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if f, ok := r.(*os.File); ok {
		disarm := armStreamDeadline(ctx, f)
		defer disarm()
	}
	// A named target's feed name is "." (see feed); the walk never streams.
	s := &grepStream{a: a, name: ".", singleFile: true, last: -1, afterEnd: -1}
	buf := make([]byte, grepStreamWindowBytes)
	var pending []byte
	i, skipping, binary := 0, false, false
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			if bytes.IndexByte(buf[:n], 0) >= 0 {
				binary = true
				break
			}
			chunk := buf[:n]
			if skipping {
				end := bytes.IndexByte(chunk, '\n')
				if end < 0 {
					chunk = nil
				} else {
					skipping = false
					i++
					chunk = chunk[end+1:]
				}
			}
			if len(chunk) > 0 {
				pending = append(pending, chunk...)
				for {
					sep := bytes.IndexByte(pending, '\n')
					if sep < 0 {
						break
					}
					line := pending[:sep]
					pending = pending[sep+1:]
					// A completed line is admitted by its own length — the
					// same boundary the unfinished tail below applies — so
					// window alignment can never decide admission.
					if len(line) > grepMaxFileBytes {
						s.a.overlongSkipped++
						i++
						continue
					}
					if s.line(i, string(line)) {
						return true, nil
					}
					i++
				}
				if len(pending) > grepMaxFileBytes {
					// A line as long as the whole per-file read budget
					// cannot be searched without holding it whole.
					s.a.overlongSkipped++
					skipping = true
					pending = nil
				}
			}
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				if cerr := ctx.Err(); cerr != nil {
					return false, cerr
				}
				if errors.Is(rerr, os.ErrDeadlineExceeded) {
					return false, errGrepStreamStalled
				}
				return false, rerr
			}
			break
		}
		if s.done {
			break
		}
	}
	if !binary && !skipping && len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if s.line(i, string(pending)) {
			return true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.eof(), nil
}

// finishWalk is finish for a walk that left paths out: excludedByIgnore
// dotfile/gitignored ones, oversized ones past the read cap. Silent-empty is
// the enemy (D2): when the walk found nothing, a note tells "no matches
// among the files searched, but N were skipped by the default dotfile/
// gitignore exclusion" from "genuinely no matches" — grep has no
// include_ignored knob, so that one is informational rather than a
// suggestion to retry — while over-cap files are worth knowing about
// however many matches came back, and each one can still be searched by
// naming it directly.
func (a *grepAccum) finishWalk(excludedByIgnore, oversized int) string {
	out, notes := a.finishNotes()
	if oversized > 0 {
		notes = append(notes, grepOversizeNote(oversized))
	}
	if out == "" && excludedByIgnore > 0 {
		notes = append(notes, fmt.Sprintf("0 matches; %d dotfile/gitignored path(s) were excluded from the search", excludedByIgnore))
	}
	if len(notes) > 0 {
		return searchresult.WithNotes(out, notes...)
	}
	return out
}

// grepTruncationNote is the note on a grep result the cap cut short, so a
// model never reads the results it got as all there are.
func grepTruncationNote(maxResults int) string {
	return fmt.Sprintf("[results truncated at %d; narrow the path or glob_filter, or raise max_results]", maxResults)
}

// grepRetainedTruncationNote is the note on a search whose retained output
// crossed the byte budget: the results it returned are not all there are,
// and the ones missing were cut for their size, not for lack of matches.
func grepRetainedTruncationNote() string {
	return fmt.Sprintf("[results truncated at the %d-byte retained-output cap; later matches omitted]", grepRetainedBytes)
}

// grepOversizeNote is the note on a walk that left files larger than the
// read cap out of the search: the matches it returned are not all there
// are, and the way to search one of the files it skipped is to name it.
func grepOversizeNote(n int) string {
	return fmt.Sprintf("%d file(s) larger than the %d-byte read cap were left out; name one directly to search it", n, grepMaxFileBytes)
}

// grepOverlongNote is the note on a named-file search that left lines longer
// than the read cap out: the model must not read the matches it got — or an
// empty — as all the file held.
func grepOverlongNote(n int) string {
	return fmt.Sprintf("%d line(s) longer than the %d-byte read cap were left out; no line that long can be searched without holding it whole", n, grepMaxFileBytes)
}

// pseudoFsRoots are the pseudo-filesystem mount points a file-tree walk must
// not descend into from outside: their contents are runtime state — process
// tables, kernel parameters, device streams — rather than files to search,
// and some are effectively unbounded (/proc/kcore presents as a regular file
// of ~128TB, so a search of / that reaches it reads without limit, #4480). A
// search explicitly rooted inside one still searches it, the way a file
// named as the target under one is still read; the skip applies to a walk
// entering the tree from outside.
var pseudoFsRoots = []string{"/proc", "/sys", "/dev"}

// isPseudoFsPath reports whether path names pseudo-filesystem content: one of
// the pseudoFsRoots, or anything under one. Only an absolute path can — a
// relative path resolves under the environment root, a real tree — so on a
// platform without these mount points nothing is ever pseudo. /dev/shm is
// carved back out: it is a tmpfs of ordinary bounded files — the RAM staging
// area even the test gates mint their scratch under — not a device tree, so
// a search rooted there is a search of real files and must not be rerouted.
// The carve is keyed on the production /dev entry itself, so a list that
// names its own roots (the tests' stubs) never has it applied.
func isPseudoFsPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range pseudoFsRoots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			if root == "/dev" && (path == "/dev/shm" || strings.HasPrefix(path, "/dev/shm/")) {
				return false
			}
			return true
		}
	}
	return false
}

// walkSkipsPseudoDir reports whether a walk rooted at the absolute path root
// must refuse to descend into the directory at the absolute path dir: only a
// pseudo-filesystem directory reached from outside the pseudo tree is
// skipped. A search rooted inside /proc searches it — naming that tree is a
// deliberate request, like naming /dev/zero as the path target.
func walkSkipsPseudoDir(root, dir string) bool {
	return isPseudoFsPath(dir) && !isPseudoFsPath(root)
}

// grepRootIsPseudo reports whether the walk root itself resolves inside a
// pseudo root: a search rooted there is a deliberate request to search that
// tree, and every regular file it reads is a stream whose descriptor must
// keep the nonblocking flag. A root that cannot be resolved is not one.
func grepRootIsPseudo(root string) bool {
	resolved, err := filepath.EvalSymlinks(root)
	return err == nil && isPseudoFsPath(resolved)
}

// grepReachesPseudoRoot reports whether a walk from the absolute directory
// dir would reach one of the pseudoFsRoots: dir is the filesystem root —
// every absolute path's ancestor — or a proper ancestor of one of them. A
// dir that is itself a pseudo root reaches nothing: it is searched, with
// each file's read bounded, rather than refused.
func grepReachesPseudoRoot(dir string) bool {
	dir = filepath.Clean(dir)
	for _, root := range pseudoFsRoots {
		if dir == string(filepath.Separator) || strings.HasPrefix(filepath.Clean(root), dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// grepMaxFileBytes bounds how much of one file a grep reads. A file is a
// stream as far as the reader is concerned — /proc/kcore presents as a
// regular file of ~128TB and /dev/zero as a size-0 source that never ends —
// so the read itself has to stop somewhere, and a file with more bytes than
// this is skipped the way a binary or unreadable one is (#4480). 16 MiB sits
// far above any file a text search reads whole while keeping one file's
// transient bytes a rounding error against the process's memory. A variable
// so a test can exercise the skip with a fixture-sized file.
var grepMaxFileBytes = 16 << 20

// grepRetainedBytes bounds the bytes of result lines a search retains across
// every file: an emitted line can be as long as the read cap, so a hundred
// matching lines of that shape retain gigabytes before the join doubles
// them. 8 MiB matches the tool output channel's own capture cap
// (cappedOutputBuffer), so the search never retains more than the channel
// delivers anyway, and an ordinary search's output sits far below it. A
// variable so a test can exercise the cut with fixture-sized lines.
var grepRetainedBytes = 8 << 20

// errGrepFileTooLarge reports a source with more bytes than grepMaxFileBytes;
// the walk's unreadable-file handling skips the file and keeps walking.
var errGrepFileTooLarge = errors.New("file exceeds the grep read cap")

// grepStreamWindowBytes bounds one read of the over-cap streaming search: a
// file named as the target is searched window by window, never held whole.
// A variable so a test can force the window boundaries with a small file.
var grepStreamWindowBytes = 1 << 20

// grepCappedRead reads r until grepMaxFileBytes and reports errGrepFileTooLarge
// when more bytes remain, so an effectively-unbounded source costs one bounded
// read instead of the whole file.
func grepCappedRead(r io.Reader) ([]byte, error) {
	if f, ok := r.(*grepCtxFile); ok {
		return grepCappedReadFile(f.ctx, f.File)
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(grepMaxFileBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > grepMaxFileBytes {
		return nil, errGrepFileTooLarge
	}
	return data, nil
}

// grepCtxFile is the file grepAdmitFS hands back: the admitted descriptor
// plus the context its read must honor. The reader seams' signature is
// ctx-less (grepReadFile), so the context travels with the file instead.
type grepCtxFile struct {
	*os.File
	ctx context.Context
}

// grepReadStallTimeout bounds how long one file's capped read may take: an
// admitted stream with no writer — an idle pty master, a device nobody
// feeds — would otherwise hold the read forever, because the byte cap only
// counts bytes that arrive (#4574 round 3, HIGH). A multiple of the 10s
// subprocess timeout the ripgrep arm used to give these targets, so the
// native bound is never the tighter one.
var grepReadStallTimeout = 30 * time.Second

// errGrepStreamStalled reports a read that hit grepReadStallTimeout without
// finishing; the walk's unreadable-file handling skips the file and keeps
// walking.
var errGrepStreamStalled = errors.New("stream read stalled")

// grepCappedReadFile is grepCappedRead for a descriptor the runtime poller
// owns: the read arms a deadline that cancellation yanks to now, so a
// blocked stream read ends promptly either way. A regular file is not
// pollable — SetReadDeadline refuses it — and never blocks past its data,
// so it falls through to the plain capped read.
func grepCappedReadFile(ctx context.Context, f *os.File) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	disarm := armStreamDeadline(ctx, f)
	defer disarm()
	data, err := io.ReadAll(io.LimitReader(f, int64(grepMaxFileBytes)+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, errGrepStreamStalled
		}
		return nil, err
	}
	if len(data) > grepMaxFileBytes {
		return nil, errGrepFileTooLarge
	}
	return data, nil
}

// armStreamDeadline arms f's read deadline for grepReadStallTimeout, watched
// by ctx: cancellation yanks the deadline to now, so a blocked read ends
// promptly either way. A descriptor the runtime poller does not own — the
// ordinary blocking regular file — arms nothing, and such a read never
// blocks past its data. The returned disarm clears the deadline and stops
// the watcher.
func armStreamDeadline(ctx context.Context, f *os.File) (disarm func()) {
	if err := f.SetReadDeadline(time.Now().Add(grepReadStallTimeout)); err != nil {
		return func() {}
	}
	watched := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Yank the deadline: the poller wakes the blocked read with a
			// deadline error, which the caller maps back to ctx.
			_ = f.SetReadDeadline(time.Now())
		case <-watched:
		}
	}()
	return func() {
		_ = f.SetReadDeadline(time.Time{})
		close(watched)
	}
}

// readIgnoreFileBytes reads up to limit bytes of an ignore file with the
// same deadline-and-cancellation treatment the search reads have: an
// admitted regular stream that never becomes readable — /proc/kmsg behind a
// .gitignore symlink — would otherwise park the read on the runtime poller
// with nothing bounding the wait (#4574 round 8). A stall surfaces as
// errGrepStreamStalled, which readIgnoreFile's unreadable-skip class
// already answers; a cancellation maps to itself there.
func readIgnoreFileBytes(f fs.File, limit int64) ([]byte, error) {
	if cf, ok := f.(*grepCtxFile); ok {
		disarm := armStreamDeadline(cf.ctx, cf.File)
		defer disarm()
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, errGrepStreamStalled
		}
		return nil, err
	}
	return data, nil
}

// grepReadFileCapped reads one file of a grep walk through fsys, bounded by
// grepMaxFileBytes. It is the default of both arms' reader seams, so a test
// stubbing a seam replaces the failure it injects, not the cap.
func grepReadFileCapped(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return grepCappedRead(f)
}

// grepAdmitFS is the filesystem the off arm's grep walk reads through: it
// answers Open itself with a nonblocking, type-checked open of the file
// under root, following symlinks as the off arm always has, so a FIFO or
// socket entry — or a device reached during a walk — is refused instead of
// blocking the open until a peer appears (#4574 round 1's HIGH finding: a
// readerless FIFO hung a path=/ search indefinitely, because no context
// check can interrupt a blocked open). Everything else — ReadDir, Stat, the
// listing budgets — stays the wrapped filesystem's job.
type grepAdmitFS struct {
	fs.FS
	// root is the absolute directory the wrapped filesystem is rooted at; a
	// direct open needs it because os.DirFS's own Open is blocking.
	root string
	// ctx is checked before an open: this layer replaces cancelFS's Open for
	// reads, so it inherits that check.
	ctx context.Context
	// admitDevice lets the search's own named target be a device, which is
	// still read, bounded by grepMaxFileBytes. Naming /dev/zero is a
	// deliberate request; a FIFO or socket is never admitted, deliberate or
	// not, because reading one needs a peer that may never appear.
	admitDevice bool
	// rootPseudo reports whether the walk root itself resolves inside a
	// pseudo root: every file this search opens is a pseudo-rooted stream
	// then, so each open keeps the nonblocking flag without resolving the
	// file's own path.
	rootPseudo bool
}

func (f grepAdmitFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	full := filepath.Join(f.root, filepath.FromSlash(name))
	file, err := openRegularFollow(full, f.admitDevice, f.pseudoStreamed(full))
	if err != nil {
		return nil, err
	}
	return &grepCtxFile{File: file, ctx: f.ctx}, nil
}

// pseudoStreamed reports whether full names a file whose resolved path sits
// inside a pseudo root — the walk root already does, or the leaf is a
// symlink alias into one. Such a file is a stream that can block in read(2)
// although it stats regular (/proc/kmsg), so its descriptor must keep
// O_NONBLOCK for the capped read's deadline to reach it (#4574 round 4).
func (f grepAdmitFS) pseudoStreamed(full string) bool {
	if f.rootPseudo {
		return true
	}
	if info, err := os.Lstat(full); err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(full)
	return err == nil && isPseudoFsPath(resolved)
}

// ReadDir and Stat forward to the wrapped filesystem rather than letting
// fs.ReadDir fall back to Open plus the directory's own ReadDir — the same
// trap boundedDirFS.Stat documents for its own embedding: the fallback would
// bypass boundedDirFS's chunked, budgeted listing, and a directory large
// enough to matter would materialize whole before any budget could refuse.
func (f grepAdmitFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.FS, name)
}

func (f grepAdmitFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(f.FS, name)
}

// grepTakesNativeArm reports whether the resolved grep target must take the
// native walk rather than ripgrep, whatever rg would do with it. ripgrep has
// no per-file read bound of its own: --max-filesize is metadata-based, so a
// stream that stats small (a FIFO, /proc/kmsg) is searched to its end, and rg
// ignores the flag outright for a file named on its command line. The
// targets that must not reach it: a search root that reaches or sits inside
// a pseudo-filesystem root (/proc/kcore alone presents as a ~128TB regular
// file, #4480), a named non-regular file (rg would block on a FIFO, stream a
// device), and a named file over the read cap (the native arm skips it, as
// both arms skip an oversized file found in a walk).
func grepTakesNativeArm(dir string) bool {
	if isPseudoFsPath(dir) || grepReachesPseudoRoot(dir) {
		return true
	}
	// A symlink alias can spell pseudo-filesystem content as an ordinary
	// path — /tmp/proc-alias -> /proc — so classify what the target resolves
	// to as well: os.Stat follows the alias and reports a small regular
	// file. Resolution walks ancestors and leaf alike and needs no
	// permission beyond stat.
	if resolved, rerr := filepath.EvalSymlinks(dir); rerr == nil && (isPseudoFsPath(resolved) || grepReachesPseudoRoot(resolved)) {
		return true
	}
	info, err := os.Stat(dir)
	if err != nil || info.IsDir() {
		return false
	}
	return !info.Mode().IsRegular() || info.Size() > int64(grepMaxFileBytes)
}

// sortPathStat is the stat the glob result ordering runs on; a variable so
// tests can observe how many times each path is stat'ed.
var sortPathStat = os.Stat

// sortPathsByMtimeDesc sorts paths newest-modification-first, ties broken by
// path. Shared by the off and sandboxed glob so their ordering is identical.
//
// Every path is stat'ed once up front rather than from inside the comparator,
// which stat'ed O(n log n) times and left a large result set sorting for
// seconds with nothing watching ctx. The stat loop checks ctx between paths,
// so a cancelled glob stops here too and says so instead of handing back a
// half-ordered list.
func sortPathsByMtimeDesc(ctx context.Context, paths []string) error {
	type dated struct {
		path     string
		mod      time.Time
		modKnown bool
	}
	entries := make([]dated, len(paths))
	for i, p := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries[i] = dated{path: p}
		if info, err := sortPathStat(p); err == nil {
			entries[i].mod, entries[i].modKnown = info.ModTime(), true
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		// A path whose stat failed has no modification time to order by, so
		// it falls back to path order — as it did when the comparator stat'ed.
		if !a.modKnown || !b.modKnown {
			return a.path < b.path
		}
		if !a.mod.Equal(b.mod) {
			return a.mod.After(b.mod)
		}
		return a.path < b.path
	})
	for i, e := range entries {
		paths[i] = e.path
	}
	return nil
}
