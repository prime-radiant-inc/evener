package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/lineend"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
)

// stableWatchAttentionIDPrefix names every durable watch attention row this
// ingress writes. Classification by this prefix is what lets the unleased
// projection tell a watch row apart from any other delegate attention.
const stableWatchAttentionIDPrefix = "watchdiag:"

// stableWatchReminderAttentionIDPrefix names the durable rows a
// long-running-jobs reminder writes. It is nested under
// stableWatchAttentionIDPrefix, so a reminder is still a watch row, and
// narrower than it, so replacing a stale reminder retires only older reminders
// and leaves this ingress's other attention rows pending.
const stableWatchReminderAttentionIDPrefix = stableWatchAttentionIDPrefix + "reminder:"

// stableWatchOwner snapshots the stable delegate owner and its controller under
// jm.mu. Every reader of stableOwner takes this snapshot: a new generation
// rewrites the field in bindDelegateParent under the same mutex, and a timer or
// shell callback enqueues concurrently with exactly that rewrite. The caller
// holds no lock afterwards, so transcript I/O and wake callbacks run outside
// jm.mu.
func (jm *jobManager) stableWatchOwner() (*Session, *delegateTreeController) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.stableOwner, jm.delegateController
}

func watchNotification(jobID, reason string) jobNotification {
	return jobNotification{
		Kind:    jobNotificationKindWatch,
		JobID:   jobID,
		JobType: jobNotificationEventWatch,
		Status:  jobNotificationEventWatch,
		Reason:  reason,
	}
}

// watchSendDiagnosticNotification builds a send-rail diagnostic (a drop, a
// failed persist, an eviction) that names the watch whose delivery failed.
// The watch id rides the display-only OriginWatchID field — never WatchID,
// which would subject the diagnostic to the orphan-tick drop once its watch
// detaches (see OriginWatchID's doc comment in jobs.go).
func watchSendDiagnosticNotification(watchID, jobID, reason string) jobNotification {
	n := watchNotification(jobID, reason)
	n.OriginWatchID = watchID
	return n
}

// stableWatchNoticeAttentionID is the durable attention identity of a watch
// notice bound for a stable delegate child (LIFE-01, #2340).
//
// Two of these records are the same occurrence only when a RETRY reproduces
// them. appendDelegateNotificationDurably treats one identity with the same
// content as a no-op and one identity with different content as corruption, so
// the id must be stable for a retry and distinct for every new fire: a
// repeating timer's second tick is new news even though its watch, job and
// reason are unchanged, and a later long-running-jobs reminder describes a
// different set of jobs while its reason stays the same.
//
// Watch key, job and reason identify the SOURCE of a notice; the occurrence
// identifies which statement of it this is.
func stableWatchNoticeAttentionID(n jobNotification) string {
	scope := n.WatchID
	if scope == "" {
		scope = n.OriginWatchID
	}
	if scope == "" {
		scope = n.JobID
	}
	prefix := stableWatchAttentionIDPrefix
	if n.isLongRunningJobsReminder() {
		prefix = stableWatchReminderAttentionIDPrefix
	}
	sum := sha256.Sum256([]byte(n.Reason))
	return prefix + scope + ":" + n.JobID + ":" + hex.EncodeToString(sum[:8]) + ":" + stableWatchNoticeOccurrence(n)
}

// stableWatchNoticeOccurrence is what distinguishes one fire, tick or snapshot
// from the next within one source. It is empty only when the notice carries no
// per-occurrence marker at all: a one-shot terminal notice and a send-rail
// diagnostic are single facts, so their source identity already names them and
// a replay of the same failure stays the same item.
//
// A timer tick is identified by the clock instant the tick fired at, which
// watchTimers stamps for exactly that fire and no other. A long-running-jobs
// reminder is identified by its own rendered snapshot, so a later reminder
// listing different jobs is a NEW occurrence rather than a conflicting replay
// of the earlier one -- the queue's replacement rule for that reminder, moved
// onto the durable rail where the rows are immutable.
func stableWatchNoticeOccurrence(n jobNotification) string {
	switch {
	case n.Occurrence != "":
		// A producer that knows its own occurrence owns it.
		return n.Occurrence
	case n.isLongRunningJobsReminder():
		sum := sha256.Sum256([]byte(n.Note))
		return "reminder:" + hex.EncodeToString(sum[:8])
	default:
		return ""
	}
}

// persistStableWatchPending is enqueueNotifications' stable-delegate branch: a
// watch notice bound for a stable child becomes durable attention rather than a
// queued notification, so the child can only run it inside a leased generation
// (LIFE-01, #2340). It reports whether it handled the notice.
//
// Without this, the notice lands on the ordinary queue, whose only drain is an
// EntryNotification turn -- and a stable child's unleased turn must not carry a
// watch notice, so the gate would refuse the turn and leave the notice to be
// refused again. That is a stranded delivery, not a fixed one. The reachable
// case is the one the issue names: a watch-send diagnostic, emitted precisely
// when the send rail had no other way to report the failure.
//
// A manager with no stable owner, or one whose owner is not a delegate, keeps
// the ordinary rail. The owner and controller are snapshotted ONCE under jm.mu:
// a new generation rewrites both fields under that same mutex, so re-reading
// them after the transcript I/O would race a concurrent timer or shell
// callback.
// dropClearedStableWatchTicks removes the durable rows whose repeating timer
// has since been cleared, resolving each as discarded.
//
// The queue drops such a tick at delivery time (timerWatchIsLive). A durable
// row cannot be re-decided that way -- its content is frozen and it is already
// in the transcript -- so the timer identity is kept beside it and checked
// here, at the one point the row is about to become a request. A one-shot's
// fire carries no timer identity and is never dropped: it is terminal even if
// the timer is cleared afterwards (LIFE-01, #2340 review Low).
func (s *Session) dropClearedStableWatchTicks(ids []string) []string {
	if s == nil || len(ids) == 0 {
		return ids
	}
	s.pendingJobNotifsMu.Lock()
	tickWatch := make(map[string]string, len(ids))
	for _, id := range ids {
		if watchID, ok := s.stableWatchTickWatch[id]; ok {
			tickWatch[id] = watchID
		}
	}
	s.pendingJobNotifsMu.Unlock()
	if len(tickWatch) == 0 {
		return ids
	}
	kept := make([]string, 0, len(ids))
	var cleared []string
	for _, id := range ids {
		watchID, isTick := tickWatch[id]
		if isTick && !s.timerWatchIsLive(watchID) {
			cleared = append(cleared, id)
			continue
		}
		kept = append(kept, id)
	}
	if len(cleared) > 0 {
		if err := s.resolveAttentionDurably(cleared, delegateAttentionDiscarded); err != nil {
			// The row stays pending if the discard cannot be written: a
			// presented stale tick is better than a silently lost clear, and
			// the next drive retries the same discard.
			s.emit(warningDataFromError("discard cleared watch tick", err))
			return ids
		}
	}
	return kept
}

// pruneStableWatchTickWatch drops the timer identity recorded beside each
// attention row that has just resolved.
//
// The record exists so a clear landing AFTER a tick was persisted can still
// suppress that tick before its delivery. A resolved row is never presented, so
// that check can never run for it, and no other path deletes the entry: keeping
// it would leave one entry per delivered tick for the life of the session.
func (s *Session) pruneStableWatchTickWatch(ids ...string) {
	if s == nil || len(ids) == 0 {
		return
	}
	s.pendingJobNotifsMu.Lock()
	for _, id := range ids {
		delete(s.stableWatchTickWatch, id)
	}
	s.pendingJobNotifsMu.Unlock()
}

// isStableWatchAttentionID reports whether attentionID names a watch notice
// produced by this ingress. Classification is by identity, never by message
// text: every id this file mints starts with stableWatchAttentionIDPrefix.
func isStableWatchAttentionID(attentionID string) bool {
	return strings.HasPrefix(attentionID, stableWatchAttentionIDPrefix)
}

// isStableWatchReminderAttentionID reports whether attentionID names a
// long-running-jobs reminder this ingress wrote. Only these rows are superseded
// by a newer reminder snapshot.
func isStableWatchReminderAttentionID(attentionID string) bool {
	return strings.HasPrefix(attentionID, stableWatchReminderAttentionIDPrefix)
}

// watchAttentionFilterState reads the session's attention ledger ONCE and
// reports which watch identities are still pending and which were resolved as
// discarded.
//
// Both sets come from the same fold, so a caller deciding what a projection may
// present can never see one identity as pending from one read and discarded
// from another. The read happens under attentionMu, the lock every ledger write
// holds.
func (s *Session) watchAttentionFilterState() (pendingWatch, discardedWatch map[string]struct{}, err error) {
	if s == nil {
		return nil, nil, nil
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range fold.order {
		if !isStableWatchAttentionID(id) {
			continue
		}
		if _, resolved := fold.resolutions[id]; resolved {
			continue
		}
		if pendingWatch == nil {
			pendingWatch = make(map[string]struct{})
		}
		pendingWatch[id] = struct{}{}
	}
	for id, disposition := range fold.resolutions {
		if disposition != delegateAttentionDiscarded || !isStableWatchAttentionID(id) {
			continue
		}
		if discardedWatch == nil {
			discardedWatch = make(map[string]struct{})
		}
		discardedWatch[id] = struct{}{}
	}
	return pendingWatch, discardedWatch, nil
}

// filterWatchAttentionTurns removes the watch attention turns a model request
// or a compaction input must not present. The same filter decides every
// projection — leased and unleased, live and restored — so no builder can hand
// the model a row the ledger already retracted.
//
// A watch row resolved as discarded is always dropped. The clear that discarded
// it retracted the row before any request presented it, and the row
// deliberately stays in the transcript as the audit record of what happened. A
// terminal one-shot fire is never discarded, so its row survives.
//
// When dropUnresolved is true — an unleased stable-delegate turn, whose
// generation cannot consume what it presents — a watch row still pending on the
// ledger is dropped as well. The row stays pending, so the leased drive that
// can present it under a committed generation still owes it; presenting it here
// would show it twice and once outside that generation.
//
// An unreadable ledger drops every watch row: the projection cannot confirm
// which rows were retracted, so they wait for a later drive instead of reaching
// the model on a guess. Turns without a watch identity, and watch rows the
// ledger reports resolved without a discard, always project.
//
// droppedAt holds the ORIGINAL indices of the dropped turns in ascending order,
// so a caller holding an index into the input can shift it with
// shiftReplayBoundaryForDrops.
func (s *Session) filterWatchAttentionTurns(turns []schema.Turn, dropUnresolved bool) (kept []schema.Turn, droppedAt []int) {
	if s == nil || len(turns) == 0 {
		return turns, nil
	}
	// Most projections hold no watch row at all, and a transcript read for them
	// would buy nothing: answer before touching the ledger.
	if !slices.ContainsFunc(turns, func(turn schema.Turn) bool { return isStableWatchAttentionID(turn.AttentionID) }) {
		return turns, nil
	}
	pending, discarded, err := s.watchAttentionFilterState()
	for i, turn := range turns {
		if isStableWatchAttentionID(turn.AttentionID) {
			_, retracted := discarded[turn.AttentionID]
			_, unresolved := pending[turn.AttentionID]
			if err != nil || retracted || (dropUnresolved && unresolved) {
				if kept == nil {
					kept = make([]schema.Turn, 0, len(turns)-1)
					kept = append(kept, turns[:i]...)
				}
				droppedAt = append(droppedAt, i)
				continue
			}
		}
		if kept != nil {
			kept = append(kept, turn)
		}
	}
	if len(droppedAt) == 0 {
		return turns, nil
	}
	return kept, droppedAt
}

// shiftReplayBoundaryForDrops moves the in-flight boundary one position left
// per dropped row that sat before it.
//
// droppedAt holds ORIGINAL indices and the boundary being returned is the final
// one, so the comparison is against the ORIGINAL boundary: decrementing as the
// loop runs undercounts two adjacent rows before the boundary (the second row's
// original index equals the already-shifted boundary) and leaves the boundary
// one too far on, where it strips the replay-sensitive thinking blocks the
// in-flight exemption keeps (LIFE-01, #2340 review M3).
func shiftReplayBoundaryForDrops(inFlightFrom int, droppedAt []int) int {
	shifted := inFlightFrom
	for _, at := range droppedAt {
		if at < inFlightFrom {
			shifted--
		}
	}
	return shifted
}

// retainedStableWatchNotice is a watch notice whose durable append has not yet
// landed. It keeps the exact identity AND content so a retry is a replay of one
// occurrence rather than a second item.
type retainedStableWatchNotice struct {
	notification jobNotification
	attentionID  string
	content      string
	// delivery, when set, is the resolved watch-send this notice carries. Its
	// durable append IS that delivery reaching the model, so the send is
	// acknowledged the moment a retry lands and never before.
	delivery *deliverableJobNotification
}

// retainStableWatchPending records a notice whose durable append failed and
// schedules the session's existing notification retry wake. The notice is NOT
// put back on the ordinary queue: that queue is the unleased route this ingress
// exists to close (LIFE-01, #2340 review M4).
//
// delivery carries the resolved watch-send the notice acknowledges once it is
// durable, or nil for a notice that owes no acknowledgement.
func (s *Session) retainStableWatchPending(n jobNotification, attentionID, content string, delivery *deliverableJobNotification) {
	if s == nil || attentionID == "" {
		return
	}
	s.pendingJobNotifsMu.Lock()
	if s.stableWatchRetained == nil {
		s.stableWatchRetained = make(map[string]retainedStableWatchNotice)
	}
	// Last write wins for one identity: the same occurrence retried describes
	// the same statement, and a newer statement carries its own identity.
	s.stableWatchRetained[attentionID] = retainedStableWatchNotice{notification: n, attentionID: attentionID, content: content, delivery: delivery}
	s.scheduleJobNotificationRetryLocked()
	s.pendingJobNotifsMu.Unlock()
}

// retryRetainedStableWatchPending re-attempts every retained notice. It is
// idempotent by construction: appendDelegateNotificationDurably treats the same
// identity and content as a no-op, so a notice whose earlier attempt actually
// landed despite reporting an error is not appended twice.
func (s *Session) retryRetainedStableWatchPending() {
	if s == nil {
		return
	}
	s.pendingJobNotifsMu.Lock()
	retained := make([]retainedStableWatchNotice, 0, len(s.stableWatchRetained))
	for _, r := range s.stableWatchRetained {
		retained = append(retained, r)
	}
	s.pendingJobNotifsMu.Unlock()
	if len(retained) == 0 {
		return
	}
	for _, r := range retained {
		if _, err := s.appendDelegateNotificationDurably(r.attentionID, r.content); err != nil {
			// Still not durable: keep it retained and leave the retry wake armed
			// so the next firing tries again. This is the only place a retained
			// notice is dropped from the map, and only on success.
			continue
		}
		s.pendingJobNotifsMu.Lock()
		// One claim per identity: a concurrent retry that already retired the
		// notice owes neither a second settle nor a second arm.
		if _, still := s.stableWatchRetained[r.attentionID]; !still {
			s.pendingJobNotifsMu.Unlock()
			continue
		}
		delete(s.stableWatchRetained, r.attentionID)
		s.pendingJobNotifsMu.Unlock()
		if r.delivery != nil {
			s.settleDeliveredWatchNotification(context.Background(), *r.delivery)
		}
		if err := s.armDelegateAttention(r.attentionID); err != nil {
			s.emit(warningDataFromError("arm stable watch attention", err))
		}
	}
}

// retireSupersededStableWatchPending retires the durable rows a newer
// long-running-jobs reminder replaces.
//
// The queue keeps only the newest undelivered reminder, because a stale listing
// names jobs that may already have finished. Durable rows are immutable, so the
// same rule is expressed as: persist the newer snapshot, then resolve the older
// identities as discarded. Persisting first means a failure leaves the older
// row in place rather than retiring it into nothing.
func (s *Session) retireSupersededStableWatchPending(n jobNotification, attentionID string) {
	if s == nil || !n.isLongRunningJobsReminder() {
		return
	}
	pending, err := s.pendingDelegateAttentionIDs()
	if err != nil {
		s.emit(warningDataFromError("inspect stable watch attention", err))
		return
	}
	var superseded []string
	for _, id := range pending {
		if id != attentionID && isStableWatchReminderAttentionID(id) {
			superseded = append(superseded, id)
		}
	}
	if len(superseded) == 0 {
		return
	}
	if err := s.resolveAttentionDurably(superseded, delegateAttentionDiscarded); err != nil {
		s.emit(warningDataFromError("retire superseded stable watch attention", err))
	}
}

func (jm *jobManager) persistStableWatchPending(n jobNotification) bool {
	owner, controller := jm.stableWatchOwner()
	if owner == nil || owner.owningDelegateID == "" {
		return false
	}
	// A watch-send token names a pending send rather than carrying its frame,
	// so the frame and the delivery identity must be loaded here, before the
	// row is built and before the attention identity is minted.
	var delivery *deliverableJobNotification
	if n.WatchSend != nil {
		resolved, ok := owner.resolveStableWatchSend(&n)
		if !ok {
			// The token no longer names a current pending send: replaced,
			// cleared, dropped, or already settled. There is no frame to
			// persist and no delivery to acknowledge.
			return true
		}
		delivery = &resolved
	}
	// A repeating tick is dropped here when its timer has already been cleared
	// (LIFE-01, #2340 review Low). The queue applies this check at delivery
	// time; the durable row cannot, because its content is already frozen. The
	// timer identity is recorded WITH the row so a clear that lands after this
	// point still suppresses it (stableWatchTickAttentionWatchID).
	if n.WatchID != "" && !n.Terminal && !owner.timerWatchIsLive(n.WatchID) {
		// The timer was cleared before this tick was persisted: it is a tick
		// nobody owes, exactly as the queue's own drop rules it.
		return true
	}
	attentionID := stableWatchNoticeAttentionID(n)
	content := owner.formatJobNotificationReminder([]deliverableJobNotification{{notification: n}})
	if controller != nil {
		// Register the notice's own cause BEFORE the item can be presented: a
		// runaway-watch diagnostic carries the chain the breaker stopped, and
		// the response event must descend from it or the breaker reads that
		// response as a fresh trigger and fires again.
		controller.attentionCauses.remember(attentionID, n.Provenance)
	}
	// An accepted notice is news for the job_status repeat guard, exactly as a
	// queued one is: appendOrFoldJobNotificationLocked forgets the target on
	// every enqueue, so the durable route must do the same or the child's next
	// job_status call on that target is refused as a repeat.
	if n.JobID == "" {
		owner.jobStatusPolls.forgetAll()
	} else {
		owner.jobStatusPolls.forget(n.JobID)
	}
	// Persist, THEN arm. The order is the guarantee: a wake that pointed at an
	// item nothing had written would be an attention record naming no row.
	appended, err := owner.appendDelegateNotificationDurably(attentionID, content)
	if err != nil {
		// The append failed, so the notice is NOT durable. Queueing it would
		// reopen the unleased path this ingress exists to close, and dropping
		// it (what this branch used to do) loses the failure report the
		// diagnostic exists to carry -- the source pending send is already
		// removed, so nothing else will retry it. Retain it and re-attempt the
		// SAME identity through the session's existing notification retry
		// wake; a retry reproduces stableWatchNoticeAttentionID, so the second
		// attempt is a replay rather than a second item.
		owner.retainStableWatchPending(n, attentionID, content, delivery)
		owner.emit(warningDataFromError("persist stable watch attention", err))
		return true
	}
	if appended {
		// A newer long-running-jobs reminder replaces an older undelivered one
		// rather than queueing behind a stale listing, exactly as the queue's
		// fold rule does (appendOrFoldJobNotificationLocked). The durable rows
		// are immutable, so replacement is expressed as retirement of the
		// superseded identities.
		owner.retireSupersededStableWatchPending(n, attentionID)
		// Remember which timer watch produced this tick, so a clear landing
		// after this point can still suppress it (LIFE-01, #2340 review Low).
		// A one-shot is excluded: its fire is terminal, so a later clear must
		// not retract it.
		if n.WatchID != "" && !n.Terminal {
			owner.pendingJobNotifsMu.Lock()
			if owner.stableWatchTickWatch == nil {
				owner.stableWatchTickWatch = make(map[string]string)
			}
			owner.stableWatchTickWatch[attentionID] = n.WatchID
			owner.pendingJobNotifsMu.Unlock()
		}
	}
	// The row is durable, so a watch-send it carries has reached the model:
	// acknowledge that exact delivery. A replay of an already-present row
	// settles too -- the earlier attempt's ack is what may have been lost.
	if delivery != nil {
		owner.settleDeliveredWatchNotification(context.Background(), *delivery)
	}
	if err := owner.armDelegateAttention(attentionID); err != nil {
		// The row IS durable; only its wake failed. armDelegateAttention
		// already retains the id for its own paced re-arm, so the notice is
		// not lost.
		owner.emit(warningDataFromError("arm stable watch attention", err))
	}
	return true
}

// resolveStableWatchSend loads the CURRENT frame a watch-send token names and
// stamps the fields its durable row renders from. It reports false for a stale
// token, which is the same answer filterDeliverableJobNotifications gives one:
// the pending send was replaced, cleared, dropped, or already settled, so
// neither a frame nor an acknowledgement is owed.
//
// The occurrence is the delivery id: the same watch fires again under a fresh
// delivery, and a retry of one delivery reproduces its own id.
func (s *Session) resolveStableWatchSend(n *jobNotification) (deliverableJobNotification, bool) {
	jm, cfg, state, ok := s.resolveWatchSendToken(n.WatchSend)
	if !ok {
		return deliverableJobNotification{}, false
	}
	n.watchSendFrame = state.Frame
	if n.backstopFires = jm.watchSendBackstopFires(cfg, state); n.backstopFires > 0 && n.OriginWatchID == "" {
		n.OriginWatchID = cfg.watchID
	}
	n.Provenance = provenance.Union(n.Provenance, state.Provenance)
	if state.DeliveryID != "" {
		n.Occurrence = "send:" + state.DeliveryID
	}
	return deliverableJobNotification{notification: *n, watchJM: jm, watchCfg: cfg, watchState: state}, true
}

func (jm *jobManager) watchNotificationFromWatch(cfg *watchConfig, jobID, reason string, root *provenance.Causal) jobNotification {
	return jm.watchNotificationWithDelivery(cfg, jobID, reason, root, "")
}

// watchNotificationWithDelivery is watchNotificationFromWatch with a delivery id
// on the notification's own lineage entry. A no-send condition fire carries
// one, so the runaway guard can count how many of this watch's fires an
// event descends from (conditionFireNotificationLocked).
func (jm *jobManager) watchNotificationWithDelivery(cfg *watchConfig, jobID, reason string, root *provenance.Causal, deliveryID string) jobNotification {
	n := watchNotification(jobID, reason)
	if cfg == nil {
		return n
	}
	// Keep the note raw until withNotificationNote applies the shared body
	// encoder, so notes and excerpts escape ampersands exactly once.
	n.Note = cfg.note
	// The originating watch rides a display-only field, never WatchID: the
	// session drops a non-terminal WatchID-bearing entry whose timer key no
	// longer resolves, and a condition watch's key slot is not its id — so a
	// stamped fire or teardown would vanish as an orphaned tick.
	n.OriginWatchID = cfg.watchID
	visibleSessionID := cfg.receiverSessionID
	if visibleSessionID == "" {
		visibleSessionID = jm.sessionID
	}
	n.Provenance = provenance.WithWatch(root, cfg.watchID, cfg.generation, deliveryID, visibleSessionID, jobID)
	// Two no-send fires of one watch with the same job and reason differ only
	// by their delivery, so the delivery id is what makes the second a new
	// occurrence rather than a replay of the first.
	if deliveryID != "" && n.Occurrence == "" {
		n.Occurrence = "delivery:" + deliveryID
	}
	if cfg.receiverSessionID != "" {
		n.receiverSessionID = cfg.receiverSessionID
		n.receiverNotify = cfg.receiverNotify
		n.receiverHoldWake = cfg.receiverHoldWake
	}
	return n
}

func (jm *jobManager) buildWatchFrame(cfg *watchConfig, jobID string, trigger string, deliveryID string, ev events.SessionEvent, p *provenance.Causal) string {
	if cfg == nil || cfg.send == nil {
		return ""
	}

	var b strings.Builder
	message := limitWatchText(strings.TrimSpace(cfg.send.Message), watchMessageMaxChars)
	if message != "" {
		b.WriteString(message)
		b.WriteString("\n\n")
	}
	b.WriteString("Watch frame\n")
	if cfg.watchID != "" {
		b.WriteString("watch_id: ")
		b.WriteString(limitWatchText(cfg.watchID, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	if deliveryID != "" {
		b.WriteString("delivery_id: ")
		b.WriteString(limitWatchText(deliveryID, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	b.WriteString("job_id: ")
	b.WriteString(limitWatchText(jobID, watchTriggerMaxChars))
	b.WriteString("\n")
	writeWatchFrameTopField(&b, "trigger", limitWatchText(trigger, watchTriggerMaxChars))
	// A send delivers the frame instead of a rendered notification body, so the
	// note has to ride here or a stable receiver never sees why the watch was
	// armed. Bounded where the note is stored, as watchConditionSummary bounds it.
	if cfg.note != "" {
		writeWatchFrameTopField(&b, "note", limitWatchText(cfg.note, watchMessageMaxChars))
	}
	writeWatchFrameProvenance(&b, p)
	writeWatchFrameEvent(&b, ev)

	if !isWatchSessionTarget(jobID) && cfg.send.IncludeExcerpt {
		excerpt, _, truncated, err := jm.readOutput(jobID, watchExcerptTailBytes)
		b.WriteString("excerpt:\n")
		if err != nil {
			writeWatchFrameIndentedBlock(&b, "output_read_error: "+limitWatchText(err.Error(), watchReadErrorMaxChars))
		} else {
			writeWatchFrameIndentedBlock(&b, limitWatchText(excerpt, watchExcerptMaxChars))
			if truncated {
				writeWatchFrameIndentedBlock(&b, "[excerpt truncated]")
			}
		}
	}

	return limitWatchText(b.String(), watchFrameMaxChars)
}

// appendWatchFrameJobRead teaches a recipient how to read the concrete job
// named by a finished-job notification. It is appended after the frame cap so
// the annotation is never truncated away.
func appendWatchFrameJobRead(frame, jobID string) string {
	if frame == "" || jobID == "" {
		return frame
	}
	if !strings.HasSuffix(frame, "\n") {
		frame += "\n"
	}
	return frame + `read with: read_transcript(transcript_ref="job:` + jobID + `")` + "\n"
}

func writeWatchFrameIndentedBlock(b *strings.Builder, text string) {
	text = lineend.Normalize(text)
	for line := range strings.SplitSeq(text, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func writeWatchFrameTopField(b *strings.Builder, name, value string) {
	value = lineend.Normalize(value)
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(strings.ReplaceAll(value, "\n", "\n  "))
	b.WriteString("\n")
}

func writeWatchFrameProvenance(b *strings.Builder, p *provenance.Causal) {
	if p == nil || len(p.WatchKeys) == 0 {
		b.WriteString("provenance: external\n")
		return
	}
	b.WriteString("provenance:\n")
	b.WriteString("  watch_keys:\n")
	for _, key := range p.WatchKeys {
		b.WriteString("    - watch_id: ")
		b.WriteString(limitWatchText(key.WatchID, watchTriggerMaxChars))
		b.WriteString("\n      watch_generation: ")
		b.WriteString(limitWatchText(key.WatchGeneration, watchTriggerMaxChars))
		b.WriteString("\n")
	}
	if latest := provenance.LatestDeliveryID(p); latest != "" {
		b.WriteString("  latest_delivery_id: ")
		b.WriteString(limitWatchText(latest, watchTriggerMaxChars))
		b.WriteString("\n")
	}
}

func writeWatchFrameEvent(b *strings.Builder, ev events.SessionEvent) {
	switch data := ev.Data.(type) {
	case events.CommunicateData:
		writeCommunicateWatchEvent(b, data)
	case events.AssistantTextEndData:
		writeAssistantMessageWatchEvent(b, data)
	case events.ToolCallEndData:
		writeAssistantToolWatchEvent(b, data)
	case events.JobFinishedData:
		writeJobNotificationWatchEvent(b, data)
	}
}

func writeCommunicateWatchEvent(b *strings.Builder, data events.CommunicateData) {
	// maxMessageChars caps the event-payload excerpt; tighter than the frame's own
	// message cap because this is context, not the primary message.
	const maxMessageChars = 1000
	message := limitWatchText(data.Message, maxMessageChars)
	truncated := message != data.Message
	b.WriteString("event:\n")
	b.WriteString("  kind: communicate\n")
	writeWatchFrameTextField(b, "message", message)
	b.WriteString("  end_turn: ")
	if data.EndTurn {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
	b.WriteString("  truncated: ")
	if truncated {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}

func writeAssistantMessageWatchEvent(b *strings.Builder, data events.AssistantTextEndData) {
	text, truncated := limitedWatchEventText(data.Text)
	b.WriteString("event:\n")
	b.WriteString("  kind: assistant.message\n")
	writeWatchFrameOptionalField(b, "model", data.Model)
	writeWatchFrameOptionalField(b, "finish_reason", data.FinishReason)
	writeWatchFrameTextField(b, "text", text)
	writeWatchFrameBoolField(b, "truncated", truncated)
}

func writeAssistantToolWatchEvent(b *strings.Builder, data events.ToolCallEndData) {
	b.WriteString("event:\n")
	b.WriteString("  kind: assistant.tool\n")
	writeWatchFrameOptionalField(b, "tool_name", data.ToolName)
	writeWatchFrameOptionalField(b, "call_id", data.CallID)
	if data.Error != "" {
		writeWatchFrameOptionalField(b, "status", "error")
	} else {
		writeWatchFrameOptionalField(b, "status", "ok")
	}
	writeWatchFrameOptionalField(b, "arguments_json", data.ArgumentsJSON)
	if data.Output != "" {
		output, truncated := watchToolEndField(data.Output, data.OutputBytes)
		writeWatchFrameTextField(b, "output", output)
		writeWatchFrameBoolField(b, "output_truncated", truncated)
	}
	if data.Error != "" {
		errText, truncated := watchToolEndField(data.Error, data.ErrorBytes)
		writeWatchFrameTextField(b, "error", errText)
		writeWatchFrameBoolField(b, "error_truncated", truncated)
	}
}

func writeJobNotificationWatchEvent(b *strings.Builder, data events.JobFinishedData) {
	b.WriteString("event:\n")
	b.WriteString("  kind: job.notification\n")
	writeWatchFrameOptionalField(b, "job_id", data.JobID)
	writeWatchFrameOptionalField(b, "job_type", data.JobType)
	writeWatchFrameOptionalField(b, "status", data.Status)
	writeWatchFrameOptionalField(b, "reason", data.Reason)
	writeWatchFrameOptionalField(b, "intent", data.Intent)
	if data.ExitCode != nil {
		writeWatchFrameOptionalField(b, "exit_code", strconv.Itoa(*data.ExitCode))
	}
	b.WriteString("  output_bytes: ")
	b.WriteString(strconv.FormatInt(data.OutputBytes, 10))
	b.WriteString("\n")
}

func limitedWatchEventText(s string) (string, bool) {
	limited := limitWatchText(s, events.ToolEndExcerptRunes)
	return limited, limited != s
}

// watchToolEndField renders one text field of an assistant.tool frame. A text
// the event cut is already its first ToolEndExcerptRunes runes, with the full
// byte length alongside; the frame renders that excerpt exactly as it rendered
// the whole text when the event carried it, indicator and flag included, so a
// frame for a bounded event and one for the same text unbounded are the same
// bytes.
//
// A cut text is the excerpt bound exactly, so it needs room for the indicator
// even though no second bound applies; the copy is over at most
// ToolEndExcerptRunes runes, not the output's full length.
func watchToolEndField(text string, fullBytes int) (string, bool) {
	if fullBytes <= 0 {
		return limitedWatchEventText(text)
	}
	runes := []rune(text)
	keep := min(len(runes), toolEndExcerptKeepRunes)
	return string(runes[:keep]) + watchTruncatedIndicator, true
}

// toolEndExcerptKeepRunes is how much of a bounded field fits beside the
// truncation indicator within the excerpt bound. The indicator is ASCII, so its
// byte length is its rune count.
const toolEndExcerptKeepRunes = events.ToolEndExcerptRunes - len(watchTruncatedIndicator)

func writeWatchFrameOptionalField(b *strings.Builder, name, value string) {
	value = limitWatchText(value, watchTriggerMaxChars)
	if value == "" {
		return
	}
	writeWatchFrameTextField(b, name, value)
}

func writeWatchFrameTextField(b *strings.Builder, name, value string) {
	value = lineend.Normalize(value)
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(": ")
	b.WriteString(strings.ReplaceAll(value, "\n", "\n    "))
	b.WriteString("\n")
}

func writeWatchFrameBoolField(b *strings.Builder, name string, value bool) {
	b.WriteString("  ")
	b.WriteString(name)
	b.WriteString(": ")
	if value {
		b.WriteString("true\n")
	} else {
		b.WriteString("false\n")
	}
}

func limitWatchText(s string, maxChars int) string {
	if maxChars <= 0 || len([]rune(s)) <= maxChars {
		return s
	}
	indicator := watchTruncatedIndicator
	keep := maxChars - len([]rune(indicator))
	if keep <= 0 {
		return string([]rune(s)[:maxChars])
	}
	return string([]rune(s)[:keep]) + indicator
}

func (jm *jobManager) enqueueWatchNotifications(notifications []jobNotification) {
	_ = jm.enqueueNotifications(jm.routeWatchNotifications(notifications))
}

// routeWatchNotifications delivers every watch notification addressed to
// another session and RETURNS the ones bound for this session's own rail,
// rather than queueing them. A caller that has more to say about the same
// event (armFinalizedJob, which also has the job's terminal status) can then
// queue the whole group with one wake; enqueueWatchNotifications is the
// nothing-else-to-add case.
func (jm *jobManager) routeWatchNotifications(notifications []jobNotification) []jobNotification {
	if len(notifications) == 0 {
		return nil
	}
	jm.watchNotifyMu.Lock()
	defer jm.watchNotifyMu.Unlock()
	jm.mu.Lock()
	closing := jm.closing
	jm.mu.Unlock()
	if closing {
		return nil
	}
	var own []jobNotification
	type receiverGroup struct {
		sessionID string
		notify    func(jobNotification)
		hold      func() func()
		notices   []jobNotification
	}
	var receiverGroups []receiverGroup
	groupBySession := make(map[string]int)
	for _, n := range notifications {
		if n.receiverNotify != nil {
			if n.receiverHoldWake == nil {
				enqueue := n.receiverNotify
				n.receiverNotify = nil
				n.receiverHoldWake = nil
				enqueue(n)
				continue
			}
			group, ok := groupBySession[n.receiverSessionID]
			if !ok {
				group = len(receiverGroups)
				groupBySession[n.receiverSessionID] = group
				receiverGroups = append(receiverGroups, receiverGroup{
					sessionID: n.receiverSessionID,
					notify:    n.receiverNotify,
					hold:      n.receiverHoldWake,
				})
			}
			n.receiverNotify = nil
			n.receiverHoldWake = nil
			receiverGroups[group].notices = append(receiverGroups[group].notices, n)
			continue
		}
		if n.receiverSessionID != "" && n.receiverSessionID != jm.sessionID {
			continue
		}
		own = append(own, n)
	}
	for _, group := range receiverGroups {
		release := group.hold()
		func() {
			defer release()
			for _, n := range group.notices {
				group.notify(n)
			}
		}()
	}
	return own
}
