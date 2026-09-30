package agent

import (
	"errors"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// isOutputNotExistErr reports whether err means the job's output file does
// not exist, through the jobstore fmt.Errorf %w wrapping. (The raw os.Stat
// results in the historical readers use os.IsNotExist directly; wrapped
// paths need errors.Is.)
func isOutputNotExistErr(err error) bool { return errors.Is(err, os.ErrNotExist) }

// JobOutputTail is a wire payload, so its definition lives in appwire beside
// the evener/jobs/output shape (and under that package's camelCase tag
// carve-out). The alias keeps this package's producer named in domain terms.
type JobOutputTail = appwire.JobOutputTail

// JobActivityJob is the activity-tree job node, aliased here for the same
// reason JobOutputTail is: the wire shape lives in appwire, and evener/jobs/get
// returns exactly it.
type JobActivityJob = appwire.JobActivityJob

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

// JobOutputTail is the live-daemon evener/jobs/output payload. found=false
// means no job with that id exists; a found job with no output file yet is
// an empty tail, not an error. beforeBytes > 0 pages backwards: the window of
// up to maxBytes ending at that lifetime offset, with HasEarlier reporting
// whether a further page exists.
func (s *Session) JobOutputTail(jobID string, beforeBytes, maxBytes int64) (JobOutputTail, bool, error) {
	if s == nil || s.jobManager == nil {
		return JobOutputTail{}, false, nil
	}
	w, err := s.jobManager.readOutputWindow(jobID, beforeBytes, clampJobTailBytes(maxBytes))
	if err != nil {
		if isJobNotFoundErr(err) {
			return JobOutputTail{}, false, nil
		}
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	return jobOutputTailFromWindow(w), true, nil
}

func jobOutputTailFromWindow(w jobOutputWindow) JobOutputTail {
	return JobOutputTail{
		Tail:          w.content,
		TotalBytes:    w.total,
		RetainedStart: w.start,
		Truncated:     w.start > 0 || w.end < w.total,
		HasEarlier:    w.start > w.earliest,
	}
}

// loadSessionJobRecord reads one local session's durable jobs.jsonl and folds
// out one job's record, for the hub's past-session fallback. It is read-only.
// found=false covers both a session with no jobs journal and a journal with no
// such job; the caller proceeds on found alone.
func loadSessionJobRecord(stateDir, sessionID, jobID string) (*jobstore.JobRecord, bool, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return nil, false, err
	}
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, err := historicalJobsStat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	events, err := jobstore.ReadEvents(path)
	if err != nil {
		return nil, false, err
	}
	rec := jobstore.Fold(events)[jobID]
	if rec == nil {
		return nil, false, nil
	}
	return rec, true, nil
}

// LoadSessionJobOutputTail reads one local session's durable jobs.jsonl and
// returns a window of one job's output file, for the hub's past-session
// fallback. It is read-only. found=false means no job with that id exists;
// a found job with no output file yet is an empty tail, not an error.
// beforeBytes has the same paging meaning as Session.JobOutputTail's.
func LoadSessionJobOutputTail(stateDir, sessionID, jobID string, beforeBytes, maxBytes int64) (JobOutputTail, bool, error) {
	rec, found, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobOutputTail{}, false, err
	}
	if !found {
		return JobOutputTail{}, false, nil
	}
	outPath := rec.OutputPath
	if outPath == "" {
		outPath = filepath.Join(jobsDir(stateDir, sessionID), "jobs", jobID+".log")
	}
	validatedTotal, earliest, err := validatedOutputStatsForRecord(outPath, rec)
	if err != nil {
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	content, start, end, err := windowOutputFile(outPath, beforeBytes, clampJobTailBytes(maxBytes), validatedTotal, earliest)
	if err != nil {
		if isOutputNotExistErr(err) {
			return JobOutputTail{}, true, nil
		}
		return JobOutputTail{}, true, err
	}
	return jobOutputTailFromWindow(jobOutputWindow{
		content:  content,
		start:    start,
		end:      end,
		total:    validatedTotal,
		earliest: earliest,
	}), true, nil
}

// JobGet resolves one job's record — the running record when the job is live,
// else the store's folded record — and projects it into the activity-tree job
// shape, including the untruncated command. found=false means no job with that
// id exists.
func (s *Session) JobGet(jobID string) (JobActivityJob, bool, error) {
	if s == nil || s.jobManager == nil {
		return JobActivityJob{}, false, nil
	}
	_, rec, err := s.jobManager.recordForRead(jobID)
	if err != nil {
		return JobActivityJob{}, false, err
	}
	if rec == nil {
		return JobActivityJob{}, false, nil
	}
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: rec.OwnerSessionID}.String()
	return projectActivityJob(rec, ownerRef), true, nil
}

// LoadSessionJobGet reads one local session's durable jobs.jsonl and projects
// one job's record, for the hub's past-session fallback. It is read-only.
// found=false means no job with that id exists.
func LoadSessionJobGet(stateDir, sessionID, jobID string) (JobActivityJob, bool, error) {
	rec, found, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobActivityJob{}, false, err
	}
	if !found {
		return JobActivityJob{}, false, nil
	}
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	return projectActivityJob(rec, ownerRef), true, nil
}
