package agent

import (
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provenance"
)

type deliverableJobNotification struct {
	notification jobNotification
	terminalGen  string
	watchJM      *jobManager
	watchCfg     *watchConfig
	watchState   jobstore.WatchSendState
}

const jobNotificationEventWatch = "watch"

// terminalExcerptBytes bounds the byte budget read for a job_finished
// notification excerpt (shell tail / delegate head). terminalExcerptMaxChars
// rune-bounds the rendered excerpt with the shared watch-text discipline.
const (
	terminalExcerptBytes    = 8000
	terminalExcerptMaxChars = 8000
)

// recordNotificationProvenance is the causal provenance a record's notification
// carries: the explicit NotificationProvenance set at finish time, falling back
// to the record's own Provenance. Callers wrap the result with provenance.Clone.
func recordNotificationProvenance(rec *jobstore.JobRecord) *provenance.Causal {
	if rec.NotificationProvenance != nil {
		return rec.NotificationProvenance
	}
	return rec.Provenance
}

func jobRecordDisplayLabel(rec *jobstore.JobRecord) string {
	description := rec.Description
	if description == "" {
		description = rec.Command
	}
	if description == "" {
		description = rec.Task
	}
	return description
}

func jobNotificationFromRecord(rec *jobstore.JobRecord) jobNotification {
	return jobNotification{
		JobID:            rec.JobID,
		JobType:          string(rec.Type),
		Description:      jobRecordDisplayLabel(rec),
		Status:           string(rec.Status),
		Reason:           rec.Reason,
		ExhaustionBudget: rec.ExhaustionBudget,
		ExhaustionLimit:  rec.ExhaustionLimit,
		Resumable:        rec.Resumable,
		TranscriptRef:    jobTranscriptRef(rec),
		OutputBytes:      rec.OutputBytes,
		ExitCode:         rec.ExitCode,
		Provenance:       provenance.Clone(recordNotificationProvenance(rec)),
	}
}

// notificationExcerpt is a rendered terminal-result excerpt plus whether it
// contains the job's complete output. Completeness drives the body wording:
// a complete excerpt needs no transcript-read instruction. worktree carries
// the isolation lane's path/branch/ahead/dirty state for a terminal isolated
// delegate job (native worktree tools spec §9 lifecycle step 3), so the
// background completion-notification path surfaces the same lane report the
// inline-wait tool result carries; nil for every non-isolated job.
type notificationExcerpt struct {
	text     string
	complete bool
	worktree *delegateWorktreeReport
}

// terminalNotificationExcerpt resolves the bounded result excerpt for a finished
// job notification: the shell tail or the delegate report's head, re-read from
// the job's output at render time. It returns the zero value for watch frames,
// no-job watch events, and any job whose output is empty or cannot be read (a
// failed read degrades to no excerpt rather than failing the notification
// render).
func (s *Session) terminalNotificationExcerpt(n jobNotification) notificationExcerpt {
	// Watch frames and watch events (with or without a job id) are not terminal
	// job_finished notifications and carry no result excerpt.
	if n.WatchSend != nil || n.JobID == "" || n.Status == jobNotificationEventWatch {
		return notificationExcerpt{}
	}
	jm, rec, err := s.nestedOrLocalJobManager(n.JobID)
	if err != nil || jm == nil {
		return notificationExcerpt{}
	}
	// Only an actually-terminal job has a result to excerpt.
	if rec == nil || !rec.Status.IsTerminal() {
		return notificationExcerpt{}
	}
	jobType := string(rec.Type)

	// An isolated delegate's terminal notification carries its lane report
	// (spec §9 step 3) whether or not the job produced any output — so it is
	// computed before the empty-excerpt early return below. Empty for every
	// non-isolated job (isolatedDelegateWorktreeReport returns nil).
	var worktreeReport *delegateWorktreeReport
	if jobType == string(jobstore.JobDelegate) {
		worktreeReport = s.isolatedDelegateWorktreeReport(rec.DelegateRestore)
	}

	var (
		excerpt   string
		truncated bool
	)
	if jobType == string(jobstore.JobDelegate) {
		excerpt, _, truncated, err = jm.readOutputHead(n.JobID, terminalExcerptBytes)
	} else {
		excerpt, _, truncated, err = jm.readOutput(n.JobID, terminalExcerptBytes)
	}
	if err != nil || excerpt == "" {
		return notificationExcerpt{worktree: worktreeReport}
	}
	rendered := limitWatchText(strings.ToValidUTF8(excerpt, "\uFFFD"), terminalExcerptMaxChars)
	if truncated {
		rendered += "\n[excerpt truncated]"
	}
	return notificationExcerpt{text: rendered, complete: !truncated, worktree: worktreeReport}
}

func notificationTranscriptRef(n jobNotification) string {
	if n.TranscriptRef != "" {
		return n.TranscriptRef
	}
	if n.JobID != "" {
		return "job:" + n.JobID
	}
	return ""
}

// escapeNotificationText makes text safe to interpolate into a
// <job-notification> wrapper, in two steps. First, invalid UTF-8 (job output
// and agent-composed text carry no encoding guarantee) is replaced with
// U+FFFD via strings.ToValidUTF8, so the returned block is always valid
// UTF-8. Before 522f25ab9, attribute values were rendered with fmt's %q verb,
// which escapes invalid UTF-8 as a side effect; that commit switched
// attributes to this function without also carrying over the ToValidUTF8 call
// the body-text call sites already had, so a raw invalid byte in a value like
// a job's Reason reached the rendered block unescaped (the
// FuzzShellNotificationRenderProgram regression corpus catches it). Second, &
// < > " are HTML-entity-escaped so the content cannot prematurely close an
// attribute value, close the opening tag early, terminate the block via a
// literal </job-notification>, or forge a second block. & is escaped first so
// the other entities are not themselves escaped a second time.
// NotificationCard's decodeEntities (cmd/serf-hub/frontend) is the paired
// decoder.
func escapeNotificationText(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// escapeNotificationBody makes text safe to interpolate into the body of a
// <job-notification> wrapper, escaping only the characters that could
// prematurely terminate the block (a literal </job-notification> substring).
// Invalid UTF-8 is normalized to U+FFFD via strings.ToValidUTF8. Unlike
// escapeNotificationText (used for attribute values), this function does NOT
// escape & > or " because body text is not inside a quoted attribute wrapper.
// The only structural hazard is a literal < which could forge a new tag or
// prematurely close </job-notification>.
func escapeNotificationBody(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return s
}

// notificationAttr renders one key="value" wrapper attribute with value
// escaped by escapeNotificationText, so a delimiter inside value cannot move
// the opening tag's own boundary (the web parser's tag match is naive about
// quoting and stops at the first literal '>').
func notificationAttr(key, value string) string {
	return key + `="` + escapeNotificationText(value) + `"`
}

// formatJobNotificationBlock renders one notification block. excerpt is the
// bounded result excerpt for a finished job (shell tail / delegate head),
// appended only to the terminal job_finished branch; it is ignored for watch
// frames and no-job watch events.
//
// canReadTranscript is the receiving session's answer to "do I serve
// read_transcript": false drops the output pointer rather than pointing at a
// tool the receiver cannot call.
func formatJobNotificationBlock(n jobNotification, excerpt notificationExcerpt, canReadTranscript bool) string {
	if n.WatchSend != nil {
		attrs := []string{
			notificationAttr("job_id", n.JobID),
			`event="watch_send"`,
			notificationAttr("delivery_id", n.WatchSend.DeliveryID),
			notificationAttr("trigger", n.Reason),
		}
		return fmt.Sprintf("<job-notification %s>\n%s\n</job-notification>",
			strings.Join(attrs, " "), escapeNotificationBody(n.watchSendFrame))
	}

	event := n.Status
	if event == "" {
		event = "running"
	}

	attrs := []string{
		notificationAttr("job_id", n.JobID),
		notificationAttr("event", event),
		notificationAttr("job_type", n.JobType),
		notificationAttr("description", n.Description),
		notificationAttr("status", n.Status),
		notificationAttr("reason", n.Reason),
	}
	attrs = append(attrs, notificationAttr("output_bytes", strconv.FormatInt(n.OutputBytes, 10)))
	if n.Status == string(jobstore.StatusExhausted) {
		attrs = append(attrs,
			notificationAttr("budget", n.ExhaustionBudget),
			notificationAttr("limit", strconv.Itoa(n.ExhaustionLimit)),
			notificationAttr("resumable", strconv.FormatBool(n.Resumable != nil && *n.Resumable)),
		)
	}
	if n.ExitCode != nil {
		attrs = append(attrs, notificationAttr("exit_code", strconv.Itoa(*n.ExitCode)))
	}
	if n.TranscriptRef != "" {
		attrs = append(attrs, notificationAttr("transcript_ref", n.TranscriptRef))
	}
	// An isolated delegate's terminal notification carries its lane report so
	// the parent can merge the lane between jobs even in the default
	// fire-and-forget mode, where the result never rides an inline tool
	// response (native worktree tools spec §9 lifecycle step 3).
	if wt := excerpt.worktree; wt != nil {
		attrs = append(attrs,
			notificationAttr("worktree_path", wt.Path),
			notificationAttr("worktree_branch", wt.Branch),
			notificationAttr("worktree_head_sha", wt.HeadSHA),
			notificationAttr("worktree_ahead", strconv.Itoa(wt.Ahead)),
			notificationAttr("worktree_dirty", strconv.FormatBool(wt.Dirty)),
		)
	}

	if n.Status == jobNotificationEventWatch && n.JobID == "" {
		return fmt.Sprintf(
			"<job-notification %s>\n"+
				"Watch event triggered: %s.\n"+
				"</job-notification>",
			strings.Join(attrs, " "),
			escapeNotificationBody(n.Reason),
		)
	}

	// A complete excerpt makes a transcript read redundant. Otherwise,
	// present output inspection as an available follow-up instead of the next
	// required action.
	instruction := ""
	if canReadTranscript {
		instruction = "Output is available through read_transcript if needed."
		if ref := notificationTranscriptRef(n); ref != "" {
			instruction = fmt.Sprintf("Output is available through read_transcript(transcript_ref=%q) if needed.", ref)
		}
	}
	if excerpt.text != "" && excerpt.complete {
		instruction = "Complete output below."
	}
	body := strings.TrimSpace(fmt.Sprintf("Job %s %s. %s", n.JobID, event, instruction))
	if excerpt.text != "" {
		body += "\nexcerpt:\n" + escapeNotificationBody(excerpt.text)
	}
	// The spec §P2 completion nudge rides the same lane report as the inline
	// tool result, gated identically (has-op AND owns-delegate) — the report's
	// DisposalHint is non-empty only when both gates hold.
	if wt := excerpt.worktree; wt != nil && wt.DisposalHint != "" {
		body += "\n" + escapeNotificationBody(wt.DisposalHint)
	}
	return fmt.Sprintf(
		"<job-notification %s>\n%s\n</job-notification>",
		strings.Join(attrs, " "),
		body,
	)
}
