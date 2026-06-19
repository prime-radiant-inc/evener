package agent

import (
	"strings"
	"testing"
)

func TestCoordinatorWorkflowUsesDelegateSend(t *testing.T) {
	coord := coordinatorWorkflowAgentForTest(t, "coordinator")
	if !hasString(coord.Tools, "delegate_send") {
		t.Fatalf("coordinator tools = %+v, want delegate_send", coord.Tools)
	}
	if hasString(coord.Tools, "job_send_message") {
		t.Fatalf("coordinator tools = %+v, must not include removed job_send_message", coord.Tools)
	}
	if !strings.Contains(coord.SystemPrompt, "delegate_send") {
		t.Fatalf("coordinator prompt should mention delegate_send:\n%s", coord.SystemPrompt)
	}
	if !strings.Contains(coord.SystemPrompt, "delegate_id") {
		t.Fatalf("coordinator prompt should mention delegate_id follow-up:\n%s", coord.SystemPrompt)
	}
	if strings.Contains(coord.SystemPrompt, "job_send_message") {
		t.Fatalf("coordinator prompt must not advertise removed job_send_message:\n%s", coord.SystemPrompt)
	}
}
