package agent

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/internal/jobstore"

	"primeradiant.com/evener/agent/events"
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

func (jm *jobManager) watchNotificationFromWatch(cfg *watchConfig, jobID, reason string, root *provenance.Causal) jobNotification {
	n := watchNotification(jobID, reason)
	if cfg == nil {
		return n
	}
	n.callbackConfig = cfg
	n.callbackTrigger = reason
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
	n.Provenance = provenance.WithWatch(root, cfg.watchID, cfg.generation, "", visibleSessionID, jobID)
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
		output, truncated := limitedWatchEventText(data.Output)
		writeWatchFrameTextField(b, "output", output)
		writeWatchFrameBoolField(b, "output_truncated", truncated)
	}
	if data.Error != "" {
		errText, truncated := limitedWatchEventText(data.Error)
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
	const maxEventTextChars = 1000
	limited := limitWatchText(s, maxEventTextChars)
	return limited, limited != s
}

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
	var err error
	notifications, err = jm.produceCallbackNotifications(notifications)
	// A failed member cannot hide source work already persisted by this batch.
	// Dispatch healthy IDs and route its successful legacy prefix before retry.
	jm.dispatchProducedWatchSends()
	var legacy []jobNotification
	for _, n := range notifications {
		if n.callbackConfig == nil || (jm.retirementOwner == nil && jm.delegateController == nil) {
			legacy = append(legacy, n)
		}
	}
	jm.enqueueNotifications(jm.routeWatchNotifications(legacy))
	if err != nil {
		jm.kick()
	}
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
	var unproduced []jobNotification
	for _, n := range notifications {
		if n.callbackConfig != nil && (jm.retirementOwner != nil || jm.delegateController != nil) {
			if _, err := jm.produceCallbackNotifications([]jobNotification{n}); err != nil {
				jm.kick()
			}
			continue
		}
		unproduced = append(unproduced, n)
	}
	jm.dispatchProducedWatchSends()
	notifications = unproduced
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
			own = append(own, watchSendDiagnosticNotification(n.WatchID, n.JobID, "watch callback receiver unavailable: "+n.receiverSessionID))
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

func (jm *jobManager) produceCallbackNotifications(notifications []jobNotification) ([]jobNotification, error) {
	type callbackBudgetRelease struct {
		cfg        *watchConfig
		deliveryID string
	}
	var releases []callbackBudgetRelease
	defer func() {
		for _, release := range slices.Backward(releases) {
			jm.releaseCallbackBudgetOwner(release.cfg, release.deliveryID)
		}
	}()
	var produced []jobNotification
	var errs []error
	for _, n := range notifications {
		cfg := n.callbackConfig
		if cfg == nil {
			produced = append(produced, n)
			continue
		}
		jm.mu.Lock()
		trigger := n.callbackTrigger
		condition := strings.HasPrefix(trigger, "event: ") || strings.HasPrefix(trigger, "output_match: ")
		if condition && cfg.budgetTripped {
			jm.mu.Unlock()
			continue
		}
		identity := cfg.target
		if trigger == watchBudgetEndTrigger {
			identity = ""
		}
		d := jm.watchSendSnapshot(cfg, identity, trigger, events.SessionEvent{SessionID: jm.sessionID, Provenance: n.Provenance}).withSelfInfluence(jm.classifySelfInfluenceLocked(cfg, n.Provenance))
		d.callback = true
		d.production = n.production
		// Terminal notices and callbacks that entered before a clear retain their
		// source home independently of the declarative registration.
		jm.rememberDetachedPendingLocked(cfg)
		d.allowAfterTerminalExpiry = true
		jm.mu.Unlock()
		d.frame = (&Session{}).formatJobNotificationReminder([]deliverableJobNotification{{notification: n}})
		// This call retains ownership through recursive notice source production,
		// including every failure return. Nested or older producers cannot release it.
		releases = append(releases, callbackBudgetRelease{cfg: cfg, deliveryID: d.deliveryID})
		_, _, ok, notice, err := jm.recordWatchSendForBatch(d)
		if ok {
			produced = append(produced, n)
		}
		if err != nil {
			errs = append(errs, err)
		} else if !ok {
			errs = append(errs, errDelegateTargetBusy)
		}
		if notice != nil {
			// The notice has a distinct durable key. Persist it without recursively
			// routing or kicking the batch whose final firing produced it.
			extra, extraErr := jm.produceCallbackNotifications([]jobNotification{*notice})
			produced = append(produced, extra...)
			if extraErr != nil {
				errs = append(errs, extraErr)
			}
		}
	}
	return produced, errors.Join(errs...)
}

func (jm *jobManager) releaseCallbackBudgetOwner(cfg *watchConfig, owner string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if cfg.callbackBudgetOwner == owner {
		cfg.callbackBudgetOwner = ""
	}
}

func (jm *jobManager) noteProducedWatchFiringLocked(cfg *watchConfig, state jobstore.WatchSendState) (crossedBudget bool) {
	if cfg.producedUpdateSeq[state.Key] >= state.UpdateSeq {
		return false
	}
	if cfg.producedUpdateSeq == nil {
		cfg.producedUpdateSeq = make(map[jobstore.WatchSendKey]uint64)
	}
	cfg.producedUpdateSeq[state.Key] = state.UpdateSeq
	if strings.HasPrefix(state.TriggerReason, "event: ") || strings.HasPrefix(state.TriggerReason, "output_match: ") {
		_, crossedBudget = noteConditionFireLocked(cfg)
	}
	if state.CallbackFrame && callbackFiringReason(state.TriggerReason) {
		jm.countWatchDeliveryLocked(cfg)
	}
	if cfg.oneShot {
		cfg.firedPendingEnd = true
	}
	return crossedBudget
}

func (jm *jobManager) finishProducedWatchFiring(cfg *watchConfig) {
	jm.mu.Lock()
	budget := cfg.budgetTripped
	jm.mu.Unlock()
	if budget {
		jm.autoClearWatchOverBudget(cfg)
	}
	// fireProgressTick owns one-shot teardown, once per firing or retry tick.
	// Source completion and custody dispatch must not retry its failed end.
}

func callbackFiringReason(reason string) bool {
	return strings.HasPrefix(reason, "event: ") || strings.HasPrefix(reason, "output_match: ") || reason == "progress_tick" || reason == "after" || reason == "repeat"
}

func preserveProducedWatchSend(state jobstore.WatchSendState, reason string) bool {
	return (state.CallbackFrame || state.ReceiverSessionID != "") && (reason == "watch cleared" || reason == "watch replaced")
}
