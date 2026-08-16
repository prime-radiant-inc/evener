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
	session *Session
	drained chan struct{}

	mu     sync.Mutex
	kinds  []events.EventKind
	starts []events.TurnStartedData
}

func serveAndRecord(t *testing.T, s *Session) *boundaryRecorder {
	t.Helper()
	rec := &boundaryRecorder{session: s, drained: make(chan struct{})}
	s.ConsumeEventsLossless(func(ev events.SessionEvent) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		rec.kinds = append(rec.kinds, ev.Kind)
		if ev.Kind == events.EventTurnStarted {
			if data, ok := ev.Data.(events.TurnStartedData); ok {
				rec.starts = append(rec.starts, data)
			}
		}
	}, func() { close(rec.drained) })
	return rec
}

// snapshot closes the session and waits for the consumer goroutine to finish
// before reading, so every assertion sees the whole stream.
//
// ProcessInputKind returning does NOT mean the events it emitted have been
// consumed: ConsumeEventsLossless drains on its own goroutine, so reading the
// recorder directly after the call is a race. A positive assertion would flake,
// and — worse — a negative one ("no boundary was announced") would pass simply
// by reading before the event it is meant to catch arrived. Closing the session
// closes the event channel, which ends the drain loop and fires onDrained; that
// is the awaitable completion, not a sleep.
func (r *boundaryRecorder) snapshot() ([]events.EventKind, []events.TurnStartedData) {
	r.session.Close()
	<-r.drained
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

// TestNotificationBoundaryPrecedesItsJobReminder is the ordering test for the
// shape that actually emits a reminder. It matters more than it looks: the web
// reducer attaches a steering item to whatever activeTurnId it currently
// holds, so a reminder arriving before turn/started is either filed under the
// previous turn or dropped outright when there is no active turn at all.
//
// It needs the full job-notification setup (a job manager plus a durable
// record) because acceptNotificationInput drops a hand-made in-memory
// notification as undeliverable and refuses the wake.
func TestNotificationBoundaryPrecedesItsJobReminder(t *testing.T) {
	dir := t.TempDir()
	s := newTestSessionForEnvctx(t, withDir(dir))
	rec := serveAndRecord(t, s)

	jm, err := newJobManager(dir, s.ID(), s.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	s.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, s.ID())
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

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
		t.Fatalf("no EventSteeringInjected in %v; this wake emitted no reminder, so it is not the shape under test", kinds)
	}
	if started > injected {
		t.Fatalf("EventTurnStarted at %d came after the reminder at %d; the reminder would be filed under the previous turn, or dropped", started, injected)
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

// TestUnservedSessionAnnouncesNoBoundary pins the guard that keeps descendant
// projections untouched — and it cannot be pinned through the events channel,
// because an unserved session's events go nowhere a test can drain. It rides
// the descendant hook instead, which is exactly the path that matters:
// sendEvent forwards to descendantEvent regardless of authoritativeConsumer
// (session_events.go), so an unguarded emit would push a boundary into every
// in-process subagent's projection and close and reopen turns on a delegate
// thread no client can address.
func TestUnservedSessionAnnouncesNoBoundary(t *testing.T) {
	s := newTestSessionForEnvctx(t) // no drain registered

	var mu sync.Mutex
	var forwarded []events.EventKind
	// descendantEvent is what a real child inherits from its parent
	// (cfg.spawn.descendantEvent, installed at spawn); setting it here reaches
	// the same sendEvent branch without standing a subagent up.
	s.descendantEvent = func(ev events.SessionEvent) {
		mu.Lock()
		defer mu.Unlock()
		forwarded = append(forwarded, ev.Kind)
	}
	s.SteerKind("look at this", events.SteeringKindNotification)

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if i := indexOf(forwarded, events.EventTurnStarted); i >= 0 {
		t.Fatalf("an unserved session forwarded a turn boundary to its descendants at %d: %v", i, forwarded)
	}
	if indexOf(forwarded, events.EventSteeringInjected) < 0 {
		t.Fatalf("the descendant hook saw no events at all (%v); the test would pass for the wrong reason", forwarded)
	}
}
