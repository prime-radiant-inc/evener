package agent

import (
	"context"
	"errors"
	"fmt"
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

// ErrJobScopeUnresolved reports a saved-job read that could not establish the
// job's ABSENCE. The requested session's journal ends in an unterminated append
// and nothing in its intact prefix mentions the job, so the torn line could be
// that job's own record. Absence is a claim about the whole journal, and a scan
// that stopped short of one cannot make it.
//
// It is retryable rather than permanent: the append may complete, and the next
// read then answers normally. Callers must not turn it into "job not found",
// which asserts the job does not exist.
var ErrJobScopeUnresolved = errors.New("agent: job history ends in an unterminated append and does not establish whether this job exists")

// ErrJobStatusUnresolved reports a saved-job read whose selected record states a
// NONTERMINAL status that this journal cannot vouch for, because its scan stopped
// at durable corruption. The decoded prefix proves the job exists and where it
// ran; it does not prove the job is still running, since the finish may be the
// very line that failed to decode.
//
// Returning the prefix's running row with a nil error would assert an end state
// the scan never saw. Reporting unavailable says what is true: the history
// exists and its outcome is unreadable. It is not not-found — the job was found.
var ErrJobStatusUnresolved = errors.New("agent: job history stops at durable corruption before establishing the job's state")

// sessionJobRecordDiagnostics is what a retained job read could not establish
// about the journals it consulted. It rides the read so the precedence decision
// can tell a settled record from a fragment; see stateEstablished.
//
// Both fields are per-JOURNAL facts about evidence this read needed and could
// not get, which is why they merge across every journal consulted rather than
// describing the winner. Neither is a verdict on a record: what a particular
// RECORD could not establish is recordStatusUnconfirmed's question, and the two
// are deliberately separate.
type sessionJobRecordDiagnostics struct {
	// TornTail is set when the journal the record was folded from ended in an
	// unterminated, syntactically incomplete append.
	TornTail bool
	// IncompleteLifecycle is set when that journal's events do not describe a
	// whole job (see jobstore.ErrIncompleteLifecycle).
	IncompleteLifecycle bool
}

// sessionJobRecordRead is one cold per-session job read: the record the read
// settled on, the journal that supplied it, whether it found one, and what it
// could not establish.
type sessionJobRecordRead struct {
	record *jobstore.JobRecord
	// sourceSessionID is the session whose journal supplied record. It is the
	// routing identity for a record that names no owner of its own: the caller
	// can resolve the job's default log under the ref it read from, which is
	// where the bytes actually live.
	sourceSessionID string
	found           bool
	diagnostics     sessionJobRecordDiagnostics
	// scanErr is the durable-corruption error THIS branch's own scan produced,
	// or nil. It stays branch-local on purpose: mergeIntegrity carries the
	// damage report forward, but whether one particular record's state can be
	// stated is a fact about that record's journal alone.
	scanErr error
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

// ownerClaimContradicts reports whether a record's own OwnerSessionID names a
// session the job ID does not encode.
//
// An EMPTY claim states nothing and is the pre-owner legacy shape, and a job ID
// that encodes no owner leaves nothing to contradict. Both are therefore not
// conflicts — the same two exceptions checkJobStart makes, for the same reason.
func ownerClaimContradicts(rec *jobstore.JobRecord) bool {
	if rec == nil || rec.OwnerSessionID == "" {
		return false
	}
	encoded := encodedJobOwner(rec.JobID)
	return encoded != "" && encoded != rec.OwnerSessionID
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

// recordStatusUnconfirmed reports whether THIS record's nonterminal status is
// not established.
//
// TornTail and scanErr are evidence against the record itself: an unterminated
// append or durable corruption leaves the finish unaccounted for, and the
// status states only the prefix that survived.
//
// IncompleteLifecycle is NOT, on its own. FoldCheckedOrdered reports the FIRST
// structural gap anywhere in the journal — including another job's — so using
// the journal's verdict here would mark a healthy running row unconfirmed
// because a neighbour is broken. What makes THIS record a fragment is the gap's
// own shape: a record with a type was folded from a start, and one without a
// type is a notification or finish that never had one.
//
// A status the fold copied from a malformed finish is the other per-record
// shape, and it is the flag rather than a field the event may omit: the finish
// declared no end at all, so the journal has recorded the job's end and the
// record cannot say what it was. `terminal_generation` is NOT the test — a
// writer may omit it, and a finish that omits it while carrying "running" is
// exactly the case that would slip through.
func recordStatusUnconfirmed(rec *jobstore.JobRecord, scanErr error, d sessionJobRecordDiagnostics) bool {
	if rec == nil || rec.Status == "" || rec.Status.IsTerminal() {
		return false
	}
	if scanErr != nil || d.TornTail {
		return true
	}
	// The record's OWN owner claim can be the conflict. Checked before the
	// lifecycle half: a row that names an owner its job ID contradicts was
	// folded from a start that never described this job, so its "running" is a
	// statement about some other session's launch.
	if ownerClaimContradicts(rec) {
		return true
	}
	// A finish that declared no terminal status: the fold recorded the job's end
	// and this record cannot say what that end was.
	if rec.StatusFromMalformedFinish {
		return true
	}
	return d.IncompleteLifecycle && rec.Type == ""
}

// stateEstablished reports whether a read proves the job's settled state.
//
// A decoded terminal event is a complete observation on its own: the first
// terminal write fixes the status, end time and exit code, so damage after it
// can add nothing to that answer.
//
// A NON-TERMINAL record is established only when its own status is established.
// A clean scan of the journal is necessary but not sufficient: the record can
// carry its own reason to doubt the status it states. An owner start with no
// `type` whose journal the fold could not pair with a launch is the case that
// matters — its running status is a fragment, and a fragment must not displace a
// terminal observation however clean the scan that produced it.
//
// Both kinds of scan damage leave the finish unaccounted for, and both are
// therefore incomplete evidence: durable corruption can be the finish itself,
// and a torn tail is an append that landed without its newline — the in-flight
// finish, or one that will never complete. Reading either as "still running"
// would state the job's state without having seen it.
//
// A record with NO STATUS established nothing at all. A journal holding only a
// notification for a job folds into a row for it — the row exists because the
// job was named, not because anyone said what it did — and such a row has no
// status, no end time and no exit code to speak of. A clean scan of a journal
// that never described the job's state is not evidence about that state, so it
// may not displace a record that does state it.
func stateEstablished(read sessionJobRecordRead) bool {
	if read.record == nil {
		return false
	}
	if read.record.Status.IsTerminal() {
		return true
	}
	if read.record.Status == "" {
		return false
	}
	return !recordStatusUnconfirmed(read.record, read.scanErr, read.diagnostics)
}

// statusUnreadable reports whether this read's selected record states a
// NONTERMINAL status that durable corruption left unvouched for, so no journal
// consulted could say how the job ended.
//
// The distinction from recordStatusUnconfirmed matters. That predicate also
// fires for a torn append, a statusless fragment and a contradictory claim —
// states where the job may well still be running, and where the honest answer is
// a row a client keeps watching. Durable corruption is different: the scan
// proved a line is undecodable, the finish is the likeliest candidate, and no
// later read of this journal gets past it. Reporting such a row as confirmed
// running is how a finished job reads as permanently running.
//
// scanErr is the branch-local error on purpose: an intact owner record that
// inherited an ancestor's damage has an established status, and only the record's
// own scan can make its status unreadable.
func statusUnreadable(read sessionJobRecordRead) bool {
	if read.record == nil || read.scanErr == nil {
		return false
	}
	return !stateEstablished(read)
}

// retainedJobReadOutranks reports whether the incoming read replaces the
// existing one for the same job. It is the ONE decision every branch of
// loadSessionJobRecord consults — the encoded-owner branch and the legacy
// corroborated-hint branch — and it composes two rules in a fixed order:
//
//  1. Authority (keepIncomingDescendantRow, the live recursive path's own rule
//     in jobs_nested.go): the record from the owner's journal wins, and between
//     two records of equal authority the one already held is kept.
//  2. Evidence: a record that cannot state the job's state never displaces one
//     that can. The owner's journal is the authority on what the job IS, but a
//     scan that stopped at durable corruption hands back a prefix, and a prefix
//     ending at a running row says nothing about whether the job finished — the
//     finish may be the very line that failed to decode. The competing forwarded
//     copy's terminal observation is then the only settled answer there is.
//
// The invariant both rules serve, in one sentence: a record that is not fully
// established must not outrank one that is.
//
// The two AUTHORITY inputs come from the caller because the identifier is not
// always there to consult: an encoded owner is proved by retainedJobAuthority,
// and a legacy job ID with no owner in it is proved by corroborating the row's
// own claim against that journal. What those facts MEAN once established — and
// how they rank against evidence — is this function, stated once.
//
// Rule 2 gates rule 1's winner rather than competing with it: it can only stop
// an incoming record from DISPLACING a settled one. It never promotes a
// forwarded copy over an owner record that establishes state — an owner terminal
// row always wins — and it never demotes a record on authority grounds.
func retainedJobReadOutranks(existingIsOwner bool, existing sessionJobRecordRead, incomingIsOwner bool, incoming sessionJobRecordRead) bool {
	if !keepIncomingDescendantRow(existing.found, existingIsOwner, incomingIsOwner) {
		// Authority keeps what is already held; nothing displaces it.
		return false
	}
	// Authority hands the row to the incoming read. It may not take it while
	// being unable to say whether the job finished, if what it would displace is
	// a complete terminal observation.
	return stateEstablished(incoming) || !stateEstablished(existing)
}

// preserveLaunchFields fills a record's unset launch fields from the same job's
// scoped copy. An owner journal that holds only a finish describes the job's
// END, and an end alone cannot say what was launched: the command, the type, the
// start time and the description all live in the start event, which in this
// situation only the scoped journal still has. Those are facts, not inferences —
// they come from the same job's record, and the owner's terminal status, exit
// code and diagnostics stay exactly as the owner wrote them.
//
// Only fields the owner left EMPTY are filled. A field the owner's journal set
// is the owner's to state.
//
// A scoped start whose own owner claim the job ID contradicts never described
// this job: it is a forwarded copy of some other session's launch, and its
// command and explicit output path belong to THAT job. Recovering them would
// show a foreign command in the pane and send the output read to a path that is
// not this job's log, where a stale pointer hides the owner's default log. Such
// a start supplies nothing, so its launch fields stay unset and the default log
// under the validated owner answers instead.
//
// The legacy corroborated-hint call site cannot reach the contradiction: that
// branch exists only for a job ID that encodes no owner, so there is no
// identifier for a claim to contradict and the guard is inert by construction
// rather than by a second trust argument.
func preserveLaunchFields(rec, scoped *jobstore.JobRecord) {
	if rec == nil || scoped == nil || rec.JobID != scoped.JobID {
		return
	}
	if ownerClaimContradicts(scoped) {
		return
	}
	if rec.Type == "" {
		rec.Type = scoped.Type
	}
	if rec.Command == "" {
		rec.Command = scoped.Command
	}
	if rec.Task == "" {
		rec.Task = scoped.Task
	}
	if rec.Description == "" {
		rec.Description = scoped.Description
	}
	if rec.Intent == "" {
		rec.Intent = scoped.Intent
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = scoped.StartedAt
	}
	if rec.OutputPath == "" {
		rec.OutputPath = scoped.OutputPath
	}
	if rec.WorkingDir == "" {
		rec.WorkingDir = scoped.WorkingDir
	}
	// Background is a LAUNCH fact for the same reason and from the same source:
	// the start event carries it, a finish never restates it, and a record whose
	// Background came out false would report a background job as foreground. It
	// cannot be "unset by the owner" the way an empty string can, so the test
	// below is `!rec.Background` rather than an ownership question: a
	// finish-only record has nothing to preserve, and a record that already says
	// background keeps saying it.
	if !rec.Background {
		rec.Background = scoped.Background
	}
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
//
// A scan that hits durable corruption returns the events it decoded before the
// damage alongside its error, and the two answers are kept SEPARATE: the decoded
// prefix is still evidence — a forwarded start in it proves this session's scope
// for the job — while the error says the journal past that prefix is unreadable.
// Collapsing them, in either direction, is how a read ends up reporting a
// healthy running job whose finish was the line that failed to decode.
//
// Which errors carry a decoded prefix is a property of the ERROR, not of the
// slice (see jobstore.ScanEventsFrom). This read needs no such distinction: an
// empty prefix simply leaves no record, which the branch below handles, and a
// non-empty one is evidence about this journal whatever the error says.
func readSessionJobJournal(stateDir, sessionID, jobID string) (sessionJobRecordRead, error) {
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	if _, valid, err := validateLocalJobJournal(stateDir, path); err != nil {
		return sessionJobRecordRead{}, err
	} else if !valid {
		return sessionJobRecordRead{}, nil
	}
	events, readDiagnostics, scanErr := jobstore.ScanEventsWithDiagnostics(context.Background(), path, jobstore.ScanLimits{})
	// A torn tail is not an error at all: the scan reached a clean boundary and
	// the unterminated append rides the diagnostic.
	//
	// TornTail and Corrupt are mutually exclusive for one journal: the scan sets
	// a torn tail on the unterminated-final-line break, which returns no error,
	// and reports Corrupt only from the paths that stop before a boundary. An
	// unopenable journal reports neither, because nothing was observed about its
	// bytes — and that difference is why a validation failure must not be
	// reported as damage. Corrupt needs no field here: scanErr carries it, and it
	// is read where it decides a record's confidence (recordStatusUnconfirmed).
	diagnostics := sessionJobRecordDiagnostics{
		TornTail: readDiagnostics.TornTail,
	}
	recs, foldErr := jobstore.FoldCheckedOrdered(events)
	if foldErr != nil && errors.Is(foldErr, jobstore.ErrIncompleteLifecycle) {
		diagnostics.IncompleteLifecycle = true
	}
	var rec *jobstore.JobRecord
	for _, candidate := range recs {
		if candidate.JobID == jobID {
			rec = candidate
			break
		}
	}
	if rec == nil {
		// The prefix holds nothing about this job. If the scan then failed, the
		// journal cannot answer the question at all, so the damage is the answer.
		// The diagnostics ride the read anyway: the caller that asked about this
		// journal is not always the journal's owner, and a competing branch
		// serving a forwarded copy still has to be able to say that the owner's
		// history is damaged rather than merely absent.
		if scanErr != nil {
			return sessionJobRecordRead{diagnostics: diagnostics}, scanErr
		}
		return sessionJobRecordRead{diagnostics: diagnostics}, nil
	}
	// The prefix holds a record: it proves this session's scope for the job. What
	// it may NOT prove is the job's settled state, because the finish can be the
	// very line the scan could not decode. scanErr rides the read so the caller
	// that decides between competing branches can tell the difference; see
	// stateEstablished. A caller with nothing to compare against still gets the
	// best-effort record rather than an error that erases what survived.
	return sessionJobRecordRead{
		record:          rec,
		sourceSessionID: sessionID,
		found:           true,
		diagnostics:     diagnostics,
		scanErr:         scanErr,
	}, nil
}

// hintIsSessionID reports whether a stored owner hint is a session id. It is a
// PREDICATE, not a validation step: a claim that is not a session id states
// nothing this read can corroborate, which is an answer rather than a failure.
func hintIsSessionID(hint string) bool {
	return schema.ValidateSessionID(hint) == nil
}

// loadSessionJobRecord reads a job's durable record from the local state
// directory for the hub's past-session fallback. It is read-only.
//
// SCOPE IS PROVEN BEFORE DATA IS SERVED, AND AUTHORITY COMES FROM THE VALIDATED
// IDENTIFIER. The requested session must itself hold a record of the job — its
// own, or the forwarded copy a descendant's events left in its journal (see
// forwardEvent). Only then may the read follow the owner the job ID encodes
// (identifier.JobOwnerSessionID) to that session's journal. A job the requested
// session has no record of is not in its scope, however well-formed the job ID
// looks: without that evidence the lookup would serve an unrelated session's
// data behind the ref the caller asked about.
//
// Authority is retainedJobAuthority, the single rule: the record naming the
// encoded owner. A forwarded copy — which is what an ancestor holds, and which
// can be arbitrarily stale, a forwarded job_started with no terminal forward
// being exactly the permanently-running row the cold readers used to return —
// is fallback evidence only, returned and flagged when no authoritative record
// is available.
//
// found=false covers a session with no jobs journal, a journal with no record
// of the job, and an owner journal that holds none either.
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
		// Except when the scan could not reach a record boundary: a torn append
		// may BE this job's record, so the intact prefix cannot say the job is
		// absent. "Not found" would assert more than the scan established, and the
		// hub would report the job does not exist. The condition is retryable —
		// the append may complete — so it surfaces as unavailable, not not-found.
		if local.diagnostics.TornTail {
			return sessionJobRecordRead{}, ErrJobScopeUnresolved
		}
		return local, nil
	}
	owner := encodedJobOwner(jobID)
	if owner == "" {
		// A pre-owner job ID encodes nothing to rank against. Rows that old can
		// still carry an owner field, and when they do it is the only pointer to
		// the journal that may hold the finish: consult it as a HINT, and let the
		// owner's journal corroborate before it is believed. Without that, a
		// forwarded start written before owner-encoded IDs keeps showing running
		// while the owner's journal holds the completed job.
		hint := ""
		if local.record != nil {
			hint = local.record.OwnerSessionID
		}
		if hint == "" || hint == sessionID {
			return local, nil
		}
		// The hint comes from a STORED ROW, so it is untrusted input, not an
		// identifier this read has validated: it is a session id read back out of
		// a journal. Validate it before it reaches a filesystem lookup, so a
		// malformed claim cannot name a path this read would never have opened.
		//
		// Asked as a PREDICATE, not as a failure: a malformed claim is not a
		// failure of THIS read. The caller asked about the scoped session, whose
		// record is intact, and a claim that is not a session id cannot
		// corroborate anything, so there is no branch to consult and nothing to
		// retry. The scoped copy stands, and the flag below says why it is
		// fallback evidence rather than the owner's.
		if !hintIsSessionID(hint) {
			return local, nil
		}
		corroborated, hintErr := readSessionJobJournal(stateDir, hint, jobID)
		// Corroboration is not merely "the hinted journal mentions this job": the
		// row it holds must AGREE that the hinted session owns it. A forwarded
		// copy living in a different ancestor also names the job, and with a
		// contradictory owner field it would otherwise be treated as the hinted
		// owner's own record and allowed to replace the scoped finish. An empty
		// owner field is the pre-owner legacy shape and states nothing to
		// contradict.
		contradicts := corroborated.record != nil &&
			corroborated.record.OwnerSessionID != "" &&
			corroborated.record.OwnerSessionID != hint
		if hintErr != nil || !corroborated.found || contradicts {
			// The hinted journal could not corroborate — it was absent, unreadable,
			// held no record of this job, or held one naming a different owner. That
			// is a fact about the HINT, not a failure of this read: the caller asked
			// about the scoped session, whose record is intact, so the scoped copy
			// stands. The hint's damage is not discarded — it rides the diagnostic,
			// including durable corruption that made its journal unreadable — and a
			// claim nothing corroborated is not an owner, so the scoped copy keeps
			// fallback status rather than pretending to be the owner's.
			//
			// A hintErr is a VALIDATION failure (a symlinked prefix, a non-regular
			// leaf, a stat error), so no scan ever ran on that path and no durable
			// corruption was observed. Reporting the torn-tail diagnostic here
			// would tell a caller the history is damaged when the truth is that it
			// could not be opened. Only the diagnostics the journal reader
			// actually returned are merged.
			local.diagnostics.mergeIntegrity(corroborated.diagnostics)
			// A hint that cannot be read is a fact about the HINT, not a failure of
			// this read: the caller asked about the scoped session, whose record is
			// intact and is what this returns.
			return local, nil //nolint:nilerr // an unreadable owner hint is a classified fallback state carried on the diagnostics, not an operational failure
		}
		// A corroborated legacy owner goes through the SAME precedence the encoded
		// branch applies — no second authority test. Corroboration makes the
		// hinted journal the owner, but ownership is not evidence of settled
		// state: its prefix may have stopped at durable corruption short of the
		// finish the scoped copy observed. stateEstablished is what still
		// separates them.
		if retainedJobReadOutranks(false, local, true, corroborated) {
			corroborated.diagnostics.mergeIntegrity(local.diagnostics)
			// The owner's journal is the authority on what the job IS; the scoped
			// journal is the only place the launch facts survive when the owner
			// holds nothing but the finish. Same rule as the encoded branch, one
			// call.
			preserveLaunchFields(corroborated.record, local.record)
			return corroborated, nil
		}
		local.diagnostics.mergeIntegrity(corroborated.diagnostics)
		return local, nil
	}
	if owner == sessionID {
		// An ID naming this session is already the authoritative branch.
		return local, nil
	}
	owned, ownerErr := readSessionJobJournal(stateDir, owner, jobID)
	if ownerErr == nil && owned.found {
		// Both journals hold a record: apply the one precedence rule.
		existingIsOwner := retainedJobAuthority(jobID, local.sourceSessionID, local.record)
		incomingIsOwner := retainedJobAuthority(jobID, owned.sourceSessionID, owned.record)
		if retainedJobReadOutranks(existingIsOwner, local, incomingIsOwner, owned) {
			owned.diagnostics.mergeIntegrity(local.diagnostics)
			preserveLaunchFields(owned.record, local.record)
			return owned, nil
		}
		// The copy already held outranks the owner journal's. It is still the best
		// evidence available, so it keeps any integrity signal the owner branch
		// carried rather than losing the damage report with the race.
		local.diagnostics.mergeIntegrity(owned.diagnostics)
		return local, nil
	}
	// The owner branch is unreadable or holds nothing. Its absence must not erase
	// the only surviving evidence, so the scoped record stands as the answer: a
	// dead descendant's job survives only as the copy its ancestor forwarded, and
	// dropping it would lose the job. Callers can tell the two apart by comparing
	// the record's owner against the owner the job ID encodes — the same rule that
	// produced this read — so no separate flag is carried.
	//
	// The owner's OWN damage report still travels: a torn tail or a failed scan
	// there is a fact about this job's history, and dropping it because no row
	// survived would present unreadability and corruption as the same thing.
	local.diagnostics.mergeIntegrity(owned.diagnostics)
	return local, nil
}

// retainedJobOwnerSession names the session whose directory holds a read
// record's default-named output log. The identifier answers first: a record
// resolved from another session's journal has its bytes under the OWNER's
// directory, which is where the job actually ran, and the requested session only
// forwarded a copy. It falls back to the journal that supplied the record for a
// pre-owner job ID, which is the only pointer there is.
func retainedJobOwnerSession(jobID string, read sessionJobRecordRead) string {
	if owner := encodedJobOwner(jobID); owner != "" {
		return owner
	}
	// A pre-owner ID encodes nothing, so the RECORD's own stored owner is the only
	// statement of who ran the job. A forwarded copy read from an ancestor names
	// the descendant that ran it, and overwriting that with the ancestor would
	// attribute the job to the session that merely forwarded it.
	if read.record != nil && read.record.OwnerSessionID != "" {
		return read.record.OwnerSessionID
	}
	return read.sourceSessionID
}

// jobRecordForProjection returns the record to project onto the wire, with the
// launch facts withheld from one whose owner claim contradicts the validated
// identifier.
//
// Such a record was folded from a start that described some other session's
// launch. Its STATUS is still this job's best surviving evidence — the prefix it
// came from is what proved the scope the row is served under — but the facts
// that start declared about WHAT WAS LAUNCHED are another job's, and a pane that
// showed them would label this job's recovered output with the wrong command.
//
// The fields withheld are exactly the ones preserveLaunchFields refuses to COPY
// from a contradictory start, so there is one rule about what a contradiction may
// say: that guard keeps it out of another record, this keeps it off the wire. Both
// consult ownerClaimContradicts rather than a second predicate.
//
// StartedAt and OutputPath are deliberately KEPT. They are facts about this job's
// run and its bytes, not about which command produced them: output routing
// ignores a contradictory path anyway, and blanking StartedAt would tell the pane
// the job has no start time when its own event carries one.
//
// OutputBytes is NOT kept. A terminal count describes one specific log, and this
// record's path is the one the page reader discards as contradictory: its path
// reaches another job's bytes, and the read serves the default log under the
// validated owner instead. Projecting the discarded count would describe a file
// this read never opens.
func jobRecordForProjection(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil || !ownerClaimContradicts(rec) {
		return rec
	}
	withheld := rec.Clone()
	withheld.Type = ""
	withheld.Command = ""
	withheld.Task = ""
	withheld.Description = ""
	withheld.OutputBytes = 0
	return withheld
}

// jobRecordOutputPath resolves the log a record's bytes were written to. An
// explicit OutputPath is a LAUNCH FACT and is honored only when the record is a
// statement about this job: a start naming a session the job ID contradicts
// described some other session's launch, and its path reaches that job's bytes —
// or a stale file that happens to still exist. That is the counterpart of
// preserveLaunchFields, not a second rule: that guard stops a contradiction
// being COPIED into another record, this one stops it being HONORED.
//
// The default lives beside the journal of the session that ran the job, which
// after owner-authoritative resolution need not be the session that was asked.
func jobRecordOutputPath(stateDir, jobID string, read sessionJobRecordRead) string {
	if rec := read.record; rec != nil && rec.OutputPath != "" && !ownerClaimContradicts(rec) {
		return rec.OutputPath
	}
	return filepath.Join(jobsDir(stateDir, retainedJobOwnerSession(jobID, read)), "jobs", jobID+".log")
}

// jobReadError translates the reader's structural sentinels into the wire
// errors a caller can act on. A scope that a torn append left unresolved is
// UNAVAILABLE, not not-found: the scan could not establish absence, and the
// next read may answer normally once the append completes.
func jobReadError(jobID string, err error) error {
	if errors.Is(err, ErrJobScopeUnresolved) || errors.Is(err, ErrJobStatusUnresolved) {
		return appwire.Unavailable(fmt.Sprintf("job %q: %v", jobID, err))
	}
	return err
}

// LoadSessionJobOutputPage reads a coherent page from a local saved job.
// It is read-only and uses the same selectors and errors as the live producer.
//
// The record is resolved owner-authoritatively (see loadSessionJobRecord), and
// the log is read from where the session that RAN the job wrote it rather than
// from the session that was asked.
func LoadSessionJobOutputPage(stateDir, sessionID, jobID string, beforeBytes *int64, maxBytes int64) (JobOutputPage, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobOutputPage{}, false, jobReadError(jobID, err)
	}
	if !read.found {
		return JobOutputPage{}, false, nil
	}
	outPath := jobRecordOutputPath(stateDir, jobID, read)
	page, err := readJobOutputPageForRecord(outPath, read.record, beforeBytes, clampJobPageBytes(maxBytes))
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
//
// OwnerRef echoes the ref the caller ASKED about, and OwnerSessionID names the
// session that actually owns the job. The two are deliberately different
// questions: a client opens a job under the pane's ref and rejects a response
// whose ref does not match what it requested, so answering with the owner's ref
// there would discard the command and terminal status this read exists to
// recover. The owner's identity is not lost — it rides OwnerSessionID, the field
// that means it.
func LoadSessionJobGet(stateDir, sessionID, jobID string) (JobActivityJob, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID)
	if err != nil {
		return JobActivityJob{}, false, jobReadError(jobID, err)
	}
	if !read.found {
		return JobActivityJob{}, false, nil
	}
	// A decode reached this job but durable corruption left its status
	// unestablished: the row proves the job exists, not that it is still running.
	// Report that as unavailable rather than as a running job. Output paging is
	// deliberately NOT gated on this — the bytes that landed are still readable,
	// and reading them is how a person establishes what happened.
	if statusUnreadable(read) {
		return JobActivityJob{}, false, jobReadError(jobID, ErrJobStatusUnresolved)
	}
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	job := projectActivityJob(jobRecordForProjection(read.record), ownerRef)
	// The fold states no owner for a finish-only journal and a contradictory one
	// for a fallback start; the validated identifier states the truth.
	job.OwnerSessionID = retainedJobOwnerSession(jobID, read)
	return job, true, nil
}
