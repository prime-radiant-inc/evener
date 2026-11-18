package agent

import (
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// TestRestoredSteeringWakesWhenTheDaemonAttaches closes the crash case the
// retry path cannot reach on its own.
//
// If the process dies after a steer's durable commit and the client never
// retries, restoreDurableClientMutationQueues rebuilds the steering queue at
// startup -- and then nothing asks the session to run. The steer waits for
// whatever the user happens to do next.
//
// The case has two halves and this test covers both, because either one alone
// strands the steer: restore must reconstitute the durable steering into the
// runtime queue, and registering the wake callback must then fire on it.
// SetNotifyFunc is the hook for the second half -- it is the moment the wake
// callback provably exists, and it already fires for every other kind of
// pending work (job notifications, delegate deliveries, root and stable
// attention, watch settlement retries). Steering was simply missing from that
// list.
//
// The crash is real, not simulated: the steer is committed through the
// production durable path, the session is closed, and a fresh one is rebuilt
// from the same state dir through RestoreSessionFromMetaWithConfig -- the same
// function `evener serve --resume` calls.
func TestRestoredSteeringWakesWhenTheDaemonAttaches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	// The user's own steer, through the path a client takes. The kind matters:
	// only user steering is work the session owes someone, so only it wakes on
	// attach. Daemon-authored steering is context for the next turn and must
	// not start one -- see TestNoTurnRunsBeforeTheUsersFirstPrompt.
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-restored-steer",
		Input:            []appwire.InputItem{{Type: "text", Text: "restored steer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	// The process dies here. The steer is durably committed, no turn ever ran
	// it, and the client never retries.
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	serveSession(t, restored)

	// A diagnostic, not this test's coverage. Five cases in
	// session_queue_persist_test.go already fail if restore stops
	// reconstituting steering, so deleting that line reddens the package with
	// or without this line -- it is here to say WHICH half broke when this test
	// fails, not to hold the half. The wake below is what this test uniquely
	// holds. hasPendingUserSteering, not hasPendingSteering: the wake consults
	// the user-scoped term, so that is the one restore has to have refilled.
	if !restored.hasPendingUserSteering() {
		t.Fatal("restore did not put the durably-committed steer back in the runtime steering queue; nothing downstream can deliver a steer the queue does not hold")
	}

	var mu sync.Mutex
	notifies := 0
	restored.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a restored session with steering already queued did not wake when the daemon attached; a steer that survived a crash waits for unrelated input")
	}
}

// TestSteerLandsWhenItsTurnAlreadyEnded pins the rule that a steer always
// lands.
//
// The reachable case is a race, not an exotic one. You press Steer while the
// agent is working; between the click and the request arriving, the turn ends.
// The daemon then compares your expectedTurnId against an ActiveTurnID that is
// now empty and rejects with Conflict("turn is not active") -- and per kata
// 2f41 you are told nothing. The thing you typed is simply gone.
//
// A steer typed while the agent works means "take this into account", and the
// user cannot see turn boundaries, so the daemon accepts it into the steering
// queue instead. The steering queue is the existing delivery path: a wake
// proceeds on pending steering alone, with no job notifications involved.
func TestSteerLandsWhenItsTurnAlreadyEnded(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

	// No turn is running: the one the client aimed at has ended.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-1",
		Input:            []appwire.InputItem{{Type: "text", Text: "also check the tests"}},
	}); err != nil {
		t.Fatalf("a steer whose turn ended under it was refused: %v", err)
	}

	if !s.hasPendingSteering() {
		t.Fatal("the steer was accepted but is not queued for delivery; it would be silently lost")
	}
}

// TestSteerWakesAnIdleSessionToDeliverItself is the other half of the ruling.
// Accepting the steer is not enough: if no turn ever runs, the steering queue
// is never drained and the message is lost more quietly than a rejection would
// have been. The daemon must provoke a turn of its own.
func TestSteerWakesAnIdleSessionToDeliverItself(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-2",
		Input:            []appwire.InputItem{{Type: "text", Text: "also check the tests"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a steer accepted with no turn running woke nothing; the steering queue has no drain, so the message waits forever")
	}
}

// TestSteerWakesEvenWhileATurnIsRunning covers the race that killed the
// obvious optimisation. Gating the wake on "no turn is running" looks free --
// the round loop drains steering between rounds, so why kick? -- but a turn can
// pass its FINAL steering drain and still own the turn id. A steer arriving in
// that window is skipped by the gate and then never looked at again, because
// the turn releases its id without rechecking steering.
//
// An unneeded kick is cheap: the wake finds nothing and no-ops. A missed one
// loses what the user typed.
func TestSteerWakesEvenWhileATurnIsRunning(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.NextTurnSequence++
		snapshot.ActiveTurnID = appwire.ClientMutationTurnID(snapshot.NextTurnSequence)
		return nil
	}); err != nil {
		t.Fatalf("seed a running turn: %v", err)
	}

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-running",
		Input:            []appwire.InputItem{{Type: "text", Text: "mid-turn steer"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer against the running turn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("a steer accepted during a turn woke nothing; if that turn had already passed its final drain, the steer is never looked at again")
	}
}

// TestSteerRetryStillProvokesDelivery is the crash case. The store commits, the
// process dies before the wake, and the client retries with the same
// clientMutationId. The store replays idempotently and returns early -- which
// is correct for the record and useless for the user, because the steer is
// still sitting undelivered. Replay is idempotent in the store; the wake is
// what makes it idempotent in effect.
func TestSteerRetryStillProvokesDelivery(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)

	params := appwire.TurnSteerParams{
		ClientMutationID: "cm-steer-retry",
		Input:            []appwire.InputItem{{Type: "text", Text: "retried steer"}},
	}
	if _, err := s.AcceptClientMutationSteer(params); err != nil {
		t.Fatalf("first accept: %v", err)
	}

	var mu sync.Mutex
	notifies := 0
	s.SetNotifyFunc(func() {
		mu.Lock()
		notifies++
		mu.Unlock()
	})

	if _, err := s.AcceptClientMutationSteer(params); err != nil {
		t.Fatalf("retry of the same steer: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if notifies == 0 {
		t.Fatal("the retry replayed the record and woke nothing; a steer stranded by a crash stays stranded no matter how often the client retries")
	}
}

// TestReattachedSteerAppendsOneEntryPerAcceptedMutation accepts a steer, drops
// the daemon before it runs, attaches a replacement, lets its carrier deliver
// the steer, then runs a following turn/start. It pins the invariant both
// append paths owe: one accepted client mutation appends exactly one transcript
// entry, and a steer's entry is STEERING, never USER_INPUT.
//
// The oracle is the client mutation id, not the message text: a steer and a
// start legitimately carry identical words when a client submits one
// composition twice, and each still owes its own single entry.
// TestSteerDrainedIntoARunningStartAppendsOneEntryPerMutation holds the case
// where both land under one turn.
func TestReattachedSteerAppendsOneEntryPerAcceptedMutation(t *testing.T) {
	t.Parallel()
	const (
		steerText = "carried across the daemon replacement"
		steerID   = "cm-reattach-steer"
		startID   = "cm-reattach-start"
		startText = "a different message that follows"
	)
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// The steer is accepted and reserves its carrier turn.
	if _, err := crashed.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: steerID,
		Input:            []appwire.InputItem{{Type: "text", Text: steerText}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	carrier := crashed.clientMutations.snapshot().PendingExecutions[steerID]
	if carrier.TurnID == "" || carrier.ExecutionState != "accepted" {
		t.Fatalf("steer pending = %#v, want an accepted steer holding a reserved carrier turn", carrier)
	}
	// The accepting daemon is replaced before the steer runs.
	crashed.Close()

	// The replacement attaches and rebuilds the durable steering, then carries
	// the steer on a turn of its own.
	restored := restoreWithScriptedModel(t, dir, id)
	if !restored.hasPendingUserSteering() {
		t.Fatal("the steer did not survive the daemon replacement; nothing downstream can deliver it")
	}
	_, ran, _, err := restored.ProcessPendingUserInput(t.Context(), nil)
	if err != nil {
		t.Fatalf("carrier input: ran=%v err=%v", ran, err)
	}
	if !ran {
		t.Fatal("the replacement ran no carrier turn, so the counts below would only prove that nothing was appended")
	}
	// The carrier delivers the steer as STEERING and nothing else. A duplicate of
	// the shape this test exists for is the same message appended a second time
	// as an untagged USER_INPUT, which the per-mutation counts below cannot see:
	// its mutation id matches neither claimed mutation.
	if got := countEntriesOfKind(t, restored, schema.TurnUserInput); got != 0 {
		t.Fatalf("after the carrier ran, USER_INPUT entries = %d, want 0: the carrier delivers the steer as STEERING, and any second entry carrying it is the duplicate", got)
	}

	// A fresh turn/start follows the carrier.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: startID,
		Input:            []appwire.InputItem{{Type: "text", Text: startText}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, _, err := restored.ProcessClientMutationStart(t.Context(), nil); err != nil {
		t.Fatalf("client mutation start: %v", err)
	}

	assertOneEntryPerMutation(t, restored, steerID, schema.TurnSteering, steerText)
	assertOneEntryPerMutation(t, restored, startID, schema.TurnUserInput, startText)
	// The start's own entry is the one USER_INPUT now, so the zero above is about
	// the carrier and not about silencing the start.
	if got := countEntriesOfKind(t, restored, schema.TurnUserInput); got != 1 {
		t.Fatalf("after the start ran, USER_INPUT entries = %d, want 1: the start records its own entry, so the carrier's zero cannot come from USER_INPUT being broken", got)
	}

	// An incorporated steer is only a duplicate if it is appended again. Drive
	// both ways: another wake in this process, and another replacement.
	if _, _, _, err := restored.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("second wake over an incorporated steer: %v", err)
	}
	restored.Close()
	again := restoreWithScriptedModel(t, dir, id)
	if _, _, _, err := again.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("wake after a second replacement: %v", err)
	}
	assertOneEntryPerMutation(t, again, steerID, schema.TurnSteering, steerText)
}

// TestSteerDrainedIntoARunningStartAppendsOneEntryPerMutation covers a steer
// and a turn/start carrying the same words landing under ONE running turn.
//
// The steer is accepted and left pending, then the start claims and runs a
// turn whose tool round drains the pending steering into itself. Both entries
// then carry the start's turn id, and the steering entry's OwningTurnID names
// that same turn. A dedup scoped to one owning turn has to drop the second
// entry here, and that is a bug: the steer and the start are two accepted
// mutations, and identical text does not make them one.
//
// This is why scanning the transcript for repeated text is not the fix: it
// discards a genuine second mutation. The count that matters is per mutation
// id, which is what this test asserts.
func TestSteerDrainedIntoARunningStartAppendsOneEntryPerMutation(t *testing.T) {
	t.Parallel()
	const (
		shared  = "the same words submitted twice"
		steerID = "cm-same-text-steer"
		startID = "cm-same-text-start"
	)
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	// The steer is accepted and stays pending: nothing drains it until the
	// start's own tool round does.
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: steerID,
		Input:            []appwire.InputItem{{Type: "text", Text: shared}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: startID,
		Input:            []appwire.InputItem{{Type: "text", Text: shared}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, _, err := s.ProcessClientMutationStart(t.Context(), nil); err != nil {
		t.Fatalf("client mutation start: %v", err)
	}
	steering := assertOneEntryPerMutation(t, s, steerID, schema.TurnSteering, shared)
	start := assertOneEntryPerMutation(t, s, startID, schema.TurnUserInput, shared)
	// The fixture only covers a same-turn dedup if the steer really landed in the
	// start's turn: the steering entry's owner is the turn the start recorded its
	// own USER_INPUT under. Without this, the two entries could sit in different
	// turns and a dedup scoped to one owning turn would go unnoticed.
	if start.TurnID == "" {
		t.Fatal("the start's entry carries no turn id, so the fixture cannot show where the steer landed")
	}
	if steering.OwningTurnID != start.TurnID {
		t.Fatalf("the steering entry is owned by turn %q, want the start's turn %q: the steer has to be drained into the start's own turn, not delivered on a carrier of its own", steering.OwningTurnID, start.TurnID)
	}
}

// countEntriesOfKind counts transcript entries of kind, across every mutation.
func countEntriesOfKind(t *testing.T, s *Session, kind schema.TurnKind) int {
	t.Helper()
	n := 0
	for _, entry := range transcriptEntries(t, s) {
		if entry.Turn.Kind == kind {
			n++
		}
	}
	return n
}

// assertOneEntryPerMutation fails unless the transcript holds exactly one entry
// for clientMutationID, that entry's kind is want, and it carries wantText.
// wantText proves the id-to-text mapping; it is the caller's own sentinel, never
// production copy.
func assertOneEntryPerMutation(t *testing.T, s *Session, clientMutationID string, want schema.TurnKind, wantText string) schema.Turn {
	t.Helper()
	var matched []schema.Turn
	for _, entry := range transcriptEntries(t, s) {
		if entry.Turn.ClientMutationID == clientMutationID {
			matched = append(matched, entry.Turn)
		}
	}
	var kinds []string
	for _, turn := range matched {
		kinds = append(kinds, string(turn.Kind))
	}
	if len(matched) != 1 {
		t.Fatalf("transcript entries for client mutation %q = %d (%v), want exactly 1: one accepted mutation appends one entry", clientMutationID, len(matched), kinds)
	}
	if got := matched[0].Kind; got != want {
		t.Fatalf("client mutation %q appended a %q entry, want %q", clientMutationID, got, want)
	}
	if !strings.Contains(matched[0].Message.Text(), wantText) {
		t.Fatalf("the entry for client mutation %q does not carry the text it was accepted with", clientMutationID)
	}
	return matched[0]
}
