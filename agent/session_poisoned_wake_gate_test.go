package agent

import (
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

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

	// Hold Session.mu across the claim, the way a meta construction or a history
	// fold that later reaches the serializer would.
	sess.mu.Lock()
	claimDone := make(chan struct{})
	go func() {
		defer close(claimDone)
		_, _ = sess.popQueueHeadRefusingPoison()
	}()

	// The claim cannot finish while Session.mu is held either way; the question
	// is only what it holds while it waits. A fixed sleep cannot prove the claim
	// reached the serializer (a descheduled goroutine leaves the mutex free for
	// an unrelated reason), so watch the mutex for the whole window: any
	// observation of clientMutations.mu held is the violation.
	deadline := time.Now().Add(2 * time.Second)
	violation := false
	for time.Now().Before(deadline) {
		if !sess.clientMutations.mu.TryLock() {
			violation = true
			break
		}
		sess.clientMutations.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	sess.mu.Unlock()
	<-claimDone
	if violation {
		t.Fatal("the queue claim held the mutation-store mutex while waiting on Session.mu: the serializer must never wait on s.mu")
	}
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
	_, ran, err := sess.ProcessClientMutationStart(t.Context(), func(turnID string) {
		announced = append(announced, turnID)
	})
	if !errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("start across the poison window = (ran=%v, err=%v), want a refusal wrapping transcript.ErrWriterPoisoned", ran, err)
	}
	if ran || len(announced) != 0 {
		t.Fatalf("announced %v (ran=%v), want no announcement for a refused claim", announced, ran)
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
