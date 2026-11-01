package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

// recordInputAdmission runs only after entry acceptance, before its first
// request. A new input retires the previous input's done fence, including for
// notifications that do not escalate the generation's report requirement.
func (c *delegateTreeController) recordInputAdmission(lease delegateLease, requiresReport bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, live, err := c.exactLeaseLocked(lease)
	if err != nil {
		return err
	}
	if requiresReport {
		if err := c.escalateCompletionRequirementLocked(lease); err != nil {
			return err
		}
	}
	if evidence := live.binding.evidence; evidence != nil && evidence.completionAdmitted {
		evidence.completionAdmitted = false
		c.evidenceVersion++
	}
	return nil
}

func (c *delegateTreeController) validateCompletionLocked(actor delegateActor) error {
	if actor.lease == nil {
		return nil
	}
	if _, _, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning); err != nil {
		return err
	}
	descendants := c.delegateSubtreeIDsLocked(actor.lease.delegateID)
	for id := range descendants {
		child := c.durable[id]
		if child.CurrentRunOpen || child.Phase == delegatestore.PhaseRunning || child.Phase == delegatestore.PhaseSettling || child.Phase == delegatestore.PhaseStopping {
			return appwire.InvalidParams("communicate done refused while child " + id + " is live, use waiting_on_work or blocked")
		}
	}
	for _, reservation := range c.reservations {
		parent := reservation.descriptor.ParentDelegateID
		if !reservation.create {
			if child := c.durable[reservation.delegateID]; child != nil {
				parent = child.Descriptor.ParentDelegateID
			}
		}
		_, descendant := descendants[parent]
		if parent == actor.lease.delegateID || descendant {
			return appwire.InvalidParams("communicate done refused while a child start is reserved, use waiting_on_work or blocked")
		}
	}
	return nil
}

type staleWaitTargetError struct {
	target string
	state  any
}

func (e *staleWaitTargetError) Error() string {
	return "wait target " + e.target + " is already settled"
}

// validateWaitTarget resolves a wait target against a live waker. It accepts a
// bare identity or a kind-prefixed one (job:, watch:, child:, input:).
func (s *Session) validateWaitTarget(ctx context.Context, target string) error {
	if s == nil {
		return errors.New("no session")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("wait target is empty")
	}
	kind, id := splitWaitTarget(target)
	if id == "" {
		return fmt.Errorf("wait target %q has no identity", target)
	}
	_ = ctx

	if kind == "" || kind == "job" {
		if s.jobManager != nil {
			journal, err := retirementJobJournal(filepath.Join(s.jobManager.dir, "jobs.jsonl"))
			if err != nil {
				return fmt.Errorf("cannot verify job %q: %w", id, err)
			}
			for _, record := range jobstore.Fold(journal) {
				if record.JobID == id {
					if record.Status.IsTerminal() {
						return &staleWaitTargetError{target: target, state: record}
					}
					return nil
				}
			}
			s.jobManager.mu.Lock()
			_, running := s.jobManager.running[id]
			s.jobManager.mu.Unlock()
			if running {
				return nil
			}
		}
	}

	if kind == "" || kind == "watch" {
		if slices.Contains(s.receiverWatchIDs(), id) {
			return nil
		}
	}

	if kind == "" || kind == "child" {
		if s.delegateController != nil {
			s.delegateController.mu.Lock()
			_, descendant := s.delegateController.delegateSubtreeIDsLocked(s.owningDelegateID)[id]
			aggregate := s.delegateController.durable[id]
			live := descendant && aggregate != nil && (aggregate.CurrentRunOpen ||
				aggregate.Phase == delegatestore.PhaseRunning ||
				aggregate.Phase == delegatestore.PhaseSettling ||
				aggregate.Phase == delegatestore.PhaseStopping)
			var terminal any
			if descendant && aggregate != nil && !live {
				terminal = map[string]any{"phase": aggregate.Phase, "generation": aggregate.Generation, "outcome": aggregate.LatestOutcome}
			}
			s.delegateController.mu.Unlock()
			if live {
				return nil
			}
			if terminal != nil {
				return &staleWaitTargetError{target: target, state: terminal}
			}
		}
	}

	if kind == "" || kind == "input" {
		s.mu.Lock()
		for _, input := range s.inputQueue {
			if input.ID == id {
				s.mu.Unlock()
				return nil
			}
		}
		for _, steering := range s.steeringQueue {
			if steering.StableTurnID == id || steering.ClientMutationID == id {
				s.mu.Unlock()
				return nil
			}
		}
		s.mu.Unlock()
	}

	return fmt.Errorf("wait target %q is absent or already settled", target)
}

func splitWaitTarget(target string) (kind, id string) {
	prefix, rest, ok := strings.Cut(target, ":")
	if !ok {
		return "", target
	}
	switch prefix {
	case "job", "watch", "child", "input":
		return prefix, rest
	default:
		return "", target
	}
}

// delegateToolBatch is process-local authority for one generation's admitted
// tool execution and synced result persistence. It is never recovered on reopen.
type delegateToolBatch struct {
	lease delegateLease
	done  chan struct{}
}
type delegateToolBatchContextKey struct{}

func (s *Session) beginDelegateToolBatch(ctx context.Context) (context.Context, func(), error) {
	actor, err := s.delegateActor(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if actor.lease == nil || s.delegateController == nil {
		return ctx, func() {}, nil
	}
	c := s.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, _, err := c.admitLeaseLocked(*actor.lease, delegatestore.PhaseRunning); err != nil {
		return ctx, nil, err
	}
	evidence, err := c.completionEvidenceLocked(*actor.lease)
	if err != nil {
		return ctx, nil, err
	}
	if evidence.batch != nil || c.hasSettlementClaimLocked(*actor.lease) {
		return ctx, nil, errDelegateTargetBusy
	}
	batch := &delegateToolBatch{lease: *actor.lease, done: make(chan struct{})}
	evidence.batch = batch
	end := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if evidence.batch == batch {
			evidence.batch = nil
		}
		select {
		case <-batch.done:
		default:
			close(batch.done)
		}
	}
	return context.WithValue(ctx, delegateToolBatchContextKey{}, batch), end, nil
}

func (c *delegateTreeController) ownsPublicationBatchLocked(actor delegateActor) bool {
	if actor.lease == nil || actor.batch == nil || actor.batch.lease != *actor.lease {
		return false
	}
	evidence, err := c.completionEvidenceLocked(*actor.lease)
	if err != nil || evidence.batch != actor.batch {
		return false
	}
	select {
	case <-actor.batch.done:
		return false
	default:
		return true
	}
}

func readyClosedChannel() <-chan struct{} { done := make(chan struct{}); close(done); return done }
