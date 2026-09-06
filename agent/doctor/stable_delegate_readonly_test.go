package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
)

func TestDoctorStableDelegateReportsLegacyStateAndWatchFailures(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	jobsPath := filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")
	startedAt := time.Unix(100, 0).UTC()
	legacyID := "job_legacy_delegate"
	writeJobsEvents(t, jobsPath, []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: legacyID, Type: jobstore.JobType("delegate"), OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &startedAt},
		{Kind: jobstore.EventWatchRegistered, WatchID: "watch_legacy_delegate", Watch: &jobstore.WatchEvent{
			Generation: "wg_legacy", OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: legacyID, ConfigHash: "legacy-config",
		}},
	})

	jobs, err := Jobs(base, sidA, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	watches, err := Watches(base, sidA, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	wire, err := json.Marshal(struct {
		Jobs    JobReport
		Watches WatchReport
	}{jobs, watches})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"legacy_delegate_state", "legacy_delegate_watch_state"} {
		if !bytes.Contains(wire, []byte(code)) {
			t.Errorf("doctor omitted fail-closed diagnostic %q: %s", code, wire)
		}
	}
	if bytes.Contains(wire, []byte("migrated")) {
		t.Fatalf("doctor claimed legacy migration: %s", wire)
	}
}

func TestDoctorStableDelegatePreservesShellAndWatchDiagnostics(t *testing.T) {
	base, sid, _, _ := stableDoctorFixture(t)
	jobs, err := Jobs(base, sid, JobOpts{})
	if err != nil {
		t.Fatalf("Jobs: %v", err)
	}
	watches, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	wire, err := json.Marshal(struct {
		Jobs    JobReport
		Watches WatchReport
	}{jobs, watches})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job_doctor_shell", "dlg_doctor_stable", "watch_doctor", "output_match:ready"} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("doctor projection dropped %q: %s", want, wire)
		}
	}
}

func stableDoctorFixture(t *testing.T) (base, sid, jobsPath, delegatesPath string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)
	writeSession(t, bucket, sidB)
	jobsPath = filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	startedAt := time.Unix(100, 0).UTC()
	writeJobsEvents(t, jobsPath, []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_doctor_shell", Type: jobstore.JobShell, Command: "make verify", OwnerSessionID: sid, VisibleToSession: sid, StartedAt: &startedAt},
		{Kind: jobstore.EventWatchRegistered, WatchID: "watch_doctor", Watch: &jobstore.WatchEvent{
			Generation: "wg_doctor", OwnerSessionID: sid, VisibleSessionID: sid,
			Target: "job_doctor_shell", Condition: "output_match:ready", ConfigHash: "doctor-config",
		}},
	})
	delegatesPath = filepath.Join(bucket, "sessions", sid, "delegates.jsonl")
	store, err := delegatestore.Open(delegatesPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := delegatestore.Descriptor{
		ChildSessionID: sidB, TranscriptRef: "proj:" + hash1 + ":" + sidB,
		OwnerSessionID: sid, VisibleSessionID: sid, Task: "inspect doctor state",
		Description: "stable forensic row", AgentType: "explorer",
		ResolvedModel: "gpt-5.2", ToolNameCeiling: []string{"communicate"}, Resumable: true,
	}
	_, _, err = store.AppendBatch(make(delegatestore.State), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_doctor_stable", Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		{Kind: delegatestore.EventDelegateRunStarted, DelegateID: "dlg_doctor_stable", RunStarted: &delegatestore.RunStarted{
			Generation: 1, Trigger: delegatestore.TriggerInitial, StartedAt: startedAt,
		}},
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{jobsPath, delegatesPath} {
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		fixed := time.Unix(1234, 0).UTC()
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatal(err)
		}
	}
	return base, sid, jobsPath, delegatesPath
}
