package agent

import (
	"errors"
	"os"
	"path/filepath"

	"primeradiant.com/serf/appwire"
)

// isOutputNotExistErr reports whether err means the job's output file does
// not exist, through the jobstore fmt.Errorf %w wrapping. (The raw os.Stat
// results in the historical readers use os.IsNotExist directly; wrapped
// paths need errors.Is.)
func isOutputNotExistErr(err error) bool { return errors.Is(err, os.ErrNotExist) }

// JobOutputTail is a wire payload, so its definition lives in appwire beside
// the serf/jobs/output shape (and under that package's camelCase tag
// carve-out). The alias keeps this package's producer named in domain terms.
type JobOutputTail = appwire.JobOutputTail

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
