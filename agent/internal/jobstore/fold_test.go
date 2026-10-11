package jobstore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/provenance"
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
			e.Type = JobShell
			e.Task = "summarize"
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
			e.OutputPath = "/tmp/evener/jobs/job_A.log"
		}),
	}
	r := Fold(events)["job_A"]
	if r.OutputPath != "/tmp/evener/jobs/job_A.log" {
		t.Errorf("output_path = %q, want /tmp/evener/jobs/job_A.log", r.OutputPath)
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

func TestFold_ExhaustedPreservesBudgetMetadataAndFirstTerminalWins(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	events := []Event{
		ev(EventJobStarted, 1, "job_A", func(e *Event) {
			e.Type = JobShell
			e.OwnerSessionID = "S1"
			e.VisibleToSession = "S1"
			e.StartedAt = &start
		}),
		ev(EventJobFinished, 2, "job_A", func(e *Event) {
			e.Status = StatusExhausted
			e.Reason = "delegate_turn_budget_exhausted"
			e.TerminalGen = "GEN1"
			e.ExhaustionBudget = "max_turns"
			e.ExhaustionLimit = 500
		}),
	}

	r := Fold(events)["job_A"]
	if r.Status != StatusExhausted || r.Reason != "delegate_turn_budget_exhausted" {
		t.Fatalf("exhausted terminal state not folded: %+v", r)
	}
	if r.TerminalGen != "GEN1" {
		t.Fatalf("terminal_generation = %q, want GEN1", r.TerminalGen)
	}
	if r.ExhaustionBudget != "max_turns" || r.ExhaustionLimit != 500 {
		t.Fatalf("exhaustion metadata = (%q, %d), want (max_turns, 500)", r.ExhaustionBudget, r.ExhaustionLimit)
	}
	events = append(events, ev(EventJobFinished, 3, "job_A", func(e *Event) {
		e.Status = StatusCompleted
		e.Reason = "completed_later"
		e.TerminalGen = "GEN2"
		e.ExhaustionBudget = "max_continuations"
		e.ExhaustionLimit = 1
	}))

	r = Fold(events)["job_A"]
	if r.Status != StatusExhausted || r.Reason != "delegate_turn_budget_exhausted" || r.TerminalGen != "GEN1" {
		t.Fatalf("later terminal event replaced exhausted state: %+v", r)
	}
	if r.ExhaustionBudget != "max_turns" || r.ExhaustionLimit != 500 {
		t.Fatalf("later terminal event replaced exhaustion metadata: %+v", r)
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
			e.Type = JobShell
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
			e.Type = JobShell
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
			e.Type = JobShell
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
