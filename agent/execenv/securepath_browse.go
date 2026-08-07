package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/sys/unix"
)

var (
	secureBrowseWalkDir  = fs.WalkDir
	secureBrowseReadFile = fs.ReadFile
)

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

// secureDirFS is an fs.FS rooted at a base directory fd that refuses symlink
// traversal and cannot escape the base: every Open/ReadDir/Stat resolves beneath
// baseFd with the same symlink-refusing primitive the file tools use. It backs
// glob and the native grep walk so neither can be steered out of policy by a
// symlinked entry mid-walk. basePath is the canonical base, used to skip masked
// subtrees.
type secureDirFS struct {
	baseFd   int
	basePath string
	fs       *sandboxFS
}

func (f *secureDirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (f *secureDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: toFsErr(err)}
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	return df.ReadDir(-1)
}

func (f *secureDirFS) Stat(name string) (fs.FileInfo, error) {
	fd, err := openBeneathRoot(f.baseFd, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: toFsErr(err)}
	}
	df := os.NewFile(uintptr(fd), name)
	defer func() { _ = df.Close() }()
	return df.Stat()
}

// toFsErr maps a symlink-refusal or root-escape from the resolver to
// fs.ErrNotExist so glob/WalkDir treat a symlinked/escaping entry as absent
// (invisible) rather than surfacing a raw errno.
func toFsErr(err error) error {
	switch {
	case errors.Is(err, errSymlinkComponent), errors.Is(err, errEscapesRoot),
		errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return fs.ErrNotExist
	default:
		return err
	}
}

// glob resolves matches for pattern beneath a policy-checked base directory,
// refusing symlink traversal (so a pattern cannot escape through a symlink) and
// dropping any match under a masked path. Results are absolute and sorted like the
// off path (newest mtime first, ties by path).
func (s *sandboxFS) glob(tool, base, pattern string) ([]string, error) {
	patterns, err := expandSearchPattern(pattern)
	if err != nil {
		return nil, err
	}
	baseFd, canonical, err := s.openReadBaseFd(tool, base)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(baseFd) }()

	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s}
	seen := make(map[string]struct{})
	var abs []string
	for _, pattern := range patterns {
		matches, err := doublestar.Glob(fsys, pattern)
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			p := filepath.Join(canonical, m)
			if s.underMasked(p) {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			abs = append(abs, p)
		}
	}
	sortPathsByMtimeDesc(abs)
	return abs, nil
}

// grepNative runs the native (ripgrep-absent) grep beneath a policy-checked base,
// walking a secureDirFS so symlinks are never followed and refusing to descend
// into masked subtrees. The per-file matching/formatting is shared with the off
// path via grepAccum, so output semantics are identical.
func (s *sandboxFS) grepNative(pattern, base, globFilter string, caseInsensitive bool, maxResults int, outputMode string) (string, error) {
	globFilters, err := expandGrepFilter(globFilter)
	if err != nil {
		return "", err
	}
	baseFd, canonical, err := s.openReadBaseFd("grep", base)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(baseFd) }()

	a, err := newGrepAccum(pattern, caseInsensitive, maxResults, outputMode)
	if err != nil {
		return "", err
	}
	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: s}
	err = secureBrowseWalkDir(fsys, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil //nolint:nilerr // skip unreadable / symlink-refused entries and keep walking
		}
		abs := filepath.Join(canonical, rel)
		if s.underMasked(abs) {
			if d.IsDir() {
				return fs.SkipDir // never descend into a masked subtree
			}
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && rel != "." {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if len(globFilters) > 0 {
			matched, matchErr := matchesAnyGrepFilter(d.Name(), globFilters)
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		data, rerr := secureBrowseReadFile(fsys, rel)
		if rerr != nil {
			return nil //nolint:nilerr // best-effort grep: skip unreadable files
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		if a.feed(rel, data) {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return a.finish(), nil
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
