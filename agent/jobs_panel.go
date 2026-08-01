package agent

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/appwire"
)

// isOutputNotExistErr reports whether err means the job's output file does
// not exist, through the jobstore fmt.Errorf %w wrapping. (The raw os.Stat
// results in the historical readers use os.IsNotExist directly; wrapped
// paths need errors.Is.)
func isOutputNotExistErr(err error) bool { return errors.Is(err, os.ErrNotExist) }

// JobSummary and JobOutputTail are wire payloads, so their definitions live
// in appwire beside the serf/jobs/list and serf/jobs/output shapes (and
// under that package's camelCase tag carve-out). The aliases keep this
// package's producers named in domain terms.
type (
	JobSummary    = appwire.JobSummary
	JobOutputTail = appwire.JobOutputTail
)

const (
	jobOutputTailDefaultBytes = 4096
	jobOutputTailMaxBytes     = 65536
)

func clampJobTailBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return jobOutputTailDefaultBytes
	}
	if maxBytes > jobOutputTailMaxBytes {
		return jobOutputTailMaxBytes
	}
	return maxBytes
}

// summarizeJobRecord projects one record. Description is the first non-empty
// of Description, Command, Task. HasOutput means a tail read is worth
// attempting: an output path is recorded or bytes were counted.
func summarizeJobRecord(rec *jobstore.JobRecord) JobSummary {
	if rec == nil {
		return JobSummary{}
	}
	desc := rec.Description
	if desc == "" {
		desc = rec.Command
	}
	if desc == "" {
		desc = rec.Task
	}
	s := JobSummary{
		JobID:       rec.JobID,
		Type:        string(rec.Type),
		Status:      string(rec.Status),
		Reason:      rec.Reason,
		Description: desc,
		Command:     rec.Command,
		Task:        rec.Task,
		Background:  rec.Background,
		StartedAt:   rec.StartedAt.UTC().Format(time.RFC3339),
		ExitCode:    rec.ExitCode,
		OutputBytes: rec.OutputBytes,
		HasOutput:   rec.OutputPath != "" || rec.OutputBytes > 0,
	}
	if rec.EndedAt != nil {
		s.EndedAt = rec.EndedAt.UTC().Format(time.RFC3339)
	}
	return s
}

func summarizeJobRecords(ordered []*jobstore.JobRecord) []JobSummary {
	out := make([]JobSummary, 0, len(ordered))
	for _, rec := range ordered {
		if rec == nil {
			continue
		}
		out = append(out, summarizeJobRecord(rec))
	}
	return out
}

// JobSummaries is the live-daemon serf/jobs/list payload: every job in the
// session's durable store, in append order, with the manager's live records
// laid over the fold (liveJobRecords, the same overlay listWithError applies).
// A running job's live state is not all in the log — Background is live-only
// and OutputBytes is stamped durably only at terminal — so the fold alone
// would report every job foreground and every running job silent. A nil
// jobManager (a session that never started job infrastructure) yields an
// empty, non-nil slice, so the wire carries [] rather than null.
//
// A store that cannot be read is an ERROR, never an empty list. "No jobs
// ran" and "I can't tell you what ran" are different answers, and only one
// of them is reassuring; a corrupt jobs.jsonl reported as the first would
// reach the panel as "No jobs yet". LoadSessionJobList, the past-session
// reader for this same payload, has always surfaced it.
func (s *Session) JobSummaries() ([]JobSummary, error) {
	if s == nil || s.jobManager == nil {
		return []JobSummary{}, nil
	}
	ordered, err := s.jobManager.store.LoadOrdered()
	if err != nil {
		return nil, err
	}
	live := s.jobManager.liveJobRecords()
	for i, rec := range ordered {
		if rec == nil {
			continue
		}
		if liveRec, ok := live[rec.JobID]; ok {
			ordered[i] = liveRec
		}
	}
	return summarizeJobRecords(ordered), nil
}

// JobOutputTail is the live-daemon serf/jobs/output payload. found=false
// means no job with that id exists; a found job with no output file yet is
// an empty tail, not an error.
func (s *Session) JobOutputTail(jobID string, maxBytes int64) (JobOutputTail, bool, error) {
	if s == nil || s.jobManager == nil {
		return JobOutputTail{}, false, nil
	}
	content, total, truncated, err := s.jobManager.readOutput(jobID, int(clampJobTailBytes(maxBytes)))
	if err != nil {
		if isJobNotFoundErr(err) {
			return JobOutputTail{}, false, nil
		}
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	return jobOutputTailFrom(content, total, truncated), true, nil
}

func jobOutputTailFrom(content string, total int64, truncated bool) JobOutputTail {
	retainedStart := max(total-int64(len(content)), 0)
	return JobOutputTail{Tail: content, TotalBytes: total, RetainedStart: retainedStart, Truncated: truncated}
}

// LoadSessionJobList reads one local session's durable jobs.jsonl and
// returns every job in append order, projected for the webui jobs panel. It
// is read-only: a session with no jobs.jsonl yields an empty slice and
// creates no file.
func LoadSessionJobList(stateDir, sessionID string) ([]JobSummary, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return []JobSummary{}, nil
		}
		return nil, err
	}
	store, err := historicalJobsOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()
	ordered, err := store.LoadOrdered()
	if err != nil {
		return nil, err
	}
	return summarizeJobRecords(ordered), nil
}

// LoadSessionJobOutputTail reads one local session's durable jobs.jsonl and
// returns the tail of one job's output file, for the hub's past-session
// fallback. It is read-only. found=false means no job with that id exists;
// a found job with no output file yet is an empty tail, not an error.
func LoadSessionJobOutputTail(stateDir, sessionID, jobID string, maxBytes int64) (JobOutputTail, bool, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return JobOutputTail{}, false, nil
		}
		return JobOutputTail{}, false, err
	}
	store, err := historicalJobsOpen(path)
	if err != nil {
		return JobOutputTail{}, false, err
	}
	defer func() { _ = store.Close() }()
	recs, err := store.Load()
	if err != nil {
		return JobOutputTail{}, false, err
	}
	rec := recs[jobID]
	if rec == nil {
		return JobOutputTail{}, false, nil
	}
	outPath := rec.OutputPath
	if outPath == "" {
		outPath = filepath.Join(jobsDir(stateDir, sessionID), "jobs", jobID+".log")
	}
	validatedTotal, _, err := validatedOutputStatsForRecord(outPath, rec)
	if err != nil {
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	content, total, truncated, err := tailOutputFile(outPath, int(clampJobTailBytes(maxBytes)), validatedTotal)
	if err != nil {
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	return jobOutputTailFrom(content, total, truncated), true, nil
}
