package agent

import (
	"errors"
	"log/slog"
	"slices"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// notificationSchedule is protected by Session.mu, the input admission lock.
// The transcript remains the sole authority for pending message membership.
type notificationSchedule struct {
	version          uint64
	hinted           bool
	running          bool // outer-input suppression, not admission authority
	paused           bool
	pauseWarned      bool // survives fresh wake eligibility until successful recovery
	retry            notificationRetry
	admissionBlocked bool
	nameBlocked      bool
	releaseTurnID    string
}

// notificationExecutionState snapshots execution suppression and recovery,
// not admission authority or producer membership. Host/lease admission remains
// authoritative, and durable pending IDs remain in the transcript fold.
type notificationExecutionState struct {
	version    uint64
	held       bool
	running    bool
	recovering bool
}

func (s *Session) notificationExecutionState() notificationExecutionState {
	s.mu.Lock()
	state := notificationExecutionState{
		version:    s.notificationSchedule.version,
		held:       s.closingOrClosedLocked() || len(s.askPending) != 0 || s.notificationSchedule.paused || s.notificationSchedule.nameBlocked,
		running:    s.notificationSchedule.running,
		recovering: s.notificationSchedule.retry.active || s.notificationSchedule.releaseTurnID != "",
	}
	s.mu.Unlock()
	state.held = state.held || s.notificationStopHeld()
	return state
}

func (s *Session) notificationWorkState() (bool, error) {
	state := s.notificationExecutionState()
	pending, err := s.hasUnackedNotifications()
	pending = pending || s.delegateController.generationOwesSteering(s.owningDelegateID)
	return state.recovering || (!state.held && pending), err
}

func (s *Session) deferNotificationAdmission() {
	s.mu.Lock()
	s.notificationSchedule.admissionBlocked = true
	s.notificationSchedule.hinted = false
	s.mu.Unlock()
	s.scheduleNotificationRetry()
}

func (s *Session) releaseNotificationAdmission() error {
	s.mu.Lock()
	s.notificationSchedule.version++
	s.notificationSchedule.hinted = false
	if s.notificationSchedule.admissionBlocked {
		s.notificationSchedule.admissionBlocked = false
		s.notificationSchedule.retry.generation++
		s.notificationSchedule.retry.active = false
	}
	s.mu.Unlock()
	return s.dispatchNotificationWake()
}

func (s *Session) requestNotificationWake() error {
	if s == nil {
		return errors.New("notification receiver is nil")
	}
	s.mu.Lock()
	s.notificationSchedule.version++
	s.notificationSchedule.hinted = false
	s.notificationSchedule.paused = false
	s.mu.Unlock()
	return s.dispatchNotificationWake()
}

// dispatchNotificationWake retains pending custody and schedules paced recovery
// before returning a transport error. Best-effort callers log that failure,
// rather than rejecting an already committed mutation or owning another retry.
func (s *Session) dispatchNotificationWake() error {
	if s.deferJobNotificationWake() {
		return nil
	}
	s.mu.Lock()
	releaseTurnID := s.notificationSchedule.releaseTurnID
	s.mu.Unlock()
	if releaseTurnID != "" {
		if s.releaseRunningTurnID(releaseTurnID) == turnNameReleaseStoreFailed {
			return nil
		}
		s.wakeForPendingSteering()
		s.notify()
	}
	for {
		state := s.notificationExecutionState()
		if state.held || state.running || state.recovering {
			return nil
		}
		if err := s.ackNotifications(nil); err != nil {
			s.scheduleNotificationRetry()
			return err
		}
		ids, err := s.pendingNotificationIDs()
		// Existing admitted parent steering is input readiness, not a synthetic
		// notification. Its controller admission stays with the open generation.
		pending := len(ids) != 0 || s.delegateController.generationOwesSteering(s.owningDelegateID)
		if err == nil && s.delegateController != nil && s.owningDelegateID != "" {
			for _, id := range ids {
				if _, openErr := s.delegateController.openDelegateAttention(s.owningDelegateID, id); openErr != nil {
					err = errors.Join(err, openErr)
				}
			}
		}
		if err != nil {
			s.scheduleNotificationRetry()
			return err
		}
		if !pending {
			return nil
		}
		s.mu.Lock()
		if state.version != s.notificationSchedule.version {
			s.mu.Unlock()
			continue
		}
		f := s.notifyFunc
		wake := f != nil && !s.notificationSchedule.hinted && !s.notificationSchedule.running && !s.closingOrClosedLocked() && len(s.askPending) == 0
		if wake {
			s.notificationSchedule.hinted = true
		}
		s.mu.Unlock()
		if wake {
			f()
		}
		return nil
	}
}

func (s *Session) scheduleNotificationRetry() {
	s.mu.Lock()
	if s.notificationSchedule.retry.active || s.closingOrClosedLocked() {
		s.mu.Unlock()
		return
	}
	r := &s.notificationSchedule.retry
	delay := r.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	r.active = true
	r.generation++
	generation := r.generation
	r.delay = min(delay*2, jobNotificationRetryMaxDelay)
	s.mu.Unlock()
	s.sclock().AfterFunc(delay, func() {
		// Superseded timers have no work and must not reset idle eligibility.
		// Keep the second check below for supersession during admission.
		s.mu.Lock()
		if s.notificationSchedule.retry.generation != generation {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.observeNotificationProgress("retry-admission-before")
		release, err := s.beginAttentionCallback()
		s.observeNotificationProgress("retry-admission-after")
		s.mu.Lock()
		current := s.notificationSchedule.retry.generation == generation
		var progress *notificationProgress
		if current {
			s.pendingJobNotifsMu.Lock()
			progress = s.joinNotificationProgressLocked()
			s.pendingJobNotifsMu.Unlock()
			s.notificationSchedule.retry.active = false
			s.notificationSchedule.hinted = false
		}
		s.mu.Unlock()
		s.observeNotificationProgress("retry-transition")
		if !current {
			if release != nil {
				release()
				s.observeNotificationProgress("custody-return")
			}
			return
		}
		if err != nil {
			s.scheduleNotificationRetry()
			s.finishNotificationProgress(progress, false)
			return
		}
		func() {
			defer func() {
				release()
				s.observeNotificationProgress("custody-return")
			}()
			if !s.notificationStopHeld() {
				if observe := s.cfg.testOnly.notificationRetryCallback; observe != nil {
					observe()
				}
			}
			s.observeNotificationProgress("retry-dispatch")
			err = s.dispatchNotificationWake()
		}()
		if err != nil {
			slog.Warn("notification wake failed", "session_id", s.ID(), "error", err)
		}
		s.observeNotificationProgress("retry-leave")
		// The complete callback release above includes retirement custody.
		s.finishNotificationProgress(progress, err == nil)
	})
}

func (s *Session) notificationWorkPending() bool {
	pending, err := s.notificationWorkState()
	return err != nil || pending
}

// finishNotificationExecution does not grant observation. It repairs receipts
// and records one receiver-scoped retry/deferral, shared by both adapters.
func (s *Session) finishNotificationExecution(turnErr error) error {
	err := s.ackNotifications(s.unionCoveredAttention(nil))
	pending, readErr := s.hasUnackedNotifications()
	err = errors.Join(err, readErr)
	s.mu.Lock()
	s.notificationSchedule.hinted = false
	if err == nil && (!pending || turnErr == nil) {
		s.notificationSchedule.paused = false
		s.notificationSchedule.pauseWarned = false
		if s.notificationSchedule.releaseTurnID == "" {
			s.notificationSchedule.retry.generation++
			s.notificationSchedule.retry.active = false
			s.notificationSchedule.retry.delay = jobNotificationRetryInitialDelay
		}
		s.mu.Unlock()
		return nil
	}
	permanent := err == nil && turnErr != nil && !roundWasCancelled(turnErr) && llm.Classify(turnErr) == llm.ErrorClassPermanent
	announce := permanent && !s.notificationSchedule.pauseWarned
	if announce {
		s.notificationSchedule.pauseWarned = true
	}
	s.notificationSchedule.paused = permanent
	s.mu.Unlock()
	if permanent {
		if announce {
			s.announceNotificationsPaused(turnErr)
		}
	} else {
		s.scheduleNotificationRetry()
	}
	return err
}

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
	_, err := s.appendDelegateAttentionMessageDurably(n.ID, n.Message, n.Provenance)
	if err != nil {
		return err
	}
	var custodyErr error
	if afterCustody != nil {
		custodyErr = afterCustody()
	}
	// The append primitive has released its writer/fold locks before any wake.
	if s.delegateController != nil && s.owningDelegateID != "" {
		_, projectionErr := s.delegateController.openDelegateAttention(s.owningDelegateID, n.ID)
		custodyErr = errors.Join(custodyErr, projectionErr)
	}
	return errors.Join(custodyErr, s.requestNotificationWake())
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
func (s *Session) ackNotifications(ids []string) (err error) {
	s.mu.Lock()
	version := s.notificationSchedule.version
	s.mu.Unlock()
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	s.attentionMu.Unlock()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil || len(fold.pendingIDs()) != 0 {
			return
		}
		s.mu.Lock()
		if s.notificationSchedule.version == version && s.notificationSchedule.releaseTurnID == "" {
			s.notificationSchedule.retry.generation++
			s.notificationSchedule.retry.active = false
			s.notificationSchedule.retry.delay = jobNotificationRetryInitialDelay
		}
		s.mu.Unlock()
	}()
	toResolve := slices.Clone(ids)
	for _, id := range fold.order {
		_, observed := fold.observed[id]
		_, resolved := fold.resolutions[id]
		if observed && !resolved {
			toResolve = appendUniqueStrings(toResolve, id)
		}
	}
	// Historical consumed resolutions may carry a nonzero resume generation.
	// They are already indexed and must not be rewritten as generation zero.
	toResolve = slices.DeleteFunc(toResolve, func(id string) bool {
		return fold.resolutions[id] == delegateAttentionConsumed
	})
	if len(toResolve) != 0 {
		if err := s.resolveAttentionDurably(toResolve, delegateAttentionConsumed); err != nil {
			return err
		}
	}
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
		receiver, err = c.deliveryReceiverWithEvidenceLocked(ownerID)
		if err != nil {
			c.mu.Unlock()
			return err
		}
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
	if err := c.requestSessionWake(ownerSessionID); err != nil {
		slog.Warn("notification wake failed", "session_id", ownerSessionID, "error", err)
	}
	return nil
}

// requestSessionWake restores only the addressed receiver. The restore hold
// excludes a cold writer or retirement until the resident adapter owns it.
type notificationWakeClosingError struct{}

func (notificationWakeClosingError) Error() string { return errDelegateTargetBusy.Error() }
func (notificationWakeClosingError) Unwrap() error { return errDelegateTargetBusy }

func (c *delegateTreeController) requestSessionWake(sessionID string) error {
	recheck, err := c.requestSessionWakeEffect(sessionID)
	if recheck {
		c.recheckNotificationReceivers()
	}
	return err
}

// Recursive empty-to-pending passes return their effect to the original caller,
// after every enclosing retirement admission and restore hold has unwound.
func (c *delegateTreeController) requestSessionWakeEffect(sessionID string) (bool, error) {
	var recheck bool
	if c == nil {
		return false, errDelegateDeliveryReceiverUnavailable
	}
	release, err := c.beginRetirementMutation()
	if err != nil {
		return recheck, err
	}
	defer release()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return recheck, notificationWakeClosingError{}
	}
	if sessionID == c.rootSessionID {
		root := c.rootRuntime
		c.mu.Unlock()
		if root != nil {
			return recheck, root.dispatchNotificationWake()
		}
		return recheck, errDelegateDeliveryReceiverUnavailable
	}
	id := c.delegateOwnerOfSessionLocked(sessionID)
	if id == "" {
		c.mu.Unlock()
		return recheck, errDelegateDeliveryReceiverUnavailable
	}
	if live := c.live[id]; live != nil && live.runtime != nil {
		receiver := live.runtime
		c.mu.Unlock()
		return recheck, receiver.releaseNotificationAdmission()
	}
	run := c.durable[id]
	if run != nil && run.Phase == delegatestore.PhaseRunning && run.CurrentRunOpen {
		root := c.rootRuntime
		stateDir, ref := c.stateDir, run.Descriptor.TranscriptRef
		blocked := c.stopCoversLocked(id) || c.reclamationCoversLocked(id) || c.hasColdDeliveryWorkForOwnerLocked(id)
		c.mu.Unlock()
		if blocked || root == nil {
			return recheck, nil
		}
		path, expectedID, pendingErr := delegateTranscriptPathFromRef(stateDir, ref)
		var fold delegateAttentionFold
		if pendingErr == nil {
			fold, pendingErr = readExistingDelegateAttentionFold(path, expectedID)
		}
		if pendingErr != nil {
			c.scheduleSessionWakeRetry(sessionID)
			return recheck, pendingErr
		}
		if len(fold.pendingIDs()) == 0 {
			return recheck, nil
		}
		_, sub, released, restoreErr := root.restoreColdDelegateAttentionRuntime(id)
		recheck = recheck || released
		if restoreErr != nil {
			c.scheduleSessionWakeRetry(sessionID)
			return recheck, restoreErr
		}
		return recheck, sub.sess.releaseNotificationAdmission()
	}
	// Restore can replay a committed receiver's still-unacknowledged source
	// packet. Its deferred wake must not join the reconstruction it is inside.
	// The existing owner dispatches again after attachment, or schedules recovery
	// on failure. This hold is receiver-scoped, not a fence on other receivers.
	if c.attentionRestoreHeldLocked(id) {
		c.mu.Unlock()
		return recheck, nil
	}
	state := c.attention[id]
	paused := state != nil && state.parked
	ready := c.delegateAttentionWakeEligibleLocked(id) && !c.hasColdDeliveryWorkForOwnerLocked(id)
	closedAncestor := ""
	if ready {
		ready, closedAncestor = c.attentionWakeAncestorGateLocked(id)
	}
	root := c.rootRuntime
	if closedAncestor != "" && root != nil {
		plan := c.escalationPlanLocked(id, c.durable[id], c.delegateAttentionWakeIDsLocked(id))
		c.mu.Unlock()
		err := root.escalateOneUnreachableDelegateAttention(plan, readExistingDelegateAttentionFold)
		if err != nil {
			root.warnDelegateAttentionFailed(delegateAttentionEscalateLabel, id, err)
			if !paused || !parkedEscalationStandsDown(err) {
				c.scheduleSessionWakeRetry(sessionID)
			}
		} else {
			root.delegateAttentionWarningResolved(delegateAttentionEscalateLabel, id)
		}
		return recheck, err
	}
	if !ready || paused || root == nil {
		c.mu.Unlock()
		return recheck, nil
	}
	ref := c.durable[id].Descriptor.TranscriptRef
	stateDir := c.stateDir
	c.holdAttentionRestoreLocked(id)
	c.mu.Unlock()
	holdingRestore := true
	defer func() {
		if holdingRestore {
			c.releaseAttentionRestoreHold(id)
		}
	}()
	path, expectedID, err := delegateTranscriptPathFromRef(stateDir, ref)
	if err == nil {
		var fold delegateAttentionFold
		fold, err = readExistingDelegateAttentionFold(path, expectedID)
		if err == nil && len(fold.pendingIDs()) == 0 {
			if err := c.clearResolvedDelegateAttention(id); err != nil {
				// A current retry must not be consumed against our own hold.
				c.releaseAttentionRestoreHold(id)
				holdingRestore = false
				c.scheduleSessionWakeRetry(sessionID)
				if hook := root.cfg.testOnly.afterSessionWakeRecoveryScheduled; hook != nil {
					hook(sessionID, err)
				}
				return recheck, err
			}
			// Acceptance may have coalesced behind this empty pass's hold.
			// Clear revalidates custody but does not dispatch pending content.
			// Inspect retained wakes and release our hold under one lock, so an
			// acceptance is either ours to recheck or owns its own unheld wake.
			c.mu.Lock()
			releaseCountedLocked(c.attentionRestoreHolds, id)
			pending := len(c.delegateAttentionWakeIDsLocked(id)) != 0
			c.mu.Unlock()
			holdingRestore = false
			if pending {
				return c.requestSessionWakeEffect(sessionID)
			}
			return recheck, nil
		}
	}
	var sub *subagent
	if err == nil {
		var released bool
		_, sub, released, err = root.restoreColdDelegateAttentionRuntime(id)
		recheck = recheck || released
	}
	if err != nil {
		root.warnDelegateAttentionFailed(delegateAttentionRestoreLabel, id, err)
		root.countDelegateAttentionRestoreFailure(id, err)
		c.releaseAttentionRestoreHold(id)
		holdingRestore = false
		c.scheduleSessionWakeRetry(sessionID)
		if hook := root.cfg.testOnly.afterSessionWakeRecoveryScheduled; hook != nil {
			hook(sessionID, err)
		}
		return recheck, err
	}
	c.delegateAttentionRestored(id)
	root.delegateAttentionWarningResolved(delegateAttentionRestoreLabel, id)
	if hook := root.cfg.testOnly.afterDelegateAttentionRestore; hook != nil {
		hook(id, sub)
	}
	// Callback installation can refuse before AttachIdleRuntime completes.
	// Attachment releases that existing admission hold, as on the resident path.
	return recheck, sub.sess.releaseNotificationAdmission()
}

func (c *delegateTreeController) scheduleSessionWakeRetry(sessionID string) {
	c.mu.Lock()
	id := c.delegateOwnerOfSessionLocked(sessionID)
	root := c.rootRuntime
	if id == "" || root == nil || c.closing {
		c.mu.Unlock()
		return
	}
	r := &c.attentionStateLocked(id).retry
	if r.active {
		c.mu.Unlock()
		return
	}
	delay := r.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	r.active = true
	r.generation++
	generation := r.generation
	r.delay = min(delay*2, jobNotificationRetryMaxDelay)
	c.mu.Unlock()
	root.sclock().AfterFunc(delay, func() {
		c.mu.Lock()
		state := c.attention[id]
		current := !c.closing && state != nil && state.retry.generation == generation
		if current {
			state.retry.active = false
		}
		c.mu.Unlock()
		if current {
			if err := c.requestSessionWake(sessionID); err != nil {
				slog.Warn("notification wake failed", "session_id", sessionID, "error", err)
			}
		}
	})
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
	if err := writer.EstablishDurability(); err != nil {
		return false, false, err
	}
	_, accepted = fold.content[notificationID]
	return accepted, closed, nil
}

// notificationProgress joins only canonical retry and waking completion-release
// publication. All fields and the Session pointer use pendingJobNotifsMu.
// It is neither input admission nor an additional retirement lease.
type notificationProgress struct {
	users             int
	progressCandidate bool
	legacyHint        bool
}

// notificationProgressObserver is a test-only scheduling/observation seam. It
// cannot replace admission, dispatch, custody release, or the publication claim.
// Every call is outside owner locks, and a normal session leaves it nil.
type notificationProgressObserver struct{ observe func(string) }

func (s *Session) observeNotificationProgress(point string) {
	if observer := s.testOnlyNotificationProgress.Load(); observer != nil {
		observer.observe(point)
	}
}

// Only registration allocates, never a finishing tail. Caller holds the pending
// mutex, and if both locks are needed, the order is mu then pendingJobNotifsMu.
func (s *Session) joinNotificationProgressLocked() *notificationProgress {
	if s.notificationProgress == nil {
		s.notificationProgress = &notificationProgress{}
	}
	p := s.notificationProgress
	p.users++
	return p
}

func (s *Session) finishNotificationProgress(p *notificationProgress, candidate bool) {
	s.pendingJobNotifsMu.Lock()
	p.progressCandidate = p.progressCandidate || candidate
	p.users--
	last := p.users == 0
	s.pendingJobNotifsMu.Unlock()
	if !last {
		return
	}
	s.observeNotificationProgress("claim-before")
	// Durable Stop reads must stay outside owner locks.
	stopped := s.notificationStopHeld()
	s.mu.Lock()
	s.pendingJobNotifsMu.Lock()
	if s.notificationProgress != p || p.users != 0 {
		s.pendingJobNotifsMu.Unlock()
		s.mu.Unlock()
		return
	}
	if s.notifyWakeHolds > 0 {
		s.notifyWakeDeferred = true
		s.pendingJobNotifsMu.Unlock()
		s.mu.Unlock()
		return
	}
	// Detach before callback handoff. Reentry may join a fresh obligation,
	// which this old claimant must never erase after returning from the call.
	s.notificationProgress = nil
	n := &s.notificationSchedule
	var f func()
	if p.progressCandidate && !stopped && !s.closingOrClosedLocked() &&
		!n.running && len(s.askPending) == 0 && !n.paused && !n.nameBlocked &&
		!n.retry.active && n.releaseTurnID == "" && !n.hinted &&
		(!p.legacyHint || len(s.pendingJobNotifs) == 0) {
		f = s.notifyFunc
	}
	s.pendingJobNotifsMu.Unlock()
	s.mu.Unlock()
	s.observeNotificationProgress("claim-after")
	if f != nil {
		f()
	}
}
