package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// TestFinalizeInterruptHoldsNoSessionLock calls the interrupt finalizer directly
// while Session.mu is held. It is a pure function of a store snapshot -- the
// transcript facts it needs are sampled by its callers before the serializer --
// so it must run to completion here. The store serializer must never wait on
// Session.mu, and the transcript scan that answers "does the transcript already
// hold this turn?" takes Session.mu: a finalizer that reached for session state
// would deadlock the moment a Session.mu holder reached the serialize path, which
// is a rare hang rather than a failing assertion.
//
// The snapshot also carries a claimed queue execution the transcript already
// holds, so the same call pins the settle half: promoting it to incorporated
// clears the active turn instead of leaving the execution claimed and the turn
// pinned until a restart.
func TestFinalizeInterruptHoldsNoSessionLock(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "finalize-outside-session-mu", "already recorded")
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "finalize-outside-session-mu" {
		t.Fatalf("setup: claimed = %#v refusal = %v", claimed, refusal)
	}
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("already recorded"))
	turn.ClientMutationID = claimed.ClientMutationID
	turn.StableTurnID = claimed.StableTurnID
	if err := sess.appendUserInputTurnRefusingPoison(turn); err != nil {
		t.Fatalf("record the user-input turn: %v", err)
	}

	snapshot := sess.clientMutations.snapshot()
	const stopID = "stop-over-the-recorded-queue-claim"
	snapshot.InterruptFence = &clientMutationInterruptFence{
		ClientMutationID: stopID,
		ExpectedTurnID:   claimed.StableTurnID,
	}
	snapshot.Journal[stopID] = clientMutationRecord{ClientMutationID: stopID, Method: clientMutationMethodInterrupt}
	claimedRecorded := map[string]bool{claimed.ClientMutationID: true}

	// Held across the call: a finalizer that took Session.mu could not finish.
	sess.mu.Lock()
	_, err := finalizeClientMutationInterrupt(&snapshot, sess.ID(), func(string) string { return "" }, claimedRecorded)
	sess.mu.Unlock()
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if got := snapshot.ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after finalizing a recorded claim, want it cleared; left set, the session is wedged until a restart", got)
	}
	if _, still := snapshot.PendingExecutions[claimed.ClientMutationID]; still {
		t.Fatal("the recorded claim is still pending after finalization; it already ran, so it must settle")
	}
}

// TestStartAnnouncementHoldsTheTranscriptDoor: the announcement is not made
// after a health check taken outside the writer's door; it is made inside it, so
// no append can record a poisoning between the health decision and the
// publication. That is the structural half of the fix: there is no window left
// to inject into, which is why no injection can show the boundary -- an append
// that tries to interleave blocks on the door the announcement holds
// (issue #1165 review).
func TestStartAnnouncementHoldsTheTranscriptDoor(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-announce-holds-door",
		Input:            []appwire.InputItem{{Type: "text", Text: "runs"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	writer := sess.attachedTranscript()
	started := make(chan struct{})
	gotDoor := make(chan struct{})
	// The turn's model outcome is not this test's subject; that the announcement
	// runs under the door is.
	_, _, _ = sess.ProcessClientMutationStart(t.Context(), func(_ string, phase ClientMutationStartPhase) {
		if phase != ClientMutationStartClaimed {
			return
		}
		go func() {
			close(started)
			_ = writer.Poisoned() // takes the same write door an append holds
			close(gotDoor)
		}()
		<-started
		select {
		case <-gotDoor:
			t.Error("the announcement did not hold the transcript's write door: an append can record a poisoning between the health decision and the publication")
		case <-time.After(500 * time.Millisecond): // TRIPWIRE: the other side already holds no lock and is running; half a second is orders of magnitude above the instant it needs.
		}
	})
}

// armPoisonAtNextStoreCommit aims the transcript poisoning at the store's commit
// boundary: the partial write is armed now and runs from the next snapshot
// rename, which is after whatever health check preceded that commit. That is the
// window a claim's own refusal cannot cover -- the check and the commit are two
// steps over two different locks.
func armPoisonAtNextStoreCommit(t *testing.T, sess *Session) {
	t.Helper()
	armPoisonAtStoreCommitAfter(t, sess, 0)
}

// armPoisonAtStoreCommitAfter is armPoisonAtNextStoreCommit for a caller that
// needs the fault to pass some earlier commits by first -- the queue-head claim
// runs one before the carrier claim, and poisoning the first would exercise the
// claim's refusal rather than the announcement's.
func armPoisonAtStoreCommitAfter(t *testing.T, sess *Session, skip int) {
	t.Helper()
	fs := attachEnvironmentFailureFS(t, sess)
	armEnvironmentPartialWrite(fs)
	sess.clientMutations.faults.BeforeEffectSnapshotRename = func() error {
		if skip > 0 {
			skip--
			return nil
		}
		sess.clientMutations.faults.BeforeEffectSnapshotRename = nil
		if err := sess.writeTranscript(schema.NewTurn(schema.TurnAssistant, llm.Assistant("stops partway"))); err == nil {
			t.Error("partial transcript write reported success")
		}
		if !sess.attachedTranscript().Poisoned() {
			t.Error("the commit-boundary write did not poison the writer")
		}
		return nil
	}
}

// TestStartClaimDoesNotPublishWhenPoisonLandsAtTheCommit: poison injected after
// the claim's health check and at the commit it cannot cover. The claim commits,
// but the running turn must not be published, and the claim goes back so
// recovery can run it (issue #1165 review).
func TestStartClaimDoesNotPublishWhenPoisonLandsAtTheCommit(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-poisoned-at-commit",
		Input:            []appwire.InputItem{{Type: "text", Text: "never runs"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	armPoisonAtNextStoreCommit(t, sess)

	var announced []string
	var phases []ClientMutationStartPhase
	_, ran, err := sess.ProcessClientMutationStart(t.Context(), func(turnID string, phase ClientMutationStartPhase) {
		phases = append(phases, phase)
		if phase == ClientMutationStartClaimed {
			announced = append(announced, turnID)
		}
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("start with poison landing at the claim commit = (ran=%v, err=%v), want a refusal", ran, err)
	}
	if !slices.Contains(phases, ClientMutationStartArmed) {
		t.Fatalf("phases = %v, want the armed phase", phases)
	}
	if ran || slices.Contains(phases, ClientMutationStartClaimed) || len(announced) != 0 {
		t.Fatalf("published a running turn for a transcript poisoned at the claim commit: phases=%v announced=%v ran=%v", phases, announced, ran)
	}
	pending, still := sess.clientMutations.snapshot().PendingExecutions["start-poisoned-at-commit"]
	if !still || pending.ExecutionState != "accepted" {
		t.Fatalf("pending = %#v still = %v, want the claim handed back as accepted for recovery", pending, still)
	}
}

// TestWakeDoesNotPublishWhenPoisonLandsAtTheClaimCommit: the same injection for
// the wake's queue-head claim. The message must go back to the queue and no
// running turn may be published (issue #1165 review).
func TestWakeDoesNotPublishWhenPoisonLandsAtTheClaimCommit(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "queued-poisoned-at-commit", "runs after the restart")
	armPoisonAtNextStoreCommit(t, sess)

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("wake with poison landing at the claim commit = (%q, ran=%v, err=%v), want a refusal", result, ran, err)
	}
	if ran || len(announced) != 0 {
		t.Fatalf("published %v (ran=%v) for a transcript poisoned at the claim commit", announced, ran)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the refused wake = %d, want the message returned to the queue", got)
	}
}

// TestQueuedRecoveryClaimPoisonedAtTheCommitIsReturned: claimClientMutationStart
// also serves a queued recovery entry from the head of the input queue, so
// giving that claim back through the start path alone would no-op and leave it
// claimed, out of the queue, with its budget spent and the active turn pinned
// until a restart. It must go back through the queue path instead (issue #1165
// review).
func TestQueuedRecoveryClaimPoisonedAtTheCommitIsReturned(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "queued-recovery-poisoned-at-commit", "runs after the restart")
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "queued-recovery-poisoned-at-commit" {
		t.Fatalf("setup: claimed = %#v refusal = %v", claimed, refusal)
	}
	// Put it back at the head with its turn still the active one -- the shape
	// Start's queue branch claims.
	if err := sess.pushQueueHead(claimed); err != nil {
		t.Fatalf("pushQueueHead: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	if snapshot.ActiveTurnID != claimed.StableTurnID || len(snapshot.InputQueue) != 1 {
		t.Fatalf("setup: active = %q queue = %d, want the entry at the head with its turn active", snapshot.ActiveTurnID, len(snapshot.InputQueue))
	}
	armPoisonAtNextStoreCommit(t, sess)

	if _, _, err := sess.ProcessClientMutationStart(t.Context(), func(string, ClientMutationStartPhase) {}); !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("queued recovery claim poisoned at the commit = %v, want a refusal", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the refused queued claim = %d, want the message returned to the queue rather than left claimed", got)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the refused queued claim, want the active turn released", got)
	}
}

// TestCarrierAnnouncementPoisonedAtTheCommitReleasesTheSlot: the carrier's
// announce-time give-back is a distinct cleanup path from the queue case -- it
// releases the active-turn slot rather than returning a queue entry. If it
// regressed, ActiveTurnID would stay pinned to a turn that never runs and every
// later turn/start would be refused until a restart (issue #1165 review).
func TestCarrierAnnouncementPoisonedAtTheCommitReleasesTheSlot(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	if err := sess.SteerFromUser("carried after the window"); err != nil {
		t.Fatalf("SteerFromUser: %v", err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("setup: the steer is not pending")
	}
	// The queue pop commits first and claims nothing; the carrier's claim is the
	// next commit, so the poison is aimed there -- the claim lands and the
	// announcement is what refuses it.
	armPoisonAtStoreCommitAfter(t, sess, 1)

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("carrier poisoned at the announcement = (%q, ran=%v, err=%v), want a refusal", result, ran, err)
	}
	if ran || len(announced) != 0 {
		t.Fatalf("published %v (ran=%v) for a poisoned carrier announcement", announced, ran)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the refused announcement, want the carrier's slot released", got)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the refused announcement consumed the steering it never delivered")
	}
}

// TestStopReturnsAClaimedQueuedMessageRatherThanLosingIt: a queue entry claimed
// but never incorporated did not run, so a Stop that finalizes it must return it
// to the queue the way a cancelled turn's completion does. Retiring it as
// interrupted drops a durably accepted message and pins ActiveTurnID
// (issue #1165 review).
func TestStopReturnsAClaimedQueuedMessageRatherThanLosingIt(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "stopped-during-claim", "runs after the Stop")
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "stopped-during-claim" {
		t.Fatalf("setup: claimed = %#v refusal = %v", claimed, refusal)
	}

	if _, err := sess.InterruptClientMutation(t.Context(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-the-claimed-queue",
	}, func() {}); err != nil {
		t.Fatalf("stop over the claimed queue entry: %v", err)
	}
	if pending, still := sess.clientMutations.snapshot().PendingExecutions["stopped-during-claim"]; still && pending.ExecutionState == "interrupted" {
		t.Fatal("the Stop retired a claimed-but-unrun message as interrupted: it is lost")
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the Stop = %d, want the claimed-but-unrun message returned to the queue", got)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the Stop, want the claim's turn released", got)
	}
}

// TestWhileHealthyRefusesAClosedWriter is the writer door's other refusal: once
// closed, ordinary appends are silent no-ops, so an announcement against it would
// publish a turn that records nothing. It must refuse with its own reason rather
// than being reported as poisoned (issue #1165 review).
func TestWhileHealthyRefusesAClosedWriter(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	writer := sess.attachedTranscript()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	ran := false
	err := writer.WhileHealthy(func() { ran = true })
	if ran {
		t.Fatal("WhileHealthy ran an announcement against a closed writer")
	}
	if !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("WhileHealthy = %v, want transcript.ErrWriterClosed", err)
	}
	if errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatal("a closed writer was reported as poisoned")
	}
}

// TestStopDoesNotRequeueAClaimedQueuedMessageAlreadyInTheTranscript: "claimed"
// does not mean "never recorded". The user-input turn is written to the
// transcript before its incorporation mark, so a failed mark leaves a claimed
// execution whose turn is already on disk; requeueing that on a Stop appends and
// processes the message twice (issue #1165 review).
func TestStopDoesNotRequeueAClaimedQueuedMessageAlreadyInTheTranscript(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "claimed-and-recorded", "already recorded")
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "claimed-and-recorded" {
		t.Fatalf("setup: claimed = %#v refusal = %v", claimed, refusal)
	}
	// The transcript entry landed; only the store's incorporation mark failed, so
	// the pending execution is still claimed while the transcript holds the turn.
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("already recorded"))
	turn.ClientMutationID = claimed.ClientMutationID
	turn.StableTurnID = claimed.StableTurnID
	if err := sess.appendUserInputTurnRefusingPoison(turn); err != nil {
		t.Fatalf("record the user-input turn: %v", err)
	}
	if !sess.clientMutationTranscriptHolds(claimed.ClientMutationID, claimed.StableTurnID) {
		t.Fatal("setup: the recorded turn is not visible to the transcript check")
	}

	if _, err := sess.InterruptClientMutation(t.Context(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-over-the-recorded-claim",
	}, func() {}); err != nil {
		t.Fatalf("stop over the recorded claim: %v", err)
	}
	if got := sess.QueueDepth(); got != 0 {
		t.Fatalf("queue depth after the Stop = %d, want the already-recorded input left alone; requeued, it is appended and processed twice", got)
	}
}

// TestClosedTranscriptClaimIsRefusedBeforeItCommits: the claim boundary and the
// announcement boundary must decide on the same facts. A closed writer refused
// only at the announcement would be claimed durably -- active turn set, queue
// entry removed, budget spent -- and then handed back and woken, committing and
// fsyncing twice on every wake, which is a self-sustaining loop
// (issue #1165 review).
func TestClosedTranscriptClaimIsRefusedBeforeItCommits(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "closed-transcript-claim", "runs")
	if err := sess.attachedTranscript().Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	before := sess.clientMutations.snapshot().QueueRevision

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("wake over a closed transcript = (%q, ran=%v, err=%v), want the closed refusal", result, ran, err)
	}
	if ran || len(announced) != 0 {
		t.Fatalf("published %v (ran=%v) for a closed transcript", announced, ran)
	}
	if after := sess.clientMutations.snapshot().QueueRevision; after != before {
		t.Fatalf("QueueRevision moved %d -> %d: the claim committed durable state before the closed-writer refusal, so every wake claims, hands back and wakes again", before, after)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the refusal, want no claim taken", got)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the refusal = %d, want the message untouched", got)
	}
}

// TestTurnGateRefusesAClosedTranscript: the claims refuse a closed writer, so the
// turn gate that reads their refusal must know that fact too. A gate that knew
// only poison answered a closed writer's claim refusal as success -- the drain
// loop returned (outputs, nil) and skipped the settle/emit tail, telling the
// caller the input finished normally while the message stayed unclaimed
// (issue #1165 review).
func TestTurnGateRefusesAClosedTranscript(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if err := sess.attachedTranscript().Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	err := sess.refuseTurnOnUnhealthyTranscript(t.Context())
	if !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("turn gate over a closed transcript = %v, want transcript.ErrWriterClosed; nil means the claim refusal was reported as success", err)
	}
}

// TestStartClaimGoesBackWhenTheTranscriptClosesAfterTheAnnounce: a close can land
// after the claim and the announce, so the post-run give-back must know it the
// way it knows poison. Keyed on poisoning alone, the committed claim stayed
// claimed with the active turn pinned and its budget spent, on a turn that could
// never record (issue #1165 review).
func TestStartClaimGoesBackWhenTheTranscriptClosesAfterTheAnnounce(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-closed-after-announce",
		Input:            []appwire.InputItem{{Type: "text", Text: "never records"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	writer := sess.attachedTranscript()
	sess.cfg.testOnly.clientMutationStartAnnounced = func() {
		sess.cfg.testOnly.clientMutationStartAnnounced = nil
		if err := writer.Close(); err != nil {
			t.Errorf("close the transcript: %v", err)
		}
	}

	_, _, err := sess.ProcessClientMutationStart(t.Context(), func(string, ClientMutationStartPhase) {})
	if !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("start whose transcript closed after the announce = %v, want transcript.ErrWriterClosed", err)
	}
	pending, still := sess.clientMutations.snapshot().PendingExecutions["start-closed-after-announce"]
	if !still || pending.ExecutionState != "accepted" {
		t.Fatalf("pending = %#v still = %v, want the claim given back as accepted; left claimed, the active turn stays pinned", pending, still)
	}
}

// TestUserInputAppendRefusesAClosedTranscript: the user-input append is the last
// point that can notice a writer it will not record, and a close can land after
// the turn gate. The ordinary append is a silent nil no-op for a closed writer,
// so a path that keyed on poisoning alone put the turn in memory and carried on
// with no transcript record at all -- an input the session believes it ran and no
// restart can recover (issue #1165 review).
func TestUserInputAppendRefusesAClosedTranscript(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if err := sess.attachedTranscript().Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	before := len(sess.history)
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("recorded nowhere"))
	turn.ClientMutationID = "closed-transcript-append"
	turn.StableTurnID = "turn_closed_append"

	err := sess.appendUserInputTurnRefusingPoison(turn)
	if !errors.Is(err, transcript.ErrWriterClosed) {
		t.Fatalf("user-input append to a closed transcript = %v, want transcript.ErrWriterClosed", err)
	}
	if got := len(sess.history); got != before {
		t.Fatalf("history grew %d -> %d for a turn no transcript door accepted: the session believes it ran an input it did not record", before, got)
	}
	// It is the transcript refusing this turn, not a write that failed: the
	// warn-and-continue path would tell the operator a single record was lost
	// while the session carried on, and leave the refusal unclassified for the
	// callers that key on it.
	for _, event := range drainPendingEvents(sess) {
		if event.Kind != events.EventWarning {
			continue
		}
		warning, ok := event.Data.(events.WarningData)
		if ok && strings.Contains(warning.Message, "transcript write failed") {
			t.Fatalf("a closed transcript was reported as a write failure (%q) rather than as the transcript's refusal", warning.Message)
		}
	}
}

// TestCompletionDoesNotRequeueAClaimedQueuedMessageAlreadyInTheTranscript: the
// completion path's claimed-queue branch must not requeue a claimed execution
// whose turn the transcript already holds -- "claimed" does not mean "never
// recorded", and requeued it is appended and processed a second time
// (issue #1165 review).
func TestCompletionDoesNotRequeueAClaimedQueuedMessageAlreadyInTheTranscript(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "completion-claimed-recorded", "already recorded")
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "completion-claimed-recorded" {
		t.Fatalf("setup: claimed = %#v refusal = %v", claimed, refusal)
	}
	turn := schema.NewTurn(schema.TurnUserInput, llm.User("already recorded"))
	turn.ClientMutationID = claimed.ClientMutationID
	turn.StableTurnID = claimed.StableTurnID
	if err := sess.appendUserInputTurnRefusingPoison(turn); err != nil {
		t.Fatalf("record the user-input turn: %v", err)
	}
	if !sess.clientMutationTranscriptHolds(claimed.ClientMutationID, claimed.StableTurnID) {
		t.Fatal("setup: the recorded turn is not visible to the transcript check")
	}

	if _, err := sess.completeClientMutationTurnWithState(claimed.ClientMutationID, "terminal"); err != nil {
		t.Fatalf("complete the turn: %v", err)
	}
	if got := sess.QueueDepth(); got != 0 {
		t.Fatalf("queue depth after completion = %d, want the already-recorded input left alone; requeued, it is appended and processed twice", got)
	}
	// Not requeued is only half of it: left claimed, the execution keeps the
	// active turn pinned on a message the session already ran, wedging every
	// later turn until a restart.
	if pending, still := sess.clientMutations.snapshot().PendingExecutions[claimed.ClientMutationID]; still {
		t.Fatalf("pending = %#v after completion, want the recorded claim settled; left claimed, it wedges the session", pending)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after completion, want it cleared; left set, every later turn/start is refused until a restart", got)
	}
}

// TestWakeRefusesTheSteeringCarrierWhenPoisonLandsBeforeTheClaim: the wake's
// decision that a steer is runnable and the claim that takes its carrier are
// separated by a window a poisoning can land in. The refusal is decided inside
// the claim's own mutation now, on the generation the claim commits against, so
// a transcript that stops accepting records in that window stops the claim: no
// turn is announced and the steer stays where it is for the restart
// (issue #1165).
func TestWakeRefusesTheSteeringCarrierWhenPoisonLandsBeforeTheClaim(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	if err := sess.SteerFromUser("carried after the window"); err != nil {
		t.Fatalf("SteerFromUser: %v", err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("setup: the steer is not pending")
	}
	// The poisoning lands after the wake has decided the steer is work it could
	// carry, and before the claim commits.
	sess.cfg.testOnly.steeringCarrierClaiming = func() {
		sess.cfg.testOnly.steeringCarrierClaiming = nil
		poisonSessionTranscript(t, sess)
	}

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("wake carrying steering across the poison window = (%q, %v, %v), want a refusal wrapping transcript.ErrWriterPoisoned", result, ran, err)
	}
	if len(announced) != 0 {
		t.Fatalf("the refused wake announced turns %v, want a refusal ahead of any claim", announced)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the refused wake consumed the steering it never delivered")
	}
}

// TestWakeStandsDownWhenAStopParksTheSteerBeforeTheClaim: the mirror window.
// The wake no longer reads a claimability snapshot the claim might contradict,
// so a Stop that parks the steering rail between the wake's view of the work
// and the claim is not answered with an error for a claim that will not happen
// -- a parked steer is idle work, exactly as it is when the Stop is already in
// place (issue #1165).
func TestWakeStandsDownWhenAStopParksTheSteerBeforeTheClaim(t *testing.T) {
	sess := newTestSessionForEnvctx(t)
	if err := sess.SteerFromUser("parked by a stop that lands late"); err != nil {
		t.Fatalf("SteerFromUser: %v", err)
	}
	poisonSessionTranscript(t, sess)
	// The Stop lands after the wake has decided the steer is work, and before
	// the claim commits: the claim finds the rail closed and claims nothing.
	sess.cfg.testOnly.steeringCarrierClaiming = func() {
		sess.cfg.testOnly.steeringCarrierClaiming = nil
		if err := sess.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
			snapshot.SteeringHeld = true
			return nil
		}); err != nil {
			t.Fatalf("park the steering rail: %v", err)
		}
	}

	var announced []string
	result, ran, err := sess.ProcessPendingUserInput(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if result != "" || ran || err != nil {
		t.Fatalf("wake over a steer parked before the claim = (%q, %v, %v), want a quiet stand-down", result, ran, err)
	}
	if len(announced) != 0 {
		t.Fatalf("the stood-down wake announced turns %v, want none", announced)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the stand-down consumed the parked steer")
	}
	if !sess.clientMutations.steeringHeld() {
		t.Fatal("the stand-down released the Stop's hold on the steering rail")
	}
}

// TestPoisonedTranscriptRefusesTheQueueHeadClaim: the queue head's claim decides
// the poisoned-transcript refusal on the generation it commits against, so a
// transcript that has stopped accepting records cannot be handed a durable
// claim whose turn will die. The message stays queued for the restart
// (issue #1165).
func TestPoisonedTranscriptRefusesTheQueueHeadClaim(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "queued-behind-a-dead-transcript", "runs after the restart")
	poisonSessionTranscript(t, sess)

	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if !errors.Is(refusal, transcript.ErrWriterPoisoned) {
		t.Fatalf("popQueueHeadRefusingPoison refusal = %v, want one wrapping transcript.ErrWriterPoisoned", refusal)
	}
	if claimed.ClientMutationID != "" {
		t.Fatalf("popQueueHead claimed %q on a poisoned transcript; the turn it announced cannot be recorded", claimed.ClientMutationID)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the refused claim = %d, want the message still queued", got)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the refused claim, want empty", got)
	}
}

// TestClaimSamplesSessionMuOutsideTheStoreSerializer: a claim holds
// clientMutations.mu for the whole of its mutation, and the serializer must
// never wait on Session.mu there -- a Session.mu holder that then reaches the
// serializer would deadlock. The claim samples the transcript writer under
// Session.mu before it enters the serializer and reads the refusal with only the
// writer's own lock inside (issue #1165 review).
func TestClaimSamplesSessionMuOutsideTheStoreSerializer(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "queued-for-the-lock-order-check", "runs")

	// Both seams run on the claim goroutine. The entry seam records, from inside
	// the serializer, whether the sample had already happened -- so the order is
	// read at the claim's own entry point, not reconstructed afterwards from
	// channel state the claim goroutine may have raced ahead to change.
	sampled := make(chan struct{})
	sampledBeforeEntry := make(chan bool, 1)
	sess.cfg.testOnly.queueHeadClaimSampled = func() {
		sess.cfg.testOnly.queueHeadClaimSampled = nil
		close(sampled)
	}
	sess.cfg.testOnly.queueHeadClaimInSerializer = func() {
		sess.cfg.testOnly.queueHeadClaimInSerializer = nil
		select {
		case <-sampled:
			sampledBeforeEntry <- true
		default:
			sampledBeforeEntry <- false
		}
	}
	claimDone := make(chan struct{})
	go func() {
		defer close(claimDone)
		_, _ = sess.popQueueHeadRefusingPoison()
	}()

	// The entry seam always runs when the claim enters the serializer, so this
	// receive is the synchronization: no sleep, no polling, no time bound.
	if !<-sampledBeforeEntry {
		t.Fatal("the queue claim entered the mutation-store serializer before sampling the transcript under Session.mu: the serializer must never wait on s.mu")
	}
	<-claimDone
}

// TestStartClaimDoesNotAnnounceATurnItRefused: onRunnable publishes the running
// turn to the daemon and wires cancellation to it, so it must not run for a
// claim that refuses. A poisoning that lands after ProcessClientMutationStart's
// cheap pre-check and before the claim used to announce a phantom turn that the
// transcript could never record (issue #1165 review).
func TestStartClaimDoesNotAnnounceATurnItRefused(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-refused-by-poison",
		Input:            []appwire.InputItem{{Type: "text", Text: "never runs"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	// The poisoning lands in the claim window, after the pre-check saw a healthy
	// writer and before the claim commits.
	sess.cfg.testOnly.clientMutationStartClaiming = func() {
		sess.cfg.testOnly.clientMutationStartClaiming = nil
		poisonSessionTranscript(t, sess)
	}

	var announced []string
	var phases []ClientMutationStartPhase
	_, ran, err := sess.ProcessClientMutationStart(t.Context(), func(turnID string, phase ClientMutationStartPhase) {
		phases = append(phases, phase)
		if phase == ClientMutationStartClaimed {
			announced = append(announced, turnID)
		}
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("start across the poison window = (ran=%v, err=%v), want a refusal wrapping transcript.ErrWriterPoisoned", ran, err)
	}
	if !slices.Contains(phases, ClientMutationStartArmed) {
		t.Fatalf("phases = %v, want the armed phase before the claim so a Stop can cancel it", phases)
	}
	if ran || slices.Contains(phases, ClientMutationStartClaimed) || len(announced) != 0 {
		t.Fatalf("phases = %v announced %v (ran=%v), want no publish for a refused claim", phases, announced, ran)
	}
}

// TestStartClaimReportsArmedBeforeTheClaimCommits: cancellation is wired before
// the claim and publication only after it, in that order. A turn/interrupt that
// finalizes the claimed start in between must find a runner to cancel, or the
// start runs despite having been stopped (issue #1165 review).
func TestStartClaimReportsArmedBeforeTheClaimCommits(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-armed-then-claimed",
		Input:            []appwire.InputItem{{Type: "text", Text: "runs"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}

	var phases []ClientMutationStartPhase
	_, ran, err := sess.ProcessClientMutationStart(t.Context(), func(_ string, phase ClientMutationStartPhase) {
		phases = append(phases, phase)
	})
	// The turn's model outcome is not this test's subject; that the claim
	// happened is.
	if !ran {
		t.Fatalf("start = (ran=%v, err=%v), want the start claimed", ran, err)
	}
	if want := []ClientMutationStartPhase{ClientMutationStartArmed, ClientMutationStartClaimed}; !slices.Equal(phases, want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
}

// TestStartClaimRefusesAnIncorporatedStartRecoveryWhenPoisoned: an incorporated
// start is a crash-recovery reclaim, but selecting it still returns a claimed
// input the daemon announces as a running turn. A poisoned transcript owes it
// the same refusal as a fresh claim -- otherwise the turn is announced, the gate
// refuses it, and the pending entry stays active behind a phantom running turn
// (issue #1165 review).
func TestStartClaimRefusesAnIncorporatedStartRecoveryWhenPoisoned(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	runningStartTurn(t, sess, "incorporated-start-recovery", "already on disk")
	pending := sess.clientMutations.snapshot().PendingExecutions["incorporated-start-recovery"]
	if pending.ExecutionState != "incorporated" {
		t.Fatalf("setup: pending state = %q, want incorporated", pending.ExecutionState)
	}
	poisonSessionTranscript(t, sess)

	claimed, ok, err := sess.claimClientMutationStart()
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("incorporated start claim = (%#v, ok=%v, err=%v), want a refusal wrapping transcript.ErrWriterPoisoned", claimed, ok, err)
	}
	if ok || claimed.ClientMutationID != "" {
		t.Fatalf("claimed = (%#v, ok=%v), want nothing claimed", claimed, ok)
	}
	if _, still := sess.clientMutations.snapshot().PendingExecutions["incorporated-start-recovery"]; !still {
		t.Fatal("the refusal dropped the incorporated recovery entry; recovery can no longer run it")
	}
}

// TestStartClaimRefusesAnIncorporatedQueuedRecoveryWhenPoisoned: the queued
// recovery branch owes the same refusal. It names a queued turn whose transcript
// entry already landed and whose turn is still the active one, and selecting it
// announces that turn as running just like the start branch does.
func TestStartClaimRefusesAnIncorporatedQueuedRecoveryWhenPoisoned(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "incorporated-queued-recovery", "already on disk")
	queued, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || queued.ClientMutationID != "incorporated-queued-recovery" {
		t.Fatalf("setup: claimed = %#v refusal = %v", queued, refusal)
	}
	// Land the transcript entry a turn that ran would, leaving the pending
	// execution incorporated with the active turn still named.
	if err := sess.acceptUserInput(withQueuedClientMutation(t.Context(), queued), queued.Text, queued.Images, nil, false); err != nil {
		t.Fatalf("incorporate queued turn: %v", err)
	}
	snapshot := sess.clientMutations.snapshot()
	pending := snapshot.PendingExecutions["incorporated-queued-recovery"]
	if pending.ExecutionState != "incorporated" || snapshot.ActiveTurnID != pending.TurnID {
		t.Fatalf("setup: state = %q active = %q, want the incorporated turn named active", pending.ExecutionState, snapshot.ActiveTurnID)
	}
	poisonSessionTranscript(t, sess)

	claimed, ok, err := sess.claimClientMutationStart()
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("incorporated queued claim = (%#v, ok=%v, err=%v), want a refusal wrapping transcript.ErrWriterPoisoned", claimed, ok, err)
	}
	if ok || claimed.ClientMutationID != "" {
		t.Fatalf("claimed = (%#v, ok=%v), want nothing claimed", claimed, ok)
	}
}

// TestRefusedTurnGateReturnsADrainClaimedQueuedMessage: the drain loop claims a
// queued message, then the top-of-loop transcript gate refuses the turn. The
// claim must go back to the queue rather than sit claimed with ActiveTurnID
// pinned until restart recovery (issue #1165 review).
func TestRefusedTurnGateReturnsADrainClaimedQueuedMessage(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	queueOneMutation(t, sess, "claimed-then-refused", "runs after the restart")

	// Claim the message the way the drain loop does.
	claimed, refusal := sess.popQueueHeadRefusingPoison()
	if refusal != nil || claimed.ClientMutationID != "claimed-then-refused" {
		t.Fatalf("setup: claimed = %#v refusal = %v, want the queued message", claimed, refusal)
	}
	if got := sess.QueueDepth(); got != 0 {
		t.Fatalf("setup: queue depth = %d, want the message claimed out of the queue", got)
	}

	// The transcript stops accepting records before the turn runs, so the
	// top-of-loop gate refuses the already-claimed turn.
	poisonSessionTranscript(t, sess)

	ctx := withQueuedClientMutation(t.Context(), claimed)
	if _, err := sess.ProcessInputKind(ctx, claimed.Text, claimed.Images, EntryUserInput); !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("turn gate over a claimed queued message = %v, want transcript.ErrWriterPoisoned", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after the refused gate = %d, want the message returned to the queue", got)
	}
	if got := sess.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID = %q after the refused gate, want empty", got)
	}
}

// TestRefusedTurnGateKeepsAnIncorporatedClaim: the gate's give-back must not
// settle a claim whose transcript entry already landed. That turn is
// recoverable, and terminalizing it here marks a durably recorded turn dead and
// drops it instead of leaving it for restart recovery (issue #1165 review).
func TestRefusedTurnGateKeepsAnIncorporatedClaim(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	runningStartTurn(t, sess, "incorporated-start", "already on disk")
	pending := sess.clientMutations.snapshot().PendingExecutions["incorporated-start"]
	if pending.ExecutionState != "incorporated" {
		t.Fatalf("setup: pending state = %q, want incorporated", pending.ExecutionState)
	}

	// The transcript stops accepting records before the claimed turn runs, so
	// the top-of-loop gate refuses it.
	poisonSessionTranscript(t, sess)

	ctx := withQueuedClientMutation(t.Context(), queuedInput{
		ClientMutationID: "incorporated-start",
		StableTurnID:     pending.TurnID,
	})
	if _, err := sess.ProcessInputKind(ctx, "already on disk", nil, EntryUserInput); !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("refused gate over an incorporated claim = %v, want transcript.ErrWriterPoisoned", err)
	}
	snapshot := sess.clientMutations.snapshot()
	if _, still := snapshot.PendingExecutions["incorporated-start"]; !still {
		t.Fatal("the refused gate settled an incorporated claim: the durably recorded turn is dropped and recovery can no longer run it")
	}
	if snapshot.ActiveTurnID != pending.TurnID {
		t.Fatalf("ActiveTurnID = %q after the refused gate, want the incorporated turn %q", snapshot.ActiveTurnID, pending.TurnID)
	}
}

// TestStartClaimDoesNotRefusePoisonWithNothingClaimable: the start claim decides
// the poisoned-transcript refusal only where it takes a claim. A poisoning with
// nothing left to claim is a no-claim, not an error -- refusing there would
// report a poisoned transcript for work the claim never touched (issue #1165
// review).
func TestStartClaimDoesNotRefusePoisonWithNothingClaimable(t *testing.T) {
	sess := newQueuePersistTestSession(t, t.TempDir())
	defer sess.Close()
	poisonSessionTranscript(t, sess)

	claimed, ok, err := sess.claimClientMutationStart()
	if err != nil {
		t.Fatalf("start claim on a poisoned store with nothing claimable = %v, want a quiet no-claim", err)
	}
	if ok || claimed.ClientMutationID != "" {
		t.Fatalf("start claim = (%#v, ok=%v), want no claim", claimed, ok)
	}
}
