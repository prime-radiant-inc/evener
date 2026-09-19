package sandbox

import (
	"errors"
	"fmt"
	"io"
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
	sessionScratchRemoveAll    = os.RemoveAll
	// scratchWindowBeforeSample runs after the window has confirmed the container is
	// in an exported layout and before it samples that layout's mode. Tests use it to
	// interleave a second window.
	scratchWindowBeforeSample func(dir string)
	// scratchContainerChmod applies a mode to an open container handle. Tests use it to
	// observe the modes Evener sets, and to fail one.
	scratchContainerChmod = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
)

const (
	// sessionScratchPrefix reserves the children that Evener may remove from a
	// selected scratch base.
	sessionScratchPrefix = "evener-sandbox-"
	// sessionScratchLeaseName is held for the lifetime of a live scratch owner.
	sessionScratchLeaseName = ".evener-session.lock"
	// sessionScratchTmpName is the shared, sticky temp subdirectory inside a
	// session scratch. It — not the private scratch itself — is what $TMPDIR
	// names, because TMPDIR is inherited by every descendant, including one that
	// deliberately runs as another user (see sessionScratchTmpMode).
	sessionScratchTmpName = "tmp"
	// sessionScratchPrivateName is the agent's private subtree: the directory
	// $EVENER_SCRATCH_DIR names and the only place the session's own files (and
	// its redirected language caches) live. A 0711 scratch container cannot keep
	// known-name 0644 artifacts private, so those files go one level down, behind
	// a 0700 boundary (see sessionScratchPrivateMode).
	sessionScratchPrivateName = "private"
)

const (
	// sessionScratchDirMode is the scratch container's mode while the session is
	// LIVE: owner r-x, group and other TRAVERSE ONLY. Everyone can reach the
	// exported subtrees through it and NOBODY can create an entry in it — not
	// another user (no group/other write) and not a wrapperless command running as
	// the session's own user (no owner write either). That is what keeps a 0644
	// artifact from being dropped beside the subtrees where another local user
	// could read it by name (issue #495).
	sessionScratchDirMode = 0o511
	// sessionScratchRetainedDirMode is the container's mode once the session has
	// ended and the scratch is retained for handoff, inspection or a borrowing
	// consumer. A container that withholds owner write cannot be removed without an
	// intervening chmod, and retained-scratch cleanup is MANUAL, so the retained
	// state keeps the owner's write — the same mode a retained scratch had before
	// the live lockdown. No session command of this owner runs any more.
	sessionScratchRetainedDirMode = 0o711
	// sessionScratchSetupMode is the container's mode while Evener itself is
	// writing inside it: laying out the subtrees, migrating legacy entries,
	// acquiring the liveness lease, publishing or clearing the retention pin. It is
	// owner-only, so the window adds no exposure to any other user.
	sessionScratchSetupMode = 0o700
	// sessionScratchTmpMode is the shared temp subdirectory's mode: world-writable
	// with the sticky bit, exactly like /tmp. Any user may create a temp file
	// there and no user may remove or rename a file they do not own. It is the
	// only directory in a scratch that is writable by another user.
	sessionScratchTmpMode = os.ModeSticky | 0o777
	// sessionScratchPrivateMode is the agent's private subtree's mode: owner-only,
	// exactly as the whole scratch was before this split (issue #495). Nothing a
	// session writes — artifacts, caches, temp it chose to keep — is reachable by
	// another user, and the 0711 container cannot enumerate it or enter it.
	sessionScratchPrivateMode = 0o700
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
	// borrow is the shared claim a borrowing consumer holds instead of a lease: it
	// counts the consumers sharing one retained allocation so the container can stay
	// write-withheld for all of them and still return to the retained mode afterwards.
	borrow scratchLease
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
	if err := prepareSessionScratch(dir); err != nil {
		// A container that failed its layout is restored to an exported mode, and the live
		// mode withholds owner write — so the removal needs the same window Evener's own
		// disposal takes.
		_ = removeSessionScratchTree(dir)
		return nil, fmt.Errorf("sandbox: secure session scratch: %w", err)
	}
	lease, contended, acquireErr := acquireScratchLeaseInWindow(dir)
	if acquireErr != nil {
		_ = removeSessionScratchTree(dir)
		return nil, fmt.Errorf("sandbox: acquire session scratch lease: %w", acquireErr)
	}
	if contended {
		_ = removeSessionScratchTree(dir)
		return nil, errors.New("sandbox: new session scratch lease is already held")
	}
	return &SessionScratch{Dir: dir, base: cleanBase, lease: lease}, nil
}

// Retain releases the live-session lease without removing the directory. This
// is the normal session-teardown operation: the absolute path is handed to the
// parent and cleanup remains a manual decision. A borrowed handle releases its
// shared claim instead, and the same settle runs.
func (s *SessionScratch) Retain() error {
	if s == nil {
		return nil
	}
	if s.borrow != nil {
		err := s.borrow.Release()
		s.borrow = nil
		if s.Dir != "" {
			settleRetainedScratchMode(s.Dir)
		}
		return err
	}
	if s.lease == nil {
		return nil
	}
	err := s.lease.Release()
	s.lease = nil
	// The live mode withholds owner write, which would make a retained scratch
	// impossible to remove without an intervening chmod; retained cleanup is manual,
	// so the retained state keeps the owner's write. Best effort: the lease release
	// is the operation that must not be lost.
	if s.Dir != "" {
		settleRetainedScratchMode(s.Dir)
	}
	return err
}

// acquireScratchBorrowClaim registers one borrowing consumer on a scratch that is
// already in the exported layout. The claim is a shared lock on the container's private
// subtree, which every exported layout is required to have and which nothing migrates
// or replaces: the container's own handle carries the setup window's exclusive lock, so
// the two never collide, and the kernel counts the holders across processes.
func acquireScratchBorrowClaim(dir string) (scratchLease, error) {
	subtree, err := openScratchDirNoFollow(SessionScratchPrivateDir(dir))
	if err != nil {
		return nil, err
	}
	if err := lockScratchDirFile(subtree, true); err != nil {
		_ = subtree.Close()
		return nil, err
	}
	return &unixStyleScratchBorrow{file: subtree}, nil
}

// unixStyleScratchBorrow is one borrowing consumer's shared claim. Releasing it drops
// the count; the retained mode is settled by whoever finds itself the last holder.
type unixStyleScratchBorrow struct {
	file *os.File
}

func (b *unixStyleScratchBorrow) Release() error {
	unlockErr := unlockScratchDirFile(b.file)
	closeErr := b.file.Close()
	return errors.Join(unlockErr, closeErr)
}

// settleRetainedScratchMode puts a finished scratch back into the retained, owner-
// writable mode so the allocation stays removable by hand. A borrowing consumer still
// holds the scratch, though, and it needs the container write-withheld, so the retained
// mode is restored only by the last of them to finish — the exclusive probe answers
// "are there any left?" atomically, across processes. Best effort: teardown must not
// fail on a mode change.
//
// The borrower count alone is not enough, because a lease-owning live session holds no
// claim on the private subtree: its lock is on the lease file. Without the second probe a
// borrowing consumer that finished first would downgrade a live adopter's container to
// 0711 — owner-writable and other-traversable, so a 0644 artifact dropped beside the
// subtrees becomes readable by any local user who knows its name (issue #495).
func settleRetainedScratchMode(dir string) {
	probe, err := openScratchDirNoFollow(SessionScratchPrivateDir(dir))
	if err != nil {
		return
	}
	defer probe.Close() //nolint:errcheck // read-only directory handle
	last, err := tryLockScratchDirFile(probe)
	if err != nil || !last {
		return
	}
	defer func() { _ = unlockScratchDirFile(probe) }()
	_ = settleRetainedContainerMode(dir)
}

// settleRetainedContainerMode applies the retained mode only while no lease is held, and
// decides that under the same container lock a lease acquisition takes. Probing the lease
// outside that lock leaves a window: a restorer can acquire the lease between the probe and
// the chmod, and the container would then be relaxed to owner-writable while a live session
// owns it — re-enabling the direct container writes the live mode withholds (issue #495).
func settleRetainedContainerMode(dir string) error {
	container, err := openScratchDirNoFollow(dir)
	if err != nil {
		return err
	}
	defer container.Close() //nolint:errcheck // read-only directory handle
	if err := lockScratchDirFile(container, false); err != nil {
		return err
	}
	defer func() { _ = unlockScratchDirFile(container) }()
	if scratchLeaseHeld(dir) {
		return nil
	}
	return scratchContainerChmod(container, sessionScratchRetainedDirMode)
}

// scratchLeaseHeld reports whether any process still owns this scratch's live lease. The
// lease file is the only thing a lease-owning session holds — its borrower-style claim on
// the private subtree does not exist — so a settle that consulted the borrower count alone
// would see a live adopter as finished. Contention, and any other failure to take the
// lease, both read as "still owned", which keeps the container live rather than relaxing
// it.
var scratchLeaseHeld = func(dir string) bool {
	path := filepath.Join(dir, sessionScratchLeaseName)
	if _, err := os.Lstat(path); err != nil {
		// No lease file: no lease can be held here, so the mode may settle.
		return false
	}
	lease, _, err := acquireScratchLease(path)
	if err != nil {
		return true
	}
	_ = lease.Release()
	return false
}

// setScratchContainerMode applies an exported mode to a settled container. It takes the
// same exclusive lock as the setup window, so a mode change cannot land in the middle of
// one.
func setScratchContainerMode(dir string, mode os.FileMode) error {
	container, err := openScratchDirNoFollow(dir)
	if err != nil {
		return err
	}
	defer container.Close() //nolint:errcheck // read-only directory handle
	if err := lockScratchDirFile(container, false); err != nil {
		return err
	}
	defer func() { _ = unlockScratchDirFile(container) }()
	return scratchContainerChmod(container, mode)
}

// HasLease reports whether this scratch still owns its live lease. A scratch
// whose lease was released (Retain/Cleanup) keeps its directory but can no
// longer be pinned.
func (s *SessionScratch) HasLease() bool {
	return s != nil && s.lease != nil
}

// scratchSubtreeExists reports whether path is an existing directory — the test the
// layout migration uses, which does not depend on any container mode.
func scratchSubtreeExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// withWritableScratchContainer runs fn with the container temporarily at the
// owner-only setup mode, then restores the EXPORTED mode the container carried. It is
// the only way Evener writes inside a settled container: the live mode withholds
// write from everyone, so Evener's own metadata operations (lease, pin, layout) take
// the window, and during it no other user can write there either. A directory that is
// not one of ours in the exported layout (a legacy or foreign directory) is never
// chmod'ed; fn runs against it as it is.
//
// The window is serialized on the container itself, so no two of them overlap: an
// exclusive advisory lock is held across the layout check, the mode read, the chmod and
// fn. Without it two windows could both read the exported mode before either chmod'ed,
// and the loser would run fn against the write-withheld live mode and fail its own
// write with EACCES (a lease acquisition reporting contention as a hard failure, a pin
// that cannot be published or cleared).
//
// The restore target is never whatever mode the container happens to hold when the
// window closes: only the two exported modes qualify, so a setup mode a concurrent
// window is holding is refused rather than restored. A live container can therefore
// never be left at 0700 — which would leave a scratch that a privilege-dropping
// child's $TMPDIR could not reach and that its owner could write (issue #495).
func withWritableScratchContainer(dir string, fn func() error) error {
	container, err := openScratchDirNoFollow(dir)
	if err != nil {
		// Not a directory Evener can open: a legacy or foreign path is never chmod'ed, so
		// fn runs against it as it is. A directory in the exported layout is always
		// openable, so an open failure there is a real error.
		if validateSessionScratchLayout(dir) != nil {
			return fn()
		}
		return err
	}
	defer container.Close() //nolint:errcheck // read-only directory handle
	if err := lockScratchDirFile(container, false); err != nil {
		return err
	}
	defer func() { _ = unlockScratchDirFile(container) }()
	// The layout is judged only under the lock. Deciding it before the lock would let a
	// window that starts while another one is inside the setup mode run its callback
	// through the direct path and have that callback's write land after the first window
	// restored the live mode — the same EACCES the serialization exists to remove.
	if validateSessionScratchLayout(dir) != nil {
		return fn()
	}
	if hook := scratchWindowBeforeSample; hook != nil {
		hook(dir)
	}
	info, err := container.Stat()
	if err != nil {
		return err
	}
	original, exported := exportedScratchMode(info.Mode())
	if !exported {
		return fmt.Errorf("sandbox: session scratch %q is in mode %04o, not an exported mode", dir, info.Mode().Perm())
	}
	if err := scratchContainerChmod(container, sessionScratchSetupMode); err != nil {
		return err
	}
	fnErr := fn()
	return errors.Join(fnErr, scratchContainerChmod(container, original))
}

// exportedScratchMode reports the exported container mode a stat result describes.
// Only the two exported modes qualify: any other value is a legacy container (which
// never reaches the setup window) or the setup mode of a window that is already open.
// A platform that records no POSIX modes has no exported mode to preserve.
func exportedScratchMode(mode os.FileMode) (os.FileMode, bool) {
	if !scratchModesRecorded {
		return sessionScratchDirMode, true
	}
	switch perm := mode.Perm(); perm {
	case sessionScratchDirMode, sessionScratchRetainedDirMode:
		return perm, true
	default:
		return 0, false
	}
}

// acquireScratchLeaseInWindow takes the scratch's live lease inside the owner-only setup
// window, and guarantees that a failure AFTER the lease was taken does not leak it. The
// window can fail on its restore chmod once the callback has already opened the lease, and
// a caller that only inspects the window error would drop its handle: the lease stays held
// (an open descriptor with an exclusive flock) until the garbage collector finalizes it, so
// the directory reads as live and a retained scratch cannot be restored, reclaimed or
// settled for the life of the process.
func acquireScratchLeaseInWindow(dir string) (scratchLease, bool, error) {
	var lease scratchLease
	var contended bool
	err := withWritableScratchContainer(dir, func() error {
		var acquireErr error
		lease, contended, acquireErr = acquireScratchLease(filepath.Join(dir, sessionScratchLeaseName))
		return acquireErr
	})
	if err == nil || lease == nil {
		return lease, contended, err
	}
	releaseErr := lease.Release()
	return nil, contended, errors.Join(err, releaseErr)
}

// removeSessionScratchTree removes a whole scratch allocation. A live container
// withholds write even from its owner, so it is opened up first — otherwise the
// removal could not unlink what is inside it — and the tree is then removed.
func removeSessionScratchTree(dir string) error {
	if err := os.Chmod(dir, sessionScratchSetupMode); err != nil {
		return errors.Join(err, sessionScratchRemoveAll(dir))
	}
	return sessionScratchRemoveAll(dir)
}

// SessionScratchTmpDir returns the shared temp directory inside scratchDir that
// is exported to spawned processes as $TMPDIR, or "" for no scratch. It is a
// pure path function: callers that must ensure the directory exists (mint,
// restore) call prepareSessionScratch instead.
//
// TMPDIR must be this child and not the private subtree, because TMPDIR is
// inherited by every descendant — including one that drops privileges — and a
// 0700 directory is unusable to those descendants (issue #495). It must also be
// inside the scratch, because the scratch is the sandbox's granted writable temp
// root (the macOS Seatbelt platform defaults deliberately do not grant /tmp).
func SessionScratchTmpDir(scratchDir string) string {
	if strings.TrimSpace(scratchDir) == "" {
		return ""
	}
	return filepath.Join(scratchDir, sessionScratchTmpName)
}

// SessionScratchPrivateDir returns the agent's private subtree inside scratchDir
// — the directory $EVENER_SCRATCH_DIR names — or "" for no scratch. It is a pure
// path function like SessionScratchTmpDir.
func SessionScratchPrivateDir(scratchDir string) string {
	if strings.TrimSpace(scratchDir) == "" {
		return ""
	}
	return filepath.Join(scratchDir, sessionScratchPrivateName)
}

// SessionScratchWriteRoots returns the directories inside a scratch CONTAINER that
// a sandboxed process may write, in bind order: the private subtree (the agent's
// files) and the shared temp subtree (world-writable and sticky). The container
// itself is deliberately NOT one of them — it exists only so the two subtrees are
// reachable, and a writable container would let a shell deposit a 0644 artifact
// beside them, readable by any local user who can reach the base (issue #495).
//
// It is a pure path function, so a caller whose scratchDir is not a session
// scratch (no subtrees under it) simply gets two paths that do not exist; the
// backends skip a bind target that is absent.
func SessionScratchWriteRoots(scratchDir string) []string {
	if strings.TrimSpace(scratchDir) == "" {
		return nil
	}
	return []string{
		SessionScratchPrivateDir(scratchDir),
		SessionScratchTmpDir(scratchDir),
	}
}

// prepareSessionScratchForWrapper puts the container directory the wrapper is given
// into the exported layout: the container traversable, the private subtree behind
// `0700`, the shared temp subtree `1777`+sticky, and any legacy direct entry
// migrated into the private subtree. A container that NewSessionScratch or a restore
// already laid out is untouched by it, so the wrapper pays nothing for the normal
// case; a directory that was not laid out is repaired rather than wrapped into a
// sandbox whose exported `$TMPDIR` its parent will not let a dropped-privilege child
// traverse.
//
// It refuses, rather than replaces, a non-directory entry at a subtree name: this
// runs on a directory the caller owns, which Evener has not laid out, so such an
// entry is the caller's and is never unlinked.
func prepareSessionScratchForWrapper(dir string) error {
	// A non-directory at a subtree name is NOT refused here. A caller-supplied directory
	// must keep every entry it has, and ensureScratchSubtrees refuses the non-directory
	// instead of replacing it; an Evener allocation is repaired by the migration, which
	// moves the squatter aside with freeReservedSubtreeNames and preserves it inside the
	// private subtree. Checking here would abort the restore of a retained legacy scratch
	// whose root still holds a file named `tmp` or `private`, because the wrapper is
	// rebuilt before the restore re-settles the layout.
	// Evener's own scratch is settled at the live mode, which nobody can write. A
	// directory the caller supplied that is NOT Evener's (a fixture, an operator's own
	// temp dir) is provisioned and migrated but keeps the mode it had: it is the
	// caller's directory, it is not an allocation Evener handed out, and changing its
	// mode would be Evener reaching outside its namespace. Such a directory is never a
	// live session scratch — the mint and restore paths both go through
	// prepareSessionScratch.
	if strings.HasPrefix(filepath.Base(filepath.Clean(dir)), sessionScratchPrefix) {
		return prepareSessionScratch(dir)
	}
	return provisionSessionScratch(dir)
}

// provisionSessionScratch puts dir into the exported layout while PRESERVING the
// container's own mode: it opens the owner-only setup window it needs, migrates and
// creates the subtrees, and restores exactly the mode it found.
func provisionSessionScratch(dir string) error {
	container, err := openScratchDirNoFollow(dir)
	if err != nil {
		return err
	}
	defer container.Close() //nolint:errcheck // read-only directory handle
	info, err := container.Stat()
	if err != nil {
		return err
	}
	original := info.Mode().Perm()
	if err := container.Chmod(sessionScratchSetupMode); err != nil {
		return err
	}
	if err := ensureScratchSubtrees(dir); err != nil {
		return errors.Join(err, container.Chmod(original))
	}
	return container.Chmod(original)
}

// ensureScratchSubtrees creates the two exported subtrees if they are absent, and
// refuses — never replaces — a non-directory entry at either name. It performs no
// migration: it is what a CALLER-supplied directory gets, so Evener never reorganizes
// files it did not put there. An Evener allocation migrates through
// layOutSessionScratch instead.
func ensureScratchSubtrees(dir string) error {
	for _, sub := range []struct {
		path string
		mode os.FileMode
	}{
		{SessionScratchPrivateDir(dir), sessionScratchPrivateMode},
		{SessionScratchTmpDir(dir), sessionScratchTmpMode},
	} {
		if info, err := os.Lstat(sub.path); err == nil && !info.IsDir() {
			return fmt.Errorf("sandbox: session scratch subtree %q must be a directory", sub.path)
		}
		if err := ensureScratchSubdir(sub.path, sub.mode); err != nil {
			return err
		}
	}
	return nil
}

// validateSessionScratchLayout reports whether dir is already in the exported
// layout: a container other users can traverse, with both subtrees present as
// directories. It mutates nothing, so it is safe on a lease-less borrow, and it is
// what keeps a legacy allocation that its owner has not migrated yet from being
// published to a sharing consumer as a scratch whose exported temp path is
// unreachable to a dropped-privilege child.
func validateSessionScratchLayout(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("sandbox: session scratch %q is not a directory", dir)
	}
	// The container must carry exactly the exported mode: traversal for other users,
	// no group/other read or write — AND the owner's own rwx, without which the session
	// itself could neither traverse nor write its scratch (a mode like 0001 satisfies
	// the "others may traverse" half while making the scratch unusable). Special bits
	// beyond the expected set are rejected for the same reason. A platform that does
	// not record POSIX modes records none of this, so the mode checks are skipped
	// there rather than refusing every borrow.
	if scratchModesRecorded {
		perm := info.Mode().Perm()
		// Live (0511) and retained (0711) are the two exported states; both let every
		// user traverse and withhold write from everyone but the owner, which is what
		// makes the exported subtrees reachable without exposing the container.
		if perm != sessionScratchDirMode && perm != sessionScratchRetainedDirMode {
			return fmt.Errorf("sandbox: session scratch %q has container mode %04o, want %04o (live) or %04o (retained)", dir, perm, sessionScratchDirMode, sessionScratchRetainedDirMode)
		}
		if special := info.Mode() & (os.ModeSticky | os.ModeSetuid | os.ModeSetgid); special != 0 {
			return fmt.Errorf("sandbox: session scratch %q carries unexpected special mode bits %v", dir, special)
		}
	}
	private, tmp := SessionScratchPrivateDir(dir), SessionScratchTmpDir(dir)
	privateInfo, err := os.Lstat(private)
	if err != nil || !privateInfo.IsDir() {
		return fmt.Errorf("sandbox: session scratch %q is not in the exported layout (private subtree %q)", dir, private)
	}
	tmpInfo, err := os.Lstat(tmp)
	if err != nil || !tmpInfo.IsDir() {
		return fmt.Errorf("sandbox: session scratch %q is not in the exported layout (temp subtree %q)", dir, tmp)
	}
	if scratchModesRecorded {
		if privateInfo.Mode().Perm() != sessionScratchPrivateMode {
			return fmt.Errorf("sandbox: session scratch %q has private subtree mode %04o, want %04o", dir, privateInfo.Mode().Perm(), sessionScratchPrivateMode)
		}
		if tmpInfo.Mode().Perm() != sessionScratchTmpMode.Perm() || tmpInfo.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("sandbox: session scratch %q has temp subtree mode %v, want %v", dir, tmpInfo.Mode(), sessionScratchTmpMode)
		}
	}
	return nil
}

// prepareSessionScratch puts an existing scratch allocation into the layout this
// session exports: the container traversable, the private subtree owner-only, the
// shared temp subtree world-writable and sticky. It is idempotent, and because it
// repairs every mode it is also the migration for a scratch minted before this
// layout existed (issue #495).
//
// The migration order is load-bearing. A legacy scratch is 0700 with the session's
// files directly at its root, several of them world-readable by their own mode. The
// private subtree is created first, those entries are MOVED into it, and only then
// is the container loosened to the live mode 0511 — so a legacy file is never reachable through a
// traversable container, not even transiently. Relocating (rather than refusing to
// loosen) is deliberate: a 0700 container would make the temp subtree unreachable
// to a privilege-dropping child, which is the failure this layout exists to fix.
//
// A container that is already settled at the live mode is a no-op. Every wrapper
// rebuild and re-root re-runs this, and taking the setup window for a container that
// needs nothing would transiently widen a settled scratch to 0700 — a
// privilege-dropping child's $TMPDIR lookup can lose that race for no gain. A failure
// always restores the exported mode the container carried (the live mode for a legacy
// container, which must never be left at 0700 either).
func prepareSessionScratch(dir string) error {
	container, err := openScratchDirNoFollow(dir)
	if err != nil {
		return err
	}
	defer container.Close() //nolint:errcheck // read-only directory handle
	if err := lockScratchDirFile(container, false); err != nil {
		return err
	}
	defer func() { _ = unlockScratchDirFile(container) }()
	info, err := container.Stat()
	if err != nil {
		return err
	}
	mode, exported := exportedScratchMode(info.Mode())
	if exported && mode == sessionScratchDirMode && validateSessionScratchLayout(dir) == nil {
		return nil
	}
	// A failed layout is restored to the exported mode the container carried. A LEGACY
	// container has none: it is 0700 with the session's files still at its root, some of
	// them world-readable, so publishing 0511 partway through a migration that stopped
	// would make whatever the relocation has not reached yet readable to every local user
	// who knows its name. It stays at the setup mode, and the caller removes it through
	// removeSessionScratchTree, which opens the container up first (issue #495).
	var restore os.FileMode = sessionScratchSetupMode
	if exported {
		restore = mode
	}
	// Evener's own setup needs write access the live mode deliberately withholds.
	if err := scratchContainerChmod(container, sessionScratchSetupMode); err != nil {
		return err
	}
	if err := layOutSessionScratch(dir); err != nil {
		return errors.Join(err, scratchContainerChmod(container, restore))
	}
	return scratchContainerChmod(container, sessionScratchDirMode)
}

// layOutSessionScratch performs the layout steps shared by the settling
// (prepareSessionScratch) and preserving (provisionSessionScratch) boundaries; the
// caller owns the container's mode, and must have opened a writable window.
func layOutSessionScratch(dir string) error {
	// The layout is judged by whether the exported subtrees are there, not by the
	// container's mode, which differs between "legacy 0700", "live 0511" and
	// "retained 0711".
	legacy := !scratchSubtreeExists(SessionScratchPrivateDir(dir)) || !scratchSubtreeExists(SessionScratchTmpDir(dir))
	// A pre-existing `tmp` that is not ALREADY the exported shared subtree keeps
	// whatever a previous layout left in it: its contents are hardened before the
	// 1777 mode publishes them, or a readable legacy artifact would become readable
	// to every local user by name.
	if tmpInfo, err := os.Lstat(SessionScratchTmpDir(dir)); err == nil && tmpInfo.IsDir() {
		if tmpInfo.Mode().Perm() != sessionScratchTmpMode.Perm() || tmpInfo.Mode()&os.ModeSticky == 0 {
			legacy = true
		}
	}
	// A legacy scratch can already hold an entry named `private` or `tmp`. Those
	// two names are Evener's now and are NOT relocated (the private subtree is the
	// relocation destination), so their old contents are hardened in place before
	// the exported modes — 0700 and, for `tmp`, 1777 — make them reachable.
	if legacy {
		if err := hardenScratchSubtreeContents(SessionScratchPrivateDir(dir)); err != nil {
			return err
		}
		if err := hardenScratchSubtreeContents(SessionScratchTmpDir(dir)); err != nil {
			return err
		}
	}
	// A legacy REGULAR FILE holding one of the subtree names is session data that
	// merely shares Evener's name. Move it aside under a free non-reserved name
	// before the subtree is created, so the relocation pass below preserves it
	// instead of ensureScratchSubdir unlinking it (issue #495).
	if err := freeReservedSubtreeNames(dir); err != nil {
		return err
	}
	if err := ensureScratchSubdir(SessionScratchPrivateDir(dir), sessionScratchPrivateMode); err != nil {
		return err
	}
	if err := relocateScratchRootEntries(dir); err != nil {
		return err
	}
	return ensureScratchSubdir(SessionScratchTmpDir(dir), sessionScratchTmpMode)
}

// sessionScratchLegacySuffix names a legacy file that had to be moved aside
// because it held a name Evener now reserves for an exported subtree.
const sessionScratchLegacySuffix = "legacy"

// freeReservedSubtreeNames moves ANY non-directory entry sitting at one of the two
// subtree names aside, inside the container, under a free non-reserved name — a
// regular file, a symlink or a FIFO all belong to the session that left them there,
// so none of them may be unlinked. Only a directory at such a name is left alone,
// because it is reused as the subtree itself.
func freeReservedSubtreeNames(dir string) error {
	for _, name := range scratchContainerSubtreeNames {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.IsDir() {
			// A directory at a subtree name is reused as that subtree, not moved.
			continue
		}
		dst, err := freeScratchEntryName(dir, name+"."+sessionScratchLegacySuffix)
		if err != nil {
			return err
		}
		if err := os.Rename(path, dst); err != nil {
			return err
		}
	}
	return nil
}

// hardenScratchSubtreeContents applies hardenRelocatedEntry to every child of dir,
// leaving dir's own mode to the exported layout. A missing dir, or one that is not
// a directory (ensureScratchSubdir replaces those), is not an error.
func hardenScratchSubtreeContents(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := sessionScratchReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := hardenRelocatedEntry(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// scratchContainerSubtreeNames are the two names Evener reserves for the exported
// subtrees inside a container.
var scratchContainerSubtreeNames = []string{
	sessionScratchPrivateName,
	sessionScratchTmpName,
}

// scratchContainerMetadataNames are the entries a container keeps at its own root
// and that are Evener's own state, never session data: the liveness lease and the
// retention pin. They are the only entries the migration never touches.
var scratchContainerMetadataNames = []string{
	sessionScratchLeaseName,
	scratchPinName,
}

// scratchContainerReservedNames is every direct entry a settled container keeps:
// the two subtrees and the two metadata files.
var scratchContainerReservedNames = append(
	slices.Clone(scratchContainerSubtreeNames), scratchContainerMetadataNames...)

// relocateScratchRootEntries moves every legacy session entry at the container's
// root into the private subtree and tightens it to owner-only there. It runs while
// the container is still owner-only, so neither the move nor the mode change is ever
// observable through a traversable container. Evener's own metadata (the lease and
// the pin) and the two subtree names are not relocated: the subtrees are Evener's,
// and a regular file that held one of those names was moved aside by
// freeReservedSubtreeNames before this runs.
func relocateScratchRootEntries(dir string) error {
	entries, err := sessionScratchReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	private := SessionScratchPrivateDir(dir)
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(scratchContainerMetadataNames, name) {
			continue
		}
		if slices.Contains(scratchContainerSubtreeNames, name) {
			continue
		}
		dst, err := freeScratchEntryName(private, name)
		if err != nil {
			return err
		}
		// os.Rename carries a symlink entry itself, never its target.
		if err := os.Rename(filepath.Join(dir, name), dst); err != nil {
			return err
		}
		if err := hardenRelocatedEntry(dst); err != nil {
			return err
		}
	}
	return nil
}

// freeScratchEntryName returns parent/name, or the first suffixed variant nothing
// occupies, so moving a legacy entry never clobbers a file the destination already
// holds.
func freeScratchEntryName(parent, name string) (string, error) {
	candidate := filepath.Join(parent, name)
	for i := 0; ; i++ {
		if i > 0 {
			candidate = filepath.Join(parent, fmt.Sprintf("%s.%d", name, i))
		}
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
		if i >= 1000 {
			return "", fmt.Errorf("sandbox: no free name to relocate scratch entry %q", name)
		}
	}
}

// hardenRelocatedEntry makes a relocated entry owner-only: directories 0700,
// regular files 0600 with the owner's execute bit preserved. Symbolic links are
// skipped, never followed — the walk uses Lstat semantics and os.Chmod follows a
// symlink, so chmod'ing one would reach outside the scratch. Anything else (a
// socket, fifo or device) is left alone.
func hardenRelocatedEntry(path string) error {
	return filepath.WalkDir(path, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		switch {
		case entry.IsDir():
			return os.Chmod(current, sessionScratchPrivateMode)
		case info.Mode().IsRegular():
			// A legacy artifact can be a HARD LINK to a file outside the scratch — a
			// worktree file, say — and a hard link shares its inode's mode, so
			// chmod'ing it here would tighten that outside file too. Break the link
			// first so the mode change lands on a private copy.
			if hardLinked(info) {
				// Breaking the link is best effort: the tightened mode is defense in
				// depth, and the private subtree's own 0700 already blocks every other
				// user. An artifact that cannot be copied (unreadable, or inside an
				// unreadable directory) is therefore left with its own mode rather than
				// failing the whole migration — which on the restore path would make a
				// retained scratch impossible to resume.
				_ = breakScratchHardLink(current, info)
				return nil
			}
			return os.Chmod(current, 0o600|info.Mode().Perm()&0o100)
		default:
			return nil
		}
	})
}

// hardLinked reports whether info describes a regular file with more than one hard
// link. A platform that does not expose the link count reports false.
func hardLinked(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	links, ok := linkCount(info)
	return ok && links > 1
}

// breakScratchHardLink replaces path with a copy of itself that has a fresh inode,
// so the tightened mode cannot reach the file's other links. The copy keeps the
// owner's execute bit and is staged beside the original before an atomic rename.
func breakScratchHardLink(path string, info os.FileInfo) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close() //nolint:errcheck // read-only handle
	staged, err := os.CreateTemp(filepath.Dir(path), ".evener-hardlink-*")
	if err != nil {
		return err
	}
	stagedName := staged.Name()
	discard := func(cause error) error {
		_ = staged.Close()
		_ = os.Remove(stagedName)
		return cause
	}
	if _, err := io.Copy(staged, src); err != nil {
		return discard(err)
	}
	// CreateTemp makes the copy 0600; the execute bit is what a relocation keeps.
	if err := staged.Chmod(0o600 | info.Mode().Perm()&0o100); err != nil {
		return discard(err)
	}
	if err := staged.Close(); err != nil {
		_ = os.Remove(stagedName)
		return err
	}
	if err := os.Rename(stagedName, path); err != nil {
		_ = os.Remove(stagedName)
		return err
	}
	return nil
}

// ensureScratchSubdir creates path as a directory with mode, or repairs the mode
// of an existing one. It never follows an entry at path: a session owns its
// scratch and can leave any entry behind there, so a planted symlink called
// "private" or "tmp" must be removed rather than chmod'ed through — following it
// would hand an arbitrary directory outside the scratch the exported mode.
//
// The mode is applied through an O_NOFOLLOW directory handle (fchmod), not by
// path, so a swap between the check and the mode change cannot redirect it
// either. Mkdir honours the umask, which is why the mode is always set
// explicitly rather than left to the creation mode.
func ensureScratchSubdir(path string, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() {
			// os.Remove unlinks a symlink itself, never its target.
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	subdir, err := openScratchDirNoFollow(path)
	if err != nil {
		return err
	}
	defer subdir.Close() //nolint:errcheck // read-only directory handle
	return subdir.Chmod(mode)
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
	explicit := strings.TrimSpace(requested) != ""
	preferred, preferredOK := validSessionScratchBase(preferredSessionScratchCandidate(requested), workspaceRoot)
	// A caller-named base is honoured exactly as asked, without consulting anything
	// else. A DEFAULTED base must satisfy both properties below (issue #495).
	if preferredOK && explicit {
		return preferred, nil
	}
	cacheBase, cacheOK := "", false
	if preferredOK && scratchBaseReachable(preferred) && scratchBaseUnteplaceable(preferred) {
		return preferred, nil
	}
	if cache, err := sessionScratchUserCacheDir(); err == nil {
		cacheBase, cacheOK = validSessionScratchBase(cache, workspaceRoot)
	}
	if cacheOK && scratchBaseReachable(cacheBase) && scratchBaseUnteplaceable(cacheBase) {
		return cacheBase, nil
	}
	// No candidate is both reachable and safe from replacement. Integrity is the one
	// that cannot be conceded: a base any other user can write into lets them replace
	// this session's container, and with it the sandbox's own grant target. Reachability
	// is what is given up here, because a host whose bases all sit under a private
	// ancestor (macOS's per-user temp) still has to start a session — the shared temp
	// subtree is then unreachable to another user, which is the pre-existing
	// limitation, never a silently-replaced container.
	if preferredOK && scratchBaseUnteplaceable(preferred) {
		return preferred, nil
	}
	if cacheOK && scratchBaseUnteplaceable(cacheBase) {
		return cacheBase, nil
	}
	return "", fmt.Errorf("sandbox: no session scratch base outside workspace %q that another user cannot replace", workspaceRoot)
}

// scratchBaseReachable reports whether every component of base — base itself and each
// ancestor up to the filesystem root — grants other-user execute permission. That is
// the whole kernel check for the path lookup a privilege-dropping child performs to
// reach `<base>/<scratch>/tmp`, so a base failing it leaves the shared temp subtree
// unreachable no matter what modes the scratch itself carries.
//
// Symlinks are resolved (the kernel walks the target's modes), and a component that
// cannot be stat'ed is treated as unreachable.
func scratchBaseReachable(base string) bool {
	reachable, _ := scratchBaseFacts(base)
	return reachable
}

// scratchBaseUnteplaceable reports whether base is somewhere no other user can delete
// or replace this session's container: no component of the path — base or any ancestor
// — may grant group or other write without also carrying the sticky bit. A
// world-writable, non-sticky ancestor is as dangerous as the base itself, since
// replacing an entry in it moves everything below.
func scratchBaseUnteplaceable(base string) bool {
	_, unteplaceable := scratchBaseFacts(base)
	return unteplaceable
}

// scratchBaseFacts walks base's whole ancestor chain once and reports the two
// properties the allocator reasons about: reachability (other-user traverse on every
// component) and unteplaceability (no group/other-writable component lacking the
// sticky bit, including "/" at 0755 and "/tmp" at 1777 as they are normally mode'd).
func scratchBaseFacts(base string) (reachable, unteplaceable bool) {
	reachable, unteplaceable = true, true
	for dir := filepath.Clean(base); ; {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return false, false
		}
		mode := info.Mode()
		if mode.Perm()&0o001 == 0 {
			reachable = false
		}
		// A platform that does not record POSIX modes cannot be judged by them; the
		// synthesized bits would mark every location replaceable and refuse to
		// allocate a scratch at all.
		if scratchModesRecorded && mode.Perm()&0o022 != 0 {
			// A writable component is safe only when nobody else can remove what is inside
			// it. The sticky bit stops another user from removing OUR entry only while they
			// do not own the DIRECTORY: a directory's owner may delete or rename anything in
			// it, sticky or not, so a sticky but foreign-owned base is exactly as
			// replaceable as a plain world-writable one — and replacing it moves the sandbox
			// bind and grant targets with it. Root (the trusted system owner of a shared
			// temp directory such as /tmp) is the exception.
			if mode&os.ModeSticky == 0 || !scratchComponentOwnerTrusted(info) {
				unteplaceable = false
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return reachable, unteplaceable
		}
		dir = parent
	}
}

// scratchComponentOwnerTrusted reports whether a writable path component's owner cannot
// be a different local user: this process owns it, or it is root's. A platform that does
// not report the owner is trusted, because no mode-based judgement is made there.
func scratchComponentOwnerTrusted(info os.FileInfo) bool {
	uid, ok := fileOwnerID(info)
	if !ok {
		return true
	}
	return uid == currentUserID() || uid == 0
}

// sessionScratchBases lists, in allocation-preference order, every base a
// session on this workspace may end up in: the requested base (or the temp dir)
// and the user cache dir, each canonical and listed once. Allocation stops at
// the first; a reclaim has to visit them all, because a workspace that contains
// the temp dir sends its own sessions to the cache dir instead.
func sessionScratchBases(requested, workspaceRoot string) []string {
	candidates := []string{preferredSessionScratchCandidate(requested)}
	if cache, err := sessionScratchUserCacheDir(); err == nil {
		candidates = append(candidates, cache)
	}
	var bases []string
	for _, candidate := range candidates {
		base, ok := validSessionScratchBase(candidate, workspaceRoot)
		if ok && !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	return bases
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
	removeErr := removeSessionScratchTree(dir)
	if removeErr != nil {
		// A privilege-dropping child can own a tree inside the shared temp subtree, and
		// this session is not its owner and cannot become root, so the tree cannot be
		// removed from here. RemoveAll has already taken everything it could; tighten
		// the container so no FURTHER foreign entry can be created in what is left, and
		// report the residue rather than pretend the scratch is gone.
		_ = os.Chmod(dir, sessionScratchPrivateMode)
	}
	return errors.Join(releaseErr, removeErr)
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
		// Only sweep somewhere this allocator would put a scratch: a base another user
		// can replace (group/other-writable without the sticky bit) lets them redirect
		// a name between our lease check and the removal, so we do not touch it at all.
		if !scratchBaseUnteplaceable(base) {
			continue
		}
		if err := sweepCrashedSessionScratch(base); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// sweepCrashedSessionScratch removes old Evener-owned children only when their
// lease is currently acquirable. A candidate whose lease is held, or whose age
// cannot be read, is left untouched and is not an error: it is someone else's.
// A candidate carrying a retention pin is skipped while that owner's manifest is
// unreleased, held through the retention check and any removal so a concurrent
// same-path restore cannot interleave, and its identity is verified after the
// lease is acquired. A malformed or conflicting pin is conservatively retained
// with a bounded diagnostic.
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
		lease, contended, leaseErr := acquireScratchLeaseInWindow(dir)
		if leaseErr != nil || contended {
			continue
		}
		after, statErr := os.Stat(dir)
		if statErr != nil || !os.SameFile(before, after) {
			_ = lease.Release()
			continue
		}
		retain, retentionErr := scratchDirectoryRetained(dir)
		if retentionErr != nil {
			failures = append(failures, retentionErr)
			_ = lease.Release()
			continue
		}
		if retain {
			_ = lease.Release()
			continue
		}
		// Hold the lease through removal: releasing first would let a same-path
		// restore acquire the lease and be deleted out from under it.
		if err := removeSessionScratchTree(dir); err != nil {
			// Foreign-owned residue inside the shared temp subtree cannot be removed
			// from here; tighten the container so it cannot grow further.
			_ = os.Chmod(dir, sessionScratchPrivateMode)
			failures = append(failures, fmt.Errorf("sandbox: remove crashed session scratch %q: %w", dir, err))
		}
		_ = lease.Release()
	}
	return errors.Join(failures...)
}
