package agent

import (
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
)

var historicalJobsStat = os.Stat

// HistoricalJobRecord is a read-only projection of a retained job.
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

// HistoricalJobDiagnostics reports authority and integrity evidence.
type HistoricalJobDiagnostics struct {
	Incomplete      bool
	Mismatches      []string
	InvalidOwners   []string
	LifecycleErrors []string
	MissingOwners   []string
	TornTails       []string
	CorruptBranches []string
	Compatibility   []string
}

// LoadSessionHistoricalJobRecordsWithDiagnostics loads retained jobs and evidence.
func LoadSessionHistoricalJobRecordsWithDiagnostics(stateDir, sessionID string) (map[string]HistoricalJobRecord, HistoricalJobDiagnostics, error) {
	out, d, err := loadSessionHistoricalJobRecordsWithDiagnostics(stateDir, sessionID)
	return out, HistoricalJobDiagnostics{Incomplete: d.Incomplete, Mismatches: d.Mismatches, InvalidOwners: d.InvalidOwners, LifecycleErrors: d.LifecycleErrors, MissingOwners: d.MissingOwners, TornTails: d.TornTails, CorruptBranches: d.CorruptBranches, Compatibility: d.Compatibility}, err
}

// LoadSessionHistoricalJobRecords loads one session's retained jobs.
func LoadSessionHistoricalJobRecords(stateDir, sessionID string) (map[string]HistoricalJobRecord, error) {
	out, _, err := loadSessionHistoricalJobRecordsWithDiagnostics(stateDir, sessionID)
	return out, err
}

func loadSessionHistoricalJobRecordsWithDiagnostics(stateDir, sessionID string) (map[string]HistoricalJobRecord, jobstore.AuthorityDiagnostics, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return nil, jobstore.AuthorityDiagnostics{}, err
	}
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]HistoricalJobRecord{}, jobstore.AuthorityDiagnostics{}, nil
		}
		return nil, jobstore.AuthorityDiagnostics{}, err
	}
	records, diagnostics, err := loadRetainedJobHistory(stateDir, sessionID)
	if err != nil {
		return nil, diagnostics, err
	}
	out := make(map[string]HistoricalJobRecord, len(records))
	for jobID, rec := range records {
		if rec == nil {
			continue
		}
		out[jobID] = HistoricalJobRecord{JobID: rec.JobID, Type: string(rec.Type), Status: string(rec.Status), Reason: rec.Reason, Task: rec.Task, OriginTurnID: rec.OriginTurnID, OriginToolCallID: rec.OriginToolCallID, OriginItemID: rec.OriginItemID, OutputBytes: rec.OutputBytes}
	}
	return out, diagnostics, nil
}
