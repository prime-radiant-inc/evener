package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/lineend"
	"primeradiant.com/evener/agent/provenance"
)

// stableWatchAttentionIDPrefix names every durable watch attention row this
// ingress writes. Classification by this prefix is what lets the unleased
// projection tell a watch row apart from any other delegate attention.
const stableWatchAttentionIDPrefix = "watchdiag:"

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

// watchSendDiagnosticNotificationForDelivery is watchSendDiagnosticNotification
// for a failure that names the delivery it is about.
//
// The delivery id is what makes the diagnostic a distinct occurrence. Without
// it, every firing of one watch shares a source identity, so the second
// diagnostic about the same watch, job and reason reads as a replay of the
// first and the durable attention fold treats it as already resolved.
func watchSendDiagnosticNotificationForDelivery(watchID, jobID, deliveryID, reason string) jobNotification {
	n := watchSendDiagnosticNotification(watchID, jobID, reason)
	if deliveryID != "" {
		n.Occurrence = "delivery:" + deliveryID
	}
	return n
}

// stableWatchNoticeAttentionID is the durable attention identity of a watch
// notice a leased handoff presents for a stable delegate child.
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
	sum := sha256.Sum256([]byte(n.Reason))
	return stableWatchAttentionIDPrefix + scope + ":" + n.JobID + ":" + hex.EncodeToString(sum[:8]) + ":" + stableWatchNoticeOccurrence(n)
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
// of the earlier one.
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

// isStableWatchAttentionID reports whether attentionID names a watch notice
// produced by this ingress. Classification is by identity, never by message
// text: every id this file mints starts with stableWatchAttentionIDPrefix.
func isStableWatchAttentionID(attentionID string) bool {
	return strings.HasPrefix(attentionID, stableWatchAttentionIDPrefix)
}

// stableWatchAttentionResolved reports whether the owner's transcript fold
// already carries a resolution for attentionID, either consumed by a generation
// or discarded.
//
// It answers exactly one question: is this row still owed? A pending row is,
// and a row this drive's own reservation has just accepted is too — acceptance
// resolves it, but the notice that resolution consumed is this generation's to
// present, and the caller checks that case before asking here.
func (s *Session) stableWatchAttentionResolved(attentionID string) bool {
	if s == nil || attentionID == "" {
		return false
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	fold, err := s.delegateAttentionFoldLocked()
	if err != nil {
		return false
	}
	resolution, ok := fold.resolutions[attentionID]
	return ok && (resolution == delegateAttentionConsumed || resolution == delegateAttentionDiscarded)
}

// queuedWatchNoticePresentable reports whether the leased handoff may turn n
// into durable attention. A watch-send token is not: its frame is rendered by
// key from the sending manager's live state at accept time, so the notification
// turn that owns the token delivers it and no row is frozen for it here.
func queuedWatchNoticePresentable(n jobNotification) bool {
	return n.isWatch() && n.WatchSend == nil
}

// appendStableWatchNotice writes the durable attention row for n under
// attentionID, WITHOUT arming the wake.
//
// The split exists because a drive may already hold the reservation for this
// row when the row is written: arming blocks until that reservation closes, so
// the drive appends first and its own generation publishes the wake.
//
// A return of err leaves the notice owed and not durable. The caller keeps its
// queue entry under the same identity, so the notification retry replays one
// occurrence rather than starting a second item: appendDelegateNotification-
// Durably treats the same identity and content as a no-op.
//
// appended=false with no err means the row was ALREADY durable under this
// identity: this notice is a replay of a statement the fold has recorded. The
// caller must then decide by the fold's resolution, not assume the row is its
// own to consume.
func (jm *jobManager) appendStableWatchNotice(n jobNotification, attentionID string) (appended bool, err error) {
	owner, controller := jm.stableWatchOwner()
	if owner == nil || owner.owningDelegateID == "" {
		return false, errors.New("stable watch notice has no stable delegate owner")
	}
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
	return owner.appendDelegateNotificationDurably(attentionID, content)
}

// queuedWatchNoticeAttentionID reports the attention identity of the first
// queued watch notice a leased handoff could present, or "" when the queue
// holds none.
//
// Every watch notice waits on s's ordinary notification queue, so the queue's
// own rules state it: a repeating timer's missed ticks fold into one entry, a
// newer long-running-jobs reminder replaces an older listing, a tick whose
// timer was cleared is dropped at delivery, and a terminal cutoff drops what
// the model declined to wait for. The identity is readable without taking the
// entry, which is what lets a drive reserve its generation FIRST and take the
// notice only once the reservation is in hand: notices arriving while the drive
// waits for capacity keep folding in place instead of freezing one row each.
func (s *Session) queuedWatchNoticeAttentionID() string {
	if s == nil {
		return ""
	}
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	for _, n := range s.pendingJobNotifs {
		if queuedWatchNoticePresentable(n) {
			return stableWatchNoticeAttentionID(n)
		}
	}
	return ""
}

// queuedWatchNotice returns the queued watch notice whose identity is
// attentionID, WITHOUT taking it off the queue.
func (s *Session) queuedWatchNotice(attentionID string) (jobNotification, bool) {
	if s == nil || attentionID == "" {
		return jobNotification{}, false
	}
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	for _, n := range s.pendingJobNotifs {
		if queuedWatchNoticePresentable(n) && stableWatchNoticeAttentionID(n) == attentionID {
			return n, true
		}
	}
	return jobNotification{}, false
}

// removeQueuedWatchNotice drops the queued watch notice whose identity is
// attentionID, reporting whether the queue still held it.
func (s *Session) removeQueuedWatchNotice(attentionID string) bool {
	if s == nil || attentionID == "" {
		return false
	}
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	for i, n := range s.pendingJobNotifs {
		if !queuedWatchNoticePresentable(n) || stableWatchNoticeAttentionID(n) != attentionID {
			continue
		}
		s.pendingJobNotifs = append(s.pendingJobNotifs[:i], s.pendingJobNotifs[i+1:]...)
		return true
	}
	return false
}

// presentQueuedWatchNotice turns the queued watch notice this drive has already
// reserved into its durable attention row, reporting whether the generation may
// go on to present it.
//
// The reservation comes first: while the tree is at its drive budget the notice
// waits on the queue and keeps folding, so however long capacity takes the
// transcript gains one row stating the whole wait rather than one row per
// missed tick.
//
// The row is written BEFORE the queue entry is removed. A failed append leaves
// the entry in place under the identity this drive reserved and arms the
// session's notification retry, whose firing wakes this parent's drive again:
// the same occurrence is replayed once the write succeeds, so a storage failure
// delays the notice instead of losing it.
//
// Every refusal releases the reservation it was given. A refusal with retry
// leaves the next attempt to re-read whatever is really owed; a cleared timer
// owes nothing, so its notice is dropped from the queue with no row written and
// no retry unless more watch work is waiting behind it.
//
// A replay of a row the fold has already RESOLVED is the same kind of no-op: the
// statement was presented and consumed by an earlier generation, so there is
// nothing left for this one to carry. The append reports the row was already
// durable, the fold reports it consumed, and the queued entry is dropped and the
// reservation released. Without that release the drive slot stays held for a row
// no later attempt can commit, which blocks every later start.
func (s *Session) presentQueuedWatchNotice(sub *subagent, reservation *delegateStartReservation, attentionID string) bool {
	abort := func(retry bool) bool {
		if err := s.delegateController.AbortStart(reservation); err != nil {
			s.emit(warningDataFromError("abort queued watch notice reservation", err))
		}
		if retry {
			s.delegateController.retryDelegateAttentionLater()
		}
		return false
	}
	if s == nil || sub == nil || sub.sess == nil || sub.sess.jobManager == nil || attentionID == "" {
		return false
	}
	// Nothing below can persist without a bound owner, and taking the notice
	// first would then drop it.
	if owner, _ := sub.sess.jobManager.stableWatchOwner(); owner == nil || owner.owningDelegateID == "" {
		return abort(false)
	}
	notice, queued := sub.sess.queuedWatchNotice(attentionID)
	if !queued {
		// The notice left the queue under another claim, or it was written to the
		// transcript by another rail. Either way this reservation has nothing to
		// add, and the retry re-reads whatever is really owed.
		return abort(true)
	}
	if notice.WatchID != "" && !notice.Terminal && !sub.sess.timerWatchIsLive(notice.WatchID) {
		// The timer was cleared while the notice waited: nobody owes it, so no
		// row is written for news the model can no longer act on.
		sub.sess.removeQueuedWatchNotice(attentionID)
		return abort(sub.sess.queuedWatchNoticeAttentionID() != "")
	}
	appended, err := sub.sess.jobManager.appendStableWatchNotice(notice, attentionID)
	if err != nil {
		sub.sess.emit(warningDataFromError("persist stable watch attention", err))
		sub.sess.scheduleJobNotificationRetry()
		return abort(true)
	}
	if !appended && sub.sess.stableWatchAttentionResolved(attentionID) {
		// The row is already durable and already consumed: this notice replays a
		// statement an earlier generation presented, so this one presents nothing.
		// Dropping the replay and releasing the reservation keeps the drive slot
		// free for work that is still owed.
		sub.sess.removeQueuedWatchNotice(attentionID)
		return abort(sub.sess.queuedWatchNoticeAttentionID() != "")
	}
	sub.sess.removeQueuedWatchNotice(attentionID)
	return true
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
