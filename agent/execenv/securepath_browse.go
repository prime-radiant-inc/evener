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
	a.results = append(a.results, line)
	return false
}

// cutAtCap records that feed found a result past maxResults, which it leaves
// out, and returns feed's stop.
func (a *grepAccum) cutAtCap() (stop bool) {
	a.truncated = true
	return true
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
	out := strings.Join(a.results, "\n")
	if a.truncated {
		return searchresult.WithNotes(out, grepTruncationNote(a.maxResults))
	}
	return out
}

// finishWalk is finish for a walk that left excludedByIgnore paths out.
// Silent-empty is the enemy (D2): when the walk found nothing, a note tells
// "no matches among the files searched, but N were skipped by the default
// dotfile/gitignore exclusion" from "genuinely no matches". grep has no
// include_ignored knob, so this is informational rather than a suggestion to
// retry.
func (a *grepAccum) finishWalk(excludedByIgnore int) string {
	out := a.finish()
	if out == "" && excludedByIgnore > 0 {
		return searchresult.WithNotes("", fmt.Sprintf("0 matches; %d dotfile/gitignored path(s) were excluded from the search", excludedByIgnore))
	}
	return out
}

// grepTruncationNote is the note on a grep result the cap cut short, so a
// model never reads the results it got as all there are.
func grepTruncationNote(maxResults int) string {
	return fmt.Sprintf("[results truncated at %d; narrow the path or glob_filter, or raise max_results]", maxResults)
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
// platform without these mount points nothing is ever pseudo.
func isPseudoFsPath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	for _, root := range pseudoFsRoots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
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

// errGrepFileTooLarge reports a source with more bytes than grepMaxFileBytes;
// the walk's unreadable-file handling skips the file and keeps walking.
var errGrepFileTooLarge = errors.New("file exceeds the grep read cap")

// grepCappedRead reads r until grepMaxFileBytes and reports errGrepFileTooLarge
// when more bytes remain, so an effectively-unbounded source costs one bounded
// read instead of the whole file.
func grepCappedRead(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(grepMaxFileBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > grepMaxFileBytes {
		return nil, errGrepFileTooLarge
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
}

func (f grepAdmitFS) Open(name string) (fs.File, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return openRegularFollow(filepath.Join(f.root, filepath.FromSlash(name)), f.admitDevice)
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
