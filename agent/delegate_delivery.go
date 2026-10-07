package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	earlier  *delegateInlineEarlier
	fallback bool
}

// delegateInlineEarlier is an earlier result of the same delegate that the
// waiting caller was holding for its next round, which the inline reply
// carries ahead of its own (#3906). Its commit completes before the reply's.
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
	// reply took over, restored if the reply's tool round is abandoned;
	// heldToken, on the reply's own admission, is that held admission.
	heldClaim *delegateDeliveryClaim
	heldToken delegateDeliveryToken
}

type delegateDeliveryClaimToken struct {
	processID  uint64
	deliveryID string
}

type delegateDeliveryClaim struct {
	token      delegateDeliveryClaimToken
	delegateID string
	ownerID    string
	waiter     *delegateInlineWaiter
	cold       bool
	// held marks a plan its receiver deferred to its next round because it
	// was mid-turn.
	held bool
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
	// held is the delegate's head delivery its receiver is holding for its
	// next round, which this waiter-bearing plan carries ahead of its own
	// (#3906); heldClaim is the receiver's claim on it, which this plan's
	// claim replaces until it is admitted or refused.
	held      *delegatestore.PendingDelivery
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
	defer func() {
		if !admitted {
			c.releaseHeldClaimLocked(plan)
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
	if plan.held != nil {
		if !reflect.DeepEqual(head, *plan.held) || len(aggregate.PendingDeliveries) < 2 {
			return delegateDeliveryToken{}, false, nil
		}
		head = aggregate.PendingDeliveries[1]
	}
	if head.DeliveryID != plan.deliveryID || head.OwnerDelegateID != plan.ownerDelegateID || !reflect.DeepEqual(head.Packet, plan.packet) {
		return delegateDeliveryToken{}, false, nil
	}
	if c.closing || aggregate.PendingStopSeq != 0 {
		delete(c.deliveryClaims, plan.deliveryID)
		c.evidenceVersion++
		return delegateDeliveryToken{}, false, nil
	}
	if plan.ownerDelegateID != "" {
		owner := c.durable[plan.ownerDelegateID]
		if owner == nil || owner.PendingStopSeq != 0 {
			delete(c.deliveryClaims, plan.deliveryID)
			c.evidenceVersion++
			return delegateDeliveryToken{}, false, nil
		}
	}
	if c.stop != nil {
		_, senderCovered := c.stop.members[plan.delegateID]
		_, ownerCovered := c.stop.members[plan.ownerDelegateID]
		if senderCovered || ownerCovered {
			delete(c.deliveryClaims, plan.deliveryID)
			c.evidenceVersion++
			return delegateDeliveryToken{}, false, nil
		}
	}
	if plan.ownerDelegateID != "" && c.hasAttentionStartReservationLocked(plan.ownerDelegateID) {
		delete(c.deliveryClaims, plan.deliveryID)
		c.evidenceVersion++
		return delegateDeliveryToken{}, false, nil
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
	var heldToken delegateDeliveryToken
	if plan.held != nil {
		delete(c.deliveryClaims, plan.held.DeliveryID)
		// The held delivery is admitted under the receiver's own claim
		// token, so a retry of it is planned as that delivery's.
		held := admit(plan.heldClaim.token)
		held.heldClaim = plan.heldClaim
		heldToken = held.token
	}
	carrier := admit(plan.claim)
	carrier.heldToken = heldToken
	token := carrier.token
	c.evidenceVersion++
	return token, true, nil
}

// heldDeliveryStillDeliverableLocked reports whether receipt's held delivery
// is still the head of its delegate's queue with nothing closing, stopping or
// covering the delegate or its owner, the checks BeginDelivery would refuse on.
func (c *delegateTreeController) heldDeliveryStillDeliverableLocked(receipt *delegateDeliveryAdmission) bool {
	aggregate := c.durable[receipt.delegateID]
	if c.closing || aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != receipt.token.deliveryID || aggregate.PendingStopSeq != 0 {
		return false
	}
	if owner := c.durable[receipt.ownerID]; receipt.ownerID != "" && (owner == nil || owner.PendingStopSeq != 0) {
		return false
	}
	if c.stop != nil {
		_, senderCovered := c.stop.members[receipt.delegateID]
		_, ownerCovered := c.stop.members[receipt.ownerID]
		return !senderCovered && !ownerCovered
	}
	return true
}

// releaseHeldClaimLocked gives a refused plan's held delivery back to the
// receiver's claim, so the plan the receiver holds delivers it as before.
func (c *delegateTreeController) releaseHeldClaimLocked(plan delegateDeliveryPlan) {
	if plan.held == nil {
		return
	}
	if claim := c.deliveryClaims[plan.held.DeliveryID]; claim != nil && claim.token == plan.claim {
		c.deliveryClaims[plan.held.DeliveryID] = plan.heldClaim
		c.evidenceVersion++
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
		if receipt.heldClaim != nil && c.deliveryClaims[token.deliveryID] == nil && c.heldDeliveryStillDeliverableLocked(receipt) {
			// The reply carrying this held result was abandoned: the receiver's
			// held plan delivers it at its next round, as before. Not when a
			// stop or close has since taken the delivery: the claim would
			// outlive it and block the owner for good.
			c.deliveryClaims[token.deliveryID] = receipt.heldClaim
		}
		if c.stop != nil {
			if _, tracked := c.stop.deliveries[token]; tracked {
				delete(c.stop.deliveries, token)
				c.signalStopProgressLocked()
			}
		}
		c.evidenceVersion++
		return delegateMutationPlans{}, nil
	}
	aggregate := c.durable[receipt.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != token.deliveryID {
		delete(c.deliveries, token.processID)
		if c.stop != nil {
			if _, tracked := c.stop.deliveries[token]; tracked {
				delete(c.stop.deliveries, token)
				c.signalStopProgressLocked()
			}
		}
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
	if c.stop != nil {
		if _, tracked := c.stop.deliveries[token]; tracked {
			delete(c.stop.deliveries, token)
			c.signalStopProgressLocked()
		}
	}
	c.evidenceVersion++
	plans := delegateMutationPlans{}
	if ownerChanged {
		plans.updates = append(plans.updates, c.capturedPlanLocked(receipt.ownerID))
	}
	plans.updates = append(plans.updates, c.capturedPlanLocked(receipt.delegateID))
	plans.deliveries = append(plans.deliveries, c.replayDeliveriesForOwnerLocked(receipt.ownerID)...)
	return plans, nil
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
		if receipt := c.deliveryReceiptLocked(aggregate.PendingDeliveries[0].DeliveryID); receipt != nil {
			if receipt.retryable {
				if plan := c.retryDeliveryPlanLocked(receipt); plan != nil {
					plans = append(plans, *plan)
				}
			}
			continue
		}
		if plan := c.newHeadDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans
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
		if plan.held != nil {
			held := plan.controller.heldAdmission(token)
			resolution.earlier = &delegateInlineEarlier{
				packet: cloneDelegateTerminalPacket(plan.held.Packet),
				commit: &delegateToolResultCommit{controller: plan.controller, token: held, deliveryID: held.deliveryID},
			}
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
			plan.controller.notifyStableDelegateAttention()
		}
	case coldDelegateDeliveryReceiver:
		plan.controller.notifyStableDelegateAttention()
	}
	return plans, err
}

func delegateAttentionID(deliveryID string) string { return "delegate:" + deliveryID }

// completeDelegateDeliveryCommits acknowledges one tool call's delivery
// commits in order. A reply carrying a held result acknowledges it ahead of
// its own (#3906), and the store takes them only in queue order, so after one
// fails the rest are left for the same retry rather than abandoned: they are
// already in the reply and must never come back as notifications.
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
	var held *delegatestore.PendingDelivery
	var heldClaim *delegateDeliveryClaim
	if head.DeliveryID != deliveryID {
		if heldClaim = c.carriableHeldClaimLocked(delegateID, aggregate.PendingDeliveries, deliveryID); heldClaim == nil {
			return nil
		}
		held = &aggregate.PendingDeliveries[0]
		head = aggregate.PendingDeliveries[1]
	}
	if head.OwnerDelegateID != "" {
		owner := c.durable[head.OwnerDelegateID]
		if owner == nil || owner.PendingStopSeq != 0 {
			return nil
		}
	}
	if c.stop != nil {
		_, senderCovered := c.stop.members[delegateID]
		_, ownerCovered := c.stop.members[head.OwnerDelegateID]
		if senderCovered || ownerCovered {
			return nil
		}
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
	waiter := c.claimDelegateWaiterLocked(delegateID, head.Generation)
	claim := &delegateDeliveryClaim{
		token:      claimToken,
		delegateID: delegateID,
		ownerID:    head.OwnerDelegateID,
		waiter:     waiter,
		cold:       cold,
	}
	c.deliveryClaims[deliveryID] = claim
	if held != nil {
		// The held delivery's claim moves to this plan, so the plan its
		// receiver holds is no longer admitted and no new one is made.
		c.deliveryClaims[held.DeliveryID] = claim
		copied := *held
		copied.Packet = cloneDelegateTerminalPacket(held.Packet)
		held = &copied
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
		held:            held,
		heldClaim:       heldClaim,
	}
}

// carriableHeldClaimLocked returns the receiver's claim on the delegate's head
// delivery when deliveryID's inline reply may carry it: deliveryID is next in
// line, its generation has an inline waiter, and the head goes to the same
// owner, which is holding it for its next round. That waiter's caller is the
// owner, mid-tool-round, so without the reply the held result waits on the
// round, the round on the wait, and the wait sits out its ceiling (#3906).
func (c *delegateTreeController) carriableHeldClaimLocked(delegateID string, pending []delegatestore.PendingDelivery, deliveryID string) *delegateDeliveryClaim {
	if len(pending) < 2 || pending[1].DeliveryID != deliveryID || pending[0].OwnerDelegateID != pending[1].OwnerDelegateID {
		return nil
	}
	if live := c.live[delegateID]; live == nil || live.waiters[pending[1].Generation] == nil {
		return nil
	}
	if claim := c.deliveryClaims[pending[0].DeliveryID]; claim != nil && claim.held {
		return claim
	}
	return nil
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
	if ownerDelegateID == "" {
		return false
	}
	for _, claim := range c.deliveryClaims {
		if claim != nil && claim.ownerID == ownerDelegateID {
			return true
		}
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

// heldAdmission returns the admission of the held delivery admitted with the
// inline reply that token admits.
func (c *delegateTreeController) heldAdmission(token delegateDeliveryToken) delegateDeliveryToken {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt := c.deliveries[token.processID]; receipt != nil && receipt.token == token {
		return receipt.heldToken
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
		// A new head left retryable behind a failed acknowledgment is retried
		// as ReplayDeliveries would.
		if receipt := c.deliveryReceiptLocked(aggregate.PendingDeliveries[0].DeliveryID); receipt != nil && receipt.retryable {
			if plan := c.retryDeliveryPlanLocked(receipt); plan != nil {
				plans = append(plans, *plan)
			}
			continue
		}
		if plan := c.newHeadDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
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

func (c *delegateTreeController) notifyStableDelegateAttention() {
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
	for len(queue) != 0 {
		plan := queue[0]
		queue = queue[1:]
		var next delegateMutationPlans
		var err error
		if receiver, ok := plan.receiver.(*Session); ok {
			var deferred bool
			next, deferred, err = receiver.acceptDelegateDeliveryPlan(plan)
			if deferred {
				continue
			}
		} else if _, cold := plan.receiver.(coldDelegateDeliveryReceiver); cold {
			next, err = deliverColdDelegatePacket(plan)
		} else {
			next, err = deliverDelegatePacket(plan, plan.receiver)
		}
		if err != nil {
			s.requeueReplayableDelegateDeliveries(queue)
			return err
		}
		s.delegateController.emitDelegateUpdates(next)
		queue = append(queue, next.deliveries...)
	}
	return nil
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
	pump := s.delegateController.rootRuntime
	s.delegateController.mu.Unlock()
	if pump == nil {
		pump = s
	}
	pump.delegateDeliveryMu.Lock()
	retry := make([]delegateDeliveryPlan, 0, len(plans)+len(unprocessed)+len(pump.pendingDelegateDeliveries))
	retry = append(retry, plans...)
	retry = append(retry, unprocessed...)
	retry = append(retry, pump.pendingDelegateDeliveries...)
	pump.pendingDelegateDeliveries = retry
	shouldWake := false
	if pump.delegateDeliveryPumping {
		pump.scheduleDelegateDeliveryRetryLocked()
	} else if !pump.delegateDeliveryWake && !pump.delegateDeliveryRetry.active {
		pump.delegateDeliveryWake = true
		shouldWake = true
	}
	pump.delegateDeliveryMu.Unlock()
	if shouldWake {
		pump.notify()
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
	delay := s.delegateDeliveryRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.delegateDeliveryRetry.active = true
	s.delegateDeliveryRetry.generation++
	generation := s.delegateDeliveryRetry.generation
	s.sclock().AfterFunc(delay, func() {
		s.delegateDeliveryMu.Lock()
		if s.delegateDeliveryRetry.generation != generation {
			s.delegateDeliveryMu.Unlock()
			return
		}
		s.delegateDeliveryRetry.active = false
		pending := len(s.pendingDelegateDeliveries) != 0
		shouldWake := pending && !s.delegateDeliveryWake
		if shouldWake {
			s.delegateDeliveryWake = true
		}
		if pending {
			s.delegateDeliveryRetry.delay = min(delay*2, jobNotificationRetryMaxDelay)
		} else {
			s.delegateDeliveryRetry.delay = jobNotificationRetryInitialDelay
		}
		s.delegateDeliveryMu.Unlock()
		if shouldWake {
			s.notify()
		}
	})
}

func (s *Session) resetDelegateDeliveryRetryLocked() {
	s.delegateDeliveryRetry.generation++
	s.delegateDeliveryRetry.active = false
	s.delegateDeliveryRetry.delay = jobNotificationRetryInitialDelay
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
		s.pendingDelegateDeliveries = append(s.pendingDelegateDeliveries, plan)
		shouldWake := !s.delegateDeliveryWake && !s.delegateDeliveryRetry.active
		if shouldWake {
			s.delegateDeliveryWake = true
		}
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
	if s.delegateDeliveryRetry.active {
		s.delegateDeliveryRetry.generation++
		s.delegateDeliveryRetry.active = false
	}
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

		if receiver, ok := plan.receiver.(*Session); ok && receiver == s {
			plans, err := deliverDelegatePacket(plan, s)
			if err != nil {
				s.requeueReplayableDelegateDeliveries(nil)
				return err
			}
			if err := s.executeDelegateMutationPlans(plans); err != nil {
				return err
			}
			continue
		}
		if err := s.executeDelegateMutationPlans(delegateMutationPlans{deliveries: []delegateDeliveryPlan{plan}}); err != nil {
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

func delegateNotificationContent(plan delegateDeliveryPlan) (string, error) {
	packet, err := json.Marshal(plan.packet)
	if err != nil {
		return "", fmt.Errorf("marshal delegate delivery packet: %w", err)
	}
	// The name attribute is display-only: the frame stays addressable by
	// delegate_id alone, and an unnamed delegate renders no name attribute.
	attrs := []string{notificationAttr("delegate_id", plan.delegateID)}
	if plan.name != "" {
		attrs = append(attrs, notificationAttr("name", plan.name))
	}
	return fmt.Sprintf("<delegate-notification %s>%s</delegate-notification>", strings.Join(attrs, " "), packet), nil
}
