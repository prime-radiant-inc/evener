package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/task"
)

// retirementEvidence reads only this Session's owners. The shared delegate
// controller invokes it once per exact resident, outside its mutex and before
// revalidating the captured tree version and pointers. It never scans that tree.
func (s *Session) retirementEvidence() ([]RetirementBlocker, error) {
	blockers := s.retirementLocalBlockers()
	blockers = append(blockers, s.retirementDelegateBlockers()...)
	// Root attention cached behind a Stop's park or a permanent provider
	// failure's pause is not work this process can do: it waits for the user,
	// and both the attention (transcript fold) and the park (the durable
	// queue/steering holds) are restored by the next resume. A fresh delegate
	// wake or a paced retry is still work. This is pendingRootDelegateAttention's
	// rule, applied inside the one attentionMu section below rather than
	// through it: an arm-retry callback moves IDs from delegateAttentionArmIDs
	// to rootAttentionWakeIDs under attentionMu, and only a single section sees
	// that handoff atomically. The park is read first, the lock order
	// rootAttentionRailParked documents.
	if s.retirementAttentionBlocked(s.rootAttentionRailParked()) {
		blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: s.id})
	}
	if state, ok := s.getOrCreateGoalStore().Snapshot(); ok && state.Status == goal.StatusActive {
		blockers = append(blockers, RetirementBlocker{Category: "autonomous", SessionID: s.id})
	}
	if s.jobManager != nil {
		local, err := s.jobManager.retirementEvidence(s.id)
		blockers = append(blockers, local...)
		if err != nil {
			return blockers, err
		}
	}
	return blockers, nil
}

// retirementNonBlockingEvidenceWithNaming reads this Session's obligations
// whose owner locks cannot be held across admitted durable I/O, so a claim
// attempt may call it while work is admitted without ever waiting on one of
// those locks. It may do bounded filesystem I/O: the job journal is read
// through retirementNonBlockingJournalPending's bounded scan. It is the subset
// of retirementEvidence that omits retirementDelegateBlockers (the delivery and
// child-manager locks) and the shared tree's durable-member validation; the
// attention registry and the job manager's counters are read through their own
// non-blocking forms.
//
// It is deliberately partial, not a claim that the omitted work is always covered
// by a live lease: retirementDelegateBlockers (delegate delivery residue and the
// child-manager locks) and the tree's durable-only member validation can each
// outlive the lease that created them. An early return that takes this read can
// therefore under-report those; it does not invent an obligation it did not read
// (a busy owner lock yields silence in the pre-check), and the full read that
// gates the actual retire/refuse decision runs only once nothing is admitted.
//
// includeNaming keeps this Session's namer's own naming.pending contribution
// (#4055): the claim snapshot passes true; the manual retire's wait pre-check
// passes false, because it waits for a namer and nothing else, and naming.pending
// reports the same "autonomous" category as an active goal, so the namer's
// contribution has to be dropped by identity rather than filtered by category.
// It is also the snapshot/pre-check split for the fail-closed cautions: true
// lets an unreadable source add a shape-honest placeholder to a diagnostic
// snapshot, false keeps the wait pre-check silent on it.
func (s *Session) retirementNonBlockingEvidenceWithNaming(includeNaming bool) []RetirementBlocker {
	blockers := s.retirementLocalBlockersWithNaming(includeNaming)
	blockers = append(blockers, s.retirementNonBlockingAttention(includeNaming)...)
	if state, ok := s.getOrCreateGoalStore().Snapshot(); ok && state.Status == goal.StatusActive {
		blockers = append(blockers, RetirementBlocker{Category: "autonomous", SessionID: s.id})
	}
	if s.jobManager != nil {
		blockers = append(blockers, s.jobManager.retirementNonBlockingEvidence(s.id, includeNaming)...)
	}
	return blockers
}

// retirementAttentionBlocked reports whether the attention registry holds an
// obligation. parked is rootAttentionRailParked's answer, read before
// attentionMu per the lock order that function documents. It takes attentionMu.
func (s *Session) retirementAttentionBlocked(parked bool) bool {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.retirementAttentionBlockedLocked(parked)
}

// retirementAttentionBlockedLocked is the one attention predicate shared by the
// full and non-blocking reads, so the two cannot drift. Callers hold
// attentionMu; parked is the durable rail hold, read before the lock. It never
// calls rootAttentionRailParked itself, so it cannot invert that lock order.
// stableAttentionRetry is deliberately absent: the full read does not count it,
// so counting it here would change the decision read.
func (s *Session) retirementAttentionBlockedLocked(parked bool) bool {
	deferred := s.rootAttentionPaused && !s.rootAttentionWake && !s.rootAttentionRetry.active
	rootAttentionPending := len(s.rootAttentionWakeIDs) != 0 && !parked && !deferred
	return rootAttentionPending || s.rootAttentionRetry.active || s.attentionCallbacks != 0 || len(s.delegateAttentionArmIDs) != 0 || s.delegateAttentionArmRetry.active
}

// retirementNonBlockingAttention reads the attention registry without waiting
// on admitted transcript work. attentionMu is held across a durable transcript
// fold (rearmRootDelegateAttentionFromTranscript), so the read uses TryLock,
// mirroring the tree read's owner TryLock branch (#4055). A busy lock reports
// nothing for the manual retire's wait pre-check, so the retire keeps waiting
// rather than treating contention as clear; reportUnreadable adds the
// shape-honest "notification" caution the diagnostic snapshot uses. parked is
// read before the lock, the order rootAttentionRailParked documents.
func (s *Session) retirementNonBlockingAttention(reportUnreadable bool) []RetirementBlocker {
	if s == nil {
		return nil
	}
	parked := s.rootAttentionRailParked()
	if !s.attentionMu.TryLock() {
		if !reportUnreadable {
			return nil
		}
		return []RetirementBlocker{{Category: "notification", SessionID: s.id}}
	}
	blocked := s.retirementAttentionBlockedLocked(parked)
	s.attentionMu.Unlock()
	if !blocked {
		return nil
	}
	return []RetirementBlocker{{Category: "notification", SessionID: s.id}}
}

// retirementLocalBlockers reads this Session's lease-free in-memory obligations.
// Every lock it takes (s.mu, clientMutationsInitMu, the committed-generation
// stateMu, pendingJobNotifsMu, the goal store) is held only for an in-memory read
// and never across durable I/O, so a caller may invoke it while work is admitted.
func (s *Session) retirementLocalBlockers() []RetirementBlocker {
	return s.retirementLocalBlockersWithNaming(true)
}

// retirementLocalBlockersWithNaming is retirementLocalBlockers with the
// naming.pending contribution optionally omitted; see
// RetirementController.nonBlockingEvidenceRead for why the manual retire's
// pre-check omits it.
func (s *Session) retirementLocalBlockersWithNaming(includeNaming bool) []RetirementBlocker {
	blockers := s.retirementInputBlockers()
	s.mu.Lock()
	if len(s.envWork) != 0 || len(s.disposeRetirement) != 0 || len(s.pendingReLock) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "environment", SessionID: s.id})
	}
	if len(s.askPending) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "question", SessionID: s.id})
	}
	if includeNaming && s.naming.pending != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "autonomous", SessionID: s.id})
	}
	s.mu.Unlock()
	s.pendingJobNotifsMu.Lock()
	if len(s.pendingJobNotifs) != 0 || s.jobNotifyRetry.active || s.jobNotifyCallbacks != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: s.id})
	}
	s.pendingJobNotifsMu.Unlock()
	return blockers
}

func (jm *jobManager) retirementEvidence(sessionID string) ([]RetirementBlocker, error) {
	var blockers []RetirementBlocker
	jm.mu.Lock()
	if len(jm.running) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "job", SessionID: sessionID})
	}
	if jm.watchRetirementPendingLocked() {
		blockers = append(blockers, RetirementBlocker{Category: "watch", SessionID: sessionID})
	}
	jm.mu.Unlock()
	journal, err := retirementJobJournal(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		blockers = append(blockers, RetirementBlocker{Category: "unsupported", SessionID: sessionID})
		return blockers, err
	}
	for _, record := range jobstore.Fold(journal) {
		if record.OwnerSessionID == sessionID && record.NotifyState == jobstore.NotifyPending {
			blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: sessionID})
			break
		}
	}
	return blockers, nil
}

// retirementNonBlockingEvidence reads this manager's lease-free job and watch
// obligations without waiting on admitted work. The running map and the watch
// counters live under jm.mu, which rematerializeDurablePendings also holds
// across jm.store.Load() (session_jobtree_drain.go), so the read takes
// TryLock: a busy manager contributes no counter evidence to the wait
// pre-check, which keeps the retire waiting, and reportUnreadable adds the
// shape-honest "job" caution the diagnostic snapshot uses. No job ID is
// invented: RetirementBlocker carries none for a run that was never read. The
// journal read below runs independently of jm.mu.
func (jm *jobManager) retirementNonBlockingEvidence(sessionID string, reportUnreadable bool) []RetirementBlocker {
	if jm == nil {
		return nil
	}
	var blockers []RetirementBlocker
	if jm.mu.TryLock() {
		if len(jm.running) != 0 {
			blockers = append(blockers, RetirementBlocker{Category: "job", SessionID: sessionID})
		}
		if jm.watchRetirementPendingLocked() {
			blockers = append(blockers, RetirementBlocker{Category: "watch", SessionID: sessionID})
		}
		jm.mu.Unlock()
	} else if reportUnreadable {
		// The unread state is job or watch counters; "job" names the manager's
		// unread process-local work, not a specific run, because none was read.
		blockers = append(blockers, RetirementBlocker{Category: "job", SessionID: sessionID})
	}
	// The pre-check needs one blocker to skip the wait, so the journal scan is
	// skipped when the in-memory counters already supplied one. The diagnostic
	// snapshot keeps every source.
	if reportUnreadable || len(blockers) == 0 {
		if jm.retirementNonBlockingJournalPending(sessionID) {
			blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: sessionID})
		}
	}
	return blockers
}

// watchRetirementPendingLocked reports whether this manager holds watch work
// retirement must refuse over. Caller holds jm.mu.
func (jm *jobManager) watchRetirementPendingLocked() bool {
	watchPending := len(jm.watches) != 0
	for cfg := range jm.terminalFlush {
		watchPending = watchPending || len(cfg.pending) != 0
	}
	// Claimed stable deliveries (receipts awaiting commit) and retained
	// settlement retries are process-only owner state; the session pending-work
	// inventory already treats them as outstanding, so evidence must too.
	return watchPending || len(jm.stableWatchReceipts) != 0 || len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying
}

// retirementNonBlockingJournalMaxBytes bounds the manual retire's journal
// pre-check. jobs.jsonl is append-only and never compacted, so a long-lived
// session can exceed this; that session keeps today's namer wait rather than
// paying unbounded I/O on the retire path. A fold through the job manager's
// store is not an alternative: jobstore.Store.Load takes store.mu, which an
// append holds across durable I/O.
const retirementNonBlockingJournalMaxBytes = 4 << 20

// retirementNonBlockingJournalPending reports whether this manager's journal
// holds an owner NotifyPending record, using a bounded, lock-free read suitable
// for the manual retire's wait pre-check. pendingJobNotifs is not a substitute:
// rematerializeDurablePendings exists because such a record can survive only in
// the durable store.
//
// The read mirrors retirementJobJournal's coherence rule at a bounded scale:
// stat before, scan with MaxBytes, stat after, and accept the fold only when the
// file identity, size and modification time are unchanged and the scan reached
// the end. An empty journal is a genuine clear read. Any error or mismatch
// contributes no evidence, so the pre-check keeps today's wait; it never treats
// an unreadable journal as clear. The strict reader still decides the actual
// refusal. MaxLineBytes is set to the same bound because jobstore checks
// MaxBytes only after a whole line decodes: an append that grows one line past
// the cap is refused instead of buffering the default 128 MiB line limit.
func (jm *jobManager) retirementNonBlockingJournalPending(sessionID string) bool {
	if jm == nil || jm.dir == "" {
		return false
	}
	path := filepath.Join(jm.dir, "jobs.jsonl")
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() > retirementNonBlockingJournalMaxBytes {
		return false
	}
	if before.Size() == 0 {
		return false
	}
	events, offset, err := jobstore.ScanEventsFrom(context.Background(), path, 0, jobstore.ScanLimits{
		MaxBytes:     retirementNonBlockingJournalMaxBytes,
		MaxLineBytes: retirementNonBlockingJournalMaxBytes,
	})
	if err != nil {
		return false
	}
	after, err := os.Stat(path)
	if err != nil || !retirementJournalCoherent(before, after, offset) {
		return false
	}
	for _, record := range jobstore.Fold(events) {
		if record.OwnerSessionID == sessionID && record.NotifyState == jobstore.NotifyPending {
			return true
		}
	}
	return false
}

// retirementJournalCoherent reports whether one journal scan observed a single
// unchanged file from start to finish: the same identity, size and modification
// time, and a scan that reached the file end. Both journal readers require it,
// so a partial prefix can never be read as a complete journal.
func retirementJournalCoherent(before, after os.FileInfo, offset int64) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && offset == after.Size()
}

// retirementJobJournal uses the canonical read-only decoder, not Store.Load:
// the latter repairs partial tails. Eligibility requires every byte to have
// decoded and the required regular journal to retain its observed identity.
// Every stat failure fails closed, including absence: a live job manager's
// journal is created when it materializes (jobstore.Open, agent/jobs.go), so a
// missing one is lost evidence. A cold member that never materialized a job
// manager is the one case where absence is expected, and that call site decides
// it in retirementColdJobJournal.
func retirementJobJournal(path string) ([]jobstore.Event, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("retirement job evidence is not a regular journal")
	}
	events, offset, err := jobstore.ScanEventsFrom(context.Background(), path, 0, jobstore.ScanLimits{})
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	if !retirementJournalCoherent(before, after, offset) {
		return nil, errors.New("retirement job evidence is incomplete or changed during collection")
	}
	return events, nil
}

// retirementJobOutputDirName is the job output store's directory inside a
// member's job directory. newJobManagerWithRestore (agent/jobs.go) creates it,
// and the journal beside it, in the same call.
const retirementJobOutputDirName = "jobs"

// retirementColdJobJournal reads the job journal of an already-enumerated COLD
// member, which may legitimately have none: a member that never materialized a
// job manager has no jobs.jsonl, and its next restore resurrects no job from
// one — jobstore.ScanEventsFrom and jobstore.Store.Load both read a missing
// journal as no events, and the main job path reads it the same way. Eligibility
// has to reach that verdict, or the member is classified unsupported forever and
// can never retire.
//
// Absence alone is not proof of that, though. The constructor that creates the
// journal creates the member's job output directory beside it (jobs.go:626-636),
// so a member that still carries that directory had a journal and no longer has
// one: its absence is lost durable evidence, not a member that never ran a job,
// and stays unsupported. Only a member with neither is one whose restore would
// resurrect nothing. A stat failure of any other kind, on either path, is still
// an error — the static evidence checks below and the live job manager keep
// their own strict readers.
func retirementColdJobJournal(dir string) ([]jobstore.Event, error) {
	path := filepath.Join(dir, "jobs.jsonl")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, outputErr := os.Stat(filepath.Join(dir, retirementJobOutputDirName)); errors.Is(outputErr, os.ErrNotExist) {
			return nil, nil
		}
	}
	return retirementJobJournal(path)
}

// retirementColdEvidence validates only this already-enumerated cold member.
// It neither walks the shared tree nor opens a runtime or append-capable store.
func retirementColdEvidence(stateDir, sessionID, delegateID string) ([]RetirementBlocker, error) {
	events, err := retirementColdJobJournal(jobsDir(stateDir, sessionID))
	if err != nil {
		return []RetirementBlocker{{Category: "unsupported", SessionID: sessionID, DelegateID: delegateID}}, err
	}
	// Fold the same content restore would resurrect: an unreconciled
	// nonterminal job, an armed-but-undelivered notification, an active watch,
	// or a pending watch send all become work again when the member materializes.
	var blockers []RetirementBlocker
	for _, record := range jobstore.Fold(events) {
		if record.OwnerSessionID != "" && record.OwnerSessionID != sessionID {
			continue
		}
		if !record.Status.IsTerminal() {
			blockers = append(blockers, RetirementBlocker{Category: "job", SessionID: sessionID, DelegateID: delegateID})
			continue
		}
		if record.NotifyState == jobstore.NotifyPending {
			blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: sessionID, DelegateID: delegateID})
		}
	}
	watchPending := len(jobstore.FoldWatchSends(events).Pending) != 0
	for _, watch := range jobstore.FoldWatches(events) {
		if watch.Active && (watch.OwnerSessionID == "" || watch.OwnerSessionID == sessionID) {
			watchPending = true
		}
	}
	if watchPending {
		blockers = append(blockers, RetirementBlocker{Category: "watch", SessionID: sessionID, DelegateID: delegateID})
	}
	// A cold member's next restore opens these durable stores from their primary
	// files, so a malformed one must block retirement here rather than fail the
	// restore later. The strict readers are the ones restore itself uses, they
	// never repair a partial write, and a missing file is not an error: only a
	// present-but-corrupt store is evidence the restore cannot reconstruct the
	// member.
	if _, err := loadClientMutationSnapshotFS(afero.NewOsFs(), stateDir, sessionID); err != nil {
		return append(blockers, RetirementBlocker{Category: "unsupported", SessionID: sessionID, DelegateID: delegateID}), fmt.Errorf("retirement cold evidence: client mutation snapshot: %w", err)
	}
	if err := task.NewTaskStore(stateDir, sessionID).Load(); err != nil {
		return append(blockers, RetirementBlocker{Category: "unsupported", SessionID: sessionID, DelegateID: delegateID}), fmt.Errorf("retirement cold evidence: task store: %w", err)
	}
	return blockers, nil
}
