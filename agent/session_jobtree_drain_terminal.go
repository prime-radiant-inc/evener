package agent

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// hasAcceptedTerminalCommunicate reports whether the model has explicitly
// ended the turn that ends the process: a communicate with end_turn=true was
// accepted while TurnEndsProcess. From that point on there is no future model
// turn to deliver leftovers to (issue #329) — new deliverables can only come
// from work that was still LIVE at that moment, which the drain keeps
// settling exactly as before.
func (s *Session) hasAcceptedTerminalCommunicate() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalCommunicateAccepted
}

// discardTerminalDrainLeftovers disposes of this session's undelivered
// notification leftovers once the model has ended the process-ending turn:
// the in-memory queue is dropped, and every owned durable NotifyPending record
// with no live run is settled as CONSUMED — the ledger state for "the owner no
// longer needs a notification turn for this", which is exactly the terminal
// disposition. Jobs still in the running map are skipped: they are live work,
// their finalization owns their notification, and anything they produce after
// this point is a fresh completion the drain still delivers (PRI-2441).
//
// The one-shot drain calls this once, at entry, so the cut is temporal: what
// was already pending when the terminal communicate ended the run is
// discarded; what live work produces afterwards is delivered as always.
func (s *Session) discardTerminalDrainLeftovers() error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	jm := s.jobManager
	dropped := s.drainJobNotifications()
	// Same lock discipline as rematerializeDurablePendings: the durable load
	// and the running-map snapshot under ONE jm.mu hold, so a finalizing job is
	// either still visibly running (skip: its finalization owns the
	// notification) or its NotifyPending append is already visible.
	jm.mu.Lock()
	recs, err := jm.store.Load()
	if err != nil {
		jm.mu.Unlock()
		return err
	}
	liveRunning := make(map[string]struct{}, len(jm.running))
	for id := range jm.running {
		liveRunning[id] = struct{}{}
	}
	jm.mu.Unlock()
	var consumed []string
	for _, rec := range recs {
		if !isOwnedDrainJob(rec, jm.sessionID) {
			continue
		}
		if rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
			continue
		}
		if _, live := liveRunning[rec.JobID]; live {
			continue
		}
		consumeTerminalJobNotification(s, jm, rec)
		consumed = append(consumed, rec.JobID)
	}
	sort.Strings(consumed)
	droppedIDs := make([]string, 0, len(dropped))
	for _, n := range dropped {
		droppedIDs = append(droppedIDs, n.JobID)
	}
	if len(dropped) > 0 || len(consumed) > 0 {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf(
			"terminal communicate already ended this run; discarding undelivered notification leftovers (queued: %s; pending: %s)",
			joinOrNone(droppedIDs), joinOrNone(consumed))})
	}
	return nil
}

// subtreeHasLiveTerminalDrainWork reports whether anything in this session's
// managed-job subtree can still PRODUCE a deliverable after the model has
// ended the process-ending turn: an outstanding managed job of its own
// (running, or terminal with an undelivered owner notification the drain
// settles on its own), or an active / non-gated descendant. It is
// treeHasOutstandingWork minus the residue signals — pending watch sends,
// queued-but-stale notifications, pending delegate deliveries and attention —
// which, with no live work behind them, have no future model turn to be
// delivered to (issue #329). Deliverable-right-now items never reach this
// check: the drain loop's own turn gate (peekNotifications / root attention)
// runs first each pass.
//
// It walks the same liveDirectSubagents path as treeHasOutstandingWork,
// skipping stop-gated, fatally failed, and drain-abandoned children
// identically: they are never driven, so nothing they hold can convert.
func (s *Session) subtreeHasLiveTerminalDrainWork() (bool, error) {
	if s.jobManager != nil {
		n, err := s.jobManager.outstandingDrainJobCount()
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	for _, sub := range s.liveDirectSubagents() {
		sub.mu.Lock()
		active := sub.running || sub.finalizing || sub.driving
		child := sub.sess
		sub.mu.Unlock()
		if child != nil && (s.childStopGated(child.id) || s.childFatalRunGated(child.id) || s.childDrainAbandoned(child.id)) {
			continue
		}
		if active {
			return true, nil
		}
		if child != nil {
			live, err := child.subtreeHasLiveTerminalDrainWork()
			if err != nil {
				return false, err
			}
			if live {
				return true, nil
			}
		}
	}
	return false, nil
}

func joinOrNone(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}
