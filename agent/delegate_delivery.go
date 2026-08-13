package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"reflect"
	"sort"

	"primeradiant.com/serf/agent/internal/delegatestore"
)

var errDelegateDeliveryReceiverUnavailable = errors.New("delegate delivery receiver is unavailable")

type delegateWaiterToken struct{ id uint64 }

type delegateInlineWaiter struct {
	token      delegateWaiterToken
	generation uint64
	resolution chan delegateInlineResolution
}

type delegateInlineResolution struct {
	packet   *delegatestore.TerminalPacket
	commit   *delegateToolResultCommit
	fallback bool
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
}

type delegateDeliveryPlan struct {
	controller      *delegateTreeController
	delegateID      string
	deliveryID      string
	ownerDelegateID string
	waiter          *delegateInlineWaiter
	packet          delegatestore.TerminalPacket
}

type delegateDeliveryReceiver interface {
	appendDelegateNotificationDurably(attentionID, content string) (bool, error)
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

func (c *delegateTreeController) BeginDelivery(plan delegateDeliveryPlan) (delegateDeliveryToken, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return delegateDeliveryToken{}, false, nil
	}
	if plan.controller != c || plan.deliveryID == "" {
		return delegateDeliveryToken{}, false, errDelegateStaleLease
	}
	aggregate := c.durable[plan.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 {
		return delegateDeliveryToken{}, false, nil
	}
	head := aggregate.PendingDeliveries[0]
	if head.DeliveryID != plan.deliveryID || head.OwnerDelegateID != plan.ownerDelegateID || !reflect.DeepEqual(head.Packet, plan.packet) {
		return delegateDeliveryToken{}, false, nil
	}
	if aggregate.PendingStopSeq != 0 {
		return delegateDeliveryToken{}, false, nil
	}
	if plan.ownerDelegateID != "" {
		owner := c.durable[plan.ownerDelegateID]
		if owner == nil || owner.PendingStopSeq != 0 {
			return delegateDeliveryToken{}, false, nil
		}
	}
	if c.stop != nil {
		_, senderCovered := c.stop.members[plan.delegateID]
		_, ownerCovered := c.stop.members[plan.ownerDelegateID]
		if senderCovered || ownerCovered {
			return delegateDeliveryToken{}, false, nil
		}
	}
	for _, receipt := range c.deliveries {
		if receipt.token.deliveryID == plan.deliveryID {
			return receipt.token, true, nil
		}
	}
	c.nextToken++
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: plan.deliveryID}
	c.deliveries[token.processID] = &delegateDeliveryAdmission{
		token:      token,
		delegateID: plan.delegateID,
		ownerID:    plan.ownerDelegateID,
	}
	c.evidenceVersion++
	return token, true, nil
}

func (c *delegateTreeController) CompleteDelivery(token delegateDeliveryToken, committed bool) (delegateMutationPlans, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := c.deliveries[token.processID]
	if receipt == nil || receipt.token != token {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if !committed {
		delete(c.deliveries, token.processID)
		if c.stop != nil {
			delete(c.stop.deliveries, token)
		}
		c.evidenceVersion++
		return delegateMutationPlans{}, nil
	}
	aggregate := c.durable[receipt.delegateID]
	if aggregate == nil || len(aggregate.PendingDeliveries) == 0 || aggregate.PendingDeliveries[0].DeliveryID != token.deliveryID {
		delete(c.deliveries, token.processID)
		if c.stop != nil {
			delete(c.stop.deliveries, token)
		}
		c.evidenceVersion++
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateDeliveryAcknowledged,
		DelegateID: receipt.delegateID,
		DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{
			DeliveryID: token.deliveryID,
		},
	}); err != nil {
		delete(c.deliveries, token.processID)
		if c.stop != nil {
			delete(c.stop.deliveries, token)
		}
		c.evidenceVersion++
		return delegateMutationPlans{}, err
	}
	delete(c.deliveries, token.processID)
	if c.stop != nil {
		delete(c.stop.deliveries, token)
	}
	c.evidenceVersion++
	plan := c.capturedPlanLocked(receipt.delegateID)
	plans := delegateMutationPlans{updates: []delegateUpdatePlan{plan}}
	aggregate = c.durable[receipt.delegateID]
	if len(aggregate.PendingDeliveries) != 0 {
		if delivery := c.newHeadDeliveryPlanLocked(receipt.delegateID, aggregate.PendingDeliveries[0].DeliveryID); delivery != nil {
			plans.deliveries = append(plans.deliveries, *delivery)
		}
	}
	return plans, nil
}

func (c *delegateTreeController) ReplayDeliveries() []delegateDeliveryPlan {
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
		if plan := c.newHeadDeliveryPlanLocked(id, aggregate.PendingDeliveries[0].DeliveryID); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans
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
		resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{
			packet: &packet,
			commit: &delegateToolResultCommit{
				controller: plan.controller,
				token:      token,
				deliveryID: plan.deliveryID,
			},
		})
		return delegateMutationPlans{}, nil
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
	if _, err := receiver.appendDelegateNotificationDurably(delegateAttentionID(plan.deliveryID), content); err != nil {
		_, completionErr := plan.controller.CompleteDelivery(token, false)
		return delegateMutationPlans{}, errors.Join(err, completionErr)
	}
	return plan.controller.CompleteDelivery(token, true)
}

func delegateAttentionID(deliveryID string) string { return "delegate:" + deliveryID }

func (commit *delegateToolResultCommit) Complete(committed bool) (delegateMutationPlans, error) {
	if commit == nil || commit.controller == nil || commit.deliveryID != commit.token.deliveryID {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	return commit.controller.CompleteDelivery(commit.token, committed)
}

func (c *delegateTreeController) newHeadDeliveryPlanLocked(delegateID, deliveryID string) *delegateDeliveryPlan {
	if deliveryID == "" {
		return nil
	}
	aggregate := c.durable[delegateID]
	if aggregate == nil || aggregate.PendingStopSeq != 0 || len(aggregate.PendingDeliveries) == 0 {
		return nil
	}
	head := aggregate.PendingDeliveries[0]
	if head.DeliveryID != deliveryID {
		return nil
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
	return &delegateDeliveryPlan{
		controller:      c,
		delegateID:      delegateID,
		deliveryID:      head.DeliveryID,
		ownerDelegateID: head.OwnerDelegateID,
		waiter:          c.claimDelegateWaiterLocked(delegateID, head.Generation),
		packet:          cloneDelegateTerminalPacket(head.Packet),
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

func resolveDelegateInlineClaim(waiter *delegateInlineWaiter, resolution delegateInlineResolution) {
	if waiter == nil {
		return
	}
	waiter.resolution <- resolution
}

func delegateNotificationContent(plan delegateDeliveryPlan) (string, error) {
	packet, err := json.Marshal(plan.packet)
	if err != nil {
		return "", fmt.Errorf("marshal delegate delivery packet: %w", err)
	}
	return fmt.Sprintf(
		"<delegate-notification delegate_id=\"%s\" delivery_id=\"%s\">%s</delegate-notification>",
		html.EscapeString(plan.delegateID),
		html.EscapeString(plan.deliveryID),
		packet,
	), nil
}
