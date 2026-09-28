//go:build linux || darwin

package execenv

// The process boundary is crash-fencing spec 08c §3's local ownership seam:
// "Boot kills residual group members only through an ownership-verifiable
// handle. It never kills on a group id alone: a recycled id can name unrelated
// work." This file is the boundary-enumeration seam §3 names "beside the
// existing process_signal_unix.go / detach_unix.go process-group seams, never
// the agent/envctx probe seam".
//
// A boundary is created BEFORE the spawn that populates it, and it is the
// pre-created half the caller persists in its pending-spawn intent (the
// controller side, hostops). What the kernel guarantees differs by platform,
// and the arms stay asymmetric because the kernels are (§3):
//
//   - Linux: the boundary is a cgroup v2 directory under a delegated subtree.
//     Membership is kernel-enforced: writing a pid into cgroup.procs is the
//     only way in, and no app-created marker file lives in cgroupfs. The spawn
//     enters the cgroup at clone time (CLONE_INTO_CGROUP), so a process can
//     never be a member before the boundary was created and can never leave
//     while it lives.
//   - Darwin: the boundary is the (process group id, session id) pair the
//     worker's setsid-detached launcher holds. There is no cgroup or
//     job-object primitive, so the pair is the boundary, "never a group id
//     alone" (§3).
//
// The boundary alone never authorizes a kill. §3 proves a process instance with
// a kernel-owned start token observed beside the nonce: the caller persists the
// launcher-observed (pid, start token) pair, and this seam signals only a
// member whose kernel start token still equals the persisted token. A pid whose
// token differs is a reused id naming a different process and reads as already
// clean; the seam refuses to signal it.
//
// How "every process in this boundary is dead" is proven: Await is the
// bounded proof. It re-enumerates membership until the caller's clean predicate
// holds or the wait expires; expiry is ErrBoundaryNotSettled, never a claim of
// cleanliness. The predicate is the caller's because §3's clean rule depends on
// the persisted pairs the caller holds (empty is clean; a start-token mismatch
// is clean; a member whose pid matches no persisted pair is live, never clean).
//
// The failure contract is fail-closed: every operation that cannot verify what
// it is asked to signal reports an error and signals nothing. A boundary that
// cannot be created (no writable cgroup2 subtree on Linux, a launcher that has
// not setsid'd on Darwin) is ErrBoundaryUnavailable. What such a failure means
// for the caller's startup is the caller's contract, never a boundary
// startup-refusal of its own.

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// boundaryPollInterval is how often AskDead re-enumerates membership while it
// waits. Enumeration is a small read; the interval only bounds how quickly a
// clean boundary is observed, never how long the wait itself may run.
const boundaryPollInterval = 10 * time.Millisecond

// BoundaryPlatform is the platform arm a boundary identity belongs to: the
// `kind` discriminator of §9's local-linux and local-darwin variants.
type BoundaryPlatform string

const (
	// BoundaryPlatformLinux is the cgroup arm: the boundary's ownership is the
	// kernel-enforced cgroup v2 membership (§3).
	BoundaryPlatformLinux BoundaryPlatform = "linux"
	// BoundaryPlatformDarwin is the (process group id, session id) arm (§3).
	BoundaryPlatformDarwin BoundaryPlatform = "darwin"
)

// BoundaryIdentity is the persisted ownership identity of one pre-created
// boundary: exactly the ownership data §9's local-linux, local-darwin and
// local-markerless variants carry (minus the nonce and the launcher-observed
// marker, which live beside it in the caller's intent).
type BoundaryIdentity struct {
	// Platform selects the arm and therefore which of the fields below is the
	// boundary.
	Platform BoundaryPlatform
	// CgroupID is the Linux arm's cgroup v2 directory path.
	CgroupID string
	// PGID and SessionID are the Darwin arm's pair.
	PGID      int
	SessionID int
}

// Validate checks the identity against the one-arm rule: exactly the fields the
// platform arm defines, never both arms and never neither.
func (id BoundaryIdentity) Validate() error {
	switch id.Platform {
	case BoundaryPlatformLinux:
		if id.CgroupID == "" {
			return errors.New("execenv: a linux boundary carries no cgroup id")
		}
		if id.PGID != 0 || id.SessionID != 0 {
			return errors.New("execenv: a linux boundary carries a darwin group id")
		}
	case BoundaryPlatformDarwin:
		if id.PGID <= 0 || id.SessionID <= 0 {
			return fmt.Errorf("execenv: a darwin boundary carries no (pgid, session id) pair: %d/%d", id.PGID, id.SessionID)
		}
		if id.CgroupID != "" {
			return errors.New("execenv: a darwin boundary carries a cgroup id")
		}
	default:
		return fmt.Errorf("execenv: boundary platform %q is not a local arm", id.Platform)
	}
	return nil
}

// BoundaryMember is one process the kernel reports in a boundary at enumeration
// time, with the kernel-owned start token read beside it. The token is the
// proof of instance: it is not derivable from the pid, and a process that
// inherits a pid never inherits its predecessor's token.
type BoundaryMember struct {
	PID        int
	StartToken string
}

var (
	// ErrBoundaryUnavailable reports that this platform (or this host's
	// delegation) cannot provide the boundary the caller asked for. It is
	// fail-closed: the caller must not treat the absence as an empty boundary.
	ErrBoundaryUnavailable = errors.New("execenv: no local process boundary is available")
	// ErrBoundaryGone reports that a persisted boundary no longer exists. The
	// kernel only removes a cgroup that is empty, so a vanished boundary holds
	// no process: the reap may read it as already clean.
	ErrBoundaryGone = errors.New("execenv: the persisted boundary no longer exists")
	// ErrBoundaryMemberGone reports that the named member has no live process:
	// already clean, never something to signal.
	ErrBoundaryMemberGone = errors.New("execenv: the boundary member is gone")
	// ErrBoundaryIdentityChanged reports a member whose kernel start token no
	// longer equals the persisted token: a reused pid naming a different
	// process. §3 reads it as already clean; the seam must never signal it.
	ErrBoundaryIdentityChanged = errors.New("execenv: the boundary member is a different process")
	// ErrBoundaryNotSettled reports that the dead proof's bounded wait expired
	// with the caller's clean predicate still false: the boundary is unproven,
	// never clean.
	ErrBoundaryNotSettled = errors.New("execenv: the boundary did not settle within the wait")
)

// boundaryOps is one boundary's platform operations. They are values, not an
// interface, because every arm is built from the same three primitives and the
// nil case is checked once, at construction.
type boundaryOps struct {
	// spawnAttr opens this spawn's handle on the boundary and returns the
	// attributes the child must carry. The returned release must be called once
	// Start has returned (successfully or not): it closes the handle.
	spawnAttr func() (*syscall.SysProcAttr, func(), error)
	// members enumerates current membership with kernel start tokens.
	members func() ([]BoundaryMember, error)
	// observe reads one process's kernel start token.
	observe func(pid int) (string, error)
	// signal terminates one enumerated member.
	signal func(pid int) error
	// release tears the boundary down after it is proven clean.
	release func() error
}

// Boundary is a live handle on one local process boundary.
type Boundary struct {
	id  BoundaryIdentity
	ops boundaryOps
}

// CreateBoundary pre-creates a fresh boundary and returns a handle on it. The
// caller persists the returned identity (with a freshly minted nonce) in its
// pending-spawn intent BEFORE the spawn, and spawns the child with the
// attributes SpawnAttr returns.
//
// root names the parent the boundary is created under on Linux (empty means the
// process's own delegated cgroup subtree, walking up to the nearest writable
// ancestor). On Darwin the boundary is the launcher's own (pgid, session id)
// pair and root is ignored.
//
// A platform or caller that cannot pre-create the boundary fails closed with
// ErrBoundaryUnavailable: the caller spawns nothing into a boundary that does
// not exist, because a not-yet-populated boundary can never authorize a kill
// (§3) and neither can no boundary at all.
func CreateBoundary(root string) (*Boundary, error) {
	boundary, err := createBoundary(root)
	if err != nil {
		return nil, err
	}
	if err := boundary.id.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBoundaryUnavailable, err)
	}
	return boundary, nil
}

// OpenBoundary reopens a persisted boundary identity for enumeration and
// verification — the boot reap's half of §3, which has the persisted identity
// and nothing live. A boundary the kernel already removed reports
// ErrBoundaryGone (empty by construction); any other failure to reach the
// boundary is ErrBoundaryUnavailable and fails closed.
func OpenBoundary(id BoundaryIdentity) (*Boundary, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return openBoundary(id)
}

// Identity returns the persisted ownership identity of this boundary.
func (b *Boundary) Identity() BoundaryIdentity { return b.id }

// SpawnAttr returns the process attributes a spawn into this boundary must
// carry, and a release the caller must call once Start has returned. Spawning
// with these attributes is what makes the child a boundary member before it can
// run: the child is placed in the cgroup by the kernel at clone time on Linux,
// and inherits the launcher's (pgid, session id) on Darwin.
//
// A spawn that does not carry the returned attributes is not a member, and its
// process can never be reaped through this boundary — so the caller must treat
// a SpawnAttr failure as a spawn-refusal, never as a spawn without a boundary.
func (b *Boundary) SpawnAttr() (*syscall.SysProcAttr, func(), error) {
	if b == nil || b.ops.spawnAttr == nil {
		return nil, nil, fmt.Errorf("%w: this boundary has no spawn handle", ErrBoundaryUnavailable)
	}
	attr, release, err := b.ops.spawnAttr()
	if err != nil {
		return nil, nil, err
	}
	if attr == nil || release == nil {
		return nil, nil, fmt.Errorf("%w: this boundary returned no spawn attributes", ErrBoundaryUnavailable)
	}
	return attr, release, nil
}

// Observe reads one process's kernel-owned start token: the instance proof the
// caller persists beside the nonce after the spawn, and the value every signal
// re-checks. A pid with no live process reports ErrBoundaryMemberGone.
func (b *Boundary) Observe(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
	}
	return b.ops.observe(pid)
}

// Members enumerates current boundary membership with kernel start tokens.
// Membership selects the candidate set only; §3 never kills on membership
// alone. A member whose process exits between the membership read and the token
// read is omitted: it is gone, and gone reads as already clean.
func (b *Boundary) Members() ([]BoundaryMember, error) {
	if b == nil || b.ops.members == nil {
		return nil, fmt.Errorf("%w: this boundary has no enumeration", ErrBoundaryUnavailable)
	}
	return b.ops.members()
}

// SignalVerified signals the one member the caller has verified: it re-reads
// the member's kernel start token and signals only when it still equals
// startToken. A member whose token differs is a reused pid naming a different
// process — §3's already-clean rule — and is never signaled; a member whose
// process is gone is ErrBoundaryMemberGone. An empty startToken refuses: the
// boundary never kills on a pid alone.
func (b *Boundary) SignalVerified(pid int, startToken string) error {
	if pid <= 0 {
		return fmt.Errorf("%w: pid %d", ErrBoundaryMemberGone, pid)
	}
	if startToken == "" {
		return errors.New("execenv: refuse to signal a member with no kernel start token")
	}
	if b == nil || b.ops.observe == nil || b.ops.signal == nil {
		return fmt.Errorf("%w: this boundary cannot signal", ErrBoundaryUnavailable)
	}
	current, err := b.ops.observe(pid)
	if err != nil {
		return err
	}
	if current != startToken {
		return fmt.Errorf("%w: pid %d carried token %s, expected %s", ErrBoundaryIdentityChanged, pid, current, startToken)
	}
	return b.ops.signal(pid)
}

// Await is the bounded proof that a boundary no longer holds an unverified
// process. It re-enumerates membership until clean reports true — the caller's
// §3 predicate over the persisted pairs, because only the caller knows which
// pids are accounted for — or wait expires, which reports
// ErrBoundaryNotSettled. A wait of zero still takes one look.
//
// Await never claims cleanliness itself: it is only as strong as the
// predicate, and the caller must pass one that reads unrecognized members as
// live.
func (b *Boundary) Await(wait time.Duration, clean func([]BoundaryMember) bool) error {
	if clean == nil {
		return errors.New("execenv: the dead proof needs a clean predicate")
	}
	deadline := time.Now().Add(wait)
	for {
		members, err := b.Members()
		if err != nil {
			return err
		}
		if clean(members) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("%w: %d member(s) remain", ErrBoundaryNotSettled, len(members))
		}
		if remaining > boundaryPollInterval {
			remaining = boundaryPollInterval
		}
		time.Sleep(remaining)
	}
}

// Close tears the boundary down. It is only safe once the boundary is proven
// dead: the kernel refuses to remove a cgroup that still holds members, and
// that refusal is reported, never swallowed.
func (b *Boundary) Close() error {
	if b == nil || b.ops.release == nil {
		return nil
	}
	return b.ops.release()
}
