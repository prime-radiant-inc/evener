package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

const compactedAttentionText = `<delegate-notification delegate_id="dlg_worker" kind="update">COMPACTED_UPDATE: which table holds the drain cursor?</delegate-notification>`

// newCompactableSession builds a session whose summarizer is scripted and whose
// main model records its requests and answers each with a final report.
func newCompactableSession(t *testing.T, configure ...func(*SessionConfig)) (*Session, *fakeAdapter) {
	t.Helper()
	main := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("answered the update") })
	client := llm.NewClient()
	client.Register(main)
	client.Register(&agenttest.ScriptedAdapter{Provider: "compact-cheap", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}})
	stateDir := t.TempDir()
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "compact-cheap/model")
	cfg := SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}
	for _, configure := range configure {
		configure(&cfg)
	}
	sess := newSession(t, withClient(client), withProfile(profile), withDir(stateDir), withConfig(cfg))
	return sess, main
}

func compactAfterAttention(t *testing.T, sess *Session) {
	t.Helper()
	seedNumberedSessionHistory(t, sess, 12)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

// A draining delegate's pending update survives a live compaction: the update's
// attention turn stays in the history, so the drain's one turn presents it,
// credits it, and marks it consumed with no second turn.
func TestDelegateUpdatePendingAcrossCompactionIsPresentedAndConsumedOnce(t *testing.T) {
	t.Parallel()
	sess, main := newCompactableSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_coordinator", "")
	controller.mu.Lock()
	controller.live["dlg_coordinator"].binding.runtime = sess
	controller.mu.Unlock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_coordinator"
	updateID := delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_worker", 1))
	if _, err := sess.appendDelegateNotificationDurably(updateID, compactedAttentionText); err != nil {
		t.Fatalf("append update attention: %v", err)
	}
	compactAfterAttention(t, sess)

	turns := 0
	process := func(ctx context.Context, text string, images []ImageAttachment, kind EntryKind) (string, error) {
		turns++
		if turns > 2 {
			t.Error("the drain keeps running turns for an update that stays pending")
			return "", context.Canceled
		}
		return sess.ProcessInputKind(ctx, text, images, kind)
	}
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_coordinator", generation: 1})
	if _, err := sess.drainJobTreeWith(ctx, make(chan time.Time), func(context.Context) error { return nil }, process); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}
	if len(main.requests) != 1 || !requestMessagesContainText(main.requests[0].Messages, "COMPACTED_UPDATE") {
		t.Fatalf("the drain made %d model requests; want 1 presenting the compacted update", len(main.requests))
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the drain turn = %v (err %v), want it consumed", pending, err)
	}
}

// The root's pending delegate attention survives a live compaction the same
// way: its notification turn presents the report and consumes it.
func TestRootDelegateAttentionPendingAcrossCompactionIsPresentedAndConsumed(t *testing.T) {
	t.Parallel()
	sess, main := newCompactableSession(t)
	const attentionID = "delegate:dlg_worker/delivery/1"
	if _, err := sess.appendDelegateNotificationDurably(attentionID, compactedAttentionText); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := sess.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm attention: %v", err)
	}
	compactAfterAttention(t, sess)

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if len(main.requests) != 1 || !requestMessagesContainText(main.requests[0].Messages, "COMPACTED_UPDATE") {
		t.Fatalf("the notification turn made %d model requests; want 1 that presents the attention item pending across compaction", len(main.requests))
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the notification turn = %v (err %v), want it consumed", pending, err)
	}
}

// A transcript read that fails during the fold cannot say which attention items
// are pending, so the fold keeps every attention turn the history held: the
// item is still presented to, and consumed by, the turn that follows.
func TestRootDelegateAttentionSurvivesCompactionWhenTheTranscriptReadFails(t *testing.T) {
	t.Parallel()
	var failReads atomic.Bool
	sess, main := newCompactableSession(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if failReads.Load() {
				return delegateAttentionFold{}, errors.New("transient transcript read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	const attentionID = "delegate:dlg_worker/delivery/1"
	if _, err := sess.appendDelegateNotificationDurably(attentionID, compactedAttentionText); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := sess.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm attention: %v", err)
	}
	failReads.Store(true)
	compactAfterAttention(t, sess)
	failReads.Store(false)

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if len(main.requests) != 1 || !requestMessagesContainText(main.requests[0].Messages, "COMPACTED_UPDATE") {
		t.Fatalf("the notification turn made %d model requests; want 1 that presents the attention item pending across compaction", len(main.requests))
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the notification turn = %v (err %v), want it consumed", pending, err)
	}
}

// Items delivered before and after the compaction boundary keep their delivery
// order: the older item is restored ahead of the newer one that survived in the
// recent tail, and one turn presents and consumes both.
func TestRootDelegateAttentionKeepsDeliveryOrderAcrossCompaction(t *testing.T) {
	t.Parallel()
	sess, main := newCompactableSession(t)
	deliver := func(attentionID, text string) {
		t.Helper()
		if _, err := sess.appendDelegateNotificationDurably(attentionID, text); err != nil {
			t.Fatalf("append attention: %v", err)
		}
		if err := sess.armDelegateAttention(attentionID); err != nil {
			t.Fatalf("arm attention: %v", err)
		}
	}
	deliver("delegate:dlg_worker/delivery/1", `<delegate-notification delegate_id="dlg_worker" kind="update">FIRST_ITEM</delegate-notification>`)
	seedNumberedSessionHistory(t, sess, 12)
	deliver("delegate:dlg_worker/delivery/2", `<delegate-notification delegate_id="dlg_worker" kind="update">SECOND_ITEM</delegate-notification>`)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if len(main.requests) != 1 {
		t.Fatalf("the notification turn made %d model requests, want 1", len(main.requests))
	}
	text := messagesText(main.requests[0].Messages)
	first, second := strings.Index(text, "FIRST_ITEM"), strings.Index(text, "SECOND_ITEM")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("request presents the items at %d and %d, want both with the first ahead:\n%s", first, second, text)
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the notification turn = %v (err %v), want both consumed", pending, err)
	}
}

// A restored attention turn lands ahead of the in-flight turn, so the fold's
// baseline correction counts it: the in-flight turn stays exactly where the
// boundary says it starts, and replay filtering still treats it as in flight.
func TestRootDelegateAttentionRestoredAheadOfTheInFlightTurnMovesTheBaseline(t *testing.T) {
	t.Parallel()
	sess, _ := newCompactableSession(t)
	deliver := func(attentionID, text string) {
		t.Helper()
		if _, err := sess.appendDelegateNotificationDurably(attentionID, text); err != nil {
			t.Fatalf("append attention: %v", err)
		}
	}
	deliver("delegate:dlg_worker/delivery/1", `<delegate-notification delegate_id="dlg_worker" kind="update">FIRST_ITEM</delegate-notification>`)
	seedNumberedSessionHistory(t, sess, 12)
	deliver("delegate:dlg_worker/delivery/2", `<delegate-notification delegate_id="dlg_worker" kind="update">SECOND_ITEM</delegate-notification>`)
	seedNumberedSessionHistory(t, sess, 2)
	history := currentHistory(t, sess)
	inFlightText := history[len(history)-2].Message.Text()
	sess.mu.Lock()
	sess.turnHistoryBaseline = len(history) - 2
	sess.mu.Unlock()

	if err := sess.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	sess.applyPendingForceCompact(context.Background())

	post := currentHistory(t, sess)
	want := indexOfTurnText(post, inFlightText)
	if want < 0 {
		t.Fatalf("in-flight turn %q did not survive the fold", inFlightText)
	}
	if indexOfTurnText(post, `<delegate-notification delegate_id="dlg_worker" kind="update">FIRST_ITEM</delegate-notification>`) < 0 {
		t.Fatal("test setup: the fold did not summarize away and restore the first item")
	}
	sess.mu.Lock()
	got := sess.turnHistoryBaseline
	sess.mu.Unlock()
	if got != want {
		t.Fatalf("turnHistoryBaseline = %d, want %d (the in-flight turn's post-fold index)", got, want)
	}
}

// After a restart the resumed history starts at the last compaction marker, so
// an item delivered before the marker is restored behind one delivered after
// it unless the rearm keeps delivery order.
func TestRootDelegateAttentionKeepsDeliveryOrderAcrossCompactionRestart(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	if err := os.MkdirAll(schema.SessionsDir(stateDir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	const firstText = `<delegate-notification delegate_id="dlg_worker" kind="update">FIRST_ITEM</delegate-notification>`
	const secondText = `<delegate-notification delegate_id="dlg_worker" kind="update">SECOND_ITEM</delegate-notification>`
	appendTurn := func(turn schema.Turn) {
		t.Helper()
		if err := writer.AppendDurable(turn); err != nil {
			t.Fatalf("append %q: %v", turn.Message.Text(), err)
		}
	}
	attention := func(attentionID, text string) schema.Turn {
		turn := schema.NewTurn(schema.TurnSteering, llm.User(text))
		turn.AttentionID = attentionID
		turn.StableTurnID = newQueueEntryID()
		return turn
	}
	appendTurn(attention("delegate:dlg_worker/delivery/1", firstText))
	appendTurn(schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nfold after the first item\n[END SUMMARY]")))
	appendTurn(attention("delegate:dlg_worker/delivery/2", secondText))
	if err := writer.Close(); err != nil {
		t.Fatalf("close root transcript: %v", err)
	}
	meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save root metadata: %v", err)
	}
	restored, err := RestoreSessionFromMeta(llm.NewClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	t.Cleanup(restored.Close)

	history := currentHistory(t, restored)
	first, second := indexOfTurnText(history, firstText), indexOfTurnText(history, secondText)
	if first < 0 || second < 0 || first > second {
		t.Fatalf("resumed history holds the items at %d and %d, want both with the first ahead", first, second)
	}
}

// An accepted generation's trigger is resolved as consumed before its first
// request presents it. A fold in that first request must keep the trigger's
// turn, or the model never sees what woke it and no pending wake remains.
func TestAcceptedDelegateAttentionTriggerSurvivesCompactionBeforeItsFirstRequest(t *testing.T) {
	t.Parallel()
	sess := acceptedTriggerSession(t)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if indexOfTurnText(currentHistory(t, sess), acceptedTriggerText) < 0 {
		t.Fatal("the fold dropped the accepted generation's trigger before any request presented it")
	}
}

// Once a settled request has presented the trigger, a later fold may summarize
// it away like any other consumed turn.
func TestAcceptedDelegateAttentionTriggerFoldsAwayOncePresented(t *testing.T) {
	t.Parallel()
	sess := acceptedTriggerSession(t)
	ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_target", generation: 1})
	sess.stagePresentedAttentionCoverage(ctx, currentHistory(t, sess), nil)
	sess.promoteStagedAttentionCoverage(llm.Request{})
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if indexOfTurnText(currentHistory(t, sess), acceptedTriggerText) >= 0 {
		t.Fatal("the fold kept a trigger a settled request already presented")
	}
}

// A responses-continuation delta never carries an attention delivery: every
// one is a steering turn, and the eligibility rules reject any delta whose
// suffix holds a steering turn. A round that settles as a delta therefore
// presented no attention at all — it credits no coverage and clears no latch —
// so a latched trigger's reopen stays owed even when the staged set, the whole
// projection, holds it. Suppressing the reopen on staging alone would lose the
// report: nothing else presented it. Only a settled full-history request may
// clear the latch, because only that form can have carried the report.
func TestSettledResponsesDeltaLeavesTheLatchedReopenOwed(t *testing.T) {
	t.Parallel()
	leaseCtx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_target", generation: 1})
	pendingReopen := func(sess *Session) string {
		pending, err := sess.pendingDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read pending delegate attention: %v", err)
		}
		for _, id := range pending {
			if strings.Contains(id, delegateAttentionReopenMarker) {
				return id
			}
		}
		return ""
	}

	// The strongest witness: the latch sits in the staged set — the staged
	// set is the whole projection — and the round still settled as a delta.
	// The delta provably carried no steering turn, so the model saw no
	// report, and the reopen must fire.
	sess := acceptedTriggerSession(t)
	sess.stagePresentedAttentionCoverage(leaseCtx, currentHistory(t, sess), nil)
	sess.promoteStagedAttentionCoverage(llm.Request{HistoryMode: llm.HistoryModeResponsesDelta})
	if _, err := sess.settleUnpresentedDelegateAttention(0); err != nil {
		t.Fatalf("settleUnpresentedDelegateAttention: %v", err)
	}
	if reopened := pendingReopen(sess); reopened == "" {
		t.Fatal("the reopen skipped a latched trigger a settled delta provably did not carry (no pending row holds a reopened identity)")
	}
}

// A trigger whose generation fails before presenting reopens under a fresh
// identity. Resolving the original dropped the controller's copy of its cause,
// so the latch is the only place that lineage survives; the reopen must carry
// it across. A recovered turn that loses it drops the watch ancestry it
// descended from and restarts the runaway count.
func TestReopenedTriggerKeepsTheCauseLatchCarried(t *testing.T) {
	t.Parallel()
	sess := acceptedTriggerSession(t)
	controller := sess.delegateController
	// The fixture already reserved and accepted this trigger; it is the
	// attention the latch holds.
	trigger := "delegate:dlg_worker/delivery/1"
	// The trigger's cause carries a watch fire, and the accept's resolution
	// drops the controller's copy, leaving the latch as the only witness.
	watch := provenance.WithWatch(nil, "watch_loop", "wg_loop", "wd_reopen", "child-session", "")
	controller.attentionCauses.remember(trigger, watch)
	sess.holdAcceptedTrigger(trigger)
	controller.attentionCauses.forget(trigger)

	reopened, err := sess.settleUnpresentedDelegateAttention(0)
	if err != nil {
		t.Fatalf("settleUnpresentedDelegateAttention: %v", err)
	}
	if reopened == "" || reopened == trigger {
		t.Fatalf("setup: the latch did not move to a reopened identity (got %q)", reopened)
	}

	// The latch is a single slot: any other accepted row replaces both its id
	// and its cause. The reopened row must therefore carry its cause in the
	// controller's registry too, or a competing accept between the reopen and
	// the presenting request strands the recovered lineage.
	other := "delegate:dlg_worker/delivery/2"
	controller.attentionCauses.remember(other, provenance.WithWatch(nil, "watch_other", "wg_other", "wd_other", "child-session", ""))
	sess.holdAcceptedTrigger(other)

	// The recovered turn presents the reopened identity; its lineage must still
	// descend from the watch the failed generation came from.
	sess.attentionMu.Lock()
	sess.attentionStagedIDs = map[string]struct{}{reopened: {}}
	sess.attentionMu.Unlock()
	sess.promoteStagedAttentionCoverage(llm.Request{})
	if depth := provenance.SelfInfluenceDepth(sess.activeCausalProvenance(), "watch_loop", "", func(string) bool { return true }); depth == 0 {
		t.Fatal("the recovered turn's lineage lost the watch its failed generation descended from")
	}
}

// A projection that kept a held row out is a nonstandard view of the resident
// history: no stored exchange matches its prefix, and the state its own
// exchange would store is missing turns the resident history keeps. The
// request must not continue from an anchor, and the exchange it settles must
// record no continuation metadata — a later request that continued from this
// exchange's server state would silently drop the held row; the report's own
// run would anchor past its turn, carry only newer items, and the model would
// answer around a report it was never shown.
func TestHeldAttentionProjectionSeedsNoContinuationAnchor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		MaxSubagentDepth:            1,
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)

	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_target", "")
	lease := delegateLease{delegateID: "dlg_target", generation: 1}
	ownController := sess.delegateController
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_target"
	controller.mu.Lock()
	controller.live[lease.delegateID].runtime = sess
	controller.live[lease.delegateID].binding.runtime = sess
	controller.mu.Unlock()
	// The harness tree names this session as its running delegate only for
	// the projection under test. Give the session its own tree back before
	// its Close, which closes the tree it owns: the harness tree's seeded
	// generation has no run to settle it, and tearing that tree down would
	// tear this session down inside its own close.
	defer func() {
		sess.mu.Lock()
		sess.delegateController = ownController
		sess.owningDelegateID = ""
		sess.mu.Unlock()
	}()

	const heldID = "delegate:dlg_worker/delivery/1/reopened/2"
	held := schema.NewTurn(schema.TurnSteering, llm.User("held report"))
	held.AttentionID = heldID
	anchor := responsesContinuationEligibleAssistantTurn("resp_phase4d_anchor")
	anchor.ResponseIDHash = "cont-handle-v1:response_id:phase4d"
	anchor.ResponseRequestFingerprint = "cont-req-v1:phase4d"
	anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:phase4d"
	sess.mu.Lock()
	sess.history = []schema.Turn{
		held,
		anchor,
		schema.NewTurn(schema.TurnEnvironment, llm.User("environment context")),
	}
	sess.mu.Unlock()
	sess.attentionMu.Lock()
	sess.attentionAutomaticDriveHolds = map[string]uint64{heldID: 1}
	sess.attentionMu.Unlock()

	ctx := context.WithValue(context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease), delegateAttentionRunContextKey{}, struct{}{})
	var timings events.RoundTimings
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepare model request: %v", err)
	}
	if req.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("HistoryMode = %q, want full history: a projection that dropped a held row is a nonstandard view and must not continue from a stored anchor", req.HistoryMode)
	}
	if req.PreviousResponseID != "" {
		t.Fatalf("PreviousResponseID = %q, want empty: the filtered projection must not continue from an anchor", req.PreviousResponseID)
	}
	if req.Continuation != nil {
		t.Fatalf("Continuation metadata = %+v, want none: the exchange this request settles must not become a later anchor", req.Continuation)
	}
	_, meta := singleAttemptRequestMetadata(req)
	if meta.ContextMarker != "" || meta.RequestFingerprint != "" {
		t.Fatalf("attempt metadata = %+v, want no anchor fields: the settled exchange must not record a continuation fingerprint", meta)
	}
}

const acceptedTriggerText = `<delegate-notification delegate_id="dlg_worker" kind="update">TRIGGER_ITEM</delegate-notification>`

// consumeAcceptedDelegateAttentionForLegacyFixture replays the pre-#5681 accept
// path: the generation had already consumed its trigger at the reservation's
// generation, and the latch named it for the request. Production now commits
// first and consumes only when a request presents the row, so no live path
// writes these markers; a fixture that models a journal written before the
// upgrade is the only caller.
func consumeAcceptedDelegateAttentionForLegacyFixture(t *testing.T, controller *delegateTreeController, sess *Session, reservation *delegateStartReservation) {
	t.Helper()
	attentionID, generation, err := controller.attentionReservationIdentity(reservation, sess)
	if err != nil {
		t.Fatalf("legacy accept identity: %v", err)
	}
	sess.holdAcceptedTrigger(attentionID)
	if err := sess.resolveAttentionDurablyForGeneration([]string{attentionID}, delegateAttentionConsumed, generation); err != nil {
		t.Fatalf("legacy accept consume: %v", err)
	}
}

// acceptedTriggerSession is a delegate runtime that has accepted an attention
// generation whose trigger is old enough for a fold to summarize away, with a
// later item still pending.
func acceptedTriggerSession(t *testing.T) *Session {
	t.Helper()
	sess, _ := newCompactableSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, controller, "dlg_target", "")
	controller.mu.Lock()
	controller.live["dlg_target"] = &delegateLiveState{runtime: sess}
	controller.mu.Unlock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_target"

	const triggerID, laterID = "delegate:dlg_worker/delivery/1", "delegate:dlg_worker/delivery/2"
	const laterText = `<delegate-notification delegate_id="dlg_worker" kind="update">LATER_ITEM</delegate-notification>`
	if _, err := sess.appendDelegateNotificationDurably(triggerID, acceptedTriggerText); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	controller.noteDelegateAttention("dlg_target", triggerID)
	seedNumberedSessionHistory(t, sess, 12)
	if _, err := sess.appendDelegateNotificationDurably(laterID, laterText); err != nil {
		t.Fatalf("append later item: %v", err)
	}
	controller.noteDelegateAttention("dlg_target", laterID)

	reservation, err := controller.ReserveAttention(sess, triggerID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := acceptDelegateAttentionForTest(controller, sess, reservation); err != nil {
		t.Fatalf("acceptDelegateAttention: %v", err)
	}
	// LEGACY PRE-UPGRADE GENERATION (pre-#5681): the restored generation had
	// consumed its trigger before it ran.
	consumeAcceptedDelegateAttentionForLegacyFixture(t, controller, sess, reservation)
	// No drive owns this accepted reservation, so nothing commits it; release
	// it the way a drive that cannot commit does, before the session and the
	// controller close (cleanups are LIFO), so Close finds no start to wait on.
	t.Cleanup(func() {
		if err := controller.AbortStart(reservation); err != nil {
			t.Errorf("AbortStart: %v", err)
		}
	})
	return sess
}

// deriveHeldFixture builds the durable shape a user-stopped presentation
// leaves for a cold restore: a delegate whose first generation settled with
// the given outcome, its accepted source row consumed at that generation, and
// a reopened row keyed to the same generation still pending. The session's
// controller is the harness's; the derivation reads only durable state.
func deriveHeldFixture(t *testing.T, outcome delegatestore.OutcomeStatus, reason string) (*Session, string) {
	t.Helper()
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, outcome, reason)
	return sess, reopenedID
}

// A restored session's held rows come from durable state, and the predicate
// draws exactly the user stop's boundary: a pending reopen of the generation
// the delegate rests at is held when that generation settled cancelled and
// no new event has armed the row's wake; a controller-noted wake is a new
// event's arm and presents, and reopens keyed to a generation the journal
// never reached — the busy and failed-launch shapes — stay presentable on
// the restore.
func TestDeriveHeldDelegateAttentionFromOutcome(t *testing.T) {
	t.Parallel()

	t.Run("cancelled generation holds its reopened row", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID := deriveHeldFixture(t, delegatestore.OutcomeCancelled, "cancelled")
		sess.deriveHeldDelegateAttentionFromOutcome()
		sess.attentionMu.Lock()
		held := sess.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
		sess.attentionMu.Unlock()
		if !held {
			t.Fatal("the cancelled generation's reopened row was not held")
		}
		ids, err := sess.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if slices.Contains(ids, reopenedID) {
			t.Fatalf("the drive-side read still offers the held row: %v", ids)
		}
	})

	t.Run("controller-noted wake presents instead", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID := deriveHeldFixture(t, delegatestore.OutcomeCancelled, "cancelled")
		sess.delegateController.noteDelegateAttention("dlg_hold", reopenedID)
		sess.deriveHeldDelegateAttentionFromOutcome()
		ids, err := sess.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if !slices.Contains(ids, reopenedID) {
			t.Fatalf("a controller-noted wake is a new event's arm and must present: %v", ids)
		}
	})

	t.Run("busy-keyed reopen stays presentable", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID := deriveHeldFixture(t, delegatestore.OutcomeCancelled, "cancelled")
		// A busy-class refusal claims the recycled generation without ever
		// journaling it, so its reopen keys past the aggregate's rest.
		const busySourceID = "delegate:dlg_worker/delivery/2"
		transcriptPath := sess.TranscriptPath()
		appendColdAttentionRow(t, transcriptPath, sess.id, busySourceID, acceptedTriggerText)
		appendColdConsumedClaim(t, transcriptPath, sess.id, busySourceID, 2)
		busyReopenedID := delegateAttentionReopenID(busySourceID, 2)
		appendColdAttentionRow(t, transcriptPath, sess.id, busyReopenedID, acceptedTriggerText)
		sess.deriveHeldDelegateAttentionFromOutcome()
		ids, err := sess.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if !slices.Contains(ids, busyReopenedID) {
			t.Fatalf("a busy-keyed reopen must stay presentable on the restore: %v", ids)
		}
		if slices.Contains(ids, reopenedID) {
			t.Fatalf("the cancelled generation's reopened row lost its hold beside the busy key: %v", ids)
		}
	})

	t.Run("failed launch stays presentable", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID := deriveHeldFixture(t, delegatestore.OutcomeFailed, "launch_failed")
		sess.deriveHeldDelegateAttentionFromOutcome()
		ids, err := sess.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if !slices.Contains(ids, reopenedID) {
			t.Fatalf("a failed launch's reopened row must stay presentable on the restore: %v", ids)
		}
	})

	t.Run("stopped send at the recycled generation owes no attention quiet", func(t *testing.T) {
		t.Parallel()
		// The reviewed shape: a busy refusal reopened the source at the
		// recycled generation, and a SEND — not an attention run — committed
		// and settled that generation before the paced retry presented the
		// reopen. The send's user stop settles cancelled at the same
		// generation the reopen claims, so the generation and outcome match
		// the attention quiet exactly; only the run's own trigger
		// distinguishes the send, which is why the fixture journals the run
		// rather than seeding the aggregate. The resident gate holds attention
		// runs' reports alone, so the restored session must not hold the
		// reopen either.
		sess, reopenedID := deriveSendSettledFixture(t)
		sess.deriveHeldDelegateAttentionFromOutcome()
		sess.attentionMu.Lock()
		held := sess.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
		sess.attentionMu.Unlock()
		if held {
			t.Fatal("a stopped send at the recycled generation held the busy reopen: the resident quiet covers attention runs alone")
		}
		ids, err := sess.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if !slices.Contains(ids, reopenedID) {
			t.Fatalf("the stopped send's busy reopen must stay presentable on the restore: %v", ids)
		}
	})

	// The corner this derivation got wrong: the quiet a stop leaves is
	// in-memory, and a newer event that ended it leaves no trace in the wake
	// set once the idle release reclaims the runtime. Without a durable order,
	// the restored session reads the stopped generation's cancelled outcome and
	// rebuilds a quiet the newer event already released. The derivation must
	// take its quiet only from evidence no later event has superseded.
	t.Run("a released quiet is not re-imposed on the restore", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
		// The stop's own quiet, written through the production setter the settle
		// tail writes with, stamped with the generation the stop settled.
		heldGeneration := settledGeneration(t, sess)
		sess.holdDelegateAttentionFromAutomaticDriveAt(reopenedID, heldGeneration)
		// The newer event: a fresh row is appended and admitted to the
		// controller. openDelegateAttention is the primitive the resident arm and
		// the cold drive both reach, and it journals the release for the
		// generation the wake supersedes in the same batch that publishes the
		// wake. Driving it here rather than through the Session's arm keeps the
		// fixture off a real turn while still exercising the production path.
		const freshID = "delegate:dlg_worker/delivery/2"
		if _, err := sess.appendDelegateNotificationDurably(freshID, acceptedTriggerText); err != nil {
			t.Fatalf("append the fresh row: %v", err)
		}
		if _, _, err := openDelegateAttentionForTest(sess.delegateController, "dlg_hold", freshID); err != nil {
			t.Fatalf("admit the fresh wake: %v", err)
		}
		admittedGeneration := settledGeneration(t, sess)
		sess.releaseAutomaticDriveHolds(delegateHoldReleaseArm, admittedGeneration)
		sess.attentionMu.Lock()
		heldBefore := sess.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
		sess.attentionMu.Unlock()
		if heldBefore {
			t.Fatal("the release left the row held on the resident runtime")
		}
		// The cold restore: a fresh session over the same durable evidence,
		// rebuilt the way the idle release's restore rebuilds it. The wake set
		// the resident runtime emptied does not survive the reclaim, so only
		// the durable release record can tell the derivation the quiet ended.
		restored := coldRestoredDeriveSession(t, sess, journalPath)
		restored.deriveHeldDelegateAttentionFromOutcome()
		ids, err := restored.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if !slices.Contains(ids, reopenedID) {
			t.Fatalf("a quiet a newer event already released was re-imposed on the restore: %v", ids)
		}
	})

	// The direction the derivation exists for: with no newer event, the
	// stopped generation's quiet must still stand on a cold restore.
	t.Run("an unreleased quiet still stands on the restore", func(t *testing.T) {
		t.Parallel()
		sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
		restored := coldRestoredDeriveSession(t, sess, journalPath)
		restored.deriveHeldDelegateAttentionFromOutcome()
		restored.attentionMu.Lock()
		held := restored.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
		restored.attentionMu.Unlock()
		if !held {
			t.Fatal("the stop's own quiet did not survive the cold restore")
		}
		ids, err := restored.automaticDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read the drive-side rows: %v", err)
		}
		if slices.Contains(ids, reopenedID) {
			t.Fatalf("the drive-side read offered a row the stop still quiets: %v", ids)
		}
	})
}

// A released report whose own turn a compaction marker cut from the resumed
// history must be back in history before the automatic drive can select it. The
// marker leaves the fold listing the row pending while resume keeps only the
// marker and what follows, so the row is presentable with no turn behind it:
// the drive claims it, the request carries no report, and the row is durably
// consumed with the model never having read it.
func TestReleasedReportSurvivesACompactionMarkerBeforeSelection(t *testing.T) {
	t.Parallel()
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	// The newer event: a fresh row is admitted, which journals the release that
	// ends the stopped generation's quiet.
	const (
		freshID   = "delegate:dlg_worker/delivery/2"
		freshText = `<delegate-notification delegate_id="dlg_worker" kind="update">FRESH_AFTER_RELEASE</delegate-notification>`
	)
	if _, err := sess.appendDelegateNotificationDurably(freshID, freshText); err != nil {
		t.Fatalf("append the fresh row: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(sess.delegateController, "dlg_hold", freshID); err != nil {
		t.Fatalf("admit the fresh wake: %v", err)
	}
	sess.releaseAutomaticDriveHolds(delegateHoldReleaseArm, settledGeneration(t, sess))
	// The compaction: the summarizer folds everything the transcript holds,
	// including the released report's own turn, and every later request resumes
	// from the marker it writes. The fold still lists the row pending.
	appendCompactionMarkerAfterReopenedRow(t, sess.TranscriptPath(), sess.id)
	// The resumed history a cold restore builds for this transcript: everything
	// from the last compaction marker on, so the released report's turn is gone
	// while the fold still lists its row pending.
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), sess.stateDir)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	resumed, _ := resumeHistoryIndexed(data.Entries)
	if indexOfTurnText(resumed, acceptedTriggerText) >= 0 {
		t.Fatal("premise broken: the resumed history still holds the released report's turn")
	}
	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.mu.Lock()
	restored.history = slices.Clone(resumed)
	restored.mu.Unlock()
	restored.deriveHeldDelegateAttentionFromOutcome()
	// The report must be presentable...
	ids, err := restored.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read the drive-side rows: %v", err)
	}
	if !slices.Contains(ids, reopenedID) {
		t.Fatalf("the released report is not offered to the drive: %v", ids)
	}
	// ...and the request that presents it must carry the report, which a turn
	// the resumed history dropped cannot.
	history := currentHistory(t, restored)
	if at := indexOfAttentionTurn(history, reopenedID); at < 0 {
		t.Fatal("the released report was presentable with no turn in the resumed history: the drive would consume a row the model never saw")
	} else if at != len(history)-1 {
		t.Fatalf("the released report resumed at %d of %d, want the tail so the presenting request ends on it", at, len(history)-1)
	}
}

// indexOfAttentionTurn returns the position of attentionID's steering turn in
// history, or -1 when history holds none. Rows can carry identical content, so
// a position assertion asks for the row's identity rather than its text.
func indexOfAttentionTurn(history []schema.Turn, attentionID string) int {
	return slices.IndexFunc(history, func(turn schema.Turn) bool {
		return turn.AttentionID == attentionID && turn.Kind == schema.TurnSteering
	})
}

// A compaction marker that follows both a reopened report and the fresh
// delivery that released it cuts both turns from the resumed history. Restoring
// the released rows alone leaves the fresh row selectable with no turn behind
// it, so the drive claims it and a successful run consumes it with the model
// never having read it. Every selectable pending row must be back in history
// before a drive can select it, and the report the release freed must still be
// the request's last message.
func TestReleasedReportAndTheFreshDeliveryBothSurviveACompactionMarker(t *testing.T) {
	t.Parallel()
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	const (
		freshID    = "delegate:dlg_worker/delivery/2"
		freshText  = `<delegate-notification delegate_id="dlg_worker" kind="update">FRESH_DELIVERY</delegate-notification>`
		reportText = acceptedTriggerText
	)
	if _, err := sess.appendDelegateNotificationDurably(freshID, freshText); err != nil {
		t.Fatalf("append the fresh row: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(sess.delegateController, "dlg_hold", freshID); err != nil {
		t.Fatalf("admit the fresh wake: %v", err)
	}
	sess.releaseAutomaticDriveHolds(delegateHoldReleaseArm, settledGeneration(t, sess))
	// The compaction lands after both rows, so the resumed history holds
	// neither turn while the fold still lists both rows pending.
	appendCompactionMarkerAfterReopenedRow(t, sess.TranscriptPath(), sess.id)
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), sess.stateDir)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	resumed, _ := resumeHistoryIndexed(data.Entries)
	for _, text := range []string{reportText, freshText} {
		if indexOfTurnText(resumed, text) >= 0 {
			t.Fatalf("premise broken: the resumed history still holds the turn for %q", text)
		}
	}
	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.mu.Lock()
	restored.history = slices.Clone(resumed)
	restored.mu.Unlock()
	restored.deriveHeldDelegateAttentionFromOutcome()

	ids, err := restored.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read the drive-side rows: %v", err)
	}
	for _, want := range []string{reopenedID, freshID} {
		if !slices.Contains(ids, want) {
			t.Fatalf("row %q is not offered to the drive: %v", want, ids)
		}
	}
	history := currentHistory(t, restored)
	reportAt, freshAt := indexOfTurnText(history, reportText), indexOfTurnText(history, freshText)
	if reportAt < 0 || freshAt < 0 {
		t.Fatalf("a selectable pending row had no turn in the resumed history (report at %d, fresh at %d): the drive would consume a row the model never saw", reportAt, freshAt)
	}
	if reportAt < freshAt {
		t.Fatalf("the released report resumed at %d, ahead of the fresh delivery at %d: the presenting request must end on the report the release freed", reportAt, freshAt)
	}
	if reportAt != len(history)-1 {
		t.Fatalf("the released report resumed at %d of %d, want the tail so the presenting request ends on it", reportAt, len(history)-1)
	}
}

// A report a cold restore still holds behind a compaction marker has no turn in
// the resumed history. When a later wake clears that hold the row becomes
// presentable, so the release must put its turn back — and at the tail, behind
// the rows that arrived while the hold stood — or the drive claims the row and
// attaches a request with no report in it.
func TestReleasedHoldRestoresACompactedReportAtTheTail(t *testing.T) {
	t.Parallel()
	sess, reopenedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	const (
		laterID   = "delegate:dlg_worker/delivery/2"
		laterText = `<delegate-notification delegate_id="dlg_worker" kind="update">LATER_WHILE_HELD</delegate-notification>`
	)
	if _, err := sess.appendDelegateNotificationDurably(laterID, laterText); err != nil {
		t.Fatalf("append the later row: %v", err)
	}
	// The marker follows both rows, so the resume holds neither turn.
	appendCompactionMarkerAfterReopenedRow(t, sess.TranscriptPath(), sess.id)
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), sess.stateDir)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	resumed, _ := resumeHistoryIndexed(data.Entries)
	for _, text := range []string{acceptedTriggerText, laterText} {
		if indexOfTurnText(resumed, text) >= 0 {
			t.Fatalf("premise broken: the resumed history still holds the turn for %q", text)
		}
	}
	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.mu.Lock()
	restored.history = slices.Clone(resumed)
	restored.mu.Unlock()
	// The restore rebuilds the stop's quiet from the durable outcome, so the
	// report stays held and its turn stays out of history.
	restored.deriveHeldDelegateAttentionFromOutcome()
	restored.attentionMu.Lock()
	heldAtRestore := restored.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
	restored.attentionMu.Unlock()
	if !heldAtRestore {
		t.Fatal("premise broken: the restored session did not rebuild the stop's quiet")
	}
	if indexOfAttentionTurn(currentHistory(t, restored), reopenedID) >= 0 {
		t.Fatal("premise broken: the held row's turn was in the resumed history")
	}
	// A later wake supersedes the quiet; its release callback has not moved
	// anything yet.
	restored.releaseAutomaticDriveHolds(delegateHoldReleaseArm, settledGeneration(t, restored))
	restored.attentionMu.Lock()
	stillHeld := restored.delegateAttentionHeldFromAutomaticDriveLocked(reopenedID)
	restored.attentionMu.Unlock()
	if stillHeld {
		t.Fatal("the release left the row held")
	}
	history := currentHistory(t, restored)
	reportAt := indexOfAttentionTurn(history, reopenedID)
	if reportAt < 0 {
		t.Fatal("the released row became presentable with no turn in history: the drive would consume a row the model never saw")
	}
	if reportAt != len(history)-1 {
		t.Fatalf("the released report resumed at %d of %d, want the tail behind the rows that arrived while it was held", reportAt, len(history)-1)
	}
	laterAt := indexOfAttentionTurn(history, laterID)
	if laterAt < 0 {
		t.Fatalf("the other pending row's turn is missing from history: %v", history)
	}
	if laterAt > reportAt {
		t.Fatalf("the other pending row resumed at %d, behind the released report at %d: the release must put the freed report last", laterAt, reportAt)
	}
}

// Restoring a pending row a compaction marker cut from the resumed history must
// keep it ahead of the rows that arrived after it, including a later row whose
// turn the resumed history still holds. Such a row is the anchor the restore
// inserts the older turn before; a restore that collects only the missing turns
// drops it, appends the older row at the tail, and the first request presents
// the rows in reverse arrival order, so stale information reads as current. The
// report a hold release frees still ends the request, at the tail.
func TestRestoredOlderPendingTurnPrecedesALaterResidentTurn(t *testing.T) {
	t.Parallel()
	const (
		olderID   = "delegate:dlg_worker/delivery/3"
		olderText = `<delegate-notification delegate_id="dlg_worker" kind="update">OLDER_BEFORE_MARKER</delegate-notification>`
		newerID   = "delegate:dlg_worker/delivery/4"
		newerText = `<delegate-notification delegate_id="dlg_worker" kind="update">NEWER_AFTER_MARKER</delegate-notification>`
	)
	sess, releasedID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	// The plain older row arrives before the marker, behind the report the stop
	// quieted.
	if _, err := sess.appendDelegateNotificationDurably(olderID, olderText); err != nil {
		t.Fatalf("append the older row: %v", err)
	}
	// A newer wake journals the release that ends the stop's quiet, so the
	// restored session presents the freed report instead of holding it.
	journalDeriveHeldRelease(t, sess, 1)
	// The marker lands after both older rows, cutting their turns from the
	// resumed history.
	appendCompactionMarkerAfterReopenedRow(t, sess.TranscriptPath(), sess.id)
	// The newer row arrives after the marker, so the resumed history already
	// holds its turn: it is the later arrival the restore must anchor the older
	// row on.
	if _, err := sess.appendDelegateNotificationDurably(newerID, newerText); err != nil {
		t.Fatalf("append the newer row: %v", err)
	}
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), sess.stateDir)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	resumed, _ := resumeHistoryIndexed(data.Entries)
	for _, attentionID := range []string{releasedID, olderID} {
		if indexOfAttentionTurn(resumed, attentionID) >= 0 {
			t.Fatalf("premise broken: the resumed history still holds the turn for %q", attentionID)
		}
	}
	if indexOfAttentionTurn(resumed, newerID) < 0 {
		t.Fatal("premise broken: the resumed history does not hold the newer row's turn")
	}
	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.mu.Lock()
	restored.history = slices.Clone(resumed)
	restored.mu.Unlock()
	restored.deriveHeldDelegateAttentionFromOutcome()
	// The drive-side read every automatic presentation passes through restores
	// what the resumed history is missing before the request is captured.
	ids, err := restored.automaticDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read the drive-side rows: %v", err)
	}
	for _, want := range []string{olderID, newerID, releasedID} {
		if !slices.Contains(ids, want) {
			t.Fatalf("row %q is not offered to the drive: %v", want, ids)
		}
	}
	// The first request's history is the automatic attention projection.
	projection, droppedAt := restored.delegateAutomaticAttentionHistorySnapshotWithCoverage()
	if len(droppedAt) != 0 {
		t.Fatalf("the projection dropped turns at %v, want none: no row is held after the release", droppedAt)
	}
	olderAt := indexOfAttentionTurn(projection.turns, olderID)
	newerAt := indexOfAttentionTurn(projection.turns, newerID)
	releasedAt := indexOfAttentionTurn(projection.turns, releasedID)
	if olderAt < 0 || newerAt < 0 || releasedAt < 0 {
		t.Fatalf("a selectable pending row had no turn in the first request (older %d, newer %d, released %d)", olderAt, newerAt, releasedAt)
	}
	if olderAt > newerAt {
		t.Fatalf("the first request presents the older row at %d, behind the newer resident row at %d: reverse arrival order makes stale information read as current", olderAt, newerAt)
	}
	if releasedAt != len(projection.turns)-1 {
		t.Fatalf("the freed report resumed at %d of %d, want the tail so the presenting request ends on it", releasedAt, len(projection.turns)-1)
	}
}

// Restoring an older pending turn ahead of a later resident one inserts into
// the middle of the history, which pushes every in-flight turn right by one.
// The boundary that says where the in-flight turn starts must move with them,
// or turns that belong to the settled past read as in flight and keep their
// thinking on a request that must not replay it. The deletion paths carry the
// same correction in the other direction.
func TestRestoredOlderPendingTurnMovesTheInFlightBoundary(t *testing.T) {
	t.Parallel()
	const (
		olderID   = "delegate:dlg_worker/delivery/1"
		olderText = `<delegate-notification delegate_id="dlg_worker" kind="update">OLDER_RESTORED</delegate-notification>`
		newerID   = "delegate:dlg_worker/delivery/2"
		newerText = `<delegate-notification delegate_id="dlg_worker" kind="update">NEWER_RESIDENT</delegate-notification>`
	)
	sess, _ := newCompactableSession(t)
	if _, err := sess.appendDelegateNotificationDurably(newerID, newerText); err != nil {
		t.Fatalf("append the newer row: %v", err)
	}
	// The older row is durable in the fold but absent from the resident
	// history, the shape a compaction marker leaves behind.
	if _, err := sess.appendDelegateNotificationDurably(olderID, olderText); err != nil {
		t.Fatalf("append the older row: %v", err)
	}
	resident := currentHistory(t, sess)
	olderTurn := resident[indexOfAttentionTurn(resident, olderID)]
	sess.mu.Lock()
	sess.history = slices.DeleteFunc(slices.Clone(resident), func(turn schema.Turn) bool {
		return turn.AttentionID == olderID
	})
	inFlight := len(sess.history)
	sess.turnHistoryBaseline = inFlight
	sess.mu.Unlock()

	if err := sess.retainDelegateAttentionTurn(olderTurn, resident[indexOfAttentionTurn(resident, newerID)]); err != nil {
		t.Fatalf("retain the older turn: %v", err)
	}
	sess.mu.Lock()
	got := sess.turnHistoryBaseline
	sess.mu.Unlock()
	if got != inFlight+1 {
		t.Fatalf("turnHistoryBaseline = %d, want %d: the restored turn landed ahead of the in-flight turn and the boundary must move with it", got, inFlight+1)
	}
}

// Once a settled round of the active turn has presented a delegate attention
// turn, a mid-turn compaction must fold it away rather than re-injecting it
// after the summary, while still preserving any later attention item that
// arrived before compaction without being presented yet, and consuming both
// durably when the turn finishes.
func TestRootDelegateAttentionCoveredMidTurnFoldsAwayWhileUncoveredSurvives(t *testing.T) {
	t.Parallel()
	const (
		coveredID     = "delegate:dlg_worker/delivery/1"
		coveredText   = `<delegate-notification delegate_id="dlg_worker" kind="report">EARLY_COVERED_ITEM</delegate-notification>`
		uncoveredID   = "delegate:dlg_worker/delivery/2"
		uncoveredText = `<delegate-notification delegate_id="dlg_worker" kind="report">LATE_UNCOVERED_ITEM</delegate-notification>`
	)
	sess, main := newCompactableSession(t)
	muteNoteElicitation(sess)
	if _, err := sess.appendDelegateNotificationDurably(coveredID, coveredText); err != nil {
		t.Fatalf("append covered attention: %v", err)
	}
	if err := sess.armDelegateAttention(coveredID); err != nil {
		t.Fatalf("arm covered attention: %v", err)
	}

	var round1SawCovered, round2SawSummary, round2SawCovered, round2SawUncovered bool
	main.setSteps([]func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			round1SawCovered = requestMessagesContainText(req.Messages, "EARLY_COVERED_ITEM")
			if _, err := sess.appendDelegateNotificationDurably(uncoveredID, uncoveredText); err != nil {
				t.Errorf("append uncovered attention: %v", err)
			}
			if err := sess.armDelegateAttention(uncoveredID); err != nil {
				t.Errorf("arm uncovered attention: %v", err)
			}
			seedNumberedSessionHistory(t, sess, 12)
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "compact-mid-turn",
				Name:      "compact_context",
				Arguments: []byte(`{"note_to_self":"keep going"}`),
				Type:      "function",
			})
		},
		func(req llm.Request) llm.Response {
			round2SawSummary = requestMessagesContainText(req.Messages, "[CONTEXT SUMMARY]")
			round2SawCovered = requestMessagesContainText(req.Messages, "EARLY_COVERED_ITEM")
			round2SawUncovered = requestMessagesContainText(req.Messages, "LATE_UNCOVERED_ITEM")
			return agenttest.ToolCallResponse(communicateCall("finish-turn", "all caught up"))
		},
	})

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if !round1SawCovered {
		t.Fatal("round 1 did not present EARLY_COVERED_ITEM")
	}
	if !round2SawSummary {
		t.Fatal("round 2 did not carry the mid-turn compaction summary")
	}
	if round2SawCovered {
		t.Fatal("mid-turn compaction re-injected EARLY_COVERED_ITEM after round 1 already presented it")
	}
	if !round2SawUncovered {
		t.Fatal("mid-turn compaction dropped LATE_UNCOVERED_ITEM before any settled round presented it")
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after turn completion = %v (err %v), want both items consumed", pending, err)
	}
}

// If a mid-turn compaction folds away a covered attention turn and a later
// round of that same turn fails before durable consumption, finishAttentionTurn
// must restore the still-pending turn back into resident history so the
// follow-up retry turn still presents and consumes it.
func TestRootDelegateAttentionCoveredAndFoldedMidTurnRestoresWhenTurnFails(t *testing.T) {
	t.Parallel()
	const (
		coveredID   = "delegate:dlg_worker/delivery/1"
		coveredText = `<delegate-notification delegate_id="dlg_worker" kind="report">EARLY_COVERED_ON_FAIL</delegate-notification>`
	)
	var sess *Session
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	step := 0
	var round2SawCovered, retryTurnSawCovered bool
	main := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			step++
			switch step {
			case 1:
				seedNumberedSessionHistory(t, sess, 12)
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "compact-before-fail",
					Name:      "compact_context",
					Arguments: []byte(`{"note_to_self":"about to fail"}`),
					Type:      "function",
				}), nil
			case 2:
				round2SawCovered = requestMessagesContainText(req.Messages, "EARLY_COVERED_ON_FAIL")
				cancelTurn()
				return llm.Response{}, context.Canceled
			default:
				retryTurnSawCovered = requestMessagesContainText(req.Messages, "EARLY_COVERED_ON_FAIL")
				return agenttest.ToolCallResponse(communicateCall("retry-done", "recovered")), nil
			}
		},
	}
	sess, _ = newCompactableSession(t)
	sess.client.Register(main)
	muteNoteElicitation(sess)

	if _, err := sess.appendDelegateNotificationDurably(coveredID, coveredText); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := sess.armDelegateAttention(coveredID); err != nil {
		t.Fatalf("arm attention: %v", err)
	}

	if _, err := sess.ProcessInputKind(turnCtx, "", nil, EntryNotification); !errors.Is(err, context.Canceled) {
		t.Fatalf("ProcessInputKind on cancelled round 2 = %v, want context.Canceled", err)
	}
	if round2SawCovered {
		t.Fatal("round 2 saw EARLY_COVERED_ON_FAIL re-injected after mid-turn compaction")
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || !slices.Equal(pending, []string{coveredID}) {
		t.Fatalf("pending after failed turn = %v (err %v), want [%s]", pending, err, coveredID)
	}
	if indexOfTurnText(currentHistory(t, sess), coveredText) < 0 {
		t.Fatal("failed turn did not restore the folded covered attention turn back into resident history")
	}

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("recovery notification turn: %v", err)
	}
	if !retryTurnSawCovered {
		t.Fatal("recovery notification turn did not see restored EARLY_COVERED_ON_FAIL")
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovery turn = %v (err %v), want consumed", pending, err)
	}
}

// Stale attentionCoveredIDs from a failed turn must not suppress a still-pending
// attention item during an idle Compact() before the recovery turn runs.
func TestRootDelegateAttentionFailedTurnDoesNotSuppressIdleCompact(t *testing.T) {
	t.Parallel()
	const (
		coveredID   = "delegate:dlg_worker/delivery/1"
		coveredText = `<delegate-notification delegate_id="dlg_worker" kind="report">IDLE_COMPACT_AFTER_FAIL</delegate-notification>`
	)
	var sess *Session
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	step := 0
	var recoverySawCovered bool
	main := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			step++
			switch step {
			case 1:
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "round-one-tool",
					Name:      "task_list",
					Arguments: []byte(`{}`),
					Type:      "function",
				}), nil
			case 2:
				cancelTurn()
				return llm.Response{}, context.Canceled
			default:
				recoverySawCovered = requestMessagesContainText(req.Messages, "IDLE_COMPACT_AFTER_FAIL")
				return agenttest.ToolCallResponse(communicateCall("recovered", "done")), nil
			}
		},
	}
	sess, _ = newCompactableSession(t)
	sess.client.Register(main)
	muteNoteElicitation(sess)

	if _, err := sess.appendDelegateNotificationDurably(coveredID, coveredText); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := sess.armDelegateAttention(coveredID); err != nil {
		t.Fatalf("arm attention: %v", err)
	}
	if _, err := sess.ProcessInputKind(turnCtx, "", nil, EntryNotification); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled turn err = %v, want context.Canceled", err)
	}

	compactAfterAttention(t, sess)

	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("recovery turn after idle Compact: %v", err)
	}
	if !recoverySawCovered {
		t.Fatal("idle Compact after a failed turn dropped IDLE_COMPACT_AFTER_FAIL before the recovery turn saw it")
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovery = %v (err %v), want consumed", pending, err)
	}
}

// When unionCoveredAttention degrades on a transient fold-read failure at turn
// exit, any covered attention turn folded out of resident history mid-turn was
// not durably resolved and must be restored for the wake retry.
func TestRootDelegateAttentionMidTurnFoldRestoresWhenUnionFoldReadFails(t *testing.T) {
	t.Parallel()
	const (
		coveredID   = "delegate:dlg_worker/delivery/1"
		coveredText = `<delegate-notification delegate_id="dlg_worker" kind="report">MID_TURN_UNION_READ_FAIL</delegate-notification>`
	)
	var failUnionRead atomic.Bool
	var sess *Session
	var wakeSawCovered bool
	sess, main := newCompactableSession(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if failUnionRead.CompareAndSwap(true, false) {
				return delegateAttentionFold{}, errors.New("transient union fold read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	muteNoteElicitation(sess)
	if _, err := sess.appendDelegateNotificationDurably(coveredID, coveredText); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := sess.armDelegateAttention(coveredID); err != nil {
		t.Fatalf("arm attention: %v", err)
	}

	main.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			seedNumberedSessionHistory(t, sess, 12)
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "compact-user-turn",
				Name:      "compact_context",
				Arguments: []byte(`{"note_to_self":"fold mid-turn"}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response {
			failUnionRead.Store(true)
			return agenttest.ToolCallResponse(communicateCall("user-turn-done", "finished user turn"))
		},
		func(req llm.Request) llm.Response {
			wakeSawCovered = requestMessagesContainText(req.Messages, "MID_TURN_UNION_READ_FAIL")
			return agenttest.ToolCallResponse(communicateCall("wake-done", "consumed on wake"))
		},
	})

	if _, err := sess.ProcessInput(context.Background(), "run user turn", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !wakeSawCovered {
		t.Fatal("wake turn after degraded unionCoveredAttention did not present MID_TURN_UNION_READ_FAIL")
	}
	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after wake = %v (err %v), want consumed", pending, err)
	}
}

// Restoring an older uncovered attention turn during mid-turn compaction (and
// restoring an older covered turn on fallback when a newer uncovered turn sits
// in history) must preserve arrival order ahead of newer resident attention
// turns.
func TestRootDelegateAttentionMidTurnFoldPreservesArrivalOrderAcrossAnchors(t *testing.T) {
	t.Parallel()
	const (
		firstID    = "delegate:dlg_worker/delivery/1"
		firstText  = `<delegate-notification delegate_id="dlg_worker" kind="update">ORDER_FIRST_HELD</delegate-notification>`
		secondID   = "delegate:dlg_worker/delivery/2"
		secondText = `<delegate-notification delegate_id="dlg_worker" kind="update">ORDER_SECOND_RECENT</delegate-notification>`
	)
	var failRestoreRead atomic.Bool
	var sess *Session
	sess, _ = newCompactableSession(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if failRestoreRead.Load() {
				return delegateAttentionFold{}, errors.New("transient restore fold read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	muteNoteElicitation(sess)

	// Path A: firstID is uncovered (e.g., held from presentation) and folded
	// away, while secondID is covered in the recent tail and survives in folded.
	if _, err := sess.appendDelegateNotificationDurably(firstID, firstText); err != nil {
		t.Fatalf("append first: %v", err)
	}
	seedNumberedSessionHistory(t, sess, 12)
	if _, err := sess.appendDelegateNotificationDurably(secondID, secondText); err != nil {
		t.Fatalf("append second: %v", err)
	}
	sess.beginAttentionCoverageRounds()
	sess.attentionMu.Lock()
	sess.attentionCoveredIDs = map[string]struct{}{secondID: {}}
	sess.attentionMu.Unlock()

	if err := sess.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	sess.applyPendingForceCompact(context.Background())
	sess.endAttentionCoverageRounds()

	post := currentHistory(t, sess)
	firstIdx, secondIdx := indexOfTurnText(post, firstText), indexOfTurnText(post, secondText)
	if firstIdx < 0 || secondIdx < 0 || firstIdx > secondIdx {
		t.Fatalf("Path A history indices = (first=%d, second=%d), want both present with first < second", firstIdx, secondIdx)
	}

	// Path B: firstID is covered and folded away while secondID is uncovered in
	// resident history, and pendingAttentionTurnsLocked fails during failure
	// unwind.
	sess2, _ := newCompactableSession(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if failRestoreRead.Load() {
				return delegateAttentionFold{}, errors.New("transient restore fold read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	muteNoteElicitation(sess2)
	if _, err := sess2.appendDelegateNotificationDurably(firstID, firstText); err != nil {
		t.Fatalf("append first on sess2: %v", err)
	}
	seedNumberedSessionHistory(t, sess2, 12)
	if _, err := sess2.appendDelegateNotificationDurably(secondID, secondText); err != nil {
		t.Fatalf("append second on sess2: %v", err)
	}
	sess2.beginAttentionCoverageRounds()
	sess2.attentionMu.Lock()
	sess2.attentionCoveredIDs = map[string]struct{}{firstID: {}}
	sess2.attentionMu.Unlock()
	if err := sess2.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	sess2.applyPendingForceCompact(context.Background())
	failRestoreRead.Store(true)
	_ = sess2.finishRootDelegateAttentionTurn([]string{firstID}, errors.New("turn failed"))
	sess2.endAttentionCoverageRounds()
	failRestoreRead.Store(false)

	post2 := currentHistory(t, sess2)
	firstIdx2, secondIdx2 := indexOfTurnText(post2, firstText), indexOfTurnText(post2, secondText)
	if firstIdx2 < 0 || secondIdx2 < 0 || firstIdx2 > secondIdx2 {
		t.Fatalf("Path B fallback history indices = (first=%d, second=%d), want both present with first < second", firstIdx2, secondIdx2)
	}

	// Path C: firstID is uncovered (kept in history) while secondID is covered
	// and folded away, and pendingAttentionTurnsLocked fails during unwind.
	sess3, _ := newCompactableSession(t, func(cfg *SessionConfig) {
		cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
			if failRestoreRead.Load() {
				return delegateAttentionFold{}, errors.New("transient restore fold read failure")
			}
			return readDelegateAttentionFold(path, sessionID)
		}
	})
	muteNoteElicitation(sess3)
	if _, err := sess3.appendDelegateNotificationDurably(firstID, firstText); err != nil {
		t.Fatalf("append first on sess3: %v", err)
	}
	if _, err := sess3.appendDelegateNotificationDurably(secondID, secondText); err != nil {
		t.Fatalf("append second on sess3: %v", err)
	}
	seedNumberedSessionHistory(t, sess3, 12)
	sess3.beginAttentionCoverageRounds()
	sess3.attentionMu.Lock()
	sess3.attentionCoveredIDs = map[string]struct{}{secondID: {}}
	sess3.attentionMu.Unlock()
	if err := sess3.requestForceCompact(""); err != nil {
		t.Fatal(err)
	}
	sess3.applyPendingForceCompact(context.Background())
	failRestoreRead.Store(true)
	_ = sess3.finishRootDelegateAttentionTurn([]string{secondID}, errors.New("turn failed"))
	sess3.endAttentionCoverageRounds()
	failRestoreRead.Store(false)

	post3 := currentHistory(t, sess3)
	firstIdx3, secondIdx3 := indexOfTurnText(post3, firstText), indexOfTurnText(post3, secondText)
	if firstIdx3 < 0 || secondIdx3 < 0 || firstIdx3 > secondIdx3 {
		t.Fatalf("Path C fallback history indices = (first=%d, second=%d), want both present with first < second", firstIdx3, secondIdx3)
	}
}

// The release floor: a quiet stands only while the durable release floor is
// below the row's own settled generation.

// deriveHeldFixtureWithJournal is deriveHeldFixture plus the fixture's journal
// path, for a case that restores a controller from it.
func deriveHeldFixtureWithJournal(t *testing.T, outcome delegatestore.OutcomeStatus, reason string) (*Session, string, string) {
	t.Helper()
	return deriveHeldFixtureWithJournalTrigger(t, delegatestore.TriggerAttention, outcome, reason)
}

// deriveHeldFixtureWithJournalTrigger is deriveHeldFixtureWithJournal with the
// settled generation's own run trigger chosen by the caller.
func deriveHeldFixtureWithJournalTrigger(t *testing.T, trigger delegatestore.RunTrigger, outcome delegatestore.OutcomeStatus, reason string) (*Session, string, string) {
	t.Helper()
	sess, _ := newCompactableSession(t)
	controller, journalPath := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, controller, "dlg_hold", "")
	controller.mu.Lock()
	controller.live["dlg_hold"] = &delegateLiveState{runtime: sess}
	controller.mu.Unlock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_hold"
	// The delegate identity exists only for the derivation under test: this
	// session is no real delegate child, and its own teardown cleanup would
	// otherwise treat the fabricated settled outcome as a run whose result it
	// must deliver, waiting out the delivery give-up on close. Detach the
	// fabrication before that teardown runs (cleanups are LIFO, and the
	// session's own close was registered first).
	t.Cleanup(func() {
		sess.mu.Lock()
		sess.delegateController = nil
		sess.owningDelegateID = ""
		sess.mu.Unlock()
	})

	const sourceID = "delegate:dlg_worker/delivery/1"
	if _, err := sess.appendDelegateNotificationDurably(sourceID, acceptedTriggerText); err != nil {
		t.Fatalf("append the source row: %v", err)
	}
	reservation, err := controller.ReserveAttention(sess, sourceID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := acceptDelegateAttentionForTest(controller, sess, reservation); err != nil {
		t.Fatalf("acceptDelegateAttention: %v", err)
	}
	// LEGACY PRE-UPGRADE GENERATION (pre-#5681): the journal this case restores
	// came from a release that consumed the trigger before its run.
	consumeAcceptedDelegateAttentionForLegacyFixture(t, controller, sess, reservation)
	// The claimed generation's rest: the run settled with the fixture's
	// outcome, leaving the aggregate at that generation. The stop is journaled
	// rather than seeded in memory, so a controller reopened from the journal —
	// the cold restore a case builds — folds exactly the aggregate the
	// derivation reads. The finish's terminal delivery is acknowledged in the
	// same journal so no teardown waits on a delivery nothing will present.
	journalDeriveHeldStopWithTrigger(t, controller, trigger, outcome, reason)
	// The journaled finish settles the run, and production releases its start
	// record on the same path. Releasing it here keeps the fixture equal to a
	// settled generation: an accepted record left in the map is a run the
	// controller still treats as starting, and any later attention open would
	// wait on a done channel nothing will ever close.
	controller.mu.Lock()
	var settling []*delegateStartRecord
	for _, record := range controller.reservations {
		if record != nil && record.delegateID == "dlg_hold" && record.trigger == delegatestore.TriggerAttention {
			settling = append(settling, record)
		}
	}
	var settles []context.CancelFunc
	for _, record := range settling {
		settles = append(settles, controller.releaseReservationLocked(record))
	}
	controller.mu.Unlock()
	for _, settle := range settles {
		if settle != nil {
			settle()
		}
	}
	reopenedID := delegateAttentionReopenID(sourceID, 1)
	if _, err := sess.appendDelegateNotificationDurably(reopenedID, acceptedTriggerText); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
	return sess, reopenedID, journalPath
}

// deriveSendSettledFixture is deriveHeldFixtureWithJournal's shape with the
// settled generation's own run trigger set to a send: a busy refusal reopened
// the source at the recycled generation, and an explicit send — not an
// attention run — committed and settled that generation. Only the run's own
// trigger and outcome can tell the two apart, which is why a restore reads them
// per generation rather than from the delegate's newest outcome.
func deriveSendSettledFixture(t *testing.T) (*Session, string) {
	t.Helper()
	sess, reopenedID, _ := deriveHeldFixtureWithJournalTrigger(t, delegatestore.TriggerOwnerInput, delegatestore.OutcomeCancelled, "cancelled")
	return sess, reopenedID
}

// Two stops can quiet two reports before either presents. The delegate then
// rests at the second generation and an armed successor runs for the second
// row. A cold restore that asks only the newest generation rebuilds the second
// quiet and presents the FIRST report on a stale wake that never released it.
// Each reopened row is ordered against its own settled stop.

// journalDeriveHeldStopWithTrigger journals a settled generation whose run
// trigger the caller chooses: run started at generation 1, then the run finished
// with the given outcome and the terminal error packet a stop settles. The
// trigger matters because a send that committed the recycled generation owes no
// attention quiet, and only the run's own trigger says so. The finish's delivery
// is acknowledged in the same batch, so the aggregate rests with no pending
// delivery and no teardown waits on one. The stop's own hold is not written
// here — the derivation is what rebuilds it from this evidence.
func journalDeriveHeldStopWithTrigger(t *testing.T, controller *delegateTreeController, trigger delegatestore.RunTrigger, outcome delegatestore.OutcomeStatus, reason string) {
	t.Helper()
	const delegateID = "dlg_hold"
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	_, err := controller.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: delegateID,
			RunStarted: &delegatestore.RunStarted{
				Generation: 1,
				Trigger:    trigger,
				StartedAt:  time.Unix(10, 0).UTC(),
			},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: delegateID,
			RunFinished: &delegatestore.RunFinished{
				Generation:  1,
				Outcome:     delegatestore.Outcome{Status: outcome, Reason: reason, EndedAt: time.Unix(20, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  delegateID + "/delivery/1",
				Packet:      &stopped,
			},
		},
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID:           delegateID,
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: delegateID + "/delivery/1"},
		},
	)
	if err != nil {
		t.Fatalf("journal the stopped generation: %v", err)
	}
}

// A restored session's held rows come from durable state, and the predicate
// draws exactly the user stop's boundary: a pending reopen of the generation
// the delegate rests at is held when that generation settled cancelled and
// no new event has armed the row's wake; a controller-noted wake is a new
// event's arm and presents, and reopens keyed to a generation the journal
// never reached — the busy and failed-launch shapes — stay presentable on
// the restore.

// Two stops can quiet two reports before either presents. The delegate then
// rests at the second generation and an armed successor runs for the second
// row. A cold restore that asks only the newest generation rebuilds the second
// quiet and presents the FIRST report on a stale wake that never released it.
// Each reopened row is ordered against its own settled stop.
func TestDeriveHoldsEachRowByItsOwnStop(t *testing.T) {
	t.Parallel()
	sess, firstID, journalPath := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The second stop: another attention run settles cancelled at generation 2,
	// quieting its own reopened row. The delegate now rests at 2, so the
	// aggregate's newest outcome describes the second row alone.
	const secondSource = "delegate:dlg_worker/delivery/2"
	if _, err := sess.appendDelegateNotificationDurably(secondSource, acceptedTriggerText); err != nil {
		t.Fatalf("append the second source row: %v", err)
	}
	reservation, err := controller.ReserveAttention(sess, secondSource)
	if err != nil {
		t.Fatalf("reserve the second source row: %v", err)
	}
	if err := acceptDelegateAttentionForTest(controller, sess, reservation); err != nil {
		t.Fatalf("accept the second source row: %v", err)
	}
	// LEGACY PRE-UPGRADE GENERATION (pre-#5681): both stops in this journal came
	// from a release that consumed its trigger before its run.
	consumeAcceptedDelegateAttentionForLegacyFixture(t, controller, sess, reservation)
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	controller.mu.Lock()
	_, err = controller.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_hold",
			RunStarted: &delegatestore.RunStarted{Generation: 2, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(30, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_hold",
			RunFinished: &delegatestore.RunFinished{
				Generation:  2,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "cancelled", EndedAt: time.Unix(40, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  "dlg_hold/delivery/2",
				Packet:      &stopped,
			},
		},
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID:           "dlg_hold",
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: "dlg_hold/delivery/2"},
		},
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("journal the second stop: %v", err)
	}
	secondID := delegateAttentionReopenID(secondSource, 2)
	if _, err := sess.appendDelegateNotificationDurably(secondID, acceptedTriggerText); err != nil {
		t.Fatalf("append the second reopened row: %v", err)
	}
	// No release was journaled for either generation, so both quiets stand.
	restored := coldRestoredDeriveSession(t, sess, journalPath)
	restored.deriveHeldDelegateAttentionFromOutcome()
	restored.attentionMu.Lock()
	firstHeld := restored.delegateAttentionHeldFromAutomaticDriveLocked(firstID)
	secondHeld := restored.delegateAttentionHeldFromAutomaticDriveLocked(secondID)
	firstStamp := restored.attentionAutomaticDriveHolds[firstID]
	secondStamp := restored.attentionAutomaticDriveHolds[secondID]
	restored.attentionMu.Unlock()
	if !firstHeld || !secondHeld {
		t.Fatalf("a stop's quiet did not survive the restore (first held=%t, second held=%t): both rows are unreleased", firstHeld, secondHeld)
	}
	if firstStamp != 1 || secondStamp != 2 {
		t.Fatalf("the restored holds carry stamps %d and %d, want each row's own stop (1 and 2)", firstStamp, secondStamp)
	}
}

// A daemon can restart after a fresh wake journaled its release but before
// either row presented. Bootstrap puts both the released report and the fresh
// frame in the wake set, so a restore that consults the noted set when choosing
// which rows to move leaves the report ahead of the frame in the first request —
// the resident release always puts it at the tail. The noted set decides a
// hold's rebuild, never a move.

// Once a stop's quiet has been released, later generations owe it nothing. The
// scan that decides whether a wake supersedes an unreleased stop must stop at
// the durable release floor; otherwise the first wake at every later settled
// generation re-finds that same historical stop and appends another release
// for it, turning ordinary wakes into durable writes.
func TestReleasedStopOwesNoFurtherReleaseAtLaterGenerations(t *testing.T) {
	t.Parallel()
	sess, _, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// Generation 1 was the attention stop, and a wake already released it.
	journalDeriveHeldRelease(t, sess, 1)

	// A later generation settles for another row, then a fresh frame arrives.
	controller.mu.Lock()
	_, err := controller.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_hold",
			RunStarted: &delegatestore.RunStarted{Generation: 2, Trigger: delegatestore.TriggerAttention, StartedAt: time.Unix(70, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_hold",
			RunFinished: &delegatestore.RunFinished{
				Generation:  2,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCompleted, EndedAt: time.Unix(80, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  "dlg_hold/delivery/2",
				Packet:      &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"done"`)},
			},
		},
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID:           "dlg_hold",
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: "dlg_hold/delivery/2"},
		},
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("settle the later generation: %v", err)
	}
	controller.mu.Lock()
	before := controller.durable["dlg_hold"].AttentionHoldReleasedGeneration
	controller.mu.Unlock()

	const freshID = "delegate:dlg_worker/delivery/6"
	if _, err := sess.appendDelegateNotificationDurably(freshID, acceptedTriggerText); err != nil {
		t.Fatalf("append the fresh frame: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(controller, "dlg_hold", freshID); err != nil {
		t.Fatalf("admit the fresh wake: %v", err)
	}

	// No release event was appended: the floor stayed where the earlier release
	// left it. A release here would name a stop already superseded.
	controller.mu.Lock()
	after := controller.durable["dlg_hold"].AttentionHoldReleasedGeneration
	controller.mu.Unlock()
	if after != before {
		t.Fatalf("the wake at generation 3 re-released the stop already released at floor %d (now %d): the scan must stop at the floor", before, after)
	}
	if before != 1 {
		t.Fatalf("premise broken: the earlier release did not land at generation 1 (floor %d)", before)
	}
}

func TestFreshWakeJournalsAReleasePastALaterSendSettle(t *testing.T) {
	t.Parallel()
	sess, reopenedID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	controller := sess.delegateController
	// The earlier attention stop's quiet stands: the row is held at its own
	// generation, and no release has reached it.
	sess.holdDelegateAttentionFromAutomaticDriveAt(reopenedID, 1)
	// A later explicit send commits and settles generation 2 cancelled. Its own
	// trigger is owner input, so nothing about it is an attention stop — but the
	// earlier report is still pending and still quieted.
	stopped := delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"stopped"`)}
	controller.mu.Lock()
	_, err := controller.appendLocked(
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "dlg_hold",
			RunStarted: &delegatestore.RunStarted{Generation: 2, Trigger: delegatestore.TriggerOwnerInput, StartedAt: time.Unix(50, 0).UTC()},
		},
		delegatestore.Event{
			Kind:       delegatestore.EventDelegateRunFinished,
			DelegateID: "dlg_hold",
			RunFinished: &delegatestore.RunFinished{
				Generation:  2,
				Outcome:     delegatestore.Outcome{Status: delegatestore.OutcomeCancelled, Reason: "cancelled", EndedAt: time.Unix(60, 0).UTC()},
				Disposition: delegatestore.DispositionTerminalError,
				DeliveryID:  "dlg_hold/delivery/2",
				Packet:      &stopped,
			},
		},
		delegatestore.Event{
			Kind:                 delegatestore.EventDelegateDeliveryAcknowledged,
			DelegateID:           "dlg_hold",
			DeliveryAcknowledged: &delegatestore.DeliveryAcknowledged{DeliveryID: "dlg_hold/delivery/2"},
		},
	)
	controller.mu.Unlock()
	if err != nil {
		t.Fatalf("settle the later send: %v", err)
	}

	// A fresh frame arrives and its wake is admitted at the settled generation.
	const freshID = "delegate:dlg_worker/delivery/4"
	if _, err := sess.appendDelegateNotificationDurably(freshID, acceptedTriggerText); err != nil {
		t.Fatalf("append the fresh frame: %v", err)
	}
	if _, _, err := openDelegateAttentionForTest(controller, "dlg_hold", freshID); err != nil {
		t.Fatalf("admit the fresh wake: %v", err)
	}

	controller.mu.Lock()
	floor := controller.durable["dlg_hold"].AttentionHoldReleasedGeneration
	controller.mu.Unlock()
	if floor != 2 {
		t.Fatalf("the fresh wake journaled no release (floor %d, want 2): a later send's own trigger does not make the earlier attention stop's quiet current, so a restart rebuilds it over the fresh frame", floor)
	}
}

// settledGeneration reads the generation sess's delegate rests at, sampled the
// way the controller samples it when it admits a wake — the value a hold's
// stamp and a release's admitted generation are both ordered against.
func settledGeneration(t *testing.T, sess *Session) uint64 {
	t.Helper()
	sess.mu.Lock()
	controller := sess.delegateController
	owningDelegateID := sess.owningDelegateID
	sess.mu.Unlock()
	if controller == nil {
		t.Fatal("the fixture session has no controller to sample")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.attentionWakeGenerationLocked(owningDelegateID)
}

// journalDeriveHeldRelease journals the release an admitted wake records for
// generation: the durable floor a hold's install is ordered against. It writes
// the event the controller's own funnel writes, through the same locked append.
func journalDeriveHeldRelease(t *testing.T, sess *Session, generation uint64) {
	t.Helper()
	sess.mu.Lock()
	controller := sess.delegateController
	owningDelegateID := sess.owningDelegateID
	sess.mu.Unlock()
	if controller == nil {
		t.Fatal("the fixture session has no controller to journal through")
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if _, err := controller.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionHoldReleased,
		DelegateID: owningDelegateID,
		AttentionHoldReleased: &delegatestore.AttentionHoldReleased{
			Generation: generation,
		},
	}); err != nil {
		t.Fatalf("journal the hold release at generation %d: %v", generation, err)
	}
}

// coldRestoredDeriveSession rebuilds the durable side of a session the idle
// release reclaimed: a controller over the same journal path, folded from disk
// exactly as a restart folds it, and a session that owns the same delegate and
// reads the same transcript. Everything the released runtime held only in
// memory — the holds map, the controller's wake set — is gone by construction,
// which is the state the derivation must decide from.
//
// The restored session is built with the reopened controller and the released
// transcript identity from the start, so it opens no journal of its own: one
// store per journal, closed by the one cleanup that owns it. A fixture that
// opened a second journal and swapped the field would strand an open store, and
// a leaked store is the same defect class as a test that cannot fail.
func coldRestoredDeriveSession(t *testing.T, released *Session, journalPath string) *Session {
	t.Helper()
	released.mu.Lock()
	stateDir := released.stateDir
	sessionID := released.id
	owningDelegateID := released.owningDelegateID
	released.mu.Unlock()
	if journalPath == "" {
		t.Fatal("the fixture has no journal path to restore from")
	}
	// The journal's own write side closes with the released runtime; a
	// reopened store reads the bytes the release wrote.
	if err := released.delegateController.store.Close(); err != nil {
		t.Fatalf("close the released journal: %v", err)
	}
	store, err := delegatestore.Open(journalPath)
	if err != nil {
		t.Fatalf("reopen the journal: %v", err)
	}
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      stateDir,
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("open the restored controller: %v", err)
	}
	restored := newSession(t, withDir(stateDir), withConfig(SessionConfig{
		StateDir: stateDir,
		spawn: spawnConfig{
			sessionID:          sessionID,
			delegateController: controller,
			owningDelegateID:   owningDelegateID,
		},
	}))
	// The inherited controller is this fixture's own: it owns the store, and
	// this is the single cleanup that closes it.
	restored.ownsDelegateController = true
	t.Cleanup(func() {
		_ = controller.store.Close()
		restored.mu.Lock()
		restored.delegateController = nil
		restored.owningDelegateID = ""
		restored.mu.Unlock()
	})
	return restored
}

// Once a settled round of the active turn has presented a delegate attention
// turn, a mid-turn compaction must fold it away rather than re-injecting it
// after the summary, while still preserving any later attention item that
// arrived before compaction without being presented yet, and consuming both
// durably when the turn finishes.

func openDelegateAttentionForTest(c *delegateTreeController, delegateID, attentionID string) (bool, uint64, error) {
	for {
		added, blocker, plan, emit, generation, err := c.tryOpenDelegateAttention(delegateID, attentionID)
		if err != nil {
			return false, 0, err
		}
		if blocker != nil {
			<-blocker
			continue
		}
		if emit {
			c.emitDelegateUpdate(plan)
		}
		return added, generation, nil
	}
}

// appendCompactionMarkerAfterReopenedRow writes, after the reopened row's turn,
// the compaction marker every later request resumes from — the shape in which a
// pending row's turn falls behind the anchor while the fold still lists the row
// pending.
func appendCompactionMarkerAfterReopenedRow(t *testing.T, path, sessionID string) {
	t.Helper()
	writer, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatalf("open the attention transcript: %v", err)
	}
	marker := schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nfolded after the released report\n[END SUMMARY]"))
	if _, err := writer.Record(marker, transcript.RecordOptions{Door: transcript.DoorSynced}); err != nil {
		t.Fatalf("append the compaction marker: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close the attention transcript: %v", err)
	}
}
