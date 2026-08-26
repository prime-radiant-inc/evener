package jobstore

import (
	"testing"
	"time"
)

func TestMergeRecordsOwnerAuthoritativeAndForwardedFallback(t *testing.T) {
	owner := &JobRecord{JobID: "j1", OwnerSessionID: "owner", Type: JobShell, Status: StatusCompleted, StartedAt: mustTime("2026-01-01T00:00:00Z")}
	forwarded := &JobRecord{JobID: "j1", OwnerSessionID: "owner", Type: JobShell, Status: StatusRunning}
	fallback := &JobRecord{JobID: "j2", OwnerSessionID: "gone", Type: JobShell, Status: StatusRunning}
	got, d := MergeRecords([]RecordSource{
		{SessionID: "root", Available: true, Records: map[string]*JobRecord{"j1": forwarded, "j2": fallback}},
		{SessionID: "owner", Available: true, Records: map[string]*JobRecord{"j1": owner}},
	})
	if got["j1"] != owner || got["j1"].Status != StatusCompleted {
		t.Fatalf("owner record not authoritative: %+v", got["j1"])
	}
	if got["j2"] != fallback || !d.Incomplete {
		t.Fatalf("forwarded fallback lost or not incomplete: got=%+v diagnostics=%+v", got["j2"], d)
	}
	if len(d.Mismatches) == 0 {
		t.Fatal("owner/forwarded mismatch was not diagnosed")
	}
}

func TestMergeRecordsRejectsInvalidOwnerIdentityAndOrphanFinish(t *testing.T) {
	bad := &JobRecord{JobID: "j1", OwnerSessionID: "other", Type: JobShell, Status: StatusRunning}
	orphan := Event{Kind: EventJobFinished, JobID: "j2", Status: StatusCompleted}
	badStart := Event{Kind: EventJobStarted, JobID: "j3", OwnerSessionID: "other", Type: JobShell}
	recs, d := MergeRecords([]RecordSource{{SessionID: "owner", Available: true, Records: map[string]*JobRecord{"j1": bad}, Events: []Event{orphan, badStart}}})
	if recs["j1"] != bad {
		t.Fatal("unavailable owner fallback was rejected")
	}
	if len(d.InvalidOwners) == 0 || len(d.LifecycleErrors) == 0 {
		t.Fatalf("missing integrity diagnostics: %+v", d)
	}
}

func mustTime(s string) (t time.Time) { t, _ = time.Parse(time.RFC3339, s); return }
