package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// jobRecordDiagnosticCodes renders a retained read's diagnostics as the wire
// codes clients read. It keeps the jobstore-shaped internals out of the wire
// type: appwire carries strings, this package owns the mapping.
func jobRecordDiagnosticCodes(d sessionJobRecordDiagnostics) []string {
	var codes []string
	if d.ForwardedFallback {
		codes = append(codes, "forwarded_fallback")
	}
	if d.OwnerUnavailable {
		codes = append(codes, "owner_unavailable")
	}
	if d.TornTail {
		codes = append(codes, "journal_torn_tail")
	}
	if d.IncompleteLifecycle {
		codes = append(codes, "incomplete_lifecycle")
	}
	if d.ScanIncomplete {
		codes = append(codes, "journal_scan_incomplete")
	}
	return codes
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

// OwnerDeletionFence reports whether a session is durably fenced for deletion.
// It is the hub's own record (hubcore.DeletionStore), and it lands BEFORE the
// on-disk tombstone: a deletion records its fence, then removes the target.
// Between those two steps a session is durably fenced while its marker does not
// exist yet, so a read that trusts only the marker serves data the hub has
// already committed to deleting.
//
// The hub passes its configured fence into BOTH saved-job readers, which apply
// it to the owner they RESOLVED — the session whose journal or log they are
// about to serve — not merely to the session that was asked about. A nil fence
// is the truth for a bare agent process reading its own state directory: there
// is no hub record to consult, and the tombstone check below still applies.
// Passing it rather than storing it in a package variable is deliberate: a
// variable that production never assigns is a seam that reads as wired and
// behaves as unset, which is exactly how a deletion gap ships unnoticed.
type OwnerDeletionFence func(sessionID string) (fenced bool, err error)

// retainedOwnerFenced reports whether sessionID is fenced for deletion, by the
// caller's durable record when one was supplied and by the on-disk tombstone
// always. Either standing fence is enough: the two answer the same question at
// different points in a deletion's progress, and a read that honors only one of
// them serves data across the gap between them. A deletion that was abandoned
// removes its record, so both answers go back to false and reads resume.
func retainedOwnerFenced(stateDir, sessionID string, fence OwnerDeletionFence) (bool, error) {
	if fence != nil {
		fenced, err := fence(sessionID)
		if err != nil {
			// An unreadable durable fence is not permission: the read fails
			// closed, because serving a possibly-deleted session's job is the
			// harm the fence exists to prevent.
			return false, err
		}
		if fenced {
			return true, nil
		}
	}
	return ownerSessionTombstoned(stateDir, sessionID), nil
}

// jobReadFenced reports whether ANY of the sessions a read would serve is
// durably fenced. Every session whose journal or log a request can carry gets
// the same check: the one that was asked about, the one whose journal supplied
// the record, and the one whose directory holds the bytes. Repeats collapse, so
// a read that resolves to the requested session alone consults the fence once.
func jobReadFenced(stateDir string, fence OwnerDeletionFence, sessions ...string) (bool, error) {
	seen := make(map[string]struct{}, len(sessions))
	for _, sessionID := range sessions {
		if sessionID == "" {
			continue
		}
		if _, repeat := seen[sessionID]; repeat {
			continue
		}
		seen[sessionID] = struct{}{}
		if fenced, err := retainedOwnerFenced(stateDir, sessionID, fence); err != nil {
			return false, err
		} else if fenced {
			return true, nil
		}
	}
	return false, nil
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
	// ScanIncomplete is set when the journal the record came from hit durable
	// corruption, so the scan returned a decoded prefix rather than reaching a
	// clean record boundary. The record is then a statement about the prefix,
	// not necessarily about the job: see stateEstablished.
	ScanIncomplete bool
	// CleanBoundary is set by a journal read whose scan reached the END of the
	// journal at a record boundary — a readable place to append, not a damaged
	// one. It says the journal can still GROW: a finish appended there lands on
	// the next read, and may outrank the row this read selected. That is a
	// different fact from TornTail (an append half-landed) and from
	// ScanIncomplete (an append cannot be reached at all), which is why it is
	// carried separately and merged across every consulted journal.
	//
	// This describes the JOURNAL, so it is not evidence against any record: a
	// record's own confidence is recordStatusUnconfirmed's question. It exists
	// for statusRecheckableOnWire, which asks whether any eligible source could
	// still answer.
	CleanBoundary bool
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
	// artifactSessionID is the session whose directory holds record's own
	// artifacts — its default-named output log — when ownership was corroborated
	// but the selected record's status still came from another journal. The two
	// are different questions: sourceSessionID says where the STATUS came from,
	// this says where the BYTES were written. Empty means the two coincide, and
	// routing falls back to sourceSessionID.
	//
	// Only a corroborated owner is ever named here. An uncorroborated claim named
	// a session whose directory was never involved, and routing there hid a
	// readable log sitting beside the scoped journal.
	artifactSessionID string
	// hintedSessionID is a VALIDATED owner hint from a legacy row, carried
	// separately from artifactSessionID and regardless of whether the owner's
	// journal corroborated it. It answers a different question: not "where are
	// the bytes by default", but "which session's data could this read still
	// reach". A forwarded row can carry an explicit OutputPath pointing into the
	// owner's directory even when that owner's journal is absent, so the fence
	// has to cover the hinted session even though routing never names it.
	hintedSessionID string
	found           bool
	diagnostics     sessionJobRecordDiagnostics
	// scanErr is the durable-corruption error THIS branch's own scan produced,
	// or nil. It stays branch-local on purpose: mergeIntegrity carries the
	// damage report forward, but whether one particular record's state can be
	// stated is a fact about that record's journal alone.
	scanErr error
	// statusUnconfirmed is set when THIS record's nonterminal status is not
	// established. The evidence against it is specific to this record: the
	// journal that supplied it ended in a torn append or stopped at durable
	// corruption, the record itself is a fragment with no status or no start of
	// its own, its own owner claim contradicts the validated identifier, or it is
	// a forwarded copy whose owner could not be consulted. A lifecycle gap the
	// fold reports for ANOTHER job in the same journal is not evidence against
	// this record and does not set this. It stays branch-local for the same
	// reason scanErr does: mergeIntegrity says what the HISTORY could not
	// establish, this says what the SELECTED STATUS could not.
	//
	// The two are deliberately separate. A clean owner record that inherited an
	// ancestor's scan damage is a record whose status IS established — it was
	// decoded from an intact journal — and a client that treated the aggregate
	// warning as doubt about the status would stop following a healthy running
	// job and miss its finish.
	statusUnconfirmed bool
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
//
// fence is the hub's durable deletion record, applied to every session whose
// journal this read is about to serve — the requested session's, and the
// resolved owner's. It is nil outside the hub, where there is no durable record
// to consult and the on-disk tombstone is the whole fence.
func loadSessionJobRecord(stateDir, sessionID, jobID string, fence OwnerDeletionFence) (sessionJobRecordRead, error) {
	if err := schema.ValidateSessionID(sessionID); err != nil {
		return sessionJobRecordRead{}, err
	}
	// The requested session is a session whose journal this read is about to
	// open, so it gets the same fence the owner does. A deletion durably recorded
	// for it — and not yet abandoned — means its history is not servable, whether
	// the record needed here would have come from it or been forwarded through it.
	if fenced, fenceErr := retainedOwnerFenced(stateDir, sessionID, fence); fenceErr != nil {
		return sessionJobRecordRead{}, fenceErr
	} else if fenced {
		return sessionJobRecordRead{}, nil
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
		// A pre-owner job ID (a bare UUID) encodes nothing to rank against. Rows
		// that old can still carry an owner field, and when they do it is the only
		// pointer to the journal that may hold the finish: consult it as a HINT,
		// and let the owner's journal corroborate before it is believed. Without
		// that, a forwarded start written before owner-encoded IDs keeps showing
		// running while the owner's journal holds the completed job.
		hint := ""
		if local.record != nil {
			hint = local.record.OwnerSessionID
		}
		if hint == "" || hint == sessionID {
			return local, nil
		}
		// The hint comes from a STORED ROW, so it is untrusted input, not an
		// identifier this read has validated: it is a session id read back out of
		// a journal. Validate it before it reaches the deletion fence or a
		// filesystem lookup, so a malformed claim cannot name a path or a session
		// the fence is never consulted for.
		//
		// Asked as a PREDICATE, not as a failure: a malformed claim is not a failure
		// of THIS read. The caller asked about the scoped session, whose record is
		// intact, and a claim that is not a session id cannot corroborate anything,
		// so there is no branch to consult and nothing to retry. The scoped copy
		// stands, and the two flags below say why it is fallback evidence rather
		// than the owner's.
		if !hintIsSessionID(hint) {
			local.diagnostics.OwnerUnavailable = true
			local.diagnostics.ForwardedFallback = true
			return local, nil
		}
		// Validated from here on, so this read can reach the hinted session's
		// directory — through a corroborated artifact route, or through an
		// explicit OutputPath the row already carries. The fence must cover it in
		// every later check, not only the one made now.
		local.hintedSessionID = hint
		if fenced, fenceErr := retainedOwnerFenced(stateDir, hint, fence); fenceErr != nil {
			return sessionJobRecordRead{}, fenceErr
		} else if fenced {
			return sessionJobRecordRead{}, nil
		}
		corroborated, hintErr := readSessionJobJournal(stateDir, hint, jobID)
		// Corroboration is not merely "the hinted journal mentions this job": the
		// row it holds must AGREE that the hinted session owns it. A forwarded copy
		// living in a different ancestor also names the job, and with a contradictory
		// owner field it would otherwise be treated as the hinted owner's own record
		// and allowed to replace the scoped finish. An empty owner field is the
		// pre-owner legacy shape and states nothing to contradict.
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
			// corruption was observed. ScanIncomplete is the wire's "the scan stopped
			// at a bad line" code, and claiming it here would tell a client the
			// history is damaged when the truth is that it could not be opened —
			// owner_unavailable, set just below, is that truth. Only the diagnostics
			// the journal reader actually returned are merged; readSessionJobJournal
			// reports its own scan damage through corroborated.diagnostics.
			local.diagnostics.mergeIntegrity(corroborated.diagnostics)
			local.diagnostics.OwnerUnavailable = true
			local.diagnostics.ForwardedFallback = true
			// A hint that cannot be read is a fact about the HINT, not a failure
			// of this read: the caller asked about the scoped session, whose
			// record is intact and is what this returns.
			return local, nil //nolint:nilerr // an unreadable owner hint is a classified fallback state carried on the diagnostics, not an operational failure
		}
		// A corroborated legacy owner goes through the SAME precedence the encoded
		// branch applies — no second authority test. Corroboration makes the hinted
		// journal the owner, but ownership is not evidence of settled state: its
		// prefix may have stopped at durable corruption short of the finish the
		// scoped copy observed. stateEstablished is what still separates them.
		if retainedJobReadOutranks(false, local, true, corroborated) {
			corroborated.diagnostics.mergeIntegrity(local.diagnostics)
			corroborated.diagnostics.ForwardedFallback = false
			corroborated.diagnostics.OwnerUnavailable = false
			// The owner's journal is the authority on what the job IS; the scoped
			// journal is the only place the launch facts survive when the owner holds
			// nothing but the finish. Same rule as the encoded branch, one call.
			preserveLaunchFields(corroborated.record, local.record)
			return corroborated, nil
		}
		local.diagnostics.mergeIntegrity(corroborated.diagnostics)
		local.diagnostics.ForwardedFallback = true
		// Ownership is corroborated even though the scoped copy supplied the
		// selected status — a torn tail in the owner's journal is why it lost, not a
		// failed claim. The owner still owns the directory its log was written to,
		// so carry that separately from the journal that supplied the status.
		local.artifactSessionID = hint
		return local, nil
	}
	if owner == sessionID {
		// An ID naming this session is already the authoritative branch.
		return local, nil
	}
	if fenced, fenceErr := retainedOwnerFenced(stateDir, owner, fence); fenceErr != nil {
		return sessionJobRecordRead{}, fenceErr
	} else if fenced {
		// The owner's deletion fence stands. Its journal and log are not
		// servable, and a forwarded copy of a deleted session's job is not a
		// substitute for the owner's record either: the job was deleted with
		// its owner.
		return sessionJobRecordRead{}, nil
	}
	owned, ownerErr := readSessionJobJournal(stateDir, owner, jobID)
	if ownerErr == nil && owned.found {
		// Both journals hold a record: apply the one precedence rule.
		existingIsOwner := retainedJobAuthority(jobID, local.sourceSessionID, local.record)
		incomingIsOwner := retainedJobAuthority(jobID, owned.sourceSessionID, owned.record)
		if retainedJobReadOutranks(existingIsOwner, local, incomingIsOwner, owned) {
			owned.diagnostics.mergeIntegrity(local.diagnostics)
			owned.diagnostics.ForwardedFallback = !incomingIsOwner
			owned.diagnostics.OwnerUnavailable = false
			preserveLaunchFields(owned.record, local.record)
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
	// The owner's OWN damage report still travels: a torn tail or a failed scan
	// there is a fact about this job's history, and dropping it because no row
	// survived would present unreadability and corruption as the same thing.
	local.diagnostics.mergeIntegrity(owned.diagnostics)
	if !retainedJobAuthority(jobID, local.sourceSessionID, local.record) {
		local.diagnostics.ForwardedFallback = true
	}
	return local, nil
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
// under the validated owner answers instead. The owner's terminal status, exit
// code and diagnostics are untouched either way — this function only ever
// fills empty fields.
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
}

// retainedJobReadOutranks reports whether the incoming read replaces the
// existing one for the same job. It is the ONE decision both branches of
// loadSessionJobRecord consult — the encoded-owner branch and the legacy
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
	// The record's own confidence, not the journal's aggregate: this is the same
	// signal the wire carries, asked here so a nonterminal record that cannot
	// state its status never outranks a terminal one that can.
	return !recordStatusUnconfirmed(read.record, read.scanErr, read.diagnostics)
}

// statusUnconfirmedOnWire reports whether the SELECTED STATUS is unestablished,
// for the clients that decide whether to keep following a job.
//
// It answers a different question from stateEstablished, and the difference is
// the one that matters on a wire: stateEstablished ranks two records against
// each other, while this says whether the one record a caller received can
// state the job's state at all. The inputs are the read's OWN record evidence —
// its status, its scan error, its journal's lifecycle coherence, whether it is
// a forwarded copy with no owner behind it — never the merged aggregate. A
// clean owner record that inherited an ancestor's scan damage still has an
// established status: the bytes that said "running" were intact, and only the
// ancestor's journal was torn. Gating the client on the aggregate would make it
// stop following a healthy running job and miss the finish.
//
// A terminal row is always confirmed: the first terminal write is a complete
// observation, and damage after it can take nothing away. That is the same
// premise stateEstablished rests on.
//
// A NONTERMINAL row with no status at all is the third case, and it is
// unconfirmed. A journal holding only a notification for the job folds a row for
// it because the job was NAMED, not because anyone said what it did: no status,
// no end time, no exit code. Read as confirmed, a client polls it at the live
// rate forever for a state the journal never stated. The rule is the invariant
// this file keeps applying — a record whose status was never established is
// unconfirmed, not confirmed by absence — and it says nothing about the row's
// diagnostics or about any output recoverable beside it, which stay readable.
func statusUnconfirmedOnWire(read sessionJobRecordRead) bool {
	if read.record == nil || read.record.Status.IsTerminal() {
		return false
	}
	if read.record.Status == "" {
		return true
	}
	// A forwarded copy stands in for an owner nobody could consult, so nothing
	// behind it can confirm the run is still going. The invariant: a record that
	// is not fully established must not be followed as though it were.
	if read.diagnostics.ForwardedFallback {
		return true
	}
	// The record's own journal could not reach a settled boundary for THIS job.
	return read.statusUnconfirmed
}

// statusRecheckableOnWire reports whether a LATER read could still establish the
// status of the row this read returns, for the clients that decide whether to
// look again rather than never.
//
// THE INVARIANT, and every branch below follows from it: unconfirmed means
// provisional, and provisional means somebody keeps looking. A client that only
// stopped would watch an unconfirmed row forever while the evidence that would
// settle it landed.
//
// So the question this answers is never "is the row damaged" but "could a later
// read of some eligible source produce a better answer". Only one shape is
// permanent: THIS record's own scan stopped at durable corruption and no owner
// stands behind it, because then no append behind the bad line is reachable and
// there is no other journal that could answer instead.
//
// Everything else is provisional: a torn append can complete, a statusless
// fragment can gain its status, a finish that declared no end can be followed by
// a terminal one (the fold only short-circuits once a status is already
// terminal), and — the case the forwarded branch exists for — a forwarded copy
// whose owner could not be CONSULTED says nothing about whether the owner will be
// readable on the next read. Deciding "nothing better will ever arrive" from a
// temporary read failure is the failure this predicate is meant to prevent.
//
// Like statusUnconfirmed this deliberately ignores the merged aggregate: an
// intact owner row that inherited an ANCESTOR's durable corruption still rests
// on readable bytes, and a client that stopped rechecking it would miss the
// finish the owner later records.
func statusRecheckableOnWire(read sessionJobRecordRead) bool {
	if !statusUnconfirmedOnWire(read) {
		return false
	}
	if read.record == nil {
		return false
	}
	// A forwarded copy stands in for an owner that could not be consulted, and an
	// unreadable owner is a state of THIS READ, not of the owner's journal: the
	// session may be mid-write, the store may be briefly locked, the directory may
	// appear on the next lookup. The owner's own finish is the authoritative
	// answer, so the row stays provisional while that source is merely unreachable
	// now.
	if read.diagnostics.ForwardedFallback || read.diagnostics.OwnerUnavailable {
		return true
	}
	// A clean scan means the journal that supplied this row was readable to its
	// end, so a later read can still extend it.
	if read.scanErr == nil {
		return true
	}
	// This record's own scan stopped at durable corruption. That is a permanent
	// answer only when this record is the ONLY source the read consulted: with no
	// owner to consult and nothing else readable, no later read gets behind the
	// bad line. It is not permanent when another consulted journal is still
	// holding an append in flight, because completing that append can produce a
	// terminal observation that OUTRANKS this unconfirmed row on the next read.
	//
	// There are TWO ways another journal can still answer, and both are carried on
	// the merged diagnostics:
	//
	//   - TornTail: an append landed without its newline and can complete.
	//   - CleanBoundary: the scan reached the end of that journal at a record
	//     boundary, so an append there lands on the next read. A journal holding
	//     only a forwarded notification is exactly this shape — it names the job
	//     without stating its status, and a finish appended behind it outranks
	//     this row.
	//
	// The load-bearing fact is that both flags are mutually exclusive with a scan
	// ERROR for one journal: ScanEventsWithDiagnostics sets TornTail only on the
	// unterminated-final-line break, which returns a nil error, and returns an
	// error only from the durable-corruption path, which stops before any
	// boundary. So while THIS branch's scan error is non-nil, this branch's own
	// TornTail and CleanBoundary are both false, and either flag in the merged
	// aggregate can only have come from the other journal the read consulted.
	// Reading the aggregate here is therefore reading a fact about the OTHER
	// source — the opposite of the mistake this predicate was fixed for, where an
	// ancestor's damage was taken as evidence against an intact owner record.
	//
	// ScanIncomplete is deliberately NOT consulted: it says some journal had
	// durable corruption, but not which one, so it can neither confirm nor deny
	// that another source is still growable.
	return read.diagnostics.TornTail || read.diagnostics.CleanBoundary
}

// mergeIntegrity folds another read's damage report into d. Diagnostics are not
// a verdict about which branch won: either branch being torn or incoherent is
// something the caller must still know.
func (d *sessionJobRecordDiagnostics) mergeIntegrity(other sessionJobRecordDiagnostics) {
	d.TornTail = d.TornTail || other.TornTail
	d.IncompleteLifecycle = d.IncompleteLifecycle || other.IncompleteLifecycle
	d.ScanIncomplete = d.ScanIncomplete || other.ScanIncomplete
	d.CleanBoundary = d.CleanBoundary || other.CleanBoundary
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
	// TornTail and ScanIncomplete are mutually exclusive for one journal: the scan
	// sets a torn tail on the unterminated-final-line break, which returns no
	// error, and reports damage only from the paths that stop before a boundary.
	//
	// ScanIncomplete reads the scan's CLASSIFIED damage, not "an error happened".
	// A journal that passes validation and then cannot be OPENED is an access
	// failure, and reporting journal_scan_incomplete for it would tell a client
	// the history is corrupt when nothing was read; the caller's owner_unavailable
	// is that truth. CleanBoundary is the plain "the scan completed" reading,
	// because a scan that ran to the end is the only one that leaves an append
	// landable.
	diagnostics := sessionJobRecordDiagnostics{
		TornTail:       readDiagnostics.TornTail,
		ScanIncomplete: readDiagnostics.Corrupt,
		CleanBoundary:  scanErr == nil,
	}
	recs, foldErr := jobstore.FoldChecked(events)
	if foldErr != nil && errors.Is(foldErr, jobstore.ErrIncompleteLifecycle) {
		diagnostics.IncompleteLifecycle = true
	}
	rec := recs[jobID]
	if rec == nil {
		// The prefix holds nothing about this job. If the scan then failed, the
		// journal cannot answer the question at all, so the damage is the answer.
		// The diagnostics ride the read anyway: the caller that asked about this
		// journal is not always the journal's owner, and a competing branch serving
		// a forwarded copy still has to be able to say that the owner's history is
		// CORRUPT rather than merely absent. Both travels together; only the error
		// decides whether this read can stand on its own.
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
	// best-effort record, flagged, rather than an error that erases what survived.
	return sessionJobRecordRead{
		record:            rec,
		sourceSessionID:   sessionID,
		found:             true,
		diagnostics:       diagnostics,
		scanErr:           scanErr,
		statusUnconfirmed: recordStatusUnconfirmed(rec, scanErr, diagnostics),
	}, nil
}

// recordStatusUnconfirmed reports whether THIS record's nonterminal status is
// not established.
//
// TornTail and scanErr are evidence against the record itself: an unterminated
// append or durable corruption leaves the finish unaccounted for, and the
// status states only the prefix that survived.
//
// IncompleteLifecycle is NOT, on its own. FoldChecked reports the FIRST
// structural gap anywhere in the journal — including another job's — so using
// the journal's verdict here would mark a healthy running row unconfirmed
// because a neighbour is broken, and a client would stop following it. What
// makes THIS record a fragment is the gap's own shape: a record with a type and
// a start time was folded from a start, and one without a type is a
// notification or finish that never had one.
//
// A status the fold copied from a malformed finish is the other per-record shape,
// and it is the flag rather than a field the event may omit: the finish declared
// no end at all, so the journal has recorded the job's end and the record cannot
// say what it was. `terminal_generation` is NOT the test — a writer may omit it,
// and a finish that omits it while carrying "running" is exactly the case that
// slipped through.
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

// ownerClaimContradicts reports whether a record's own OwnerSessionID names a
// session the job ID does not encode.
//
// An EMPTY claim states nothing and is the pre-owner legacy shape, and a job ID
// that encodes no owner (a synthetic or legacy bare ID) leaves nothing to
// contradict. Both are therefore not conflicts — the same two exceptions
// checkJobStart makes, for the same reason.
func ownerClaimContradicts(rec *jobstore.JobRecord) bool {
	if rec.OwnerSessionID == "" {
		return false
	}
	encoded := encodedJobOwner(rec.JobID)
	return encoded != "" && encoded != rec.OwnerSessionID
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
// The fields withheld are the ones preserveLaunchFields refuses to COPY from a
// contradictory start, so there is one rule about what a contradiction may say:
// that guard keeps it out of another record, this keeps it off the wire. Both
// consult ownerClaimContradicts rather than a second predicate.
//
// StartedAt, OutputPath and the byte counts are deliberately KEPT. They are
// facts about this job's run and its bytes, not about which command produced
// them: the output reader ignores a contradictory path anyway, HasOutput is
// true either way, and blanking StartedAt would tell the pane the job has no
// start time when its own event carries one.
func jobRecordForProjection(rec *jobstore.JobRecord) *jobstore.JobRecord {
	if rec == nil || !ownerClaimContradicts(rec) {
		return rec
	}
	withheld := rec.Clone()
	withheld.Type = ""
	withheld.Command = ""
	withheld.Task = ""
	withheld.Description = ""
	return withheld
}

// routingJobSession names the session whose directory holds a record's own
// artifacts — its default-named output log. It is an INTERNAL path question, not
// the wire's OwnerRef, and it answers with the same authority rule the record's
// resolution used: the validated owner the job ID encodes, when this record is
// the authoritative one. A contradictory start that claims another session must
// not redirect the read to that session's directory, where a same-named log may
// belong to a different job entirely — the log lives under the session that RAN
// the job, and the validated identifier is what says which one that is.
//
// For a job ID encoding no owner (a legacy bare UUID) there is no identifier to
// rank against, so the journal that SUPPLIED the selected record is the answer —
// unless the resolution CORROBORATED a different owner while rejecting that
// owner's row on evidence grounds. Then the two questions come apart: the status
// is the scoped journal's, and the bytes are the owner's. The claim itself is
// never the routing input — an uncorroborated one named a session whose
// directory was never involved, and routing there hid a readable log sitting
// beside the scoped journal — but a corroborated owner's directory is exactly
// where its log was written.
func routingJobSession(jobID string, read sessionJobRecordRead) string {
	if owner := encodedJobOwner(jobID); owner != "" {
		return owner
	}
	if read.artifactSessionID != "" {
		return read.artifactSessionID
	}
	return read.sourceSessionID
}

// hintIsSessionID reports whether a stored owner hint is a session id. It is a
// PREDICATE, not a validation step: a claim that is not a session id states
// nothing this read can corroborate, which is an answer rather than a failure.
//
// It exists so no error sits in scope at the caller's `return local, nil`. That
// return is deliberate — the caller asked about the scoped session, whose record
// is intact, and the two fallback flags say why the scoped copy is evidence
// rather than the owner's — but an error-valued variable next to it reads as a
// swallowed failure, which is exactly what the nilerr check flags.
func hintIsSessionID(hint string) bool {
	return schema.ValidateSessionID(hint) == nil
}

// fencedSessions names every session whose journal OR log this read can carry,
// for the deletion fence. It is deliberately NOT routingJobSession: routing
// answers "which single directory does an unset OutputPath fall back to", while
// the fence has to cover every directory the answer could come from. A legacy
// row can carry an explicit OutputPath into a hinted owner's directory even when
// that owner's journal is absent and never corroborated it, and routing never
// names that session — so a fence built from routing alone would serve those
// bytes while the owner's deletion is pending.
func fencedSessions(jobID string, read sessionJobRecordRead, requested string) []string {
	sessions := []string{requested, routingJobSession(jobID, read)}
	if read.hintedSessionID != "" {
		sessions = append(sessions, read.hintedSessionID)
	}
	return sessions
}

// jobOutputPathSession names the session whose work directory CONTAINS a
// resolved output path, or "" when the path is somewhere else entirely.
//
// The fence has to know whose bytes a page carries, and the selected path is one
// of the ways a session becomes reachable — the claims in the record are not the
// only one. A forwarded legacy row with no owner field states nothing to
// contradict, yet its explicit path can point into the work directory of the
// session that originally ran the job. Nothing in the claim-derived fence names
// that session, so the path is asked directly, by directory.
//
// The decision is made on the DIRECTORY SHAPE under the state root, compared by
// FILESYSTEM IDENTITY rather than by spelling (see the walk below), and the
// session name is validated before it is returned: a path whose child of the
// root is not a session id names no session, and handing such a name to the
// fence would consult a deletion record for something that cannot have one. A
// launcher-chosen path outside every session work directory names none either,
// which is the honest answer — no session's deletion governs those bytes.
func jobOutputPathSession(stateDir, path string) string {
	if path == "" {
		return ""
	}
	// The root is the PARENT of the work directories, so it has to be derived
	// the same way schema.SessionWorkDir derives them — including its
	// blank-state-dir branch, which keeps a flat temp layout rather than
	// "sessions/". Anything else would compare against a directory the paths
	// were never built from.
	sessionsRoot := schema.SessionsDir(stateDir)
	if strings.TrimSpace(stateDir) == "" {
		sessionsRoot = filepath.Join(os.TempDir(), "evener-jobs")
	}
	// Identity, not spelling. The opener resolves the same file through an alias
	// — a state home reached by a symlink, which the layout prefix allows above
	// the bucket — so a path written under the alias and a root spelled the
	// physical way name the same directory while comparing unequal LEXICALLY.
	// filepath.Rel alone therefore misses the session that owns the bytes, and
	// both fence consults omit it. Walk up from the path comparing each ancestor
	// to the root by os.SameFile, and read the session name from the child one
	// level down from whichever ancestor matched.
	//
	// No-follow checks within the state layout are untouched: this decides WHICH
	// session a path belongs to, not whether the path may be read. The anchor
	// that permits an alias is the same one localJobTrustedRoot and
	// symlinkErrorDeep already use.
	sessionsInfo, err := os.Stat(sessionsRoot)
	if err != nil {
		// The root does not exist, so no path can be inside it by identity. Fall
		// through to nothing rather than guessing from a spelling.
		return ""
	}
	candidate := filepath.Dir(filepath.Clean(path))
	child := ""
	for {
		info, statErr := os.Stat(candidate)
		if statErr == nil && os.SameFile(sessionsInfo, info) {
			// The matched root identifies the CONTAINER; the session is the
			// directory one level below it, which is the child this walk last
			// came through. Reading the name off that child is what makes the
			// answer independent of how the path was spelled.
			if child == "" {
				return ""
			}
			sessionID := filepath.Base(child)
			if !hintIsSessionID(sessionID) {
				return ""
			}
			return sessionID
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return ""
		}
		child = candidate
		candidate = parent
	}
}

// jobReadError translates the reader's structural sentinels into the wire
// errors a caller can act on. A scope that a torn append left unresolved is
// UNAVAILABLE, not not-found: the scan could not establish absence, and the
// next read may answer normally once the append completes.
func jobReadError(jobID string, err error) error {
	if errors.Is(err, ErrJobScopeUnresolved) {
		return appwire.Unavailable(fmt.Sprintf("job %q: %v", jobID, err))
	}
	return err
}

// LoadSessionJobOutputPage reads a coherent page from a local saved job.
// It is read-only and uses the same selectors and errors as the live producer.
//
// fence is the hub's durable deletion record (see OwnerDeletionFence), applied
// to the owner this read resolves before that owner's log is opened. A nil
// fence leaves the on-disk tombstone as the only check, which is correct for a
// caller with no durable record to consult.
func LoadSessionJobOutputPage(stateDir, sessionID, jobID string, beforeBytes *int64, maxBytes int64, fence OwnerDeletionFence) (JobOutputPage, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID, fence)
	if err != nil {
		return JobOutputPage{}, false, jobReadError(jobID, err)
	}
	if !read.found {
		return JobOutputPage{}, false, nil
	}
	rec := read.record
	// The log lands beside the journal of the session that ran the job, which
	// after owner-authoritative resolution need not be the session that was
	// asked. The routing name comes from the validated identifier, never from
	// what the record claims: a start claiming another session would otherwise
	// send this read to that session's directory for a same-named log that
	// belongs to a different job.
	defaultOutPath := filepath.Join(jobsDir(stateDir, routingJobSession(jobID, read)), "jobs", jobID+".log")

	// A record's explicit OutputPath is a LAUNCH FACT, and it is honored only
	// when the record is a statement about this job. The same owner-claim rule
	// that governs adopting that field governs trusting it on read: a start
	// naming a session the job ID contradicts described some other session's
	// launch, and its path reaches that job's bytes — or a stale file — while
	// this job's own log sits under the validated owner. An agreeing claim and
	// an empty one (the pre-owner legacy shape) both keep their explicit path,
	// because those records do describe this job.
	//
	// This is the counterpart of preserveLaunchFields, not a second rule: that
	// guard stops a contradiction from being COPIED into another record, and
	// this one stops a contradictory record's own stored path from being
	// HONORED. A record that never entered the merge needs both.
	contradicts := ownerClaimContradicts(rec)
	outPath := rec.OutputPath
	if outPath == "" || contradicts {
		outPath = defaultOutPath
	}
	// The path is resolved BEFORE the fence is consulted, because the fence has
	// to name every session whose bytes this page can carry and the selected
	// path is one of the places a session becomes reachable. A forwarded legacy
	// row can point into the ORIGINAL owner's work directory while naming no
	// owner at all: its claim contradicts nothing, so nothing else in this read
	// names that session, and a fence built from the claim would check only the
	// session that was asked and hand back bytes whose owner's deletion is
	// already durable.
	fencedNames := fencedSessions(jobID, read, sessionID)
	if pathSession := jobOutputPathSession(stateDir, outPath); pathSession != "" {
		fencedNames = append(fencedNames, pathSession)
	}
	// The fence above was consulted before either journal was scanned. A deletion
	// recorded DURING that scan is therefore invisible to it, and this read would
	// open a log the hub has already committed to removing. Re-consult the same
	// record now, at the boundary where the bytes are opened. A standing fence
	// discards the page, and an abandoned deletion clears the record, so a later
	// read resumes by itself.
	if fenced, fenceErr := jobReadFenced(stateDir, fence, fencedNames...); fenceErr != nil {
		return JobOutputPage{}, false, fenceErr
	} else if fenced {
		return JobOutputPage{}, false, nil
	}
	// Terminal byte counts are evidence about a LOG, so they are enforced only
	// from a record that describes the log this read selected. A contradictory
	// record's own path was just discarded, which means its counts describe a
	// different file: checking the encoded owner's readable default log against
	// them rejects a page that is fine and shows the pane an unavailable error.
	// That log is read on its own snapshot checks instead. This is not a
	// leniency — the checks stay exactly as strict for the records that DO
	// describe the selected artifact, which is the only case where they mean
	// anything.
	validationRecord := rec
	if contradicts {
		validationRecord = nil
	}
	page, err := readJobOutputPageForRecord(outPath, validationRecord, beforeBytes, clampJobPageBytes(maxBytes))
	// Recheck AFTER the read: a deletion can become durable between the check
	// above and the bytes landing in this process, and the page would otherwise
	// leave with data the hub has already committed to removing. A standing
	// fence discards it; an abandoned deletion clears the record, so the next
	// request serves the page normally.
	if fenced, fenceErr := jobReadFenced(stateDir, fence, fencedNames...); fenceErr != nil {
		return JobOutputPage{}, false, fenceErr
	} else if fenced {
		return JobOutputPage{}, false, nil
	}
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
// owner the record actually came from. The two are deliberately different
// questions: a client opens a job under the pane's ref and rejects a response
// whose ref does not match what it requested (jobLogReader's check), so
// answering with the owner's ref there would discard the command and terminal
// status this read exists to recover. The owner's identity is not lost — it
// rides OwnerSessionID, the field that means it.
//
// fence is the hub's durable deletion record (see OwnerDeletionFence), applied
// to the owner this read resolves before that owner's journal is served.
func LoadSessionJobGet(stateDir, sessionID, jobID string, fence OwnerDeletionFence) (JobActivityJob, bool, error) {
	read, err := loadSessionJobRecord(stateDir, sessionID, jobID, fence)
	if err != nil {
		return JobActivityJob{}, false, jobReadError(jobID, err)
	}
	if !read.found {
		return JobActivityJob{}, false, nil
	}
	// The checks inside loadSessionJobRecord all precede the journal scans, so a
	// deletion recorded DURING those scans is invisible to them and this read
	// would return metadata the hub has already committed to deleting. Recheck a
	// third time here, after the scan and before the answer: the requested
	// session's record and the resolved owner's identity both leave this
	// process. A standing fence withholds the row; an abandoned deletion clears
	// the record, so a later read resumes rather than locking out.
	if fenced, fenceErr := jobReadFenced(stateDir, fence, fencedSessions(jobID, read, sessionID)...); fenceErr != nil {
		return JobActivityJob{}, false, fenceErr
	} else if fenced {
		return JobActivityJob{}, false, nil
	}
	ownerRef := appwire.Ref{SourceID: "local", ThreadID: sessionID}.String()
	job := projectActivityJob(jobRecordForProjection(read.record), ownerRef)
	job.Diagnostics = jobRecordDiagnosticCodes(read.diagnostics)
	job.StatusUnconfirmed = statusUnconfirmedOnWire(read)
	job.StatusRecheckable = statusRecheckableOnWire(read)
	return job, true, nil
}
