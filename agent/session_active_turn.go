package agent

import (
	"fmt"

	"primeradiant.com/serf/agent/events"
)

// A turn's identity has one owner: whatever opened it. A client's turn/start
// and a queued message claimed off the input queue arrive already named — the
// reservation before the turn runs is what makes them retry-safe across a
// crash. Everything else — a goal continuation, a job or delegate
// notification wake — is named here, so the id the daemon publishes is always
// an id the mutation preconditions accept, and Steer, Send and Stop work on
// every turn rather than only on the ones a client started.
//
// The id is minted, never adopted. An ActiveTurnID this call did not write
// belongs to a mutation that is about to run; taking it would let a Stop
// aimed at that mutation cancel this turn instead, and mark a message the
// user sent — and the session never ran — "interrupted".

// servedByDaemon reports whether a daemon is draining this session's events,
// which is the same thing as "a client can address this session's turns".
// ConsumeEventsLossless is the only writer of the flag and has only root-path
// callers, so in-process subagents and one-shot CLI runs read false.
//
// It gates both halves of naming a turn: whether to reserve a durable id at
// all, and whether to announce the turn on the wire. A session no client
// watches needs neither, and paying for them would put a durable write on
// every delegate wake.
func (s *Session) servedByDaemon() bool {
	s.eventsMu.RLock()
	defer s.eventsMu.RUnlock()
	return s.authoritativeConsumer
}

// mintRunningTurnID names the turn that is about to run and records it as the
// durable authority, or returns "" when this turn must run unnamed. The caller
// carries the result to the turn's opening event; "" there means the daemon
// could not name this turn, and the projection must then publish no active
// status for it — a control the composer offers against an id the daemon does
// not hold is rejected with nothing shown.
func (s *Session) mintRunningTurnID() string {
	// A session nobody serves has no client to name a turn to. In-process
	// subagents share the parent's StateDir (subagents.go), so this gate is
	// the difference between a durable write per delegate wake and none.
	if !s.servedByDaemon() {
		return ""
	}
	if err := s.ensureClientMutationStore(); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("open client mutation store: %v", err),
		})
		return ""
	}
	var turnID string
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		// Both refusals mirror every other durable entry point: an interrupt
		// is already ending a turn, or a mutation already owns the slot.
		// Running unnamed is the pre-existing behaviour for this turn kind, so
		// refusing costs nothing that was not already lost.
		if snapshot.InterruptFence != nil {
			return nil
		}
		if snapshot.ActiveTurnID != "" {
			return nil
		}
		var record clientMutationRecord
		reserveClientMutationTurnID(snapshot, &record)
		snapshot.ActiveTurnID = record.StableTurnID
		turnID = record.StableTurnID
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("name running turn failed: %v", err),
		})
		return ""
	}
	return turnID
}

// runningTurnNameHasOwner reports whether a pending client mutation owns the
// name currently held -- which is the same thing as "somebody is going to run
// that turn and then release it".
//
// It is the live reading of the rule forgetRunningTurnNoOneOwns applies at
// load: a name no pending execution claims belongs to a turn that will never
// finish, because there is nothing left to finish it. A caller that wants to
// wait for the name must not wait on one of those.
func (s *Session) runningTurnNameHasOwner() bool {
	if s.clientMutations == nil {
		return false
	}
	snapshot := s.clientMutations.snapshot()
	if snapshot.ActiveTurnID == "" {
		return false
	}
	for _, pending := range snapshot.PendingExecutions {
		if pending.TurnID == snapshot.ActiveTurnID {
			return true
		}
	}
	return false
}

// releaseRunningTurnID clears an id minted by mintRunningTurnID. It is a
// no-op for any other id, so a turn that ended after a client mutation took
// the slot cannot clear that mutation's identity out from under its own
// settle path.
func (s *Session) releaseRunningTurnID(turnID string) {
	if turnID == "" || s.clientMutations == nil {
		return
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		if snapshot.ActiveTurnID == turnID {
			snapshot.ActiveTurnID = ""
		}
		return nil
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{
			Message: fmt.Sprintf("release running turn failed: %v", err),
		})
	}
}
