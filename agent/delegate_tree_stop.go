package agent

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

type delegateStopState struct {
	requestSeq        uint64
	targetID          string
	previousLifecycle delegateLifecycle
	outcome           string
	members           map[string]struct{}
	active            map[delegateLease]struct{}
	starts            map[uint64]struct{}
	work              map[delegateWorkToken]string
	deliveries        map[delegateDeliveryToken]struct{}
	quietClaims       map[uint64]struct{}
	steeringClaims    map[uint64]struct{}
	modelClaims       map[uint64]struct{}
	settlementClaims  map[uint64]struct{}
	watchEnqueues     map[uint64]struct{}
	watchDeliveries   map[uint64]struct{}
	waiters           []*delegateInlineWaiter
	done              chan struct{}
	progress          chan struct{}
	driver            *delegateStopDriver
}

type delegateStopDriver struct {
	done chan struct{}
	err  error
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
	for {
		c.mu.Lock()
		if c.stop == nil {
			if err := c.authorizeMutationLocked(actor, targetID); err != nil {
				c.mu.Unlock()
				return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
			}
			if blocker := c.attentionStartBlockerLocked(targetID, false); blocker != nil {
				c.mu.Unlock()
				<-blocker
				continue
			}
		}
		result, cancel, plans, err := c.stopSubtreeLocked(actor, targetID, false)
		c.mu.Unlock()
		return result, cancel, plans, err
	}
}

// StopSubtreeAndDrive admits the durable stop and gives its reconciliation to
// the root runtime. The driver is process-only and unique for the exact stop;
// callers may stop waiting without stopping reconciliation.
func (c *delegateTreeController) StopSubtreeAndDrive(actor delegateActor, targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error) {
	for {
		c.mu.Lock()
		if c.stop == nil {
			if err := c.authorizeMutationLocked(actor, targetID); err != nil {
				c.mu.Unlock()
				return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
			}
			if blocker := c.attentionStartBlockerLocked(targetID, false); blocker != nil {
				c.mu.Unlock()
				<-blocker
				continue
			}
		}
		break
	}
	if c.closing {
		c.mu.Unlock()
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, errDelegateTargetBusy
	}
	if c.rootRuntime == nil {
		c.mu.Unlock()
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, errors.New("delegate root runtime is unavailable")
	}
	if c.stop == nil && c.stopDriver != nil {
		select {
		case <-c.stopDriver.done:
		default:
			c.mu.Unlock()
			return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, errDelegateTargetBusy
		}
	}
	result, cancelPlan, plans, err := c.stopSubtreeLocked(actor, targetID, false)
	if err != nil {
		c.mu.Unlock()
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
	}
	stop := c.stop
	root := c.rootRuntime
	startDriver := false
	if stop != nil && stop.driver == nil {
		stop.driver = &delegateStopDriver{done: make(chan struct{})}
		c.stopDriver = stop.driver
		startDriver = true
	} else if stop != nil && stop.driver.err != nil {
		select {
		case <-stop.driver.done:
			stop.driver = &delegateStopDriver{done: make(chan struct{})}
			c.stopDriver = stop.driver
			startDriver = true
		default:
		}
	}
	driver := stop.driver
	c.mu.Unlock()
	if startDriver {
		go c.runStopReconcileDriver(stop, driver, root)
	}
	return result, cancelPlan, plans, nil
}

func (c *delegateTreeController) attentionStartBlockerLocked(targetID string, acceptedOnly bool) <-chan struct{} {
	members := c.subtreeMembersLocked(targetID)
	tokens := make([]uint64, 0)
	for token, record := range c.reservations {
		if record == nil || record.trigger != delegatestore.TriggerAttention || record.done == nil || acceptedOnly && !record.attentionAdmitted {
			continue
		}
		if _, covered := members[record.delegateID]; covered {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	slices.Sort(tokens)
	return c.reservations[tokens[0]].done
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
	previousLifecycle, outcome := classifyDelegateStopAdmission(c.durable, targetID, members)
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: targetID,
		// TS is what the fold turns into Aggregate.PendingStopAt: a caller asking
		// whether the target can still honour this stop needs how LONG it has
		// been pending, and the request sequence is an ordering, not a clock.
		TS: c.now(),
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: targetID,
		},
	})
	if err != nil {
		return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, err
	}
	stop := &delegateStopState{
		requestSeq:        appended[0].Seq,
		targetID:          targetID,
		previousLifecycle: previousLifecycle,
		outcome:           outcome,
		members:           members,
		active:            make(map[delegateLease]struct{}),
		starts:            make(map[uint64]struct{}),
		work:              make(map[delegateWorkToken]string),
		deliveries:        make(map[delegateDeliveryToken]struct{}),
		quietClaims:       make(map[uint64]struct{}),
		steeringClaims:    make(map[uint64]struct{}),
		modelClaims:       make(map[uint64]struct{}),
		settlementClaims:  make(map[uint64]struct{}),
		watchEnqueues:     make(map[uint64]struct{}),
		watchDeliveries:   make(map[uint64]struct{}),
		done:              make(chan struct{}),
		progress:          make(chan struct{}, 1),
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
		slices.Sort(reservationTokens)
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
		slices.Sort(workTokens)
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
	for token, claim := range c.steeringClaims {
		if claim != nil {
			if _, covered := members[claim.delegateID]; covered {
				stop.steeringClaims[token] = struct{}{}
			}
		}
	}
	for token, claim := range c.modelClaims {
		if claim != nil {
			if _, covered := members[claim.lease.delegateID]; covered {
				stop.modelClaims[token] = struct{}{}
			}
		}
	}
	for token, claim := range c.settlementClaims {
		if claim != nil {
			if _, covered := members[claim.lease.delegateID]; covered {
				stop.settlementClaims[token] = struct{}{}
			}
		}
	}
	for token, receipt := range c.watchEnqueues {
		if receipt != nil && c.stopReceiptIntersectsMembersLocked(receipt, members) {
			stop.watchEnqueues[token] = struct{}{}
		}
	}
	for token, receipt := range c.watchDeliveries {
		if receipt != nil && c.stopReceiptIntersectsMembersLocked(receipt, members) {
			stop.watchDeliveries[token] = struct{}{}
		}
	}
	c.stop = stop
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

func (c *delegateTreeController) stopReceiptIntersectsMembersLocked(receipt *delegateWatchReceipt, members map[string]struct{}) bool {
	if receipt == nil {
		return false
	}
	_, sourceCovered := members[receipt.sourceDelegateID]
	_, receiverCovered := members[receipt.receiverDelegateID]
	return sourceCovered || receiverCovered
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
		slices.Sort(reservationTokens)
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
		slices.Sort(workTokens)
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
	return delegateStopResult{
		id:                stop.targetID,
		previousLifecycle: stop.previousLifecycle,
		lifecycle:         delegateLifecycleIdle,
		outcome:           stop.outcome,
		requestSeq:        stop.requestSeq,
		done:              stop.done,
	}
}

func classifyDelegateStopAdmission(state delegatestore.State, targetID string, members map[string]struct{}) (delegateLifecycle, string) {
	previousLifecycle := delegateLifecycleIdle
	if aggregate := state[targetID]; aggregate != nil && aggregate.CurrentRunOpen {
		previousLifecycle = delegateLifecycleRunning
	}
	for id := range members {
		if aggregate := state[id]; aggregate != nil && aggregate.CurrentRunOpen {
			return previousLifecycle, "cancelled_by_request"
		}
	}
	return previousLifecycle, "already_idle"
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
	for {
		if c.closing {
			c.mu.Unlock()
			return delegateMutationPlans{}, errDelegateTargetBusy
		}
		if err := c.authorizeMutationLocked(actor, delegateID); err != nil {
			c.mu.Unlock()
			return delegateMutationPlans{}, err
		}
		if strings.TrimSpace(reason) == "" {
			c.mu.Unlock()
			return delegateMutationPlans{}, errDelegateTargetBusy
		}
		if blocker := c.attentionStartBlockerLocked(delegateID, true); blocker != nil {
			c.mu.Unlock()
			<-blocker
			c.mu.Lock()
			continue
		}
		break
	}
	defer c.mu.Unlock()
	plan, err := c.appendResumabilityClosureLocked(delegateID, delegatestore.Event{
		Kind:               delegatestore.EventDelegateResumabilityClosed,
		DelegateID:         delegateID,
		ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: reason},
	})
	if err != nil {
		return delegateMutationPlans{}, err
	}
	return delegateMutationPlans{updates: []delegateUpdatePlan{plan}}, nil
}

// Close fences the whole controller before joining or starting teardown. It
// never creates a second stop: each root subtree is drained through the same
// durable stop operation, in stable order, before the next one can begin.
func (c *delegateTreeController) Close(ctx context.Context) error {
	if err := c.closeRuntimeTree(ctx, nil); err != nil {
		return err
	}
	return c.store.Close()
}

// closeRuntimeTree fences admission, durably stops every stable root, joins the
// exact stop reconciliation, and tears resident sessions down leaf-first. Every
// resident runtime is a child session — on its owner's environment or on a
// clone sharing the owner's process table — so the default policy is the child
// teardown (teardownChildSession, scratch retained), never Session.Close. The
// caller may supply its own policy; root Session shutdown does, so it keeps its
// shared environment alive until its own final cleanup. ownsEnv is the owner's
// record of whether the child's environment was built for it. The delegate
// store remains open for subsequent worktree disposal evidence.
func (c *delegateTreeController) closeRuntimeTree(ctx context.Context, closeChild func(child *Session, ownsEnv bool)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if closeChild == nil {
		closeChild = func(child *Session, ownsEnv bool) {
			teardownChildSession(ctx, child, ownsEnv, retainChildScratch)
		}
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
	// Each resident runtime's owner, read now while the tree is intact: the
	// close policy asks the owner whether the child's environment is its own.
	owners := make(map[*Session]*Session, len(allMembers))
	for _, id := range c.memberIDsLeafFirstLocked(allMembers) {
		live := c.live[id]
		if live == nil {
			continue
		}
		if aggregate := c.durable[id]; aggregate != nil {
			if live.runtime != nil {
				owners[live.runtime] = c.ownerRuntimeLocked(aggregate)
			}
			if live.binding != nil && live.binding.runtime != nil {
				owners[live.binding.runtime] = c.ownerRuntimeLocked(aggregate)
			}
		}
		if live.runtime != nil {
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
		stopCtx, cancelStop := closeStopJoinContext(ctx)
		err := c.joinOrDrainStopForClose(stopCtx, pending)
		cancelStop()
		if err != nil {
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
		result, cancelPlan, _, err := c.stopSubtreeForClose(ctx, rootID)
		if err != nil {
			return err
		}
		executeDelegateCancelPlan(cancelPlan)
		children = append(children, cancelPlan.children...)
		stopCtx, cancelStop := closeStopJoinContext(ctx)
		err = c.drainStopForClose(stopCtx, c.stopForResult(result))
		cancelStop()
		if err != nil {
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
		closeChild(child, owners[child].ownsChildEnvironment(child))
	}
	if _, err := c.joinStopReconcileDriver(ctx); err != nil {
		return err
	}
	return nil
}

func (c *delegateTreeController) stopSubtreeForClose(ctx context.Context, targetID string) (delegateStopResult, delegateCancelPlan, delegateMutationPlans, error) {
	for {
		c.mu.Lock()
		if c.stop == nil {
			if blocker := c.attentionStartBlockerLocked(targetID, false); blocker != nil {
				c.mu.Unlock()
				select {
				case <-blocker:
				case <-ctx.Done():
					return delegateStopResult{}, delegateCancelPlan{}, delegateMutationPlans{}, ctx.Err()
				}
				continue
			}
		}
		result, cancel, plans, err := c.stopSubtreeLocked(rootDelegateActor(c.rootSessionID), targetID, true)
		c.mu.Unlock()
		return result, cancel, plans, err
	}
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
	return c.drainStop(ctx, stop, nil)
}

func (c *delegateTreeController) drainStop(ctx context.Context, stop *delegateStopState, root *Session) error {
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
		if root != nil {
			rootPlans := plans
			rootPlans.attention = nil
			if err := root.executeDelegateMutationPlans(rootPlans); err != nil {
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

func (c *delegateTreeController) runStopReconcileDriver(stop *delegateStopState, driver *delegateStopDriver, root *Session) {
	err := c.drainStop(context.Background(), stop, root)
	c.mu.Lock()
	driver.err = err
	close(driver.done)
	c.mu.Unlock()
}

func (c *delegateTreeController) joinOrDrainStopForClose(ctx context.Context, stop *delegateStopState) error {
	c.mu.Lock()
	driver := stop.driver
	c.mu.Unlock()
	if driver == nil {
		return c.drainStopForClose(ctx, stop)
	}
	joined, err := c.joinExactStopReconcileDriver(ctx, driver)
	if !joined {
		return err
	}
	if err != nil {
		if retryErr := c.drainStopForClose(ctx, stop); retryErr != nil {
			return retryErr
		}
		c.mu.Lock()
		if stop.driver == driver {
			stop.driver = nil
		}
		if c.stopDriver == driver {
			c.stopDriver = nil
		}
		c.mu.Unlock()
	}
	return nil
}

func (c *delegateTreeController) joinStopReconcileDriver(ctx context.Context) (bool, error) {
	c.mu.Lock()
	driver := c.stopDriver
	c.mu.Unlock()
	return c.joinExactStopReconcileDriver(ctx, driver)
}

func (c *delegateTreeController) closeStoreAfterStopReconcileDriver(ctx context.Context) error {
	c.mu.Lock()
	if !c.closing {
		c.closing = true
		c.evidenceVersion++
	}
	driver := c.stopDriver
	c.mu.Unlock()
	joined, driverErr := c.joinExactStopReconcileDriver(ctx, driver)
	if !joined {
		return driverErr
	}
	return errors.Join(driverErr, c.store.Close())
}

func (c *delegateTreeController) joinExactStopReconcileDriver(ctx context.Context, driver *delegateStopDriver) (bool, error) {
	if driver == nil {
		return true, nil
	}
	select {
	case <-driver.done:
		c.mu.Lock()
		err := driver.err
		c.mu.Unlock()
		return true, err
	case <-ctx.Done():
		select {
		case <-driver.done:
			c.mu.Lock()
			err := driver.err
			c.mu.Unlock()
			return true, err
		default:
			return false, ctx.Err()
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
