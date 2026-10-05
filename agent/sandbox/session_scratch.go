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
	// sessionScratchRemoveTree removes a sweep tombstone. A test swaps it to model
	// a tree this process cannot remove, such as one another uid planted.
	sessionScratchRemoveTree = removeTree
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

// Retain releases the live-session lease without removing the directory, for a
// directory that may still be in use: a live ownership move, or a detached
// command's TMPDIR. The crashed-scratch sweep reclaims it once it is old.
func (s *SessionScratch) Retain() error {
	if s == nil || s.lease == nil {
		return nil
	}
	err := s.lease.Release()
	s.lease = nil
	return err
}

// HasLease reports whether this scratch still owns its live lease. A scratch
// whose lease was released by Retain keeps its directory; one released by
// Cleanup is gone.
func (s *SessionScratch) HasLease() bool {
	return s != nil && s.lease != nil
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

// sessionScratchBase returns the base a new session allocates in, and stops at
// the first one that serves: a session start must not wait on the cache
// filesystem, which may be slow or unavailable, once the temp dir has answered.
func sessionScratchBase(requested, workspaceRoot string) (string, error) {
	if base, ok := validSessionScratchBase(preferredSessionScratchCandidate(requested), workspaceRoot); ok {
		return base, nil
	}
	if cache, err := sessionScratchUserCacheDir(); err == nil {
		if base, ok := validSessionScratchBase(cache, workspaceRoot); ok {
			return base, nil
		}
	}
	return "", noSessionScratchBaseError(workspaceRoot)
}

// sessionScratchBases lists, in allocation-preference order, every base a
// session on this workspace may end up in: the requested base (or the temp dir)
// and the user cache dir, each canonical and listed once, plus every world-usable
// host temp base a session TEMP CONTAINER (session_tmp.go) may have been created
// in. Allocation stops at the first; a reclaim has to visit them all, because a
// workspace that contains the temp dir sends its own sessions to the cache dir
// instead, and a temp container deliberately does not use the temp dir at all
// (os.TempDir() is private on macOS and can be private on Linux).
//
// The workspace filter applies to the SCRATCH bases only. A container is minted
// in a world temp base regardless of where the workspace is — NewSessionTmp takes
// no workspace — so a base the filter would refuse for allocation (a workspace
// that CONTAINS /tmp, or is /tmp itself) still holds containers, and dropping it
// from this list would leave them unreclaimable forever.
//
// A malformed EVENER_HOST_TEMP_BASES is returned as the error beside the scratch
// bases, which are still listed: the world temp bases are then left out
// entirely rather than replaced by the defaults.
func sessionScratchBases(requested, workspaceRoot string) ([]string, error) {
	var bases []string
	add := func(base string) {
		if base != "" && !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	scratchCandidates := []string{preferredSessionScratchCandidate(requested)}
	if cache, err := sessionScratchUserCacheDir(); err == nil {
		scratchCandidates = append(scratchCandidates, cache)
	}
	for _, candidate := range scratchCandidates {
		base, ok := validSessionScratchBase(candidate, workspaceRoot)
		if ok {
			add(base)
		}
	}
	// Only where a session temp container can exist: elsewhere the bases are
	// meaningless names, and walking them would let the reclaim scan directories
	// evener never allocated in.
	if !SessionTmpSupported {
		return bases, nil
	}
	candidates, err := worldTempBaseCandidates()
	if err != nil {
		return bases, err
	}
	for _, candidate := range candidates {
		base, ok := validWorldTempBase(candidate)
		if ok {
			add(base)
		}
	}
	return bases, nil
}

// preferredSessionScratchCandidate is the base a caller asked for, or the temp
// dir when it asked for none.
func preferredSessionScratchCandidate(requested string) string {
	if strings.TrimSpace(requested) == "" {
		return sessionScratchTempDir()
	}
	return requested
}

// validSessionScratchBase reports the canonical form of candidate when Evener
// may keep session scratch there: an existing directory, outside the workspace
// both as named and as resolved.
func validSessionScratchBase(candidate, workspaceRoot string) (string, bool) {
	if strings.TrimSpace(candidate) == "" {
		return "", false
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil || pathWithin(absolute, workspaceRoot) {
		return "", false
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", false
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || pathWithin(canonical, workspaceRoot) {
		return "", false
	}
	return canonical, true
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
	inNamespace := strings.HasPrefix(filepath.Base(dir), sessionScratchPrefix) ||
		strings.HasPrefix(filepath.Base(base), sessionScratchTreePrefix)
	if s.base == "" || filepath.Dir(dir) != base || !inNamespace {
		return fmt.Errorf("sandbox: refuse cleanup outside session scratch namespace: %q", s.Dir)
	}
	releaseErr := s.Retain()
	removeErr := removeTree(dir)
	if s.Named() {
		removeEmptySessionScratchTree(base)
	}
	return errors.Join(releaseErr, removeErr)
}

// SweepCrashedSessionScratch reclaims the session scratch directories left in
// every base a session on workspaceRoot may allocate from: a session that
// crashed before removing its scratch, a detached command's TMPDIR container
// whose command has exited, and a directory a live ownership move released. It
// sweeps all the allocation bases rather than the one this workspace
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
	bases, basesErr := sessionScratchBases("", canonicalWorkspace)
	if len(bases) == 0 {
		return errors.Join(basesErr, noSessionScratchBaseError(workspaceRoot))
	}
	failures := []error{basesErr}
	for _, base := range bases {
		if err := sweepCrashedSessionScratch(base); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// crashedSessionScratchTombstoneSuffix marks the dot-prefixed tombstone a
// sweeper renames a candidate to before removing it.
const crashedSessionScratchTombstoneSuffix = ".reclaiming"

// crashedSessionScratchTombstone names the tombstone dir is renamed to
// before its removal. The dot prefix hides it from the sweep's candidate
// filter — enumerated by nobody, removed exactly once — and reclaims it
// under the same ownership, age, and lease gates.
func crashedSessionScratchTombstone(dir string) string {
	return filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+crashedSessionScratchTombstoneSuffix)
}

// isCrashedSessionScratchTombstone reports whether a base entry is the
// tombstone a sweeper renames a candidate to before removing it, so a sweep
// that crashed between the rename and the removal can be cleaned up by a
// later one.
func isCrashedSessionScratchTombstone(name string) bool {
	return strings.HasPrefix(name, "."+sessionScratchPrefix) && strings.HasSuffix(name, crashedSessionScratchTombstoneSuffix)
}

// sweepCrashedSessionScratch removes old Evener-owned children only when their
// lease is currently acquirable. A candidate whose lease is held, or whose age
// cannot be read, is left untouched and is not an error: it is someone else's.
// Its identity is verified after the lease is acquired, and it is renamed to a
// tombstone before its removal.
func sweepCrashedSessionScratch(base string) error {
	entries, err := sessionScratchReadDir(base)
	if err != nil {
		return fmt.Errorf("sandbox: read session scratch base %q: %w", base, err)
	}
	cutoff := time.Now().Add(-crashedSessionScratchMaxAge)
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isCrashedSessionScratchTombstone(entry.Name()) {
			// A sweeper that crashed between its rename and the removal left this
			// behind, where the candidate filter can never enumerate it again.
			// Reclaim it under the same age, ownership, and lease gates — the
			// lease is what excludes a live remover mid-removal, whose held flock
			// a crashed one released. No second rename: a removal that fails
			// leaves the same tombstone for the next sweep to retry.
			info, infoErr := entry.Info()
			if infoErr != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			tombstone := filepath.Join(base, entry.Name())
			owned, ownerErr := scratchEntryOwnedByProcess(tombstone)
			if ownerErr != nil || !owned {
				continue
			}
			lease, contended, leaseErr := acquireScratchLease(filepath.Join(tombstone, sessionScratchLeaseName))
			if leaseErr != nil || contended {
				continue
			}
			if removeErr := sessionScratchRemoveTree(tombstone); removeErr != nil {
				failures = append(failures, fmt.Errorf("sandbox: remove crashed session scratch tombstone %q: %w", tombstone, removeErr))
			}
			_ = lease.Release()
			continue
		}
		if !strings.HasPrefix(entry.Name(), sessionScratchPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		dir := filepath.Join(base, entry.Name())
		before, statErr := os.Stat(dir)
		if statErr != nil {
			continue
		}
		// Never touch a candidate this process does not own. The sweep walks
		// world-writable temp bases now, where any local user can plant a directory
		// under our prefix; acquiring our lease inside someone else's directory and
		// then removing it recursively would destroy files that are not ours.
		owned, ownerErr := scratchEntryOwnedByProcess(dir)
		if ownerErr != nil || !owned {
			continue
		}
		lease, contended, err := acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
		if err != nil || contended {
			continue
		}
		after, statErr := os.Stat(dir)
		if statErr != nil || !os.SameFile(before, after) {
			_ = lease.Release()
			continue
		}
		// Re-verify under the held lease: acquiring it creates a file in the
		// candidate, so a directory whose ownership changed between the two checks
		// must not be removed either.
		if owned, ownerErr := scratchEntryOwnedByProcess(dir); ownerErr != nil || !owned {
			_ = lease.Release()
			continue
		}
		// Rename before removing: the rename is atomic and size-independent, and
		// the dot-prefixed tombstone fails the sweep's own prefix filter, so a
		// sweep that crashes mid-removal leaves something only the tombstone
		// branch above reclaims. The directory lease is held through the removal.
		tombstone := crashedSessionScratchTombstone(dir)
		if renameErr := os.Rename(dir, tombstone); renameErr != nil {
			_ = lease.Release()
			// A candidate that vanished mid-sweep is not a failure; anything else
			// is, and the directory stays for a later sweep to retry.
			if !os.IsNotExist(renameErr) {
				failures = append(failures, fmt.Errorf("sandbox: rename crashed session scratch %q for reclamation: %w", dir, renameErr))
			}
			continue
		}
		if err := sessionScratchRemoveTree(tombstone); err != nil {
			// A failed removal must not leave the candidate renamed: an
			// unremovable directory stays at its original path, where the
			// operator expects it and a later sweep retries it. Only a
			// rename-back that itself fails leaves a tombstone, reported too.
			failures = append(failures, fmt.Errorf("sandbox: remove crashed session scratch %q: %w", dir, err))
			if backErr := os.Rename(tombstone, dir); backErr != nil {
				failures = append(failures, fmt.Errorf("sandbox: restore unremoved crashed session scratch %q from %q: %w", dir, tombstone, backErr))
			}
		}
		_ = lease.Release()
	}
	return errors.Join(failures...)
}
