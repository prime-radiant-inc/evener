package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
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

	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if !admitted {
		t.Fatal("a done report with no live waker was refused")
	}
	if got := routeNoToolCalls(EntryNotification, false, false, true); got != runNoToolCalls {
		t.Fatalf("parent disposition route = %v, want one steering round", got)
	}
}
