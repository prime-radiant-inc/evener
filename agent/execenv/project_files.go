package execenv

import (
	"context"
	"errors"
)

const (
	defaultProjectFilesMaxDirListings      = 5_000
	defaultProjectFilesMaxDirEntries       = 20_000
	defaultProjectFilesMaxLiveEntries      = 50_000
	defaultProjectFilesMaxPaths            = 25_000
	defaultProjectFilesMaxIgnoreTotalBytes = 16 << 20
)

// ProjectFileEntry describes one discovered file or directory relative to the
// walk root using forward-slash separators.
type ProjectFileEntry struct {
	Path  string
	IsDir bool
}

// ProjectFilesLimits bounds an interactive project-file walk. Zero or negative
// values select the production defaults, and higher values clamp to those
// defaults so callers cannot widen hub walk bounds past safe ceilings.
type ProjectFilesLimits struct {
	MaxDirListings      int
	MaxDirEntries       int
	MaxLiveEntries      int
	MaxPaths            int
	MaxIgnoreTotalBytes int
}

// ProjectFilesOptions configures WalkProjectFiles.
type ProjectFilesOptions struct {
	IncludeDotfiles bool
	HomeDir         string
	IgnoreCache     *ProjectFilesIgnoreCache
	Limits          ProjectFilesLimits
}

// ProjectFilesResult holds the discovered entries and whether any budget or
// deadline truncated the walk before finishing.
type ProjectFilesResult struct {
	Entries   []ProjectFileEntry
	Truncated bool
}

// ProjectFilesIgnoreCache memoizes compiled .gitignore matchers across walks
// without exposing unexported execenv cache types.
type ProjectFilesIgnoreCache struct {
	cache *ignoreMatcherCache
}

// NewProjectFilesIgnoreCache constructs a reusable .gitignore matcher cache for
// WalkProjectFiles callers such as the hub file search service.
func NewProjectFilesIgnoreCache() *ProjectFilesIgnoreCache {
	return &ProjectFilesIgnoreCache{cache: newIgnoreMatcherCache()}
}

func clampProjectFilesLimit(v, ceiling int) int {
	if v <= 0 || v > ceiling {
		return ceiling
	}
	return v
}

func normalizeProjectFilesLimits(l ProjectFilesLimits) ProjectFilesLimits {
	return ProjectFilesLimits{
		MaxDirListings:      clampProjectFilesLimit(l.MaxDirListings, defaultProjectFilesMaxDirListings),
		MaxDirEntries:       clampProjectFilesLimit(l.MaxDirEntries, defaultProjectFilesMaxDirEntries),
		MaxLiveEntries:      clampProjectFilesLimit(l.MaxLiveEntries, defaultProjectFilesMaxLiveEntries),
		MaxPaths:            clampProjectFilesLimit(l.MaxPaths, defaultProjectFilesMaxPaths),
		MaxIgnoreTotalBytes: clampProjectFilesLimit(l.MaxIgnoreTotalBytes, defaultProjectFilesMaxIgnoreTotalBytes),
	}
}

// WalkProjectFiles performs a confined, breadth-first, .gitignore-pruned, and
// denylist-filtered walk of root for interactive file mention completion.
func WalkProjectFiles(ctx context.Context, root string, opts ProjectFilesOptions) (ProjectFilesResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ProjectFilesResult{Truncated: true}, nil
		}
		return ProjectFilesResult{}, err
	}
	opts.Limits = normalizeProjectFilesLimits(opts.Limits)
	budget := newProjectFilesBudget(opts.Limits)
	return walkProjectFiles(ctx, root, opts, budget)
}
