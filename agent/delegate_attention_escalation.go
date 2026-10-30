package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"primeradiant.com/evener/agent/events"
)

// errDelegateAttentionSourceMissing marks an escalation that failed because
// the delegate's own transcript, the source of the attention it hands over,
// does not exist.
var errDelegateAttentionSourceMissing = errors.New("delegate attention source transcript missing")

// parkedEscalationStandsDown reports whether a parked delegate's failed
// escalation should wait with it (new attention or a restart) instead of
// re-arming the retry: only when its transcript is gone, which no retry can
// bring back. The delegate already said it is undeliverable. Any other
// failure (appending the hand-over to the root, resolving the source) may
// clear, so it keeps the retry.
func parkedEscalationStandsDown(err error) bool {
	return errors.Is(err, errDelegateAttentionSourceMissing)
}

// escalateUnreachableDelegateAttention transfers permanently fenced wakes to
// the root, preserving identity/content and idempotent crash replay.
func (s *Session) escalateUnreachableDelegateAttention() bool {
	progressed, failed := false, false
	for _, plan := range s.delegateController.permanentlyFencedDelegateAttention() {
		readFold := readDelegateAttentionFold
		if plan.parked {
			readFold = readExistingDelegateAttentionFold
		}
		if err := s.escalateOneUnreachableDelegateAttention(plan, readFold); err != nil {
			s.warnDelegateAttentionFailed(delegateAttentionEscalateLabel, plan.delegateID, err)
			failed = failed || !plan.parked || !parkedEscalationStandsDown(err)
			continue
		}
		s.delegateAttentionWarningResolved(delegateAttentionEscalateLabel, plan.delegateID)
		progressed = true
	}
	if failed {
		s.scheduleDelegateAttentionRetry()
	} else if progressed {
		s.resetDelegateAttentionRetryDelay()
	}
	return progressed
}

// escalateOneUnreachableDelegateAttention transfers plan's attention to the
// root, reading the source transcript's fold with readFold.
func (s *Session) escalateOneUnreachableDelegateAttention(plan delegateFencedAttentionEscalation, readFold func(path, expectedSessionID string) (delegateAttentionFold, error)) error {
	sourcePath, sourceSessionID, err := delegateTranscriptPathFromRef(s.delegateController.stateDir, plan.transcriptRef)
	if err != nil {
		return err
	}
	fold, err := readFold(sourcePath, sourceSessionID)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %w", errDelegateAttentionSourceMissing, err)
	}
	if err != nil {
		return err
	}
	for _, attentionID := range plan.attentionIDs {
		message, exists := fold.content[attentionID]
		if _, resolved := fold.resolutions[attentionID]; resolved || !exists {
			// Already durably resolved (or never durable): only the stale
			// process-local wake remains.
			s.delegateController.forgetDelegateAttention(plan.delegateID, attentionID)
			continue
		}
		if _, err := s.appendDelegateAttentionMessageDurably(attentionID, message); err != nil {
			return err
		}
		if err := s.armDelegateAttention(attentionID); err != nil {
			return err
		}
		if err := s.delegateController.resolveDelegateAttentionDurably(plan.runtime, plan.transcriptRef, []string{attentionID}, delegateAttentionDiscarded); err != nil {
			return err
		}
		s.delegateController.forgetDelegateAttention(plan.delegateID, attentionID)
	}
	return nil
}

// The failing delegate-attention actions the retry loop warns about.
const (
	delegateAttentionRestoreLabel  = "restore delegate attention"
	delegateAttentionEscalateLabel = "escalate unreachable delegate attention"
)

// delegateAttentionWarning names one failure episode: an action failing for
// one delegate.
type delegateAttentionWarning struct {
	action     string
	delegateID string
}

// warnDelegateAttentionFailed reports that action failed for delegateID,
// once per failure episode. The drive retries with backoff (up to every 5s)
// for as long as the attention is owed, and one warning per attempt flooded
// the transcript with the same line. It warns again when the error changes,
// and after the episode ends (delegateAttentionWarningResolved, or
// forgetSettledDelegateAttentionWarnings once the attention is no longer
// owed). The message carries the error, bounded (warningDataFromError); the
// warning has its own code, so clients show it only at their most detailed
// level; and the daemon log gets the whole error, recoverable wherever the
// transcript hides it.
func (s *Session) warnDelegateAttentionFailed(action, delegateID string, err error) {
	cause := err.Error()
	key := delegateAttentionWarning{action: action, delegateID: delegateID}
	s.delegateAttentionWarnMu.Lock()
	previous, warned := s.delegateAttentionWarned[key]
	if s.delegateAttentionWarned == nil {
		s.delegateAttentionWarned = make(map[delegateAttentionWarning]string)
	}
	s.delegateAttentionWarned[key] = cause
	s.delegateAttentionWarnMu.Unlock()
	if warned && previous == cause {
		return
	}
	slog.Warn("delegate attention failed", "action", action, "session", s.ID(), "delegate", delegateID, "error", cause)
	data := warningDataFromError(action, err)
	data.Code = events.WarningCodeDelegateAttentionRestore
	s.emit(data)
}

// delegateAttentionWarningResolved ends action's failure episode for
// delegateID, so the next failure warns again.
func (s *Session) delegateAttentionWarningResolved(action, delegateID string) {
	s.delegateAttentionWarnMu.Lock()
	delete(s.delegateAttentionWarned, delegateAttentionWarning{action: action, delegateID: delegateID})
	s.delegateAttentionWarnMu.Unlock()
}

// forgetSettledDelegateAttentionWarnings ends the failure episode of every
// delegate that no longer owes attention (delivered, retired, escalated or
// fenced), so attention owed again later warns afresh.
func (s *Session) forgetSettledDelegateAttentionWarnings() {
	s.delegateAttentionWarnMu.Lock()
	defer s.delegateAttentionWarnMu.Unlock()
	for key := range s.delegateAttentionWarned {
		if !s.delegateController.owesDelegateAttention(key.delegateID) {
			delete(s.delegateAttentionWarned, key)
		}
	}
}

// maxDelegateAttentionRestoreFailures is how many consecutive restores of a
// delegate's cold runtime may fail, for a reason that is not transient,
// before the drive stops retrying. It counts attempts, not time: at the
// retry's 5s cap it is about five minutes, but every pass counts, and a
// busy root (deliveries, job events) drives passes sooner, so it can come
// earlier.
const maxDelegateAttentionRestoreFailures = 64

// countDelegateAttentionRestoreFailure records a failed restore of
// delegateID's cold runtime and gives up on it at the limit. A transient
// failure (isTransientStartFailure) clears on its own and is not counted.
func (s *Session) countDelegateAttentionRestoreFailure(delegateID string, err error) {
	if err == nil || isTransientStartFailure(err) {
		return
	}
	if s.delegateController.recordDelegateAttentionRestoreFailure(delegateID) >= s.delegateAttentionGiveUpAfter() {
		s.giveUpDelegateAttention(delegateID, err)
	}
}

func (s *Session) delegateAttentionGiveUpAfter() int {
	if n := s.cfg.testOnly.delegateAttentionGiveUpAfter; n > 0 {
		return n
	}
	return maxDelegateAttentionRestoreFailures
}

// giveUpDelegateAttention stops retrying delegateID's restore and hands its
// owed attention to the root, the way attention a closed ancestor fences off
// is escalated: the root receives each message under its original identity
// and the source is resolved, so nothing is dropped. When the hand-over
// fails too, the delegate is parked, out of the drive's cold restores until
// new attention arrives or the daemon restarts, and the session says so once
// at every level and in the daemon log.
func (s *Session) giveUpDelegateAttention(delegateID string, restoreErr error) {
	plan, ok := s.delegateController.giveUpAttentionPlan(delegateID)
	if !ok {
		return
	}
	// The fenced escalation's lenient read treats a missing transcript as
	// empty (attention never made durable) and forgets the ids, which here
	// would drop owed attention as though delivered. A delegate whose
	// transcript is gone has nothing to hand over, so the hand-over reads it
	// strictly and a missing transcript fails it, in the same read the
	// transfer uses.
	escalateErr := s.escalateOneUnreachableDelegateAttention(plan, readExistingDelegateAttentionFold)
	if escalateErr == nil {
		s.delegateController.delegateAttentionRestored(delegateID)
		s.delegateAttentionWarningResolved(delegateAttentionRestoreLabel, delegateID)
		slog.Warn("delegate attention handed to the root after repeated restore failures", "session", s.ID(), "delegate", delegateID, "error", restoreErr.Error())
		return
	}
	// Overlapping passes may both give up; only the one that parks says so.
	if !s.delegateController.parkDelegateAttention(delegateID) {
		return
	}
	slog.Warn("delegate attention undeliverable", "session", s.ID(), "delegate", delegateID, "restore_cause", restoreErr.Error(), "handover_cause", escalateErr.Error())
	data := warningDataFromError("Evener stopped trying to deliver a subagent's message until the subagent has something new or Evener restarts: it could not be restored or handed to this session", escalateErr)
	data.Code = events.WarningCodeDelegateAttentionUndeliverable
	data.DelegateID = delegateID
	s.emit(data)
}
