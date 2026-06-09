package jobstore

import (
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
			e.Status = StatusCompleted
			e.Reason = "exit_zero"
			e.ExitCode = &code
			e.EndedAt = &end
			e.OutputBytes = 2048
			e.StructuredResult = map[string]any{"summary": "done"}
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
