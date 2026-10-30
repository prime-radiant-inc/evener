package agent

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

type delegateRuntimeAttachMode uint8

const (
	delegateRuntimeAttachIdle delegateRuntimeAttachMode = iota
	delegateRuntimeAttachStarted
)

type owedBootstrapRestore struct {
	mu       sync.Mutex
	held     bool
	pending  map[*Session]func()
	done     map[*subagent]bool
	restored []owedBootstrapRuntime
}
type owedBootstrapRuntime struct {
	deferredOwedDelegateAttentionStart
}

func (g *owedBootstrapRestore) add(owner *Session, sub *subagent, start delegateStartCommit) error {
	target := sub.sess
	target.mu.Lock()
	normal := target.notifyFunc
	if normal == nil {
		target.mu.Unlock()
		return errors.New("restored delegate notify callback is unavailable")
	}
	target.notifyFunc = func() {
		g.mu.Lock()
		drive := normal
		if g.held {
			g.pending[target], drive = normal, nil
		}
		g.mu.Unlock()
		if drive != nil {
			drive()
		}
	}
	target.mu.Unlock()
	g.restored = append(g.restored, owedBootstrapRuntime{owner: owner, sub: sub, started: start})
	return nil
}
func (g *owedBootstrapRestore) open() {
	g.mu.Lock()
	g.held = false
	pending := g.pending
	g.pending = nil
	g.restored = nil
	g.done = nil
	g.mu.Unlock()
	for _, drive := range pending {
		drive()
	}
}

type deferredOwedDelegateAttentionStart struct {
	owner   *Session
	sub     *subagent
	started delegateStartCommit
}

func (s *Session) admitOwedDelegateAttentionStarts() error {
	if s == nil || s.delegateController == nil {
		return errors.New("owed delegate attention controller is unavailable")
	}
	if strings.TrimSpace(s.cfg.StateDir) == "" {
		return nil
	}
	owed, err := s.delegateController.owedAttentionStartsFromTranscripts()
	if err != nil {
		return err
	}
	gate := &owedBootstrapRestore{held: true, pending: make(map[*Session]func()), done: make(map[*subagent]bool)}
	deferred := make([]deferredOwedDelegateAttentionStart, 0, len(owed))
	for _, start := range owed {
		admitted, err := s.admitOwedDelegateAttentionStart(start, gate)
		if err != nil {
			return errors.Join(fmt.Errorf("delegate %s attention %s: %w", start.delegateID, start.attentionID, err), s.abortOwedDelegateBootstrap(gate, err))
		}
		deferred = append(deferred, *admitted)
	}
	if err := s.delegateController.reconcileDelegateAttentionFromTranscripts(); err != nil {
		return errors.Join(err, s.abortOwedDelegateBootstrap(gate, err))
	}
	for _, restored := range gate.restored {
		if err := restored.sub.sess.runDeferredRestoreSideEffects(); err != nil {
			return errors.Join(err, s.abortOwedDelegateBootstrap(gate, err))
		}
	}
	s.delegateController.mu.Lock()
	sort.Slice(deferred, func(i, j int) bool {
		left, right := deferred[i].started.lease.delegateID, deferred[j].started.lease.delegateID
		leftDepth, rightDepth := s.delegateController.delegateDepthLocked(left), s.delegateController.delegateDepthLocked(right)
		return leftDepth > rightDepth || leftDepth == rightDepth && left < right
	})
	s.delegateController.mu.Unlock()
	for i := range deferred {
		start := deferred[i]
		launch, err := s.prepareDeferredOwedStart(start, nil)
		if err != nil {
			return errors.Join(err, s.abortOwedDelegateBootstrap(gate, err))
		}
		if !launch {
			gate.done[start.sub] = true
			continue
		}
		if err := start.owner.launchAcceptedDelegateAttention(start.sub, start.started); err != nil {
			_, failureErr := s.prepareDeferredOwedStart(start, err)
			gate.done[start.sub] = true
			return errors.Join(fmt.Errorf("launch delegate %s owed attention: %w", start.started.lease.delegateID, err), failureErr, s.abortOwedDelegateBootstrap(gate, err))
		}
		gate.done[start.sub] = true
	}
	gate.open()
	return nil
}
func (s *Session) admitOwedDelegateAttentionStart(owed delegateOwedAttentionStart, gate *owedBootstrapRestore) (*deferredOwedDelegateAttentionStart, error) {
	owner, err := s.restoreColdDelegateOwnerRuntime(owed.parentID, gate)
	if err != nil {
		return nil, fmt.Errorf("restore owner runtime: %w", err)
	}
	reservation, err := s.delegateController.reserveOwedAttentionStart(owed.delegateID, owed.attentionID, owed.generation, owed.pendingIDs)
	if err != nil {
		return nil, fmt.Errorf("reserve owed generation: %w", err)
	}
	started, err := s.delegateController.CommitStart(reservation)
	if err != nil {
		return nil, fmt.Errorf("commit owed generation: %w", err)
	}
	sub, restoreErr := s.reconstructDelegateAttentionRuntime(owner, started, delegateRuntimeAttachStarted, gate)
	if restoreErr != nil {
		return nil, fmt.Errorf("restore owed runtime: %w", errors.Join(restoreErr, s.failOwedDelegateAttentionStart(started, nil, restoreErr)))
	}
	if err := sub.sess.restoreAcceptedTrigger(owed.attentionID, owed.turns); err != nil {
		return nil, fmt.Errorf("restore owed trigger: %w", err)
	}
	return &deferredOwedDelegateAttentionStart{owner: owner, sub: sub, started: started}, nil
}
func (s *Session) abortOwedDelegateBootstrap(gate *owedBootstrapRestore, cause error) error {
	var cleanupErr error
	for _, restored := range slices.Backward(gate.restored) {
		if !gate.done[restored.sub] {
			_, err := s.prepareDeferredOwedStart(restored.deferredOwedDelegateAttentionStart, cause)
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}
func (s *Session) prepareDeferredOwedStart(start deferredOwedDelegateAttentionStart, cause error) (bool, error) {
	c := s.delegateController
	c.mu.Lock()
	aggregate, live := c.durable[start.started.lease.delegateID], c.live[start.started.lease.delegateID]
	exact := aggregate != nil && aggregate.Generation == start.started.lease.generation && aggregate.CurrentRunOpen && live != nil && live.binding != nil && live.binding.lease == start.started.lease && live.binding.runtime == start.sub.sess && live.runtime == start.sub.sess
	if cause == nil && exact && aggregate.Phase == delegatestore.PhaseRunning && aggregate.PendingStopSeq == 0 && !live.recoveryRequired {
		c.mu.Unlock()
		return true, nil
	}
	owned := exact
	conflict := live != nil && (live.binding != nil && live.binding.runtime == start.sub.sess || live.runtime == start.sub.sess) && !owned && live.binding != nil
	if !owned && !conflict && live != nil && live.binding == nil && live.runtime == start.sub.sess {
		c.setResidentRuntimeLocked(live, nil)
	}
	c.mu.Unlock()
	var persistErr error
	if owned {
		persistErr = s.failOwedDelegateAttentionStart(start.started, start.sub.sess, cause)
		c.mu.Lock()
		live = c.live[start.started.lease.delegateID]
		if live != nil && (live.binding == nil || persistErr != nil) {
			if live.binding != nil && live.binding.lease == start.started.lease && live.binding.runtime == start.sub.sess {
				live.binding.runtime = nil
			}
			if live.runtime == start.sub.sess {
				c.setResidentRuntimeLocked(live, nil)
			}
		}
		c.mu.Unlock()
	}
	if conflict {
		return false, errors.New("owed candidate conflicts with controller runtime state")
	}
	start.owner.subagents.removeSession(start.sub.id, start.sub.sess)
	start.sub.sess.discardRestoredCandidate()
	return false, persistErr
}

func (s *Session) failOwedDelegateAttentionStart(started delegateStartCommit, runtime *Session, cause error) error {
	c := s.delegateController
	c.mu.Lock()
	if _, live, err := c.exactLeaseLocked(started.lease); err == nil && live.binding.runtime == runtime {
		live.binding.ready = false
	}
	c.mu.Unlock()
	plans, err := c.FailCommittedRestart(started.lease, delegatePermanentStartFailure(cause, "construction_failed"))
	return errors.Join(err, s.executeDelegateMutationPlans(plans))
}
