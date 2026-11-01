package agent

import (
	"errors"
	"fmt"
	"log/slog"
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
	delegateRuntimeAttachWaiting
)

type owedBootstrapRestore struct {
	mu         sync.Mutex
	held       bool
	holdDrives bool // the caller owns restored drive guards until Stop/input admission
	pending    map[*Session]func()
	done       map[*subagent]bool
	restored   []owedBootstrapRuntime
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
	restored := g.restored
	g.pending = nil
	g.restored = nil
	g.done = nil
	g.mu.Unlock()
	if g.holdDrives {
		for _, runtime := range restored {
			if releaseDriveGuard(runtime.sub) {
				if err := runtime.owner.delegateController.requestSessionWake(runtime.sub.id); err != nil {
					slog.Warn("notification wake failed", "session_id", runtime.sub.id, "error", err)
				}
			}
		}
	}
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
	var recheck bool
	defer func() {
		if recheck {
			s.delegateController.recheckNotificationReceivers()
		}
	}()
	abort := func(gate *owedBootstrapRestore, cause error) error {
		released, err := s.abortOwedDelegateBootstrap(gate, cause)
		recheck = recheck || released
		return err
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
		admitted, released, err := s.admitOwedDelegateAttentionStart(start, gate)
		recheck = recheck || released
		if err != nil {
			return errors.Join(fmt.Errorf("delegate %s attention %s: %w", start.delegateID, start.attentionID, err), abort(gate, err))
		}
		deferred = append(deferred, *admitted)
	}
	if err := s.delegateController.reconcileDelegateAttentionFromTranscripts(); err != nil {
		return errors.Join(err, abort(gate, err))
	}
	for _, restored := range gate.restored {
		if err := restored.sub.sess.runDeferredRestoreSideEffects(); err != nil {
			return errors.Join(err, abort(gate, err))
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
		launch, released, err := s.prepareDeferredOwedStart(start, nil)
		recheck = recheck || released
		if err != nil {
			return errors.Join(err, abort(gate, err))
		}
		if !launch {
			gate.done[start.sub] = true
			continue
		}
		if err := start.owner.launchAcceptedDelegateAttention(start.sub, start.started); err != nil {
			_, released, failureErr := s.prepareDeferredOwedStart(start, err)
			recheck = recheck || released
			gate.done[start.sub] = true
			return errors.Join(fmt.Errorf("launch delegate %s owed attention: %w", start.started.lease.delegateID, err), failureErr, abort(gate, err))
		}
		gate.done[start.sub] = true
	}
	gate.open()
	return nil
}
func (s *Session) admitOwedDelegateAttentionStart(owed delegateOwedAttentionStart, gate *owedBootstrapRestore) (*deferredOwedDelegateAttentionStart, bool, error) {
	owner, recheck, err := s.restoreColdDelegateOwnerRuntime(owed.parentID, gate)
	if err != nil {
		return nil, recheck, fmt.Errorf("restore owner runtime: %w", err)
	}
	reservation, err := s.delegateController.reserveOwedAttentionStart(owed.delegateID, owed.attentionID, owed.generation, owed.pendingIDs)
	if err != nil {
		return nil, recheck, fmt.Errorf("reserve owed generation: %w", err)
	}
	started, err := s.delegateController.CommitStart(reservation)
	recheck = recheck || started.recheckNotifications
	if err != nil {
		return nil, recheck, fmt.Errorf("commit owed generation: %w", err)
	}
	sub, released, restoreErr := s.reconstructDelegateAttentionRuntime(owner, started, delegateRuntimeAttachStarted, gate)
	recheck = recheck || released
	if restoreErr != nil {
		released, failureErr := s.failOwedDelegateAttentionStart(started, nil, restoreErr)
		recheck = recheck || released
		return nil, recheck, fmt.Errorf("restore owed runtime: %w", errors.Join(restoreErr, failureErr))
	}
	if err := sub.sess.restoreAcceptedTrigger(owed.attentionID, owed.turns); err != nil {
		return nil, recheck, fmt.Errorf("restore owed trigger: %w", err)
	}
	return &deferredOwedDelegateAttentionStart{owner: owner, sub: sub, started: started}, recheck, nil
}
func (s *Session) abortOwedDelegateBootstrap(gate *owedBootstrapRestore, cause error) (bool, error) {
	var recheck bool
	var cleanupErr error
	for _, restored := range slices.Backward(gate.restored) {
		if !gate.done[restored.sub] {
			_, released, err := s.prepareDeferredOwedStart(restored.deferredOwedDelegateAttentionStart, cause)
			recheck = recheck || released
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return recheck, cleanupErr
}
func (s *Session) prepareDeferredOwedStart(start deferredOwedDelegateAttentionStart, cause error) (bool, bool, error) {
	var recheck bool
	c := s.delegateController
	c.mu.Lock()
	aggregate, live := c.durable[start.started.lease.delegateID], c.live[start.started.lease.delegateID]
	exact := aggregate != nil && aggregate.Generation == start.started.lease.generation && aggregate.CurrentRunOpen && live != nil && live.binding != nil && live.binding.lease == start.started.lease && live.binding.runtime == start.sub.sess && live.runtime == start.sub.sess
	if cause == nil && exact && aggregate.Phase == delegatestore.PhaseRunning && aggregate.PendingStopSeq == 0 && !live.recoveryRequired {
		c.mu.Unlock()
		return true, recheck, nil
	}
	owned := exact
	conflict := live != nil && (live.binding != nil && live.binding.runtime == start.sub.sess || live.runtime == start.sub.sess) && !owned && live.binding != nil
	if !owned && !conflict && live != nil && live.binding == nil && live.runtime == start.sub.sess {
		recheck = c.setResidentRuntimeLocked(live, nil)
	}
	c.mu.Unlock()
	var persistErr error
	if owned {
		var released bool
		released, persistErr = s.failOwedDelegateAttentionStart(start.started, start.sub.sess, cause)
		recheck = recheck || released
		c.mu.Lock()
		live = c.live[start.started.lease.delegateID]
		if live != nil && (live.binding == nil || persistErr != nil) {
			if live.binding != nil && live.binding.lease == start.started.lease && live.binding.runtime == start.sub.sess {
				live.binding.runtime = nil
			}
			if live.runtime == start.sub.sess {
				released := c.setResidentRuntimeLocked(live, nil)
				recheck = recheck || released
			}
		}
		c.mu.Unlock()
	}
	if conflict {
		return false, recheck, errors.New("owed candidate conflicts with controller runtime state")
	}
	start.owner.subagents.removeSession(start.sub.id, start.sub.sess)
	start.sub.sess.discardRestoredCandidate()
	return false, recheck, persistErr
}

func (s *Session) failOwedDelegateAttentionStart(started delegateStartCommit, runtime *Session, cause error) (bool, error) {
	c := s.delegateController
	c.mu.Lock()
	if _, live, err := c.exactLeaseLocked(started.lease); err == nil && live.binding.runtime == runtime {
		live.binding.ready = false
	}
	c.mu.Unlock()
	plans, err := c.FailCommittedRestart(started.lease, delegatePermanentStartFailure(cause, "construction_failed"))
	recheck := plans.recheckNotifications
	plans.recheckNotifications = false
	return recheck, errors.Join(err, s.executeDelegateMutationPlans(plans))
}
