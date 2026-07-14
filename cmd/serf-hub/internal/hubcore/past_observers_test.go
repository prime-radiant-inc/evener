package hubcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
)

// writeGrantLog writes a session's jobs.jsonl with a watched delegate job
// (transcript ref workerRef) and a watch-read-grant from observerSessID on that
// job. It writes the raw durable on-disk lines — the same shape the agent
// jobstore appends — so the hubcore index test stays free of the internal
// jobstore package while exercising the real reverse-resolution.
func writeGrantLog(t *testing.T, project, sessID, watchedJobID, workerRef, observerSessID string) {
	t.Helper()
	jobsDir := filepath.Join(project, "sessions", sessID)
	if err := os.MkdirAll(jobsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	lines := []string{
		`{"kind":"job_started","seq":1,"ts":"` + ts + `","job_id":"` + watchedJobID + `","type":"delegate","owner_session_id":"` + sessID + `"}`,
		`{"kind":"job_session_assigned","seq":2,"ts":"` + ts + `","job_id":"` + watchedJobID + `","transcript_ref":"` + workerRef + `"}`,
		`{"kind":"watch_read_grant","seq":3,"ts":"` + ts + `","job_id":"` + watchedJobID + `","observer_session_id":"` + observerSessID + `"}`,
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(jobsDir, "jobs.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write jobs.jsonl: %v", err)
	}
}

// A watch-read-grant on disk maps the watched job's worker session to its
// observer session id, even though neither worker nor observer carries the
// forward ObservedBy stamp (the existing-data case: 0/2211 stamped).
func fuzzScenarioPastIndex_ObserversOf_FromGrantLog(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-p-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvBRJC3228LTWod", UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvCu3kdckfnw0Gh", IsSubagent: true, ParentSessionID: "02wMz5TxvBRJC3228LTWod", UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvEMoJEDTDGOTil", IsSubagent: true, ParentSessionID: "02wMz5TxvBRJC3228LTWod", UpdatedAt: time.Now()})
	// The grant + watched-job record live in the WATCHING (parent) session's log.
	writeGrantLog(t, proj, "02wMz5TxvBRJC3228LTWod", "job_watched", "local:02wMz5TxvCu3kdckfnw0Gh", "02wMz5TxvEMoJEDTDGOTil")

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.ObserversOf("02wMz5TxvCu3kdckfnw0Gh")
	if len(got) != 1 || got[0] != "02wMz5TxvEMoJEDTDGOTil" {
		t.Fatalf("ObserversOf(worker) = %v, want observer", got)
	}
}

// A worker with no grants anywhere on disk has no observers.
func fuzzScenarioPastIndex_ObserversOf_NoneWhenUngranted(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-p-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvCu3kdckfnw0Gh", UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := idx.ObserversOf("02wMz5TxvCu3kdckfnw0Gh"); len(got) != 0 {
		t.Fatalf("ObserversOf(worker) = %v, want none", got)
	}
}

// A grant whose watched job resolves to a worker via a proj: (cross-project)
// transcript ref is skipped — the hub can only auto-open same-bucket local
// sessions, mirroring the stamp's ok=false handling.
func fuzzScenarioPastIndex_ObserversOf_SkipsCrossProjectRef(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-p-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvBRJC3228LTWod", UpdatedAt: time.Now()})
	writeGrantLog(t, proj, "02wMz5TxvBRJC3228LTWod", "job_watched", "proj:otherbucket:WORKER", "02wMz5TxvEMoJEDTDGOTil")

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := idx.ObserversOf("02wMz5TxvCu3kdckfnw0Gh"); len(got) != 0 {
		t.Fatalf("cross-project ref must be skipped; got %v", got)
	}
}

// Two observers watching the same worker (grants in distinct watching sessions)
// both surface, deduped.
func fuzzScenarioPastIndex_ObserversOf_MultipleObservers(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-p-0123456789")
	const workerID = "02wMz5TxvCu3kdckfnw0Gh"
	const observer1ID = "02wMz5TxvFpYrooBkiqxAp"
	const observer2ID = "02wMz5TxvHIJQPOuIBJQct"
	writeMeta(t, proj, schema.SessionMeta{ID: observer1ID, UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: observer2ID, UpdatedAt: time.Now()})
	writeGrantLog(t, proj, observer1ID, "job_w1", "local:"+workerID, observer1ID)
	writeGrantLog(t, proj, observer2ID, "job_w2", "local:"+workerID, observer2ID)

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.ObserversOf(workerID)
	want := map[string]bool{observer1ID: true, observer2ID: true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] || got[0] == got[1] {
		t.Fatalf("ObserversOf(worker) = %v, want %s+%s", got, observer1ID, observer2ID)
	}
}

// Rebuild does not create a jobs.jsonl in session dirs that lack one (read-only
// over existing data) — opening a jobstore with O_CREATE would litter empty logs.
func fuzzScenarioPastIndex_ObserversOf_DoesNotCreateJobsLog(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "project-p-0123456789")
	writeMeta(t, proj, schema.SessionMeta{ID: "02wMz5TxvCu3kdckfnw0Gh", UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "sessions", "02wMz5TxvCu3kdckfnw0Gh", "jobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("rebuild must not create jobs.jsonl; stat err = %v", err)
	}
}
