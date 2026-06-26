package agent

import (
	"os"
	"path/filepath"
	"sort"

	"primeradiant.com/serf/agent/internal/jobstore"
)

type HistoricalJobRecord struct {
	JobID            string
	Type             string
	Status           string
	Reason           string
	DelegateID       string
	Task             string
	TranscriptRef    string
	OriginTurnID     string
	OriginToolCallID string
	OriginItemID     string
	OutputBytes      int64
}

// LoadSessionHistoricalJobRecords reads one local session's durable jobs.jsonl and
// returns the folded job records needed by cold UI projections. It is read-only:
// a session with no jobs.jsonl yields an empty map and creates no file.
func LoadSessionHistoricalJobRecords(stateDir, sessionID string) (map[string]HistoricalJobRecord, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]HistoricalJobRecord{}, nil
		}
		return nil, err
	}

	store, err := jobstore.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]HistoricalJobRecord, len(records))
	for jobID, rec := range records {
		if rec == nil {
			continue
		}
		out[jobID] = HistoricalJobRecord{
			JobID:            rec.JobID,
			Type:             string(rec.Type),
			Status:           string(rec.Status),
			Reason:           rec.Reason,
			DelegateID:       rec.DelegateID,
			Task:             rec.Task,
			TranscriptRef:    rec.TranscriptRef,
			OriginTurnID:     rec.OriginTurnID,
			OriginToolCallID: rec.OriginToolCallID,
			OriginItemID:     rec.OriginItemID,
			OutputBytes:      rec.OutputBytes,
		}
	}
	return out, nil
}

// LoadSessionObserverGrants reads one local session's durable jobs.jsonl and
// reconstructs the worker→observers relationship the session's watch-read-grants
// encode: for each EventWatchReadGrant, the watched job's transcript ref names
// the WORKER session and ObserverSessionID names the OBSERVER. The result maps
// each worker session id to the observer session ids granted read on it.
//
// This is the historical, on-disk source for observer auto-open. It does not
// depend on the forward SessionMeta.ObservedBy stamp (empty on existing data):
// every grant ever minted is durable here, keyed by the watching session whose
// log this is. The hub folds this over every local session during its
// past-index rebuild to invert the observer-keyed grant table into the
// worker-keyed answer the workspace needs.
//
// Read-only: a session with no jobs.jsonl yields an empty map and creates no
// file. Grants whose watched worker cannot be resolved to a local same-bucket
// session — no job record, not a delegate, a cross-project (proj:) ref, or an
// undecodable ref — are skipped, mirroring watchedWorkerSessionID's ok=false.
func LoadSessionObserverGrants(stateDir, sessionID string) (map[string][]string, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, err
	}

	store, err := jobstore.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	grants, err := store.LoadGrants()
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		return map[string][]string{}, nil
	}
	records, err := store.Load()
	if err != nil {
		return nil, err
	}

	// Invert observer→watchedJobs into worker→observers, deduped.
	workerObservers := make(map[string]map[string]bool)
	for observerSessionID, watchedJobs := range grants {
		for watchedJobID := range watchedJobs {
			workerSessionID, ok := workerSessionForWatchedJob(records, watchedJobID)
			if !ok {
				continue
			}
			obs := workerObservers[workerSessionID]
			if obs == nil {
				obs = make(map[string]bool)
				workerObservers[workerSessionID] = obs
			}
			obs[observerSessionID] = true
		}
	}

	out := make(map[string][]string, len(workerObservers))
	for worker, obs := range workerObservers {
		ids := make([]string, 0, len(obs))
		for id := range obs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out[worker] = ids
	}
	return out, nil
}

// workerSessionForWatchedJob resolves a watched job record to the local worker
// session id its transcript ref names, mirroring watchedWorkerSessionID: ok is
// false when the job is unknown, not a delegate, names a cross-project (proj:)
// worker the hub cannot read, or carries an undecodable ref.
func workerSessionForWatchedJob(records map[string]*jobstore.JobRecord, watchedJobID string) (string, bool) {
	rec := records[watchedJobID]
	if rec == nil || rec.Type != jobstore.JobDelegate {
		return "", false
	}
	bucketHash, childID, err := decodeRef(rec.TranscriptRef)
	if err != nil || bucketHash != "" {
		return "", false
	}
	return childID, true
}
