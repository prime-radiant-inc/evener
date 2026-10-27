package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/searchresult"
)

// ListDirectory returns the entries under path (resolved relative to RootDir),
// recursing up to depth levels (depth <= 0 is treated as 1). Entries are sorted
// by name within each directory, nested names are prefixed with their relative
// path, and file sizes are populated.
func (e *LocalExecutionEnvironment) ListDirectory(path string, depth int) ([]DirEntry, error) {
	return e.listDirectory(path, depth, false)
}

// ListVisibleDirectory is ListDirectory without dot entries. It never reads
// a dot directory, so a large .git beneath path costs it nothing.
func (e *LocalExecutionEnvironment) ListVisibleDirectory(path string, depth int) ([]DirEntry, error) {
	return e.listDirectory(path, depth, true)
}

func (e *LocalExecutionEnvironment) listDirectory(path string, depth int, visibleOnly bool) ([]DirEntry, error) {
	if depth <= 0 {
		depth = 1
	}
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		// Sandboxed: fd-anchored recursive walk (each subdir re-opened beneath its
		// parent fd with O_NOFOLLOW; masked entries skipped; symlinks not followed).
		return sfs.listDir("list_dir", e.resolve(path), depth, visibleOnly)
	}
	root := e.resolve(path)

	var out []DirEntry
	var walk func(absDir string, relPrefix string, d int) error
	walk = func(absDir string, relPrefix string, d int) error {
		ents, err := listReadDir(absDir)
		if err != nil {
			return err
		}
		sort.SliceStable(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		for _, ent := range ents {
			name := ent.Name()
			if visibleOnly && IsDotPath(name) {
				continue
			}
			relName := name
			if relPrefix != "" {
				relName = filepath.Join(relPrefix, name)
			}
			de := DirEntry{Name: relName, IsDir: ent.IsDir()}
			if ent.Type()&os.ModeSymlink != 0 {
				de.IsSymlink = true
			}
			if !ent.IsDir() {
				if info, err := ent.Info(); err == nil {
					de.Size = info.Size()
					de.ModTime = info.ModTime()
					de.IsRegular = info.Mode().IsRegular()
					if info.Mode()&0o111 != 0 {
						de.IsExec = true
					}
				}
			}
			out = append(out, de)
			if ent.IsDir() && d > 1 {
				if err := walk(filepath.Join(absDir, name), relName, d-1); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(root, "", depth); err != nil {
		return nil, err
	}
	return out, nil
}

// ListDirectoryWithBudget implements DirListBudgeter: ListDirectory as a
// context-aware, budgeted page/walk. The walk stops once it has collected
// enough entries to answer the page the budget was built for (#2375) — or
// when the budget's bounds refuse — rather than enumerating the whole
// requested subtree; a cancelled context aborts it. Depth is clamped to
// MaxListDirDepth. What the walk collected is returned whole; slicing it
// into the rendered page is the caller's.
func (e *LocalExecutionEnvironment) ListDirectoryWithBudget(ctx context.Context, path string, depth int, budget *DirListBudget) ([]DirEntry, error) {
	if budget == nil {
		return nil, errors.New("list_dir requires a budget: pass execenv.NewDirListBudget(offset, limit, charBudget)")
	}
	if depth <= 0 {
		depth = 1
	}
	depth = min(depth, MaxListDirDepth)
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		// Sandboxed: fd-anchored recursive walk (each subdir re-opened beneath
		// its parent fd with O_NOFOLLOW; masked entries skipped; symlinks not
		// followed) under the same bounds.
		return sfs.listDirBudgeted(ctx, "list_dir", e.resolve(path), depth, budget)
	}
	root := e.resolve(path)
	var out []DirEntry
	if err := listDirWalk(ctx, root, "", depth, &out, budget); err != nil {
		return nil, err
	}
	return out, nil
}

// Glob returns absolute paths matching pattern (doublestar syntax) under
// basePath, which defaults to RootDir and is resolved relative to RootDir when
// not absolute. Results are sorted by modification time, newest first, with
// ties broken by path.
//
// Dotfiles/dirs (.git, .claude/worktrees/x, ...) and gitignored paths are
// excluded by default, matching grepNative's long-standing hidden-file skip
// plus new .gitignore awareness; pass includeIgnored(true) to restore them.
//
// The walk is bounded by ctx: cancelling it aborts an in-flight glob and
// returns ctx's error. It is also bounded by cycle detection — it will not
// list a directory that is the same file as one of its own ancestors — so a
// `**` pattern over a tree that links back into itself still terminates while
// a directory symlink that points elsewhere is followed and matched normally.
// (The sandboxed walk is stricter: it never follows a symlink at all, which is
// a policy boundary rather than a termination measure.)
func (e *LocalExecutionEnvironment) Glob(ctx context.Context, pattern string, basePath string, includeIgnored ...bool) ([]string, error) {
	matches, _, err := e.GlobWithExclusions(ctx, pattern, basePath, len(includeIgnored) > 0 && includeIgnored[0])
	return matches, err
}

// globBaseFS opens the filesystem the off-sandbox glob walks, rooted at the
// resolved base directory; a variable so tests can drive the exported entry
// point over a filesystem they control. budget is threaded through so a
// bounded implementation can charge what it reads while listing, and ctx down
// to boundedDirFS so its chunk loop can observe cancellation.
var globBaseFS = func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
	return boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}
}

// GlobWithBudget implements GlobBudgeter: same matching as Glob, but the
// caller supplies budget and reads what the call had to cut off it once the
// call returns (see GlobBudget.TruncatedAt), rather than being refused a
// truncated listing outright the way GlobWithExclusions is.
func (e *LocalExecutionEnvironment) GlobWithBudget(ctx context.Context, pattern string, basePath string, includeIgnored bool, budget *GlobBudget) ([]string, int, error) {
	if budget == nil {
		return nil, 0, errors.New("glob requires a budget: pass execenv.NewGlobBudget() or call GlobWithExclusions for an internally budgeted call")
	}
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		return nil, 0, err
	}
	base := strings.TrimSpace(basePath)
	if base == "" {
		base = e.RootDir
	}
	if !filepath.IsAbs(base) {
		base = filepath.Join(e.RootDir, base)
	}
	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		// Sandboxed: the base is policy-checked and the walk refuses symlink
		// traversal (no out-of-root match) and drops masked matches.
		return sfs.glob(ctx, "glob", base, pattern, includeIgnored, budget)
	}
	fsys := cancelFS{ctx: ctx, fsys: globBaseFS(ctx, base, budget)}
	var ignores *ignoreSet
	if !includeIgnored {
		// No masking concept off the sandboxed path: no-op skip.
		var err error
		ignores, err = loadIgnoreSet(ctx, fsys, nil, budget, ignoreScopeForPatterns(patterns))
		if err != nil {
			return nil, 0, err
		}
	}
	seen := make(map[string]struct{})
	var abs []string
	excluded := 0
	for _, pattern := range patterns {
		matches, err := globMatches(ctx, fsys, pattern, budget)
		if err != nil {
			return nil, 0, err
		}
		for _, m := range matches {
			if !includeIgnored {
				drop, err := globMatchExcluded(ctx, fsys, ignores, m)
				if err != nil {
					return nil, 0, err
				}
				if drop {
					excluded++
					continue
				}
			}
			path := filepath.Join(base, m)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			abs = append(abs, path)
		}
	}
	if err := sortPathsByMtimeDesc(ctx, abs); err != nil {
		return nil, 0, err
	}
	return abs, excluded, nil
}

// GlobWithExclusions implements GlobExcluder: same matching as Glob, but also
// reports how many candidate matches the default dotfile/gitignore exclusion
// dropped, so a fully-filtered result can be told apart from a genuinely
// empty one (see the "silent-empty is the enemy" global constraint). The
// count never includes matches dropped by sandbox masking — that's a
// separate security boundary, not something to describe to the caller as
// "gitignored".
//
// The budget backing this call belongs to GlobWithExclusions alone, so a
// caller here has no way to learn a listing was cut short: a truncation
// comes back as an error rather than a plausible-looking short list. A
// caller that wants the partial list uses GlobBudgeter.GlobWithBudget with
// its own budget instead.
func (e *LocalExecutionEnvironment) GlobWithExclusions(ctx context.Context, pattern string, basePath string, includeIgnored bool) ([]string, int, error) {
	budget := NewGlobBudget()
	matches, excluded, err := e.GlobWithBudget(ctx, pattern, basePath, includeIgnored, budget)
	if err != nil {
		return nil, 0, err
	}
	if truncatedAt := budget.TruncatedAt(); truncatedAt > 0 {
		return nil, 0, fmt.Errorf("glob matched more than %d candidates, the cap for one call, so the result would have been incomplete: narrow the pattern or its base directory, or call GlobWithBudget with a budget to get the partial list", truncatedAt)
	}
	return matches, excluded, nil
}

// DefaultGrepMaxResults is the result cap Grep applies when the caller's
// maxResults is <= 0. The grep tool's schema advertises this same default
// and its pin test asserts the advertised number against this constant.
const DefaultGrepMaxResults = 100

// Grep searches for pattern under path (defaulting to RootDir), using ripgrep
// when available and falling back to a native Go regex search otherwise.
// globFilter restricts which files are searched, caseInsensitive enables
// case-insensitive matching, and maxResults caps the output (default 100).
// outputMode selects the result format: "files_with_matches", "count", or
// matching lines otherwise.
// resolveGrepDir resolves a grep path argument against the environment root: a
// blank path means the root, and a relative path is joined under it. It is the
// pure directory-resolution core shared by Grep's ripgrep and native-fallback arms.
func resolveGrepDir(path, rootDir string) string {
	dir := strings.TrimSpace(path)
	if dir == "" {
		dir = rootDir
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(rootDir, dir)
	}
	return dir
}

// buildRipgrepArgs builds the ripgrep argument vector for a Grep call. It is the
// pure arg-construction core: a fixed no-heading / no-color prefix, then exactly
// one output-mode flag (files-with-matches / count / line-number), then a NUL
// path delimiter and the per-file size cap, then optional case-insensitivity
// and glob filter, then the trailing pattern and directory.
func buildRipgrepArgs(outputMode string, caseInsensitive bool, globFilter, pattern, dir string, contextLines int) []string {
	filters := []string(nil)
	if strings.TrimSpace(globFilter) != "" {
		filters = []string{globFilter}
	}
	return buildRipgrepArgsWithFilters(outputMode, caseInsensitive, filters, pattern, dir, contextLines)
}

func buildRipgrepArgsWithFilters(outputMode string, caseInsensitive bool, globFilters []string, pattern, dir string, contextLines int) []string {
	args := []string{"--no-heading", "--color", "never"}
	switch outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	default:
		args = append(args, "--line-number")
	}
	// --null ends each path with a NUL in place of its ":" or "-" (or line
	// ending), so ripgrepOutputLines can find the whole path even when the
	// name holds a newline.
	// --max-filesize mirrors the native reader's per-file cap: rg skips a
	// file whose size already exceeds it, exactly as grepCappedRead stops and
	// the walk skips the file, so an oversized file is treated the same with
	// and without ripgrep.
	args = append(args, "--null", "--max-filesize", strconv.Itoa(grepMaxFileBytes))
	if caseInsensitive {
		args = append(args, "-i")
	}
	for _, globFilter := range globFilters {
		if strings.TrimSpace(globFilter) != "" {
			args = append(args, "-g", globFilter)
		}
	}
	// -C (context lines) only has an effect on content output: files-with-matches
	// and count report per-file, not per-line, so surrounding lines have nothing
	// to attach to there.
	if contextLines > 0 && (outputMode == "" || outputMode == "content") {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	// A literal "--" ends rg's option parsing, so a pattern that itself looks
	// like a flag (e.g. "--font-size-body") is treated as the positional search
	// string instead of being parsed (and silently mishandled) as an option.
	args = append(args, "--", pattern, dir)
	return args
}

// Grep searches path for pattern, honoring the glob filter, case
// sensitivity, result cap, output mode, and context window the caller asked
// for.
func (e *LocalExecutionEnvironment) Grep(ctx context.Context, pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error) {
	return e.GrepSkipping(ctx, pattern, path, globFilter, caseInsensitive, maxResults, outputMode, grepContextLines(contextLines), nil)
}

// grepContextLines is the context window an optional contextLines argument
// asks for: its first value when positive, else 0.
func grepContextLines(contextLines []int) int {
	if len(contextLines) > 0 && contextLines[0] > 0 {
		return contextLines[0]
	}
	return 0
}

// GrepSkipping is Grep, except that it never searches a file for which skip,
// when not nil, reports true. skip gets the file's slash path relative to the
// searched directory. A skipped file is left out before the result cap, so
// it never uses up a result.
func (e *LocalExecutionEnvironment) GrepSkipping(ctx context.Context, pattern string, path string, globFilter string, caseInsensitive bool, maxResults int, outputMode string, ctxLines int, skip func(rel string) bool) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	dir := resolveGrepDir(path, e.RootDir)

	if sfs := e.sandbox(); sfs != nil {
		defer sfs.release()
		// Sandboxed sessions always use the denylist-aware, symlink-refusing native
		// walk, EVEN when ripgrep is present. The rg subprocess is still UNCONFINED
		// in M2 — only its base is policy-checked, so it would read masked/denylisted
		// descendants under an allowed base (e.g. ~/.ssh when the base is $HOME in
		// read-only). Its kernel wrapping is M3 defense-in-depth, not something to
		// rely on here: correctness over speed for a sandboxed session. grepNative
		// policy-checks the base itself and skips masked subtrees.
		return sfs.grepNative(ctx, pattern, dir, globFilter, caseInsensitive, maxResults, outputMode, ctxLines, skip)
	}

	// Ripgrep takes no predicate, so a search that skips files walks natively.
	if skip != nil {
		return e.grepNativeSkipping(ctx, pattern, dir, globFilter, caseInsensitive, maxResults, outputMode, ctxLines, skip)
	}
	// Targets ripgrep cannot bound never reach it (see grepTakesNativeArm);
	// the native walk applies the pseudo-root skip and the per-file read cap.
	if grepTakesNativeArm(dir) {
		return e.grepNativeSkipping(ctx, pattern, dir, globFilter, caseInsensitive, maxResults, outputMode, ctxLines, skip)
	}
	rg, err := e.findExecutable("rg")
	if err != nil {
		// Fallback to native Go regex search when ripgrep is absent
		return e.grepNativeSkipping(ctx, pattern, dir, globFilter, caseInsensitive, maxResults, outputMode, ctxLines, nil)
	}

	args := buildRipgrepArgsWithFilters(outputMode, caseInsensitive, globFilters, pattern, dir, ctxLines)

	if maxResults <= 0 {
		maxResults = DefaultGrepMaxResults
	}
	// ExecArgv, not ExecCommand: ripgrep gets a real argument vector, so no
	// shell ever parses the pattern, directory, or glob filter. The old
	// ExecCommand string rendered each token with shellquote.Literal (POSIX
	// single quoting), but on Windows ExecCommand runs the line through
	// cmd.exe, which treats a single quote as ordinary text and offers no
	// reliable way to neutralize '&' or '%'. A pattern like "foo&whoami" or
	// "%VAR%" therefore stayed live there. Portable and shell-free beats
	// platform-specific quoting: with argv in hand there is nothing to
	// escape on any platform.
	filesOnly := outputMode == "files_with_matches"
	oneFile := grepTargetsOneFile(dir)
	// Bound ripgrep's stdout at the capture itself, then parse the bounded bytes
	// with the same formatter the native arm's lines go through, so the result
	// keeps ripgrep's own exclusion rules by construction (rgCapture).
	capture := newRgCapture(dir, filesOnly, grepRetainedBytes)
	res, err := e.execPreparedCommandStdout(ctx, e.commands().Argv(rg, args...), 10_000, e.RootDir, nil, capture)
	if err == nil {
		// Keep the first maxResults lines. In content mode that counts context
		// lines and "--" separators too, which is what the grep tool's
		// max_results promises ("lines, file paths, or count entries by output
		// mode", tool.DefGrep) and what grepAccum.feedContent counts, so with
		// context_lines a cut can fall inside a match's context.
		view := capture.view(filesOnly)
		// A capture that left nothing out holds the stream in two pieces: the
		// head, and the tail's records, which are the stream's own final ones
		// whenever the cap fell on a record boundary (wholeStream). Parsing them
		// together keeps a record the cap landed on whole instead of losing it to
		// the tail note it does not need — nothing was omitted.
		source := view.head
		if whole := view.wholeStream(); whole != "" {
			source = whole
		}
		lines := ripgrepOutputLines(source, dir, oneFile, filesOnly)
		var notes []string
		if view.omitted > 0 {
			// The capture left bytes out; the note discloses that exactly as the
			// native arm's retained-output budget does, and the stream's last
			// records ride in the same note so the model still sees how the
			// matches end — never as rows past maxResults.
			notes = append(notes, grepRetainedTruncationNote())
		}
		// The tail is shown whenever the cap did something to the stream: it left
		// bytes out, or it cut a tail record at its slot. Gating on the omission
		// alone lost the tail of a boundary-ending cap that omitted nothing, with
		// no disclosure at all.
		if view.omitted > 0 || view.cut > 0 {
			if tail := rgTailNote(view); tail != "" {
				notes = append(notes, tail)
			}
		}
		if len(lines) > maxResults {
			notes = append(notes, grepTruncationNote(maxResults))
		}
		// rg skips over-cap files by --max-filesize without a trace in its
		// output, so the same omission note the native walk's finishWalk
		// composes is composed here, from ripgrep's own views of the tree.
		oversized, cerr := e.grepRgOversizeCount(ctx, rg, dir, globFilters)
		if cerr != nil {
			if cancelErr := ctx.Err(); cancelErr != nil {
				return "", cancelErr
			}
			// A listing that failed another way is no note: the disclosure
			// is best-effort and must not assert a count it could not
			// obtain — a failed listing is not an empty one.
			oversized = 0
		}
		if oversized > 0 {
			notes = append(notes, grepOversizeNote(oversized))
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if len(notes) > 0 {
			return searchresult.WithNotes(strings.Join(lines[:min(len(lines), maxResults)], "\n"), notes...), nil
		}
		return strings.Join(lines, "\n"), nil
	}
	// Exit code 1 means "no matches" for rg.
	if res.ExitCode == 1 {
		// A directory whose only matching files were over-cap was not
		// searched to its end: the empty says so rather than reading as
		// "genuinely no matches".
		oversized, cerr := e.grepRgOversizeCount(ctx, rg, dir, globFilters)
		if cerr != nil {
			if cancelErr := ctx.Err(); cancelErr != nil {
				return "", cancelErr
			}
			oversized = 0
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if oversized > 0 {
			return searchresult.WithNotes("", grepOversizeNote(oversized)), nil
		}
		return "", nil
	}
	// The capture consumed rg's stdout, so a failed search's diagnostic shows the
	// bounded prefix it kept beside rg's stderr.
	return capture.diagnostics() + res.Stderr, err
}

// grepRgOversizeCount reports how many candidate files ripgrep leaves out of
// a directory search by --max-filesize, so the rg arm can disclose the same
// omissions the native walk's finishWalk does: the difference between the
// files rg lists without the size filter and with it. Both listings run
// through the same ripgrep with the same glob filters, so the count is rg's
// own view of the tree — its ignore rules included — not a guess about which
// files it would search. A named file never needs the count (an over-cap one
// routes to the native arm), a cancelled search reports its cancellation,
// and any listing that fails another way returns none: the disclosure is
// best-effort and never fails the search it describes.
func (e *LocalExecutionEnvironment) grepRgOversizeCount(ctx context.Context, rg, dir string, globFilters []string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if grepTargetsOneFile(dir) {
		return 0, nil
	}
	all, err := e.grepRgListFiles(ctx, rg, dir, globFilters, false)
	if err != nil {
		return 0, err
	}
	under, err := e.grepRgListFiles(ctx, rg, dir, globFilters, true)
	if err != nil {
		return 0, err
	}
	if under > all {
		return 0, nil
	}
	return all - under, nil
}

// grepRgListFiles counts one rg --files listing of dir: --null keeps the
// paths separable and only their number matters — counted as they stream
// through, so a listing of any size is never retained — and --max-filesize,
// when capped, is the same filter the search itself passes. rg exits 1 for
// a listing that found no file, so both 0 and 1 are a listing's answers;
// anything else is a failure, which propagates so the caller suppresses the
// note rather than deriving a count from a half-failed pair of listings,
// while a cancellation is the search's error to report.
func (e *LocalExecutionEnvironment) grepRgListFiles(ctx context.Context, rg, dir string, globFilters []string, capped bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	args := []string{"--files", "--null"}
	if capped {
		args = append(args, "--max-filesize", strconv.Itoa(grepMaxFileBytes))
	}
	for _, globFilter := range globFilters {
		if strings.TrimSpace(globFilter) != "" {
			args = append(args, "-g", globFilter)
		}
	}
	args = append(args, "--", dir)
	counter := &nulCounter{}
	res, err := e.execPreparedCommandStdout(ctx, e.commands().Argv(rg, args...), 10_000, e.RootDir, nil, counter)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		if res.ExitCode != 0 && res.ExitCode != 1 {
			return 0, err
		}
	}
	return int(counter.n), nil
}

// nulCounter counts the NUL delimiters ripgrep --null ends each path with,
// retaining nothing: whatever streams through is counted and dropped, so a
// listing costs its delimiters' count and one pipe block of memory.
type nulCounter struct{ n int64 }

func (c *nulCounter) Write(p []byte) (int, error) {
	c.n += int64(bytes.Count(p, []byte{0}))
	return len(p), nil
}

// grepTargetsOneFile reports whether a grep's resolved target names a single
// file rather than a directory to walk.
func grepTargetsOneFile(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && !info.IsDir()
}

// ripgrepOutputLines splits ripgrep's --null output into lines in the shape
// the native fallback (grepNative) gives, so a search reads the same whether
// or not ripgrep is installed (#3259): no trailing newline, and each path
// relative to the searched directory and written as OneLinePath gives it.
// rg echoes the directory it was given in front of every path and ends the
// path with a NUL, so a line starting with that directory holds a path up to
// its NUL, even one holding a newline; any other line (a "--" between context
// groups) is plain text. A single named file is "." to the fallback, where rg
// names it in files-with-matches mode; content and count lines for a single
// file carry no path from either.
//
// rg searches files in parallel and prints each file's lines together, in
// the order the files finish, so the lines come back sorted by path in the
// native walk's order, each file's own lines in order: the cap then keeps the
// same lines on every run, with or without ripgrep (#3284). Sorting here rather than with rg's --sort path keeps the search
// parallel. A "--" between two files' lines is set again between them after
// the sort; one inside a file stays where it is.
func ripgrepOutputLines(stdout, dir string, oneFile, filesOnly bool) []string {
	if oneFile {
		if filesOnly {
			return []string{"."}
		}
		return fileLines(stdout)
	}
	prefix := strings.TrimSuffix(dir, string(filepath.Separator)) + string(filepath.Separator)
	type fileBlock struct {
		rel   string
		lines []string
	}
	var blocks []fileBlock
	pendingSeparator, separated := false, false
	for rest := stdout; rest != ""; {
		var rel, line string
		path, afterPath, cut := strings.Cut(rest, "\x00")
		switch {
		case cut && strings.HasPrefix(path, prefix) && filesOnly:
			rel, rest = path[len(prefix):], afterPath
			line = OneLinePath(rel)
		case cut && strings.HasPrefix(path, prefix):
			var text string
			rel = path[len(prefix):]
			text, rest, _ = strings.Cut(afterPath, "\n")
			line = OneLinePath(rel) + ripgrepPathSeparator(text) + text
		default:
			line, rest, _ = strings.Cut(rest, "\n")
			if line == "--" {
				// A separator between context groups belongs inside a
				// block only when the next line is the same file's.
				pendingSeparator = true
				continue
			}
			// Any other line without a path stays with the lines around it.
			if n := len(blocks); n > 0 {
				rel = blocks[n-1].rel
			}
		}
		if n := len(blocks); n > 0 && blocks[n-1].rel == rel {
			if pendingSeparator {
				blocks[n-1].lines = append(blocks[n-1].lines, "--")
			}
			blocks[n-1].lines = append(blocks[n-1].lines, line)
		} else {
			separated = separated || pendingSeparator
			blocks = append(blocks, fileBlock{rel: rel, lines: []string{line}})
		}
		pendingSeparator = false
	}
	// Compare one path segment at a time, the native walk's order: a
	// directory's files come before a sibling file whose name extends the
	// directory's ("ctx/m.txt" before "ctx.txt").
	slices.SortStableFunc(blocks, func(a, b fileBlock) int {
		return slices.Compare(strings.Split(a.rel, string(filepath.Separator)), strings.Split(b.rel, string(filepath.Separator)))
	})
	var lines []string
	for i, block := range blocks {
		if i > 0 && separated {
			lines = append(lines, "--")
		}
		lines = append(lines, block.lines...)
	}
	return lines
}

// ripgrepPathSeparator is the separator --null replaced after a path on a
// content or count line: "-" before a context line ("12-text"), else ":".
func ripgrepPathSeparator(text string) string {
	digits := strings.TrimLeft(text, "0123456789")
	if len(digits) < len(text) && strings.HasPrefix(digits, "-") {
		return "-"
	}
	return ":"
}

func (e *LocalExecutionEnvironment) grepNative(ctx context.Context, pattern, path, globFilter string, caseInsensitive bool, maxResults int, outputMode string, contextLines ...int) (string, error) {
	return e.grepNativeSkipping(ctx, pattern, path, globFilter, caseInsensitive, maxResults, outputMode, grepContextLines(contextLines), nil)
}

// grepNativeSkipping is grepNative, never searching a file skip names (see
// GrepSkipping).
func (e *LocalExecutionEnvironment) grepNativeSkipping(ctx context.Context, pattern, path, globFilter string, caseInsensitive bool, maxResults int, outputMode string, ctxLines int, skip func(rel string) bool) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	a, err := newGrepAccum(pattern, caseInsensitive, maxResults, outputMode, ctxLines)
	if err != nil {
		return "", err
	}
	path = filepath.Clean(path)
	// Walk a directory from its own fs.FS root so returned paths remain relative
	// to the requested directory. An explicitly named file is rooted at its
	// parent instead; fs.WalkDir needs the basename in that case, while grep's
	// output contract still treats the file as ".".
	walkRoot := "."
	singleFile := false
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			// fs.WalkDir follows a symlink when it is the walk root. Keep the
			// recursive no-directory-symlink-follow contract by classifying the
			// target before constructing the DirFS. A symlink to a regular file
			// remains an explicitly named file and is read through as before.
			target, targetErr := os.Stat(path)
			targetRegular := targetErr == nil && target.Mode().IsRegular()
			if !targetRegular {
				return "", nil
			}
		}
		if !info.IsDir() {
			walkRoot = filepath.Base(path)
			singleFile = true
			path = filepath.Dir(path)
		}
	}
	// fs.WalkDir does not descend through directory symlinks, matching the
	// secureDirFS policy on the sandboxed arm while retaining file-symlink
	// behavior for the unsandboxed fallback.
	budget := newGlobBudget("grep")
	// Reads go through grepAdmitFS rather than os.DirFS's blocking Open;
	// cancellation and the listing budgets stay wrapped underneath it. A
	// named target is the walk root's own file, so only that arm admits a
	// device (see grepAdmitFS.admitDevice).
	fsys := grepAdmitFS{
		FS:          cancelFS{ctx: ctx, fsys: boundedDirFS{FS: os.DirFS(path), budget: budget, ctx: ctx}},
		root:        path,
		ctx:         ctx,
		admitDevice: singleFile,
		rootPseudo:  grepRootIsPseudo(path),
	}
	// No masking concept off the sandboxed path: no-op skip.
	ignoreFS := fsys
	if singleFile {
		// Preserve the old single-file behavior: ignore rules are rooted at the
		// file argument, which cannot contain a .gitignore tree of its own.
		ignoreFS = grepAdmitFS{
			FS:          cancelFS{ctx: ctx, fsys: boundedDirFS{FS: os.DirFS(filepath.Join(path, walkRoot)), budget: budget, ctx: ctx}},
			root:        filepath.Join(path, walkRoot),
			ctx:         ctx,
			admitDevice: true,
			rootPseudo:  grepRootIsPseudo(filepath.Join(path, walkRoot)),
		}
	}
	walk := grepTreeWalk{
		ctx:         ctx,
		acc:         a,
		globFilters: globFilters,
		skip:        skip,
		root:        path,
		ignoreFS:    ignoreFS,
		budget:      budget,
		walk:        func(fn fs.WalkDirFunc) error { return grepWalk(fsys, walkRoot, fn) },
		read:        func(p string) ([]byte, error) { return grepReadFile(fsys, p) },
		namedFile:   singleFile,
	}
	if singleFile {
		// A file named as the target is a deliberate request: search the over-cap
		// regular behind it in bounded windows. A device keeps the capped skip —
		// its stream never ends, so bounding it is the cap's job, not the
		// window's.
		walk.largeNamedFile = func(p string) error {
			opened, oerr := fsys.Open(p)
			if oerr != nil {
				return nil
			}
			cf, ok := opened.(*grepCtxFile)
			if !ok {
				_ = opened.Close()
				return nil
			}
			info, ierr := cf.Stat()
			if ierr != nil || !info.Mode().IsRegular() {
				_ = opened.Close()
				return nil
			}
			stop, serr := a.feedLargeFile(cf.ctx, cf.File)
			_ = cf.Close()
			if serr != nil {
				if cancelErr := ctx.Err(); cancelErr != nil {
					return cancelErr
				}
				return nil //nolint:nilerr // best-effort grep: a stalled or failed stream is skipped, like an unreadable file
			}
			if stop {
				return filepath.SkipAll
			}
			return nil
		}
	}
	return walk.run()
}
