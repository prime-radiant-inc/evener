package execenv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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
	re         *regexp.Regexp
	outputMode string
	maxResults int
	results    []string
	fileCounts map[string]int
	filesSeen  map[string]struct{}
	total      int
}

// newGrepAccum compiles the pattern (with optional case-insensitivity) and
// initializes the accumulator; maxResults defaults to 100 when non-positive.
func newGrepAccum(pattern string, caseInsensitive bool, maxResults int, outputMode string) (*grepAccum, error) {
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
	return &grepAccum{
		re:         re,
		outputMode: outputMode,
		maxResults: maxResults,
		fileCounts: map[string]int{},
		filesSeen:  map[string]struct{}{},
	}, nil
}

// feed scans one file's lines and records matches per output mode; it returns true
// when maxResults has been reached and the walk should stop.
func (a *grepAccum) feed(relPath string, data []byte) (stop bool) {
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
			a.results = append(a.results, fmt.Sprintf("%s:%d:%s", relPath, i+1, line))
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
			countResults = append(countResults, fmt.Sprintf("%s:%d", file, cnt))
		}
		sort.Strings(countResults)
		return strings.Join(countResults, "\n")
	}
	return strings.Join(a.results, "\n")
}

// sortPathsByMtimeDesc sorts paths newest-modification-first, ties broken by path.
// Shared by the off and sandboxed glob so their ordering is identical.
func sortPathsByMtimeDesc(paths []string) {
	sort.SliceStable(paths, func(i, j int) bool {
		fi, _ := os.Stat(paths[i])
		fj, _ := os.Stat(paths[j])
		if fi == nil || fj == nil {
			return paths[i] < paths[j]
		}
		if fi.ModTime() != fj.ModTime() {
			return fi.ModTime().After(fj.ModTime())
		}
		return paths[i] < paths[j]
	})
}
