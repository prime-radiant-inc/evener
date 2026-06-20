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
	terminalExcerptBytes    = 400
	terminalExcerptMaxChars = 400
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

func jobNotificationFromRecord(rec *jobstore.JobRecord) jobNotification {
	return jobNotification{
		JobID:         rec.JobID,
		JobType:       string(rec.Type),
		Status:        string(rec.Status),
		Reason:        rec.Reason,
		TranscriptRef: rec.TranscriptRef,
		OutputBytes:   rec.OutputBytes,
		ExitCode:      rec.ExitCode,
		Provenance:    provenance.Clone(recordNotificationProvenance(rec)),
	}
}

// notificationExcerpt is a rendered terminal-result excerpt plus whether it
// contains the job's complete output. Completeness drives the body wording:
// a complete excerpt needs no job_read_output instruction.
type notificationExcerpt struct {
	text     string
	complete bool
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
		return notificationExcerpt{}
	}
	rendered := limitWatchText(excerpt, terminalExcerptMaxChars)
	if truncated {
		rendered += "\n[excerpt truncated]"
	}
	return notificationExcerpt{text: rendered, complete: !truncated}
}

// formatJobNotificationBlock renders one notification block. excerpt is the
// bounded result excerpt for a finished job (shell tail / delegate head),
// appended only to the terminal job_finished branch; it is ignored for watch
// frames and no-job watch events.
func formatJobNotificationBlock(n jobNotification, excerpt notificationExcerpt) string {
	if n.WatchSend != nil {
		attrs := []string{
			fmt.Sprintf("job_id=%q", n.JobID),
			`event="watch_send"`,
			fmt.Sprintf("delivery_id=%q", n.WatchSend.DeliveryID),
			fmt.Sprintf("trigger=%q", n.Reason),
		}
		return fmt.Sprintf("<job-notification %s>\n%s\n</job-notification>",
			strings.Join(attrs, " "), n.watchSendFrame)
	}

	event := n.Status
	if event == "" {
		event = "running"
	}

	attrs := []string{
		fmt.Sprintf("job_id=%q", n.JobID),
		fmt.Sprintf("event=%q", event),
		fmt.Sprintf("job_type=%q", n.JobType),
		fmt.Sprintf("status=%q", n.Status),
		fmt.Sprintf("reason=%q", n.Reason),
	}
	attrs = append(attrs, fmt.Sprintf("output_bytes=%q", strconv.FormatInt(n.OutputBytes, 10)))
	if n.ExitCode != nil {
		attrs = append(attrs, fmt.Sprintf("exit_code=%q", strconv.Itoa(*n.ExitCode)))
	}
	if n.TranscriptRef != "" {
		attrs = append(attrs, fmt.Sprintf("transcript_ref=%q", n.TranscriptRef))
	}

	if n.Status == jobNotificationEventWatch && n.JobID == "" {
		return fmt.Sprintf(
			"<job-notification %s>\n"+
				"Watch event triggered: %s.\n"+
				"</job-notification>",
			strings.Join(attrs, " "),
			n.Reason,
		)
	}

	// A complete excerpt makes a job_read_output call redundant. Otherwise,
	// present output inspection as an available follow-up instead of the next
	// required action.
	instruction := "Output is available through job_read_output if needed."
	if excerpt.text != "" && excerpt.complete {
		instruction = "Complete output below."
	}
	body := fmt.Sprintf("Job %s %s. %s", n.JobID, event, instruction)
	if excerpt.text != "" {
		body += "\nexcerpt:\n" + excerpt.text
	}
	return fmt.Sprintf(
		"<job-notification %s>\n%s\n</job-notification>",
		strings.Join(attrs, " "),
		body,
	)
}
