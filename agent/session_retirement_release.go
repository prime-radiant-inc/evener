package agent

import (
	"context"
	"errors"
	"fmt"

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
// is nil in production.
var retirementReleaseFault func(point string) error

func retirementReleaseFailure(point string) error {
	if retirementReleaseFault == nil {
		return nil
	}
	return retirementReleaseFault(point)
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
	// The preparation is one-use: marking it released first makes a concurrent
	// or repeated release refuse before it can double-close a runtime.
	prepared.released = true

	if err := retirementReleaseFailure("before_release"); err != nil {
		c.setReleaseFailure()
		return err
	}

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
		c.setReleaseFailure()
		return childErr
	}

	if err := s.releaseRuntime(ctx, false, releaseRetirement); err != nil {
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
	if prepared.released {
		return nil, errors.New("retirement release: preparation already released")
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
	if local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); ok {
		local.RetainSessionScratch()
	}
	s.mu.Lock()
	parked := s.worktreeRestoreEnv
	abandoned := append([]*execenv.LocalExecutionEnvironment(nil), s.abandonedEnvs...)
	s.mu.Unlock()
	if parked != nil {
		parked.RetainSessionScratch()
	}
	for _, env := range abandoned {
		env.RetainSessionScratch()
	}
	releaseRetainedScratchPool(s.retainedScratch.Swap(nil))
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
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("scratch retention release failed: %v", err)})
	}
}
