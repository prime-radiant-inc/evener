package sshconn

// Crash-fencing spec 08c §3's pre-spawn ownership lifecycle as the production
// spawn paths drive it. S19 built the durable half — the local process boundary
// (agent/execenv) and the `pending-spawn` intent store (hostops) — and the boot
// reap that converges whatever a crash leaves open. This seam is the production
// wiring §3 requires of every worker ssh subprocess:
//
//	create the boundary BEFORE the spawn -> mint the nonce and arm the durable
//	`pending-spawn` intent BEFORE the exec -> spawn the child INSIDE the
//	boundary (its SpawnAttr) -> match the intent with the child's (pid, kernel
//	start token) -> drop the intent once the child exits cleanly.
//
// The scope is carried by the deploy/restart step's context, so exactly the
// spawns an operation record owns are armed: the read-only preflight (§6's
// exemption) and every other spawn run under a context with no scope and stay
// plain. A spawn under a scope that cannot create its boundary or persist its
// intent is refused, never launched unowned (§3: a not-yet-populated boundary
// can never authorize a kill, and neither can no boundary at all).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// ErrSpawnBoundaryUnavailable reports that this platform (or this host's
// delegation) cannot pre-create the local process boundary a fenced spawn
// requires. It is fail-closed: the caller refuses the spawn rather than
// launching a child no boundary can own.
var ErrSpawnBoundaryUnavailable = errors.New("sshconn: no local process boundary is available")

// spawnBoundarySettle bounds a fenced spawn's post-exit teardown. The kernel
// refuses to remove a boundary whose member tasks it has not reaped yet, so the
// teardown retries within this bound; it only ever runs after the direct child
// was waited on, so the bound covers a surviving descendant (an ssh
// ProxyCommand, a wrapper) rather than the command itself.
const spawnBoundarySettle = time.Second

// spawnBoundaryPollInterval is how often the bounded teardown retries while a
// member is still being reaped.
const spawnBoundaryPollInterval = 25 * time.Millisecond

// SpawnIntentStore is the durable pending-spawn intent surface one fenced spawn
// drives. *hostops.Store satisfies it; a test substitutes a fake so every
// disposition is exercisable without an operation store.
type SpawnIntentStore interface {
	// ArmSpawnIntent persists one pre-spawn intent, in its own write, BEFORE
	// the spawn.
	ArmSpawnIntent(recordID string, intent hostops.SpawnIntent) (hostops.Record, error)
	// MatchSpawnIntent persists the launcher-observed (pid, start token) marker
	// after the spawn.
	MatchSpawnIntent(recordID, nonce string, pid int, startTime string) (hostops.Record, error)
	// DropSpawnIntent is the clean-convergence write: it removes one open
	// intent and is idempotent.
	DropSpawnIntent(recordID, nonce string) error
}

// BoundaryID is one pre-created boundary's ownership identity: §9's
// local-linux / local-darwin discriminators minus the nonce and the
// launcher-observed marker, which live beside it in the intent. The spellings
// match agent/execenv's BoundaryPlatform literals; the adapter in
// spawnfence_unix.go is the one place the two are mapped.
type BoundaryID struct {
	// Platform selects the arm: BoundaryPlatformLinux or BoundaryPlatformDarwin.
	Platform string
	// CgroupID is the Linux arm's cgroup v2 directory path.
	CgroupID string
	// PGID and SessionID are the Darwin arm's pair.
	PGID      int
	SessionID int
}

const (
	// BoundaryPlatformLinux is the cgroup arm.
	BoundaryPlatformLinux = "linux"
	// BoundaryPlatformDarwin is the (pgid, session id) arm.
	BoundaryPlatformDarwin = "darwin"
)

// SpawnBoundary is the execenv.Boundary surface one fenced spawn drives.
// *execenv.Boundary satisfies it through the unix adapter.
type SpawnBoundary interface {
	// Identity returns the persisted ownership identity.
	Identity() BoundaryID
	// Enforcing reports whether an empty enumeration proves every spawned
	// process gone. A non-enforcing platform (Darwin's (pgid, session id) pair,
	// which a descendant can leave by calling setsid) never reads an empty
	// enumeration as a clean boundary, so the fence drops the intent on the
	// child's own reaped exit instead of requiring an emptiness proof (§3).
	Enforcing() bool
	// SpawnAttr returns the attributes the child must carry — what makes it a
	// boundary member before it can run — plus the release the fence calls once
	// Start has returned.
	SpawnAttr() (*syscall.SysProcAttr, func(), error)
	// Observe reads one process's kernel-owned start token.
	Observe(pid int) (string, error)
	// Close tears the boundary down; an enforcing platform refuses while a
	// member is still present, which is its emptiness proof.
	Close() error
}

// CreateSpawnBoundary pre-creates a boundary under root. Nil in a SpawnScope
// means the platform default (execenv's cgroup or session arm).
type CreateSpawnBoundary func(root string) (SpawnBoundary, error)

// SpawnScope names the one durable operation record an operation's local ssh
// spawns belong to, plus the store and boundary machinery their §3 lifecycle
// drives. It is the value the hub carries through the deploy/restart step's
// context; a spawn under a context with no scope is a spawn no record owns.
type SpawnScope struct {
	// RecordID is the operation record the pre-spawn intents are armed on.
	RecordID string
	store    SpawnIntentStore
	create   CreateSpawnBoundary
	root     string
	// settle overrides spawnBoundarySettle; zero means the default.
	settle time.Duration
}

// NewSpawnScope returns the scope one operation's mutating steps run under.
// store must be the operation store the record lives in: the pre-spawn intent
// must land before the spawn, so a scope with no store refuses the spawn rather
// than launching work no record can reap.
func NewSpawnScope(recordID string, store SpawnIntentStore) *SpawnScope {
	return &SpawnScope{RecordID: recordID, store: store}
}

// spawnScopeKey carries a SpawnScope through a context.
type spawnScopeKey struct{}

// WithSpawnScope returns ctx carrying scope: every one-shot ssh child spawned
// under it is armed into the scope's record before its exec and matched after
// it. A nil scope leaves ctx untouched, so the read-only paths that carry no
// scope never arm anything.
func WithSpawnScope(ctx context.Context, scope *SpawnScope) context.Context {
	if scope == nil {
		return ctx
	}
	return context.WithValue(ctx, spawnScopeKey{}, scope)
}

// SpawnScopeFrom returns the scope ctx carries, if any. The second result is
// false for a context with no scope — the read-only preflight (§6's exemption)
// and every other path that spawns no operation-owned work.
func SpawnScopeFrom(ctx context.Context) (*SpawnScope, bool) {
	scope, ok := ctx.Value(spawnScopeKey{}).(*SpawnScope)
	return scope, ok && scope != nil
}

// runFenced runs one one-shot ssh child under §3's lifecycle. It is only
// reached for a spawn a scope owns; every failure arm refuses the spawn or
// converges it, and never launches a child without a persisted intent or
// leaves a name wedged on a boundary that is demonstrably empty.
func (s *SpawnScope) runFenced(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	if s == nil || s.store == nil || s.RecordID == "" {
		return nil, errors.New("sshconn: this spawn's scope names no operation record to arm, so the spawn is refused: a child no record can reap must not be launched")
	}
	create := s.create
	if create == nil {
		create = defaultSpawnBoundary
	}
	boundary, err := create(s.root)
	if err != nil {
		return nil, fmt.Errorf("sshconn: the spawn is refused: %w", err)
	}
	nonce, err := s.armSpawnIntent(boundary)
	if err != nil {
		// The write may have landed even though it reported (a rename that
		// landed with a follow-up error); the drop is idempotent, so converge
		// eagerly. An intent left behind would still have an empty boundary,
		// which the boot reap reads as clean — never a wedge.
		if nonce != "" {
			_ = s.store.DropSpawnIntent(s.RecordID, nonce)
		}
		_ = boundary.Close()
		return nil, fmt.Errorf("sshconn: the spawn is refused: %w", err)
	}
	attr, release, err := boundary.SpawnAttr()
	if err != nil {
		// No child exists and no attributes were applied, so the armed intent is
		// spawnless: drop it and tear the empty boundary down. A drop that cannot
		// land leaves an intent whose boundary is empty, which the boot reap reads
		// as clean.
		_ = s.store.DropSpawnIntent(s.RecordID, nonce)
		_ = boundary.Close()
		return nil, fmt.Errorf("sshconn: the spawn is refused: %w", err)
	}
	out, err := runOneShot(ctx, argv, stdin, attr, release, func(pid int) error {
		token, observeErr := boundary.Observe(pid)
		if observeErr != nil {
			return fmt.Errorf("observe the spawned process %d: %w", pid, observeErr)
		}
		if _, matchErr := s.store.MatchSpawnIntent(s.RecordID, nonce, pid, token); matchErr != nil {
			return fmt.Errorf("persist the launcher marker for process %d: %w", pid, matchErr)
		}
		return nil
	})
	if settleErr := s.convergeSpawn(boundary, nonce); settleErr != nil {
		return out, errors.Join(err, settleErr)
	}
	return out, err
}

// armSpawnIntent mints this spawn's server-generated nonce and persists the
// pre-spawn intent on the scope's record, in its own durable write, BEFORE the
// exec. It returns the nonce even on failure, so a caller can make its best
// effort to drop what may have landed.
func (s *SpawnScope) armSpawnIntent(boundary SpawnBoundary) (string, error) {
	nonce, err := hostops.NewSpawnNonce()
	if err != nil {
		return "", err
	}
	intent, err := spawnIntentFor(boundary.Identity(), nonce)
	if err != nil {
		return nonce, err
	}
	if _, err := s.store.ArmSpawnIntent(s.RecordID, intent); err != nil {
		return nonce, err
	}
	return nonce, nil
}

// spawnIntentFor renders one boundary identity plus nonce as §3's persisted
// intent. An identity outside the two local arms refuses: an intent the reap
// cannot enumerate is not an intent.
func spawnIntentFor(id BoundaryID, nonce string) (hostops.SpawnIntent, error) {
	switch id.Platform {
	case BoundaryPlatformLinux:
		if id.CgroupID == "" {
			return hostops.SpawnIntent{}, fmt.Errorf("%w: the pre-created boundary carries no cgroup id", ErrSpawnBoundaryUnavailable)
		}
		return hostops.SpawnIntent{Nonce: nonce, Platform: hostops.SpawnPlatformLinux, CgroupID: id.CgroupID}, nil
	case BoundaryPlatformDarwin:
		if id.PGID <= 0 || id.SessionID <= 0 {
			return hostops.SpawnIntent{}, fmt.Errorf("%w: the pre-created boundary carries no (pgid, session id) pair", ErrSpawnBoundaryUnavailable)
		}
		pgid, session := id.PGID, id.SessionID
		return hostops.SpawnIntent{Nonce: nonce, Platform: hostops.SpawnPlatformDarwin, PGID: &pgid, SessionID: &session}, nil
	default:
		return hostops.SpawnIntent{}, fmt.Errorf("%w: the pre-created boundary carries platform %q", ErrSpawnBoundaryUnavailable, id.Platform)
	}
}

// convergeSpawn tears one spawn's boundary down once its child is gone and
// drops the intent only when the boundary is proven clean. The order is
// close-then-drop: a crash between the two leaves an intent whose boundary is
// gone and whose recorded pair the reap can re-verify through ObserveProcess,
// which converges (§3's clean rule for a matured boundary).
//
// On an enforcing platform the teardown is the emptiness proof: a boundary that
// still holds a member the launcher never observed is live, never clean, so the
// intent stays open for the next boot's reap or orphan-resolve. On a
// non-enforcing platform (Darwin) no teardown can prove emptiness, so the
// reaped child's own exit is the clean rule and the teardown is best-effort.
func (s *SpawnScope) convergeSpawn(boundary SpawnBoundary, nonce string) error {
	if boundary.Enforcing() {
		settle := s.settle
		if settle <= 0 {
			settle = spawnBoundarySettle
		}
		if err := closeBoundaryWithin(boundary, settle); err != nil {
			return fmt.Errorf("the boundary still holds a member the launcher never observed, so the spawn intent stays open for the boot reap or orphan-resolve: %w", err)
		}
	} else {
		_ = boundary.Close()
	}
	if err := s.store.DropSpawnIntent(s.RecordID, nonce); err != nil {
		return fmt.Errorf("the spawn intent could not be dropped after a clean teardown: %w", err)
	}
	return nil
}

// closeBoundaryWithin tears a boundary down within wait, retrying only the
// refusals a not-yet-reaped member causes (the kernel removes a cgroup only
// once it is empty). A permanent failure is reported at once.
func closeBoundaryWithin(boundary SpawnBoundary, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := boundary.Close()
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) && !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return err
		}
		if remaining > spawnBoundaryPollInterval {
			remaining = spawnBoundaryPollInterval
		}
		time.Sleep(remaining)
	}
}
