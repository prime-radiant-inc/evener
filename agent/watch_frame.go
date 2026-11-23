package agent

import (
	"strconv"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/lineend"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/appwire"
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
	return jm.buildWatchFrameWithObservation(cfg, jobID, trigger, deliveryID, ev, p, nil)
}

// buildWatchFrameWithObservation renders the frame, replacing the event block
// with the observation a hub watch decoded about the watched session. The
// observation lives only in memory; the frame keeps carrying it the way it
// carries any other event payload.
func (jm *jobManager) buildWatchFrameWithObservation(cfg *watchConfig, jobID string, trigger string, deliveryID string, ev events.SessionEvent, p *provenance.Causal, observation *hubObservationFrame) string {
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
	if observation != nil {
		writeHubObservationWatchEvent(&b, observation)
	} else {
		writeWatchFrameEvent(&b, ev)
	}

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

// hubObservationFrame is what a hub watch observed about the watched session,
// decoded from the observation update and carried on the in-memory delivery.
// It is not a session-event kind: nothing about it is appended to a session's
// event log, and the durable watch state keeps only the rendered frame.
type hubObservationFrame struct {
	// Event is the watch selector the observation matched: "status",
	// "question", or "approval".
	Event string
	// PreviousStatus is the status the watch last learned, filled in under the
	// watch lock at delivery time.
	PreviousStatus string
	// Status is the thread/status/changed params of a live status or question
	// observation, when the notification carried them.
	Status *appwire.ThreadStatusChangedParams
	// Closed marks a thread/closed observation; Method names the notification
	// that reported it.
	Closed bool
	Method string
	// Question and Approval are the pending items a snapshot replay carried.
	Question *appwire.PendingQuestion
	Approval *appwire.SandboxEscalationRequested
}

// statusType is the status this observation reports, empty when it reports
// none.
func (frame *hubObservationFrame) statusType() string {
	switch {
	case frame.Closed:
		return appwire.ThreadStatusClosed
	case frame.Status != nil:
		return frame.Status.Status.Type
	}
	return ""
}

// writeHubObservationWatchEvent renders what a hub watch observed about the
// watched session. Each shape names its event kind first and then only the
// fields the observation actually carried, so an older hub's notification or a
// snapshot that omitted a field renders nothing rather than a fabricated value.
func writeHubObservationWatchEvent(b *strings.Builder, data *hubObservationFrame) {
	switch data.Event {
	case "status":
		b.WriteString("event:\n")
		b.WriteString("  kind: status\n")
		switch {
		case data.Closed:
			writeWatchFrameOptionalField(b, "status", appwire.ThreadStatusClosed)
			writeWatchFrameOptionalField(b, "previous_status", data.PreviousStatus)
			writeWatchFrameOptionalField(b, "method", data.Method)
		case data.Status != nil:
			writeWatchFrameOptionalField(b, "status", data.Status.Status.Type)
			writeWatchFrameOptionalField(b, "previous_status", data.PreviousStatus)
			writeWatchFrameOptionalField(b, "active_flags", strings.Join(data.Status.Status.ActiveFlags, ", "))
			if data.Status.AskPending != nil {
				writeWatchFrameBoolField(b, "ask_pending", *data.Status.AskPending)
			}
			if data.Status.FailedToolCalls != nil {
				writeWatchFrameOptionalField(b, "failed_tool_calls", strconv.Itoa(*data.Status.FailedToolCalls))
			}
			writeWatchFrameOptionalField(b, "active_turn_id", data.Status.ActiveTurnID)
			writeWatchFrameOptionalField(b, "ended_turn_id", data.Status.EndedTurnID)
		default:
			writeWatchFrameOptionalField(b, "previous_status", data.PreviousStatus)
		}
	case "question":
		b.WriteString("event:\n")
		b.WriteString("  kind: question\n")
		if question := data.Question; question != nil {
			writeWatchFrameOptionalField(b, "question", limitWatchText(question.Question, watchEventExcerptMaxChars))
			writeWatchFrameOptionalField(b, "options", strings.Join(question.Options, ", "))
			if question.Count > 0 {
				writeWatchFrameOptionalField(b, "count", strconv.Itoa(question.Count))
			}
		}
		if data.Status != nil {
			writeWatchFrameOptionalField(b, "status", data.Status.Status.Type)
			writeWatchFrameOptionalField(b, "active_turn_id", data.Status.ActiveTurnID)
		}
	case "approval":
		b.WriteString("event:\n")
		b.WriteString("  kind: approval\n")
		if request := data.Approval; request != nil {
			writeWatchFrameOptionalField(b, "escalation_id", request.EscalationID)
			writeWatchFrameOptionalField(b, "tool", request.Tool)
			writeWatchFrameOptionalField(b, "kind_detail", request.Kind)
			writeWatchFrameOptionalField(b, "mode", request.Mode)
			writeWatchFrameOptionalField(b, "path", request.DeniedPath)
			writeWatchFrameOptionalField(b, "requested_path", request.RequestedPath)
			writeWatchFrameOptionalField(b, "command", request.Command)
		}
	}
}

func writeCommunicateWatchEvent(b *strings.Builder, data events.CommunicateData) {
	message := limitWatchText(data.Message, watchEventExcerptMaxChars)
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
