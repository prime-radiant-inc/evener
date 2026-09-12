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

// RetirementClock, RetirementTimer and RetirementTicker expose the existing
// injectable clock boundary to daemon callers.
type RetirementClock = clock.Clock
type RetirementTimer = clock.Timer
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
	mu            sync.Mutex
	root          *Session
	generation    uint64
	phase         string
	active        map[uint64]RetirementBlocker
	nextLease     uint64
	readers       int
	readersDone   chan struct{}
	changed       chan struct{}
	clock         RetirementClock
	timeout       time.Duration
	eligibleSince time.Time
	failure       string
	blockers      []RetirementBlocker
	claim         *RetirementClaim
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

// NewRetirementController constructs a dormant process admission controller.
func NewRetirementController(timeout time.Duration, clk RetirementClock) (*RetirementController, error) {
	if timeout < 0 {
		return nil, errors.New("retirement timeout must not be negative")
	}
	if clk == nil {
		return nil, errors.New("retirement clock is required")
	}
	return &RetirementController{
		phase: "resident", active: make(map[uint64]RetirementBlocker),
		changed: make(chan struct{}, 1), clock: clk, timeout: timeout,
	}, nil
}

// AttachRoot publishes the current serve root. The caller retains its mutation
// lease through construction, publication and old-root settlement. Atomic Session
// publication lets the phase check and generation change share one short critical
// section without acquiring a Session lock or exposing a split-publication race.
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
	c.mu.Unlock()
	c.Changed()
	return nil
}

// BeginMutation admits one operation without retaining a mutex across its work.
// Nested operations get independent leases; cancellation may release each once.
func (c *RetirementController) BeginMutation(sessionID, category string) (func(), error) {
	switch category {
	case "turn", "input", "autonomous", "question", "job", "watch", "delegate", "environment", "persistence", "admission", "unsupported":
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
	state := RetirementSnapshot{Phase: c.phase, Timeout: c.timeout,
		EligibleSince: c.eligibleSince, Failure: c.failure}
	if c.root == nil {
		state.Blockers = append(state.Blockers, RetirementBlocker{Category: "unsupported"})
	}
	if c.timeout > 0 && !c.eligibleSince.IsZero() {
		state.Deadline = c.eligibleSince.Add(c.timeout)
	}
	state.Blockers = append(state.Blockers, c.blockers...)
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

// TryClaim never waits for admitted work. The root input predicate runs behind
// the admission fence without holding mu. Full tree proof is still required
// before automatic retirement can be activated.
func (c *RetirementController) TryClaim(manual bool) (*RetirementClaim, RetirementSnapshot, error) {
	// The injected clock is a callback boundary too; do not call it under mu.
	var now time.Time
	if !manual {
		now = c.clock.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != "resident" {
		return nil, c.snapshotLocked(), ErrRetirementUnavailable
	}
	if len(c.active) != 0 || c.root == nil {
		return nil, c.snapshotLocked(), nil
	}
	if !manual && (c.timeout == 0 || c.eligibleSince.IsZero() || now.Before(c.eligibleSince.Add(c.timeout))) {
		return nil, c.snapshotLocked(), nil
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
		return nil, c.snapshotLocked(), ErrRetirementUnavailable
	}
	c.blockers = blockers
	if len(blockers) != 0 || evidenceErr != nil {
		claim.finished = true
		c.claim = nil
		c.phase = "resident"
		c.eligibleSince = time.Time{}
		return nil, c.snapshotLocked(), evidenceErr
	}
	return claim, c.snapshotLocked(), nil
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
	disarm := func() {
		if timer != nil {
			timer.Stop()
		}
	}
	arm := func(d time.Duration) {
		if d < 0 {
			d = 0
		}
		if timer == nil {
			timer = c.clock.NewTimer(d)
		} else {
			timer.Reset(d)
		}
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
		remaining := c.eligibleSince.Add(c.timeout).Sub(now)
		c.mu.Unlock()
		arm(remaining)
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
			claim, _, err := c.TryClaim(false)
			if err == nil && claim != nil {
				_ = retire(ctx, claim)
			}
			evaluate()
		}
	}
}
