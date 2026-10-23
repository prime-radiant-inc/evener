package agent

import (
	"errors"
	"slices"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// notification is an immutable receiver delivery. Producer authorization and
// source FIFO stay with the producer's existing journal and admission claims.
type notification struct {
	ID         string
	Message    llm.Message
	Provenance *provenance.Causal
}

// enqueueNotification transfers custody through the session's one transcript
// writer. Even a deduplicated retry requests readiness, repairing a lost wake.
func (s *Session) enqueueNotification(n notification) error {
	return s.enqueueNotificationAfterCustody(n, nil)
}

// enqueueNotificationAfterCustody lets the delegate transport complete its
// existing source acknowledgment after fsync but before fallible wake arming.
// That source transfer must not be undone by a later arming read failure: the
// receiver already owns the durable record and its existing arm retry owns it.
// A source acknowledgment error likewise cannot suppress receiver readiness.
func (s *Session) enqueueNotificationAfterCustody(n notification, afterCustody func() error) error {
	appended, err := s.appendDelegateAttentionMessageDurably(n.ID, n.Message, n.Provenance)
	if err != nil {
		return err
	}
	var custodyErr error
	if afterCustody != nil {
		custodyErr = afterCustody()
	}
	// The append primitive has released its writer/fold locks before any wake.
	if s.delegateController != nil {
		if err := s.armDelegateAttention(n.ID); err != nil {
			return errors.Join(custodyErr, err)
		}
		// The root's arm coalesces hints. A successful retry must also repair
		// a hint lost after the original arm, without releasing a user hold.
		if !appended && s.isRootDelegateAttentionReceiver() && s.hasPendingRootDelegateAttention() && !s.rootAttentionRailParked() {
			s.notify()
		}
		return custodyErr
	}
	s.notify()
	return custodyErr
}

func (s *Session) pendingNotificationIDs() ([]string, error) {
	return s.pendingDelegateAttentionIDs()
}

func (s *Session) hasUnackedNotifications() (bool, error) {
	ids, err := s.pendingNotificationIDs()
	return len(ids) != 0, err
}

// ackNotifications repairs the derived resolution index. Synced assistant
// receipts already own observation, even if this append fails. Old resolution
// records remain readable and keep their original generation claims.
func (s *Session) ackNotifications(ids []string) error {
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil {
		return err
	}
	toResolve := slices.Clone(ids)
	var settled []string
	for _, id := range fold.order {
		_, observed := fold.observed[id]
		_, resolved := fold.resolutions[id]
		if observed && !resolved {
			toResolve = appendUniqueStrings(toResolve, id)
		}
		if observed || resolved {
			settled = append(settled, id)
		}
	}
	// Historical consumed resolutions may carry a nonzero resume generation.
	// They are already indexed and must not be rewritten as generation zero.
	toResolve = slices.DeleteFunc(toResolve, func(id string) bool {
		return fold.resolutions[id] == delegateAttentionConsumed
	})
	if len(toResolve) == 0 && len(settled) == 0 {
		return nil
	}
	if len(toResolve) != 0 {
		if err := s.resolveAttentionDurably(toResolve, delegateAttentionConsumed); err != nil {
			return err
		}
	}
	s.attentionMu.Lock()
	for _, id := range append(settled, toResolve...) {
		delete(s.rootAttentionWakeIDs, id)
	}
	if len(s.rootAttentionWakeIDs) == 0 {
		s.rootAttentionWake = false
		s.resetRootAttentionRetryLocked()
	}
	s.attentionMu.Unlock()
	return nil
}

// enqueueNotification resolves only controller members. A cold writer borrows
// the existing active delivery machinery, which also excludes attention starts
// and runtime restoration while that transcript is open.
func (c *delegateTreeController) enqueueNotification(ownerSessionID string, n notification) error {
	if c == nil || ownerSessionID == "" {
		return errDelegateDeliveryReceiverUnavailable
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return err
	}
	defer release()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return errDelegateTargetBusy
	}
	var receiver delegateDeliveryReceiver
	ownerID := ""
	if ownerSessionID == c.rootSessionID {
		if c.rootRuntime != nil {
			receiver = c.rootRuntime
		}
	} else {
		ownerID = c.delegateOwnerOfSessionLocked(ownerSessionID)
		if ownerID == "" {
			c.mu.Unlock()
			return errDelegateDeliveryReceiverUnavailable
		}
		receiver = c.deliveryReceiverLocked(ownerID)
	}
	if resident, ok := receiver.(*Session); ok {
		if resident.ID() != ownerSessionID {
			c.mu.Unlock()
			return errDelegateDeliveryReceiverUnavailable
		}
		c.mu.Unlock()
		return resident.enqueueNotification(n)
	}
	cold, ok := receiver.(coldDelegateDeliveryReceiver)
	if !ok {
		c.mu.Unlock()
		return errDelegateDeliveryReceiverUnavailable
	}
	_, sessionID, err := delegateTranscriptPathFromRef(cold.stateDir, cold.transcriptRef)
	if err != nil || sessionID != ownerSessionID {
		c.mu.Unlock()
		return errors.Join(errDelegateDeliveryReceiverUnavailable, err)
	}
	token, err := c.beginColdNotificationIOLocked(ownerID, n.ID)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	_, err = cold.appendNotificationDurably(n)
	if err == nil {
		_, err = c.openDelegateAttention(ownerID, n.ID)
	}
	// There is no source journal to acknowledge for this admission. Releasing
	// it through the existing completion path also signals a waiting Stop.
	_, completionErr := c.CompleteDelivery(token, false)
	err = errors.Join(err, completionErr)
	if err != nil {
		return err
	}
	c.notifyStableDelegateAttention()
	return nil
}

// beginColdNotificationIOLocked shares the cold writer exclusion between
// acceptance and custody queries. Stop joins this active admission through Close.
func (c *delegateTreeController) beginColdNotificationIOLocked(ownerID, notificationID string) (delegateDeliveryToken, error) {
	if c.hasColdDeliveryWorkForOwnerLocked(ownerID) || c.hasAttentionStartReservationLocked(ownerID) || c.stopCoversLocked(ownerID) {
		return delegateDeliveryToken{}, errDelegateTargetBusy
	}
	for _, reservation := range c.reservations {
		if reservation != nil && reservation.delegateID == ownerID {
			return delegateDeliveryToken{}, errDelegateTargetBusy
		}
	}
	c.nextToken++
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: "notification:" + ownerID + ":" + notificationID}
	c.deliveries[token.processID] = &delegateDeliveryAdmission{token: token, delegateID: ownerID, ownerID: ownerID, cold: true}
	c.evidenceVersion++
	return token, nil
}

// notificationCustodyAndClosure neither appends nor arms a notification. Only
// an authoritative closed delegate, with a successfully checked transcript,
// proves permanent non-resumability. Missing membership or files is uncertainty.
func (c *delegateTreeController) notificationCustodyAndClosure(ownerSessionID, notificationID string) (accepted, closed bool, err error) {
	release, err := c.beginRetirementMutation()
	if err != nil {
		return false, false, err
	}
	defer release()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return false, false, errDelegateTargetBusy
	}
	if ownerSessionID == c.rootSessionID {
		resident := c.rootRuntime
		c.mu.Unlock()
		if resident == nil {
			return false, false, errDelegateDeliveryReceiverUnavailable
		}
		accepted, err = resident.notificationSourceAccepted(notificationID)
		return accepted, false, err
	}
	events, err := c.store.Load()
	if err != nil {
		c.mu.Unlock()
		return false, false, err
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		c.mu.Unlock()
		return false, false, err
	}
	ownerID, ref := "", ""
	for id, aggregate := range state {
		if aggregate.Descriptor.ChildSessionID == ownerSessionID {
			ownerID, ref = id, aggregate.Descriptor.TranscriptRef
			closed = aggregate.Phase == delegatestore.PhaseClosed
			break
		}
	}
	if ownerID == "" || c.stopCoversLocked(ownerID) {
		c.mu.Unlock()
		return false, false, errDelegateDeliveryReceiverUnavailable
	}
	if live := c.live[ownerID]; live != nil && live.runtime != nil {
		resident := live.runtime
		c.mu.Unlock()
		accepted, err = resident.notificationSourceAccepted(notificationID)
		return accepted, closed, err
	}
	path, sessionID, err := delegateTranscriptPathFromRef(c.stateDir, ref)
	if err != nil || sessionID != ownerSessionID {
		c.mu.Unlock()
		return false, false, errors.Join(errDelegateDeliveryReceiverUnavailable, err)
	}
	token, err := c.beginColdNotificationIOLocked(ownerID, notificationID)
	open := c.attentionOpen
	c.mu.Unlock()
	if err != nil {
		return false, false, err
	}
	defer func() {
		_, completionErr := c.CompleteDelivery(token, false)
		err = errors.Join(err, completionErr)
	}()
	// A read first prevents OpenWriterForSession from creating a missing file.
	if _, err = readDelegateAttentionFold(path, sessionID); err != nil {
		return false, false, err
	}
	if open == nil {
		open = transcript.OpenWriterForSession
	}
	writer, entries, err := open(path, sessionID)
	if err != nil {
		return false, false, err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	fold, err := foldDelegateAttention(entries)
	if err != nil {
		return false, false, err
	}
	if err = writer.EstablishDurability(); err != nil {
		return false, false, err
	}
	_, accepted = fold.content[notificationID]
	return accepted, closed, nil
}
