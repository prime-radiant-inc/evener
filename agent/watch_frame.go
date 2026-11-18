package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/lineend"
	"primeradiant.com/evener/agent/provenance"
)

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
// Stability is what makes the ingress replay-safe: appendDelegateNotification-
// Durably treats the same identity and content as a no-op and a reused identity
// with different content as corruption, so a replayed enqueue must derive the
// SAME id and a genuinely different notice must derive a different one. A
// watch-send diagnostic carries no delivery lineage of its own -- OriginWatchID
// is display-only -- so its identity is the watch, the job and the reason: a
// repeat of one failure is one item, and a distinct failure is a distinct item.
func stableWatchNoticeAttentionID(n jobNotification) string {
	scope := n.WatchID
	if scope == "" {
		scope = n.OriginWatchID
	}
	if scope == "" {
		scope = n.JobID
	}
	sum := sha256.Sum256([]byte(n.Reason))
	return "watchdiag:" + scope + ":" + n.JobID + ":" + hex.EncodeToString(sum[:8])
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
func (jm *jobManager) persistStableWatchPending(n jobNotification) bool {
	owner, controller := jm.stableWatchOwner()
	if owner == nil || owner.owningDelegateID == "" {
		return false
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
	if _, err := owner.appendDelegateNotificationDurably(attentionID, content); err != nil {
		if controller != nil {
			controller.attentionCauses.forget(attentionID)
		}
		// The append failed, so the notice is NOT durable. Queueing it would
		// reopen the unleased path this ingress exists to close; dropping it
		// silently would lose a failure report. Surface it as an operator
		// diagnostic and report the notice handled, so the caller does not fall
		// back to the queue.
		owner.emit(warningDataFromError("persist stable watch attention", err))
		return true
	}
	if err := owner.armDelegateAttention(attentionID); err != nil {
		// The row IS durable; only its wake failed. armDelegateAttention
		// already retains the id for its own paced re-arm, so the notice is
		// not lost.
		owner.emit(warningDataFromError("arm stable watch attention", err))
	}
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
