package agent

import (
	"context"
	"errors"
	"os"
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

// sessionJobRecordDiagnostics reports what a cold per-session job read could
// not establish about the history behind the record it returned. It is the
// structural counterpart of jobstore.ReadDiagnostics and
// delegatestore.ReadDiagnostics: the same question — is this history whole —
// answered for the retained job read, with the branch the answer came from
// named separately.
type sessionJobRecordDiagnostics struct {
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
// settled on, the journal that supplied it, whether it found one, and what it
// could not establish.
type sessionJobRecordRead struct {
	record *jobstore.JobRecord
	// sourceSessionID is the session whose journal supplied record. It is the
	// routing identity for a record that names no owner of its own: the caller
	// can open the job under the ref it read from, which is where the bytes
	// actually live.
	sourceSessionID string
	found           bool
	diagnostics     sessionJobRecordDiagnostics
}

// encodedJobOwner names the owner a job ID encodes, or "" when the ID encodes
// none (synthetic IDs from tests and older tooling). The validated identifier is
// the only source of authority: a row's own claim is evidence to be checked
// against it, never the test itself.
func encodedJobOwner(jobID string) string {
	owner, err := identifier.JobOwnerSessionID(jobID)
	if err != nil {
		return ""
	}
	return owner
}

// retainedJobAuthority is the ONE authority rule for retained job history, and
// it takes two facts because either one alone is insufficient. A record is
// authoritative only when the journal it was read from is the owner the job ID
// itself encodes, and the record does not contradict that owner by naming
// another.
//
// The journal is load-bearing: a forwarded copy is the owner's event re-encoded
// in the ancestor's journal, so it carries the owner's own OwnerSessionID and is
// indistinguishable from the original by its fields. What separates them is
// where the bytes live. Ranking on the record's claim alone would call both
// copies authoritative and let the stale one win on arrival order, which is
// precisely the permanently-running row this read exists to stop returning.
//
// A job ID that encodes no owner makes every record non-authoritative, which is
// the honest answer: there is no identifier to rank against.
func retainedJobAuthority(jobID, journalSession string, rec *jobstore.JobRecord) bool {
	if rec == nil {
		return false
	}
	owner := encodedJobOwner(jobID)
	if owner == "" || journalSession != owner {
		return false
	}
	// An empty claim is a legacy row with no owner field: it states nothing to
	// contradict, and the journal already proved the ownership.
	return rec.OwnerSessionID == "" || rec.OwnerSessionID == owner
}

// ownerSessionTombstoned reports whether sessionID's deletion fence is still
// standing. A deletion that failed after writing the tombstone can leave a
// session's work directory in place, so the marker — not the directory's
// existence — is the evidence that its history must no longer be served.
func ownerSessionTombstoned(stateDir, sessionID string) bool {
	_, err := os.Stat(filepath.Join(stateDir, schema.SessionsSubdir, sessionID+schema.SessionMetaTombstoneSuffix))
	return err == nil
}

// loadSessionJobRecord reads a job's durable record from the local state
// directory for the hub's past-session fallback. It is read-only.
//
// SCOPE IS PROVEN BEFORE DATA IS SERVED, AND AUTHORITY COMES FROM THE VALIDATED
// IDENTIFIER. The requested session must itself hold a record of the job — its
// own, or the forwarded copy a descendant's events left in its journal (see
// forwardEvent). Only then may the read follow the owner the job ID encodes
// (identifier.JobOwnerSessionID) to that session's journal, and only after the
// owner's deletion fence has been checked. A job the requested session has no
// record of is not in its scope, however well-formed the job ID looks: without
// that evidence the lookup would serve an unrelated session's data behind the
// ref the caller asked about.
//
// Authority is retainedJobAuthority, the single rule: the record naming the
// encoded owner. A forwarded copy — which is what an ancestor holds, and which
// can be arbitrarily stale, a forwarded job_started with no terminal forward
// being exactly the permanently-running row the cold readers used to return —
// is fallback evidence only, returned and flagged when no authoritative record
// is available.
//
// found=false covers a session with no jobs journal, a journal with no record
// of the job, and an owner whose deletion fence is standing.
func loadSessionJobRecord(stateDir, sessionID, jobID string) (sessionJobRecordRead, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return sessionJobRecordRead{}, err
	}
	local, err := readSessionJobJournal(stateDir, sessionID, jobID)
	if err != nil {
		return sessionJobRecordRead{}, err
	}
	// Scope proof: no record here means this session has no claim on the job,
	// so there is nothing to resolve the encoded owner AGAINST.
	if !local.found {
		return local, nil
	}
	owner := encodedJobOwner(jobID)
	if owner == "" || owner == sessionID {
		// An ID that encodes no owner has no second branch to consult; an ID
		// naming this session is already the authoritative branch.
		return local, nil
	}
	if ownerSessionTombstoned(stateDir, owner) {
		// The owner's deletion fence stands. Its journal and log are not
		// servable, and a forwarded copy of a deleted session's job is not a
		// substitute for the owner's record either: the job was deleted with
		// its owner.
		return sessionJobRecordRead{}, nil
	}
	owned, ownerErr := readSessionJobJournal(stateDir, owner, jobID)
	if ownerErr == nil && owned.found {
		// Both journals hold a record: apply the live recursive path's own
		// precedence (jobs_nested.go's keepIncomingDescendantRow) to the authority
		// this read computed, never to what a row claims about itself.
		existingIsOwner := retainedJobAuthority(jobID, local.sourceSessionID, local.record)
		incomingIsOwner := retainedJobAuthority(jobID, owned.sourceSessionID, owned.record)
		if keepIncomingDescendantRow(local.found, existingIsOwner, incomingIsOwner) {
			owned.diagnostics.mergeIntegrity(local.diagnostics)
			owned.diagnostics.ForwardedFallback = !incomingIsOwner
			owned.diagnostics.OwnerUnavailable = false
			return owned, nil
		}
		// The copy already held outranks the owner journal's. It is still the
		// best evidence available, so it keeps any integrity signal the owner
		// branch carried rather than losing the damage report with the race.
		local.diagnostics.mergeIntegrity(owned.diagnostics)
		if !existingIsOwner {
			local.diagnostics.ForwardedFallback = true
		}
		return local, nil
	}
	// The owner branch is unreadable or holds nothing. Its absence must not erase
	// the only surviving evidence, so the forwarded copy stands — and says it is
	// a fallback rather than passing itself off as the owner's record.
	local.diagnostics.OwnerUnavailable = true
	if !retainedJobAuthority(jobID, local.sourceSessionID, local.record) {
		local.diagnostics.ForwardedFallback = true
	}
	return local, nil
}

// mergeIntegrity folds another read's damage report into d. Diagnostics are not
// a verdict about which branch won: either branch being torn or incoherent is
// something the caller must still know.
func (d *sessionJobRecordDiagnostics) mergeIntegrity(other sessionJobRecordDiagnostics) {
	d.TornTail = d.TornTail || other.TornTail
	d.IncompleteLifecycle = d.IncompleteLifecycle || other.IncompleteLifecycle
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
	diagnostics := sessionJobRecordDiagnostics{TornTail: readDiagnostics.TornTail}
	recs, foldErr := jobstore.FoldChecked(events)
	if foldErr != nil && errors.Is(foldErr, jobstore.ErrIncompleteLifecycle) {
		diagnostics.IncompleteLifecycle = true
	}
	rec := recs[jobID]
	if rec == nil {
		return sessionJobRecordRead{diagnostics: diagnostics}, nil
	}
	return sessionJobRecordRead{record: rec, sourceSessionID: sessionID, found: true, diagnostics: diagnostics}, nil
}

// routingJobSession names the session a client should open a read under: the
// owner the record names when it names one, else the journal that supplied the
// record. A retained legacy start may carry no OwnerSessionID at all, and an
// empty ref is one no client can resolve, while the command, terminal status
// and log all live beside the journal that supplied the record.
func routingJobSession(read sessionJobRecordRead) string {
	if read.record != nil && read.record.OwnerSessionID != "" {
		return read.record.OwnerSessionID
	}
	return read.sourceSessionID
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
		// that was asked, and need not be named by the record itself.
		outPath = filepath.Join(jobsDir(stateDir, routingJobSession(read)), "jobs", jobID+".log")
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
	// point a client at a session that does not hold the job. A record naming no
	// owner is routed to the journal that supplied it.
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: routingJobSession(read)}.String()
	return projectActivityJob(read.record, ownerRef), true, nil
}
