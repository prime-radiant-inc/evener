package agent

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/jobstore"
)

type historicalJobStore interface {
	Close() error
	Load() (map[string]*jobstore.JobRecord, error)
	LoadOrdered() ([]*jobstore.JobRecord, error)
}

var (
	historicalJobsStat = os.Stat
	historicalJobsOpen = func(path string) (historicalJobStore, error) { return jobstore.Open(path) }
)

// HistoricalJobRecord is a flattened, read-only snapshot of one job as it was
// persisted in a session's durable jobs.jsonl — the origin coordinates, the
// delegate/task it ran, and its terminal status — for tooling that inspects
// past sessions without replaying their event streams.
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
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]HistoricalJobRecord{}, nil
		}
		return nil, err
	}

	store, err := historicalJobsOpen(path)
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
