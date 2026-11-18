package agent

import (
	"context"
	"errors"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
)

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

// SessionJobRecordDiagnostics reports what a cold per-session job read could
// not establish about the history behind the record it returned. It is the
// structural counterpart of jobstore.ReadDiagnostics and
// delegatestore.ReadDiagnostics: the same question — is this history whole —
// answered for the retained job read, with the branch the answer came from
// named separately.
type SessionJobRecordDiagnostics struct {
	// TornTail is set when the journal the record was folded from ended in an
	// unterminated, syntactically incomplete append.
	TornTail bool
	// IncompleteLifecycle is set when that journal's events do not describe a
	// whole job (see jobstore.ErrIncompleteLifecycle).
	IncompleteLifecycle bool
	// ForwardedFallback is set when the returned record is a forwarded copy
	// because its owner's journal held no record for it. The record is then the
	// best surviving evidence, not the authoritative one.
	ForwardedFallback bool
	// OwnerUnavailable is set when the owner's journal could not be read at all
	// (absent or unreadable), which is why a forwarded copy was used.
	OwnerUnavailable bool
}

// sessionJobRecordRead is one cold per-session job read: the record the read
// settled on, whether it found one, and what it could not establish.
type sessionJobRecordRead struct {
	record      *jobstore.JobRecord
	found       bool
	diagnostics SessionJobRecordDiagnostics
}

// loadSessionJobRecord reads a job's durable record from the local state
// directory for the hub's past-session fallback. It is read-only.
//
// The record is resolved the way the live recursive path resolves it: the
// owner's record is authoritative, and a forwarded copy in another session's
// journal is only a fallback. A forwarded copy is what a descendant's events
// leave in an ancestor's journal (see forwardEvent), and it can be arbitrarily
// stale — a forwarded job_started with no terminal forward yet is exactly the
// permanently-running row the cold readers used to return. The job ID encodes
// its owner (identifier.JobOwnerSessionID), so the owner's journal is found
// without a tree walk; the same owner-from-ID resolution
// findLocalJobInProject already relies on.
//
// found=false covers a session with no jobs journal, a journal with no such
// job, and an owner journal that holds no record for a job the requested
// session also does not hold.
func loadSessionJobRecord(stateDir, sessionID, jobID string) (sessionJobRecordRead, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return sessionJobRecordRead{}, err
	}
	local, err := readSessionJobJournal(stateDir, sessionID, jobID)
	if err != nil {
		return sessionJobRecordRead{}, err
	}
	owner, ownerErr := identifier.JobOwnerSessionID(jobID)
	if ownerErr != nil || owner == sessionID {
		// An ID that does not encode an owner carries no second branch to
		// consult; the requested session's journal is all the evidence there is.
		return local, nil
	}
	owned, err := readSessionJobJournal(stateDir, owner, jobID)
	if err != nil {
		// The owner branch is unreadable. Its damage must not erase the only
		// surviving evidence, so the forwarded copy stands — and says so.
		local.diagnostics.OwnerUnavailable = true
		if local.found {
			local.diagnostics.ForwardedFallback = !ownedIsOwner(local.record, sessionID)
		}
		return local, nil
	}
	if !owned.found {
		local.diagnostics.OwnerUnavailable = true
		if local.found {
			local.diagnostics.ForwardedFallback = !ownedIsOwner(local.record, sessionID)
		}
		return local, nil
	}
	// Both journals hold a record. Apply the live recursive path's own rule
	// rather than a second, weaker one: an owner record beats a forwarded copy,
	// and between two records of the same authority the one already held wins.
	incomingIsOwner := ownedIsOwner(owned.record, owner)
	if !keepIncomingDescendantRow(local.found, ownedIsOwner(local.record, sessionID), incomingIsOwner) {
		return local, nil
	}
	owned.diagnostics.TornTail = owned.diagnostics.TornTail || local.diagnostics.TornTail
	owned.diagnostics.IncompleteLifecycle = owned.diagnostics.IncompleteLifecycle || local.diagnostics.IncompleteLifecycle
	owned.diagnostics.ForwardedFallback = !incomingIsOwner
	owned.diagnostics.OwnerUnavailable = false
	return owned, nil
}

// ownedIsOwner reports whether rec is the journal session's own record rather
// than a forwarded copy of a descendant's. It is the same test the live walk
// applies (rec.OwnerSessionID == jm.sessionID), with a nil record treated as
// no evidence either way.
func ownedIsOwner(rec *jobstore.JobRecord, sessionID string) bool {
	return rec != nil && rec.OwnerSessionID == sessionID
}

// readSessionJobJournal folds one session's durable jobs.jsonl and reports the
// integrity diagnostics the read produced. A missing or non-regular journal is
// (no record, nil error): absence is a state, not damage. A journal that is
// present but does not fold coherently still returns its best-effort record
// alongside the diagnostic — a caller reading retained history would rather
// show an incomplete row than none.
func readSessionJobJournal(stateDir, sessionID, jobID string) (sessionJobRecordRead, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, valid, err := validateLocalJobJournal(stateDir, path); err != nil {
		return sessionJobRecordRead{}, err
	} else if !valid {
		return sessionJobRecordRead{}, nil
	}
	events, readDiagnostics, err := jobstore.ScanEventsWithDiagnostics(context.Background(), path, jobstore.ScanLimits{})
	if err != nil {
		return sessionJobRecordRead{}, err
	}
	diagnostics := SessionJobRecordDiagnostics{TornTail: readDiagnostics.TornTail}
	recs := jobstore.Fold(events)
	if _, foldErr := jobstore.FoldChecked(events); foldErr != nil &&
		errors.Is(foldErr, jobstore.ErrIncompleteLifecycle) {
		diagnostics.IncompleteLifecycle = true
	}
	rec := recs[jobID]
	if rec == nil {
		return sessionJobRecordRead{diagnostics: diagnostics}, nil
	}
	return sessionJobRecordRead{record: rec, found: true, diagnostics: diagnostics}, nil
}

// LoadSessionJobOutputPage reads a coherent page from a local saved job.
// It is read-only and uses the same selectors and errors as the live producer.
func LoadSessionJobOutputPage(stateDir, sessionID, jobID string, beforeBytes *int64, maxBytes int64) (JobOutputPage, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobOutputPage{}, false, err
	}
	if !read.found {
		return JobOutputPage{}, false, nil
	}
	rec := read.record
	outPath := rec.OutputPath
	if outPath == "" {
		// The log lands beside the journal of the session that ran the job,
		// which after owner-authoritative resolution need not be the session
		// that was asked.
		owner := rec.OwnerSessionID
		if owner == "" {
			owner = sessionID
		}
		outPath = filepath.Join(jobsDir(stateDir, owner), "jobs", jobID+".log")
	}
	page, err := readJobOutputPageForRecord(outPath, rec, beforeBytes, clampJobPageBytes(maxBytes))
	return jobOutputPageResult(page, true, err)
}

// LoadSessionJobRecordWithDiagnostics is the retained job read plus an honest
// account of the history behind it: the record resolved owner-authoritatively
// (see loadSessionJobRecord), whether a record was found at all, and what the
// read could not establish. It is the job-history counterpart of
// delegatestore.ReadEventsWithDiagnostics: a caller that must not present
// damaged history as complete reports the diagnostics alongside the record.
//
// A nil record with a nil error and no diagnostic set means there is no such
// job, not that the history is damaged.
func LoadSessionJobRecordWithDiagnostics(stateDir, sessionID, jobID string) (*jobstore.JobRecord, bool, SessionJobRecordDiagnostics, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return nil, false, SessionJobRecordDiagnostics{}, err
	}
	return read.record, read.found, read.diagnostics, nil
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

// LoadSessionJobGet reads one local session's durable job history and projects
// one job's record, for the hub's past-session fallback. It is read-only.
// found=false means no job with that id exists.
//
// The record is resolved owner-authoritatively (see loadSessionJobRecord): when
// the job ID names an owner other than sessionID, the owner's record wins, and
// sessionID's copy is used only when the owner's journal holds none.
func LoadSessionJobGet(stateDir, sessionID, jobID string) (JobActivityJob, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobActivityJob{}, false, err
	}
	if !read.found {
		return JobActivityJob{}, false, nil
	}
	// The ref names the record's owner, not the session that was asked: after
	// owner-authoritative resolution the two differ exactly when the answer came
	// from the owner's own journal, and naming the asked session there would
	// point a client at a session that does not hold the job.
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: read.record.OwnerSessionID}.String()
	return projectActivityJob(read.record, ownerRef), true, nil
}
