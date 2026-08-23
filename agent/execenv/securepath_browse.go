package execenv

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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
	if s.underMasked(abs) {
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
	fileCounts   map[string]int
	filesSeen    map[string]struct{}
	total        int
}

// newGrepAccum compiles the pattern (with optional case-insensitivity) and
// initializes the accumulator; maxResults defaults to 100 when non-positive.
// contextLines (0-10, validated by the caller) adds that many lines of
// surrounding context around each match in "content"/"" output mode; it has no
// effect on "files_with_matches" or "count", which report per-file, not
// per-line.
func newGrepAccum(pattern string, caseInsensitive bool, maxResults int, outputMode string, contextLines int) (*grepAccum, error) {
	flags := ""
	if caseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	if maxResults <= 0 {
		maxResults = 100
	}
	if contextLines < 0 {
		contextLines = 0
	}
	return &grepAccum{
		re:           re,
		outputMode:   outputMode,
		maxResults:   maxResults,
		contextLines: contextLines,
		fileCounts:   map[string]int{},
		filesSeen:    map[string]struct{}{},
	}, nil
}

// feed scans one file's lines and records matches per output mode; it returns true
// when maxResults has been reached and the walk should stop.
//
// A relPath of "." means the search target was the file itself (the walk root),
// not a file found under a directory. Ripgrep omits the filename entirely when
// given a single explicit file argument, so content and count output do the
// same here; otherwise the tool's output would differ between environments with
// and without rg on PATH.
func (a *grepAccum) feed(relPath string, data []byte) (stop bool) {
	singleFile := relPath == "."
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if !a.re.MatchString(line) {
			continue
		}
		switch a.outputMode {
		case "files_with_matches":
			if _, seen := a.filesSeen[relPath]; !seen {
				a.filesSeen[relPath] = struct{}{}
				a.results = append(a.results, relPath)
				a.total++
				if a.total >= a.maxResults {
					return true
				}
			}
			return false // once recorded, move to the next file
		case "count":
			a.fileCounts[relPath]++
		default: // "content" or ""
			if a.contextLines > 0 {
				// Mirror rg's -C style: a "--" separator between match groups, the
				// match line itself using ":", and surrounding context lines using
				// "-" (both as the file/line separator), matched immediately below.
				if len(a.results) > 0 {
					a.results = append(a.results, "--")
				}
				lo, hi := i-a.contextLines, i+a.contextLines
				if lo < 0 {
					lo = 0
				}
				if hi >= len(lines) {
					hi = len(lines) - 1
				}
				for j := lo; j <= hi; j++ {
					sep := "-"
					if j == i {
						sep = ":"
					}
					if singleFile {
						a.results = append(a.results, fmt.Sprintf("%d%s%s", j+1, sep, lines[j]))
					} else {
						a.results = append(a.results, fmt.Sprintf("%s%s%d%s%s", relPath, sep, j+1, sep, lines[j]))
					}
				}
			} else if singleFile {
				a.results = append(a.results, fmt.Sprintf("%d:%s", i+1, line))
			} else {
				a.results = append(a.results, fmt.Sprintf("%s:%d:%s", relPath, i+1, line))
			}
			a.total++
			if a.total >= a.maxResults {
				return true
			}
		}
	}
	return false
}

// finish renders the accumulated results in the requested output mode.
func (a *grepAccum) finish() string {
	if a.outputMode == "count" {
		var countResults []string
		for file, cnt := range a.fileCounts {
			if file == "." {
				// Single explicit file target: rg prints the bare count.
				countResults = append(countResults, strconv.Itoa(cnt))
				continue
			}
			countResults = append(countResults, fmt.Sprintf("%s:%d", file, cnt))
		}
		sort.Strings(countResults)
		return strings.Join(countResults, "\n")
	}
	return strings.Join(a.results, "\n")
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
