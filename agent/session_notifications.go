package agent

import (
	"errors"

	"primeradiant.com/evener/agent/provenance"
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

// ackNotifications records coverage already proved by the caller, not merely
// execution admission. The existing durable resolutions remain authoritative.
func (s *Session) ackNotifications(ids []string) error {
	return s.resolveAttentionDurably(ids, delegateAttentionConsumed)
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
	if c.hasColdDeliveryWorkForOwnerLocked(ownerID) || c.hasAttentionStartReservationLocked(ownerID) || c.stopCoversLocked(ownerID) {
		c.mu.Unlock()
		return errDelegateTargetBusy
	}
	for _, reservation := range c.reservations {
		if reservation != nil && reservation.delegateID == ownerID {
			c.mu.Unlock()
			return errDelegateTargetBusy
		}
	}
	_, sessionID, err := delegateTranscriptPathFromRef(cold.stateDir, cold.transcriptRef)
	if err != nil || sessionID != ownerSessionID {
		c.mu.Unlock()
		return errors.Join(errDelegateDeliveryReceiverUnavailable, err)
	}
	// This is active I/O, not a discardable pre-admission claim. Stop joins
	// active deliveries until the writer has closed and its wake is recorded.
	c.nextToken++
	key := "notification:" + ownerID + ":" + n.ID
	token := delegateDeliveryToken{processID: c.nextToken, deliveryID: key}
	c.deliveries[token.processID] = &delegateDeliveryAdmission{token: token, delegateID: ownerID, ownerID: ownerID, cold: true}
	c.evidenceVersion++
	c.mu.Unlock()
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
