package agent

import (
	"strings"
	"testing"
)

func TestFormatNotificationReminder_WithoutTranscriptToolsOmitsReadTool(t *testing.T) {
	reminder := formatNotificationReminder([]subagentNotification{{
		AgentID:       "01CHILD",
		Status:        "completed",
		TurnsUsed:     4,
		TranscriptRef: "local:01CHILD",
	}}, nil, false)

	for _, want := range []string{"<subagent-notification", "01CHILD", "no job_id", "No archived transcript inspection path is available", "local:01CHILD"} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("notification reminder missing %q:\n%s", want, reminder)
		}
	}
	for _, forbidden := range []string{"read_session_transcript", "wait(", "subagent_output", "job_read_output"} {
		if strings.Contains(reminder, forbidden) {
			t.Fatalf("notification reminder contained unavailable guidance %q:\n%s", forbidden, reminder)
		}
	}
}

func TestFormatNotificationReminder_WithTranscriptToolsNamesReadTool(t *testing.T) {
	reminder := formatNotificationReminder([]subagentNotification{{
		AgentID:       "01CHILD",
		Status:        "completed",
		TurnsUsed:     4,
		TranscriptRef: "local:01CHILD",
	}}, nil, true)

	for _, want := range []string{"<subagent-notification", "01CHILD", "no job_id", "read_session_transcript", "local:01CHILD"} {
		if !strings.Contains(reminder, want) {
			t.Fatalf("notification reminder missing %q:\n%s", want, reminder)
		}
	}
	for _, forbidden := range []string{"wait(", "subagent_output", "job_read_output"} {
		if strings.Contains(reminder, forbidden) {
			t.Fatalf("notification reminder contained deleted tool guidance %q:\n%s", forbidden, reminder)
		}
	}
}
