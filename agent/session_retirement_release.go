package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// runtimeReleasePolicy selects the teardown semantics of Session.releaseRuntime.
// It is the plan's explicit policy that keeps retirement out of the durable
// stop-all and lane-disposal paths.
type runtimeReleasePolicy uint8

const (
	// releaseTerminal is the ordinary Session.Close policy: stop durable
	// delegates, cancel jobs/watches, dispose isolation lanes, unlock this
	// session's own managed worktrees, run the SessionEnd hook and emit
	// session_closed. It preserves every existing close branch unchanged.
	releaseTerminal runtimeReleasePolicy = iota
	// releaseRetirement is the non-terminal policy: release only process-local
	// runtime resources, keeping every durable session, delegate, lane and
	// retention record resumable. It never cancels jobs, drops watches,
	// disposes lanes, unlocks occupied worktrees, runs the SessionEnd hook or
	// emits session_closed.
	releaseRetirement
)

// retirementReleaseFault injects deterministic failures at the non-terminal
// release boundary, after Commit and before the corresponding side effect. It
// is nil in production. It is read on the release path, which can run on a
// goroutine outliving the test that set it, so the seam is synchronized: a
// plain read racing a test's plain write is a data race under the Go memory
// model, and this repository runs -race. Its "transcript_close" point is
// evaluated only under the retirement policy, so it can never affect a terminal
// close.
var (
	retirementReleaseFaultMu sync.Mutex
	retirementReleaseFault   func(point string) error
)

// setRetirementReleaseFault installs fn as the release-fault injector and
// returns a function that restores the previous seam. Tests pair it with
// t.Cleanup; production never calls it.
func setRetirementReleaseFault(fn func(point string) error) func() {
	retirementReleaseFaultMu.Lock()
	prev := retirementReleaseFault
	retirementReleaseFault = fn
	retirementReleaseFaultMu.Unlock()
	return func() {
		retirementReleaseFaultMu.Lock()
		retirementReleaseFault = prev
		retirementReleaseFaultMu.Unlock()
	}
}

func retirementReleaseFailure(point string) error {
	retirementReleaseFaultMu.Lock()
	fault := retirementReleaseFault
	retirementReleaseFaultMu.Unlock()
	if fault == nil {
		return nil
	}
	return fault(point)
}

// errRetirementTeardownSpent means the session's single release pass (a terminal
// Close or an earlier non-terminal release) already consumed closeOnce, so this
// call cannot perform the non-terminal release it was asked for.
var errRetirementTeardownSpent = errors.New("retirement release: the session teardown pass was already spent")

// retirementTeardownSpent reports whether the session is already closing/closed,
// i.e. the one teardown pass is spent or in flight. It is side-effect-free.
func (s *Session) retirementTeardownSpent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closingOrClosedLocked()
}

// ReleaseForRetirement is the free-function form of the non-terminal release
// named by the cross-task interface contract.
func ReleaseForRetirement(ctx context.Context, prepared *RetirementPreparation) error {
	if prepared == nil || prepared.root == nil {
		return errors.New("retirement release: no committed preparation")
	}
	return prepared.root.ReleaseForRetirement(ctx, prepared)
}

// ReleaseForRetirement releases the root and every exact resident child runtime
// of a committed preparation without terminating the durable sessions. It
// requires the exact committed preparation and errors before any side effect
// otherwise. A failure leaves the controller in the retiring phase with a
// bounded diagnostic; it never reopens admission or runs a fallback terminal
// Close.
func (s *Session) ReleaseForRetirement(ctx context.Context, prepared *RetirementPreparation) error {
	c, err := s.validateRetirementRelease(prepared)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		c.setReleaseFailure()
		return err
	}
	// Re-check with no lock held: a Close racing validation must not let a
	// refused release consume the preparation or touch any child.
	if s.retirementTeardownSpent() {
		c.setReleaseFailure()
		return errRetirementTeardownSpent
	}
	// The preparation is one-use: claim it with an atomic compare-and-swap so a
	// concurrent release cannot also pass the check and enter teardown before
	// closeOnce stops its later effects. Exactly one caller wins.
	if !prepared.released.CompareAndSwap(false, true) {
		return errors.New("retirement release: preparation already released")
	}

	if err := retirementReleaseFailure("before_release"); err != nil {
		c.setReleaseFailure()
		return err
	}
	if s.retirementTeardownSpent() {
		c.setReleaseFailure()
		return errRetirementTeardownSpent
	}

	if err := s.releaseRetirementTeardown(ctx, prepared); err != nil {
		c.setReleaseFailure()
		return err
	}
	// Only exact process pointers change. No durable stop, disposal or outcome
	// rewrite happens here.
	if claim := prepared.claim; claim != nil && claim.tree != nil {
		exact := make(map[string]*Session, len(prepared.sessions))
		for _, child := range prepared.sessions {
			if child != nil && child.owningDelegateID != "" {
				exact[child.owningDelegateID] = child
			}
		}
		if err := claim.tree.releaseRetiredRuntimes(exact); err != nil {
			c.setReleaseFailure()
			return err
		}
	}
	return nil
}

// releaseRetirementTeardown claims the session's single teardown pass and, under
// that claim, releases every prepared resident child runtime leaf-first and then
// the root's non-terminal runtime. Claiming the pass before any child is
// released closes the window in which a concurrent terminal Close could consume
// the one teardown pass after children had already been mutated — leaving
// retirement to fail over partially-torn-down child state. A terminal Close that
// arrives while this pass runs blocks on closeOnce until the pass completes, then
// observes it spent and does nothing.
func (s *Session) releaseRetirementTeardown(ctx context.Context, prepared *RetirementPreparation) error {
	var releaseErr error
	ran := false
	s.closeOnce.Do(func() {
		ran = true
		// The observation/fault seam at the reservation boundary; the concurrent
		// release test relies on the point running exactly once.
		_ = retirementReleaseFailure("teardown_claimed")
		// Release the exact resident children leaf-first (prepared.sessions is
		// ordered leaf-first and excludes the root), keeping their durable
		// identity, descriptors, outcomes and lanes intact.
		var childErr error
		for _, child := range prepared.sessions {
			if child == nil || child == s {
				continue
			}
			if err := retirementReleaseFailure("child_release"); err != nil {
				childErr = errors.Join(childErr, err)
				break
			}
			childErr = errors.Join(childErr, child.releaseChildRuntimeForRetirement(ctx))
		}
		if childErr != nil {
			releaseErr = childErr
			return
		}
		releaseErr = s.releaseRuntimeOnce(ctx, closeOptions{}, releaseRetirement)
	})
	if !ran {
		return errRetirementTeardownSpent
	}
	return releaseErr
}

// validateRetirementRelease proves the preparation belongs to this session, is
// exactly the controller's committed claim, has not already been released, and
// that the retained scratch pins are still present. It performs no side effect.
func (s *Session) validateRetirementRelease(prepared *RetirementPreparation) (*RetirementController, error) {
	if prepared == nil || prepared.claim == nil || prepared.root == nil {
		return nil, errors.New("retirement release: no committed preparation")
	}
	if prepared.root != s {
		return nil, errors.New("retirement release: preparation belongs to another session")
	}
	if prepared.released.Load() {
		return nil, errors.New("retirement release: preparation already released")
	}
	// A terminal Close or an earlier release has already consumed the single
	// teardown pass. Error here — before any side effect — rather than reporting
	// a vacuous success over a stopped tree.
	if s.retirementTeardownSpent() {
		return nil, errRetirementTeardownSpent
	}
	c := prepared.claim.controller
	if c == nil {
		return nil, errors.New("retirement release: preparation has no controller")
	}
	c.mu.Lock()
	valid := c.phase == "retiring" && c.claim == prepared.claim &&
		prepared.claim.controller == c && prepared.claim.root == c.root &&
		prepared.claim.generation == c.generation && prepared.claim.committed &&
		!prepared.claim.finished
	c.mu.Unlock()
	if !valid {
		return nil, ErrRetirementUnavailable
	}
	// Task 5's committed manifest/pins must still be present; a missing or
	// contradicting pin blocks release before any side effect.
	if err := s.validateRetainedScratchPresent(); err != nil {
		return nil, fmt.Errorf("retirement release: %w", err)
	}
	return c, nil
}

// setReleaseFailure records the bounded release diagnostic without reopening
// admission or reopening the claim.
func (c *RetirementController) setReleaseFailure() {
	c.mu.Lock()
	if c.phase == "retiring" {
		c.failure = "release_failed"
	}
	c.mu.Unlock()
	c.Changed()
}

// releaseChildRuntimeForRetirement performs a non-terminal release of one
// resident child runtime and settles its scratch under the handoff disposition:
// leases are released, directories and durable records are kept.
func (s *Session) releaseChildRuntimeForRetirement(ctx context.Context) error {
	return teardownChildSessionWithPolicy(ctx, s, retainChildScratch, releaseRetirement)
}

// releaseRetirementScratch releases the live scratch leases this session's
// environments hold (current, parked and abandoned) plus the ones the retained
// pool reacquired, keeping every directory and the manifest/pin records so the
// session can be restored at its original paths. It never writes the Released
// tombstone and never removes a durable pin.
func (s *Session) releaseRetirementScratch() {
	// Seal before the detach, exactly like the terminal and child-teardown
	// paths: the retired session is never resumed in-process, and the
	// retirement consumes closeOnce — the terminal release that would sweep a
	// republished pool can never run afterward — so a refresh pass interleaved
	// between the detach and its seed CAS would publish a pool nothing can
	// ever release, holding every retained directory's lease for the daemon's
	// life (round 28).
	s.sealRetainedScratch()
	s.mu.Lock()
	current := s.env
	parentShared := s.parentSharedEnv
	abandoned := append([]*execenv.LocalExecutionEnvironment(nil), s.abandonedEnvs...)
	s.mu.Unlock()
	// The current environment belongs to this session except when it is a child
	// still holding its live parent's own object (parentSharedEnv); releasing
	// that lease here would take the scratch off an environment the parent is
	// still working in. A root and a child on an environment built for it have
	// parentSharedEnv nil or distinct, so this guard skips nothing for them.
	if local, ok := current.(*execenv.LocalExecutionEnvironment); ok && !sameEnvironment(current, parentShared) {
		local.RetainSessionScratch()
	}
	// The parked environment is the parent's own for a child that started on it
	// and then entered a worktree; ownedParkedWorktreeEnvironment names the
	// parked object this session owns and returns nil for that shared one.
	if parked, ok := s.ownedParkedWorktreeEnvironment().(*execenv.LocalExecutionEnvironment); ok {
		parked.RetainSessionScratch()
	}
	for _, env := range abandoned {
		env.RetainSessionScratch()
	}
	s.detachRetainedScratch()
	if hook := s.cfg.testOnly.scratchRetirementAfterDetach; hook != nil {
		hook()
	}
}

// releaseTerminalScratchRetention writes the terminal tombstone for this
// session's own root retention manifest after a terminal close has committed.
// It never runs for retirement, a child close, or a session that merely shares
// another root's manifest: ReleaseScratchRetention belongs only to the root
// that owns it.
func (s *Session) releaseTerminalScratchRetention() {
	owner, ok := s.scratchRetentionOwner()
	if !ok || owner.RootSessionID != s.id {
		return
	}
	// A successful restore reacquires a lease per retained-scratch reference and
	// pools every allocation no live environment adopted (a cold/unrestored
	// delegate, a parked or orphan binding). Release that pool first: it still
	// holds those directories' leases, so ReleaseScratchRetention would see
	// self-inflicted contention and skip removing their pins, leaving the
	// directories pinned against collection forever. Handles whose lease a live
	// environment adopted were already removed from the pool by the transfer, so
	// this releases only unadopted handles. It is safe here because every child
	// session has already been torn down earlier in the terminal close, so no
	// consumer can adopt a pooled handle after this point. Retain() releases each
	// lease without deleting the directory, preserving the retention semantics.
	// The seal comes first: a refresh pass can still be mid-install — the
	// detach takes no manifest lock its install hold would serialize on — and
	// a seed published after the detach would never be swept, holding its
	// pins against the collector for the daemon's life. Sealed, a pass that
	// wins the seed CAS after the detach undoes its own publish and hands the
	// leases back; a pass that published before the seal is swept by the
	// detach itself.
	s.sealRetainedScratch()
	s.detachRetainedScratch()
	if hook := s.cfg.testOnly.scratchTerminalReleaseAfterDetach; hook != nil {
		hook()
	}
	// The manifest's update lock is fail-fast, so a concurrent in-process
	// writer — a refresh pass's install hold, a mint's pin transaction — can
	// refuse the tombstone with ErrScratchRetentionLockHeld. That refusal is
	// transient by construction (holds last fsync-scale and every writer is
	// mortal), while giving up after one attempt leaves the tombstone
	// unwritten and the pins durable forever with every consumer already
	// gone — so the release retries the refusal with the same bounded,
	// growing backoff every other scratch writer uses before warning.
	var releaseAttempt int
	if err := sandbox.RetryScratchLockContention(func() error {
		releaseAttempt++
		if hook := s.cfg.testOnly.scratchTerminalReleaseAttempt; hook != nil {
			hook(releaseAttempt)
		}
		return sandbox.ReleaseScratchRetention(owner)
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("scratch retention release failed: %v", err)})
	}
}
