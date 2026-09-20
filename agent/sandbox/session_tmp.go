package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// sessionTmpLeafName is the subdirectory of a session temp container that is
	// handed to spawned commands as TMPDIR. Splitting the container in two is what
	// makes a privilege-dropping child usable WITHOUT letting it hold the lease:
	// the container is owner-controlled (so another uid cannot create, replace or
	// lock .evener-session.lock and defeat the crashed-scratch reclaim), and only
	// the leaf beneath it is world-writable.
	sessionTmpLeafName = "tmp"
	// sessionTmpContainerMode keeps the container traversable but neither listable
	// nor writable by anyone but its owner. An arbitrary uid needs o+x to reach the
	// world-writable leaf; it must not be able to create anything in the container
	// itself.
	sessionTmpContainerMode = 0o711
	// sessionTmpLeafMode is /tmp's own contract, and the leaf is deliberately a
	// tiny /tmp: world-writable with the sticky bit, so any uid may create its own
	// temp entries and no uid may remove or rename another's. os.ModeSticky (rather
	// than the raw 0o1000 bit, which Go's FileMode ignores) makes Mkdir/Chmod
	// actually pass S_ISVTX to the kernel.
	sessionTmpLeafMode = 0o777 | os.ModeSticky
)

// worldTempBases are the OS-aware, world-usable host temp bases a session temp
// container may be created in, in preference order. os.TempDir() is deliberately
// NOT among them: on macOS it is the per-user 0700 directory under
// /var/folders/..., which is precisely the unwritable temp this container
// replaces, and on Linux an ambient TMPDIR (systemd PrivateTmp, a container
// image, a user's ~/tmp) can be private for the same reason. A base must already
// exist and be a world-writable sticky directory to be selected.
var worldTempBases = []string{"/tmp", "/var/tmp"}

// SessionTmp is one session's temp container: an owner-controlled, world-
// traversable directory holding the liveness lease, and a sticky world-writable
// leaf inside it that a spawned command receives as TMPDIR so a descendant that
// drops privileges can still create temp files.
//
// Unlike SessionScratch it carries no retention pin and never participates in a
// scratch binding: it is disposable temp in a shared host namespace, not session
// data, so its lifecycle is "released at close, retired by the 24h sweep", not
// "pinned for the handoff".
type SessionTmp struct {
	// Dir is the leaf handed to spawned commands as TMPDIR.
	Dir string
	// container is the prefix-named directory the lease is held in and the sweep
	// reclaims as one unit.
	container string
	base      string
	lease     scratchLease
}

// NewSessionTmp creates a session temp container in the first world-usable host
// temp base that serves, holding a process-released lease until Retain or Remove.
//
// The container is created with MkdirTemp-style uniqueness rather than a
// predictable session-id path, so another user cannot pre-create (squat) the name,
// and creation FAILS CLOSED when the directory it just made is not owned by this
// process. Every failure disposes what it created, so a failed call leaks neither
// a directory nor a lease.
func NewSessionTmp() (*SessionTmp, error) {
	base, err := worldUsableTempBase()
	if err != nil {
		return nil, err
	}
	container, err := os.MkdirTemp(base, sessionScratchPrefix+"*")
	if err != nil {
		return nil, fmt.Errorf("sandbox: create session temp container: %w", err)
	}
	dispose := func(cause error) (*SessionTmp, error) {
		_ = os.RemoveAll(container)
		return nil, cause
	}
	if err := os.Chmod(container, sessionTmpContainerMode); err != nil {
		return dispose(fmt.Errorf("sandbox: open session temp container: %w", err))
	}
	owned, err := scratchEntryOwnedByProcess(container)
	if err != nil {
		return dispose(fmt.Errorf("sandbox: verify session temp container ownership: %w", err))
	}
	if !owned {
		return dispose(fmt.Errorf("sandbox: session temp container %q is not owned by this process", container))
	}
	leaf := filepath.Join(container, sessionTmpLeafName)
	if err := os.Mkdir(leaf, sessionTmpLeafMode); err != nil {
		return dispose(fmt.Errorf("sandbox: create session temp leaf: %w", err))
	}
	// Mkdir's mode is masked by the process umask, so the world-writable sticky
	// leaf is set explicitly afterwards: an inherited umask that hid o+w would
	// hand a privilege-dropping child exactly the unwritable temp this container
	// exists to replace.
	if err := os.Chmod(leaf, sessionTmpLeafMode); err != nil {
		return dispose(fmt.Errorf("sandbox: open session temp leaf: %w", err))
	}
	lease, contended, err := acquireScratchLease(filepath.Join(container, sessionScratchLeaseName))
	if err != nil {
		return dispose(fmt.Errorf("sandbox: acquire session temp container lease: %w", err))
	}
	if contended {
		return dispose(errors.New("sandbox: new session temp container lease is already held"))
	}
	return &SessionTmp{Dir: leaf, container: container, base: base, lease: lease}, nil
}

// worldUsableTempBase returns the canonical first world-usable host temp base, or
// an error when none of them serves.
func worldUsableTempBase() (string, error) {
	for _, candidate := range worldTempBases {
		if base, ok := validWorldTempBase(candidate); ok {
			return base, nil
		}
	}
	return "", fmt.Errorf("sandbox: no world-usable host temp base among %s", strings.Join(worldTempBases, ", "))
}

// validWorldTempBase reports whether candidate is a directory any local user can
// create in — the /tmp contract: an existing directory, world-writable, and
// sticky so another user cannot remove what we put there.
func validWorldTempBase(candidate string) (string, bool) {
	if strings.TrimSpace(candidate) == "" {
		return "", false
	}
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return "", false
	}
	if info.Mode().Perm()&0o002 == 0 || info.Mode()&os.ModeSticky == 0 {
		return "", false
	}
	canonical, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false
	}
	return canonical, true
}

// Retain releases the container's liveness lease without removing the directory,
// the same handoff discipline a session scratch follows. A released container is
// eligible for the 24h crashed-scratch reclaim instead of holding its lease for
// the rest of the daemon's uptime.
func (t *SessionTmp) Retain() error {
	if t == nil || t.lease == nil {
		return nil
	}
	err := t.lease.Release()
	t.lease = nil
	return err
}

// HasLease reports whether this container still owns its live lease.
func (t *SessionTmp) HasLease() bool {
	return t != nil && t.lease != nil
}

// Remove releases the lease and attempts to remove the container's top level. It
// refuses a path outside the session scratch namespace, exactly as
// SessionScratch.Cleanup does, so it can never remove a directory Evener did not
// allocate.
//
// Removal is best-effort by construction, and the caller is told so by the
// returned error: the leaf is world-writable, so another uid may have planted a
// nested subtree this process cannot unlink. Releasing the lease first leaves
// such a container to the 24h crashed-scratch sweep, which retries under the same
// discipline — no worse than raw /tmp, where nothing reclaims a foreign nested
// tree either.
func (t *SessionTmp) Remove() error {
	if t == nil || t.container == "" {
		return nil
	}
	container := filepath.Clean(t.container)
	base := filepath.Clean(t.base)
	if t.base == "" || filepath.Dir(container) != base ||
		!strings.HasPrefix(filepath.Base(container), sessionScratchPrefix) {
		return fmt.Errorf("sandbox: refuse session temp removal outside the session scratch namespace: %q", t.container)
	}
	releaseErr := t.Retain()
	return errors.Join(releaseErr, os.RemoveAll(container))
}
