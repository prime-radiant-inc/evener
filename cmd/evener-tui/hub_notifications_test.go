package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/msgrender"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

func TestRunStillRunning_Exhausted(t *testing.T) {
	if runStillRunning("exhausted") {
		t.Fatal("exhausted run reported as still running")
	}
}

func TestTUIStableDelegateNotificationHasNoDelegateJobIdentity(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
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
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_revision",
		ProjectionRevision: 2,
		Lifecycle:          "idle",
		Status:             "idle",
		Outcome:            "completed",
		Terminal:           true,
		Task:               "finish once",
		LatestActivityAt:   "2026-08-15T10:00:00Z",
	})
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_revision",
		ProjectionRevision: 1,
		Status:             "running",
		Task:               "stale resurrection",
		LatestActivityAt:   "2026-08-15T10:01:00Z",
	})

	run := requireTUIStableDelegateRun(t, &m, "dlg_revision")
	if run.Status != "idle" || run.Outcome != "completed" || run.Task != "finish once" || run.LatestActivityAt != "2026-08-15T10:01:00Z" {
		t.Fatalf("stale revision did not fence state and max-merge activity: %+v", run)
	}
}

func TestTUIStableDelegateTerminalLifecycleDoesNotSubscribeChild(t *testing.T) {
	client, cleanup := newTestHubClient(t, nil)
	defer cleanup()

	m := newTUIStableDelegateModel()
	m.client = client
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_terminal",
		Lifecycle:          "idle",
		Status:             "idle",
		Outcome:            "completed",
		Terminal:           true,
		ProjectionRevision: 1,
		TranscriptRef:      "local:child-terminal",
	})

	if cmd := m.subscribeNewChildren(); cmd != nil {
		t.Fatal("terminal stable delegate scheduled a child subscription")
	}
	if m.watchedChildRefs["local:child-terminal"] {
		t.Fatal("terminal stable delegate was marked as watched")
	}
}

func TestTUIStableDelegateRendersTimingUsageQuietWorktreeAndWarnings(t *testing.T) {
	m := newTUIStableDelegateModel()
	durationMS := int64(2500)
	quietForMS := int64(750)
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_fidelity",
		ProjectionRevision: 3,
		Status:             "running",
		Task:               "verify projection fidelity",
		DurationMS:         &durationMS,
		QuietForMS:         &quietForMS,
		Usage: &appwire.EvenerUsage{
			InputTokens:  120,
			OutputTokens: 45,
			TotalTokens:  165,
		},
		Worktree: &appwire.JobActivityWorktree{
			Path:    "/tmp/evener-worktree",
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
	sendTUINotification(t, &m, appwire.NotifyEvenerJobStarted, appwire.EvenerJobParams{
		Ref: "local:root",
		Job: appwire.EvenerJobInfo{
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

// TestTUISteeringInjectedTiesEveryJobNotificationBlock is the end-to-end
// repro for issue #49: the daemon joins every job's <job-notification> block
// from one poll tick into a single steering event with "\n"
// (agent/session_lifecycle.go), so a steering payload naming two jobs must
// tie both rail rows to their own rich headline, not just the first (whose
// body would otherwise swallow the rest under a greedy match).
func TestTUISteeringInjectedTiesEveryJobNotificationBlock(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUINotification(t, &m, appwire.NotifyEvenerJobStarted, appwire.EvenerJobParams{
		Ref: "local:root",
		Job: appwire.EvenerJobInfo{JobID: "job_A", JobType: "shell", Status: "running", Background: true},
	})
	sendTUINotification(t, &m, appwire.NotifyEvenerJobStarted, appwire.EvenerJobParams{
		Ref: "local:root",
		Job: appwire.EvenerJobInfo{JobID: "job_B", JobType: "shell", Status: "running", Background: true},
	})

	blockA := `<job-notification job_id="job_A" job_type="delegate" status="completed" exit_code="0">` +
		`excerpt: {"data":{"test_summary":"all green","commit_hashes":["abcdef1234567890"],"concerns":["c1"]}}` +
		`</job-notification>`
	blockB := `<job-notification job_id="job_B" job_type="delegate" status="completed" exit_code="0">` +
		`excerpt: {"data":{"test_summary":"3 passed","commit_hashes":["1234567890abcdef"]}}` +
		`</job-notification>`
	sendTUINotification(t, &m, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{
		Ref:  "local:root",
		Text: blockA + "\n" + blockB,
	})

	runA := requireTUIJobRun(t, &m, "job_A")
	if want := "all green · abcdef12 · 1 concern"; runA.Headline != want {
		t.Fatalf("job_A headline = %q, want %q (must not degrade to the bare status)", runA.Headline, want)
	}
	runB := requireTUIJobRun(t, &m, "job_B")
	if want := "3 passed · 12345678"; runB.Headline != want {
		t.Fatalf("job_B headline = %q, want %q (must still get a rail tie)", runB.Headline, want)
	}
}

func TestTUIStableDelegateWatchAndObserverNoticesRemainVisible(t *testing.T) {
	m := newTUIStableDelegateModel()
	sendTUIStableDelegateNotification(t, &m, appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_observer",
		ProjectionRevision: 1,
		Status:             "running",
		Task:               "observe parent",
		ParentWatchGranted: true,
	})

	watchNotice := `<delegate-notification delegate_id="dlg_observer">watch fired for parent shell</delegate-notification>`
	observerNotice := `<delegate-notification delegate_id="dlg_observer">observer callback remains active</delegate-notification>`
	for _, notice := range []string{watchNotice, observerNotice} {
		sendTUINotification(t, &m, appwire.NotifyEvenerSteeringInjected, appwire.EvenerSteeringInjectedParams{Ref: "local:root", Text: notice})
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

func sendTUIStableDelegateNotification(t testing.TB, m *hubModel, delegate appwire.EvenerDelegateInfo) {
	t.Helper()
	sendTUINotification(t, m, appwire.NotifyEvenerDelegateUpdated, appwire.EvenerDelegateParams{
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
