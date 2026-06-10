package jobstore

import (
	"path/filepath"
	"testing"
	"time"
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
		WorkingDir:         "/work",
		LocalEnvPolicy:     "core_only",
		ResultSchema:       map[string]any{"type": "object", "required": []any{"message"}},
		ExplicitToolGrants: []string{"shell"},
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
		got.RequestedModel != desc.RequestedModel ||
		got.ResolvedProfileID != desc.ResolvedProfileID ||
		got.ResolvedModel != desc.ResolvedModel ||
		got.LocalEnvPolicy != desc.LocalEnvPolicy {
		t.Fatalf("reopened descriptor = %+v, want %+v", got, desc)
	}
	if len(got.FrozenToolNames) != 2 || got.FrozenToolNames[0] != "read_file" || got.FrozenToolNames[1] != "task_list" {
		t.Fatalf("frozen tool names = %+v", got.FrozenToolNames)
	}
	if len(got.ExplicitToolGrants) != 1 || got.ExplicitToolGrants[0] != "shell" {
		t.Fatalf("explicit tool grants = %+v", got.ExplicitToolGrants)
	}
	schema, ok := got.ResultSchema.(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Fatalf("result_schema = %#v", got.ResultSchema)
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
