package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// HasLease reports whether this scratch still owns its live lease. A scratch
// whose lease was released (Retain/Cleanup) keeps its directory but can no
// longer be pinned.
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

// scratchSweepBeforeRemove is a nil-in-production test seam fired while the
// sweep holds the candidate's lease, the reclamation mutex, and — when the
// candidate carries a pin — the pin owner's manifest lock, after the retention
// check read the directory collectible and just before the invalidating rename.
// Tests use it to run a concurrent manifest reset inside that window.
var scratchSweepBeforeRemove func()

// scratchResetBeforeReclaimLock fires just before the manifest reset attempts
// the reclamation lock, inside the retry closure and ahead of the lock's
// blocking section. Tests use it to synchronize the reset's arrival at the
// lock while the sweep holds it, instead of sleeping and hoping the goroutine
// reached the window in time.
var scratchResetBeforeReclaimLock func()

// scratchReclamationMu serializes this process's scratch reclamation with its
// manifest reset. The sweep's retention check reads the Released tombstone
// without any lock the reset's resurrection takes, and the reset's carry pass
// reclaims rows without taking the directory lease (round 25's contract
// leaves contended pins untouched), so without serialization a reset could
// carry a directory's rows into an unreleased manifest between the sweep's
// check and its removal — the sweep would then delete the scratch the
// resurrected manifest names. Both orders are safe under the mutex: a reset
// that runs first leaves !Released for the sweep's check to read, and one
// that comes second finds the directory gone and its pair dies with the
// tombstone. Every lock each side takes besides this one is fail-fast, so
// neither holder blocks on anything while holding it. Resets in OTHER
// processes sharing the state directory serialize through the pin owner's
// manifest lock instead, which the sweep holds across the same window
// (round 67): the mutex covers this process, the durable lock covers the
// rest, and both use the same both-orders-safe argument.
var scratchReclamationMu sync.Mutex

// sweepCrashedSessionScratch removes old Evener-owned children only when their
// lease is currently acquirable. A candidate whose lease is held, or whose age
// cannot be read, is left untouched and is not an error: it is someone else's.
// A candidate carrying a retention pin is skipped while that owner's manifest is
// unreleased, held through the retention check and the invalidating rename so a
// concurrent same-path restore cannot interleave, and its identity is verified
// after the lease is acquired. The renamed tombstone is then removed outside
// every lock. A malformed or conflicting pin is conservatively retained with a
// bounded diagnostic.
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
		// Cross-process serialization (round 67): the reclamation mutex below
		// is process-local, but resets run in every process sharing this state
		// directory, and ScratchDirectoryRetained reads a RELEASED manifest's
		// pinned directory collectible — so another process's reset could carry
		// the reference on its contended branch and commit an unreleased
		// manifest naming this very directory while the sweep holds its lease
		// mid-removal. The pin owner's manifest lock is the mutex's durable
		// equivalent: the reset already runs under it, so a sweep holding it
		// across the check and the invalidating rename excludes every process's
		// reset, and a reset that went first leaves !Released for the check to
		// read. The acquisition is fail-fast — the sweep never blocks holding
		// the directory lease; contention skips the candidate for a later sweep
		// to retry. A candidate with no pin needs no lock: every carry branch
		// dies on the absent pin, and writers refuse a released manifest, so
		// nothing can begin carrying for it while the removal runs.
		var manifestLock scratchLease
		if pin, pinErr := readScratchDirectoryPin(dir); pinErr == nil {
			lock, lockErr := acquireScratchRetentionLock(pin.Owner)
			if lockErr != nil {
				if !errors.Is(lockErr, ErrScratchRetentionLockHeld) {
					failures = append(failures, lockErr)
				}
				_ = lease.Release()
				continue
			}
			manifestLock = lock
		}
		scratchReclamationMu.Lock()
		retain, retentionErr := ScratchDirectoryRetained(dir)
		if retentionErr != nil {
			scratchReclamationMu.Unlock()
			if manifestLock != nil {
				_ = manifestLock.Release()
			}
			failures = append(failures, retentionErr)
			_ = lease.Release()
			continue
		}
		if retain {
			scratchReclamationMu.Unlock()
			if manifestLock != nil {
				_ = manifestLock.Release()
			}
			_ = lease.Release()
			continue
		}
		if scratchSweepBeforeRemove != nil {
			scratchSweepBeforeRemove()
		}
		// The invalidating rename is the destructive step, and it is atomic
		// and size-independent: the live path dies while every lock is still
		// held, and no same-path restore can ever re-create it, because
		// MkdirTemp mints unique names. The dot-prefixed tombstone fails the
		// sweep's own prefix filter, so nothing enumerates it and it is
		// removed exactly once. After the rename the locks guard nothing —
		// the removal of the dead tombstone runs outside all of them, so the
		// manifest lock is never held across work whose duration scales with
		// the directory's contents (round 71): every concurrent writer of
		// this root contends only with the millisecond-scale checks and the
		// rename itself.
		tombstone := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+".reclaiming")
		renameErr := os.Rename(dir, tombstone)
		scratchReclamationMu.Unlock()
		if manifestLock != nil {
			_ = manifestLock.Release()
		}
		_ = lease.Release()
		if renameErr != nil {
			// A candidate that vanished mid-sweep mirrors the removal's
			// IsNotExist tolerance; anything else is a real failure and the
			// directory stays for a later sweep to retry.
			if !os.IsNotExist(renameErr) {
				failures = append(failures, fmt.Errorf("sandbox: rename crashed session scratch %q for reclamation: %w", dir, renameErr))
			}
			continue
		}
		if err := os.RemoveAll(tombstone); err != nil {
			// A failed removal must not leave the candidate renamed: the
			// sweep's contract with an unremovable directory is to leave it
			// at its original path — where the operator expects it and a
			// later sweep retries it — while a leaked dot-prefixed tombstone
			// would be invisible to every later sweep and would wedge the
			// base's own cleanup. Rename it back; only a rename-back that
			// itself fails leaves a tombstone behind, and that residual is
			// reported alongside.
			failures = append(failures, fmt.Errorf("sandbox: remove crashed session scratch %q: %w", dir, err))
			if backErr := os.Rename(tombstone, dir); backErr != nil {
				failures = append(failures, fmt.Errorf("sandbox: restore unremoved crashed session scratch %q from %q: %w", dir, tombstone, backErr))
			}
		}
	}
	return errors.Join(failures...)
}
