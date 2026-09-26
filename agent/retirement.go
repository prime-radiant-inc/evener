package agent

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"primeradiant.com/evener/agent/internal/clock"
)

// RetirementClock exposes the existing injectable clock boundary to daemon
// callers.
type RetirementClock = clock.Clock

// RetirementTimer exposes the existing injectable timer boundary to daemon
// callers.
type RetirementTimer = clock.Timer

// RetirementTicker exposes the existing injectable ticker boundary to daemon
// callers.
type RetirementTicker = clock.Ticker

// ErrRetirementUnavailable means the process admission fence is closed.
var ErrRetirementUnavailable = errors.New("retirement unavailable")

// RetirementBlocker identifies an obligation that prevents retirement.
type RetirementBlocker struct {
	Category   string
	SessionID  string
	DelegateID string
}

// RetirementSnapshot describes process retirement, not conversation state.
type RetirementSnapshot struct {
	Phase         string
	Timeout       time.Duration
	EligibleSince time.Time
	Deadline      time.Time
	Blockers      []RetirementBlocker
	Failure       string
}

// RetirementController owns the process admission fence. Its mutex is held only
// for controller state: never across work, callbacks, Session locks or waits.
// This primitive is not a runtime eligibility proof; integration must add the
// complete tree predicate before enabling retirement.
type RetirementController struct {
	mu                sync.Mutex
	root              *Session
	generation        uint64
	phase             string
	active            map[uint64]RetirementBlocker
	nextLease         uint64
	readers           int
	readersDone       chan struct{}
	changed           chan struct{}
	clock             RetirementClock
	timeout           time.Duration
	configuredTimeout time.Duration
	timeoutWrites     uint64
	eligibleSince     time.Time
	failure           string
	claim             *RetirementClaim
	// evidenceGate is a test-only observation seam: when set it runs inside
	// nonBlockingEvidence, with the caller's captured root and generation already
	// fixed, so a test can change the controller identity while that read is in
	// flight. It is nil in production and is installed before the controller is
	// used concurrently, so it needs no lock of its own.
	evidenceGate func()
}

// RetirementClaim is an identity-bound preparing fence owned by its controller.
// Its fields are protected by that controller's mutex.
type RetirementClaim struct {
	controller *RetirementController
	root       *Session
	generation uint64
	tree       *delegateTreeController
	committed  bool
	finished   bool
}

// errRetirementTimeoutNegative is the single refusal a negative idle deadline
// meets, whether at controller construction or at a runtime Retarget.
var errRetirementTimeoutNegative = errors.New("retirement timeout must not be negative")

// NewRetirementController constructs a dormant process admission controller.
func NewRetirementController(timeout time.Duration, clk RetirementClock) (*RetirementController, error) {
	if timeout < 0 {
		return nil, errRetirementTimeoutNegative
	}
	if clk == nil {
		return nil, errors.New("retirement clock is required")
	}
	return &RetirementController{
		phase: "resident", active: make(map[uint64]RetirementBlocker),
		changed: make(chan struct{}, 1), clock: clk, timeout: timeout, configuredTimeout: timeout,
	}, nil
}

// AttachRoot publishes the current serve root and resets the idle deadline to
// the configured baseline: the fresh root starts from the same deadline a
// fresh spawn would boot with, not whatever the predecessor's archive
// decision left armed, and minting that reset as a timeout write supersedes
// every pending undo token, so a stale setter can never undo past the root
// swap. The caller retains its mutation lease through construction,
// publication and old-root settlement. Atomic Session publication lets the
// phase check and generation change share one short critical section without
// acquiring a Session lock or exposing a split-publication race.
func (c *RetirementController) AttachRoot(root *Session) error {
	if root == nil {
		return errors.New("retirement root is required")
	}
	c.mu.Lock()
	if c.phase != "resident" {
		c.mu.Unlock()
		return ErrRetirementUnavailable
	}
	if !root.retirementController.CompareAndSwap(nil, c) && root.retirementController.Load() != c {
		c.mu.Unlock()
		return ErrRetirementUnavailable
	}
	c.root = root
	c.generation++
	c.eligibleSince = time.Time{}
	c.timeout = c.configuredTimeout
	c.timeoutWrites++
	c.mu.Unlock()
	c.Changed()
	return nil
}

// BeginMutation admits one operation without retaining a mutex across its work.
// Nested operations get independent leases; cancellation may release each once.
//
// The category is a closed vocabulary rather than free text, because it reaches
// the wire in RetirementSnapshot.Blockers: an unrecognized value is replaced
// with "unsupported" instead of being echoed back. Every category a caller
// actually passes must therefore be listed here, or its blocker reports as
// unknown work.
func (c *RetirementController) BeginMutation(sessionID, category string) (func(), error) {
	switch category {
	case "turn", "input", "autonomous", "question", "job", "watch", "delegate",
		"environment", "persistence", "admission", "notification",
		"delegate_drive", "delegate_delivery", "delegate_restore", "unsupported":
	default:
		category = "unsupported"
	}
	c.mu.Lock()
	if c.phase != "resident" {
		c.mu.Unlock()
		return nil, ErrRetirementUnavailable
	}
	c.nextLease++
	lease := c.nextLease
	c.active[lease] = RetirementBlocker{Category: category, SessionID: sessionID}
	c.eligibleSince = time.Time{}
	c.mu.Unlock()
	c.Changed()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.active, lease)
			c.mu.Unlock()
			c.Changed()
		})
	}, nil
}

// Changed coalesces notifications; callers notify only after dropping owner locks.
func (c *RetirementController) Changed() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// Retarget changes the automatic-retirement idle deadline at runtime, from the
// Hub's archive/unarchive decision. The settled instant is preserved, so the
// new deadline is eligibleSince+timeout: shortening fires proportionally sooner
// — immediately when the new timeout is below already-elapsed idle time — and
// lengthening re-arms later. Zero restores the disabled state. A negative
// timeout is refused without touching the armed interval. Refused while a
// retirement claim is preparing or retiring, like every other entry point; the
// claim re-proves the deadline against the current timeout, so a stale tick
// from the pre-Retarget timer can never claim before the new deadline.
func (c *RetirementController) Retarget(timeout time.Duration) error {
	_, _, err := c.RetargetStamped(timeout)
	return err
}

// RetargetStamped is Retarget for a caller that may later have to undo its
// write: it also reports the deadline the write replaced and mints a write
// token — the count of timeout writes after this one. The undo, UndoRetarget,
// needs both: the previous deadline as its restore target, and the token as
// the exact identity of the write being undone, because two writers can
// legitimately choose the same deadline and a value comparison cannot tell
// them apart.
func (c *RetirementController) RetargetStamped(timeout time.Duration) (previous time.Duration, token uint64, err error) {
	if timeout < 0 {
		return 0, 0, errRetirementTimeoutNegative
	}
	c.mu.Lock()
	if c.phase != "resident" {
		c.mu.Unlock()
		return 0, 0, ErrRetirementUnavailable
	}
	previous = c.timeout
	c.timeout = timeout
	c.timeoutWrites++
	token = c.timeoutWrites
	c.mu.Unlock()
	c.Changed()
	return previous, token, nil
}

// UndoRetarget restores the deadline to `to` only while `token` still names
// the newest timeout write — a later Retarget, even one that chose the same
// deadline, owns the controller's timeout now and must survive the caller's
// undo. The token check and the restore share one critical section, so no
// writer can slip between the guard and the undo. It reports whether the
// restore ran; a controller that is no longer resident never restores, since
// the process is preparing or retiring and the deadline no longer matters.
func (c *RetirementController) UndoRetarget(token uint64, to time.Duration) bool {
	// Token 0 never names a stamped write on a fresh controller (whose write
	// count starts at 0), and a negative restore target is refused exactly
	// like a negative Retarget: neither may bypass the stamped write's
	// validation.
	if token == 0 || to < 0 {
		return false
	}
	c.mu.Lock()
	if c.phase != "resident" || c.timeoutWrites != token {
		c.mu.Unlock()
		return false
	}
	c.timeout = to
	c.timeoutWrites++
	c.mu.Unlock()
	c.Changed()
	return true
}

// Borrow protects an in-flight read, not a subscription's lifetime. Reads may
// enter during preparation; Commit closes their admission before draining them.
func (c *RetirementController) Borrow() (func(), error) {
	c.mu.Lock()
	if c.phase == "retiring" {
		c.mu.Unlock()
		return nil, ErrRetirementUnavailable
	}
	c.readers++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.readers--
			if c.phase == "retiring" && c.readers == 0 {
				close(c.readersDone)
			}
			c.mu.Unlock()
		})
	}, nil
}

// Commit irreversibly closes admission without waiting for existing readers.
func (c *RetirementController) Commit(claim *RetirementClaim) error {
	c.mu.Lock()
	if !c.validClaimLocked(claim) {
		c.mu.Unlock()
		return ErrRetirementUnavailable
	}
	claim.committed = true
	c.phase = "retiring"
	c.readersDone = make(chan struct{})
	if c.readers == 0 {
		close(c.readersDone)
	}
	c.mu.Unlock()
	c.Changed()
	return nil
}

// DrainReaders waits outside the controller lock. Failure never reopens admission.
func (c *RetirementController) DrainReaders(ctx context.Context) error {
	c.mu.Lock()
	if c.phase != "retiring" {
		c.mu.Unlock()
		return ErrRetirementUnavailable
	}
	done := c.readersDone
	c.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		c.failure = "reader_drain_failed"
		c.mu.Unlock()
		c.Changed()
		return ctx.Err()
	}
}

// Snapshot returns a detached view of controller state.
func (c *RetirementController) Snapshot() RetirementSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *RetirementController) snapshotLocked() RetirementSnapshot {
	return c.snapshotWithBlockersLocked(nil)
}

// claimSnapshotCurrent builds a claim-attempt snapshot from the live controller
// state plus a fresh, non-blocking evidence read of root. It never consults cached
// evidence and never holds c.mu across the evidence read, so it cannot present a
// settled obligation as current and cannot wait on admitted work. root and
// generation are the controller identity captured under c.mu; a nil root yields no
// Session evidence (the snapshot's own root check reports the missing root
// instead). The automatic (manual=false) path skips the read because Run discards
// that snapshot (retirement.go Run tick); only the manual response at
// cmd/evener/serve.go renders it.
//
// AttachRoot replaces c.root and advances c.generation under c.mu, so it can run
// entirely within the window where c.mu is dropped for the evidence read. The read
// then describes the previous root while the controller now owns a new one.
// Revalidating the captured pair after relocking and discarding a mismatched read
// is what keeps the snapshot from combining the old root's obligations with the
// new root's live leases; this is the same root/generation counter-idiom
// validClaimLocked uses on the claim path. On mismatch the snapshot degrades to
// live controller state only, and the next attempt reads the new root afresh.
func (c *RetirementController) claimSnapshotCurrent(root *Session, generation uint64, manual bool) RetirementSnapshot {
	var evidence []RetirementBlocker
	if manual {
		evidence = c.nonBlockingEvidence(root)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.root != root || c.generation != generation {
		evidence = nil
	}
	return c.snapshotWithBlockersLocked(evidence)
}

// nonBlockingEvidence reads the obligations that cannot wait on admitted work,
// mirroring the full read's root-vs-tree split. It must be called without c.mu.
func (c *RetirementController) nonBlockingEvidence(root *Session) []RetirementBlocker {
	if root == nil {
		return nil
	}
	if c.evidenceGate != nil {
		c.evidenceGate()
	}
	if tree := root.delegateController; tree != nil {
		return tree.retirementNonBlockingEvidence()
	}
	return root.retirementNonBlockingEvidence()
}

// snapshotWithBlockersLocked builds a snapshot from live controller state plus
// any extra blockers the caller supplies.
func (c *RetirementController) snapshotWithBlockersLocked(extra []RetirementBlocker) RetirementSnapshot {
	state := RetirementSnapshot{Phase: c.phase, Timeout: c.timeout,
		EligibleSince: c.eligibleSince, Failure: c.failure}
	if c.root == nil {
		state.Blockers = append(state.Blockers, RetirementBlocker{Category: "unsupported"})
	}
	if c.timeout > 0 && !c.eligibleSince.IsZero() {
		state.Deadline = c.eligibleSince.Add(c.timeout)
	}
	state.Blockers = append(state.Blockers, extra...)
	for _, blocker := range c.active {
		state.Blockers = append(state.Blockers, blocker)
	}
	slices.SortFunc(state.Blockers, func(a, b RetirementBlocker) int {
		if order := cmp.Compare(a.Category, b.Category); order != 0 {
			return order
		}
		if order := cmp.Compare(a.SessionID, b.SessionID); order != 0 {
			return order
		}
		return cmp.Compare(a.DelegateID, b.DelegateID)
	})
	state.Blockers = slices.Compact(state.Blockers)
	return state
}

// TryClaim never waits for admitted work. Every return presents current
// obligations: the pre-evidence early returns and the invalid-claim return read
// only the non-blocking evidence sources (root input predicate, in-memory owner
// counters, goal store, pending job notifications, and the shared tree's
// in-memory owner state plus its live members), while the refusal and success
// paths use the complete evidence they just read with nothing admitted. No path
// consults cached evidence, so a prior attempt's refusal can never surface on a
// later one, an active-lease refusal, or a different generation/root. Full tree
// proof is still required before automatic retirement can be activated.
func (c *RetirementController) TryClaim(manual bool) (*RetirementClaim, RetirementSnapshot, error) {
	// The injected clock is a callback boundary too; do not call it under mu.
	var now time.Time
	if !manual {
		now = c.clock.Now()
	}
	c.mu.Lock()
	if c.phase != "resident" {
		root, generation := c.root, c.generation
		c.mu.Unlock()
		return nil, c.claimSnapshotCurrent(root, generation, manual), ErrRetirementUnavailable
	}
	if len(c.active) != 0 || c.root == nil {
		root, generation := c.root, c.generation
		c.mu.Unlock()
		return nil, c.claimSnapshotCurrent(root, generation, manual), nil
	}
	if !manual && (c.timeout == 0 || c.eligibleSince.IsZero() || now.Before(c.eligibleSince.Add(c.timeout))) {
		root, generation := c.root, c.generation
		c.mu.Unlock()
		return nil, c.claimSnapshotCurrent(root, generation, manual), nil
	}
	c.phase = "preparing"
	c.claim = &RetirementClaim{controller: c, root: c.root, generation: c.generation, tree: c.root.delegateController}
	claim := c.claim
	c.mu.Unlock()
	var blockers []RetirementBlocker
	var evidenceErr error
	if claim.tree != nil {
		evidenceErr = claim.tree.setRetirementFence(claim)
		if evidenceErr == nil {
			var treeBlockers []RetirementBlocker
			treeBlockers, _, evidenceErr = claim.tree.retirementEvidence()
			blockers = append(blockers, treeBlockers...)
		}
	} else {
		blockers, evidenceErr = claim.root.retirementEvidence()
	}
	if evidenceErr != nil || len(blockers) != 0 {
		if claim.tree != nil {
			claim.tree.clearRetirementFence(claim)
		}
	}
	c.mu.Lock()
	if !c.validClaimLocked(claim) {
		c.mu.Unlock()
		// claim.root/claim.generation are the identity the rejected claim was
		// created with; revalidation against the live pair rejects (and discards)
		// an evidence read that no longer describes the controller's root.
		return nil, c.claimSnapshotCurrent(claim.root, claim.generation, manual), ErrRetirementUnavailable
	}
	if len(blockers) != 0 || evidenceErr != nil {
		claim.finished = true
		c.claim = nil
		c.phase = "resident"
		c.eligibleSince = time.Time{}
		snapshot := c.snapshotWithBlockersLocked(blockers)
		c.mu.Unlock()
		// The refusal returned the controller to the resident phase and dropped
		// the interval, exactly like Abort/AttachRoot/BeginMutation. Notify after
		// dropping the lock so Run re-evaluates the settled process and re-arms
		// the idle timer instead of waiting for an unrelated event.
		c.Changed()
		return nil, snapshot, evidenceErr
	}
	snapshot := c.snapshotWithBlockersLocked(blockers)
	c.mu.Unlock()
	return claim, snapshot, nil
}

func (c *RetirementController) validClaimLocked(claim *RetirementClaim) bool {
	return c.phase == "preparing" && claim != nil && c.claim == claim &&
		claim.controller == c && claim.root == c.root && claim.generation == c.generation &&
		!claim.committed && !claim.finished
}

// Abort reopens admission only for the exact uncommitted preparation.
func (c *RetirementController) Abort(claim *RetirementClaim, failure string) error {
	c.mu.Lock()
	if !c.validClaimLocked(claim) {
		c.mu.Unlock()
		return ErrRetirementUnavailable
	}
	claim.finished = true
	// Keep outer admission closed until the exact tree fence has been removed.
	c.mu.Unlock()
	if claim.tree != nil {
		claim.tree.clearRetirementFence(claim)
	}
	c.mu.Lock()
	c.claim = nil
	c.phase = "resident"
	c.eligibleSince = time.Time{}
	switch failure {
	case "", "prepare_failed", "release_failed", "reader_drain_failed":
		c.failure = failure
	default:
		c.failure = "prepare_failed"
	}
	c.mu.Unlock()
	c.Changed()
	return nil
}

// RealRetirementClock returns the production wall clock for daemon callers
// (cmd/evener serve) that cannot import the agent-internal clock package.
func RealRetirementClock() RetirementClock { return clock.Real() }

// Run drives the daemon-owned idle timer until ctx is done. It is the only
// automatic source of retirement claims: every other caller passes manual=true
// to TryClaim. The timer exists only while the process is fully settled —
// root attached, phase resident, no admitted work — and every tick re-proves
// eligibility through TryClaim rather than trusting that the tick is fresh,
// so a tick already in flight when a lease disarms its timer retires nothing.
//
// Eligibility never accrues across blocked time: evaluate clears
// eligibleSince whenever the process is unsettled, so the interval that
// ultimately fires started at the most recent settled instant. A preparation
// failure is survivable — the consumer aborts the claim, evaluate observes
// the settled process again and arms a fresh full interval, and Run lives on.
// Run returns nil when ctx is done; consumer errors are the consumer's
// responsibility (it owns Abort/Commit), not the loop's.
func (c *RetirementController) Run(ctx context.Context, retire func(context.Context, *RetirementClaim) error) error {
	var timer RetirementTimer
	var armedDeadline time.Time
	disarm := func() {
		if timer != nil {
			timer.Stop()
		}
		armedDeadline = time.Time{}
	}
	arm := func(deadline time.Time, d time.Duration) {
		if d < 0 {
			d = 0
		}
		if timer == nil {
			timer = c.clock.NewTimer(d)
		} else if !armedDeadline.Equal(deadline) {
			timer.Reset(d)
		} else {
			return
		}
		armedDeadline = deadline
	}
	evaluate := func() {
		// The injected clock is a callback boundary; never call it under mu.
		now := c.clock.Now()
		c.mu.Lock()
		settled := c.root != nil && c.phase == "resident" && len(c.active) == 0
		if !settled {
			c.eligibleSince = time.Time{}
			c.mu.Unlock()
			disarm()
			return
		}
		if c.eligibleSince.IsZero() {
			c.eligibleSince = now
		}
		if c.timeout == 0 {
			// Automatic retirement disabled: eligibility still tracked for
			// diagnostics, but no expiry timer exists.
			c.mu.Unlock()
			disarm()
			return
		}
		deadline := c.eligibleSince.Add(c.timeout)
		remaining := deadline.Sub(now)
		c.mu.Unlock()
		arm(deadline, remaining)
	}
	defer disarm()
	evaluate()
	for {
		var tick <-chan time.Time
		if timer != nil {
			tick = timer.C()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.changed:
			evaluate()
		case <-tick:
			armedDeadline = time.Time{}
			claim, _, err := c.TryClaim(false)
			if err == nil && claim != nil {
				_ = retire(ctx, claim)
			}
			evaluate()
		}
	}
}
