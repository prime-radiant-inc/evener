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
// as the notice's occurrence.
//
// Without that stamp two failures of the SAME watch and job whose rendered
// reasons happen to match — "watch send pending state failed: <err>" is the
// same sentence for two different deliveries when the store keeps returning the
// same error — collapsed onto one attention identity. The second failure then
// reused the first's resolved row: no new row, no wake, and a lost notice
// (#2340 review). Replaying one delivery's diagnostic still yields its original
// identity, because the delivery id is minted once per send.
func watchSendDiagnosticForState(state jobstore.WatchSendState, reason string) jobNotification {
	n := watchSendDiagnosticNotification(state.Key.WatchID, state.Key.ResolvedWatchedIdentity, reason)
	if state.DeliveryID != "" {
		n.Occurrence = "diagnostic:" + state.DeliveryID
	}
	return n
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
func appendStableWatchAttention(owner *Session, controller *delegateTreeController, n jobNotification) (string, error) {
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
	return attentionID, nil
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
