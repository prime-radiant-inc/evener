package jobstore

import (
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/provenance"
)

func ev(kind EventKind, seq int64, jobID string, mut func(*Event)) Event {
	e := Event{Kind: kind, Seq: seq, TS: time.Unix(seq, 0).UTC(), JobID: jobID}
	if mut != nil {
		mut(&e)
	}
	return e
}

func TestFoldBuildsRunningShellRecord(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.Command = "npm run dev"
			e.Description = "dev server"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
			e.WorkingDir = "/repo/worktrees/lane"
		}),
	}
	recs := Fold(events)
	r := recs["job_A"]
	if r == nil {
		t.Fatal("expected record for job_A")
	}
	if r.Status != StatusRunning {
		t.Errorf("status = %q, want running", r.Status)
	}
	if r.Command != "npm run dev" || r.Description != "dev server" {
		t.Errorf("command/description not folded: %+v", r)
	}
	if r.WorkingDir != "/repo/worktrees/lane" {
		t.Errorf("working dir = %q, want folded launch workdir", r.WorkingDir)
	}
	if r.NotifyState != NotifyNotArmed {
		t.Errorf("notify state = %q, want not_armed", r.NotifyState)
	}
}

func TestFoldIgnoresWatchSendEventsForJobRecords(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", Message: "pending"}},
	}

	recs := Fold(events)
	if _, ok := recs[""]; ok {
		t.Fatalf("watch-send event created blank job record: %+v", recs[""])
	}
	if recs["job_A"] == nil {
		t.Fatal("normal job record missing")
	}
}

func TestFoldAppliesOutputPathFromStarted(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.Task = "summarize"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
			e.OutputPath = "/tmp/serf/jobs/job_A.log"
		}),
	}
	r := Fold(events)["job_A"]
	if r.OutputPath != "/tmp/serf/jobs/job_A.log" {
		t.Errorf("output_path = %q, want /tmp/serf/jobs/job_A.log", r.OutputPath)
	}
}

func TestFoldAppliesDelegateIDFromStarted(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
	}

	r := Fold(events)["job_A"]
	if r == nil {
		t.Fatal("job_A missing")
	}
	if r.DelegateID != "dlg_A" {
		t.Fatalf("delegate_id = %q, want dlg_A", r.DelegateID)
	}
}

func TestFoldDelegateDescriptorSchemaAndStructuredReason(t *testing.T) {
	valid := false
	events := []Event{
		{
			Kind:  EventJobStarted,
			Seq:   1,
			JobID: "job_1",
			Type:  JobDelegate,
			DelegateRestore: &DelegateRestoreDescriptor{
				Version:        1,
				ChildSessionID: "child_1",
				TranscriptRef:  "transcript_1",
				ResultSchema:   map[string]any{"type": "object"},
			},
		},
		{
			Kind:                   EventJobFinished,
			Seq:                    2,
			JobID:                  "job_1",
			Status:                 StatusCompleted,
			StructuredResultValid:  &valid,
			StructuredResultReason: "schema_result_missing",
		},
	}
	rec := Fold(events)["job_1"]
	if rec.DelegateRestore == nil || rec.DelegateRestore.ResultSchema == nil {
		t.Fatalf("delegate restore/schema not folded: %+v", rec.DelegateRestore)
	}
	if rec.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("reason = %q", rec.StructuredResultReason)
	}
}

func TestFoldDelegatesLinksJobsAndProjectsCurrentLatest(t *testing.T) {
	start1 := time.Unix(1, 0).UTC()
	end1 := time.Unix(2, 0).UTC()
	start2 := time.Unix(3, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{
				ChildSessionID:   "child_A",
				TranscriptRef:    "local:child_A",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				AgentType:        "default",
				Generation:       "dg_1",
				Resumable:        true,
			}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.OwnerSessionID = "owner"
			e.VisibleToSession = "owner"
			e.TranscriptRef = "local:child_A"
			e.StartedAt = &start1
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end1
		}),
		ev(EventJobStarted, 4, "job_2", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.OwnerSessionID = "owner"
			e.VisibleToSession = "owner"
			e.TranscriptRef = "local:child_A"
			e.StartedAt = &start2
		}),
	}

	delegates := FoldDelegates(events)
	d := delegates["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.CurrentJobID != "job_2" || d.LatestJobID != "job_2" || d.Status != DelegateRunning {
		t.Fatalf("delegate projection = %+v, want current/latest job_2 running", d)
	}
	if d.ChildSessionID != "child_A" || d.TranscriptRef != "local:child_A" || !d.Resumable {
		t.Fatalf("delegate identity = %+v, want durable child metadata", d)
	}
}

func TestFoldDelegatesProjectsOwnerFromJobStarted(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_child_delegate", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_child"
			e.OwnerSessionID = "CHILD"
			e.VisibleToSession = "ROOT"
			e.TranscriptRef = "local:child"
			e.StartedAt = &start
		}),
	}

	delegates := FoldDelegates(events)
	d := delegates["dlg_child"]
	if d == nil {
		t.Fatal("delegate dlg_child missing")
	}
	if d.OwnerSessionID != "CHILD" || d.VisibleSessionID != "ROOT" {
		t.Fatalf("delegate ownership = owner %q visible %q, want CHILD/ROOT", d.OwnerSessionID, d.VisibleSessionID)
	}
}

func TestFoldDelegatesAppliesSessionAssignedResumability(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	resumable := false
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{
				ChildSessionID: "child_A",
				TranscriptRef:  "local:child_A",
				Generation:     "dg_1",
				Resumable:      true,
			}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.TranscriptRef = "local:child_A"
			e.StartedAt = &start
		}),
		ev(EventJobSessionAssigned, 3, "job_1", func(e *Event) {
			e.TranscriptRef = "local:child_A"
			e.Resumable = &resumable
			e.NotResumableWhy = "missing checkpoint"
		}),
		ev(EventJobFinished, 4, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end
		}),
	}

	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.Resumable {
		t.Fatalf("delegate resumable = true, want false: %+v", d)
	}
	if d.Status != DelegateNotResumable {
		t.Fatalf("delegate status = %q, want %q", d.Status, DelegateNotResumable)
	}
	if d.NotResumableWhy != "missing checkpoint" {
		t.Fatalf("delegate not_resumable_reason = %q, want missing checkpoint", d.NotResumableWhy)
	}
}

func TestFoldDelegatesClosesStopGateForCurrentJob(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCancelled
			e.Reason = "stopped_by_parent"
			e.EndedAt = &end
		}),
		ev(EventDelegateStopGateClosed, 4, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{Generation: "dg_2", StopJobID: "job_1"}
		}),
	}

	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if !d.StopGateClosed || d.Generation != "dg_2" || d.CurrentJobID != "" || d.LatestJobID != "job_1" || d.Status != DelegateStopped {
		t.Fatalf("delegate after stop = %+v, want closed gate with latest job_1 stopped and no current job", d)
	}
}

func TestFoldDelegatesIgnoresStaleStopGateAfterNewerStart(t *testing.T) {
	start1 := time.Unix(1, 0).UTC()
	end1 := time.Unix(2, 0).UTC()
	start2 := time.Unix(3, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start1
		}),
		ev(EventJobFinished, 3, "job_1", func(e *Event) {
			e.Status = StatusCancelled
			e.Reason = "stopped_by_parent"
			e.EndedAt = &end1
		}),
		ev(EventDelegateCreated, 4, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_3", Resumable: true}
		}),
		ev(EventJobStarted, 5, "job_2", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start2
		}),
		ev(EventDelegateStopGateClosed, 6, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{Generation: "dg_stale", StopJobID: "job_1"}
		}),
	}

	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.StopGateClosed || d.Generation != "dg_3" || d.CurrentJobID != "job_2" || d.Status != DelegateRunning {
		t.Fatalf("delegate after stale gate = %+v, want job_2 running with generation dg_3", d)
	}
}

func TestFoldDelegatesIgnoresFinishedNonCurrentJob(t *testing.T) {
	start1 := time.Unix(1, 0).UTC()
	start2 := time.Unix(2, 0).UTC()
	end1 := time.Unix(3, 0).UTC()
	events := []Event{
		ev(EventDelegateCreated, 1, "", func(e *Event) {
			e.DelegateID = "dlg_A"
			e.Delegate = &DelegateEvent{ChildSessionID: "child_A", TranscriptRef: "local:child_A", Generation: "dg_1", Resumable: true}
		}),
		ev(EventJobStarted, 2, "job_1", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start1
		}),
		ev(EventJobStarted, 3, "job_2", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_A"
			e.StartedAt = &start2
		}),
		ev(EventJobFinished, 4, "job_1", func(e *Event) {
			e.Status = StatusCompleted
			e.EndedAt = &end1
		}),
	}

	d := FoldDelegates(events)["dlg_A"]
	if d == nil {
		t.Fatal("delegate dlg_A missing")
	}
	if d.CurrentJobID != "job_2" || d.LatestJobID != "job_2" || d.Status != DelegateRunning {
		t.Fatalf("delegate after stale finish = %+v, want current/latest job_2 running", d)
	}
}

func TestFoldWatchesUpsertsByConfigHashAndClearsByID(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{
				Generation:       "wg_1",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				Target:           "job_1",
				SendTo:           "dlg_obs",
				ConfigHash:       "hash_A",
				Condition:        "events: [communicate]",
			}
		}),
		ev(EventWatchRegistered, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{
				Generation:       "wg_1",
				OwnerSessionID:   "owner",
				VisibleSessionID: "owner",
				Target:           "job_1",
				SendTo:           "dlg_obs",
				ConfigHash:       "hash_A",
				Condition:        "events: [communicate]",
			}
		}),
		ev(EventWatchCleared, 3, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "cleared"}
		}),
	}

	watches := FoldWatches(events)
	w := watches["watch_A"]
	if w == nil {
		t.Fatal("watch_A missing")
	}
	if w.Active || w.EndReason != "cleared" || w.Target != "job_1" || w.SendTo != "dlg_obs" {
		t.Fatalf("watch = %+v, want inactive cleared registry row", w)
	}
}

func TestFoldWatchesRejectsStaleClearGeneration(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_2", OwnerSessionID: "owner", VisibleSessionID: "owner", Target: "job_1", ConfigHash: "hash_2"}
		}),
		ev(EventWatchCleared, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "cleared"}
		}),
	}

	w := FoldWatches(events)["watch_A"]
	if w == nil || !w.Active || w.Generation != "wg_2" {
		t.Fatalf("watch = %+v, want stale clear ignored", w)
	}
}

func TestFoldWatchesRejectsInvalidRegistrationAndEmptyClearGeneration(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_missing_target"
			e.Watch = &WatchEvent{Generation: "wg_1", OwnerSessionID: "owner", VisibleSessionID: "owner", ConfigHash: "hash_1"}
		}),
		ev(EventWatchRegistered, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", OwnerSessionID: "owner", VisibleSessionID: "owner", Target: "job_1", ConfigHash: "hash_1"}
		}),
		ev(EventWatchCleared, 3, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{EndReason: "cleared"}
		}),
	}

	watches := FoldWatches(events)
	if watches["watch_missing_target"] != nil {
		t.Fatalf("invalid registration folded into active registry: %+v", watches["watch_missing_target"])
	}
	w := watches["watch_A"]
	if w == nil || !w.Active {
		t.Fatalf("watch_A = %+v, want active because empty clear generation is ignored", w)
	}
}

func TestFoldWatchesPreservesFirstClearReason(t *testing.T) {
	events := []Event{
		ev(EventWatchRegistered, 1, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", OwnerSessionID: "owner", VisibleSessionID: "owner", Target: "job_1", ConfigHash: "hash_1"}
		}),
		ev(EventWatchCleared, 2, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "auto_removed_terminal"}
		}),
		ev(EventWatchCleared, 3, "", func(e *Event) {
			e.WatchID = "watch_A"
			e.Watch = &WatchEvent{Generation: "wg_1", EndReason: "cleared"}
		}),
	}

	w := FoldWatches(events)["watch_A"]
	if w == nil || w.Active || w.EndReason != "auto_removed_terminal" {
		t.Fatalf("watch = %+v, want first clear reason preserved", w)
	}
}

func TestDelegateRestoreDescriptorSurvivesStoreReopenAndFold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	start := time.Unix(10, 0).UTC()
	desc := &DelegateRestoreDescriptor{
		Version:            1,
		ChildSessionID:     "child_1",
		TranscriptRef:      "local:child_1",
		ParentSessionID:    "parent_1",
		ParentJobID:        "job_1",
		OwnerSessionID:     "owner_1",
		VisibleSessionID:   "visible_1",
		OriginTurnID:       "turn_1",
		OriginToolCallID:   "call_1",
		Task:               "inspect",
		AgentType:          "reviewer",
		RequestedModel:     "openai/gpt-5.3",
		ResolvedProfileID:  "openai",
		ResolvedModel:      "gpt-5.3",
		ReasoningEffort:    "high",
		AgentName:          "reviewer",
		FrozenRolePrompt:   "Review carefully.",
		FrozenTaskPrompt:   "Check the patch.",
		FrozenToolNames:    []string{"read_file", "task_list"},
		FrozenSkillNames:   []string{"review-skill"},
		FrozenSkillBodies:  []string{"Use the stored review checklist."},
		WorkingDir:         "/work",
		LocalEnvPolicy:     "core_only",
		ResultSchema:       map[string]any{"type": "object", "required": []any{"message"}},
		ExplicitToolGrants: []string{"shell"},
		Isolation:          "worktree",
	}
	if err := store.Append(Event{
		Kind:             EventJobStarted,
		JobID:            "job_1",
		Type:             JobDelegate,
		Task:             "inspect",
		OwnerSessionID:   "owner_1",
		VisibleToSession: "visible_1",
		StartedAt:        &start,
		TranscriptRef:    "local:child_1",
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append start: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recs, err := reopened.Load()
	if err != nil {
		t.Fatalf("load reopened store: %v", err)
	}
	rec := recs["job_1"]
	if rec == nil || rec.DelegateRestore == nil {
		t.Fatalf("reopened record missing descriptor: %+v", rec)
	}
	got := rec.DelegateRestore
	if got.ChildSessionID != desc.ChildSessionID ||
		got.TranscriptRef != desc.TranscriptRef ||
		got.ParentSessionID != desc.ParentSessionID ||
		got.ParentJobID != desc.ParentJobID ||
		got.OwnerSessionID != desc.OwnerSessionID ||
		got.VisibleSessionID != desc.VisibleSessionID ||
		got.OriginTurnID != desc.OriginTurnID ||
		got.OriginToolCallID != desc.OriginToolCallID ||
		got.OriginItemID != desc.OriginItemID ||
		got.Task != desc.Task ||
		got.AgentType != desc.AgentType ||
		got.RequestedModel != desc.RequestedModel ||
		got.ResolvedProfileID != desc.ResolvedProfileID ||
		got.ResolvedModel != desc.ResolvedModel ||
		got.ReasoningEffort != desc.ReasoningEffort ||
		got.AgentName != desc.AgentName ||
		got.FrozenRolePrompt != desc.FrozenRolePrompt ||
		got.FrozenTaskPrompt != desc.FrozenTaskPrompt ||
		got.WorkingDir != desc.WorkingDir ||
		got.LocalEnvPolicy != desc.LocalEnvPolicy ||
		got.ParentWatchGranted != desc.ParentWatchGranted ||
		got.DelegationAllowance != desc.DelegationAllowance ||
		got.Isolation != desc.Isolation {
		t.Fatalf("reopened descriptor = %+v, want %+v", got, desc)
	}
	if len(got.FrozenToolNames) != 2 || got.FrozenToolNames[0] != "read_file" || got.FrozenToolNames[1] != "task_list" {
		t.Fatalf("frozen tool names = %+v", got.FrozenToolNames)
	}
	if len(got.FrozenSkillNames) != 1 || got.FrozenSkillNames[0] != "review-skill" {
		t.Fatalf("frozen skill names = %+v", got.FrozenSkillNames)
	}
	if len(got.FrozenSkillBodies) != 1 || got.FrozenSkillBodies[0] != "Use the stored review checklist." {
		t.Fatalf("frozen skill bodies = %+v", got.FrozenSkillBodies)
	}
	if len(got.ExplicitToolGrants) != 1 || got.ExplicitToolGrants[0] != "shell" {
		t.Fatalf("explicit tool grants = %+v", got.ExplicitToolGrants)
	}
	schema, ok := got.ResultSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("result_schema = %#v", got.ResultSchema)
	}
}

func TestFoldGrantsBuildsObserverTable(t *testing.T) {
	events := []Event{
		ev(EventWatchReadGrant, 1, "job_A", func(e *Event) { e.ObserverSessionID = "obs_1" }),
		// Duplicate (observer, job) pairs fold to a single grant.
		ev(EventWatchReadGrant, 2, "job_A", func(e *Event) { e.ObserverSessionID = "obs_1" }),
		ev(EventWatchReadGrant, 3, "job_B", func(e *Event) { e.ObserverSessionID = "obs_1" }),
		ev(EventWatchReadGrant, 4, "job_A", func(e *Event) { e.ObserverSessionID = "obs_2" }),
		// Malformed grants (missing observer or job) are skipped.
		ev(EventWatchReadGrant, 5, "job_C", nil),
		ev(EventWatchReadGrant, 6, "", func(e *Event) { e.ObserverSessionID = "obs_3" }),
	}
	grants := FoldGrants(events)
	if len(grants) != 2 {
		t.Fatalf("observers = %d, want 2: %+v", len(grants), grants)
	}
	if len(grants["obs_1"]) != 2 || !grants["obs_1"]["job_A"] || !grants["obs_1"]["job_B"] {
		t.Errorf("obs_1 grants = %+v, want job_A and job_B", grants["obs_1"])
	}
	if len(grants["obs_2"]) != 1 || !grants["obs_2"]["job_A"] {
		t.Errorf("obs_2 grants = %+v, want job_A", grants["obs_2"])
	}
}

func TestFoldIgnoresWatchReadGrantForJobRecords(t *testing.T) {
	events := []Event{
		ev(EventWatchReadGrant, 1, "job_A", func(e *Event) { e.ObserverSessionID = "obs_1" }),
	}
	recs := Fold(events)
	if len(recs) != 0 {
		t.Fatalf("watch-read-grant event created job records: %+v", recs)
	}
}

func TestWatchReadGrantSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Append the same (observer, job) pair twice: the fold must stay idempotent.
	for range 2 {
		if err := store.Append(Event{
			Kind:              EventWatchReadGrant,
			JobID:             "job_A",
			ObserverSessionID: "obs_1",
		}); err != nil {
			t.Fatalf("append grant: %v", err)
		}
	}
	grants, err := store.LoadGrants()
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if len(grants) != 1 || len(grants["obs_1"]) != 1 || !grants["obs_1"]["job_A"] {
		t.Fatalf("grants = %+v, want exactly obs_1 -> job_A", grants)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	grants, err = reopened.LoadGrants()
	if err != nil {
		t.Fatalf("grants after reopen: %v", err)
	}
	if len(grants) != 1 || len(grants["obs_1"]) != 1 || !grants["obs_1"]["job_A"] {
		t.Fatalf("reopened grants = %+v, want exactly obs_1 -> job_A", grants)
	}
}

func TestFoldStructuredResultReasonOnlyForInvalidStructuredResult(t *testing.T) {
	valid := true
	events := []Event{
		ev(EventJobFinished, 1, "job_valid", func(e *Event) {
			e.Status = StatusCompleted
			e.StructuredResultValid = &valid
			e.StructuredResultReason = "schema_result_missing"
		}),
		ev(EventJobFinished, 1, "job_unspecified", func(e *Event) {
			e.Status = StatusCompleted
			e.StructuredResultReason = "schema_result_missing"
		}),
	}
	recs := Fold(events)
	if recs["job_valid"].StructuredResultReason != "" {
		t.Fatalf("valid structured result folded reason %q", recs["job_valid"].StructuredResultReason)
	}
	if recs["job_unspecified"].StructuredResultReason != "" {
		t.Fatalf("unspecified structured result folded reason %q", recs["job_unspecified"].StructuredResultReason)
	}
}

func TestFoldAppliesFinishAndKeepsFirstGeneration(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(2, 0).UTC()
	code := 0
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			valid := true
			e.Status = StatusCompleted
			e.Reason = "exit_zero"
			e.ExitCode = &code
			e.EndedAt = &end
			e.OutputBytes = 2048
			e.StructuredResult = map[string]any{"summary": "done"}
			e.StructuredResultValid = &valid
			e.TerminalGen = "GEN1"
		}),
		// A duplicate reconstructed terminal write must NOT replace the generation.
		ev(EventJobFinished, 3, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN2"
		}),
	}
	r := Fold(events)["job_A"]
	if r.Status != StatusCompleted || r.Reason != "exit_zero" {
		t.Errorf("finish not folded: %+v", r)
	}
	if r.OutputBytes != 2048 || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("finish payload not folded: %+v", r)
	}
	structured, ok := r.StructuredResult.(map[string]any)
	if !ok || structured["summary"] != "done" {
		t.Errorf("structured result = %+v, want summary=done", r.StructuredResult)
	}
	if r.StructuredResultValid == nil || !*r.StructuredResultValid {
		t.Errorf("structured_result_valid = %v, want true", r.StructuredResultValid)
	}
	if r.TerminalGen != "GEN1" {
		t.Errorf("terminal_generation = %q, want GEN1 (first wins)", r.TerminalGen)
	}
}

func TestFoldAppliesEventsInSeqOrder(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobFinished, 3, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN2"
		}),
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN1"
		}),
	}
	r := Fold(events)["job_A"]
	if r.TerminalGen != "GEN1" {
		t.Errorf("terminal_generation = %q, want GEN1 from lower seq event", r.TerminalGen)
	}
}

func TestFoldAppliesSessionAssigned(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	resumable := false
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobSessionAssigned, 2, "job_A", func(e *Event) {
			e.TranscriptRef = "sessions/S2.transcript.jsonl"
			e.Resumable = &resumable
			e.NotResumableWhy = "missing checkpoint"
		}),
	}
	r := Fold(events)["job_A"]
	if r.TranscriptRef != "sessions/S2.transcript.jsonl" {
		t.Errorf("transcript_ref = %q, want sessions/S2.transcript.jsonl", r.TranscriptRef)
	}
	if r.Resumable == nil || *r.Resumable {
		t.Errorf("resumable = %v, want false", r.Resumable)
	}
	if r.NotResumableWhy != "missing checkpoint" {
		t.Errorf("not_resumable_reason = %q, want missing checkpoint", r.NotResumableWhy)
	}
}

func TestFoldNotificationStateTransitions(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) { e.Status = StatusCompleted; e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationPending, 3, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationDelivered, 4, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
	}
	r := Fold(events)["job_A"]
	if r.NotifyState != NotifyDelivered {
		t.Errorf("notify state = %q, want delivered", r.NotifyState)
	}
}

func TestFoldNotificationPendingDoesNotDowngradeDelivered(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) { e.Status = StatusCompleted; e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationDelivered, 3, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
		ev(EventJobNotificationPending, 4, "job_A", func(e *Event) { e.TerminalGen = "GEN1" }),
	}
	r := Fold(events)["job_A"]
	if r.NotifyState != NotifyDelivered {
		t.Errorf("notify state = %q, want delivered", r.NotifyState)
	}
}

func TestFoldIgnoresNotificationsForDiscardedTerminalGeneration(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN1"
		}),
		ev(EventJobFinished, 3, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN2"
		}),
		ev(EventJobNotificationPending, 4, "job_A", func(e *Event) { e.TerminalGen = "GEN2" }),
		ev(EventJobNotificationDelivered, 5, "job_A", func(e *Event) { e.TerminalGen = "GEN2" }),
	}
	r := Fold(events)["job_A"]
	if r.TerminalGen != "GEN1" {
		t.Errorf("terminal_generation = %q, want GEN1 (first wins)", r.TerminalGen)
	}
	if r.NotifyState != NotifyNotArmed {
		t.Errorf("notify state = %q, want not_armed", r.NotifyState)
	}
}

func TestFoldStoresJobProvenanceFromStartedEvent(t *testing.T) {
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.OwnerSessionID = "session_1"
			e.VisibleToSession = "session_1"
			e.Provenance = p
		}),
	}

	rec := Fold(events)["job_A"]
	if rec == nil {
		t.Fatal("job_A missing")
	}
	if !provenance.ContainsWatch(rec.Provenance, "watch_A", "wg_1") {
		t.Fatalf("record provenance = %+v, want watch_A/wg_1", rec.Provenance)
	}
}

func TestFoldStoresRicherJobProvenanceFromFinishedEvent(t *testing.T) {
	startProvenance := provenance.WithWatch(nil, "watch_start", "wg_1", "wd_start", "session_1", "caller")
	finishProvenance := provenance.WithWatch(startProvenance, "watch_finish", "wg_1", "wd_finish", "session_1", "caller")
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.OwnerSessionID = "session_1"
			e.VisibleToSession = "session_1"
			e.Provenance = startProvenance
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN1"
			e.Provenance = finishProvenance
		}),
	}

	rec := Fold(events)["job_A"]
	if rec == nil {
		t.Fatal("job_A missing")
	}
	if !provenance.ContainsWatch(rec.Provenance, "watch_start", "wg_1") ||
		!provenance.ContainsWatch(rec.Provenance, "watch_finish", "wg_1") {
		t.Fatalf("record provenance = %+v, want terminal provenance with start and finish watches", rec.Provenance)
	}
}

func TestFoldStoresNotificationProvenanceFromPendingEvent(t *testing.T) {
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.OwnerSessionID = "session_1"
			e.VisibleToSession = "session_1"
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusCompleted
			e.TerminalGen = "GEN1"
		}),
		ev(EventJobNotificationPending, 3, "job_A", func(e *Event) {
			e.TerminalGen = "GEN1"
			e.Provenance = p
		}),
	}

	rec := Fold(events)["job_A"]
	if rec == nil {
		t.Fatal("job_A missing")
	}
	if !provenance.ContainsWatch(rec.NotificationProvenance, "watch_A", "wg_1") {
		t.Fatalf("notification provenance = %+v, want watch_A/wg_1", rec.NotificationProvenance)
	}
}

// TestFoldMarksDisposedDelegate covers the native worktree tools spec §9 step 5
// disposed flag: a delegate_disposed event keyed by delegate id marks every job
// record of that delegate (across resumes) Disposed, and only that delegate's
// records.
func TestFoldMarksDisposedDelegate(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_1"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobStarted, 2, "job_A2", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_1"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobStarted, 3, "job_B", func(e *Event) {
			e.Type = JobDelegate
			e.DelegateID = "dlg_2"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventDelegateDisposed, 4, "", func(e *Event) {
			e.DelegateID = "dlg_1"
		}),
	}

	recs := Fold(events)
	if recs["job_A"] == nil || !recs["job_A"].Disposed {
		t.Errorf("job_A Disposed = %v, want true", recs["job_A"] != nil && recs["job_A"].Disposed)
	}
	if !recs["job_A2"].Disposed {
		t.Error("job_A2 (same delegate, later resume) not marked disposed")
	}
	if recs["job_B"].Disposed {
		t.Error("job_B (different delegate) wrongly marked disposed")
	}

	// Round-trips through FoldOrdered too.
	ordered := FoldOrdered(events)
	for _, r := range ordered {
		if r.DelegateID == "dlg_1" && !r.Disposed {
			t.Errorf("FoldOrdered: %s not marked disposed", r.JobID)
		}
	}
}
