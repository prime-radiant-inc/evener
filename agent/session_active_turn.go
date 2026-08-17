package agent

import (
	"fmt"
	"time"

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

// turnNameRefusal says WHY mintRunningTurnID declined to name a turn. An empty
// id alone conflates three situations that want three different answers from
// the caller: a name another mutation is running will be handed back, a store
// that would not take the write says nothing at all about the name, and a
// session nobody serves never wanted one. Collapsing the middle case into the
// others is what let a failing disk read as a stale name (kata ajg5).
type turnNameRefusal int

const (
	// turnNameMinted is the non-refusal: an id was taken.
	turnNameMinted turnNameRefusal = iota
	// turnNameUnserved: no daemon drains this session, so no client can address
	// its turns and none needs a durable name. Not a failure.
	turnNameUnserved
	// turnNameHeld: another mutation owns the slot. Whether THIS name comes
	// back is a separate question (runningTurnNameHasOwner); that it is held
	// is what this reports.
	turnNameHeld
	// turnNameFenced: an accepted interrupt is already ending a turn. Distinct
	// from turnNameHeld because a fence over a turn the daemon never named
	// carries the EMPTY name that turn had, so runningTurnNameHasOwner -- which
	// asks about ActiveTurnID alone -- answers false for it. Collapsing the two
	// sent a Stop pressed on an unnamed turn into the stale-name arm, which
	// warns and does not re-arm; the fence clears two durable writes later.
	turnNameFenced
	// turnNameStoreFailed: the durable store could not be opened or written.
	// The name may be perfectly free; nobody can tell from here.
	turnNameStoreFailed
)

// mintRunningTurnID names the turn that is about to run and records it as the
// durable authority, or returns "" when this turn must run unnamed, with the
// reason. The caller carries the id to the turn's opening event; "" there means
// the daemon could not name this turn.
func (s *Session) mintRunningTurnID() (string, turnNameRefusal) {
	// A session nobody serves has no client to name a turn to. In-process
	// subagents share the parent's StateDir (subagents.go), so this gate is
	// the difference between a durable write per delegate wake and none.
	if !s.servedByDaemon() {
		return "", turnNameUnserved
	}
	if err := s.ensureClientMutationStore(); err != nil {
		s.warnStoreUnhealthyOnce(fmt.Sprintf("open client mutation store: %v", err))
		return "", turnNameStoreFailed
	}
	var turnID string
	refusal := turnNameMinted
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		// Both refusals mirror every other durable entry point: an interrupt
		// is already ending a turn, or a mutation already owns the slot.
		// Running unnamed is the pre-existing behaviour for this turn kind, so
		// refusing costs nothing that was not already lost. Which one it was is
		// carried out because the caller waits differently for each.
		if snapshot.InterruptFence != nil {
			refusal = turnNameFenced
			return nil
		}
		if snapshot.ActiveTurnID != "" {
			refusal = turnNameHeld
			return nil
		}
		var record clientMutationRecord
		reserveClientMutationTurnID(snapshot, &record)
		snapshot.ActiveTurnID = record.StableTurnID
		turnID = record.StableTurnID
		refusal = turnNameMinted
		return nil
	}); err != nil {
		s.warnStoreUnhealthyOnce(fmt.Sprintf("name running turn failed: %v", err))
		return "", turnNameStoreFailed
	}
	// The store took a write, so the next failure is news again.
	s.clearStoreUnhealthyWarning()
	return turnID, refusal
}

// warnStoreUnhealthyOnce reports a client-mutation-store failure at most once
// per unhealthy episode, and clearStoreUnhealthyWarning ends the episode on the
// next write the store accepts.
//
// The latch exists because this warning is on a retry loop. s.emit for
// EventWarning fires the user's Notification hook unconditionally
// (session_events.go's emitWithProvenance), unlike emitDiagnosticWarning, so a
// store that is permanently unwritable -- a read-only mount, a full disk, a
// deleted state dir -- otherwise costs a thread-visible warning AND a hook
// subprocess on every retry for the life of the daemon. The diagnostic is worth
// saying; it is worth saying once, and the hook is what makes repeating it
// expensive rather than merely noisy.
func (s *Session) warnStoreUnhealthyOnce(message string) {
	s.turnNameRetryMu.Lock()
	alreadyWarned := s.turnNameStoreUnhealthy
	s.turnNameStoreUnhealthy = true
	s.turnNameRetryMu.Unlock()
	if alreadyWarned {
		return
	}
	s.emit(events.EventWarning, events.WarningData{Message: message})
}

func (s *Session) clearStoreUnhealthyWarning() {
	s.turnNameRetryMu.Lock()
	s.turnNameStoreUnhealthy = false
	s.turnNameRetryMu.Unlock()
}

// scheduleRunningTurnNameRetry arms ONE paced wake for a notification that
// stood down because it could not name its turn.
//
// It replaces the immediate notify() that stand-down used to fire. That kick
// was a hot loop for as long as its condition held: the serve loop reads the
// wake, finds the name still unavailable, stands down and kicks again. The
// condition was assumed to be brief -- a client turn/start that would finish
// and hand the name back -- but a mutation store failing writes holds it
// indefinitely, because the pending turn can then neither be claimed nor
// released. The guard that was supposed to catch a name nobody would return
// (runningTurnNameHasOwner) reads such a turn as owned, so it passes.
//
// The wake stays guaranteed rather than best-effort; only its timing changes.
// Stand-downs coalesce into the one armed timer, and each firing that does not
// clear the condition doubles the delay to the same ceiling job notifications
// use, so a persistently unhappy disk costs a wake every few seconds instead of
// a full core.
func (s *Session) scheduleRunningTurnNameRetry(ceiling time.Duration) {
	s.turnNameRetryMu.Lock()
	if s.turnNameRetry.active {
		s.turnNameRetryMu.Unlock()
		return
	}
	delay := s.turnNameRetry.delay
	if delay <= 0 {
		delay = jobNotificationRetryInitialDelay
	}
	s.turnNameRetry.active = true
	s.turnNameRetry.generation++
	generation := s.turnNameRetry.generation
	s.turnNameRetryMu.Unlock()
	s.sclock().AfterFunc(delay, func() {
		s.turnNameRetryMu.Lock()
		if s.turnNameRetry.generation != generation {
			s.turnNameRetryMu.Unlock()
			return
		}
		s.turnNameRetry.active = false
		s.turnNameRetry.delay = min(delay*2, ceiling)
		s.turnNameRetryMu.Unlock()
		s.notify()
	})
}

// resetRunningTurnNameRetry drops the backoff and cancels any armed retry once
// a wake has been named. Bumping the generation is what stops an already-armed
// timer firing a wake nothing is waiting for.
func (s *Session) resetRunningTurnNameRetry() {
	s.turnNameRetryMu.Lock()
	s.turnNameRetry.generation++
	s.turnNameRetry.active = false
	s.turnNameRetry.delay = jobNotificationRetryInitialDelay
	s.turnNameRetryMu.Unlock()
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
