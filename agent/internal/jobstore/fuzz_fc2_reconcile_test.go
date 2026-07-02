package jobstore

import (
	"testing"
	"time"
)

// FuzzFc2Reconcile drives Reconcile — the pure recovery decision that finalizes
// durably-running jobs with no live in-memory runtime — over adversarial record
// sets and live-job sets. Reconcile is the core of jobManager.reconcileLostJobs
// (daemon recovery), yet it had no dedicated fuzz target. Oracles (beyond
// never-panic):
//   - determinism: the same (records, live) yields the same event set;
//   - eligibility: exactly the records that are StatusRunning AND not live produce
//     a runtime_lost job_finished event; nothing else does;
//   - well-formedness: every emitted event is a StatusStopped job_finished with a
//     non-empty terminal generation and the runtime_lost reason;
//   - deterministic ordering: emitted events are sorted by job id;
//   - fixpoint: applying each reconcile event's terminal status back onto its
//     record and reconciling again (the lost jobs still absent from live) produces
//     nothing — the recovery converges in one pass.
func FuzzFc2Reconcile(f *testing.F) {
	// data encodes N jobs, three bytes each: status selector, "is live" flag,
	// job-id selector (a small pool so collisions and ordering matter).
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0})          // one running, not live -> reconciled
	f.Add([]byte{0, 1, 0})          // one running, live -> kept
	f.Add([]byte{1, 0, 1, 0, 0, 2}) // a terminal + a running

	f.Fuzz(func(t *testing.T, data []byte) {
		statuses := []Status{StatusRunning, StatusCompleted, StatusStopped, StatusFailed, StatusCancelled}
		jobIDs := []string{"job_1", "job_2", "job_3", "job_4"}

		records := map[string]*JobRecord{}
		live := map[string]bool{}
		for i := 0; i+2 < len(data); i += 3 {
			id := jobIDs[int(data[i+2])%len(jobIDs)]
			records[id] = &JobRecord{
				JobID:  id,
				Type:   JobShell,
				Status: statuses[int(data[i])%len(statuses)],
			}
			if data[i+1]&1 == 1 {
				live[id] = true
			}
		}

		now := time.Unix(1_700_000_000, 0).UTC()
		events := Reconcile(records, live, now)

		// Determinism.
		events2 := Reconcile(records, live, now)
		if len(events) != len(events2) {
			t.Fatalf("non-deterministic event count: %d vs %d", len(events), len(events2))
		}
		for i := range events {
			if events[i].JobID != events2[i].JobID {
				t.Fatalf("non-deterministic order at %d: %q vs %q", i, events[i].JobID, events2[i].JobID)
			}
		}

		// Eligibility: exactly the running-and-not-live records reconcile.
		wantLost := map[string]bool{}
		for id, r := range records {
			if r.Status == StatusRunning && !live[id] {
				wantLost[id] = true
			}
		}
		if len(events) != len(wantLost) {
			t.Fatalf("reconciled %d, want %d", len(events), len(wantLost))
		}
		seen := map[string]bool{}
		var prevID string
		for i, e := range events {
			if !wantLost[e.JobID] {
				t.Fatalf("event for %q which is not running-and-not-live", e.JobID)
			}
			if seen[e.JobID] {
				t.Fatalf("duplicate reconcile event for %q", e.JobID)
			}
			seen[e.JobID] = true
			// Well-formedness.
			if e.Kind != EventJobFinished {
				t.Fatalf("event kind=%q, want job_finished", e.Kind)
			}
			if e.Status != StatusStopped {
				t.Fatalf("event status=%q, want stopped", e.Status)
			}
			if e.Reason != "runtime_lost" {
				t.Fatalf("event reason=%q, want runtime_lost", e.Reason)
			}
			if e.TerminalGen == "" {
				t.Fatalf("event for %q has empty terminal generation", e.JobID)
			}
			// Sorted by job id.
			if i > 0 && e.JobID < prevID {
				t.Fatalf("events not sorted: %q after %q", e.JobID, prevID)
			}
			prevID = e.JobID
		}

		// Fixpoint: apply each event's terminal status back onto its record, then
		// reconcile again — with the lost jobs still absent from live, a converged
		// recovery produces nothing.
		reconciled := map[string]*JobRecord{}
		for id, r := range records {
			clone := *r
			reconciled[id] = &clone
		}
		for _, e := range events {
			reconciled[e.JobID].Status = e.Status
			if !reconciled[e.JobID].Status.IsTerminal() {
				t.Fatalf("applied reconcile status for %q is not terminal: %q", e.JobID, e.Status)
			}
		}
		if again := Reconcile(reconciled, live, now); len(again) != 0 {
			t.Fatalf("second reconcile produced %d events, want fixpoint", len(again))
		}
	})
}
