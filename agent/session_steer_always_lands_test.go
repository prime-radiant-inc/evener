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

// TestReattachedSteerAppendsOneEntryPerAcceptedMutation drives the sequence
// issue #6467 names -- accept a steer, drop the daemon before it runs, attach a
// replacement, let its carrier deliver the steer, then run a following
// turn/start -- and pins the invariant both append paths owe: one accepted
// client mutation appends exactly one transcript entry, and a steer's entry is
// STEERING, never USER_INPUT.
//
// The oracle is the client mutation id, not the message text, and that
// distinction is the finding. Session 034shLavwEzQOOKf3RtUCG, the session the
// issue reports, carries the same 1892-byte text in two entries three seqs
// apart under turn_m2443:
//
//	seq 11425  USER_INPUT  client mutation cf342924-... (turn/start, turn_m2443)
//	seq 11427  STEERING    client mutation 2f94c580-... (turn/steer,  turn_m2442)
//
// Two separately accepted mutations, one entry each, both recorded against the
// turn the start ran. The journal holds both as applied: the start terminal on
// turn_m2443, the steer incorporated on turn_m2442 with owning_turn_id
// turn_m2443. So the daemon appended one mutation once per mutation; it did not
// append one mutation twice. Who issued the second mutation is a client
// question this test does not answer.
//
// That is why a text-keyed count would be the wrong assertion here: "exactly
// one entry carries this text" reddens on correct behavior whenever two
// accepted mutations carry the same words, which is precisely what the reported
// session did. TestTwoAcceptedMutationsWithIdenticalTextEachAppendOneEntry
// holds that boundary so the difference stays checkable.
//
// This test passes on unmodified main. It is a pinning test, not a
// reproduction: the duplicate in #6467 was not reproduced, and this holds the
// shape rather than the defect. It fails if one accepted mutation ever appends
// twice -- verified by making appendClientMutationTranscriptLocked write its
// turn a second time, which yields `transcript entries for client mutation
// "cm-reattach-steer" = 2 ([STEERING STEERING]), want exactly 1`.
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
	// The steer is accepted and reserves its carrier turn: mutation
	// 2f94c580-... of the report.
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
	_, ran, err := restored.ProcessPendingUserInput(t.Context(), nil)
	if err != nil {
		t.Fatalf("carrier input: ran=%v err=%v", ran, err)
	}
	if !ran {
		t.Fatal("the replacement ran no carrier turn, so the counts below would only prove that nothing was appended")
	}

	// A fresh turn/start follows, as turn_m2443 did in the report.
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

	// The issue's step 3 -- the store written again and the steer incorporated
	// -- is only a duplicate if an already-recorded steer is appended again.
	// Drive both ways: another wake in this process, and another replacement.
	if _, _, err := restored.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("second wake over an incorporated steer: %v", err)
	}
	restored.Close()
	again := restoreWithScriptedModel(t, dir, id)
	if _, _, err := again.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("wake after a second replacement: %v", err)
	}
	assertOneEntryPerMutation(t, again, steerID, schema.TurnSteering, steerText)
}

// TestTwoAcceptedMutationsWithIdenticalTextEachAppendOneEntry holds the
// boundary that rules out the mechanism issue #6467 proposes.
//
// A steer and a following turn/start carrying the same words are two accepted
// mutations, and the daemon is right to append one entry for each: a steer and
// a start are different work, and identical text does not make them one. That
// is the shape of the reported session, so no count keyed on text can tell it
// apart from a real duplicate.
//
// This is also why scanning the transcript for repeated text is not the fix for
// #6467: a daemon that deduped on text would refuse the second, genuine
// mutation. The count that matters is per mutation id, which is what
// TestReattachedSteerAppendsOneEntryPerAcceptedMutation asserts.
func TestTwoAcceptedMutationsWithIdenticalTextEachAppendOneEntry(t *testing.T) {
	t.Parallel()
	const (
		shared  = "the same words submitted twice"
		steerID = "cm-same-text-steer"
		startID = "cm-same-text-start"
	)
	s := newTestSessionForEnvctx(t)
	serveSessionWithMutationStore(t, s)
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: steerID,
		Input:            []appwire.InputItem{{Type: "text", Text: shared}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	if _, ran, err := s.ProcessPendingUserInput(t.Context(), nil); err != nil || !ran {
		t.Fatalf("deliver the steer: ran=%v err=%v", ran, err)
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

	assertOneEntryPerMutation(t, s, steerID, schema.TurnSteering, shared)
	assertOneEntryPerMutation(t, s, startID, schema.TurnUserInput, shared)
}

// assertOneEntryPerMutation fails unless the transcript holds exactly one entry
// for clientMutationID, that entry's kind is want, and it carries wantText.
// wantText proves the id-to-text mapping; it is the caller's own sentinel, never
// production copy.
func assertOneEntryPerMutation(t *testing.T, s *Session, clientMutationID string, want schema.TurnKind, wantText string) {
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
}
