package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
)

// boundaryRecorder collects the events a served session emits. Registering a
// real drain through ConsumeEventsLossless is also what marks the session
// served (session.go:109-123 makes that the only writer), so this is both the
// collector and the thing that makes turns addressable.
type boundaryRecorder struct {
	mu     sync.Mutex
	kinds  []events.EventKind
	starts []events.TurnStartedData
}

func serveAndRecord(t *testing.T, s *Session) *boundaryRecorder {
	t.Helper()
	rec := &boundaryRecorder{}
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.kinds = append(rec.kinds, ev.Kind)
		if ev.Kind == events.EventTurnStarted {
			if data, ok := ev.Data.(events.TurnStartedData); ok {
				rec.starts = append(rec.starts, data)
			}
		}
	}, func() {})
	return rec
}

func (r *boundaryRecorder) snapshot() ([]events.EventKind, []events.TurnStartedData) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.EventKind(nil), r.kinds...), append([]events.TurnStartedData(nil), r.starts...)
}

// indexOf reports the position of the first event of kind k, or -1.
func indexOf(kinds []events.EventKind, k events.EventKind) int {
	for i, got := range kinds {
		if got == k {
			return i
		}
	}
	return -1
}

// TestNotificationTurnAnnouncesOneNamedBoundary covers the wake shape that
// carries NO job notifications — it proceeds on pending steering alone, emits
// no reminder, and so had no event of its own at all. Without a boundary its
// turn opens under an id the daemon's mutation preconditions reject, and every
// Steer, Send and Stop aimed at it fails silently (kata 7vmd).
func TestNotificationTurnAnnouncesOneNamedBoundary(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	rec := serveAndRecord(t, s)
	s.SteerKind("look at this", events.SteeringKindNotification)

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	_, starts := rec.snapshot()
	if len(starts) != 1 {
		t.Fatalf("EventTurnStarted count = %d, want exactly 1", len(starts))
	}
	if !strings.HasPrefix(starts[0].TurnID, "turn_m") {
		t.Fatalf("boundary carried TurnID %q, want a turn_m<n> the preconditions accept", starts[0].TurnID)
	}
}

// TestNotificationBoundaryPrecedesTheTurnsContent pins the ordering the whole
// change exists for: content emitted before the boundary is attributed to the
// turn before it.
//
// The steering-only wake's content is injectDrainedSteering's
// EventSteeringInjected, emitted at the tail of acceptNotificationInput. Its
// sibling shape — a job-notification wake — emits a reminder from the same
// site, three lines below the boundary; the guard against the boundary being
// buried inside that shape's `if len(jobNotifs) > 0` block is
// TestNotificationTurnAnnouncesOneNamedBoundary above, which drives the shape
// that block skips entirely.
func TestNotificationBoundaryPrecedesTheTurnsContent(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	rec := serveAndRecord(t, s)
	s.SteerKind("look at this", events.SteeringKindNotification)

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	kinds, _ := rec.snapshot()
	started := indexOf(kinds, events.EventTurnStarted)
	injected := indexOf(kinds, events.EventSteeringInjected)
	if started < 0 {
		t.Fatalf("no EventTurnStarted in %v", kinds)
	}
	if injected < 0 {
		t.Fatalf("no EventSteeringInjected in %v", kinds)
	}
	if started > injected {
		t.Fatalf("EventTurnStarted at %d came after EventSteeringInjected at %d; the turn's content would land in the previous turn", started, injected)
	}
}

// TestNotificationTurnReleasesItsTurnID is the leak guard. The minted id gates
// every later turn: AcceptClientMutationStart refuses while ActiveTurnID is
// set, and mintRunningTurnID refuses to name the next agent turn, so an id
// held past its turn wedges the session for the life of the process.
func TestNotificationTurnReleasesItsTurnID(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	serveAndRecord(t, s)
	s.SteerKind("look at this", events.SteeringKindNotification)

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the notification turn ended, want it released", got)
	}
}

// TestRefusedNotificationWakeAnnouncesNoBoundary pins that a coalesced wake
// with nothing to deliver — the commonest outcome — neither announces a turn
// nor reserves an id for one that never runs.
func TestRefusedNotificationWakeAnnouncesNoBoundary(t *testing.T) {
	s := newTestSessionForEnvctx(t)
	rec := serveAndRecord(t, s)

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if _, starts := rec.snapshot(); len(starts) != 0 {
		t.Fatalf("refused wake announced %d boundaries, want 0", len(starts))
	}
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after a refused wake, want empty", got)
	}
}

// TestUnservedSessionNamesNoTurn keeps in-process subagents off both halves of
// this machinery. They run EntryNotification per delegate wake and share the
// parent's StateDir, so naming their turns would cost a durable write each and
// publish turn frames on a thread no client can address.
func TestUnservedSessionNamesNoTurn(t *testing.T) {
	s := newTestSessionForEnvctx(t) // no drain registered

	if s.servedByDaemon() {
		t.Fatal("a session with no authoritative consumer reports itself served")
	}
	if got := s.mintRunningTurnID(); got != "" {
		t.Fatalf("mintRunningTurnID = %q for an unserved session, want empty", got)
	}
}
