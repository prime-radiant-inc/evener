package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
)

// acceptNotificationInput records a job-completion notification turn at the
// start of an input turn. It mirrors acceptContinuationInput's framing — the
// drained queue is delivered to the model as a schema.TurnSteering reminder (a
// user-role message that expandHistory passes through without rendering a user
// bubble), so prepareModelRequestWithError rebuilds the request from s.history and the
// reminder reaches the model THIS turn. Unlike acceptUserInput it skips the
// namer, the UserPromptSubmit hooks, the MaxTurns check, and the s.turns++
// accounting (a notification is not a user turn).
//
// It returns proceed=false on an empty queue, having set s.sessionEndEmitted so
// the drain loop's idle tail suppresses the phantom SESSION_END{input_complete} —
// an empty notification turn is a true no-op that makes no model request.
// acceptNotificationInput decides whether this wake has anything to deliver and
// prepares it. turnID is the name processOneInput already took for the turn --
// empty only for a session no daemon serves, which needs no name. The caller
// stands down before reaching here when a served session could not be named, so
// every wake that gets this far can address its own turn.
func (s *Session) acceptNotificationInput(ctx context.Context, turnID string) (proceed bool) {
	s.drivePendingDelegateAttention()
	// Drive signal (b) (spec §3): a child driven on its pending caller-targeted
	// watch sends may have no token queued yet (the drive was launched on the
	// hasPendingWatchSends signal, not a token enqueue). Enqueue the OWN session's
	// pending caller tokens onto its rail before draining so the frame renders in
	// this same drive turn. Caller-only — sidecar delivery stays at the boundary.
	s.enqueueOwnCallerWatchSendTokens()
	jobNotifs, retryJobNotifs, injectedJobNotifs := s.filterDeliverableJobNotifications(s.drainJobNotifications())
	jobNotifs, injectedJobNotifs = s.withoutAwaitingRequest(jobNotifs), s.withoutAwaitingRequest(injectedJobNotifs)
	if sessionLifecycleFault(ctx, "inject_watch_notification") != nil {
		jobNotifs = append(jobNotifs, deliverableJobNotification{
			notification: jobNotification{Kind: jobNotificationKindWatch, JobID: "injected-watch", Status: jobNotificationEventWatch},
			watchJM:      &jobManager{},
		})
	}
	hasSteering := s.hasPendingSteering()
	hasRootAttention := s.hasPendingRootDelegateAttention()
	// A delegate parked in its finalization drain is woken for its own
	// delegates' updates (hasPendingDelegateUpdateAttention); the update is
	// already in its transcript, so the turn's model request carries it. Only
	// a turn that consumes what it presents is admitted for it: a parent's
	// unleased drive of an idle delegate would present the update and leave it
	// pending.
	hasDelegateUpdate := s.coversPresentedAttention(ctx) && s.hasPendingDelegateUpdateAttention()
	s.requeueJobNotifications(retryJobNotifs)
	injectedFailures := s.markJobNotificationsDelivered(injectedJobNotifs)
	s.requeueJobNotifications(injectedFailures)
	// A parked rail stands down here too, even with attention pending: the
	// outer gate's carve-out reads the RAW queue depth, so a stale watch tick
	// (a token whose watch died, a cleared timer) phantom-opens it. The park
	// defers the attention, and a turn carrying nothing deliverable runs
	// nothing. The begin-consumed wake stays consumed — the IDs stay cached
	// and unpark re-arms them.
	// Notifications an earlier turn wrote into the transcript but never carried
	// in a dispatched request are still owed: this turn's request carries them.
	carried := s.jobNotificationsAwaitingRequest()
	if len(jobNotifs) == 0 && len(carried) == 0 && !hasSteering && !hasDelegateUpdate && (!hasRootAttention || s.rootAttentionRailParked()) {
		if len(retryJobNotifs) == 0 && len(injectedFailures) == 0 {
			s.resetJobNotificationRetry()
		}
		s.finishNotificationNoop()
		return false
	}

	s.repairOrphanedToolResults(context.Background(), "before accepting notification")

	// A notification turn adopts the union of the provenance carried by the
	// notifications it delivers, so events the turn emits (and any watch it
	// retriggers) stamp the origin watch's lineage. The echo still delivers and is
	// classified self-influenced by the breaker (bounded by the runaway fuse), not
	// suppressed.
	var notificationProvenance *provenance.Causal
	for _, n := range append(carried, jobNotifs...) {
		notificationProvenance = provenance.Union(notificationProvenance, n.notification.Provenance)
	}
	s.replaceActiveProvenance(notificationProvenance)

	// Persist before announcing. appendSteeringTurnDurably is this turn's last
	// refusal, and a turn announced before it can still be refused -- which
	// would leave the projection holding an open, active turn that
	// finishNotificationNoop's sessionEndEmitted then suppresses the close for.
	var reminder string
	if len(jobNotifs) > 0 {
		reminder = s.formatJobNotificationReminder(jobNotifs)
		if err := errors.Join(s.appendSteeringTurnDurablyForOwner(reminder, events.SteeringKindNotification, turnID), sessionLifecycleFault(ctx, "append_notification")); err != nil {
			s.requeueJobNotifications(jobNotifications(jobNotifs))
			s.finishNotificationNoop()
			return false
		}
	}

	// Every refusal is behind us, so open the turn. This is the ONLY thing that
	// gives a notification turn a turn of its own: its reminder rides
	// EventSteeringInjected, which opens nothing, and the steering-only and
	// attention-only wakes emit no reminder at all. Announcing outside the
	// jobNotifs block is what covers all three shapes.
	//
	// The announce precedes every content event of the turn. A boundary that
	// came after would leave that content attributed to the turn before it.
	s.emit(events.TurnStartedData{TurnID: turnID})
	if reminder != "" {
		s.emit(events.SteeringInjectedData{Text: reminder, Kind: events.SteeringKindNotification})
	}

	s.awaitRequestForJobNotifications(ctx, jobNotifs)

	// Drain any pending steering messages before the first LLM call (spec 2.5).
	s.injectDrainedSteering()
	return true
}

// settleDeliveredJobNotifications records jobNotifs delivered once a dispatched
// model request has carried their durable reminder, requeueing any whose
// delivery record failed, and settles the caller-targeted watch sends among
// them.
func (s *Session) settleDeliveredJobNotifications(ctx context.Context, jobNotifs []deliverableJobNotification) {
	deliveredFailures := s.markJobNotificationsDelivered(jobNotifs)
	s.requeueJobNotifications(deliveredFailures)
	if len(deliveredFailures) == 0 {
		s.resetJobNotificationRetry()
	}
	s.countJobNotificationsDelivered(len(jobNotifs) - len(deliveredFailures))
	// Settle caller-targeted watch sends only after the durable reminder turn
	// persisted (above): the durable pending survives an appendSteeringTurnDurably
	// failure for re-token (at-least-once contract, spec §4.3).
	for _, d := range jobNotifs {
		if d.watchJM == nil {
			continue
		}
		s.settleDeliveredWatchNotification(ctx, d)
	}
}

// injectLaterJobNotifications writes the reminder for notifications that
// arrived while a notification turn was running, at the round boundary, so the
// turn's next model request carries them instead of waiting for the turn to
// end. Completions among them settle when that request is dispatched (see
// awaitRequestForJobNotifications); a failed transcript write puts them all
// back in the queue for the next wake.
func (s *Session) injectLaterJobNotifications(ctx context.Context) {
	jobNotifs, retryJobNotifs, injectedJobNotifs := s.filterDeliverableJobNotifications(s.drainJobNotifications())
	jobNotifs, injectedJobNotifs = s.withoutAwaitingRequest(jobNotifs), s.withoutAwaitingRequest(injectedJobNotifs)
	s.requeueJobNotifications(retryJobNotifs)
	s.requeueJobNotifications(s.markJobNotificationsDelivered(injectedJobNotifs))
	if len(jobNotifs) == 0 {
		return
	}
	reminder := s.formatJobNotificationReminder(jobNotifs)
	if err := s.appendSteeringTurnDurablyForOwner(reminder, events.SteeringKindNotification, s.activeTurnOwner()); err != nil {
		s.requeueJobNotifications(jobNotifications(jobNotifs))
		return
	}
	for _, n := range jobNotifs {
		s.unionActiveProvenance(n.notification.Provenance)
	}
	s.emit(events.SteeringInjectedData{Text: reminder, Kind: events.SteeringKindNotification})
	s.awaitRequestForJobNotifications(ctx, jobNotifs)
}

// maxUndispatchedNotificationTurns bounds how many turns may end without
// dispatching a request after writing a completion's reminder. A turn that
// stopped or spent its rounds early is followed by one more; when that one
// cannot dispatch either (local context admission keeps refusing), the
// completion settles with that turn's failure visible rather than waking the
// session forever. Its reminder stays in the transcript for the next request.
const maxUndispatchedNotificationTurns = 2

// awaitedJobNotification is a completion whose reminder is in the transcript
// but which no dispatched request has carried yet.
type awaitedJobNotification struct {
	deliverable       deliverableJobNotification
	undispatchedTurns int
}

// awaitRequestForJobNotifications takes jobNotifs once their reminder is
// durable in the transcript. Durable completions stay owed until a dispatched
// model request carries them: settling one earlier would mark delivered a
// completion the model never receives when the turn ends before its next
// request (a stop, a round limit, a request refused by local context
// admission), and replay detection keeps a restart from writing it twice.
// Watch frames have no replay detection, so they settle here, before the model
// call, as they always have: a restart during the call must find nothing
// pending to render again.
func (s *Session) awaitRequestForJobNotifications(ctx context.Context, jobNotifs []deliverableJobNotification) {
	var settleNow []deliverableJobNotification
	s.pendingJobNotifsMu.Lock()
	for _, d := range jobNotifs {
		if d.terminalGen == "" {
			settleNow = append(settleNow, d)
			continue
		}
		s.jobNotifsAwaitingRequest = append(s.jobNotifsAwaitingRequest, awaitedJobNotification{deliverable: d})
	}
	s.pendingJobNotifsMu.Unlock()
	if len(settleNow) > 0 {
		s.settleDeliveredJobNotifications(ctx, settleNow)
	}
}

type awaitedNotificationKey struct{ jobID, terminalGen string }

// withoutAwaitingRequest drops the completions whose reminder is already in
// the transcript awaiting a request, so a wake that re-queues one cannot write
// its reminder again or settle it before that request.
func (s *Session) withoutAwaitingRequest(notifs []deliverableJobNotification) []deliverableJobNotification {
	s.pendingJobNotifsMu.Lock()
	awaited := make(map[awaitedNotificationKey]bool, len(s.jobNotifsAwaitingRequest))
	for _, a := range s.jobNotifsAwaitingRequest {
		awaited[awaitedNotificationKey{a.deliverable.notification.JobID, a.deliverable.terminalGen}] = true
	}
	s.pendingJobNotifsMu.Unlock()
	if len(awaited) == 0 {
		return notifs
	}
	return slices.DeleteFunc(notifs, func(d deliverableJobNotification) bool {
		return d.terminalGen != "" && awaited[awaitedNotificationKey{d.notification.JobID, d.terminalGen}]
	})
}

// jobNotificationsAwaitingRequest returns the completions whose reminder no
// dispatched request has carried yet.
func (s *Session) jobNotificationsAwaitingRequest() []deliverableJobNotification {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	out := make([]deliverableJobNotification, 0, len(s.jobNotifsAwaitingRequest))
	for _, a := range s.jobNotifsAwaitingRequest {
		out = append(out, a.deliverable)
	}
	return out
}

// settleJobNotificationsCarriedByRequest settles every completion awaiting a
// request once the round loop has dispatched one: the request was built from
// the transcript that holds their reminders.
func (s *Session) settleJobNotificationsCarriedByRequest(ctx context.Context) {
	s.pendingJobNotifsMu.Lock()
	carried := make([]deliverableJobNotification, 0, len(s.jobNotifsAwaitingRequest))
	for _, a := range s.jobNotifsAwaitingRequest {
		carried = append(carried, a.deliverable)
	}
	s.jobNotifsAwaitingRequest = nil
	s.pendingJobNotifsMu.Unlock()
	if len(carried) > 0 {
		s.settleDeliveredJobNotifications(ctx, carried)
	}
}

// endTurnForJobNotificationsAwaitingRequest runs when an input ends.
// Completions it left without a dispatched request are still owed; the drain
// loop does not run a notification turn straight after one, so the paced
// notification retry wakes the session for them. When the input was admitted
// to its round loop, the turn counts against each completion's allowance, and
// one that has now ended maxUndispatchedNotificationTurns admitted turns
// undispatched settles instead. A wake that stood down before admission
// spends nothing, so a stand-down that lasts (a failing mutation store) keeps
// the completion owed under the paced retry, which backs off to
// jobNotificationRetryMaxDelay.
func (s *Session) endTurnForJobNotificationsAwaitingRequest(admitted bool) {
	var exhausted []deliverableJobNotification
	s.pendingJobNotifsMu.Lock()
	owed := s.jobNotifsAwaitingRequest[:0]
	for _, a := range s.jobNotifsAwaitingRequest {
		if admitted {
			a.undispatchedTurns++
		}
		if a.undispatchedTurns >= maxUndispatchedNotificationTurns {
			exhausted = append(exhausted, a.deliverable)
			continue
		}
		owed = append(owed, a)
	}
	s.jobNotifsAwaitingRequest = owed
	if len(owed) > 0 {
		s.scheduleJobNotificationRetryLocked()
	}
	s.pendingJobNotifsMu.Unlock()
	if len(exhausted) > 0 {
		s.settleDeliveredJobNotifications(context.Background(), exhausted)
	}
}

func (s *Session) settleDeliveredWatchNotification(ctx context.Context, d deliverableJobNotification) {
	err := sessionLifecycleFault(ctx, "settle_watch")
	if err == nil {
		err = d.watchJM.settleWatchSendDelivered(d.watchCfg, d.watchState)
	}
	if err != nil {
		s.emit(events.WarningData{Message: "watch send settle failed: " + err.Error()})
	}
}

func (s *Session) finishNotificationNoop() {
	// No turn ran, so a needs_response rest the wake interrupted still stands,
	// and the drain loop then treats the input as awaiting, holding notifications
	// and goal turns as that rest did. A rest still owed was cancelled by the wake's turn
	// start; the drain-loop settle arms it again, from the end reason the
	// turn start reset.
	rest := SessionIdle
	s.mu.Lock()
	if s.restBeforeInput == SessionAwaiting {
		rest = SessionAwaiting
	}
	s.comm.endReason = s.endReasonBeforeInput
	s.resumeQuietRest = s.quietRestBeforeInput
	s.mu.Unlock()
	s.finishProcessingAtBoundary(context.Background(), rest)
	s.suppressSessionEnd()
}

func (s *Session) filterDeliverableJobNotifications(raw []jobNotification) ([]deliverableJobNotification, []jobNotification, []deliverableJobNotification) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	survivors := make([]deliverableJobNotification, 0, len(raw))
	durableRaw := make([]jobNotification, 0, len(raw))
	seenTok := make(map[jobstore.WatchSendKey]bool)
	for _, n := range raw {
		if n.WatchSend != nil {
			jm, cfg, state, ok := s.resolveWatchSendToken(n.WatchSend)
			if !ok {
				continue // stale token: replaced, cleared, dropped, or settled
			}
			if seenTok[n.WatchSend.Key] {
				continue
			}
			seenTok[n.WatchSend.Key] = true
			n.watchSendFrame = state.Frame
			n.Provenance = provenance.Union(n.Provenance, state.Provenance)
			survivors = append(survivors, deliverableJobNotification{notification: n, watchJM: jm, watchCfg: cfg, watchState: state})
			continue
		}
		if n.isWatch() {
			if n.WatchID != "" && !n.Terminal && !s.timerWatchIsLive(n.WatchID) {
				continue // the timer was cleared after this tick was built
			}
			survivors = append(survivors, deliverableJobNotification{notification: n})
			continue
		}
		durableRaw = append(durableRaw, n)
	}
	if len(durableRaw) == 0 {
		return survivors, nil, nil
	}
	if s.jobManager == nil {
		return survivors, durableRaw, nil
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return survivors, durableRaw, nil
	}
	alreadyInjected := make(map[string]bool, len(durableRaw))
	for _, n := range durableRaw {
		rec := recs[n.JobID]
		if rec == nil || !jobstore.ShouldDeliver(rec) {
			continue
		}
		if _, ok := alreadyInjected[rec.JobID]; !ok {
			alreadyInjected[rec.JobID] = s.jobNotificationAlreadyInjected(rec.JobID)
		}
	}
	durableSurvivors, injected := classifyDurableNotifications(durableRaw, recs, alreadyInjected)
	survivors = append(survivors, durableSurvivors...)
	return survivors, nil, injected
}

// timerWatchIsLive reports whether the timer with this id is still installed.
// A timer's key is reconstructible from its id because its slot is the id, so
// this is one map lookup under jm.mu, taken with pendingJobNotifsMu released.
// With no manager to ask it fails OPEN: this answer only ever decides a drop,
// and delivering a tick whose timer cannot be checked beats losing it.
func (s *Session) timerWatchIsLive(watchID string) bool {
	jm := s.jobManager
	if jm == nil {
		return true
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: runtimeMessageAliasCaller, Slot: watchID}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.watches[key] != nil
}

// classifyDurableNotifications is the pure durable-notification classification
// core: given the drained durable notifications, the loaded job records, and a
// per-job "already injected into history" snapshot, it partitions the durable
// entries into fresh survivors and already-injected replays. It applies the
// ShouldDeliver gate and the terminal-generation dedupe (DedupeKey), so an exact
// duplicate terminal event contributes at most one deliverable. It takes only
// data snapshots and performs no I/O, locking, mutation, or event emission.
func classifyDurableNotifications(
	durableRaw []jobNotification,
	recs map[string]*jobstore.JobRecord,
	alreadyInjected map[string]bool,
) (survivors, injected []deliverableJobNotification) {
	seen := make(map[jobstore.DedupeKey]bool)
	for _, n := range durableRaw {
		rec := recs[n.JobID]
		if rec == nil || !jobstore.ShouldDeliver(rec) {
			continue
		}
		key := rec.DedupeKey()
		if seen[key] {
			continue
		}
		seen[key] = true
		deliverable := deliverableJobNotification{
			notification: jobNotificationFromRecord(rec),
			terminalGen:  rec.TerminalGen,
		}
		if alreadyInjected[rec.JobID] {
			injected = append(injected, deliverable)
			continue
		}
		survivors = append(survivors, deliverable)
	}
	return survivors, injected
}

func (s *Session) jobNotificationAlreadyInjected(jobID string) bool {
	if jobID == "" {
		return false
	}
	needle := fmt.Sprintf("job_id=%q", jobID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		// Only runtime notification steering can acknowledge delivery. Untyped
		// runtime steering remains eligible for replay recovery.
		if turn.Kind != schema.TurnSteering || turn.SteeringSource != "" ||
			(turn.SteeringKind != "" && turn.SteeringKind != events.SteeringKindNotification) {
			continue
		}
		if durableJobNotificationAlreadyInjected(turn.Message.Text(), needle) {
			return true
		}
	}
	return false
}

func durableJobNotificationAlreadyInjected(text, needle string) bool {
	for {
		start := strings.Index(text, "<job-notification")
		if start < 0 {
			return false
		}
		text = text[start:]
		headerEnd := strings.IndexByte(text, '>')
		if headerEnd < 0 {
			return false
		}
		// The formatter escapes attribute delimiters. Only the header carries
		// delivery identity; output excerpts can contain arbitrary job IDs.
		header := text[:headerEnd]
		if strings.Contains(header, needle) &&
			!strings.Contains(header, `event="watch"`) &&
			!strings.Contains(header, `event="watch_send"`) &&
			!strings.Contains(header, `status="watch"`) {
			return true
		}
		end := strings.Index(text, "</job-notification>")
		if end < 0 {
			return false
		}
		text = text[end+len("</job-notification>"):]
	}
}

func (s *Session) markJobNotificationsDelivered(notifs []deliverableJobNotification) []jobNotification {
	if len(notifs) == 0 || s.jobManager == nil {
		return nil
	}
	var failed []jobNotification
	for _, n := range notifs {
		if n.terminalGen == "" {
			continue
		}
		if err := s.jobManager.appendEvent(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          s.jobManager.now(),
			JobID:       n.notification.JobID,
			TerminalGen: n.terminalGen,
		}); err != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("job notification delivery mark failed: %v", err)})
			failed = append(failed, n.notification)
			continue
		}
		// Forward the settle up so the parent's forwarded COPY of a child-owned job
		// settles too (spec §3): the child forwards only the Pending up, so without
		// this the parent's copy stays NotifyPending after the child self-delivers
		// and is later falsely escalated as "child unreachable:". Best-effort and
		// no-op for root sessions and non-forwarded terminals (forwardSnapshot
		// guards on the configured parent resource); the parent intake (forwardEvent) settles
		// the copy without enqueueing, so no spurious render.
		if ferr := s.jobManager.forwardSnapshot(jobstore.Event{
			Kind:        jobstore.EventJobNotificationDelivered,
			TS:          s.jobManager.now(),
			JobID:       n.notification.JobID,
			TerminalGen: n.terminalGen,
		}); ferr != nil {
			s.emit(events.WarningData{Message: fmt.Sprintf("job notification delivery forward failed: %v", ferr)})
		}
	}
	return failed
}

func jobNotifications(notifs []deliverableJobNotification) []jobNotification {
	if len(notifs) == 0 {
		return nil
	}
	out := make([]jobNotification, 0, len(notifs))
	for _, n := range notifs {
		out = append(out, n.notification)
	}
	return out
}

// formatJobNotificationReminder renders notification blocks for a notification
// turn, joined by newlines. Terminal job_finished blocks carry a bounded result
// excerpt (shell tail / delegate head) re-read from the job record at render
// time, so the durable-replay path stays consistent with the live path.
func (s *Session) formatJobNotificationReminder(jobNotifs []deliverableJobNotification) string {
	blocks := make([]string, 0, len(jobNotifs))
	for _, n := range jobNotifs {
		blocks = append(blocks, formatJobNotificationBlock(n.notification, s.terminalNotificationExcerpt(n.notification), s.canInstructTool("read_transcript")))
	}
	return strings.Join(blocks, "\n")
}
