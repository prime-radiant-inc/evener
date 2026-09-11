package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// retirementEvidence reads only this Session's owners. The shared delegate
// controller invokes it once per exact resident, outside its mutex and before
// revalidating the captured tree version and pointers. It never scans that tree.
func (s *Session) retirementEvidence() ([]RetirementBlocker, error) {
	blockers := s.retirementInputBlockers()
	blockers = append(blockers, s.retirementDelegateBlockers()...)
	s.mu.Lock()
	if len(s.envWork) != 0 || len(s.disposeRetirement) != 0 || len(s.pendingReLock) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "environment", SessionID: s.id})
	}
	if len(s.askPending) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "question", SessionID: s.id})
	}
	if s.naming.pending != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "autonomous", SessionID: s.id})
	}
	s.mu.Unlock()
	s.pendingJobNotifsMu.Lock()
	if len(s.pendingJobNotifs) != 0 || s.jobNotifyRetry.active || s.jobNotifyCallbacks != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: s.id})
	}
	s.pendingJobNotifsMu.Unlock()
	s.attentionMu.Lock()
	if len(s.rootAttentionWakeIDs) != 0 || s.rootAttentionRetry.active || s.attentionCallbacks != 0 || len(s.delegateAttentionArmIDs) != 0 || s.delegateAttentionArmRetry.active {
		blockers = append(blockers, RetirementBlocker{Category: "notification", SessionID: s.id})
	}
	s.attentionMu.Unlock()
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

func (jm *jobManager) retirementEvidence(sessionID string) ([]RetirementBlocker, error) {
	var blockers []RetirementBlocker
	jm.mu.Lock()
	if len(jm.running) != 0 {
		blockers = append(blockers, RetirementBlocker{Category: "job", SessionID: sessionID})
	}
	watchPending := len(jm.watches) != 0
	for cfg := range jm.terminalFlush {
		watchPending = watchPending || len(cfg.pending) != 0
	}
	// Claimed stable deliveries (receipts awaiting commit) and retained
	// settlement retries are process-only owner state; the session pending-work
	// inventory already treats them as outstanding, so evidence must too.
	watchPending = watchPending || len(jm.stableWatchReceipts) != 0 || len(jm.stableWatchSettlementRetries) != 0 || jm.stableWatchSettlementRetrying
	if watchPending {
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

// retirementJobJournal uses the canonical read-only decoder, not Store.Load:
// the latter repairs partial tails. Eligibility requires every byte to have
// decoded and the required regular journal to retain its observed identity.
func retirementJobJournal(path string) ([]jobstore.Event, error) {
	before, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("retirement job evidence is not a regular journal")
	}
	events, offset, err := jobstore.ScanEventsFrom(context.Background(), path, 0, jobstore.ScanLimits{})
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("retirement job evidence: %w", err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || offset != after.Size() {
		return nil, fmt.Errorf("retirement job evidence is incomplete or changed during collection")
	}
	return events, nil
}

// retirementColdEvidence validates only this already-enumerated cold member.
// It neither walks the shared tree nor opens a runtime or append-capable store.
func retirementColdEvidence(stateDir, sessionID, delegateID string) ([]RetirementBlocker, error) {
	events, err := retirementJobJournal(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"))
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
	return blockers, nil
}
