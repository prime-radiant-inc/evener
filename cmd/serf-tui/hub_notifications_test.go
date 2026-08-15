package main

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/msgrender"
	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

func TestRunStillRunning_Exhausted(t *testing.T) {
	if runStillRunning("exhausted") {
		t.Fatal("exhausted run reported as still running")
	}
}

func TestTUIStableDelegateNotificationHasNoDelegateJobIdentity(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUIStableDelegateNotification(t, &m, appwire.SerfDelegateInfo{
		DelegateID:         "dlg_resource",
		ProjectionRevision: 1,
		Status:             "running",
		Task:               "inspect stable resource",
		TranscriptRef:      "local:child",
	})

	run := requireTUIStableDelegateRun(t, &m, "dlg_resource")
	if run.JobID != "" {
		t.Fatalf("stable delegate JobID = %q, want no activation job identity", run.JobID)
	}
	if body := msgrender.SubagentRunBody(*run, 100); !strings.Contains(body, "Delegate") || !strings.Contains(body, "dlg_resource") || strings.Contains(body, "job ") {
		t.Fatalf("stable delegate body = %q, want Delegate controlled by dlg_ without job identity", body)
	}
}

func TestTUIStableDelegateReducerRejectsStaleRevision(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUIStableDelegateNotification(t, &m, appwire.SerfDelegateInfo{
		DelegateID:         "dlg_revision",
		ProjectionRevision: 2,
		Status:             "completed",
		Outcome:            "done",
		Terminal:           true,
		Task:               "finish once",
		LatestActivityAt:   "2026-08-15T10:00:00Z",
	})
	sendTUIStableDelegateNotification(t, &m, appwire.SerfDelegateInfo{
		DelegateID:         "dlg_revision",
		ProjectionRevision: 1,
		Status:             "running",
		Task:               "stale resurrection",
		LatestActivityAt:   "2026-08-15T10:01:00Z",
	})

	run := requireTUIStableDelegateRun(t, &m, "dlg_revision")
	if run.Status != "completed" || run.Task != "finish once" || run.LatestActivityAt != "2026-08-15T10:01:00Z" {
		t.Fatalf("stale revision did not fence state and max-merge activity: %+v", run)
	}
}

func TestTUIStableDelegateRendersTimingUsageQuietWorktreeAndWarnings(t *testing.T) {
	m := newTUIStableDelegateModel()
	durationMS := int64(2500)
	quietForMS := int64(750)
	sendTUIStableDelegateNotification(t, &m, appwire.SerfDelegateInfo{
		DelegateID:         "dlg_fidelity",
		ProjectionRevision: 3,
		Status:             "running",
		Task:               "verify projection fidelity",
		DurationMS:         &durationMS,
		QuietForMS:         &quietForMS,
		Usage: &appwire.SerfUsage{
			InputTokens:  120,
			OutputTokens: 45,
			TotalTokens:  165,
		},
		Worktree: &appwire.JobActivityWorktree{
			Path:    "/tmp/serf-worktree",
			Branch:  "feature/resource",
			HeadSHA: "1234567890abcdef",
			Ahead:   2,
			Dirty:   true,
		},
		Warnings: []string{"observer delivery delayed"},
	})

	body := msgrender.SubagentRunBody(*requireTUIStableDelegateRun(t, &m, "dlg_fidelity"), 140)
	for _, want := range []string{"2.5s", "120 in", "45 out", "quiet 750ms", "feature/resource", "dirty", "observer delivery delayed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stable delegate body = %q, want %q", body, want)
		}
	}
}

func TestTUIStableDelegateShellRemainsJobAddressed(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifySerfJobStarted, appwire.SerfJobParams{
		Ref: "local:root",
		Job: appwire.SerfJobInfo{
			JobID:            "job_shell",
			JobType:          "shell",
			Status:           "running",
			Background:       true,
			Command:          "go test ./agent",
			ParentDelegateID: "dlg_parent",
		},
	})

	run := requireTUIJobRun(t, &m, "job_shell")
	body := msgrender.SubagentRunBody(*run, 100)
	if !strings.Contains(body, "job job_shell") || !strings.Contains(body, "parent dlg_parent") {
		t.Fatalf("shell body = %q, want job identity and stable parent delegate ancestry", body)
	}
}

func TestTUIStableDelegateWatchAndObserverNoticesRemainVisible(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUIStableDelegateNotification(t, &m, appwire.SerfDelegateInfo{
		DelegateID:         "dlg_observer",
		ProjectionRevision: 1,
		Status:             "running",
		Task:               "observe parent",
		ParentWatchGranted: true,
	})

	watchNotice := `<delegate-notification delegate_id="dlg_observer">watch fired for parent shell</delegate-notification>`
	observerNotice := `<delegate-notification delegate_id="dlg_observer">observer callback remains active</delegate-notification>`
	for _, notice := range []string{watchNotice, observerNotice} {
		sendTUINotification(t, &m, appwire.NotifySerfSteeringInjected, appwire.SerfSteeringInjectedParams{Ref: "local:root", Text: notice})
	}

	seen := map[string]bool{}
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgSteering {
			seen[msg.Text] = true
		}
	}
	if !seen[watchNotice] || !seen[observerNotice] {
		t.Fatalf("steering messages = %+v, want stable watch and observer notices visible", m.session.messages)
	}
}

func newTUIStableDelegateModel() hubModel {
	m := newHubModel(nil, "http://hub.test")
	m.mode = hubModeSession
	m.detail.Ref = "local:root"
	return m
}

func sendTUIStableDelegateNotification(t testing.TB, m *hubModel, delegate appwire.SerfDelegateInfo) {
	t.Helper()
	sendTUINotification(t, m, appwire.NotifySerfDelegateUpdated, appwire.SerfDelegateParams{
		Ref:      "local:root",
		Delegate: delegate,
	})
}

func sendTUINotification(t testing.TB, m *hubModel, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s notification: %v", method, err)
	}
	m.applyHubNotification(appwire.Notification{Method: method, Params: raw})
}

func requireTUIStableDelegateRun(t testing.TB, m *hubModel, delegateID string) *transcript.SubagentRunInfo {
	t.Helper()
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil && msg.Tool.Subagent.DelegateID == delegateID {
			return msg.Tool.Subagent
		}
	}
	t.Fatalf("messages = %+v, want stable delegate %q", m.session.messages, delegateID)
	return nil
}

func requireTUIJobRun(t testing.TB, m *hubModel, jobID string) *transcript.SubagentRunInfo {
	t.Helper()
	for _, msg := range m.session.messages {
		if msg.Kind == transcript.MsgTool && msg.Tool != nil && msg.Tool.Subagent != nil && msg.Tool.Subagent.JobID == jobID {
			return msg.Tool.Subagent
		}
	}
	t.Fatalf("messages = %+v, want job %q", m.session.messages, jobID)
	return nil
}
