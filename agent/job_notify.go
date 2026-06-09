package agent

import (
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/agent/internal/jobstore"
)

type deliverableJobNotification struct {
	notification jobNotification
	terminalGen  string
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
	}
}

func formatJobNotificationBlock(n jobNotification) string {
	event := n.Status
	if event == "" {
		event = "running"
	}

	attrs := []string{
		fmt.Sprintf("job_id=%q", n.JobID),
		fmt.Sprintf("event=%q", event),
		fmt.Sprintf("job_type=%q", n.JobType),
		fmt.Sprintf("status=%q", n.Status),
	}
	if n.Reason != "" {
		attrs = append(attrs, fmt.Sprintf("reason=%q", n.Reason))
	}
	attrs = append(attrs, fmt.Sprintf("output_bytes=%q", strconv.FormatInt(n.OutputBytes, 10)))
	if n.ExitCode != nil {
		attrs = append(attrs, fmt.Sprintf("exit_code=%q", strconv.Itoa(*n.ExitCode)))
	}
	if n.TranscriptRef != "" {
		attrs = append(attrs, fmt.Sprintf("transcript_ref=%q", n.TranscriptRef))
	}

	return fmt.Sprintf(
		"<job-notification %s>\n"+
			"Job %s %s. Use job_read_output to inspect output.\n"+
			"</job-notification>",
		strings.Join(attrs, " "),
		n.JobID,
		event,
	)
}
