package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
)

// appendGrantLog opens (creating) the jobstore for session sessID under stateDir
// and appends a watched delegate job (resolving to workerRef) plus a
// watch-read-grant from observerSessID on that job — the durable on-disk shape
// job_watch leaves in the WATCHING session's jobs.jsonl.
func appendGrantLog(t *testing.T, stateDir, sessID, watchedJobID, workerRef, observerSessID string) {
	t.Helper()
	dir := jobsDir(stateDir, sessID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close jobstore: %v", err)
		}
	}()
	now := time.Now().UTC()
	for _, e := range []jobstore.Event{
		{Kind: jobstore.EventJobStarted, TS: now, JobID: watchedJobID, Type: jobstore.JobDelegate, OwnerSessionID: sessID, StartedAt: &now},
		{Kind: jobstore.EventJobSessionAssigned, TS: now, JobID: watchedJobID, TranscriptRef: workerRef},
		{Kind: jobstore.EventWatchReadGrant, TS: now, JobID: watchedJobID, ObserverSessionID: observerSessID},
	} {
		if err := store.Append(e); err != nil {
			t.Fatalf("append %s: %v", e.Kind, err)
		}
	}
}

// A watch-read-grant on disk reverse-resolves the watched job to its worker
// session, mapping worker -> observer from the durable log alone.
func TestLoadSessionObserverGrants_ResolvesWorkerToObserver(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const workerID = "02wMz5Txv1C3Hut0M8GCeB"
	const observerID = "02wMz5Txv2enqVTitaig6F"
	appendGrantLog(t, stateDir, "PARENT", "job_watched", encodeRef("", workerID), observerID)

	got, err := LoadSessionObserverGrants(stateDir, "PARENT")
	if err != nil {
		t.Fatalf("LoadSessionObserverGrants: %v", err)
	}
	if obs := got[workerID]; len(obs) != 1 || obs[0] != observerID {
		t.Fatalf("got[%s] = %v, want [%s]", workerID, obs, observerID)
	}
}

func TestLoadSessionObserverGrants_SkipsInvalidLocalSessionIDs(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const validWorker = "02wMz5Txv1C3Hut0M8GCeB"
	const validObserver = "02wMz5Txv2enqVTitaig6F"
	appendGrantLog(t, stateDir, "PARENT", "job_bad_worker", encodeRef("", "01LEGACYWORKER"), validObserver)
	appendGrantLog(t, stateDir, "PARENT", "job_bad_observer", encodeRef("", validWorker), "01LEGACYOBSERVER")
	appendGrantLog(t, stateDir, "PARENT", "job_valid", encodeRef("", validWorker), validObserver)

	got, err := LoadSessionObserverGrants(stateDir, "PARENT")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[validWorker]) != 1 || got[validWorker][0] != validObserver {
		t.Fatalf("grants=%v, want only valid local worker/observer IDs", got)
	}
}

// A session with no jobs.jsonl returns an empty map and creates no file.
func TestLoadSessionObserverGrants_MissingLogIsEmptyNoCreate(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	got, err := LoadSessionObserverGrants(stateDir, "NOLOG")
	if err != nil {
		t.Fatalf("LoadSessionObserverGrants: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing log must yield no grants; got %v", got)
	}
	if _, err := os.Stat(filepath.Join(jobsDir(stateDir, "NOLOG"), "jobs.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("must not create jobs.jsonl; stat err = %v", err)
	}
}

// A grant whose watched job resolves via a proj: (cross-project) transcript ref
// is skipped — the hub cannot read a cross-bucket worker's meta. Mirrors
// watchedWorkerSessionID's projectID != "" -> ok=false handling.
func TestLoadSessionObserverGrants_SkipsCrossProjectRef(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	appendGrantLog(t, stateDir, "PARENT", "job_watched", encodeRef("otherbucket", "WORKER"), "OBSERVER")

	got, err := LoadSessionObserverGrants(stateDir, "PARENT")
	if err != nil {
		t.Fatalf("LoadSessionObserverGrants: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("cross-project ref must be skipped; got %v", got)
	}
}

// A grant for a watched job that is not a delegate (or has no record) is skipped.
func TestLoadSessionObserverGrants_SkipsUnresolvableWatchedJob(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	dir := jobsDir(stateDir, "PARENT")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	// A grant whose JobID has no corresponding job record at all.
	if err := store.Append(jobstore.Event{
		Kind: jobstore.EventWatchReadGrant, TS: time.Now().UTC(),
		JobID: "job_ghost", ObserverSessionID: "OBSERVER",
	}); err != nil {
		t.Fatalf("append grant: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := LoadSessionObserverGrants(stateDir, "PARENT")
	if err != nil {
		t.Fatalf("LoadSessionObserverGrants: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unresolvable watched job must be skipped; got %v", got)
	}
}
