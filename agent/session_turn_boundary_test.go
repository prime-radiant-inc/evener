package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/appwire"
)

// withMetaFS routes the session's session-meta JSON IO through fs instead of
// the real filesystem (kata 49k3). It mutates the config an earlier withConfig
// installed, so it must be passed after any withConfig option — which
// newTestSessionForEnvctx's base-then-opts ordering guarantees.
func withMetaFS(fs afero.Fs) sessionOpt {
	return func(o *sessionOpts) { o.cfg.testOnly.metaFS = fs }
}

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
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
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
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
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
	metaFS := afero.NewMemMapFs()
	dir := t.TempDir()
	s := newTestSessionForEnvctx(t, withDir(dir), withMetaFS(metaFS))
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
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
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
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
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

// TestRefusedDurableAppendAnnouncesNoBoundary pins the ordering
// acceptNotificationInput's "persist before announcing" comment argues for.
// appendSteeringTurnDurably is the turn's last refusal, and moving the mint and
// the boundary above it costs twice: the refusal path returns no id, so the
// deferred release in processOneInput never sees the one that was minted and it
// leaks, and the projection is left holding an open active turn whose close
// finishNotificationNoop's sessionEndEmitted then suppresses.
func TestRefusedDurableAppendAnnouncesNoBoundary(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	dir := t.TempDir()
	s := newTestSessionForEnvctx(t, withDir(dir), withMetaFS(metaFS))
	rec := serveAndRecord(t, s)

	jm, err := newJobManager(dir, s.ID(), s.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	s.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, s.ID())
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

	ctx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{},
		map[string]error{"append_notification": errors.New("append failed")})
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if _, starts := rec.snapshot(); len(starts) != 0 {
		t.Fatalf("a wake refused at its durable append announced %d boundaries, want 0", len(starts))
	}
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if got := s.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after a refused wake; the id was minted above the refusal and leaked", got)
	}
}

// TestWakeStandsDownWhileAUserTurnIsClaimed is the ordinary-usage race, not an
// exotic one: inputCh holds one slot, and a job-completion wake that finds it
// full parks a goroutine blocked on the send (server.go:768). Finish a turn
// while a job wake is parked, then send a message, and the two race for the
// freed slot. When the parked wake wins, the serve loop runs it while
// ActiveTurnID is already claimed by the user's turn/start -- so the wake
// cannot name itself, and a notification turn that can run for minutes has no
// Stop and no Steer for its whole life.
//
// Standing down is the fix rather than naming it anyway: the user's turn is
// next and is already named, and the drain loop's tail gate
// (session_lifecycle.go:816, peekNotifications) runs the notification turn
// inline once that turn ends, by which point nothing is claimed and the wake
// names itself. Nothing is lost, and no turn ever runs unnameable.
func TestWakeStandsDownWhileAUserTurnIsClaimed(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	dir := t.TempDir()
	s := newTestSessionForEnvctx(t, withDir(dir), withMetaFS(metaFS))
	rec := serveAndRecord(t, s)

	jm, err := newJobManager(dir, s.ID(), s.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	s.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, s.ID())
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

	// A turn/start the client has been told exists, accepted but not yet run.
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		turnID := appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		snapshot.ActiveTurnID = turnID
		snapshot.PendingExecutions["cm-user"] = appwire.PendingMutation{
			ClientMutationID: "cm-user",
			Method:           clientMutationMethodStart,
			ExecutionState:   "accepted",
			TurnID:           turnID,
			ProjectionState:  appwire.MutationProjectionPending,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed a claimed user turn: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if _, starts := rec.snapshot(); len(starts) != 0 {
		t.Fatalf("the wake opened %d turns while a user turn was claimed; it cannot name one, so it must not run one", len(starts))
	}
	if got := s.peekNotifications(); got == 0 {
		t.Fatal("the wake consumed its notifications while standing down; the user's turn tail has nothing left to deliver")
	}
}

// TestStandDownConsumesNoWakeState is the constraint that decides WHERE the
// stand-down check goes. beginRootDelegateAttentionTurn (session_lifecycle.go,
// in processOneInput) runs ahead of acceptNotificationInput and consumes the
// process-local wake: it clears rootAttentionWake AND cancels any scheduled
// retry (session_attention.go:518-521). Only finishRootDelegateAttentionTurn
// re-arms it, and that is skipped unless the wake was accepted.
//
// So a stand-down decided after that call strands the very attention it was
// woken for, with nothing left to raise it again until some unrelated wake
// happens by. The decision has to come first, while nothing has been consumed.
func TestStandDownConsumesNoWakeState(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
	serveSession(t, s)

	s.attentionMu.Lock()
	s.rootAttentionWakeIDs = map[string]struct{}{"att_1": {}}
	s.rootAttentionWake = true
	s.attentionMu.Unlock()

	// A user's turn/start already owns the durable name.
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed a claimed user turn: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	s.attentionMu.Lock()
	armed := s.rootAttentionWake
	pending := len(s.rootAttentionWakeIDs)
	s.attentionMu.Unlock()
	if pending == 0 {
		t.Fatal("the stand-down dropped the pending attention ids outright")
	}
	if !armed {
		t.Fatal("the stand-down consumed the attention wake and cancelled its retry; nothing re-arms it, so this delegate waits on an unrelated wake")
	}
}

// TestStandDownSettlesTheProcessingTransition covers the state flip the
// stand-down inherits. processOneInput sets SessionProcessing at :1000, well
// before the stand-down decision, so returning early without settling leaves
// the session reporting itself busy with nothing running -- and the serve
// loop's SetState(sess.WireState()) then publishes that. Every other refusal
// on this path settles through finishNotificationNoop; this one must too.
func TestStandDownSettlesTheProcessingTransition(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
	serveSession(t, s)

	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed a claimed user turn: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if got := s.State(); got == SessionProcessing {
		t.Fatalf("session state = %q after standing down; nothing is running, so it must not report busy", got)
	}
}

// TestStandDownReArmsTheWake is the delivery guarantee, and it is the assertion
// that matters: preserving rootAttentionWake is NOT the same as the wake still
// being deliverable, because the flag being set is exactly what suppresses a
// new one. armRootDelegateAttention only notifies when !rootAttentionWake
// (session_attention.go:485), scheduleRootAttentionRetryLocked returns early
// while it is set (:559), and the drain loop's tail gate counts job
// notifications alone -- peekNotifications reads s.pendingJobNotifs and nothing
// else (session.go:700-704).
//
// So a stood-down wake whose EntryNotification the serve loop already consumed
// has nothing left to raise it. It must ask for another one on its way out.
//
// The ask is PACED rather than immediate (kata ajg5): notify() pushes into a
// one-slot channel the serve loop is about to read, so an immediate kick spins
// for as long as the name stays held -- which a mutation store failing writes
// makes forever. The guarantee is unchanged; only the timing is.
func TestStandDownReArmsTheWake(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
	clk := agenttest.NewFakeClock()
	s.clock = clk
	serveSession(t, s)

	var mu sync.Mutex
	notifies := 0
	woken := make(chan struct{}, 4)
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
		woken <- struct{}{}
	})

	s.attentionMu.Lock()
	s.rootAttentionWakeIDs = map[string]struct{}{"att_1": {}}
	s.rootAttentionWake = true
	s.attentionMu.Unlock()

	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		turnID := appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		snapshot.ActiveTurnID = turnID
		// The pending execution is what makes this name temporary: the serve
		// loop runs that turn next and releases it. Without it there would be
		// nothing to wait for -- see TestStandDownDoesNotSpinOnAStaleName.
		snapshot.PendingExecutions["cm-user"] = appwire.PendingMutation{
			ClientMutationID: "cm-user",
			Method:           clientMutationMethodStart,
			ExecutionState:   "accepted",
			TurnID:           turnID,
			ProjectionState:  appwire.MutationProjectionPending,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed a claimed user turn: %v", err)
	}

	timersBefore := clk.BlockedCount()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	if got := clk.BlockedCount(); got != timersBefore+1 {
		t.Fatalf("standing down armed %d retry timers, want the baseline %d plus one; the consumed EntryNotification is gone, the attention flag suppresses a new one, and the tail gate does not count attention -- without this the delegate waits forever",
			got, timersBefore)
	}
	clk.Advance(jobNotificationRetryInitialDelay)
	<-woken

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("standing down asked for no further wake")
	}
}

// TestStandDownDoesNotSpinOnAStaleName bounds the re-arm. notify() pushes into
// a one-slot channel the serve loop is about to read, so a wake that stands
// down and unconditionally asks for another is a livelock whenever the name
// will never be released: dequeue, stand down, notify, dequeue, forever,
// burning a core.
//
// A name a pending client mutation owns WILL be released -- the serve loop runs
// that turn next -- so re-arming against it terminates. A name nobody owns is a
// fault (a crash between reserving and releasing, or a failed release write),
// and re-arming against it never terminates. That is the same ownership rule
// forgetRunningTurnNoOneOwns applies at load; here it decides whether asking
// again can ever help.
func TestStandDownDoesNotSpinOnAStaleName(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS))
	serveSession(t, s)

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	// A name left behind by a turn that died, with no pending execution to run
	// and release it.
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed a stale name: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies != 0 {
		t.Fatalf("stood down against a name nobody owns and asked for %d more wakes; nothing will ever release it, so this is a hot loop", notifies)
	}
}

// TestWakeResumesOnceTheUserTurnReleasesTheName is the other half of standing
// down, and the half that makes it safe: the wake must be DEFERRED, not
// dropped. A finished job whose notification is silently discarded is never
// heard again, which is strictly worse than an unstoppable turn.
//
// Once the user's turn ends and gives the name back, the same wake names itself
// and runs -- which is what the serve loop's tail gate provokes for real.
func TestWakeResumesOnceTheUserTurnReleasesTheName(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	dir := t.TempDir()
	s := newTestSessionForEnvctx(t, withDir(dir), withMetaFS(metaFS))
	rec := serveAndRecord(t, s)

	jm, err := newJobManager(dir, s.ID(), s.enqueueJobNotification)
	if err != nil {
		t.Fatalf("newJobManager: %v", err)
	}
	s.jobManager = jm
	appendPendingJobNotificationRecord(t, jm, s.ID())
	s.enqueueJobNotification(jobNotification{JobID: "job_X", JobType: "shell", Status: "completed", OutputBytes: 42})

	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed a claimed user turn: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) while claimed: %v", err)
	}

	// The user's turn ends and hands the name back.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.ActiveTurnID = ""
		return nil
	}); err != nil {
		t.Fatalf("release the user turn's name: %v", err)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("ProcessInputKind(EntryNotification) after release: %v", err)
	}

	_, starts := rec.snapshot()
	if len(starts) != 1 {
		t.Fatalf("boundary count = %d, want exactly 1 (none while claimed, one after)", len(starts))
	}
	if !strings.HasPrefix(starts[0].TurnID, "turn_m") {
		t.Fatalf("the resumed wake opened turn %q, want a turn_m<n> its controls can address", starts[0].TurnID)
	}
}

// TestUnservedSessionNamesNoTurn keeps in-process subagents off both halves of
// this machinery. They run EntryNotification per delegate wake and share the
// parent's StateDir (subagents.go), so naming their turns would cost a durable
// write per wake and publish turn frames on a thread no client can address.
//
// It asserts the gate (servedByDaemon) and its consequence (no name) together,
// because the second is only correct while the first holds; split apart, either
// half would keep passing after the other stopped being true.
//
// NOTE: `docs/superpowers/plans/2026-08-16-controllable-subagents.md` proposes
// reversing this deliberately — delegates would take durable names so they can
// be stopped. When that lands, this test inverts rather than being deleted.
func TestUnservedSessionNamesNoTurn(t *testing.T) {
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS)) // no drain registered

	if s.servedByDaemon() {
		t.Fatal("a session with no authoritative consumer reports itself served")
	}
	got, refusal := s.mintRunningTurnID()
	if got != "" {
		t.Fatalf("mintRunningTurnID = %q for an unserved session, want empty", got)
	}
	if refusal != turnNameUnserved {
		t.Fatalf("refusal = %v for an unserved session, want turnNameUnserved", refusal)
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
	metaFS := afero.NewMemMapFs()
	s := newTestSessionForEnvctx(t, withMetaFS(metaFS)) // no drain registered

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
