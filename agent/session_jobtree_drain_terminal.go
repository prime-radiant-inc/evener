package agent

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
)

// terminalNotificationIdentity names one durable terminal-notification
// generation. Job IDs alone are not sufficient: a stale pre-cut disposition
// must never consume a later notification generation carrying the same ID.
type terminalNotificationIdentity struct {
	jobID       string
	terminalGen string
}

// terminalNotificationCut is the exact acceptance-time boundary between
// leftovers and future work. durable contains owned, non-running NotifyPending
// generations observed under jm.mu. queueSeq is the last in-memory queue
// sequence observed while the same jm.mu hold prevented finalization from
// crossing its pending-record/running-map handoff.
type terminalNotificationCut struct {
	durable  map[terminalNotificationIdentity]struct{}
	queueSeq uint64
}

func terminalIdentity(jobID, terminalGen string) terminalNotificationIdentity {
	return terminalNotificationIdentity{jobID: jobID, terminalGen: terminalGen}
}

// captureTerminalNotificationCut takes and durably records the terminal
// temporal cut. The durable load, running-map classification, consumed-event
// batch, and queue watermark are one atomic ordering boundary with job
// finalization: armFinalizedJob cannot delete a live run while jm.mu is held,
// and it cannot enqueue the terminal owner notification until after that
// delete. Thus a finalizer is wholly on one side of this cut:
//
//   - still running: excluded, and its later enqueue is fresh;
//   - no longer running with NotifyPending durable: included by generation,
//     even if its matching queue enqueue has not executed yet.
func (s *Session) captureTerminalNotificationCut() (terminalNotificationCut, error) {
	cut := terminalNotificationCut{durable: make(map[terminalNotificationIdentity]struct{})}
	if s == nil || s.jobManager == nil {
		return cut, nil
	}
	jm := s.jobManager
	jm.mu.Lock()
	if hook := s.cfg.testOnly.terminalCutAfterManagerLock; hook != nil {
		hook()
	}
	recs, err := jm.store.Load()
	if err != nil {
		jm.mu.Unlock()
		return terminalNotificationCut{}, err
	}
	for _, rec := range recs {
		if !isOwnedDrainJob(rec, jm.sessionID) || rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
			continue
		}
		if _, live := jm.running[rec.JobID]; live {
			continue
		}
		cut.durable[terminalIdentity(rec.JobID, rec.TerminalGen)] = struct{}{}
	}
	s.pendingJobNotifsMu.Lock()
	cut.queueSeq = s.nextJobNotifSeq
	s.pendingJobNotifsMu.Unlock()

	ids := make([]terminalNotificationIdentity, 0, len(cut.durable))
	for id := range cut.durable {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].jobID == ids[j].jobID {
			return ids[i].terminalGen < ids[j].terminalGen
		}
		return ids[i].jobID < ids[j].jobID
	})
	consumedEvents := make([]jobstore.Event, 0, len(ids))
	for _, id := range ids {
		consumedEvents = append(consumedEvents, jobstore.Event{
			Kind:        jobstore.EventJobNotificationConsumed,
			TS:          jm.now(),
			JobID:       id.jobID,
			TerminalGen: id.terminalGen,
		})
	}
	if err := jm.appendJobEvents(consumedEvents); err != nil {
		jm.mu.Unlock()
		return terminalNotificationCut{}, err
	}
	jm.mu.Unlock()
	// The owner disposition is already durable and therefore survives a crash
	// before DrainJobTree. Forwarding the parent's drive-signal copy retains the
	// existing consume semantics; a forwarding failure remains observable but
	// cannot roll back the committed owner cut.
	for _, consumed := range consumedEvents {
		if err := jm.forwardSnapshot(consumed); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("terminal notification cut forward failed: %v", err)})
		}
	}
	return cut, nil
}

func (s *Session) acceptTerminalCommunicate() error {
	s.mu.Lock()
	alreadyAccepted := s.terminalCommunicateAccepted
	s.mu.Unlock()
	if alreadyAccepted {
		return nil
	}
	cut, err := s.captureTerminalNotificationCut()
	if err != nil {
		return err
	}
	s.mu.Lock()
	if !s.terminalCommunicateAccepted {
		s.terminalNotificationCut = cut
		s.terminalCommunicateAccepted = true
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) acceptedTerminalNotificationCut() (terminalNotificationCut, bool) {
	if s == nil {
		return terminalNotificationCut{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.terminalCommunicateAccepted {
		return terminalNotificationCut{}, false
	}
	cut := terminalNotificationCut{
		durable:  make(map[terminalNotificationIdentity]struct{}, len(s.terminalNotificationCut.durable)),
		queueSeq: s.terminalNotificationCut.queueSeq,
	}
	for id := range s.terminalNotificationCut.durable {
		cut.durable[id] = struct{}{}
	}
	return cut, true
}

// hasAcceptedTerminalCommunicate reports whether the model has explicitly
// ended the turn that ends the process: a communicate with end_turn=true was
// accepted while TurnEndsProcess. From that point on there is no future model
// turn to deliver leftovers to (issue #329) — new deliverables can only come
// from work that was still LIVE at that moment, which the drain keeps
// settling exactly as before.
func (s *Session) hasAcceptedTerminalCommunicate() bool {
	_, accepted := s.acceptedTerminalNotificationCut()
	return accepted
}

// discardTerminalDrainLeftovers disposes of this session's undelivered
// notification leftovers captured when the model ended the process-ending
// turn. It never takes a new snapshot: only queue sequences and exact durable
// generations present in cut are eligible. A completion that lands between
// acceptance and drain entry is therefore preserved for its provider turn.
func (s *Session) discardTerminalDrainLeftovers(cut terminalNotificationCut) error {
	if s == nil || s.jobManager == nil {
		return nil
	}
	jm := s.jobManager
	dropped := s.discardTerminalQueueCut(cut)
	jm.mu.Lock()
	recs, err := jm.store.Load()
	jm.mu.Unlock()
	if err != nil {
		return err
	}
	var consumed []string
	for id := range cut.durable {
		rec := recs[id.jobID]
		if rec == nil || rec.TerminalGen != id.terminalGen || !isOwnedDrainJob(rec, jm.sessionID) {
			continue
		}
		if rec.NotifyState != jobstore.NotifyPending || rec.TerminalGen == "" {
			continue
		}
		markTerminalJobNotificationConsumed(s, jm, rec, false)
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

func (s *Session) discardTerminalQueueCut(cut terminalNotificationCut) []jobNotification {
	s.pendingJobNotifsMu.Lock()
	defer s.pendingJobNotifsMu.Unlock()
	dropped := make([]jobNotification, 0)
	kept := s.pendingJobNotifs[:0]
	for _, n := range s.pendingJobNotifs {
		_, durableLeftover := cut.durable[terminalIdentity(n.JobID, n.TerminalGen)]
		preCutSequence := n.queueSeq != 0 && n.queueSeq <= cut.queueSeq
		if preCutSequence || (!n.isWatch() && durableLeftover) {
			dropped = append(dropped, n)
			continue
		}
		kept = append(kept, n)
	}
	s.pendingJobNotifs = kept
	return dropped
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
