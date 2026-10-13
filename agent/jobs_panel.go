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

// JobOutputPage is a wire payload, so its definition lives in appwire beside
// the evener/jobs/output shape (and under that package's camelCase tag
// carve-out). The alias keeps this package's producer named in domain terms.
type JobOutputPage = appwire.JobOutputPage

// JobActivityJob is the activity-tree job node, aliased here for the same
// reason JobOutputPage is: the wire shape lives in appwire, and evener/jobs/get
// returns exactly it.
type JobActivityJob = appwire.JobActivityJob

const (
	jobOutputPageDefaultBytes = 4096
	jobOutputPageMaxBytes     = 65536
)

func clampJobPageBytes(maxBytes int64) int {
	if maxBytes <= 0 {
		return jobOutputPageDefaultBytes
	}
	if maxBytes > jobOutputPageMaxBytes {
		return jobOutputPageMaxBytes
	}
	return int(maxBytes)
}

// JobOutputPage reads lossless output from the live owner or its durable log.
// found=false means the job does not exist; unreadable output is an error.
func (s *Session) JobOutputPage(jobID string, beforeBytes *int64, maxBytes int64) (JobOutputPage, bool, error) {
	if s == nil || s.jobManager == nil {
		return JobOutputPage{}, false, nil
	}
	page, found, err := s.jobManager.readOutputPage(jobID, beforeBytes, clampJobPageBytes(maxBytes))
	return jobOutputPageResult(page, found, err)
}

func jobOutputPageResult(page jobstore.OutputWindowSnapshot, found bool, err error) (JobOutputPage, bool, error) {
	if err != nil {
		switch {
		case errors.Is(err, jobstore.ErrOutputPruned):
			return JobOutputPage{}, found, appwire.JobOutputPruned(page.RetainedStart, page.TotalBytes)
		case errors.Is(err, jobstore.ErrInvalidOffset):
			return JobOutputPage{}, found, appwire.InvalidParams("invalid job output beforeBytes")
		default:
			return JobOutputPage{}, found, appwire.Unavailable(err.Error())
		}
	}
	if !found {
		return JobOutputPage{}, false, nil
	}
	encoding, data := encodeRawOutputBytes(page.Content)
	return JobOutputPage{
		OffsetBytes:        page.Start,
		BytesReturned:      int64(len(page.Content)),
		TotalBytes:         page.TotalBytes,
		RetainedStartBytes: page.RetainedStart,
		Encoding:           encoding,
		Data:               data,
	}, true, nil
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

// LoadSessionJobOutputPage reads a coherent page from a local saved job.
// It is read-only and uses the same selectors and errors as the live producer.
func LoadSessionJobOutputPage(stateDir, sessionID, jobID string, beforeBytes *int64, maxBytes int64) (JobOutputPage, bool, error) {
	rec, found, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobOutputPage{}, false, err
	}
	if !found {
		return JobOutputPage{}, false, nil
	}
	outPath := rec.OutputPath
	if outPath == "" {
		outPath = filepath.Join(jobsDir(stateDir, sessionID), "jobs", jobID+".log")
	}
	page, err := readJobOutputPageForRecord(outPath, rec, beforeBytes, clampJobPageBytes(maxBytes))
	return jobOutputPageResult(page, true, err)
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
