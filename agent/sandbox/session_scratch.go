package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	sessionScratchTempDir      = os.TempDir
	sessionScratchUserCacheDir = os.UserCacheDir
	sessionScratchReadDir      = os.ReadDir
)

const (
	// sessionScratchPrefix reserves the children that Serf may remove from a
	// selected scratch base.
	sessionScratchPrefix = "serf-sandbox-"
	// sessionScratchLeaseName is held for the lifetime of a live scratch owner.
	sessionScratchLeaseName = ".serf-session.lock"
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
	first := requested
	if strings.TrimSpace(first) == "" {
		first = sessionScratchTempDir()
	}
	candidates := []string{first}
	if cache, err := sessionScratchUserCacheDir(); err == nil && strings.TrimSpace(cache) != "" {
		candidates = append(candidates, cache)
	}
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
		return canonical, nil
	}
	return "", fmt.Errorf("sandbox: no session scratch base outside workspace %q", workspaceRoot)
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

// sweepCrashedSessionScratch removes old Serf-owned children only when their
// lease is currently acquirable. Errors leave the candidate untouched.
func sweepCrashedSessionScratch(base string) {
	entries, err := sessionScratchReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-crashedSessionScratchMaxAge)
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
		_ = os.RemoveAll(dir)
	}
}
