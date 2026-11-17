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

// TestReattachedSteerRecordsExactlyOneEntryAcrossAPeerStart drives the
// reattach shape issue #6467 reports, with a scripted provider and the real
// accept, claim and append code below it.
//
// The reported session is 034shLavwEzQOOKf3RtUCG. There, one user message
// appears in the transcript twice: entry 11425 as USER_INPUT under client
// mutation cf342924-72e5-4c13-b2dc-4717b5f99532 (a turn/start that reserved
// turn_m2443) and entry 11427 as STEERING under mutation
// 2f94c580-f674-4d1c-92d0-143ba85d78fd (a turn/steer that reserved turn_m2442
// and was claimed as that turn's carrier). The two entries carry byte-identical
// text (1892 bytes each) and different client mutation ids.
//
// The assertion this test holds is the invariant both append paths owe:
// ONE accepted mutation produces ONE transcript entry, and a steer's entry is
// STEERING, never USER_INPUT. What such a test cannot hold is a client that
// submits the same composition twice under two ids -- the daemon is correct to
// accept both, because they are different mutations. This test fixes which
// side of that line the defect is on by driving the sequence the issue names:
// accept a steer, drop the daemon before it runs, attach a new one, then start
// a turn, and count the entries that carry the text.
//
// It PASSES on the code as it stands, and that is its result, not a gap: the
// lost-wake-then-reattach sequence the issue names does not duplicate anything
// in the daemon. The steer's carrier append and the peer start's user-input
// append are keyed by different client mutation ids and carry different text,
// so the count is one before and after a second replacement. The duplicate in
// 034shLavwEzQOOKf3RtUCG is two mutations, not one mutation appended twice.
//
// A scan of every transcript in the state dir (10,964 sessions) found ZERO
// client mutation ids that produced more than one entry kind. The ten ids that
// produced two entries each produced two of the SAME kind: seven pairs of
// USER_INPUT and three of STEERING, all replayed after a compaction by
// resumeHistoryIndexed (child of a CHECKPOINT, same seq neighbourhood as the
// checkpoint the replay repeats). So the "one mutation, two append paths"
// mechanism the issue proposes is not reachable through the code as it stands,
// and the observed duplicate came from two separately accepted mutations.
func TestReattachedSteerRecordsExactlyOneEntryAcrossAPeerStart(t *testing.T) {
	t.Parallel()
	const (
		steerText = "carried across the daemon replacement"
		steerID   = "cm-reattach-steer"
	)
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	serveSession(t, crashed)
	if err := crashed.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	// Step 1: the steer is accepted and reserves its carrier turn. This is the
	// mutation 2f94c580-... in the report.
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
	// Step 2: the accepting daemon is replaced before the steer runs.
	crashed.Close()

	// Step 3: the replacement attaches and rebuilds the durable steering. The
	// restore is scripted so the turn it runs can complete.
	restored := restoreWithScriptedModel(t, dir, id)
	if !restored.hasPendingUserSteering() {
		t.Fatal("the steer did not survive the daemon replacement; nothing downstream can deliver it")
	}

	// The replacement carries the steer on its own turn, as the report's step
	// 3 did for turn_m2442.
	_, ran, err := restored.ProcessPendingUserInput(t.Context(), nil)
	if err != nil {
		t.Fatalf("carrier input: ran=%v err=%v", ran, err)
	}
	if !ran {
		t.Fatal("the replacement ran no carrier turn, so the count below would only prove that nothing was appended")
	}

	// Step 4: a fresh turn/start follows, as turn_m2443 did in the report.
	startID := "cm-reattach-start"
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: startID,
		Input:            []appwire.InputItem{{Type: "text", Text: "a different message that follows"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if _, ran, err := restored.ProcessClientMutationStart(t.Context(), nil); err != nil {
		t.Fatalf("client mutation start: ran=%v err=%v", ran, err)
	}

	// The invariant: exactly one entry carries the steer's text, and it is the
	// steering entry the carrier path constructs -- not a USER_INPUT the start
	// path would construct from the same text.
	var carriers []schema.Turn
	for _, entry := range transcriptEntries(t, restored) {
		if strings.Contains(entry.Turn.Message.Text(), steerText) {
			carriers = append(carriers, entry.Turn)
		}
	}
	if len(carriers) != 1 {
		var kinds []string
		for _, turn := range carriers {
			kinds = append(kinds, string(turn.Kind)+"/"+turn.ClientMutationID)
		}
		t.Fatalf("entries carrying the steer text = %d (%v), want exactly 1: an accepted mutation appends exactly one transcript entry", len(carriers), kinds)
	}
	if got := carriers[0].Kind; got != schema.TurnSteering {
		t.Fatalf("the entry carrying the steer text is %q, want %q: the steer only ever arrives through consumeSteeringMessage's steering append", got, schema.TurnSteering)
	}
	if got := carriers[0].ClientMutationID; got != steerID {
		t.Fatalf("the steering entry carries client mutation %q, want %q", got, steerID)
	}

	// The issue's step 3 -- "the mutation store was written again and the steer
	// became incorporated" -- is only a duplicate if a steer that is already in
	// the transcript is appended a second time. Drive the two ways that could
	// happen: another wake in this process, and another daemon replacement.
	if _, _, err := restored.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("second wake over an incorporated steer: %v", err)
	}
	restored.Close()
	again := restoreWithScriptedModel(t, dir, id)
	if _, _, err := again.ProcessPendingUserInput(t.Context(), nil); err != nil {
		t.Fatalf("wake after a second replacement: %v", err)
	}
	var after int
	for _, entry := range transcriptEntries(t, again) {
		if strings.Contains(entry.Turn.Message.Text(), steerText) {
			after++
		}
	}
	if after != 1 {
		t.Fatalf("entries carrying the steer text after a second replacement and wake = %d, want 1: an incorporated steer must not be re-appended by restore", after)
	}
}
