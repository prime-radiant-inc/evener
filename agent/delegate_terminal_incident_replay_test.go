package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
)

func TestDelegateTerminalIncidentReplay(t *testing.T) {
	dir := t.TempDir()
	writeTaskStoreFixture(t, dir, "s1", []task.Task{
		{ID: 1, Description: "finish the incident replay", Status: task.TaskInProgress},
	})
	s := &Session{id: "s1", stateDir: dir}

	summary := buildReportSummary(s, "done")
	if !summary.Tasks.Present || summary.Tasks.Remaining != 1 || len(summary.Tasks.Incomplete) != 1 || summary.Tasks.Incomplete[0].ID != 1 {
		t.Fatalf("incident summary = %+v, want one in-progress task", summary.Tasks)
	}
	notice := renderReportNotice(summary)
	if !strings.Contains(notice, "incident replay") {
		t.Fatalf("notice = %q, want the in-progress task", notice)
	}

	admitted, err := s.acceptCommunicateTerminal(context.Background(), schema.CommunicateInfo{Message: "incident replay report", EndTurn: true, EndReason: tool.CommunicateEndReasonDone}, "incident replay report", "", nil)
	if err != nil || !admitted {
		t.Fatalf("done acceptance with incomplete task: accepted=%v err=%v", admitted, err)
	}
	if got := routeNoToolCalls(EntryNotification, false, false, true); got != runNoToolCalls {
		t.Fatalf("parent disposition route = %v, want one steering round", got)
	}
}
