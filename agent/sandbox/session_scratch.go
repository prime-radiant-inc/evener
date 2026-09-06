package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	sessionScratchTempDir      = os.TempDir
	sessionScratchUserCacheDir = os.UserCacheDir
	sessionScratchReadDir      = os.ReadDir
)

const (
	// sessionScratchPrefix reserves the children that Evener may remove from a
	// selected scratch base.
	sessionScratchPrefix = "evener-sandbox-"
	// sessionScratchLeaseName is held for the lifetime of a live scratch owner.
	sessionScratchLeaseName = ".evener-session.lock"
)

var crashedSessionScratchMaxAge = 24 * time.Hour

type scratchLease interface {
	Release() error
}

// SessionScratch is one live session's private scratch directory.
type SessionScratch struct {
	Dir   string
	base  string
	lease scratchLease
}

// NewSessionScratch creates a private directory outside workspaceRoot and holds
// a process-released lease until Retain or Cleanup. Candidate bases must already
// exist.
func NewSessionScratch(base, workspaceRoot string) (*SessionScratch, error) {
	canonicalWorkspace, err := canonicalScratchRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	cleanBase, err := sessionScratchBase(base, canonicalWorkspace)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(cleanBase, sessionScratchPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create session scratch: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox: secure session scratch: %w", err)
	}
	lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sandbox: acquire session scratch lease: %w", err)
	}
	if contended {
		_ = os.RemoveAll(dir)
		return nil, errors.New("sandbox: new session scratch lease is already held")
	}
	return &SessionScratch{Dir: dir, base: cleanBase, lease: lease}, nil
}

// Retain releases the live-session lease without removing the directory. This
// is the normal session-teardown operation: the absolute path is handed to the
// parent and cleanup remains a manual decision.
func (s *SessionScratch) Retain() error {
	if s == nil || s.lease == nil {
		return nil
	}
	err := s.lease.Release()
	s.lease = nil
	return err
}

func canonicalScratchRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve workspace root: %w", err)
	}
	return canonical, nil
}

func sessionScratchBase(requested, workspaceRoot string) (string, error) {
	bases := sessionScratchBases(requested, workspaceRoot)
	if len(bases) == 0 {
		return "", noSessionScratchBaseError(workspaceRoot)
	}
	return bases[0], nil
}

// sessionScratchBases lists, in allocation-preference order, every base a
// session on this workspace may end up in: the requested base (or the temp dir)
// and the user cache dir, each canonical, keeping only the ones that exist as a
// directory outside the workspace. Allocation takes the first; a reclaim has to
// visit them all, because a workspace that contains the temp dir sends its
// sessions to the cache dir instead.
func sessionScratchBases(requested, workspaceRoot string) []string {
	first := requested
	if strings.TrimSpace(first) == "" {
		first = sessionScratchTempDir()
	}
	candidates := []string{first}
	if cache, err := sessionScratchUserCacheDir(); err == nil && strings.TrimSpace(cache) != "" {
		candidates = append(candidates, cache)
	}
	var bases []string
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil || pathWithin(absolute, workspaceRoot) {
			continue
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			continue
		}
		canonical, err := filepath.EvalSymlinks(absolute)
		if err != nil || pathWithin(canonical, workspaceRoot) {
			continue
		}
		if !slices.Contains(bases, canonical) {
			bases = append(bases, canonical)
		}
	}
	return bases
}

func noSessionScratchBaseError(workspaceRoot string) error {
	return fmt.Errorf("sandbox: no session scratch base outside workspace %q", workspaceRoot)
}

func pathWithin(path, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Cleanup releases the liveness lease and removes only the exact, prefix-owned
// child recorded by the allocator.
func (s *SessionScratch) Cleanup() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	dir := filepath.Clean(s.Dir)
	base := filepath.Clean(s.base)
	if s.base == "" || filepath.Dir(dir) != base ||
		!strings.HasPrefix(filepath.Base(dir), sessionScratchPrefix) {
		return fmt.Errorf("sandbox: refuse cleanup outside session scratch namespace: %q", s.Dir)
	}
	releaseErr := s.Retain()
	return errors.Join(releaseErr, os.RemoveAll(dir))
}

// SweepCrashedSessionScratch reclaims the session scratch directories left in
// every base a session on workspaceRoot may allocate from. A session releases
// its lease and keeps its directory at close and on handoff, so nothing else
// ever removes those: this is what makes retention safe rather than a permanent
// leak. It sweeps all the allocation bases rather than the one this workspace
// would pick, because a workspace containing the temp dir allocates from the
// cache dir instead, and it skips a base inside workspaceRoot for the same
// reason allocation refuses one: nothing Evener owns is ever written there. It
// reports only the failures an operator can act on — an unreadable base, a
// directory it owned but could not remove — so it is best called once at
// process start, off the startup path.
func SweepCrashedSessionScratch(workspaceRoot string) error {
	canonicalWorkspace, err := canonicalScratchRoot(workspaceRoot)
	if err != nil {
		return err
	}
	bases := sessionScratchBases("", canonicalWorkspace)
	if len(bases) == 0 {
		return noSessionScratchBaseError(workspaceRoot)
	}
	var failures []error
	for _, base := range bases {
		if err := sweepCrashedSessionScratch(base); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// sweepCrashedSessionScratch removes old Evener-owned children only when their
// lease is currently acquirable. A candidate whose lease is held, or whose age
// cannot be read, is left untouched and is not an error: it is someone else's.
func sweepCrashedSessionScratch(base string) error {
	entries, err := sessionScratchReadDir(base)
	if err != nil {
		return fmt.Errorf("sandbox: read session scratch base %q: %w", base, err)
	}
	cutoff := time.Now().Add(-crashedSessionScratchMaxAge)
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), sessionScratchPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
		if err != nil || contended {
			continue
		}
		if err := lease.Release(); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			failures = append(failures, fmt.Errorf("sandbox: remove crashed session scratch %q: %w", dir, err))
		}
	}
	return errors.Join(failures...)
}
