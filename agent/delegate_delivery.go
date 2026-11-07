package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/transcript"
)

var errDelegateDeliveryReceiverUnavailable = errors.New("delegate delivery receiver is unavailable")

type delegateWaiterToken struct{ id uint64 }

type delegateInlineWaiter struct {
	token       delegateWaiterToken
	generation  uint64
	resolution  chan delegateInlineResolution
	resolveOnce sync.Once
}

type delegateInlineResolution struct {
	packet   *delegatestore.TerminalPacket
	commit   *delegateToolResultCommit
	earlier  []delegateInlineEarlier
	fallback bool
}

// delegateInlineEarlier is an earlier result of the same delegate that the
// waiting caller had not yet received, which the inline reply carries ahead of
// its own (#3906, #3951). Its commit completes before the reply's.
type delegateInlineEarlier struct {
	packet delegatestore.TerminalPacket
	commit *delegateToolResultCommit
}

type delegateToolResultCommit struct {
	controller *delegateTreeController
	token      delegateDeliveryToken
	deliveryID string
}

type delegateDeliveryToken struct {
	processID  uint64
	deliveryID string
}

type delegateDeliveryAdmission struct {
	token      delegateDeliveryToken
	delegateID string
	ownerID    string
	claim      delegateDeliveryClaimToken
	cold       bool
	inline     bool
	retryable  bool
	// heldClaim is the receiver's own claim on a held delivery an inline
	// reply took over, restored if the reply's tool round is abandoned.
	heldClaim *delegateDeliveryClaim
}

type delegateDeliveryClaimToken struct {
	processID  uint64
	deliveryID string
}

// errDelegateDeliveryRetry reports a delivery plan the controller refused
// because its owner is beginning an attention turn. Nothing is in flight and
// nothing errored, but the owner cannot take an appended attention row in the
// middle of that start, so the caller must retry the plan rather than treat
// the refusal as a completed delivery. A stop, a close, or a missing owner is
// permanent and stays a nil refusal.
var errDelegateDeliveryRetry = errors.New("delegate delivery refused; retry")

type delegateDeliveryClaim struct {
	token      delegateDeliveryClaimToken
	delegateID string
	ownerID    string
	waiter     *delegateInlineWaiter
	cold       bool
	// held marks a plan its receiver deferred to its next round because it
	// was mid-turn.
	held bool
	// startRefused marks the claim a transient refusal kept for its retry
	// while the owner was beginning an attention turn. Such a claim is
	// deferred delivery, not work in flight, so a start admission must not
	// count it.
	startRefused bool
}

type delegateDeliveryPlan struct {
	controller      *delegateTreeController
	delegateID      string
	name            string
	deliveryID      string
	ownerDelegateID string
	waiter          *delegateInlineWaiter
	packet          delegatestore.TerminalPacket
	claim           delegateDeliveryClaimToken
	receiver        delegateDeliveryReceiver
	callerCommitted bool
	// carried is every delivery queued ahead of this waiter-bearing plan's own,
	// oldest first, which its reply carries (#3906, #3951): the delegate's head,
	// which its receiver is holding for its next round, and any behind it.
	// heldClaim is the receiver's claim on the head, which this plan's claim
	// replaces until it is admitted or refused.
	carried   []delegatestore.PendingDelivery
	heldClaim *delegateDeliveryClaim
}

type delegateDeliveryReceiver interface {
	appendDelegateNotificationDurably(attentionID, content string) (bool, error)
}

type coldDelegateDeliveryReceiver struct {
	stateDir      string
	transcriptRef string
	now           func() time.Time
	open          delegateAttentionWriterOpener
}

func (receiver coldDelegateDeliveryReceiver) appendDelegateNotificationDurably(attentionID, content string) (bool, error) {
	path, sessionID, err := delegateTranscriptPathFromRef(receiver.stateDir, receiver.transcriptRef)
	if err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if receiver.now != nil {
		now = receiver.now()
	}
	open := receiver.open
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	return appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, content, now, open)
}

type committedCallerDeliveryReceiver struct{}

func (committedCallerDeliveryReceiver) appendDelegateNotificationDurably(string, string) (bool, error) {
	return false, nil
}

func (c *delegateTreeController) RegisterInlineWaiter(reservation *delegateStartReservation) (*delegateInlineWaiter, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return nil, errDelegateTargetBusy
	}
	record, err := c.reservationRecordLocked(reservation)
	if err != nil || record.waiter != nil {
		return nil, errDelegateTargetBusy
	}
	c.nextToken++
	waiter := &delegateInlineWaiter{
		token:      delegateWaiterToken{id: c.nextToken},
		generation: record.generation,
		resolution: make(chan delegateInlineResolution, 1),
	}
	record.waiter = waiter
	c.evidenceVersion++
	return waiter, nil
}

func (c *delegateTreeController) waitForDelegateInline(ctx context.Context, waiter *delegateInlineWaiter) delegateInlineResolution {
	if waiter == nil {
		return delegateInlineResolution{fallback: true}
	}
	select {
	case resolution := <-waiter.resolution:
		return resolution
	case <-ctx.Done():
	}

	c.mu.Lock()
	withdrawn := false
	for _, live := range c.live {
		if live == nil || live.waiters == nil || live.waiters[waiter.generation] != waiter {
			continue
		}
		delete(live.waiters, waiter.generation)
		c.evidenceVersion++
		withdrawn = true
		break
	}
	c.mu.Unlock()
	if withdrawn {
		return delegateInlineResolution{fallback: true}
	}
	return <-waiter.resolution
}

func (c *delegateTreeController) BeginDelivery(plan delegateDeliveryPlan) (_ delegateDeliveryToken, admitted bool, _ error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return delegateDeliveryToken{}, false, err
	}
	defer release()
	c.mu.Lock()
	defer c.mu.Unlock()
	retry := false
	defer func() {
		// A refused carrier gives its held delivery back to the receiver's
		// claim and drops its own, which would otherwise keep its delivery
		// from ever being planned once the held one is acknowledged.
		// A transient refusal keeps the plan exactly as it is: the same claim,
		// the same held claim, and the same carried batch are retried.
		if admitted || retry || len(plan.carried) == 0 {
			return
		}
		if claim := c.deliveryClaims[plan.carried[0].DeliveryID]; claim != nil && claim.token == plan.claim {
			c.restoreHeldClaimLocked(plan.heldClaim)
			c.evidenceVersion++
		}
		if claim := c.deliveryClaims[plan.deliveryID]; claim != nil && claim.token == plan.claim {
			delete(c.deliveryClaims, plan.deliveryID)
			c.evidenceVersion++
		}
	}()
	if plan.controller != c || plan.deliveryID == "" || plan.claim.deliveryID != plan.deliveryID {
		return delegateDeliveryToken{}, false, errDelegateStaleLease
	}
	if receipt := c.deliveryReceiptLocked(plan.deliveryID); receipt != nil {
		if !receipt.retryable || receipt.claim != plan.claim || receipt.delegateID != plan.delegateID || receipt.ownerID != plan.ownerDelegateID || plan.waiter != nil {
			return delegateDeliveryToken{}, false, nil
		}
		aggregate := c.durable[receipt.delegateID]
		if aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != plan.deliveryID || !reflect.DeepEqual(aggregate.PendingDeliveries[0].Packet, plan.packet) {
			return delegateDeliveryToken{}, false, nil
		}
		receipt.retryable = false
		c.evidenceVersion++
		return receipt.token, true, nil
	}
	claim := c.deliveryClaims[plan.deliveryID]
	if claim == nil || claim.token != plan.claim || claim.delegateID != plan.delegateID || claim.ownerID != plan.ownerDelegateID || claim.waiter != plan.waiter {
		return delegateDeliveryToken{}, false, nil
	}
	aggregate := c.durable[plan.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
		return delegateDeliveryToken{}, false, nil
	}
	head := aggregate.PendingDeliveries[0]
	if len(plan.carried) > 0 {
		if len(aggregate.PendingDeliveries) <= len(plan.carried) || !reflect.DeepEqual(aggregate.PendingDeliveries[:len(plan.carried)], plan.carried) {
			return delegateDeliveryToken{}, false, nil
		}
		head = aggregate.PendingDeliveries[len(plan.carried)]
	}
	if head.DeliveryID != plan.deliveryID || head.OwnerDelegateID != plan.ownerDelegateID || !reflect.DeepEqual(head.Packet, plan.packet) {
		return delegateDeliveryToken{}, false, nil
	}
	if c.deliveryStoppedLocked(plan.delegateID, plan.ownerDelegateID) {
		delete(c.deliveryClaims, plan.deliveryID)
		c.evidenceVersion++
		return delegateDeliveryToken{}, false, nil
	}
	if plan.ownerDelegateID != "" && c.hasAttentionStartReservationLocked(plan.ownerDelegateID) {
		// Transient: the owner is beginning an attention turn and cannot take
		// an appended attention row in the middle of that start. Keep the
		// claim so this exact plan stays valid for the retry the caller
		// queues; deleting it would strand the durable pending delivery. The
		// waiter, if any, was already answered with the fallback resolution by
		// deliverDelegatePacket, so clear it here too or the retry's waiter
		// match would fail and strand the claim permanently.
		claim.waiter = nil
		claim.startRefused = true
		retry = true
		return delegateDeliveryToken{}, false, errDelegateDeliveryRetry
	}
	delete(c.deliveryClaims, plan.deliveryID)
	cold := claim.cold
	admit := func(claim delegateDeliveryClaimToken) *delegateDeliveryAdmission {
		c.nextToken++
		receipt := &delegateDeliveryAdmission{
			token:      delegateDeliveryToken{processID: c.nextToken, deliveryID: claim.deliveryID},
			delegateID: plan.delegateID,
			ownerID:    plan.ownerDelegateID,
			claim:      claim,
			cold:       cold,
			inline:     plan.waiter != nil || plan.callerCommitted,
		}
		c.deliveries[receipt.token.processID] = receipt
		return receipt
	}
	if len(plan.carried) > 0 {
		delete(c.deliveryClaims, plan.carried[0].DeliveryID)
		// The held head is admitted under the receiver's own claim token, so
		// a retry of it is planned as that delivery's. The deliveries behind
		// it were never planned, so each gets a claim token of its own.
		admit(plan.heldClaim.token).heldClaim = plan.heldClaim
		for _, behind := range plan.carried[1:] {
			c.nextToken++
			admit(delegateDeliveryClaimToken{processID: c.nextToken, deliveryID: behind.DeliveryID})
		}
	}
	token := admit(plan.claim).token
	c.evidenceVersion++
	return token, true, nil
}

// deliveryStoppedLocked reports whether closing or a stop refuses
// delegateID's deliveries to ownerID for good.
func (c *delegateTreeController) deliveryStoppedLocked(delegateID, ownerID string) bool {
	if aggregate := c.durable[delegateID]; c.closing || aggregate == nil || aggregate.PendingStopSeq != 0 {
		return true
	}
	if owner := c.durable[ownerID]; ownerID != "" && (owner == nil || owner.PendingStopSeq != 0) {
		return true
	}
	return c.stopCoversLocked(delegateID) || c.stopCoversLocked(ownerID)
}

// restoreHeldClaimLocked gives a held delivery a reply took over back to its
// receiver's claim, so the plan the receiver holds delivers it as before,
// while it is still its delegate's head and no closing or stop refuses it for
// good; otherwise the claim is dropped, since it would outlive the delivery
// and block the owner. A transient attention start reservation is left to the
// held plan's own retry.
func (c *delegateTreeController) restoreHeldClaimLocked(held *delegateDeliveryClaim) {
	id := held.token.deliveryID
	delete(c.deliveryClaims, id)
	if !c.deliveryStoppedLocked(held.delegateID, held.ownerID) && len(c.durable[held.delegateID].PendingDeliveries) != 0 && c.durable[held.delegateID].PendingDeliveries[0].DeliveryID == id {
		c.deliveryClaims[id] = held
	}
}

func (c *delegateTreeController) CompleteDelivery(token delegateDeliveryToken, committed bool) (delegateMutationPlans, error) {
	defer c.retirementChanged()
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := c.deliveries[token.processID]
	if receipt == nil || receipt.token != token {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if !committed {
		delete(c.deliveries, token.processID)
		if receipt.heldClaim != nil && c.deliveryClaims[token.deliveryID] == nil {
			// The reply carrying this held result was abandoned.
			c.restoreHeldClaimLocked(receipt.heldClaim)
		}
		untrackStopLocked(c, func(s *delegateStopState) map[delegateDeliveryToken]struct{} { return s.deliveries }, token)
		c.evidenceVersion++
		return delegateMutationPlans{}, nil
	}
	aggregate := c.durable[receipt.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != token.deliveryID {
		delete(c.deliveries, token.processID)
		untrackStopLocked(c, func(s *delegateStopState) map[delegateDeliveryToken]struct{} { return s.deliveries }, token)
		c.evidenceVersion++
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	events := make([]delegatestore.Event, 0, 2)
	ownerChanged := false
	attentionID := delegateAttentionID(token.deliveryID)
	if !receipt.inline && receipt.ownerID != "" {
		openEvent, err := c.delegateAttentionOpenEventLocked(receipt.ownerID)
		if err != nil {
			return delegateMutationPlans{}, err
		}
		if openEvent != nil {
			events = append(events, *openEvent)
			ownerChanged = true
		}
	}
	events = append(events, delegatestore.Event{
		Kind:       delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID: receipt.delegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{
			DeliveryID: token.deliveryID,
		},
	})
	if _, err := c.appendLocked(events...); err != nil {
		receipt.retryable = true
		c.evidenceVersion++
		return delegateMutationPlans{}, err
	}
	if !receipt.inline && receipt.ownerID != "" {
		c.noteDelegateAttentionLocked(receipt.ownerID, attentionID)
	}
	delete(c.deliveries, token.processID)
	untrackStopLocked(c, func(s *delegateStopState) map[delegateDeliveryToken]struct{} { return s.deliveries }, token)
	c.evidenceVersion++
	plans := delegateMutationPlans{}
	if ownerChanged {
		plans.updates = append(plans.updates, c.capturedPlanLocked(receipt.ownerID))
	}
	plans.updates = append(plans.updates, c.capturedPlanLocked(receipt.delegateID))
	plans.deliveries = append(plans.deliveries, c.replayDeliveriesForOwnerLocked(receipt.ownerID)...)
	return plans, nil
}

// PostUpdate records a running delegate's mid-work update in its journal,
// queued behind the delegate's earlier deliveries and ahead of the report its
// generation will settle, and plans the update's delivery when it is the
// delegate's head. A delegate parked in its finalization drain after its
// terminal communicate can still post: its report is prepared only when the
// settlement claim is taken, and the fold refuses an update from then on. It
// refuses a run that is no longer current, a delegate that is stopping or
// settling, and a blank message; the update is then not queued anywhere.
func (c *delegateTreeController) PostUpdate(actor delegateActor, message string) (string, delegateMutationPlans, error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	defer release()
	if actor.lease == nil {
		return "", delegateMutationPlans{}, errDelegateNotControllable
	}
	defer c.retirementChanged()
	lease := *actor.lease
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, err = c.admitLeaseLocked(lease, delegatestore.PhaseRunning)
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	if c.hasSettlementClaimLocked(lease) {
		return "", delegateMutationPlans{}, errDelegateTargetBusy
	}
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:         delegatestore.EventDelegateUpdatePosted,
		TS:           c.now(),
		DelegateID:   lease.delegateID,
		UpdatePosted: &delegatestore.UpdatePosted{Generation: lease.generation, Message: message},
	})
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	deliveryID := delegatestore.UpdateDeliveryID(lease.delegateID, appended[0].Seq)
	c.evidenceVersion++
	var plans delegateMutationPlans
	// Only the head is planned; an update behind an earlier delivery waits for
	// its turn rather than carrying the earlier one.
	if pending := c.durable[lease.delegateID].PendingDeliveries; len(pending) != 0 && pending[0].DeliveryID == deliveryID {
		if plan := c.newHeadDeliveryPlanLocked(lease.delegateID, deliveryID); plan != nil {
			plans.deliveries = append(plans.deliveries, *plan)
		}
	}
	return deliveryID, plans, nil
}

// deliveryPending reports whether delegateID's delivery queue still holds
// deliveryID unacknowledged.
func (c *delegateTreeController) deliveryPending(delegateID, deliveryID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[delegateID]
	return aggregate != nil && slices.ContainsFunc(aggregate.PendingDeliveries, func(pending delegatestore.PendingDelivery) bool {
		return pending.DeliveryID == deliveryID
	})
}

func (c *delegateTreeController) ReplayDeliveries() []delegateDeliveryPlan {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return nil
	}
	defer release()
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.durable))
	for id := range c.durable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	plans := make([]delegateDeliveryPlan, 0, len(ids))
	for _, id := range ids {
		aggregate := c.durable[id]
		if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
			continue
		}
		if plan := c.headDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans
}

// headDeliveryPlanLocked plans delegateID's head delivery: a retry when it is
// admitted and left retryable, otherwise a new plan.
func (c *delegateTreeController) headDeliveryPlanLocked(delegateID, deliveryID string) *delegateDeliveryPlan {
	if receipt := c.deliveryReceiptLocked(deliveryID); receipt != nil {
		return c.retryDeliveryPlanLocked(receipt)
	}
	return c.newHeadDeliveryPlanLocked(delegateID, deliveryID)
}

func prepareColdDelegateDeliveryReplay(c *delegateTreeController, plans []delegateDeliveryPlan) ([]delegateDeliveryPlan, error) {
	pending := make([]delegateDeliveryPlan, 0, len(plans))
	queue := append([]delegateDeliveryPlan(nil), plans...)
	for len(queue) != 0 {
		plan := queue[0]
		queue = queue[1:]
		committed, err := callerCommittedDelegateDelivery(c, plan)
		if err != nil {
			return nil, err
		}
		if !committed {
			pending = append(pending, plan)
			continue
		}
		plan.callerCommitted = true
		next, err := deliverDelegatePacket(plan, committedCallerDeliveryReceiver{})
		if errors.Is(err, errDelegateDeliveryRetry) {
			// A transient admission refusal. The controller kept the plan's
			// claim, so this exact parcel stays valid for the queue the caller
			// installs and its retry needs no store replay.
			pending = append(pending, plan)
			continue
		}
		if err != nil {
			return nil, err
		}
		queue = append(queue, next.deliveries...)
	}
	return pending, nil
}

func callerCommittedDelegateDelivery(c *delegateTreeController, plan delegateDeliveryPlan) (bool, error) {
	path, sessionID, err := c.deliveryTranscriptIdentity(plan.ownerDelegateID)
	if err != nil {
		return false, err
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		return false, err
	}
	if fold.deliveryCommits[plan.deliveryID] == "" {
		return false, nil
	}
	open := c.attentionOpen
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	writer, entries, err := open(path, sessionID)
	if err != nil {
		return false, err
	}
	defer func() { _ = writer.Close() }()
	verified, err := foldDelegateAttention(entries)
	if err != nil {
		return false, err
	}
	if verified.deliveryCommits[plan.deliveryID] == "" {
		return false, nil
	}
	if err := writer.EstablishDurability(); err != nil {
		return false, err
	}
	return true, nil
}

func deliverColdDelegatePacket(plan delegateDeliveryPlan) (delegateMutationPlans, error) {
	committed, err := callerCommittedDelegateDelivery(plan.controller, plan)
	if err != nil {
		return delegateMutationPlans{}, err
	}
	if committed {
		plan.callerCommitted = true
		return deliverDelegatePacket(plan, committedCallerDeliveryReceiver{})
	}
	return deliverDelegatePacket(plan, plan.receiver)
}

func (c *delegateTreeController) deliveryTranscriptIdentity(ownerDelegateID string) (string, string, error) {
	c.mu.Lock()
	stateDir := c.stateDir
	rootSessionID := c.rootSessionID
	transcriptRef := ""
	if ownerDelegateID != "" {
		if owner := c.durable[ownerDelegateID]; owner != nil {
			transcriptRef = owner.Descriptor.TranscriptRef
		}
	}
	c.mu.Unlock()
	if ownerDelegateID == "" {
		return transcriptPath(stateDir, rootSessionID), rootSessionID, nil
	}
	if transcriptRef == "" {
		return "", "", errDelegateDeliveryReceiverUnavailable
	}
	return delegateTranscriptPathFromRef(stateDir, transcriptRef)
}

func deliverDelegatePacket(plan delegateDeliveryPlan, receiver delegateDeliveryReceiver) (delegateMutationPlans, error) {
	if plan.controller == nil {
		resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{fallback: true})
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	// Re-plan a stale head before admission (see replanStaleDelivery).
	if replan := plan.controller.replanStaleDelivery(plan); replan != nil {
		plan = *replan
		receiver = plan.receiver
	}
	token, admitted, err := plan.controller.BeginDelivery(plan)
	if err != nil || !admitted {
		resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{fallback: true})
		return delegateMutationPlans{}, err
	}
	if plan.waiter != nil {
		packet := cloneDelegateTerminalPacket(plan.packet)
		resolution := delegateInlineResolution{
			packet: &packet,
			commit: &delegateToolResultCommit{controller: plan.controller, token: token, deliveryID: plan.deliveryID},
		}
		for _, earlier := range plan.carried {
			earlierToken := plan.controller.admittedDeliveryToken(earlier.DeliveryID)
			resolution.earlier = append(resolution.earlier, delegateInlineEarlier{
				packet: cloneDelegateTerminalPacket(earlier.Packet),
				commit: &delegateToolResultCommit{controller: plan.controller, token: earlierToken, deliveryID: earlierToken.deliveryID},
			})
		}
		resolveDelegateInlineClaim(plan.waiter, resolution)
		return delegateMutationPlans{}, nil
	}
	if plan.controller.deliveryReceiptIsInline(token) {
		return plan.controller.CompleteDelivery(token, true)
	}
	if receiver == nil {
		plans, completionErr := plan.controller.CompleteDelivery(token, false)
		return plans, errors.Join(errDelegateDeliveryReceiverUnavailable, completionErr)
	}
	content, err := delegateNotificationContent(plan)
	if err != nil {
		_, completionErr := plan.controller.CompleteDelivery(token, false)
		return delegateMutationPlans{}, errors.Join(err, completionErr)
	}
	attentionID := delegateAttentionID(plan.deliveryID)
	if _, err := receiver.appendDelegateNotificationDurably(attentionID, content); err != nil {
		_, completionErr := plan.controller.CompleteDelivery(token, false)
		return delegateMutationPlans{}, errors.Join(err, completionErr)
	}
	plans, err := plan.controller.CompleteDelivery(token, true)
	if err != nil {
		return plans, err
	}
	switch receiver := receiver.(type) {
	case *Session:
		if receiver.isRootDelegateAttentionReceiver() {
			err = receiver.armDelegateAttention(attentionID)
		} else {
			receiver.notify()
			plan.controller.notifyDelegateAttention()
		}
	case coldDelegateDeliveryReceiver:
		plan.controller.notifyDelegateAttention()
	}
	return plans, err
}

// replanStaleDelivery re-plans a delivery whose claim is gone but whose durable
// head is still this delivery. It reports nil when the durable state has moved
// on (the head was settled, replaced, or stopped), which is a permanent refusal.
// A caller-committed plan is excluded: rebuilding it from the store would drop
// the inline acknowledgement that committed cold path exists to produce.
func (c *delegateTreeController) replanStaleDelivery(plan delegateDeliveryPlan) *delegateDeliveryPlan {
	if plan.deliveryID == "" || len(plan.carried) != 0 || plan.callerCommitted {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, claimed := c.deliveryClaims[plan.deliveryID]; claimed || c.hasDeliveryReceiptLocked(plan.deliveryID) {
		return nil
	}
	aggregate := c.durable[plan.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
		return nil
	}
	if aggregate.PendingDeliveries[0].DeliveryID != plan.deliveryID {
		return nil
	}
	return c.newHeadDeliveryPlanLocked(plan.delegateID, plan.deliveryID)
}

func delegateAttentionID(deliveryID string) string { return "delegate:" + deliveryID }

// isDelegateUpdateAttentionID reports whether attentionID names a delegate
// update's delivery rather than a generation's report.
func isDelegateUpdateAttentionID(attentionID string) bool {
	deliveryID, ok := strings.CutPrefix(attentionID, delegateAttentionID(""))
	return ok && delegatestore.IsUpdateDeliveryID(deliveryID)
}

// completeDelegateDeliveryCommits acknowledges one tool call's delivery
// commits in order. A reply carrying earlier results acknowledges them, oldest
// first, ahead of its own (#3906, #3951), and the store takes them only in
// queue order, so after one fails the rest are left for the same retry rather
// than abandoned: they are already in the reply and must never come back as
// notifications.
func completeDelegateDeliveryCommits(commits []*delegateToolResultCommit) ([]delegateMutationPlans, error) {
	var completed []delegateMutationPlans
	for i, commit := range commits {
		plans, err := commit.Complete(true)
		if err != nil {
			for _, rest := range commits[i+1:] {
				rest.controller.leaveDeliveryForRetry(rest.token)
			}
			return completed, err
		}
		completed = append(completed, plans)
	}
	return completed, nil
}

// leaveDeliveryForRetry marks an admitted inline delivery retryable without
// acknowledging it, as a failed acknowledgment does.
func (c *delegateTreeController) leaveDeliveryForRetry(token delegateDeliveryToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt := c.deliveries[token.processID]; receipt != nil && receipt.token == token {
		receipt.retryable = true
		c.evidenceVersion++
	}
}

func (commit *delegateToolResultCommit) Complete(committed bool) (delegateMutationPlans, error) {
	if commit == nil || commit.controller == nil || commit.deliveryID != commit.token.deliveryID {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	return commit.controller.CompleteDelivery(commit.token, committed)
}

func (c *delegateTreeController) newHeadDeliveryPlanLocked(delegateID, deliveryID string) *delegateDeliveryPlan {
	if c.closing || deliveryID == "" {
		return nil
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.PendingStopSeq != 0 || len(aggregate.PendingDeliveries) == 0 {
		return nil
	}
	head := aggregate.PendingDeliveries[0]
	var carried []delegatestore.PendingDelivery
	var heldClaim *delegateDeliveryClaim
	if head.DeliveryID != deliveryID {
		var at int
		if heldClaim, at = c.carriableHeldClaimLocked(delegateID, aggregate.PendingDeliveries, deliveryID); heldClaim == nil {
			return nil
		}
		for _, pending := range aggregate.PendingDeliveries[:at] {
			pending.Packet = cloneDelegateTerminalPacket(pending.Packet)
			carried = append(carried, pending)
		}
		head = aggregate.PendingDeliveries[at]
	}
	if c.deliveryStoppedLocked(delegateID, head.OwnerDelegateID) {
		return nil
	}
	if _, claimed := c.deliveryClaims[deliveryID]; claimed || c.hasDeliveryReceiptLocked(deliveryID) {
		return nil
	}
	receiver := c.deliveryReceiverLocked(head.OwnerDelegateID)
	if head.OwnerDelegateID != "" && receiver == nil {
		return nil
	}
	_, cold := receiver.(coldDelegateDeliveryReceiver)
	if cold && c.hasColdDeliveryWorkForOwnerLocked(head.OwnerDelegateID) {
		return nil
	}
	c.nextToken++
	claimToken := delegateDeliveryClaimToken{processID: c.nextToken, deliveryID: deliveryID}
	// An update never answers an inline wait: the waiting caller wants the
	// generation's report, so the waiter stays registered for it, and the
	// report's reply carries the update ahead of itself
	// (carriableHeldClaimLocked).
	var waiter *delegateInlineWaiter
	if head.Packet.Kind != delegatestore.PacketUpdate {
		waiter = c.claimDelegateWaiterLocked(delegateID, head.Generation)
	}
	claim := &delegateDeliveryClaim{
		token:      claimToken,
		delegateID: delegateID,
		ownerID:    head.OwnerDelegateID,
		waiter:     waiter,
		cold:       cold,
	}
	c.deliveryClaims[deliveryID] = claim
	if len(carried) > 0 {
		// The held head's claim moves to this plan, so the plan its receiver
		// holds is no longer admitted and no new one is made.
		c.deliveryClaims[carried[0].DeliveryID] = claim
	}
	c.evidenceVersion++
	return &delegateDeliveryPlan{
		controller:      c,
		delegateID:      delegateID,
		name:            aggregate.Descriptor.Name,
		deliveryID:      head.DeliveryID,
		ownerDelegateID: head.OwnerDelegateID,
		waiter:          waiter,
		packet:          cloneDelegateTerminalPacket(head.Packet),
		claim:           claimToken,
		receiver:        receiver,
		carried:         carried,
		heldClaim:       heldClaim,
	}
}

// carriableHeldClaimLocked returns the receiver's claim on the delegate's head
// delivery, and deliveryID's place in the queue, when deliveryID's inline
// reply may carry every delivery ahead of it: its generation has an inline
// waiter, every delivery ahead of it goes to the same owner, the owner is
// holding the head for its next round, and none behind the head was planned.
// That waiter's caller is the owner, mid-tool-round, so without the reply the
// held result waits on the round, the round on the wait, and the wait sits
// out its ceiling (#3906); results queued behind the held one wait on it
// (#3951).
func (c *delegateTreeController) carriableHeldClaimLocked(delegateID string, pending []delegatestore.PendingDelivery, deliveryID string) (*delegateDeliveryClaim, int) {
	at := slices.IndexFunc(pending, func(p delegatestore.PendingDelivery) bool { return p.DeliveryID == deliveryID })
	if at < 1 {
		return nil, 0
	}
	if live := c.live[delegateID]; live == nil || live.waiters[pending[at].Generation] == nil {
		return nil, 0
	}
	for _, ahead := range pending[:at] {
		if ahead.OwnerDelegateID != pending[at].OwnerDelegateID {
			return nil, 0
		}
	}
	for _, behind := range pending[1:at] {
		if c.deliveryClaims[behind.DeliveryID] != nil || c.hasDeliveryReceiptLocked(behind.DeliveryID) {
			return nil, 0
		}
	}
	if claim := c.deliveryClaims[pending[0].DeliveryID]; claim != nil && claim.held {
		return claim, at
	}
	return nil, 0
}

func (c *delegateTreeController) hasColdDeliveryWorkForOwnerLocked(ownerDelegateID string) bool {
	for _, claim := range c.deliveryClaims {
		if claim != nil && claim.cold && claim.ownerID == ownerDelegateID {
			return true
		}
	}
	for _, receipt := range c.deliveries {
		if receipt != nil && receipt.cold && receipt.ownerID == ownerDelegateID {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) hasDeliveryWorkForOwnerLocked(ownerDelegateID string) bool {
	return c.deliveryWorkForOwnerLocked(ownerDelegateID, false)
}

// hasDeliveryWorkForOwnerBlockingStartLocked is hasDeliveryWorkForOwnerLocked
// for a start admission. A claim the transient delivery arm kept for its retry
// is deferred delivery, not work in flight: counting it would release the very
// attention start whose reservation deferred the delivery, after
// acceptDelegateAttention already consumed its trigger. The marker lives only
// on the claim and dies with it — every admission deletes the claim — so it
// can never suppress a later real delivery-work check.
func (c *delegateTreeController) hasDeliveryWorkForOwnerBlockingStartLocked(ownerDelegateID string) bool {
	return c.deliveryWorkForOwnerLocked(ownerDelegateID, true)
}

func (c *delegateTreeController) deliveryWorkForOwnerLocked(ownerDelegateID string, skipStartRefused bool) bool {
	if ownerDelegateID == "" {
		return false
	}
	for _, claim := range c.deliveryClaims {
		if claim == nil || claim.ownerID != ownerDelegateID {
			continue
		}
		if skipStartRefused && claim.startRefused {
			continue
		}
		return true
	}
	for _, receipt := range c.deliveries {
		if receipt != nil && receipt.ownerID == ownerDelegateID {
			return true
		}
	}
	return false
}

func (c *delegateTreeController) hasDeliveryReceiptLocked(deliveryID string) bool {
	return c.deliveryReceiptLocked(deliveryID) != nil
}

func (c *delegateTreeController) deliveryReceiptLocked(deliveryID string) *delegateDeliveryAdmission {
	for _, receipt := range c.deliveries {
		if receipt != nil && receipt.token.deliveryID == deliveryID {
			return receipt
		}
	}
	return nil
}

func (c *delegateTreeController) retryDeliveryPlanLocked(receipt *delegateDeliveryAdmission) *delegateDeliveryPlan {
	if receipt == nil || !receipt.retryable {
		return nil
	}
	aggregate := c.durable[receipt.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
		return nil
	}
	head := aggregate.PendingDeliveries[0]
	if head.DeliveryID != receipt.token.deliveryID || head.OwnerDelegateID != receipt.ownerID {
		return nil
	}
	receiver := c.deliveryReceiverLocked(receipt.ownerID)
	if !receipt.inline && receipt.ownerID != "" && receiver == nil {
		return nil
	}
	return &delegateDeliveryPlan{
		controller:      c,
		delegateID:      receipt.delegateID,
		name:            aggregate.Descriptor.Name,
		deliveryID:      head.DeliveryID,
		ownerDelegateID: head.OwnerDelegateID,
		packet:          cloneDelegateTerminalPacket(head.Packet),
		claim:           receipt.claim,
		receiver:        receiver,
	}
}

func (c *delegateTreeController) admittedDeliveryToken(deliveryID string) delegateDeliveryToken {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt := c.deliveryReceiptLocked(deliveryID); receipt != nil {
		return receipt.token
	}
	return delegateDeliveryToken{}
}

// holdDeliveryClaim marks plan's claim as held by its receiver for its next
// round, while the claim is still plan's.
func (c *delegateTreeController) holdDeliveryClaim(plan delegateDeliveryPlan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim := c.deliveryClaims[plan.deliveryID]; claim != nil && claim.token == plan.claim {
		claim.held = true
	}
}

func (c *delegateTreeController) deliveryReceiptIsInline(token delegateDeliveryToken) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := c.deliveries[token.processID]
	return receipt != nil && receipt.token == token && receipt.inline
}

func (c *delegateTreeController) replayDeliveriesForOwnerLocked(ownerDelegateID string) []delegateDeliveryPlan {
	ids := make([]string, 0, len(c.durable))
	for id := range c.durable {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	plans := make([]delegateDeliveryPlan, 0)
	for _, id := range ids {
		aggregate := c.durable[id]
		if aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].OwnerDelegateID != ownerDelegateID {
			continue
		}
		if plan := c.headDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans
}

func (c *delegateTreeController) deliveryReceiverLocked(ownerDelegateID string) delegateDeliveryReceiver {
	if ownerDelegateID == "" {
		return c.rootRuntime
	}
	if live := c.live[ownerDelegateID]; live != nil && live.runtime != nil {
		return live.runtime
	}
	if owner := c.durable[ownerDelegateID]; owner != nil && owner.Phase == delegatestore.PhaseIdle && owner.Descriptor.TranscriptRef != "" {
		return coldDelegateDeliveryReceiver{
			stateDir:      c.stateDir,
			transcriptRef: owner.Descriptor.TranscriptRef,
			now:           c.now,
			open:          c.attentionOpen,
		}
	}
	return nil
}

func (c *delegateTreeController) notifyDelegateAttention() {
	if c == nil {
		return
	}
	c.mu.Lock()
	root := c.rootRuntime
	c.mu.Unlock()
	if root != nil {
		root.notify()
	}
}

func (s *Session) executeDelegateMutationPlans(plans delegateMutationPlans) error {
	if plans.retirementRelease != nil {
		defer plans.retirementRelease()
	}
	if s == nil || s.delegateController == nil {
		return errDelegateDeliveryReceiverUnavailable
	}
	s.delegateController.emitDelegateUpdates(plans)
	for _, attention := range plans.attention {
		if err := s.delegateController.executeDelegateAttentionCleanup(attention); err != nil {
			if plans.attentionFinalization != nil {
				err = errors.Join(err, s.delegateController.RequireFinalizationRecovery(plans.attentionFinalization))
			}
			return err
		}
	}
	queue := append([]delegateDeliveryPlan(nil), plans.deliveries...)
	for i := 0; i < len(queue); i++ {
		plan := queue[i]
		next, err := s.deliverDelegatedPlan(plan)
		if errors.Is(err, errDelegateDeliveryRetry) {
			// The controller refused this parcel transiently and kept its
			// claim. Queue only this plan: the plans behind it were never
			// tried, so they must keep their own claims and waiters and go
			// through their normal paths. Passing a never-tried waiter-bearing
			// plan through the retry would clear its waiter while its claim
			// still held it, and the retry would then fail the waiter match
			// permanently. The refused plan is no longer in queue, so the loop
			// makes progress.
			s.delegateController.queueRefusedDelegateDelivery(plan)
			continue
		}
		if err != nil {
			s.requeueReplayableDelegateDeliveries(queue[i+1:])
			return err
		}
		s.delegateController.emitDelegateUpdates(next)
		queue = append(queue, next.deliveries...)
	}
	return nil
}

// deliverDelegatedPlan executes one delivery plan through the path its
// receiver requires: a session's own acceptance (which may defer it into that
// session's queue), a cold transcript writer, or a custom receiver. The
// controller's transient refusal is returned unchanged.
func (s *Session) deliverDelegatedPlan(plan delegateDeliveryPlan) (delegateMutationPlans, error) {
	if receiver, ok := plan.receiver.(*Session); ok {
		next, deferred, err := receiver.acceptDelegateDeliveryPlan(plan)
		if deferred {
			return delegateMutationPlans{}, nil
		}
		return next, err
	}
	if _, cold := plan.receiver.(coldDelegateDeliveryReceiver); cold {
		return deliverColdDelegatePacket(plan)
	}
	return deliverDelegatePacket(plan, plan.receiver)
}

// queueRefusedDelegateDelivery queues a plan the controller refused transiently
// on the session that will deliver it: the owner's live receiver, or the root
// when the owner has no resident session. The controller kept the plan's claim,
// so the plan in hand stays valid and is retried by that session's own flush.
// It returns the session it queued on, or nil when none could take the parcel;
// in that case it releases the kept claim so a later store re-plan
// (ReplayDeliveries) can pick the delivery up — a claimed delivery is never
// re-planned.
func (c *delegateTreeController) queueRefusedDelegateDelivery(plan delegateDeliveryPlan) *Session {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	receiver := c.deliveryReceiverLocked(plan.ownerDelegateID)
	target, _ := receiver.(*Session)
	fallback := c.rootRuntime
	c.mu.Unlock()
	if target == nil {
		target = fallback
	}
	if target != nil {
		if release, err := target.beginRetirementMutation("delegate_delivery"); err == nil {
			target.enqueueDelegateDelivery(delegateDeliveryRetryPlan(plan))
			release()
			return target
		} else if target != fallback && fallback != nil {
			if release, fallbackErr := fallback.beginRetirementMutation("delegate_delivery"); fallbackErr == nil {
				fallback.enqueueDelegateDelivery(delegateDeliveryRetryPlan(plan))
				release()
				return fallback
			}
		}
	}
	c.dropDeliveryClaim(plan)
	return nil
}

// dropDeliveryClaim releases plan's claim on its delivery without admitting it,
// so a parcel no session can retry is still re-plannable from the durable
// store.
func (c *delegateTreeController) dropDeliveryClaim(plan delegateDeliveryPlan) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if claim := c.deliveryClaims[plan.deliveryID]; claim != nil && claim.token == plan.claim {
		delete(c.deliveryClaims, plan.deliveryID)
		c.evidenceVersion++
	}
}

// delegateDeliveryRetryPlan prepares the plan in hand for its retry. A
// waiter-bearing plan cannot reach the transient arm — ReserveAttention and
// CommitStart both refuse while the owner has delivery work, so an owner that
// holds this plan's claim is never beginning an attention turn, and the stop
// and closing arms return the permanent nil refusal instead. The waiter is
// still cleared defensively so that if a future transient arm ever admits one,
// the retry delivers the packet as a notification instead of re-resolving a
// claim its caller already saw fall back; BeginDelivery's transient arm clears
// the claim's waiter in the same step, so the retry's waiter match holds.
// Carried plans are excluded on the same ground: their owner sits in a tool
// round.
func delegateDeliveryRetryPlan(plan delegateDeliveryPlan) delegateDeliveryPlan {
	plan.waiter = nil
	return plan
}

func (s *Session) requeueReplayableDelegateDeliveries(unprocessed []delegateDeliveryPlan) {
	if s == nil || s.delegateController == nil {
		return
	}
	plans := s.delegateController.ReplayDeliveries()
	if len(plans) == 0 && len(unprocessed) == 0 {
		return
	}
	s.delegateController.mu.Lock()
	fallback := s.delegateController.rootRuntime
	s.delegateController.mu.Unlock()
	if fallback == nil {
		fallback = s
	}
	// Queue each parcel on the session that will deliver it. A retry parked on
	// an ancestor that never pumps it (a root with no drain running, the shape
	// the orphan-arm wake test builds) is a lost report; the receiving session
	// flushes its own queue at every turn boundary and drain pass.
	for _, plan := range append(plans, unprocessed...) {
		target := fallback
		if receiver, ok := plan.receiver.(*Session); ok && receiver != nil {
			target = receiver
		}
		release, err := target.beginRetirementMutation("delegate_delivery")
		if err != nil {
			if target == fallback {
				continue
			}
			target = fallback
			if release, err = target.beginRetirementMutation("delegate_delivery"); err != nil {
				continue
			}
		}
		target.enqueueDelegateDelivery(plan)
		release()
	}
}

// enqueueDelegateDeliveryLocked appends plan to this session's pending
// delivery queue and applies the pipeline's single wake/backoff rule: a wake
// when no pump is running and no retry is armed, otherwise a paced retry. The
// caller holds delegateDeliveryMu and notifies after unlocking when it returns
// true.
func (s *Session) enqueueDelegateDeliveryLocked(plan delegateDeliveryPlan) (shouldWake bool) {
	s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, plan)
	if s.delegateDeliveryPumping {
		s.scheduleDelegateDeliveryRetryLocked()
		return false
	}
	if s.delegateDeliveryWake || s.delegateDeliveryRetry.active {
		return false
	}
	s.delegateDeliveryWake = true
	return true
}

// enqueueDelegateDelivery is enqueueDelegateDeliveryLocked for callers outside
// the pipeline's lock.
func (s *Session) enqueueDelegateDelivery(plan delegateDeliveryPlan) {
	s.delegateDeliveryMu.Lock()
	shouldWake := s.enqueueDelegateDeliveryLocked(plan)
	s.delegateDeliveryMu.Unlock()
	if shouldWake {
		s.notify()
	}
}

// delegateDeliveryResiduePending reports whether this session's delegate
// delivery pipeline still owes work: parcels queued for the pump, a pump
// pass in flight, a wake owed, or a retry armed. Retirement refuses on it,
// and the idle-release pre-gate refuses on it for the same reason: a
// teardown through any of these would abandon a parcel mid-flight.
func (s *Session) delegateDeliveryResiduePending() bool {
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	return len(s.pendingDelegateDeliveries) != 0 || s.delegateDeliveryPumping || s.delegateDeliveryWake || s.delegateDeliveryRetry.active
}

func (s *Session) scheduleDelegateDeliveryRetryLocked() {
	if s.delegateDeliveryRetry.active || s.delegateDeliveryWake || len(s.pendingDelegateDeliveries) == 0 {
		return
	}
	delay, generation := s.delegateDeliveryRetry.arm()
	s.sclock().AfterFunc(delay, func() {
		s.delegateDeliveryMu.Lock()
		if !s.delegateDeliveryRetry.fire(generation) {
			s.delegateDeliveryMu.Unlock()
			return
		}
		pending := len(s.pendingDelegateDeliveries) != 0
		shouldWake := pending && !s.delegateDeliveryWake
		if shouldWake {
			s.delegateDeliveryWake = true
		}
		s.delegateDeliveryRetry.settle(pending, delay, jobNotificationRetryMaxDelay)
		s.delegateDeliveryMu.Unlock()
		if shouldWake {
			s.notify()
		}
	})
}

func (s *Session) resetDelegateDeliveryRetryLocked() {
	s.delegateDeliveryRetry.reset()
}

func (s *Session) hasPendingDelegateDeliveries() bool {
	if s == nil {
		return false
	}
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	return len(s.pendingDelegateDeliveries) != 0
}

func (s *Session) acceptDelegateDeliveryPlan(plan delegateDeliveryPlan) (delegateMutationPlans, bool, error) {
	if s == nil {
		return delegateMutationPlans{}, false, errDelegateDeliveryReceiverUnavailable
	}
	release, err := s.beginRetirementMutation("delegate_delivery")
	if err != nil {
		return delegateMutationPlans{}, false, err
	}
	defer release()
	s.delegateDeliveryMu.Lock()
	s.mu.Lock()
	// goalInTurn remains true across a terminal communicate's Idle transition
	// until the enclosing ProcessInput drain has made its delivery cut.
	processing := s.state == SessionProcessing || s.goalInTurn
	s.mu.Unlock()
	// A waiter-bearing plan completes a tool executing in this processing turn.
	// Deferring it behind that turn would make the turn wait on its own queue.
	deferred := processing && plan.waiter == nil
	if hook := s.cfg.testOnly.delegateDeliveryClassified; hook != nil {
		hook(s, deferred)
	}
	if deferred {
		if plan.controller != nil {
			plan.controller.holdDeliveryClaim(plan)
		}
		shouldWake := s.enqueueDelegateDeliveryLocked(plan)
		s.delegateDeliveryMu.Unlock()
		if shouldWake {
			s.notify()
		}
		return delegateMutationPlans{}, true, nil
	}
	plans, err := deliverDelegatePacket(plan, s)
	s.delegateDeliveryMu.Unlock()
	return plans, false, err
}

func (s *Session) flushPendingDelegateDeliveries() error {
	if s == nil {
		return nil
	}
	release, err := s.beginRetirementMutation("delegate_delivery")
	if err != nil {
		return err
	}
	defer release()
	s.delegateDeliveryMu.Lock()
	if s.delegateDeliveryPumping {
		s.delegateDeliveryMu.Unlock()
		return nil
	}
	s.delegateDeliveryPumping = true
	s.delegateDeliveryWake = false
	s.delegateDeliveryRetry.disarmIfArmed()
	s.delegateDeliveryMu.Unlock()
	defer func() {
		s.delegateDeliveryMu.Lock()
		s.delegateDeliveryPumping = false
		s.delegateDeliveryMu.Unlock()
	}()
	for {
		s.delegateDeliveryMu.Lock()
		if len(s.pendingDelegateDeliveries) == 0 {
			s.delegateDeliveryWake = false
			s.resetDelegateDeliveryRetryLocked()
			s.delegateDeliveryMu.Unlock()
			// Restore reaches this empty boundary after prepared delivery replay has
			// drained and after the root runtime, tools, and transcript are ready,
			// but before the Session is returned to its caller.
			if s.isRootDelegateAttentionReceiver() && s.delegateController.takeOwedAttentionAdmission() {
				if err := s.admitOwedDelegateAttentionStarts(); err != nil {
					return fmt.Errorf("admit owed delegate attention: %w", err)
				}
				continue
			}
			return nil
		}
		plan := s.pendingDelegateDeliveries[0]
		s.pendingDelegateDeliveries = s.pendingDelegateDeliveries[1:]
		s.delegateDeliveryMu.Unlock()

		var plans delegateMutationPlans
		if receiver, ok := plan.receiver.(*Session); ok && receiver == s {
			plans, err = deliverDelegatePacket(plan, s)
		} else {
			plans, err = s.deliverDelegatedPlan(plan)
		}
		if errors.Is(err, errDelegateDeliveryRetry) {
			// Queue the retry on the session that will deliver it. End this
			// pass only when the plan went back onto this same session's
			// queue: retrying it here would spin on a guard that has not
			// cleared. A plan queued elsewhere leaves this queue unprocessed
			// and unwoken, so keep draining it.
			if target := s.delegateController.queueRefusedDelegateDelivery(plan); target == s {
				return nil
			}
			continue
		}
		if err != nil {
			s.requeueReplayableDelegateDeliveries(nil)
			return err
		}
		if err := s.executeDelegateMutationPlans(plans); err != nil {
			return err
		}
	}
}

func (c *delegateTreeController) claimDelegateWaiterLocked(delegateID string, generation uint64) *delegateInlineWaiter {
	live := c.live[delegateID]
	if live == nil || live.waiters == nil {
		return nil
	}
	waiter := live.waiters[generation]
	if waiter != nil {
		delete(live.waiters, generation)
		c.evidenceVersion++
	}
	return waiter
}

// dropStoppedDelegateWaiter removes the generation's inline waiter when its
// delegate carries a pending subtree stop, reporting whether it did. That is
// the durable half of the coverage newHeadDeliveryPlanLocked refuses an inline
// delivery plan on, so the waiter is never handed a packet and can only be
// answered by the wait's ceiling. Dropping it lets the send answer with the
// stopped outcome as soon as the stop has closed its start (#3502).
func (c *delegateTreeController) dropStoppedDelegateWaiter(lease delegateLease) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	aggregate := c.durable[lease.delegateID]
	if aggregate == nil || aggregate.PendingStopSeq == 0 {
		return false
	}
	return c.claimDelegateWaiterLocked(lease.delegateID, lease.generation) != nil
}

func resolveDelegateInlineClaim(waiter *delegateInlineWaiter, resolution delegateInlineResolution) {
	if waiter == nil {
		return
	}
	waiter.resolveOnce.Do(func() {
		waiter.resolution <- resolution
	})
}

// delegateUpdateMessage decodes the delegate's own words from an update
// packet, whose Message holds a JSON string.
func delegateUpdateMessage(packet delegatestore.TerminalPacket) (string, error) {
	var message string
	if err := json.Unmarshal(packet.Message, &message); err != nil {
		return "", fmt.Errorf("decode delegate update message: %w", err)
	}
	return message, nil
}

func delegateNotificationContent(plan delegateDeliveryPlan) (string, error) {
	// The name attribute is display-only: the frame stays addressable by
	// delegate_id alone, and an unnamed delegate renders no name attribute.
	attrs := []string{notificationAttr("delegate_id", plan.delegateID)}
	if plan.name != "" {
		attrs = append(attrs, notificationAttr("name", plan.name))
	}
	kind, body := "report", ""
	if plan.packet.Kind == delegatestore.PacketUpdate {
		message, err := delegateUpdateMessage(plan.packet)
		if err != nil {
			return "", err
		}
		// The shared body codec preserves literal entities and escapes "<"
		// so the delegate's own words cannot close or forge a frame.
		kind, body = "update", escapeNotificationBody(message)
	} else {
		packet, err := json.Marshal(plan.packet)
		if err != nil {
			return "", fmt.Errorf("marshal delegate delivery packet: %w", err)
		}
		body = string(packet)
	}
	attrs = append(attrs, notificationAttr("kind", kind))
	return fmt.Sprintf("<delegate-notification %s>%s</delegate-notification>", strings.Join(attrs, " "), body), nil
}
