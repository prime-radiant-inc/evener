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
	main := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.FinalResponse("answered the update") },
	}}
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
	if _, err := sess.reopenLatchedDelegateAttention(); err != nil {
		t.Fatalf("reopenLatchedDelegateAttention: %v", err)
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

	reopened, err := sess.reopenLatchedDelegateAttention()
	if err != nil {
		t.Fatalf("reopenLatchedDelegateAttention: %v", err)
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
	sess.attentionAutomaticDriveHolds = map[string]struct{}{heldID: {}}
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
	if err := sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("acceptDelegateAttention: %v", err)
	}
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

// deriveHeldFixtureWithJournal is deriveHeldFixture plus the fixture's journal
// path, for a case that restores a controller from it.
func deriveHeldFixtureWithJournal(t *testing.T, outcome delegatestore.OutcomeStatus, reason string) (*Session, string, string) {
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
	if err := sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("acceptDelegateAttention: %v", err)
	}
	// The claimed generation's rest: the run settled with the fixture's
	// outcome, leaving the aggregate at that generation. The stop is journaled
	// rather than seeded in memory, so a controller reopened from the journal —
	// the cold restore a case builds — folds exactly the aggregate the
	// derivation reads. The finish's terminal delivery is acknowledged in the
	// same journal so no teardown waits on a delivery nothing will present.
	journalDeriveHeldStop(t, controller, outcome, reason)
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

// journalDeriveHeldStop journals the attention generation a user's stop
// settled: run started at generation 1, then the run finished with the given
// outcome and the terminal error packet a stop settles. The finish's delivery
// is acknowledged in the same batch, so the aggregate rests with no pending
// delivery and no teardown waits on one. The stop's own hold is not written
// here — the derivation is what rebuilds it from this evidence.
func journalDeriveHeldStop(t *testing.T, controller *delegateTreeController, outcome delegatestore.OutcomeStatus, reason string) {
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
				Trigger:    delegatestore.TriggerAttention,
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
		sess, reopenedID := deriveHeldFixture(t, delegatestore.OutcomeCancelled, "cancelled")
		// The reviewed shape: a busy refusal reopened the source at the
		// recycled generation, and a SEND — not an attention run — committed
		// and settled that generation before the paced retry presented the
		// reopen. The send's user stop settles cancelled at the same
		// generation the reopen claims, so the generation and outcome match
		// the attention quiet exactly; only the run's trigger distinguishes
		// the send. The resident gate holds attention runs' reports alone,
		// so the restored session must not hold the reopen either.
		controller := sess.delegateController
		controller.mu.Lock()
		aggregate := controller.durable["dlg_hold"]
		aggregate.Trigger = delegatestore.TriggerOwnerInput
		controller.durable["dlg_hold"] = aggregate
		controller.mu.Unlock()
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
		// tail writes with.
		sess.holdDelegateAttentionFromAutomaticDrive(reopenedID)
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
		if _, err := sess.delegateController.openDelegateAttention("dlg_hold", freshID); err != nil {
			t.Fatalf("admit the fresh wake: %v", err)
		}
		sess.releaseAutomaticDriveHolds(delegateHoldReleaseArm)
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

// coldRestoredDeriveSession rebuilds the durable side of a session the idle
// release reclaimed: a fresh controller over the same journal path, folded from
// disk exactly as a restart folds it, and a fresh session that owns the same
// delegate and reads the same transcript. Everything the released runtime held
// only in memory — the holds map, the controller's wake set — is gone by
// construction, which is the state the derivation must decide from.
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
	t.Cleanup(func() { _ = store.Close() })
	restored := newSession(t, withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir}))
	// newSession opened a journal for the identity it minted, and this fixture
	// reads the released session's journal instead. Close that one now and
	// detach its ownership: leaving the field to be overwritten would strand an
	// open store no cleanup owns, and a leaked store is a fixture defect of the
	// same kind as a test that cannot fail.
	restored.mu.Lock()
	openedController := restored.delegateController
	restored.delegateController = nil
	restored.ownsDelegateController = false
	restored.mu.Unlock()
	if openedController != nil && openedController.store != nil {
		if err := openedController.store.Close(); err != nil {
			t.Fatalf("close the fixture's unused journal: %v", err)
		}
	}
	// The released session wrote its transcript under its own session id; the
	// restored one reads that file rather than creating an empty transcript of
	// its own, so the fold sees the rows the stop left.
	restored.mu.Lock()
	restored.id = sessionID
	restored.mu.Unlock()
	controller, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: "root-session",
		stateDir:      stateDir,
	})
	if err != nil {
		t.Fatalf("open the restored controller: %v", err)
	}
	restored.delegateController = controller
	restored.owningDelegateID = owningDelegateID
	t.Cleanup(func() {
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
	main.steps = []func(llm.Request) llm.Response{
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
	}

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

	main.steps = []func(llm.Request) llm.Response{
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
	}

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
