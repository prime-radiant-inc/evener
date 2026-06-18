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
func TestPastIndex_ObserversOf_FromGrantLog(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "p")
	writeMeta(t, proj, schema.SessionMeta{ID: "PARENT", UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: "WORKER", IsSubagent: true, ParentSessionID: "PARENT", UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: "OBSERVER", IsSubagent: true, ParentSessionID: "PARENT", UpdatedAt: time.Now()})
	// The grant + watched-job record live in the WATCHING (parent) session's log.
	writeGrantLog(t, proj, "PARENT", "job_watched", "local:WORKER", "OBSERVER")

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.ObserversOf("WORKER")
	if len(got) != 1 || got[0] != "OBSERVER" {
		t.Fatalf("ObserversOf(WORKER) = %v, want [OBSERVER]", got)
	}
}

// A worker with no grants anywhere on disk has no observers.
func TestPastIndex_ObserversOf_NoneWhenUngranted(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "p")
	writeMeta(t, proj, schema.SessionMeta{ID: "WORKER", UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := idx.ObserversOf("WORKER"); len(got) != 0 {
		t.Fatalf("ObserversOf(WORKER) = %v, want none", got)
	}
}

// A grant whose watched job resolves to a worker via a proj: (cross-project)
// transcript ref is skipped — the hub can only auto-open same-bucket local
// sessions, mirroring the stamp's ok=false handling.
func TestPastIndex_ObserversOf_SkipsCrossProjectRef(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "p")
	writeMeta(t, proj, schema.SessionMeta{ID: "PARENT", UpdatedAt: time.Now()})
	writeGrantLog(t, proj, "PARENT", "job_watched", "proj:otherbucket:WORKER", "OBSERVER")

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if got := idx.ObserversOf("WORKER"); len(got) != 0 {
		t.Fatalf("cross-project ref must be skipped; got %v", got)
	}
}

// Two observers watching the same worker (grants in distinct watching sessions)
// both surface, deduped.
func TestPastIndex_ObserversOf_MultipleObservers(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "p")
	writeMeta(t, proj, schema.SessionMeta{ID: "P1", UpdatedAt: time.Now()})
	writeMeta(t, proj, schema.SessionMeta{ID: "P2", UpdatedAt: time.Now()})
	writeGrantLog(t, proj, "P1", "job_w1", "local:WORKER", "OBS1")
	writeGrantLog(t, proj, "P2", "job_w2", "local:WORKER", "OBS2")

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got := idx.ObserversOf("WORKER")
	want := map[string]bool{"OBS1": true, "OBS2": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] || got[0] == got[1] {
		t.Fatalf("ObserversOf(WORKER) = %v, want OBS1+OBS2", got)
	}
}

// Rebuild does not create a jobs.jsonl in session dirs that lack one (read-only
// over existing data) — opening a jobstore with O_CREATE would litter empty logs.
func TestPastIndex_ObserversOf_DoesNotCreateJobsLog(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "p")
	writeMeta(t, proj, schema.SessionMeta{ID: "WORKER", UpdatedAt: time.Now()})

	idx := NewPastIndex(filepath.Join(root, "projects", "*"))
	if err := idx.Rebuild(); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(proj, "sessions", "WORKER", "jobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("rebuild must not create jobs.jsonl; stat err = %v", err)
	}
}
