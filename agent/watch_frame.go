package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/lineend"
	"primeradiant.com/evener/agent/provenance"
)

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

// watchSendDiagnosticForState is watchSendDiagnosticNotification for a site
// that holds the failed send's own state, and it stamps the send's delivery id
// together with the stage that failed as the notice's occurrence.
//
// Without that stamp two failures of the SAME watch and job whose rendered
// reasons happen to match — "watch send pending state failed: <err>" is the
// same sentence for two different deliveries when the store keeps returning the
// same error — collapsed onto one attention identity. The second failure then
// reused the first's resolved row: no new row, no wake, and a lost notice
// (#2340 review). Replaying one delivery's diagnostic still yields its original
// identity, because the delivery id is minted once per send.
//
// The stage is part of the identity for the same reason the delivery is. One
// delivery can fail more than once at DIFFERENT stages: the write of the
// dropped state fails, a retry succeeds, and the final drop diagnostic follows
// for the same delivery. Without the stage both notices claimed one identity,
// and the transcript's conflicting-content check rejected the second — so the
// final diagnostic never landed and its retry kept the runtime resident
// (#2340 review M4). Replaying one stage reuses its row; a different stage is a
// different notice. The payload is stable across retries of one stage because
// every retry re-persists the same notice object.
func watchSendDiagnosticForState(state jobstore.WatchSendState, stage string, reason string) jobNotification {
	n := watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, reason)
	if state.DeliveryID != "" {
		n.Occurrence = "diagnostic:" + stage + ":" + state.DeliveryID
	}
	return n
}

// The diagnostic stages a watch-send delivery can fail at. Each is part of its
// notice's occurrence identity (see watchSendDiagnosticForState), so one
// delivery's failures at different stages are different notices.
const (
	watchSendStagePendingState  = "pending_state"
	watchSendStageStableEnqueue = "stable_enqueue"
	watchSendStageDroppedState  = "dropped_state"
	watchSendStageDrop          = "drop"
	watchSendStageEvicted       = "evicted"
)

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
	// A condition fire's delivery id is minted once per fire and reused on a
	// replay, so it is exactly an occurrence identity: two distinct fires with
	// identical reasons stay distinct, and replaying one reuses its original
	// durable attention id instead of appending a second row (LIFE-01, #2340).
	if deliveryID != "" {
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

// stableWatchOwner snapshots the stable delegate owner and its controller under
// jm.mu. Every reader of stableOwner must take this snapshot: a new generation
// rewrites the field in bindDelegateParent under the same mutex, and a timer or
// shell callback enqueues concurrently with exactly that rewrite. The caller
// holds no lock afterwards, so transcript I/O and wake callbacks below run
// outside jm.mu.
func (jm *jobManager) stableWatchOwner() (*Session, *delegateTreeController) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	return jm.stableOwner, jm.delegateController
}

// deliverableStableWatchAttention applies the queue's delivery rules to a group
// of watch notices before the stable-attention ingress persists them, so the
// leased path obeys the SAME rules as the ordinary queue instead of a subset of
// them (LIFE-01, #2340 review).
//
// Two rules are applied here, and both are CALLS, not restatements:
//
//   - deliverability. watchTickDeliverable decides whether a tick may still be
//     delivered, and it is the same predicate filterDeliverableJobNotifications
//     calls for the ordinary queue. A cleared timer's tick is dropped on both
//     routes by one implementation.
//   - folding. Ticks from one watch collapse into a single counted reminder,
//     exactly as appendOrFoldJobNotificationLocked folds them in the queue. The
//     attention entry is replaced rather than appended a second time, and its
//     Fires carry the total, so the reminder says how many ticks it stands for.
//
// Deterministic order is kept so replay of the same group persists identically.
func (s *Session) deliverableStableWatchAttention(notifs []jobNotification) []jobNotification {
	out := make([]jobNotification, 0, len(notifs))
	foldedAt := make(map[string]int, len(notifs))
	for _, n := range notifs {
		if !s.watchTickDeliverable(n) {
			continue
		}
		if n.WatchID != "" && !n.Terminal {
			if at, ok := foldedAt[n.WatchID]; ok {
				p := &out[at]
				p.Fires += n.Fires
				p.Provenance = provenance.Union(p.Provenance, n.Provenance)
				continue
			}
			foldedAt[n.WatchID] = len(out)
		}
		out = append(out, n)
	}
	return out
}

// persistStableWatchAttention turns a watch notice bound for a stable delegate
// child into durable stable attention (LIFE-01, #2340).
//
// A stable child is a delegate session, so every model turn it runs must be an
// exact generation under a lease (docs/subagent-management/11-delegate-resource-model.md).
// A watch notice pushed onto its ordinary notification queue instead wakes the
// child through the notification drive, which renders a model-bearing
// EntryNotification turn WITHOUT reserving or committing a generation: the
// controller reports the child idle while the child calls its provider, so
// normal generation accounting, stop admission, and result settlement do not
// cover that turn. Diagnostics are the reachable case, because a failed watch
// send is exactly when no other attention is pending.
//
// Stability comes from the notice's identity, so a replayed enqueue refolds
// into the same attention item instead of appending a second one. Watch
// diagnostics carry no delivery lineage of their own (OriginWatchID is display
// only), so their identity is the watch, the job, and the reason: a repeat of
// the same failure is one item, and a distinct failure is a distinct item.
//
// The notice never enters the plain queue. On success a lease-governed
// attention run delivers it; on failure the error is returned so the caller can
// surface it as an operator diagnostic rather than silently starting an
// unleased turn.
func persistStableWatchAttention(owner *Session, controller *delegateTreeController, n jobNotification) (string, error) {
	attentionID, err := appendStableWatchAttention(owner, controller, n)
	if err != nil {
		return "", err
	}
	// Nothing to arm: the notice was a stale token, or a frame the receiver
	// already holds and only its acknowledgement was owed.
	if attentionID == "" {
		return "", nil
	}
	if err := owner.armDelegateAttention(attentionID); err != nil {
		return "", err
	}
	return attentionID, nil
}

// appendStableWatchAttention writes the notice's durable attention row WITHOUT
// arming it. Split from the arm so a GROUP can be made fully durable before the
// first wake: a wake that lands while later rows in the same batch are still
// unwritten lets the child's first request miss them and start a second
// generation for one batch (LIFE-01, #2340 review L2).
//
// A caller-targeted wake token is resolved here, against the CURRENT pending
// frame, before anything is rendered or persisted (LIFE-01, #2340 review). A
// token is not a diagnostic: its content is a rendered frame that only the
// resolution step populates, so persisting it unrendered gave the model an
// empty watch-send body while the send stayed pending for good. The token's
// family is the send rail's ("watch:<delivery>:<seq>"), not the diagnostic one,
// and the send is acknowledged only after its frame is durable.
//
// An empty return with no error means there is nothing to arm: a stale token
// (replaced, cleared, dropped or settled) or a frame the receiver already took
// whose acknowledgement was the only thing owed.
func appendStableWatchAttention(owner *Session, controller *delegateTreeController, n jobNotification) (string, error) {
	if n.WatchSend != nil {
		return appendCallerWatchSendAttention(owner, controller, n.WatchSend)
	}
	// A notice whose family this receiver already holds a PENDING row for
	// supersedes that row rather than appending beside it.
	// deliverableStableWatchAttention folds within ONE enqueue batch, but a
	// timer callback enqueues its own batch: two ticks of one timer then wrote
	// two rows the model read as two reminders, and a newer long-running-job
	// snapshot queued behind an older one listing jobs that had already
	// finished. The queue folded both across callbacks; this is the same rule
	// (#2340 review M2).
	folded, superseded := owner.foldPendingStableWatchNotice(n)
	n = folded
	attentionID := stableWatchDiagnosticAttentionID(n)
	// Register the notice's own cause BEFORE the item can be presented: a
	// runaway-watch diagnostic carries the chain the breaker stopped, and the
	// response event must descend from it or the breaker reads that response as
	// a fresh trigger and fires again. This is the same registration the stable
	// watch-send frame makes (watch_send_rail.go), so the lineage survives the
	// hop identically.
	if controller != nil {
		controller.attentionCauses.remember(attentionID, n.Provenance)
	}
	// An accepted notice is news for the job_status repeat guard, exactly as a
	// queued one is: the same rule, the same helper (LIFE-01, #2340 review L3).
	owner.forgetJobStatusPollForNotification(n)
	content := owner.formatJobNotificationReminder([]deliverableJobNotification{{notification: n}})
	if _, err := owner.appendDelegateNotificationDurably(attentionID, content); err != nil {
		if controller != nil {
			controller.attentionCauses.forget(attentionID)
		}
		return "", err
	}
	owner.notePendingStableWatchNotice(n, attentionID)
	// The survivor is durable, so the rows it folded are retired rather than
	// presented: keeping them pending would show the model the older tick or
	// snapshot beside the newer one that already replaced it. A failed
	// discard keeps them pending -- the retry appends a row that already
	// exists, folds onto it, and tries again (#2340 review M2).
	owner.discardSupersededStableWatchNotices(controller, superseded)
	return attentionID, nil
}

// appendCallerWatchSendAttention turns a caller-targeted wake token into durable
// attention carrying its rendered frame, then acknowledges that exact send.
//
// The token names a delivery by key, update sequence and delivery id; the frame
// lives in the manager's pending entry, and only resolveWatchSendToken can say
// whether this token is still the current one (latest-frame-wins). Rendering
// from the token itself is the bug this replaces: a token carries no body, so
// the model was woken by an empty watch-send block while the send it named
// stayed pending and re-tokened forever.
//
// The acknowledgement is the same durable settle the ordinary rail performs
// after its reminder is persisted, and it runs only after the frame is durable
// here — at-least-once holds either way, because an unsettled frame is still
// pending and its token is re-minted.
func appendCallerWatchSendAttention(owner *Session, controller *delegateTreeController, tok *watchSendToken) (string, error) {
	jm, cfg, state, ok := owner.resolveWatchSendToken(tok)
	if !ok {
		return "", nil // stale token: replaced, cleared, dropped, or settled
	}
	// A caller token names a send the owning session renders on its OWN rail.
	// A non-caller target here would be a mis-tokened sidecar frame, which the
	// send rail delivers itself; leave it to that rail rather than render it.
	if state.Key.ResolvedSendTo != runtimeMessageAliasCaller {
		return "", nil
	}
	if jm.watchSendTranscribed(state.DeliveryID) {
		// The frame is already durable in this receiver's transcript and only
		// its acknowledgement is outstanding.
		return "", jm.settleTranscribedWatchSend(cfg, state)
	}
	attentionID := stableWatchAttentionID(state)
	rendered := jm.renderWatchSendFrame(cfg, state)
	if rendered != state.Frame {
		// The hourly backstop notice renders content the pending state does not
		// hold. Persist the exact frame first, so a replay after a restart
		// presents the same text instead of conflicting with the row the
		// receiver already took — the same ordering the sidecar rail keeps.
		noticed := state
		noticed.Frame = rendered
		if err := jm.appendWatchSendEvents([]jobstore.Event{{
			Kind:      jobstore.EventWatchSendPending,
			TS:        jm.now(),
			WatchSend: &noticed,
		}}); err != nil {
			return "", err
		}
		jm.rememberRenderedWatchSendFrame(cfg, noticed)
	}
	if controller != nil {
		controller.attentionCauses.remember(attentionID, state.Provenance)
	}
	// The rendered frame is the content, under the send rail's own family, so
	// the projection filter and the settlement retry recognise it as watch
	// attention of the send family rather than a diagnostic.
	if _, err := owner.appendDelegateNotificationDurably(attentionID, stableWatchNotificationContent(rendered)); err != nil {
		if controller != nil {
			controller.attentionCauses.forget(attentionID)
		}
		return "", err
	}
	// The frame is durable, so from here a settle failure must not render it a
	// second time: the transcribed mark turns the retry into an acknowledgement
	// only.
	jm.noteWatchSendTranscribed(state.DeliveryID)
	if err := jm.settleWatchSendDelivered(cfg, state); err != nil {
		return "", err
	}
	return attentionID, nil
}

// stableWatchPendingNotice retains one pending stable-watch row's NOTICE FACTS
// so the leased presentation can re-apply the delivery rules the ordinary queue
// applies when it folds (#2340 review M2).
//
// A record is what the notice would still render as, plus the identity its
// presentation has to answer for:
//
//   - occurrence is the notice's own firing identity. A later notice of the
//     same occurrence is a REPLAY of this one (a retry, a restore) and must not
//     supersede it.
//   - watchID, jobID, snapshot and diagnostic identify the notice's family, so
//     the presentation rules can tell "one watch's second tick" from "an
//     unrelated notice that happens to share a batch".
//   - order is arrival sequence, so an older long-running snapshot can be
//     recognised as older than the one already held.
type stableWatchPendingNotice struct {
	attentionID string
	occurrence  string
	// family names the fold group the notice belongs to (see
	// stableWatchPendingFamily), or "" for a notice that is a fact of its own.
	family string
	// fires is the total the row stands for, so a tick folded into it on the
	// durable side still counts its predecessor's fires.
	fires int
	// provenance is the union of the folded notices' lineages: the turn that
	// presents the row must descend from every fire it answers.
	provenance *provenance.Causal
	// watchID is the timer the row belongs to, for the liveness recheck.
	watchID string
	// terminal marks a row whose registration is GONE BY CONSTRUCTION: a
	// one-shot timer unregisters itself before it enqueues the notice of its
	// only fire, so "the timer is no longer installed" is the normal state of a
	// terminal row, not evidence that the delegate cleared it. The liveness
	// recheck must skip these rows or it discards the firing it was written to
	// protect (#2340 review).
	terminal bool
	// order is arrival sequence: a newer snapshot replaces an older one, and a
	// moved record can be told from a still-current one.
	order uint64
}

// stableWatchPendingFamily names the notice family whose pending rows fold
// together, or "" when the notice is a fact of its own.
//
// Only two families fold, and they are the queue's own two fold rules: a
// repeating timer's ticks collapse into one counted reminder, and a
// long-running-jobs reminder is a SNAPSHOT that a newer one replaces. A
// terminal notice, a one-shot fire and a failure diagnostic are separate facts
// and are never folded away (#2340 review M2).
func stableWatchPendingFamily(n jobNotification) string {
	switch {
	case n.isLongRunningJobsReminder():
		return "snapshot"
	case n.WatchID != "" && !n.Terminal:
		return "tick:" + n.WatchID
	default:
		return ""
	}
}

// stableWatchNoticeOccurrence is the identity a replay of n reproduces. A
// producer that stamps Occurrence owns it; otherwise the notice's own facts
// identify it, exactly as its attention id does.
func stableWatchNoticeOccurrence(n jobNotification) string {
	if n.Occurrence != "" {
		return n.Occurrence
	}
	return stableWatchDiagnosticAttentionID(n)
}

// foldPendingStableWatchNotice applies the queue's cross-callback fold to n and
// records it as pending, returning the notice to persist and the rows the
// surviving notice supersedes.
//
// The durable row cannot be rewritten -- extendDelegateAttention rejects a
// second append of one attention id with different content, and that
// immutability is what makes the transcript the delivery ledger. So a fold is
// expressed as IDENTITY plus RETIREMENT: the surviving notice carries the
// TOTAL (a summed fire count, the newer snapshot), it persists under its own
// occurrence, and the rows it supersedes are discarded durably once that append
// has landed. Until the append lands the superseded rows stay pending, which is
// the at-least-once side of the trade: a failed append loses no firing.
func (s *Session) foldPendingStableWatchNotice(n jobNotification) (jobNotification, []string) {
	if s == nil {
		return n, nil
	}
	family := stableWatchPendingFamily(n)
	occurrence := stableWatchNoticeOccurrence(n)
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if s.stableWatchPending == nil {
		s.stableWatchPending = make(map[string]stableWatchPendingNotice)
	}
	s.stableWatchPendingSeq++
	var superseded []string
	if family != "" {
		for _, held := range s.stableWatchPending {
			if held.family != family || held.occurrence == occurrence {
				continue
			}
			// A tick of one watch folds its count into the survivor; every other
			// family member is simply replaced, because the survivor's content is
			// a newer statement of the same fact.
			if family == "tick:"+n.WatchID {
				n.Fires += held.fires
				n.Provenance = provenance.Union(n.Provenance, held.provenance)
			}
			superseded = append(superseded, held.attentionID)
		}
	}
	return n, superseded
}

// notePendingStableWatchNotice records that n's row is pending under its own
// occurrence, replacing whatever this session held for it.
func (s *Session) notePendingStableWatchNotice(n jobNotification, attentionID string) {
	if s == nil || attentionID == "" {
		return
	}
	family := stableWatchPendingFamily(n)
	record := stableWatchPendingNotice{
		attentionID: attentionID,
		occurrence:  stableWatchNoticeOccurrence(n),
		family:      family,
		fires:       n.Fires,
		provenance:  n.Provenance,
		watchID:     n.WatchID,
		terminal:    n.Terminal,
		order:       0,
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	if s.stableWatchPending == nil {
		s.stableWatchPending = make(map[string]stableWatchPendingNotice)
	}
	s.stableWatchPendingSeq++
	record.order = s.stableWatchPendingSeq
	s.stableWatchPending[attentionID] = record
}

// pendingStableWatchNotice returns the retained facts of a pending row.
func (s *Session) pendingStableWatchNotice(attentionID string) (stableWatchPendingNotice, bool) {
	if s == nil || attentionID == "" {
		return stableWatchPendingNotice{}, false
	}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	held, ok := s.stableWatchPending[attentionID]
	return held, ok
}

// stableWatchAttentionIDPrefix names the durable-attention rows this ingress
// creates for a stable delegate's watch notices. It is a constant because two
// places must agree on it by construction: the ID builder below, and
// dropUnresolvedWatchAttentionTurns, which keeps an unresolved row's turn out
// of an unleased projection (LIFE-01, #2340 review).
const stableWatchAttentionIDPrefix = "watchdiag:"

// stableWatchSendAttentionPrefix names the durable-attention rows the watch
// SEND rail creates for a stable receiver (see stableWatchAttentionID in
// watch_send_rail.go, which builds "watch:<deliveryID>:<seq>").
//
// The two families are separate: a diagnostic reports a send that FAILED, and a
// send row carries a frame that is pending. A predicate that means "watch
// attention, either family" must test both, or it silently misses half the
// rows — the trap the drain's own predicate documents.
const stableWatchSendAttentionPrefix = "watch:"

// isStableWatchSendAttentionID reports whether attentionID belongs to the watch
// send rail's family. The id is a display/identity string, so the marker is
// checked structurally: the prefix and the two separators stableWatchAttentionID
// always emits.
func isStableWatchSendAttentionID(attentionID string) bool {
	if !strings.HasPrefix(attentionID, stableWatchSendAttentionPrefix) || strings.HasPrefix(attentionID, stableWatchAttentionIDPrefix) {
		return false
	}
	rest := strings.TrimPrefix(attentionID, stableWatchSendAttentionPrefix)
	delivery, seq, found := strings.Cut(rest, ":")
	return found && delivery != "" && seq != ""
}

// isStableWatchAttentionID reports whether attentionID names durable attention
// created for a watch notice of either family: a send-rail frame or a send
// diagnostic. Callers that mean "watch attention" use this rather than testing
// one prefix, so a row can never be invisible to them (#2340 review M2 trap).
func isStableWatchAttentionID(attentionID string) bool {
	return strings.HasPrefix(attentionID, stableWatchAttentionIDPrefix) || isStableWatchSendAttentionID(attentionID)
}

// stableWatchDiagnosticAttentionID derives a replay-stable identity for a watch
// notice from the facts that make it ONE notice.
//
// The producer's Occurrence is authoritative when it supplies one: a repeating
// timer fires many ticks with the same watch id, empty job id and reason, and a
// long-running reminder's snapshot changes while its reason does not — both must
// yield a distinct id per fire and the same id on a replay of that fire. Without
// an occurrence, the notice's own facts identify it, and the reason is hashed
// because it is bounded free text; the id only has to be equal exactly when the
// notice is.
func stableWatchDiagnosticAttentionID(n jobNotification) string {
	scope := n.WatchID
	if scope == "" {
		scope = n.OriginWatchID
	}
	if scope == "" {
		scope = n.JobID
	}
	if n.Occurrence != "" {
		occurrence := sha256.Sum256([]byte(n.Occurrence))
		return stableWatchAttentionIDPrefix + scope + ":" + n.JobID + ":" + hex.EncodeToString(occurrence[:8])
	}
	sum := sha256.Sum256([]byte(n.Reason))
	return stableWatchAttentionIDPrefix + scope + ":" + n.JobID + ":" + hex.EncodeToString(sum[:8])
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
