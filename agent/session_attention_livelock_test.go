package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The livelock this file pins was reported against a live session: once its
// provider started returning 401, the session ran turn after turn of
// identical failures (turns 1414-1479 in the report, one per paced-retry
// firing), and the Stop button could not end it. Root cause: a failed
// root-delegate-attention notification turn keeps its transcript-owned IDs
// pending and re-arms scheduleRootAttentionRetryLocked
// (finishRootDelegateAttentionTurn in session_attention.go), which doubles
// up to a maximum delay and then fires forever — and the interrupt path
// parks the queue and the steering rail but nothing reaches the attention
// rail, so every firing starts a fresh notification turn the user already
// asked to stop. Each firing is a full turn on the wire, which is what kept
// stealing the input panel's focus.
//
// The fix contract these tests pin, in order:
//
//  1. A PERMANENT provider failure (401) must not re-arm the paced retry.
//     The IDs stay pending and cached — deferred, not dropped — and ride
//     the next genuine wake: a user message, a fresh delegate event,
//     re-engagement after a Stop. A cancelled or user-aborted turn keeps
//     the retry (roundWasCancelled; the retirement family pins the
//     cancelled case); a Stop's own park is what keeps a stopped session
//     stopped.
//  2. A TRANSIENT failure keeps the paced retry. That retry is the delivery
//     guarantee for attention, and the permanent rule must not eat it.
//  3. A Stop parks the attention rail exactly as it parks the queue and the
//     steering: the paced retry cancels, a fresh arm caches its ID without
//     waking the session, a straggler wake stands down without a model turn,
//     and re-engagement (turn/start) re-arms the deferred IDs.
//  4. The park survives the restart the holds it mirrors survive: restore
//     seeds it from the durable holds, so a restarted daemon cannot re-arm
//     pending attention over a queue its user parked.
//  5. Parked is a projection of the durable holds, read at evaluation time:
//     no in-memory copy of the park can disagree with what the user stopped,
//     and a Stop's deferred attention is not autonomous work — the wire
//     state reads idle while it waits for re-engagement.
//
// The tests drive the real durable arm path (appendDelegateNotificationDurably
// + armDelegateAttention), the real turn path (ProcessInputKind with
// EntryNotification), the real interrupt path (InterruptClientMutation), and
// the real restore path (RestoreSessionFromMetaWithConfig), with only the
// LLM boundary scripted.

// newAttentionLivelockSession builds a session whose "openai" provider fails
// every stream with streamErr, on a fake clock, with a counted notify func.
// It returns the adapter so a test can count model calls. No serve loop is
// started: turns are driven by hand, so nothing runs behind the test's back.
func newAttentionLivelockSession(t *testing.T, streamErr error) (*Session, *agenttest.FakeClock, *atomic.Int64, *streamingAdapter) {
	t.Helper()
	dir := t.TempDir()
	adapter := &streamingAdapter{name: "openai", streamErr: streamErr}
	c := llm.NewClient()
	c.Register(adapter)
	// MaxRetries 0 keeps the transport's own retry out of the picture: what
	// these tests measure is the attention rail's paced retry, not the
	// provider retry chain beneath it.
	policy := llm.RetryPolicy{MaxRetries: 0}
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
		clock:          clk,
	}))
	var notifies atomic.Int64
	sess.SetNotifyFunc(func() { notifies.Add(1) })
	return sess, clk, &notifies, adapter
}

// armOneRootAttention appends a real durable delegate notification and arms
// it, the way a child delegate's terminal notification does.
func armOneRootAttention(t *testing.T, s *Session, delegateID, attentionID string) {
	t.Helper()
	content := "<delegate-notification delegate_id=\"" + delegateID + "\">terminal_error</delegate-notification>"
	if _, err := s.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append attention %s: %v", attentionID, err)
	}
	if err := s.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm attention %s: %v", attentionID, err)
	}
}

// attentionRailState snapshots the wake flag, the paced retry's armed flag,
// and the cached pending ID count under one lock acquisition.
func attentionRailState(s *Session) (wake, retryActive bool, pending int) {
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	return s.rootAttentionWake, s.rootAttentionRetry.active, len(s.rootAttentionWakeIDs)
}

// A permanent provider failure must not re-arm the paced retry. A 401 cannot
// be retried into success; every firing runs another doomed turn that
// steals focus and burns quota, and nothing ever gives up. The pending IDs
// stay cached for the next genuine wake.
func TestRootAttentionPermanentFailureDoesNotReArmThePacedRetry(t *testing.T) {
	s, clk, notifies, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 401, "unauthorized", nil, nil))
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_401", "delegate:dlg_401/delivery/1")
	baseline := notifies.Load()

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the 401 notification turn unexpectedly succeeded")
	}

	// The turn consumed the wake; a permanent failure must leave the paced
	// retry disarmed too.
	wake, retryActive, pending := attentionRailState(s)
	if retryActive {
		t.Fatal("a permanent 401 left the paced retry armed; it can never succeed, and every firing runs another doomed turn")
	}
	if wake {
		t.Fatal("a permanent 401 re-armed the attention wake")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids = %d, want 1 (deferred, not dropped)", pending)
	}

	// Nothing fires later either: advancing far past the retry's ceiling
	// must not produce another wake.
	clk.Advance(3 * jobNotificationRetryMaxDelay)
	clk.Drain()
	wake, retryActive, pending = attentionRailState(s)
	if retryActive {
		t.Fatal("the paced retry rearmed itself after a permanent failure")
	}
	if wake {
		t.Fatal("a timer fired after a permanent failure and re-armed the attention wake")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after advancing = %d, want 1", pending)
	}
	if got := notifies.Load(); got != baseline {
		t.Fatalf("notifies after a permanent failure = %d, want the baseline %d (no retry wake may fire)", got, baseline)
	}
}

// A transient provider failure keeps the paced retry: that retry is the
// delivery guarantee for attention — the item stays pending and the rail asks
// for another wake. This pins the blast radius of the permanent-failure rule.
func TestRootAttentionTransientFailureKeepsThePacedRetry(t *testing.T) {
	s, clk, notifies, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_503", "delegate:dlg_503/delivery/1")
	baseline := notifies.Load()

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the 503 notification turn unexpectedly succeeded")
	}

	_, retryActive, pending := attentionRailState(s)
	if !retryActive {
		t.Fatal("a transient 503 did not arm the paced retry; the delivery guarantee for transient failures regressed")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids = %d, want 1", pending)
	}

	// The retry fires and asks for another wake.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	wake, retryActive, pending := attentionRailState(s)
	if retryActive {
		t.Fatal("the paced retry is still armed after firing; it double-fires")
	}
	if !wake {
		t.Fatal("the paced retry fired without setting the attention wake")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the retry fired = %d, want 1", pending)
	}
	if got := notifies.Load(); got == baseline {
		t.Fatal("the paced retry fired without asking for another wake; the delivery is stranded")
	}
}

// A Stop must reach the attention rail the same way it reaches the queue and
// the steering: park it. The paced retry the Stop interrupts cancels, a
// straggler wake stands down without a model turn, a fresh child
// notification caches its ID without waking the session, and the deferred
// IDs re-arm only when the user re-engages.
func TestStopParksRootDelegateAttentionUntilReEngagement(t *testing.T) {
	s, clk, notifies, adapter := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	// Not serveSession: this test keeps the session unserved so its turns
	// run unnamed and nothing claims the parked queue behind the test's
	// back; the events channel still needs a consumer, so drain by hand.
	go func() {
		for range s.Events() {
		}
	}()

	// A queued message is what makes an idle-but-pending session report
	// processing, so the Stop is accepted (the shape pinned by
	// TestStopIsHonestAboutAQueuedMessage).
	queueOneMutation(t, s, "queue-behind-stop", "please still run me")

	// The livelock's own state: pending attention plus a transient failure
	// that left the paced retry armed.
	armOneRootAttention(t, s, "dlg_park", "delegate:dlg_park/delivery/1")
	baseline := notifies.Load()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the 503 notification turn unexpectedly succeeded")
	}
	if _, retryActive, _ := attentionRailState(s); !retryActive {
		t.Fatal("this test is not in the state it means to be: the transient failure did not arm the paced retry")
	}

	// Stop.
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-while-attention-pending",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	wake, retryActive, pending := attentionRailState(s)
	if retryActive {
		t.Fatal("the accepted Stop left the paced retry armed; it fires after the fence and starts another turn the user stopped")
	}
	if wake {
		t.Fatal("the accepted Stop left the attention wake set")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the stop = %d, want 1 (a Stop defers deliveries, it does not drop them)", pending)
	}

	// The cancelled retry's stale firing wakes nothing.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	wake, _, _ = attentionRailState(s)
	if wake {
		t.Fatal("the cancelled paced retry fired after the Stop and re-armed the attention wake")
	}
	if got := notifies.Load(); got != baseline {
		t.Fatalf("notifies after the Stop = %d, want the baseline %d (nothing may wake a stopped session)", got, baseline)
	}

	// A straggler wake — one already in the notify slot when the Stop
	// landed — stands down rather than running a model turn.
	_, streamCallsBefore := adapter.Counts()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("straggler EntryNotification while parked: %v", err)
	}
	_, streamCallsAfter := adapter.Counts()
	if streamCallsAfter != streamCallsBefore {
		t.Fatalf("a straggler wake while parked ran %d model call(s) the user stopped", streamCallsAfter-streamCallsBefore)
	}
	wake, _, pending = attentionRailState(s)
	if wake {
		t.Fatal("the straggler wake re-armed the attention rail while parked")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the straggler = %d, want 1", pending)
	}

	// A fresh child notification caches its ID but may not wake the session.
	armOneRootAttention(t, s, "dlg_park", "delegate:dlg_park/delivery/2")
	wake, _, pending = attentionRailState(s)
	if wake {
		t.Fatal("a fresh delegate notification woke a stopped session")
	}
	if pending != 2 {
		t.Fatalf("pending attention ids after the fresh notification = %d, want 2 (the ID must still be cached)", pending)
	}
	if got := notifies.Load(); got != baseline {
		t.Fatalf("notifies after the fresh notification = %d, want the baseline %d (a stopped session stays asleep)", got, baseline)
	}

	// Re-engagement unparks: turn/start releases the rail the same moment
	// it releases QueueHeld, and the deferred attention re-arms.
	beforeReEngage := notifies.Load()
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user re-engages"}},
	}); err != nil {
		t.Fatalf("turn/start after the stop: %v", err)
	}
	wake, _, pending = attentionRailState(s)
	if !wake {
		t.Fatal("re-engagement did not re-arm the deferred attention wake; the pending deliveries are stranded")
	}
	if pending != 2 {
		t.Fatalf("pending attention ids after re-engagement = %d, want 2", pending)
	}
	if got := notifies.Load(); got == beforeReEngage {
		t.Fatal("re-engagement re-armed the wake without asking for a notification turn to deliver it")
	}
}

// A user-aborted turn keeps the paced retry for the same reason a cancelled
// one does: the abort is the user stopping the request, not the provider
// refusing it, and the delivery the turn owed is still owed. An AbortError
// with no Canceled cause must not fall into the permanent branch — the
// isAbortError half of roundWasCancelled is what keeps it out.
func TestRootAttentionAbortedTurnKeepsThePacedRetry(t *testing.T) {
	s, clk, _, _ := newAttentionLivelockSession(t, llm.NewAbortError("user aborted the request", nil))
	serveSession(t, s)

	armOneRootAttention(t, s, "dlg_abort", "delegate:dlg_abort/delivery/1")

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
		t.Fatal("the aborted notification turn unexpectedly succeeded")
	}

	_, retryActive, pending := attentionRailState(s)
	if !retryActive {
		t.Fatal("an aborted attention turn did not arm the paced retry; the delivery-liveness chain regressed")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids = %d, want 1", pending)
	}
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	if _, retryActive, _ = attentionRailState(s); retryActive {
		t.Fatal("the paced retry is still armed after firing")
	}
}

// A Stop retried after the user re-engaged must not re-park the attention
// rail: replay re-runs no accept callback, so the durable QueueHeld a park
// mirrors is already released, and the deferred attention the re-engagement
// just re-armed stays armed.
func TestStopReplayAfterReEngagementDoesNotReparkAttention(t *testing.T) {
	s, _, notifies, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	// Not serveSession, for the same reason as the park test above.
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_replay", "delegate:dlg_replay/delivery/1")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-replayed-late",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Re-engage: turn/start unparks and re-arms the deferred attention.
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user re-engages"}},
	}); err != nil {
		t.Fatalf("turn/start after the stop: %v", err)
	}
	wake, retryActive, pending := attentionRailState(s)
	if !wake || retryActive || pending != 1 {
		t.Fatalf("re-engagement left wake=%t retry=%t pending=%d; this test is not in the state it means to be", wake, retryActive, pending)
	}
	afterReEngage := notifies.Load()

	// The Stop's client retries it after its response was lost: the store
	// replays the terminal mutation.
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-replayed-late",
	}, func() {}); err != nil {
		t.Fatalf("replayed stop: %v", err)
	}

	wake, retryActive, pending = attentionRailState(s)
	if !wake {
		t.Fatal("the replayed Stop re-parked the attention rail past the user's live engagement; QueueHeld is released, so nothing would re-arm it until the next re-engagement")
	}
	if retryActive {
		t.Fatal("this test is not in the state it means to be: no paced retry should be armed here")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the replay = %d, want 1", pending)
	}
	if got := notifies.Load(); got != afterReEngage {
		t.Fatalf("notifies after the replay = %d, want %d (the replayed Stop re-parked and re-armed nothing)", got, afterReEngage)
	}
}

// A replayed accept must not unpark the rail: replay re-runs no effect
// callback, so it releases no QueueHeld, and a Stop that landed after the
// original application must keep the session silent. A client retrying a
// turn/queue whose response was lost wakes nothing.
func TestReplayedAcceptAfterStopDoesNotWakeTheSession(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_replay_accept", "delegate:dlg_replay_accept/delivery/1")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-replayed-accept",
	}, func() {}); err != nil {
		t.Fatalf("first stop: %v", err)
	}

	// Fresh re-engagement: the queue accept unparks and re-arms.
	queueOneMutation(t, s, "q-retry-me", "the user re-engages")
	wake, _, pending := attentionRailState(s)
	if !wake || pending != 1 {
		t.Fatalf("re-engagement left wake=%t pending=%d; this test is not in the state it means to be", wake, pending)
	}

	// A second Stop parks again, now over the re-armed wake.
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-after-re-engagement",
	}, func() {}); err != nil {
		t.Fatalf("second stop: %v", err)
	}
	wake, _, _ = attentionRailState(s)
	if wake {
		t.Fatal("the second stop did not park the re-armed rail; this test is not in the state it means to be")
	}
	// The queue accept's client retries it after its response was lost: the
	// store replays the terminal mutation, and the replayed accept must not
	// unpark.
	queueOneMutation(t, s, "q-retry-me", "the user re-engages")

	wake, _, pending = attentionRailState(s)
	if wake {
		t.Fatal("a replayed accept unparked the attention rail past the Stop that landed after its original application")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the replayed accept = %d, want 1", pending)
	}
	// The replayed accept may still re-provoke the pending-input wake — a
	// replayed queue accept owes the queue its run (wms7), and the parked
	// claim path declines the nudge. That wake is the queue's business; the
	// attention rail's state is this test's contract, asserted above and
	// below.

	// And the rail still suppresses a fresh arm.
	armOneRootAttention(t, s, "dlg_replay_accept", "delegate:dlg_replay_accept/delivery/2")
	wake, _, pending = attentionRailState(s)
	if wake {
		t.Fatal("a fresh delegate notification woke a session the replay left unparked")
	}
	if pending != 2 {
		t.Fatalf("pending attention ids after the fresh arm = %d, want 2", pending)
	}
}

// A replayed Stop parks while either durable hold its fresh application
// mirrored still stands. Today's re-engagements release QueueHeld and
// SteeringHeld together, so a steering hold outliving its queue hold takes
// the same hand-seeded shape the steering-hold tests use — the guard reads
// both precisely so a future asymmetric release cannot leave a stopped rail
// live.
func TestReplayedStopParksWhileASteeringHoldStands(t *testing.T) {
	s, _, notifies, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_steer_hold", "delegate:dlg_steer_hold/delivery/1")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-with-a-steer-hold",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Fresh re-engagement releases both holds and unparks.
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user re-engages"}},
	}); err != nil {
		t.Fatalf("turn/start after the stop: %v", err)
	}
	wake, _, _ := attentionRailState(s)
	if !wake {
		t.Fatal("re-engagement did not re-arm; this test is not in the state it means to be")
	}

	// The asymmetric hold: steering standing, queue released.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = false
		snapshot.SteeringHeld = true
		return nil
	}); err != nil {
		t.Fatalf("seed the steering-only hold: %v", err)
	}
	beforeReplay := notifies.Load()

	// The Stop's client retries it: the replay must park against the standing
	// steering hold.
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-with-a-steer-hold",
	}, func() {}); err != nil {
		t.Fatalf("replayed stop: %v", err)
	}

	wake, _, pending := attentionRailState(s)
	if wake {
		t.Fatal("the replayed Stop left the attention rail live beside a standing steering hold it should have parked against")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the replayed stop = %d, want 1", pending)
	}
	if got := notifies.Load(); got != beforeReplay {
		t.Fatalf("notifies after the replayed stop = %d, want %d (the park asks for no wake)", got, beforeReplay)
	}
}

// A stale watch tick — one whose token died or whose timer was cleared after
// it queued — counts toward the outer gate's raw peek but delivers nothing:
// filterDeliverableJobNotifications drops it. Alone on a parked rail it must
// not phantom-open the gate and run a model turn the user stopped; the
// in-turn stand-down catches what the raw-depth carve-out let through.
func TestStaleWatchTickDoesNotPhantomOpenTheParkedGate(t *testing.T) {
	s, _, _, adapter := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_stale_tick", "delegate:dlg_stale_tick/delivery/1")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-stale-tick",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// A watch tick whose timer does not exist: queued raw, dropped by the
	// deliverability filter.
	s.enqueueJobNotification(jobNotification{
		Kind:    jobNotificationKindWatch,
		Status:  jobNotificationEventWatch,
		WatchID: "ghost-timer",
	})

	_, streamCallsBefore := adapter.Counts()
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("stale-tick wake while parked: %v", err)
	}
	_, streamCallsAfter := adapter.Counts()
	if streamCallsAfter != streamCallsBefore {
		t.Fatalf("a stale watch tick phantom-opened the parked gate and ran %d model call(s) the user stopped", streamCallsAfter-streamCallsBefore)
	}
	wake, _, pending := attentionRailState(s)
	if wake {
		t.Fatal("the stale-tick wake re-armed the attention rail while parked")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after the stale tick = %d, want 1", pending)
	}
}

// A wake that carries a REAL job notification still runs while parked — job
// delivery is not the user's to stop — and the parked attention it carries in
// history rides that successful turn: a notification turn resolves its begin
// snapshot, so the attention is delivered, not stranded. The rail itself
// stays parked; only re-engagement unparks.
func TestParkedRailRunsAJobCarryingWakeAndDeliversItsAttention(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: repeatFinalResponse(4, "ok")})
	clk := agenttest.NewFakeClock()
	s := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		StateDir:       dir,
		clock:          clk,
		LLMRetryPolicy: &llm.RetryPolicy{MaxRetries: 0},
	}))
	var notifies atomic.Int64
	s.SetNotifyFunc(func() { notifies.Add(1) })
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_job", "delegate:dlg_job/delivery/1")

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-job",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// A watch frame with no timer identity is deliverable by construction:
	// the deliverability filter only demands timer liveness for named ticks.
	beforeEnqueue := notifies.Load()
	s.enqueueJobNotificationAndNotify(jobNotification{
		Kind:   jobNotificationKindWatch,
		Status: jobNotificationEventWatch,
		JobID:  "live-watch",
	})
	if got := notifies.Load(); got != beforeEnqueue+1 {
		t.Fatalf("the job notification's enqueue asked for %d wake(s), want exactly one", got-beforeEnqueue)
	}

	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("job-carrying wake while parked: %v", err)
	}

	// The turn ran and succeeded, which the consumption proves: a successful
	// notification turn resolves its begin snapshot.
	fold, err := readDelegateAttentionFold(transcriptPath(dir, s.ID()), s.ID())
	if err != nil {
		t.Fatalf("read fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("the job turn left its begin-snapshot attention unresolved: %v", pending)
	}
	wake, retryActive, pending := attentionRailState(s)
	if wake || retryActive || pending != 0 {
		t.Fatalf("after the job turn: wake=%t retry=%t pending=%d, want the attention delivered", wake, retryActive, pending)
	}

	// The rail stays parked: a fresh arm is still cached silently.
	armOneRootAttention(t, s, "dlg_job", "delegate:dlg_job/delivery/2")
	wake, _, pending = attentionRailState(s)
	if wake || pending != 1 {
		t.Fatalf("after the fresh arm on the still-parked rail: wake=%t pending=%d, want cached and silent", wake, pending)
	}
}

// A Stop's parked attention is not live work for a drain: it is deferred to
// re-engagement and must not hold the drain open or keep its rung spinning.
func TestParkedRootAttentionIsNotDrainLive(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_drain_live", "delegate:dlg_drain_live/delivery/1")

	live, err := s.subtreeHasLiveComponent()
	if err != nil {
		t.Fatalf("subtreeHasLiveComponent: %v", err)
	}
	if !live {
		t.Fatal("this test is not in the state it means to be: pending attention should read live before the Stop")
	}

	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-drain",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	live, err = s.subtreeHasLiveComponent()
	if err != nil {
		t.Fatalf("subtreeHasLiveComponent after the stop: %v", err)
	}
	if live {
		t.Fatal("parked attention keeps the drain's liveness read busy; it is deferred to re-engagement and must not hold a drain open")
	}
}

// A Stop's park must survive the restart the holds it mirrors survive.
// Restore rebuilds the wake cache from the transcript fold; when a durable
// hold stands there, the rebuilt rail must come up parked rather than waking
// over a queue the user parked. The old restore armed the wake unconditionally,
// so one transient failure re-armed the paced retry and reproduced the exact
// livelock this series ends — one restart later. The crash-during-Stop
// recovery finalizes the fence but leaves the holds standing, so seeding
// from them covers that path too.
func TestRestartSeedsTheAttentionParkFromTheDurableHolds(t *testing.T) {
	dir := t.TempDir()
	clk := agenttest.NewFakeClock()
	transient := llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil)
	policy := llm.RetryPolicy{MaxRetries: 0}

	// Session one: pending durable attention plus an accepted Stop.
	c := llm.NewClient()
	c.Register(&streamingAdapter{name: "openai", streamErr: transient})
	s := newSession(t, withClient(c), withDir(dir), withConfig(SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
		clock:          clk,
	}))
	var notifies atomic.Int64
	s.SetNotifyFunc(func() { notifies.Add(1) })
	go func() {
		for range s.Events() {
		}
	}()
	id := s.Meta().ID

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_restart", "delegate:dlg_restart/delivery/1")
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-restart",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !s.rootAttentionRailParked() {
		t.Fatal("this test is not in the state it means to be: the accepted Stop left the live rail unparked")
	}
	// The crash: no re-engagement, the process just ends. The holds and the
	// transcript survive it; the in-memory park and wake do not.
	s.Close()

	// Session two: the restart, through the same restore plumbing a daemon
	// restart uses, with the livelock's own transient provider behind it.
	meta, err := schema.LoadSessionMeta(dir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restoredClient := llm.NewClient()
	adapter := &streamingAdapter{name: "openai", streamErr: transient}
	restoredClient.Register(adapter)
	restored, err := RestoreSessionFromMetaWithConfig(restoredClient, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
		clock:          clk,
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	var restoredNotifies atomic.Int64
	restored.SetNotifyFunc(func() { restoredNotifies.Add(1) })
	go func() {
		for range restored.Events() {
		}
	}()

	if !restored.rootAttentionRailParked() {
		t.Fatal("the restart lost the Stop's park: restore rebuilt the rail unparked, and the pending attention can re-arm the paced retry over a queue the user parked")
	}
	wake, retryActive, pending := attentionRailState(restored)
	if wake {
		t.Fatal("restore woke the attention rail over a still-held queue")
	}
	if retryActive {
		t.Fatal("restore armed the paced retry")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after restore = %d, want 1 (deferred, not dropped)", pending)
	}

	// A wake that reaches the parked rail stands down without a model turn,
	// exactly as it does live.
	_, streamCallsBefore := adapter.Counts()
	if _, err := restored.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("straggler EntryNotification while parked after restore: %v", err)
	}
	_, streamCallsAfter := adapter.Counts()
	if streamCallsAfter != streamCallsBefore {
		t.Fatalf("a straggler wake while parked after restore ran %d model call(s) the user stopped", streamCallsAfter-streamCallsBefore)
	}
	if wake, _, _ := attentionRailState(restored); wake {
		t.Fatal("the straggler wake re-armed the attention rail while parked after restore")
	}

	// Re-engagement unparks and re-arms the deferred attention, so the
	// restart deferred the delivery rather than dropping it.
	beforeReEngage := restoredNotifies.Load()
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-restart",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user re-engages"}},
	}); err != nil {
		t.Fatalf("turn/start after the restart: %v", err)
	}
	wake, _, pending = attentionRailState(restored)
	if !wake {
		t.Fatal("re-engagement did not re-arm the deferred attention wake after the restart; the pending deliveries are stranded")
	}
	if pending != 1 {
		t.Fatalf("pending attention ids after re-engagement = %d, want 1", pending)
	}
	if got := restoredNotifies.Load(); got == beforeReEngage {
		t.Fatal("re-engagement re-armed the wake without asking for a notification turn to deliver it")
	}
}

// The rail's parked state is not stored beside the wake cache: it is a
// projection of the durable holds, read at evaluation time. Pin the
// projection directly — a hold set through the store alone parks the rail,
// and clearing it through the store alone releases it, with no park or
// unpark call in between. An in-memory flag would stay stale at exactly the
// moments a Stop and a re-engagement race, and the stale copy is what could
// re-open the livelock over a queue the user parked.
func TestAttentionRailParkedIsAProjectionOfTheDurableHolds(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	if s.rootAttentionRailParked() {
		t.Fatal("a session with no holds must read unparked")
	}
	// The hold arrives through the store only — the way a Stop's admission
	// writes it — with no park call.
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = true
		return nil
	}); err != nil {
		t.Fatalf("seed the hold: %v", err)
	}
	if !s.rootAttentionRailParked() {
		t.Fatal("a standing durable hold must park the rail with no in-memory park write; a stored flag could disagree with the holds")
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.QueueHeld = false
		return nil
	}); err != nil {
		t.Fatalf("clear the hold: %v", err)
	}
	if s.rootAttentionRailParked() {
		t.Fatal("a released hold must release the rail with no in-memory unpark write")
	}
}

// A Stop-parked rail's deferred attention is not autonomous work: nothing
// idle after the Stop — exactly as a parked queue reads zero for
// pendingQueueDepth — and the busy projection returns with re-engagement.
func TestParkedRootAttentionIsNotAutonomousWork(t *testing.T) {
	s, _, _, _ := newAttentionLivelockSession(t, llm.ErrorFromHTTPStatus("openai", 503, "upstream unavailable", nil, nil))
	go func() {
		for range s.Events() {
		}
	}()

	queueOneMutation(t, s, "queue-behind-stop", "please still run me")
	armOneRootAttention(t, s, "dlg_busy", "delegate:dlg_busy/delivery/1")

	if got := s.WireState(); got != string(SessionProcessing) {
		t.Fatalf("this test is not in the state it means to be: pending attention plus a queued message must read busy before the Stop, got %q", got)
	}
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "stop-before-busy-read",
	}, func() {}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := s.WireState(); got != string(SessionIdle) {
		t.Fatalf("a Stop-parked session projects %q; parked attention is deferred to re-engagement and must not read as autonomous work", got)
	}

	// Re-engagement restores the busy projection: the still-pending
	// attention counts as work again once the rail is released.
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-after-stop",
		Input:            []appwire.InputItem{{Type: "text", Text: "the user re-engages"}},
	}); err != nil {
		t.Fatalf("turn/start after the stop: %v", err)
	}
	if got := s.WireState(); got != string(SessionProcessing) {
		t.Fatalf("re-engagement must restore the busy projection, got %q", got)
	}
}
