package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
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
	packet    *delegatestore.TerminalPacket
	commit    *delegateToolResultCommit
	earlier   []delegateInlineEarlier
	fallback  bool
	readiness string
}

// delegateInlineEarlier is an earlier result of the same delegate that the
// waiting caller had not yet received, which the inline reply carries ahead of
// its own (#3906, #3951). Its identity precedes the reply's in the carrier.
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
	// receiverCommitted means the cold writer has synced and closed. Only the
	// source acknowledgment remains, so retries must not reopen that writer or
	// block the receiver's attention admission.
	receiverCommitted bool
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
}

type delegateDeliveryPlan struct {
	controller      *delegateTreeController
	delegateID      string
	name            string
	deliveryID      string
	ownerDelegateID string
	waiter          *delegateInlineWaiter
	readiness       string
	packet          delegatestore.TerminalPacket
	claim           delegateDeliveryClaimToken
	receiver        delegateDeliveryReceiver
	callerCommitted bool
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
	return receiver.appendNotificationDurably(notification{ID: attentionID, Message: llm.User(content)})
}

// appendNotificationDurably runs under the caller's existing cold delivery
// claim. Readiness is requested after that claim is released.
func (receiver coldDelegateDeliveryReceiver) appendNotificationDurably(n notification) (bool, error) {
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
	return appendColdDelegateAttentionMessageDurablyWithOpen(path, sessionID, n.ID, n.Message, now, open, n.Provenance)
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
	if head.DeliveryID != plan.deliveryID || head.OwnerDelegateID != plan.ownerDelegateID || !reflect.DeepEqual(head.Packet, plan.packet) {
		return delegateDeliveryToken{}, false, nil
	}
	if c.deliveryStoppedLocked(plan.delegateID, plan.ownerDelegateID) || plan.ownerDelegateID != "" && c.hasAttentionStartReservationLocked(plan.ownerDelegateID) {
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
			inline:     plan.callerCommitted,
		}
		c.deliveries[receipt.token.processID] = receipt
		return receipt
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

// PostUpdate publishes an accepted nonterminal tuple through the existing outbox.
// A settlement claim fences new batches, not publication by its still-open
// owning batch. Prepared, stopped, closed, and stale generations remain refused.
func (c *delegateTreeController) PostUpdate(actor delegateActor, info schema.CommunicateInfo, packet delegatestore.TerminalPacket, readiness string) (string, delegateMutationPlans, error) {
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
	if (actor.batch != nil || c.hasSettlementClaimLocked(lease)) && !c.ownsPublicationBatchLocked(actor) {
		return "", delegateMutationPlans{}, errDelegateTargetBusy
	}
	if deliveryID := c.durable[lease.delegateID].PublicationIDs[info.CallID]; info.CallID != "" && deliveryID != "" {
		return deliveryID, delegateMutationPlans{deliveries: c.replayDeliveriesForOwnerLocked(c.durable[lease.delegateID].Descriptor.ParentDelegateID)}, nil
	}
	appended, err := c.appendLocked(delegatestore.Event{
		Kind:         delegatestore.EventDelegateUpdatePosted,
		TS:           c.now(),
		DelegateID:   lease.delegateID,
		UpdatePosted: &delegatestore.UpdatePosted{Generation: lease.generation, Message: info.Message, Readiness: readiness, Communicate: &info, Packet: &packet},
	})
	if err != nil {
		return "", delegateMutationPlans{}, err
	}
	deliveryID := delegatestore.UpdateDeliveryID(lease.delegateID, appended[0].Seq)
	if readiness == tool.DelegateReadinessReady {
		if readyWaiter := c.claimDelegateWaiterLocked(lease.delegateID, lease.generation); readyWaiter != nil {
			resolveDelegateInlineClaim(readyWaiter, delegateInlineResolution{readiness: readiness})
		}
	}
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
	if plan.controller.deliveryReceiptIsInline(token) {
		return plan.controller.CompleteDelivery(token, true)
	}
	if plan.controller.deliveryReceiverCommitted(token) {
		plans, err := plan.controller.CompleteDelivery(token, true)
		plan.controller.recheckNotificationReceivers()
		return plans, err
	}
	if receiver == nil {
		resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{fallback: true})
		plans, completionErr := plan.controller.CompleteDelivery(token, false)
		return plans, errors.Join(errDelegateDeliveryReceiverUnavailable, completionErr)
	}
	content, err := delegateNotificationContent(plan)
	if err != nil {
		resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{fallback: true})
		_, completionErr := plan.controller.CompleteDelivery(token, false)
		return delegateMutationPlans{}, errors.Join(err, completionErr)
	}
	attentionID := delegateAttentionID(plan.deliveryID)
	n := notification{ID: attentionID, Message: llm.User(content)}
	plan.controller.mu.Lock()
	if source := plan.controller.durable[plan.delegateID]; source != nil {
		n.Provenance = descriptorProvenance(source.Descriptor)
	}
	plan.controller.mu.Unlock()
	var plans delegateMutationPlans
	custodyAttempted := false
	completeCustody := func() error {
		custodyAttempted = true
		if plan.waiter != nil {
			packet := cloneDelegateTerminalPacket(plan.packet)
			resolution := delegateInlineResolution{
				packet: &packet,
				commit: &delegateToolResultCommit{controller: plan.controller, token: token, deliveryID: plan.deliveryID},
			}
			if resident, ok := receiver.(*Session); ok {
				earlier, projectionErr := resident.earlierInlineResults(plan)
				if projectionErr != nil {
					resolution = delegateInlineResolution{fallback: true}
				} else {
					resolution.earlier = earlier
				}
			}
			resolveDelegateInlineClaim(plan.waiter, resolution)
		}
		var completionErr error
		plans, completionErr = plan.controller.CompleteDelivery(token, true)
		return completionErr
	}
	switch receiver := receiver.(type) {
	case *Session:
		err = receiver.enqueueNotificationAfterCustody(n, completeCustody)
	case coldDelegateDeliveryReceiver:
		_, err = receiver.appendNotificationDurably(n)
		if err == nil {
			// Receiver custody precedes source acknowledgment. Close has released
			// the writer, so retain only the FIFO source receipt and publish the
			// transcript-backed wake even when the following source append fails.
			plan.controller.commitColdDeliveryReceiver(token)
			defer plan.controller.recheckNotificationReceivers()
		}
	default:
		_, err = receiver.appendDelegateNotificationDurably(attentionID, content)
	}
	if err != nil {
		if !custodyAttempted {
			resolveDelegateInlineClaim(plan.waiter, delegateInlineResolution{fallback: true})
			_, completionErr := plan.controller.CompleteDelivery(token, false)
			err = errors.Join(err, completionErr)
		}
		return plans, err
	}
	if !custodyAttempted {
		if err := completeCustody(); err != nil {
			return plans, err
		}
	}
	if receiver, ok := receiver.(*Session); ok {
		if !receiver.isRootDelegateAttentionReceiver() {
			plan.controller.recheckNotificationReceivers()
		}
	}
	return plans, err
}

// earlierInlineResults projects already accepted, still unobserved results of
// the same source. Neither this snapshot nor its tool-result identities owns
// transport, so a failed projection leaves ordinary steering usable.
func (s *Session) earlierInlineResults(plan delegateDeliveryPlan) ([]delegateInlineEarlier, error) {
	ids, err := s.pendingNotificationIDs()
	if err != nil {
		return nil, err
	}
	events, err := plan.controller.store.Load()
	if err != nil {
		return nil, err
	}
	pending := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		pending[id] = struct{}{}
	}
	var earlier []delegateInlineEarlier
	prepared := make(map[uint64]delegatestore.TerminalPacket)
	for _, event := range events {
		if event.DelegateID != plan.delegateID {
			continue
		}
		if terminal := event.TerminalPrepared; terminal != nil {
			prepared[terminal.Generation] = terminal.Packet
		}
		var id string
		if finished := event.RunFinished; finished != nil {
			id = finished.DeliveryID
		} else if event.UpdatePosted != nil {
			id = delegatestore.UpdateDeliveryID(event.DelegateID, event.Seq)
		}
		if id == plan.deliveryID {
			break
		}
		if _, ok := pending[delegateAttentionID(id)]; !ok {
			continue
		}
		var packet *delegatestore.TerminalPacket
		if finished := event.RunFinished; finished != nil {
			packet = finished.Packet
			if packet == nil {
				if terminal, ok := prepared[finished.Generation]; ok {
					packet = &terminal
				}
			}
		} else if update := event.UpdatePosted; update != nil {
			if update.Packet != nil {
				captured := cloneDelegateTerminalPacket(*update.Packet)
				packet = &captured
			} else {
				message, _ := json.Marshal(update.Message)
				packet = &delegatestore.TerminalPacket{Kind: delegatestore.PacketUpdate, Message: message}
			}
			projected := delegatestore.ProjectUpdatePacket(*packet, update.Readiness)
			packet = &projected
		}
		if packet != nil {
			earlier = append(earlier, delegateInlineEarlier{
				packet: cloneDelegateTerminalPacket(*packet),
				commit: &delegateToolResultCommit{controller: plan.controller, token: delegateDeliveryToken{deliveryID: id}, deliveryID: id},
			})
		}
	}
	return earlier, nil
}

func delegateAttentionID(deliveryID string) string { return "delegate:" + deliveryID }

// completeDelegateDeliveryCommits validates one persisted carrier's identities.
// Canonical receiver custody already acknowledged transport, so neither storing
// this projection nor abandoning it changes source delivery or observation.
func completeDelegateDeliveryCommits(commits []*delegateToolResultCommit) ([]delegateMutationPlans, error) {
	var completed []delegateMutationPlans
	for _, commit := range commits {
		plans, err := commit.Complete(true)
		if err != nil {
			return completed, err
		}
		completed = append(completed, plans)
	}
	return completed, nil
}

func (commit *delegateToolResultCommit) Complete(committed bool) (delegateMutationPlans, error) {
	if commit == nil || commit.controller == nil || commit.deliveryID != commit.token.deliveryID {
		return delegateMutationPlans{}, errDelegateStaleLease
	}
	// The canonical receiver record already owns custody. Persisting or
	// abandoning its inline projection cannot acknowledge transport or
	// model observation, and cannot withdraw ordinary delivery.
	return delegateMutationPlans{}, nil
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
	if head.DeliveryID != deliveryID {
		return nil
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
	// A readiness update answers an inline wait without consuming the
	// generation's report: the waiter learns the delegate is ready, and the
	// terminal delivery still travels to the parent on its own.
	var waiter *delegateInlineWaiter
	readiness := ""
	if head.Packet.Kind == delegatestore.PacketUpdate {
		readiness = delegateUpdateReadiness(head.Packet)
		if readiness == tool.DelegateReadinessReady {
			if readyWaiter := c.claimDelegateWaiterLocked(delegateID, head.Generation); readyWaiter != nil {
				resolveDelegateInlineClaim(readyWaiter, delegateInlineResolution{readiness: readiness})
			}
		}
	}
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
	c.evidenceVersion++
	return &delegateDeliveryPlan{
		controller:      c,
		delegateID:      delegateID,
		name:            aggregate.Descriptor.Name,
		deliveryID:      head.DeliveryID,
		ownerDelegateID: head.OwnerDelegateID,
		waiter:          waiter,
		readiness:       readiness,
		packet:          cloneDelegateTerminalPacket(head.Packet),
		claim:           claimToken,
		receiver:        receiver,
	}
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
		if receipt != nil && !receipt.receiverCommitted && receipt.ownerID == ownerDelegateID {
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
	if !receipt.inline && !receipt.receiverCommitted && receipt.ownerID != "" && receiver == nil {
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

func (c *delegateTreeController) deliveryReceiptIsInline(token delegateDeliveryToken) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := c.deliveries[token.processID]
	return receipt != nil && receipt.token == token && receipt.inline
}

func (c *delegateTreeController) deliveryReceiverCommitted(token delegateDeliveryToken) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	receipt := c.deliveries[token.processID]
	return receipt != nil && receipt.token == token && receipt.receiverCommitted
}

// commitColdDeliveryReceiver is called only after the canonical append, sync
// and close succeed. The retained receipt still owns source FIFO and Stop's
// source-ack join, but no longer owns receiver I/O or execution exclusion.
func (c *delegateTreeController) commitColdDeliveryReceiver(token delegateDeliveryToken) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if receipt := c.deliveries[token.processID]; receipt != nil && receipt.token == token {
		receipt.receiverCommitted = true
		receipt.cold = false
		c.noteDelegateAttentionLocked(receipt.ownerID, delegateAttentionID(token.deliveryID))
		c.evidenceVersion++
	}
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
	// Existing source delivery retries retain their obligation when selection
	// fails. Notification admission also needs the underlying uncertainty error.
	receiver, _ := c.deliveryReceiverWithEvidenceLocked(ownerDelegateID)
	return receiver
}

func (c *delegateTreeController) deliveryReceiverWithEvidenceLocked(ownerDelegateID string) (delegateDeliveryReceiver, error) {
	if ownerDelegateID == "" {
		return c.rootRuntime, nil
	}
	if live := c.live[ownerDelegateID]; live != nil && live.runtime != nil {
		return live.runtime, nil
	}
	if owner := c.durable[ownerDelegateID]; owner != nil && owner.Descriptor.TranscriptRef != "" {
		if owner.Phase != delegatestore.PhaseIdle {
			waiting, err := c.readSafeDelegateWaitLocked(owner)
			if err != nil || !waiting {
				return nil, err
			}
		}
		return coldDelegateDeliveryReceiver{
			stateDir:      c.stateDir,
			transcriptRef: owner.Descriptor.TranscriptRef,
			now:           c.now,
			open:          c.attentionOpen,
		}, nil
	}
	return nil, nil
}

func (c *delegateTreeController) recheckNotificationReceivers() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return false
	}
	root := c.rootRuntime
	ids := make([]string, 0, len(c.durable))
	for id, aggregate := range c.durable {
		if aggregate == nil || !aggregate.Resumable {
			continue
		}
		waiting := aggregate.CurrentRunOpen && aggregate.Phase == delegatestore.PhaseRunning
		if live := c.live[id]; live != nil && (live.runtime != nil || live.binding != nil) {
			waiting = waiting && live.binding != nil && live.binding.yielded
		}
		// A cold candidate is not declared safe here. Addressed pending evidence
		// and the single safe-checkpoint reader gate its actual restoration.
		if aggregate.Phase == delegatestore.PhaseIdle || waiting {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		var at, bt uint64
		if state := c.attention[a]; state != nil {
			at = state.driveTurn
		}
		if state := c.attention[b]; state != nil {
			bt = state.driveTurn
		}
		if at < bt {
			return -1
		}
		if at > bt {
			return 1
		}
		return strings.Compare(a, b)
	})
	sessionIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		sessionIDs = append(sessionIDs, c.durable[id].Descriptor.ChildSessionID)
	}
	c.mu.Unlock()
	closed := func() bool {
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if closing {
			return true
		}
		if root != nil {
			root.mu.Lock()
			closing = root.closingOrClosedLocked()
			root.mu.Unlock()
		}
		return closing
	}
	if closed() {
		return false
	}
	if root != nil {
		if hook := root.cfg.testOnly.notificationRecheckBeforeDispatch; hook != nil {
			hook()
		}
		if closed() {
			return false
		}
		if err := root.dispatchNotificationWake(); err != nil {
			slog.Warn("notification wake failed", "session_id", root.ID(), "error", err)
		}
	}
	for _, id := range sessionIDs {
		if closed() {
			break
		}
		if err := c.requestSessionWake(id); err != nil {
			if _, closing := errors.AsType[notificationWakeClosingError](err); closing {
				break
			}
			slog.Warn("notification wake failed", "session_id", id, "error", err)
		}
	}
	return len(sessionIDs) != 0
}

func (s *Session) executeDelegateMutationPlans(plans delegateMutationPlans) error {
	defer func() {
		if plans.recheckNotifications && s != nil && s.delegateController != nil {
			s.delegateController.recheckNotificationReceivers()
		}
	}()
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
	var deliveryErr error
	for len(queue) != 0 {
		plan := queue[0]
		queue = queue[1:]
		var next delegateMutationPlans
		var err error
		if receiver, ok := plan.receiver.(*Session); ok {
			next, _, err = receiver.acceptDelegateDeliveryPlan(plan)
		} else if _, cold := plan.receiver.(coldDelegateDeliveryReceiver); cold {
			next, err = deliverColdDelegatePacket(plan)
		} else {
			next, err = deliverDelegatePacket(plan, plan.receiver)
		}
		if err != nil {
			deliveryErr = errors.Join(deliveryErr, err)
		}
		s.delegateController.emitDelegateUpdates(next)
		queue = append(queue, next.deliveries...)
	}
	if deliveryErr != nil {
		s.scheduleDelegateDeliveryRetry()
	}
	return deliveryErr
}

// The producer journal is the sole retry inventory. The timer is only a paced
// transport retry hint, never a receiver delivery queue or a model executor.
func (s *Session) scheduleDelegateDeliveryRetry() {
	if s == nil || s.delegateController == nil {
		return
	}
	s.delegateController.mu.Lock()
	pump := s.delegateController.rootRuntime
	s.delegateController.mu.Unlock()
	if pump == nil {
		pump = s
	}
	pump.delegateDeliveryMu.Lock()
	defer pump.delegateDeliveryMu.Unlock()
	pump.scheduleDelegateDeliveryRetryLocked()
}

func (s *Session) delegateDeliveryResiduePending() bool {
	s.delegateDeliveryMu.Lock()
	defer s.delegateDeliveryMu.Unlock()
	return s.delegateDeliveryPumping || s.delegateDeliveryRetry.active
}

func (s *Session) scheduleDelegateDeliveryRetryLocked() {
	if s.delegateDeliveryRetry.active {
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
		s.delegateDeliveryRetry.delay = min(delay*2, jobNotificationRetryMaxDelay)
		s.delegateDeliveryMu.Unlock()
		if err := s.replayDelegateDeliveries(); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "delegate delivery retry failed: " + err.Error()})
		}
	})
}

func (s *Session) resetDelegateDeliveryRetryLocked() {
	s.delegateDeliveryRetry.generation++
	s.delegateDeliveryRetry.active = false
	s.delegateDeliveryRetry.delay = jobNotificationRetryInitialDelay
}

func (s *Session) hasPendingDelegateDeliveries() bool {
	if s == nil || s.delegateController == nil {
		return false
	}
	c := s.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, aggregate := range c.durable {
		if aggregate != nil && len(aggregate.PendingDeliveries) != 0 {
			return true
		}
	}
	return false
}

// pendingDrainDelegateReceivers returns this receiver, or the first cold child
// on a recorded receiver's ownership path. Live children belong to the drain's
// existing gated walk. This is accounting only: it never changes a destination.
func (s *Session) pendingDrainDelegateReceivers(liveChildren map[string]struct{}) []string {
	if s == nil || s.delegateController == nil {
		return nil
	}
	c := s.delegateController
	c.mu.Lock()
	defer c.mu.Unlock()
	var receivers []string
	for _, aggregate := range c.durable {
		if aggregate == nil {
			continue
		}
		for _, pending := range aggregate.PendingDeliveries {
			receiverSessionID := c.rootSessionID
			if pending.OwnerDelegateID != "" {
				owner := c.durable[pending.OwnerDelegateID]
				if owner == nil {
					continue
				}
				receiverSessionID = owner.Descriptor.ChildSessionID
			}
			if receiverSessionID == s.id {
				receivers = append(receivers, s.id)
				continue
			}
			// Cold receivers have no runtime for liveDirectSubagents to visit.
			// Walk their canonical parents only until a child in the caller's
			// actual live traversal, or its direct ownership boundary. A retained
			// controller runtime pointer alone does not make a closed child live.
			seen := make(map[string]struct{})
			for ownerID := pending.OwnerDelegateID; ownerID != ""; {
				if _, duplicate := seen[ownerID]; duplicate {
					break
				}
				seen[ownerID] = struct{}{}
				owner := c.durable[ownerID]
				if owner == nil || delegateRowStopGated(delegateSnapshot{lastOutcome: owner.LatestOutcome, currentRunOpen: owner.CurrentRunOpen}) {
					break
				}
				if _, live := liveChildren[owner.Descriptor.ChildSessionID]; live {
					break
				}
				if c.stableDelegateOwnedBySessionLocked(s, owner) {
					receivers = append(receivers, owner.Descriptor.ChildSessionID)
					break
				}
				ownerID = owner.Descriptor.ParentDelegateID
			}
		}
	}
	return receivers
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
	defer s.delegateDeliveryMu.Unlock()
	if hook := s.cfg.testOnly.delegateDeliveryClassified; hook != nil {
		hook(s, false)
	}
	plans, err := deliverDelegatePacket(plan, s)
	return plans, false, err
}

// replayDelegateDeliveries takes a finite source-journal pass. One failed
// receiver does not prevent unrelated plans from transferring custody.
func (s *Session) replayDelegateDeliveries() error {
	if s == nil || s.delegateController == nil {
		return nil
	}
	if s.cfg.spawn.delegateController != nil {
		s.delegateController.mu.Lock()
		root := s.delegateController.rootRuntime
		s.delegateController.mu.Unlock()
		// Inherited executors do not own the shared source inventory. Its root
		// pump retries receiver failures without failing an unrelated child's
		// construction, request, or finalization. Keep the fallback when no root
		// runtime exists, matching scheduleDelegateDeliveryRetry.
		if root != nil && root != s {
			return nil
		}
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
	s.delegateDeliveryRetry.generation++
	s.delegateDeliveryRetry.active = false
	s.delegateDeliveryMu.Unlock()
	defer func() {
		s.delegateDeliveryMu.Lock()
		s.delegateDeliveryPumping = false
		s.delegateDeliveryMu.Unlock()
	}()
	if err := s.executeDelegateMutationPlans(delegateMutationPlans{deliveries: s.delegateController.ReplayDeliveries()}); err != nil {
		return err
	}
	if !s.hasPendingDelegateDeliveries() {
		s.delegateDeliveryMu.Lock()
		s.resetDelegateDeliveryRetryLocked()
		s.delegateDeliveryMu.Unlock()
	}
	if s.isRootDelegateAttentionReceiver() && s.delegateController.takeOwedAttentionAdmission() {
		return s.admitOwedDelegateAttentionStarts()
	}
	return nil
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

func delegateUpdateReadiness(packet delegatestore.TerminalPacket) string {
	if len(packet.Metadata) == 0 {
		return ""
	}
	var metadata delegatestore.UpdateMetadata
	if err := json.Unmarshal(packet.Metadata, &metadata); err != nil {
		return ""
	}
	return metadata.Readiness
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
		var publication struct {
			Blocker   string `json:"blocker"`
			WaitingOn string `json:"waiting_on"`
		}
		if len(plan.packet.Metadata) != 0 {
			if err := json.Unmarshal(plan.packet.Metadata, &publication); err != nil {
				return "", fmt.Errorf("decode update metadata: %w", err)
			}
		}
		if len(plan.packet.StructuredResult) != 0 || publication.Blocker != "" || publication.WaitingOn != "" {
			packet, err := json.Marshal(terminalPacketForDelivery(plan.packet))
			if err != nil {
				return "", fmt.Errorf("marshal delegate update packet: %w", err)
			}
			body = string(packet)
		}
		if readiness := delegateUpdateReadiness(plan.packet); readiness != "" {
			attrs = append(attrs, notificationAttr("readiness", readiness))
		}
	} else {
		packet, err := json.Marshal(terminalPacketForDelivery(plan.packet))
		if err != nil {
			return "", fmt.Errorf("marshal delegate delivery packet: %w", err)
		}
		body = string(packet)
	}
	attrs = append(attrs, notificationAttr("kind", kind))
	return fmt.Sprintf("<delegate-notification %s>%s</delegate-notification>", strings.Join(attrs, " "), body), nil
}
