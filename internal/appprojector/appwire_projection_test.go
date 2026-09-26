package appprojector

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func TestProject_ModelChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventModelChanged,
		Data: events.ModelChangedData{
			OldProvider:           "openai",
			OldModel:              "gpt-5.4",
			NewProvider:           "anthropic",
			NewModel:              "claude-opus-4-6",
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
			MarkerText:            "Switched model: openai/gpt-5.4 → anthropic/claude-opus-4-6",
		},
	})
	// The switch marker is the MODEL_SWITCH entry SetModel records: history
	// shows it, so the projector announces the change alone.
	if len(out) != 1 {
		t.Fatalf("want thread/model/changed alone, got %+v", out)
	}
	if out[0].Method != appwire.NotifyThreadModelChanged {
		t.Fatalf("out[0].Method = %q, want thread/model/changed", out[0].Method)
	}
	params, ok := out[0].Params.(appwire.ThreadModelChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadModelChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("params missing threadId/ref: %+v", params)
	}
	if params.ModelProvider != "anthropic" || params.Model != "claude-opus-4-6" {
		t.Fatalf("params modelProvider/model = %s/%s, want anthropic/claude-opus-4-6", params.ModelProvider, params.Model)
	}
	if !params.SupportsReasoning || len(params.ReasoningEffortLevels) != 2 {
		t.Fatalf("params = %+v, want SupportsReasoning=true and 2 effort levels", params)
	}
}

func TestProject_ReasoningEffortChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventReasoningEffortChanged,
		Data: events.ReasoningEffortChangedData{ReasoningEffort: "high"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadReasoningEffortChanged {
		t.Fatalf("want one thread/reasoning-effort/changed notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadReasoningEffortChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadReasoningEffortChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.ReasoningEffort != "high" {
		t.Fatalf("params = %+v", params)
	}
}

func TestProject_VisionModelChanged(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventVisionModelChanged,
		Data: events.VisionModelChangedData{OldVisionModel: "", NewVisionModel: "off"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadVisionModelChanged {
		t.Fatalf("want one thread/vision-model/changed notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadVisionModelChangedParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.ThreadVisionModelChangedParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.VisionModel != "off" {
		t.Fatalf("params = %+v", params)
	}
}

// TestProject_ModelThenEffortNotificationOrdering pins the client-facing
// contract for a switch that also clamps effort: the model-changed
// notification (which carries the new reasoning-effort ladder) is delivered
// before the reasoning-effort-changed notification, so a client re-derives the
// ladder before applying the new effort value. The switch marker systemMessage
// rides between them, immediately after model-changed.
func TestProject_ModelThenEffortNotificationOrdering(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	var out []AppNotification
	out = append(out, p.Project(events.SessionEvent{
		Kind: events.EventModelChanged,
		Data: events.ModelChangedData{
			OldProvider:           "anthropic",
			OldModel:              "claude-opus-4-6",
			NewProvider:           "openai",
			NewModel:              "gpt-5.5",
			ReasoningEffortLevels: []string{"low", "high"},
			SupportsReasoning:     true,
			MarkerText:            "Switched model: anthropic/claude-opus-4-6 → openai/gpt-5.5",
		},
	})...)
	out = append(out, p.Project(events.SessionEvent{
		Kind: events.EventReasoningEffortChanged,
		Data: events.ReasoningEffortChangedData{ReasoningEffort: "high"},
	})...)

	if got := len(out); got != 2 {
		t.Fatalf("want model-changed then effort-changed, got %d: %+v", got, out)
	}
	if out[0].Method != appwire.NotifyThreadModelChanged {
		t.Fatalf("out[0].Method = %q, want thread/model/changed first", out[0].Method)
	}
	if out[len(out)-1].Method != appwire.NotifyThreadReasoningEffortChanged {
		t.Fatalf("out[last].Method = %q, want thread/reasoning-effort/changed last", out[len(out)-1].Method)
	}
}

func TestProject_TaskUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventTaskUpdated,
		Data: events.TaskUpdatedData{
			Total: 3, Done: 1, Current: &events.TaskSummaryData{ID: 2, Description: "live current task"},
			TaskStoreOwnerSessionID: "owner-session",
			TaskPublicationEpoch:    7,
			TaskPublicationRevision: 42,
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerTaskUpdated {
		t.Fatalf("want one evener/task/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.TaskUpdatedParams)
	if !ok || params.Total != 3 || params.Done != 1 || params.Current == nil || params.Current.ID != 2 || params.Current.Description != "live current task" {
		t.Fatalf("params = %+v, want Total=3 Done=1 Current={ID:2 Description:live current task}", out[0].Params)
	}
	if out[0].TaskStoreOwnerSessionID != "owner-session" {
		t.Fatalf("notification owner = %q, want owner-session", out[0].TaskStoreOwnerSessionID)
	}
	if out[0].TaskPublicationEpoch != 7 || out[0].TaskPublicationRevision != 42 ||
		p.TaskPublicationEpoch() != 7 || p.TaskPublicationRevision() != 42 {
		t.Fatalf("notification/projector publication = %d:%d/%d:%d, want 7:42", out[0].TaskPublicationEpoch, out[0].TaskPublicationRevision, p.TaskPublicationEpoch(), p.TaskPublicationRevision())
	}
	wired, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wired), "owner") || strings.Contains(string(wired), "revision") || strings.Contains(string(wired), "epoch") || strings.Contains(string(wired), "taskStoreOwnerSessionId") {
		t.Fatalf("public task params leaked internal routing metadata: %s", wired)
	}
}

func TestProject_TaskUpdatedPreservesFullyCancelledState(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventTaskUpdated,
		Data: events.TaskUpdatedData{Total: 3, Cancelled: 3, Remaining: 0},
	})
	if len(out) != 1 {
		t.Fatalf("notifications = %+v, want one task update", out)
	}
	params, ok := out[0].Params.(appwire.TaskUpdatedParams)
	if !ok || params.Total != 3 || params.Done != 0 || params.Cancelled != 3 || params.Remaining != 0 || params.Current != nil {
		t.Fatalf("params = %+v, want fully cancelled task state", out[0].Params)
	}
}

func TestProject_SessionStartCarriesCurrentWorkSeed(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{Kind: events.EventSessionStart, Data: events.SessionStartData{
		TaskStoreOwnerSessionID: "owner-session",
		TaskPublicationEpoch:    7,
		TaskPublicationRevision: 41,
		CurrentWork: &events.CurrentWorkSeedData{
			Tasks: &events.TaskStateData{Total: 3, Done: 1, Cancelled: 1, Remaining: 1, Current: &events.TaskSummaryData{ID: 2, Description: "seeded task"}},
			Goal:  &events.GoalStateData{Objective: "seeded objective", Status: "active", Iterations: 2},
		},
	}})

	thread := notificationThread(t, out, appwire.NotifyThreadStarted)
	if thread.Evener.Tasks == nil || thread.Evener.Tasks.Total != 3 || thread.Evener.Tasks.Done != 1 || thread.Evener.Tasks.Cancelled != 1 || thread.Evener.Tasks.Remaining != 1 ||
		thread.Evener.Tasks.Current == nil || thread.Evener.Tasks.Current.Description != "seeded task" {
		t.Fatalf("started tasks = %+v, want complete current-work seed", thread.Evener.Tasks)
	}
	if thread.Evener.Goal == nil || thread.Evener.Goal.Objective != "seeded objective" || thread.Evener.Goal.Status != "active" || thread.Evener.Goal.Iterations != 2 {
		t.Fatalf("started goal = %+v, want complete current-work seed", thread.Evener.Goal)
	}
	if out[0].TaskStoreOwnerSessionID != "owner-session" {
		t.Fatalf("started notification owner = %q, want owner-session", out[0].TaskStoreOwnerSessionID)
	}
	if out[0].TaskPublicationEpoch != 7 || out[0].TaskPublicationRevision != 41 ||
		p.TaskPublicationEpoch() != 7 || p.TaskPublicationRevision() != 41 {
		t.Fatalf("started notification/projector publication = %d:%d/%d:%d, want 7:41", out[0].TaskPublicationEpoch, out[0].TaskPublicationRevision, p.TaskPublicationEpoch(), p.TaskPublicationRevision())
	}
	wired, err := json.Marshal(out[0].Params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wired), "taskPublication") || strings.Contains(string(wired), "task_publication") || strings.Contains(string(wired), "epoch") ||
		strings.Contains(string(wired), `"revision":41`) || strings.Contains(string(wired), "owner") {
		t.Fatalf("public thread-start params leaked internal routing metadata: %s", wired)
	}
}

func TestProject_SessionStartExplicitNoGoalSeed(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{Kind: events.EventSessionStart, Data: events.SessionStartData{
		CurrentWork: &events.CurrentWorkSeedData{Tasks: &events.TaskStateData{}},
	}})
	thread := notificationThread(t, out, appwire.NotifyThreadStarted)
	if thread.Evener.Tasks == nil {
		t.Fatal("started tasks = nil, want authoritative present zero")
	}
	if thread.Evener.Goal != nil {
		t.Fatalf("started goal = %+v, want explicit nil", thread.Evener.Goal)
	}
}

func TestProject_SessionStartWithoutCurrentWorkRemainsCompatible(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{Kind: events.EventSessionStart, Data: events.SessionStartData{}})
	thread := notificationThread(t, out, appwire.NotifyThreadStarted)
	if thread.Evener.Tasks != nil || thread.Evener.Goal != nil {
		t.Fatalf("legacy started current work = tasks:%+v goal:%+v, want both unknown", thread.Evener.Tasks, thread.Evener.Goal)
	}
}

func TestProject_GoalUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalUpdated,
		Data: events.GoalUpdatedData{Goal: &events.GoalStateData{
			Objective: "ship focus sentence", Status: "active", Iterations: 1,
		}},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerGoalUpdated {
		t.Fatalf("want one evener/goal/updated notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.GoalUpdatedParams)
	if !ok || params.ThreadID != "th1" || params.Ref != "local:th1" || params.Goal == nil ||
		params.Goal.Objective != "ship focus sentence" || params.Goal.Status != "active" || params.Goal.Iterations != 1 {
		t.Fatalf("params = %+v, want active goal state", out[0].Params)
	}
}

func TestProject_GoalUpdatedClear(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventGoalUpdated,
		Data: events.GoalUpdatedData{Goal: nil},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerGoalUpdated {
		t.Fatalf("want one evener/goal/updated clear notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.GoalUpdatedParams)
	if !ok || params.ThreadID != "th1" || params.Ref != "local:th1" || params.Goal != nil {
		t.Fatalf("params = %+v, want explicit nil goal", out[0].Params)
	}
}

// A job lifecycle event projects ONE notification, not two. evener/job/started
// already carries the (jobId, status) pair a jobs-panel client refetches on,
// so a second lightweight notification at the same instant would duplicate a
// stream every client already receives (kata j7y6).
func TestProject_JobStartedIsTheOnlyStartNotification(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobStarted,
		Data: events.JobStartedData{JobID: "job_1", JobType: "shell", Status: "running"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerJobStarted {
		t.Fatalf("want exactly one evener/job/started notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.EvenerJobParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.EvenerJobParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.Job.JobID != "job_1" || params.Job.Status != "running" {
		t.Fatalf("params = %+v", params)
	}
}

func TestProject_JobFinishedIsTheOnlyFinishNotification(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: events.JobFinishedData{JobID: "job_1", JobType: "shell", Status: "completed", Intent: "reproduce the failure"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerJobFinished {
		t.Fatalf("want exactly one evener/job/finished notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.EvenerJobParams)
	if !ok {
		t.Fatalf("params type = %T, want appwire.EvenerJobParams", out[0].Params)
	}
	if params.ThreadID != "th1" || params.Ref != "local:th1" || params.Job.JobID != "job_1" || params.Job.Status != "completed" {
		t.Fatalf("params = %+v", params)
	}
	if params.Job.Intent != "reproduce the failure" {
		t.Fatalf("params.Job.Intent = %q, want the finished push to forward the intent", params.Job.Intent)
	}
}

func TestProject_JobStartedAlsoEmitsJobsTreeUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobStarted,
		Data: events.JobStartedData{
			JobID:         "job_1",
			JobType:       "shell",
			Status:        "running",
			Intent:        "reproduce the failure",
			RootSessionID: "root",
			TreeRevision:  9,
		},
	})
	if len(out) != 2 {
		t.Fatalf("want job started + jobs tree updated notifications, got %+v", out)
	}
	if !hasAppNotification(out, appwire.NotifyEvenerJobStarted) {
		t.Fatalf("missing %q in %+v", appwire.NotifyEvenerJobStarted, out)
	}
	started := notificationParams[appwire.EvenerJobParams](t, out, appwire.NotifyEvenerJobStarted)
	if started.Job.Intent != "reproduce the failure" {
		t.Fatalf("started push intent = %q, want the payload's intent forwarded", started.Job.Intent)
	}
	params := notificationParams[appwire.JobsTreeUpdatedParams](t, out, appwire.NotifyEvenerJobsTreeUpdated)
	if params.ThreadID != "root" || params.Ref != "local:root" || params.Revision != 9 {
		t.Fatalf("params=%+v", params)
	}
}

func TestProject_JobFinishedAlsoEmitsJobsTreeUpdated(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventJobFinished,
		Data: events.JobFinishedData{
			JobID:         "job_1",
			JobType:       "shell",
			Status:        "completed",
			Reason:        "exit_zero",
			RootSessionID: "root",
			TreeRevision:  9,
		},
	})
	if len(out) != 2 {
		t.Fatalf("want job finished + jobs tree updated notifications, got %+v", out)
	}
	if !hasAppNotification(out, appwire.NotifyEvenerJobFinished) {
		t.Fatalf("missing %q in %+v", appwire.NotifyEvenerJobFinished, out)
	}
	params := notificationParams[appwire.JobsTreeUpdatedParams](t, out, appwire.NotifyEvenerJobsTreeUpdated)
	if params.ThreadID != "root" || params.Ref != "local:root" || params.Revision != 9 {
		t.Fatalf("params=%+v", params)
	}
}

func TestProject_JobsTreeUpdatedOmittedForLegacyJobFixtures(t *testing.T) {
	tests := []events.SessionEvent{
		{Kind: events.EventJobStarted, Data: events.JobStartedData{JobID: "job_1", JobType: "shell", Status: "running"}},
		{Kind: events.EventJobFinished, Data: events.JobFinishedData{JobID: "job_1", JobType: "shell", Status: "completed", Reason: "exit_zero"}},
	}
	for _, event := range tests {
		p := NewAppEventProjector("th1", "local:th1")
		out := p.Project(event)
		if hasAppNotification(out, appwire.NotifyEvenerJobsTreeUpdated) {
			t.Fatalf("legacy fixture unexpectedly emitted jobs tree update: %+v", out)
		}
	}
}

func TestProject_SandboxEscalationRequested(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventSandboxEscalationRequested,
		Data: events.SandboxEscalationRequestedData{
			EscalationID: "esc_1",
			Mode:         "read-only",
			Tool:         "write_file",
			Kind:         "file_tool",
			DeniedPath:   "/etc/hosts", // full path for informed consent (set at the session)
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerSandboxEscalationRequested {
		t.Fatalf("want one evener/sandbox/escalation/requested notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.SandboxEscalationRequested)
	if !ok {
		t.Fatalf("params type = %T, want appwire.SandboxEscalationRequested", out[0].Params)
	}
	if params.EscalationID != "esc_1" || params.Kind != "file_tool" || params.DeniedPath != "/etc/hosts" {
		t.Fatalf("params = %+v", params)
	}
	// The notification must carry its session ref/threadId so a client can route it
	// by session (answer the right one, enqueue a non-viewed one).
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("escalation notification must carry threadId/ref, got %+v", params)
	}
}

// TestProject_SandboxEscalationResolved (wire-honesty spec Part B) mirrors
// TestProject_SandboxEscalationRequested above for the pair notification: the
// projector maps EventSandboxEscalationResolved to
// evener/sandbox/escalation/resolved, carrying only threadId/ref/escalationId
// (no reason or approved — a review decision the spec's doc comments explain).
func TestProject_SandboxEscalationResolved(t *testing.T) {
	p := NewAppEventProjector("th1", "local:th1")
	out := p.Project(events.SessionEvent{
		Kind: events.EventSandboxEscalationResolved,
		Data: events.SandboxEscalationResolvedData{EscalationID: "esc_1"},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyEvenerSandboxEscalationResolved {
		t.Fatalf("want one evener/sandbox/escalation/resolved notification, got %+v", out)
	}
	params, ok := out[0].Params.(appwire.SandboxEscalationResolved)
	if !ok {
		t.Fatalf("params type = %T, want appwire.SandboxEscalationResolved", out[0].Params)
	}
	if params.EscalationID != "esc_1" {
		t.Fatalf("params = %+v", params)
	}
	// Same session-routing requirement as the requested notification.
	if params.ThreadID != "th1" || params.Ref != "local:th1" {
		t.Fatalf("resolved notification must carry threadId/ref, got %+v", params)
	}
}

// TestProject_SandboxEscalationNotInTranscript covers both directions of the
// M7 escalation pair (requested and resolved, wire-honesty spec Part B). Both
// ride the event stream only and are never a transcript turn: the projector
// emits exactly its own notification and touches no turn/item state, so the
// model can neither observe nor replay either one. This also stands in for
// the spec's demoted unknown-notification-tolerance check: an older client
// that does not yet recognize evener/sandbox/escalation/resolved sees no
// item/turn notification riding alongside it to react to badly — it drops the
// unrecognized notification exactly like any other, the established norm in
// the TUI and legacy web.
func TestProject_SandboxEscalationNotInTranscript(t *testing.T) {
	cases := []events.SessionEvent{
		{Kind: events.EventSandboxEscalationRequested, Data: events.SandboxEscalationRequestedData{EscalationID: "esc_1", Mode: "read-only", Tool: "write_file", Kind: "file_tool", DeniedPath: "hosts"}},
		{Kind: events.EventSandboxEscalationResolved, Data: events.SandboxEscalationResolvedData{EscalationID: "esc_1"}},
	}
	for _, ev := range cases {
		p := NewAppEventProjector("th1", "local:th1")
		out := p.Project(ev)
		for _, n := range out {
			switch n.Method {
			case appwire.NotifyHistoryUpdated, appwire.NotifyOverlayUpserted, appwire.NotifyOverlayDelta, appwire.NotifyOverlayReset, appwire.NotifyOverlayEnd:
				t.Fatalf("%s must not project a turn/item (transcript) notification, got %s", ev.Kind, n.Method)
			}
		}
	}
}

func TestAppEventProjectorProjectsThreadLifecycle(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5"},
	})

	thread := notificationThread(t, started, appwire.NotifyThreadStarted)
	if thread.ID != "th_1" || thread.SessionID != "th_1" || thread.Evener.Ref != "local:th_1" {
		t.Fatalf("started thread identity=%+v", thread)
	}
	if thread.Evener.Profile != "openai" || thread.ModelProvider != "gpt-5" {
		t.Fatalf("started thread model/profile=%+v", thread)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusIdle {
		t.Fatalf("started status=%+v, want idle", status)
	}

	closed := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionEnd,
		SessionID: "th_1",
		Data:      events.SessionEndData{Reason: "done", State: "closed"},
	})
	if !hasAppNotification(closed, appwire.NotifyThreadClosed) {
		t.Fatalf("closed lifecycle missing thread/closed: %+v", closed)
	}
	if status := notificationThreadStatus(t, closed, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusClosed {
		t.Fatalf("closed status=%+v, want closed", status)
	}
}

// TestAppEventProjectorRestoredSessionStartCarriesAwaitingState covers spec
// §5.4's "two touchpoints": a SessionStart event whose payload carries the
// restored session's re-derived state (agent/session_init.go's tail scan,
// spec §6) projects ThreadStatusAwaiting on both the initial Thread.Status and
// the threadStatus notification, instead of the old hardcoded idle.
// TestAppEventProjectorProjectsThreadLifecycle above is the paired negative:
// a SessionStart with no State set still projects idle.
func TestAppEventProjectorRestoredSessionStartCarriesAwaitingState(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventSessionStart,
		SessionID: "th_1",
		Data:      events.SessionStartData{Profile: "openai", Model: "gpt-5", Restored: true, State: appwire.ThreadStatusAwaiting},
	})

	thread := notificationThread(t, started, appwire.NotifyThreadStarted)
	if thread.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("restored SessionStart thread status=%+v, want awaiting", thread.Status)
	}
	if status := notificationThreadStatus(t, started, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("restored SessionStart status notification=%+v, want awaiting", status)
	}
}

func TestAppEventProjectorMapsAwaitingSessionEnd(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	sessionEnd := projector.Project(events.SessionEvent{Kind: events.EventSessionEnd, SessionID: "th_1", Data: events.SessionEndData{
		Reason: "input_complete",
		State:  "awaiting",
	}})

	if hasAppNotification(sessionEnd, appwire.NotifyThreadClosed) {
		t.Fatalf("awaiting SessionEnd emitted thread/closed: %+v", sessionEnd)
	}
	if status := notificationThreadStatus(t, sessionEnd, appwire.NotifyThreadStatusChanged); status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("awaiting status=%+v, want awaiting", status)
	}
}

func TestAppEventProjectorProjectsJobEvents(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{
		Kind:      events.EventJobStarted,
		SessionID: "th_1",
		Data: events.JobStartedData{
			JobID:            "job_1",
			JobType:          "shell",
			Status:           "running",
			FromWatch:        true,
			Background:       true,
			Command:          "make test",
			ParentDelegateID: "dlg_parent",
			OriginTurnID:     "turn_parent",
			OriginToolCallID: "call_shell",
			OriginItemID:     "item_shell",
		},
	})
	if len(started) != 1 || started[0].Method != appwire.NotifyEvenerJobStarted {
		t.Fatalf("started=%+v", started)
	}
	startedParams, ok := started[0].Params.(appwire.EvenerJobParams)
	if !ok {
		t.Fatalf("started params=%T", started[0].Params)
	}
	startedJob := startedParams.Job
	if startedJob.JobID != "job_1" || startedJob.JobType != "shell" || startedJob.Status != "running" || !startedJob.FromWatch || !startedJob.Background ||
		startedJob.Command != "make test" || startedJob.ParentDelegateID != "dlg_parent" ||
		startedJob.OriginTurnID != "turn_parent" || startedJob.OriginToolCallID != "call_shell" || startedJob.OriginItemID != "item_shell" {
		t.Fatalf("started job=%+v", startedJob)
	}

	exitCode := 137
	finished := projector.Project(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: "th_1",
		Data: events.JobFinishedData{
			JobID:            "job_1",
			JobType:          "shell",
			Status:           "failed",
			Reason:           "signal",
			ExitCode:         &exitCode,
			OutputBytes:      0,
			Background:       true,
			Command:          "make test",
			ParentDelegateID: "dlg_parent",
			OriginTurnID:     "turn_parent",
			OriginToolCallID: "call_shell",
			OriginItemID:     "item_shell",
		},
	})
	if len(finished) != 1 || finished[0].Method != appwire.NotifyEvenerJobFinished {
		t.Fatalf("finished=%+v", finished)
	}
	finishedParams, ok := finished[0].Params.(appwire.EvenerJobParams)
	if !ok {
		t.Fatalf("finished params=%T", finished[0].Params)
	}
	finishedJob := finishedParams.Job
	if finishedJob.JobID != "job_1" || finishedJob.JobType != "shell" || finishedJob.Status != "failed" ||
		finishedJob.Reason != "signal" || finishedJob.ExitCode == nil || *finishedJob.ExitCode != exitCode ||
		finishedJob.OutputBytes != 0 || !finishedJob.Background || finishedJob.Command != "make test" || finishedJob.ParentDelegateID != "dlg_parent" ||
		finishedJob.OriginTurnID != "turn_parent" || finishedJob.OriginToolCallID != "call_shell" || finishedJob.OriginItemID != "item_shell" {
		t.Fatalf("finished job=%+v", finishedJob)
	}
	finishedJSON := string(notificationParamsJSON(t, finished, appwire.NotifyEvenerJobFinished))
	if !strings.Contains(finishedJSON, `"outputBytes":0`) {
		t.Fatalf("finished notification json=%s missing zero outputBytes", finishedJSON)
	}
}

func TestProjectJobFinished_ExhaustionMetadata(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	resumable := true
	finished := projector.Project(events.SessionEvent{
		Kind:      events.EventJobFinished,
		SessionID: "th_1",
		Data: events.JobFinishedData{
			JobID:            "job_exhausted",
			JobType:          "shell",
			Status:           "exhausted",
			Reason:           "tool_round_budget_exhausted",
			ExhaustionBudget: "max_tool_rounds_per_input",
			ExhaustionLimit:  1,
			Resumable:        &resumable,
		},
	})
	if len(finished) != 1 || finished[0].Method != appwire.NotifyEvenerJobFinished {
		t.Fatalf("finished = %+v", finished)
	}
	params, ok := finished[0].Params.(appwire.EvenerJobParams)
	if !ok {
		t.Fatalf("finished params = %T", finished[0].Params)
	}
	job := params.Job
	if job.Status != "exhausted" || job.Reason != "tool_round_budget_exhausted" ||
		job.ExhaustionBudget != "max_tool_rounds_per_input" || job.ExhaustionLimit != 1 ||
		job.Resumable == nil || !*job.Resumable {
		t.Fatalf("finished job = %+v", job)
	}
}

func TestEvenerJobInfoDelegateFieldsAreOptional(t *testing.T) {
	payload, err := json.Marshal(appwire.EvenerJobInfo{
		JobID:       "job_shell",
		JobType:     "shell",
		Status:      "running",
		OutputBytes: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"delegateId", "task", "originTurnId", "originToolCallId", "originItemId", "transcriptRef"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("shell job payload %s unexpectedly contains %s", text, forbidden)
		}
	}

}

// TestAppEventProjectorProjectsQueueChanged (kata r80p) verifies the
// projector wraps QUEUE_CHANGED into a thread/queueChanged appwire
// notification carrying the authoritative depth + first-line-truncated
// preview.
func TestAppEventProjectorProjectsQueueChanged(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventQueueChanged,
		SessionID: "th_1",
		Data: events.QueueChangedData{
			Depth:   2,
			Preview: []string{"first line", "second"},
		},
	})
	if len(out) != 1 || out[0].Method != appwire.NotifyThreadQueueChanged {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadQueueChangedParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if params.ThreadID != "th_1" || params.Ref != "local:th_1" {
		t.Fatalf("params identity=%+v", params)
	}
	if params.Queue.Depth != 2 {
		t.Fatalf("depth=%d, want 2", params.Queue.Depth)
	}
	if len(params.Queue.Preview) != 2 || params.Queue.Preview[0] != "first line" || params.Queue.Preview[1] != "second" {
		t.Fatalf("preview=%+v", params.Queue.Preview)
	}
}

// TestAppEventProjectorCopiesConsumedClientMutationIDs (issue #1704) verifies
// a drain's consumed ids ride the projected notification's params, not the
// durable Queue facet, so a client can settle those optimistic records by
// positive evidence.
func TestAppEventProjectorCopiesConsumedClientMutationIDs(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventQueueChanged,
		SessionID: "th_1",
		Data: events.QueueChangedData{
			Depth:                     0,
			ConsumedClientMutationIDs: []string{"mutation-a", "mutation-b"},
		},
	})
	if len(out) != 1 {
		t.Fatalf("out=%+v", out)
	}
	params, ok := out[0].Params.(appwire.ThreadQueueChangedParams)
	if !ok {
		t.Fatalf("params=%T", out[0].Params)
	}
	if len(params.ConsumedClientMutationIDs) != 2 ||
		params.ConsumedClientMutationIDs[0] != "mutation-a" ||
		params.ConsumedClientMutationIDs[1] != "mutation-b" {
		t.Fatalf("ConsumedClientMutationIDs=%+v, want [mutation-a mutation-b]", params.ConsumedClientMutationIDs)
	}
}

// TestAppEventProjectorOmitsConsumedClientMutationIDsWhenAbsent (issue #1704)
// is the encoding-level half of the wire-shape contract: the set is positive
// evidence, so an absent key means this push named nothing consumed, and the
// field is never [].
func TestAppEventProjectorOmitsConsumedClientMutationIDsWhenAbsent(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	out := projector.Project(events.SessionEvent{
		Kind:      events.EventQueueChanged,
		SessionID: "th_1",
		Data:      events.QueueChangedData{Depth: 1, Preview: []string{"queued"}},
	})
	if len(out) != 1 {
		t.Fatalf("out=%+v", out)
	}
	payload, err := json.Marshal(out[0].Params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "consumedClientMutationIds") {
		t.Fatalf("payload %s unexpectedly contains consumedClientMutationIds", payload)
	}
}

func hasAppNotification(items []AppNotification, method string) bool {
	for _, item := range items {
		if item.Method == method {
			return true
		}
	}
	return false
}

func notificationParamsJSON(t *testing.T, items []AppNotification, method string) []byte {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		data, err := json.Marshal(item.Params)
		if err != nil {
			t.Fatalf("marshal params for %s: %v", method, err)
		}
		return data
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return nil
}

func notificationParams[T any](t *testing.T, items []AppNotification, method string) T {
	t.Helper()
	var params T
	if err := json.Unmarshal(notificationParamsJSON(t, items, method), &params); err != nil {
		t.Fatalf("unmarshal params for %s: %v", method, err)
	}
	return params
}

// notificationThread reads the "thread" payload off a thread/started
// notification, which now sends appwire.ThreadStartedParams (kcb5).
func notificationThread(t *testing.T, items []AppNotification, method string) appwire.Thread {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		p, ok := item.Params.(appwire.ThreadStartedParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return p.Thread
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.Thread{}
}

func notificationThreadStatus(t *testing.T, items []AppNotification, method string) appwire.ThreadStatus {
	t.Helper()
	for _, item := range items {
		if item.Method != method {
			continue
		}
		params, ok := item.Params.(appwire.ThreadStatusChangedParams)
		if !ok {
			t.Fatalf("params=%T", item.Params)
		}
		return params.Status
	}
	t.Fatalf("missing notification %q in %+v", method, items)
	return appwire.ThreadStatus{}
}
