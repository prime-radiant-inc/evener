package agent

import (
	"context"
	"errors"
	"sort"
	"strings"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

type delegateStopState struct {
	requestSeq  uint64
	targetID    string
	members     map[string]struct{}
	active      map[delegateLease]struct{}
	starts      map[uint64]struct{}
	work        map[delegateWorkToken]string
	deliveries  map[delegateDeliveryToken]struct{}
	quietClaims map[uint64]struct{}
	waiters     []*delegateInlineWaiter
	done        chan struct{}
	progress    chan struct{}
}

type delegateStopResult struct {
	id                string
	previousLifecycle delegateLifecycle
	lifecycle         delegateLifecycle
	outcome           string
	requestSeq        uint64
	done              <-chan struct{}
}

type delegateCancelPlan struct {
	requestSeq uint64
	targetID   string
	cancel     []context.CancelFunc
	children   []*Session
	shells     []delegateShellWork
	waiters    []*delegateInlineWaiter
}

func (c *delegateTreeController) StopSubtree(actor delegateActor, targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopSubtreeLocked(actor, targetID, false)
}

func (c *delegateTreeController) stopSubtreeLocked(actor delegateActor, targetID string, allowClosing bool) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error) {
	if c.stop != nil {
		if c.stop.targetID == targetID {
			if err := c.authorizeMutationLocked(actor, targetID); err != nil && !allowClosing {
				return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
			}
			return c.stopResultLocked(c.stop), c.cancelPlanForStopLocked(c.stop), delegateMutationPlans{}, nil
		}
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, errDelegateTargetBusy
	}
	if c.closing && !allowClosing {
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, errDelegateTargetBusy
	}
	if err := c.authorizeMutationLocked(actor, targetID); err != nil {
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
	}
	members := c.subtreeMembersLocked(targetID)
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: targetID,
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: targetID,
		},
	})
	if err != nil {
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
	}
	stop := &delegateStopState{
		requestSeq:  appended[0].Seq,
		targetID:    targetID,
		members:     members,
		active:      make(map[delegateLease]struct{}),
		starts:      make(map[uint64]struct{}),
		work:        make(map[delegateWorkToken]string),
		deliveries:  make(map[delegateDeliveryToken]struct{}),
		quietClaims: make(map[uint64]struct{}),
		done:        make(chan struct{}),
		progress:    make(chan struct{}, 1),
	}
	plan := delegateCancelPlan{requestSeq: stop.requestSeq, targetID: targetID}
	memberIDs := c.memberIDsLeafFirstLocked(members)
	for _, id := range memberIDs {
		if live := c.live[id]; live != nil && live.binding != nil {
			stop.active[live.binding.lease] = struct{}{}
			if live.binding.cancel != nil {
				plan.cancel = append(plan.cancel, live.binding.cancel)
			}
			if live.binding.runtime != nil {
				plan.children = append(plan.children, live.binding.runtime)
			}
		}
		reservationTokens := make([]uint64, 0)
		for token, reservation := range c.reservations {
			if reservation.delegateID == id {
				reservationTokens = append(reservationTokens, token)
			}
		}
		sort.Slice(reservationTokens, func(i, j int) bool { return reservationTokens[i] < reservationTokens[j] })
		for _, token := range reservationTokens {
			reservation := c.reservations[token]
			stop.starts[token] = struct{}{}
			if reservation.cancel != nil {
				plan.cancel = append(plan.cancel, reservation.cancel)
			}
		}
		workTokens := make([]uint64, 0)
		for token, work := range c.work {
			if work.owner.delegateID == id {
				workTokens = append(workTokens, token)
			}
		}
		sort.Slice(workTokens, func(i, j int) bool { return workTokens[i] < workTokens[j] })
		for _, token := range workTokens {
			work := c.work[token]
			stop.work[work.token] = work.jobID
			if work.committed {
				plan.shells = append(plan.shells, *work)
			}
		}
	}
	for _, receipt := range c.deliveries {
		if c.deliveryIntersectsMembersLocked(receipt, members) {
			stop.deliveries[receipt.token] = struct{}{}
		}
	}
	for token, claim := range c.quietClaims {
		if claim == nil {
			continue
		}
		if _, covered := members[claim.lease.delegateID]; covered {
			stop.quietClaims[token] = struct{}{}
		}
	}
	c.dropRuntimeClaimsForMembersLocked(members)
	claimIDs := make([]string, 0, len(c.deliveryClaims))
	for deliveryID := range c.deliveryClaims {
		claimIDs = append(claimIDs, deliveryID)
	}
	sort.Strings(claimIDs)
	for _, deliveryID := range claimIDs {
		claim := c.deliveryClaims[deliveryID]
		if claim == nil {
			continue
		}
		_, senderCovered := members[claim.delegateID]
		_, ownerCovered := members[claim.ownerID]
		if !senderCovered && !ownerCovered {
			continue
		}
		delete(c.deliveryClaims, deliveryID)
		stop.waiters = append(stop.waiters, claim.waiter)
		plan.waiters = append(plan.waiters, claim.waiter)
	}
	c.stop = stop
	c.evidenceVersion++
	updates := delegateMutationPlans{}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		updates.updates = append(updates.updates, c.capturedPlanLocked(id))
	}
	return c.stopResultLocked(stop), plan, updates, nil
}

func (c *delegateTreeController) cancelPlanForStopLocked(stop *delegateStopState) delegateCancelPlan {
	plan := delegateCancelPlan{requestSeq: stop.requestSeq, targetID: stop.targetID}
	plan.waiters = append(plan.waiters, stop.waiters...)
	for _, id := range c.memberIDsLeafFirstLocked(stop.members) {
		if live := c.live[id]; live != nil && live.binding != nil {
			if _, active := stop.active[live.binding.lease]; active && live.binding.cancel != nil {
				plan.cancel = append(plan.cancel, live.binding.cancel)
			}
			if _, active := stop.active[live.binding.lease]; active && live.binding.runtime != nil {
				plan.children = append(plan.children, live.binding.runtime)
			}
		}
		reservationTokens := make([]uint64, 0)
		for token := range stop.starts {
			if reservation := c.reservations[token]; reservation != nil && reservation.delegateID == id {
				reservationTokens = append(reservationTokens, token)
			}
		}
		sort.Slice(reservationTokens, func(i, j int) bool { return reservationTokens[i] < reservationTokens[j] })
		for _, token := range reservationTokens {
			if cancel := c.reservations[token].cancel; cancel != nil {
				plan.cancel = append(plan.cancel, cancel)
			}
		}
		workTokens := make([]uint64, 0)
		for token, work := range c.work {
			if work.owner.delegateID == id {
				if _, tracked := stop.work[work.token]; tracked {
					workTokens = append(workTokens, token)
				}
			}
		}
		sort.Slice(workTokens, func(i, j int) bool { return workTokens[i] < workTokens[j] })
		for _, token := range workTokens {
			work := c.work[token]
			if work.committed {
				plan.shells = append(plan.shells, *work)
			}
		}
	}
	return plan
}

func (c *delegateTreeController) memberIDsLeafFirstLocked(members map[string]struct{}) []string {
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		leftDepth := c.delegateDepthLocked(ids[i])
		rightDepth := c.delegateDepthLocked(ids[j])
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return ids[i] < ids[j]
	})
	return ids
}

func (c *delegateTreeController) delegateDepthLocked(delegateID string) int {
	depth := 0
	for delegateID != "" {
		aggregate := c.durable[delegateID]
		if aggregate == nil {
			break
		}
		depth++
		delegateID = aggregate.Descriptor.ParentDelegateID
	}
	return depth
}

func (c *delegateTreeController) stopResultLocked(stop *delegateStopState) delegateStopResult {
	previous := delegateLifecycleIdle
	if aggregate := c.durable[stop.targetID]; aggregate != nil && aggregate.CurrentRunOpen {
		previous = delegateLifecycleRunning
	}
	return delegateStopResult{
		id:                stop.targetID,
		previousLifecycle: previous,
		lifecycle:         delegateLifecycleIdle,
		outcome:           "stopped",
		requestSeq:        stop.requestSeq,
		done:              stop.done,
	}
}

func (c *delegateTreeController) subtreeMembersLocked(targetID string) map[string]struct{} {
	members := map[string]struct{}{targetID: {}}
	changed := true
	for changed {
		changed = false
		for id, aggregate := range c.durable {
			if aggregate == nil {
				continue
			}
			if _, included := members[id]; included {
				continue
			}
			if _, parentIncluded := members[aggregate.Descriptor.ParentDelegateID]; parentIncluded {
				members[id] = struct{}{}
				changed = true
			}
		}
	}
	return members
}

func (c *delegateTreeController) stopCoversLocked(delegateID string) bool {
	if c.stop == nil {
		return false
	}
	_, covered := c.stop.members[delegateID]
	return covered
}

func (c *delegateTreeController) deliveryIntersectsMembersLocked(receipt *delegateDeliveryAdmission, members map[string]struct{}) bool {
	if receipt == nil {
		return false
	}
	_, senderCovered := members[receipt.delegateID]
	_, ownerCovered := members[receipt.ownerID]
	return senderCovered || ownerCovered
}

func (c *delegateTreeController) CloseResumability(actor delegateActor, delegateID, reason string) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
		return delegateMutationPlans{}, err
	}
	if strings.TrimSpace(reason) == "" {
		return delegateMutationPlans{}, errDelegateTargetBusy
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:               delegatestore.EventDelegateResumabilityClosed,
		DelegateID:         delegateID,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: reason},
	}); err != nil {
		return delegateMutationPlans{}, err
	}
	return delegateMutationPlans{updates: []delegateUpdatePlan{c.capturedPlanLocked(delegateID)}}, nil
}

// Close fences the whole controller before joining or starting teardown. It
// never creates a second stop: each root subtree is drained through the same
// durable stop operation, in stable order, before the next one can begin.
func (c *delegateTreeController) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.evidenceVersion++
	}
	pending := c.stop
	pendingCancelPlan := delegateCancelPlan{}
	joinedMembers := make(map[string]struct{})
	allMembers := make(map[string]struct{}, len(c.durable))
	for id := range c.durable {
		allMembers[id] = struct{}{}
	}
	children := make([]*Session, 0, len(allMembers))
	for _, id := range c.memberIDsLeafFirstLocked(allMembers) {
		if live := c.live[id]; live != nil && live.runtime != nil {
			children = append(children, live.runtime)
		}
	}
	if pending != nil {
		pendingCancelPlan = c.cancelPlanForStopLocked(pending)
		for id := range pending.members {
			joinedMembers[id] = struct{}{}
		}
	}
	c.mu.Unlock()
	children = append(children, pendingCancelPlan.children...)
	if pending != nil {
		executeDelegateCancelPlan(pendingCancelPlan)
		if err := c.drainStopForClose(ctx, pending); err != nil {
			return err
		}
	}

	c.mu.Lock()
	roots := make([]string, 0)
	for id, aggregate := range c.durable {
		if aggregate != nil && aggregate.Descriptor.ParentDelegateID == "" {
			roots = append(roots, id)
		}
	}
	c.mu.Unlock()
	sort.Strings(roots)
	for _, rootID := range roots {
		if _, joined := joinedMembers[rootID]; joined {
			continue
		}
		c.mu.Lock()
		aggregate := c.durable[rootID]
		missing := aggregate == nil
		c.mu.Unlock()
		if missing {
			continue
		}
		result, cancelPlan, _, err := c.stopSubtreeForClose(rootID)
		if err != nil {
			return err
		}
		executeDelegateCancelPlan(cancelPlan)
		children = append(children, cancelPlan.children...)
		if err := c.drainStopForClose(ctx, c.stopForResult(result)); err != nil {
			return err
		}
	}
	closed := make(map[*Session]struct{}, len(children))
	for _, child := range children {
		if child == nil {
			continue
		}
		if _, duplicate := closed[child]; duplicate {
			continue
		}
		closed[child] = struct{}{}
		child.Close()
	}
	return c.store.Close()
}

func (c *delegateTreeController) stopSubtreeForClose(targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopSubtreeLocked(rootDelegateActor(c.rootSessionID), targetID, true)
}

func (c *delegateTreeController) stopForResult(result delegateStopResult) *delegateStopState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil && c.stop.requestSeq == result.requestSeq {
		return c.stop
	}
	return nil
}

func (c *delegateTreeController) drainStopForClose(ctx context.Context, stop *delegateStopState) error {
	if stop == nil {
		return nil
	}
	for {
		select {
		case <-stop.done:
			return nil
		default:
		}
		requirements := c.ReconcileRequirements()
		evidence, err := collectDelegateReconcileEvidence(c.stateDir, requirements)
		if err != nil {
			return err
		}
		plans, err := c.Reconcile(evidence)
		if err != nil {
			if errors.Is(err, errDelegateTargetBusy) {
				continue
			}
			return err
		}
		for _, plan := range plans.shellRepairs {
			if err := executeDelegateShellRepair(plan, c.now()); err != nil {
				return err
			}
		}
		for _, plan := range plans.attention {
			if err := c.executeDelegateAttentionCleanup(plan); err != nil {
				if errors.Is(err, errDelegateStaleLease) {
					continue
				}
				return err
			}
		}
		if len(plans.attention) != 0 || len(plans.shellRepairs) != 0 {
			continue
		}
		if !delegateStopDone(stop) {
			if err := waitForDelegateStopProgress(ctx, stop); err != nil {
				return err
			}
		}
	}
}

func (c *delegateTreeController) signalStopProgressLocked() {
	if c.stop == nil {
		return
	}
	select {
	case c.stop.progress <- struct{}{}:
	default:
	}
}

func waitForDelegateStopProgress(ctx context.Context, stop *delegateStopState) error {
	select {
	case <-stop.done:
		return nil
	case <-stop.progress:
		return nil
	default:
	}
	select {
	case <-stop.done:
		return nil
	case <-stop.progress:
		return nil
	case <-ctx.Done():
		// Exact progress wins over cancellation when both become observable at
		// the wait boundary, so close gets one final evidence recollection.
		select {
		case <-stop.done:
			return nil
		case <-stop.progress:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func executeDelegateCancelPlan(plan delegateCancelPlan) {
	for _, cancel := range plan.cancel {
		if cancel != nil {
			cancel()
		}
	}
	for _, shell := range plan.shells {
		if shell.cancel != nil {
			shell.cancel()
		}
	}
	for _, waiter := range plan.waiters {
		resolveDelegateInlineClaim(waiter, delegateInlineResolution{fallback: true})
	}
}

func delegateStopDone(stop *delegateStopState) bool {
	if stop == nil {
		return true
	}
	select {
	case <-stop.done:
		return true
	default:
		return false
	}
}
