package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
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

// TestDelegateWatchOnlyDrainTurnAcknowledgesAndConsumesItsRow pins the drain
// spin LIFE-01 leaves behind (#2340 review M3).
//
// The drain opens a leased EntryDelegateAttention turn to present a pending
// watch row. A delegate that ALREADY reported answers that turn with plain
// text, and recordAttentionNoAction refuses the no-action outcome once the
// completion requirement is report-required -- so before this fix the turn
// retried toward a report it must not overwrite, ended in error, resolved no
// coverage, and keepingReport swallowed the failure. The row stayed pending and
// the drain opened another turn for it, forever.
//
// The fixture is the reviewer's own shape: the REAL turn runs against a
// scripted acknowledgment, and the assertion is made only after the drain
// RETURNS. A test that checked the row while the drain was still spinning would
// pass with the defect in place, which is exactly the trap here.
//
// The bound is what makes the wait meaningful: the process func counts turns
// and cancels the drain on the third, so a drain that keeps re-opening turns
// fails loudly instead of hanging, and one that does not return at all fails on
// the context deadline.
func TestDelegateWatchOnlyDrainTurnAcknowledgesAndConsumesItsRow(t *testing.T) {
	t.Parallel()
	sess, main := newCompactableSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, "dlg_coordinator", "")
	controller.mu.Lock()
	controller.live["dlg_coordinator"].binding.runtime = sess
	controller.mu.Unlock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_coordinator"
	// The report was already accepted: this delegate owes nothing but the watch
	// acknowledgment, which is what makes the no-action outcome the right
	// answer and its refusal the defect.
	sess.mu.Lock()
	sess.terminalCommunicateAccepted = true
	sess.latestReport = communicateResult{called: true, reply: "already reported"}
	sess.mu.Unlock()

	// A pending WATCH row, appended through the production durable append.
	attentionID := stableWatchAttentionIDPrefix + "watch:life-01-m3:job_m3:0f0f0f0f0f0f0f0f"
	if _, err := sess.appendDelegateNotificationDurably(attentionID, "Timer fired, 1 times since your last turn."); err != nil {
		t.Fatalf("append watch attention: %v", err)
	}
	if err := sess.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm watch attention: %v", err)
	}

	turns := 0
	process := func(ctx context.Context, text string, images []ImageAttachment, kind EntryKind) (string, error) {
		turns++
		if turns > 2 {
			t.Error("the drain keeps opening turns for a watch row the acknowledged turn already presented: the row is never consumed")
			return "", context.Canceled
		}
		return sess.ProcessInputKind(ctx, text, images, kind)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, delegateRunLeaseContextKey{}, delegateLease{delegateID: "dlg_coordinator", generation: 1})
	if _, err := sess.drainJobTreeWith(ctx, make(chan time.Time), func(context.Context) error { return nil }, process); err != nil {
		t.Fatalf("drainJobTreeWith: %v", err)
	}

	if pending, err := sess.pendingDelegateAttentionIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after the drain returned = %v (err %v), want the acknowledged watch row consumed", pending, err)
	}
	if len(main.requests) == 0 {
		t.Fatal("the drain made no model request: the acknowledgment was never elicited, so this fixture proves nothing")
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

// A request has two builders, and an unleased stable-child turn must keep an
// unresolved watch row out of BOTH. The first builder's projection filtered the
// row, but the skill-delivery rebuild re-snapshots history and copies every turn
// appended since the round's snapshot into the primary and the fallback source
// without asking the same question — so a watch notice landing between the
// snapshot and the rebuild reached the provider on an unleased turn, and the
// leased drive then owed it again (#2340 review finding 1).
//
// The second arm is the guard against over-filtering: the filter must not cost
// the ordinary continuation, or dropping a watch row would trade a leak for a
// request that can no longer continue from its anchor.
func TestUnleasedStableChildKeepsAnUnresolvedWatchRowOutOfEveryBuilder(t *testing.T) {
	t.Parallel()
	const (
		priorText = "prior conversation marker"
		watchText = "unresolved watch row marker"
		envText   = "environment marker appended after the anchor"
	)
	newContinuationSession := func(t *testing.T) *Session {
		t.Helper()
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
		t.Cleanup(sess.Close)
		drainSessionEvents(sess)
		// A stable delegate child with no lease in context: the turn the issue
		// permits (a completion drive, a restored first task) and the one whose
		// projection must keep unresolved watch rows out.
		controller, _ := newDelegateControllerTestHarness(t, 1, 1)
		sess.mu.Lock()
		ownController := sess.delegateController
		sess.delegateController = controller
		sess.owningDelegateID = "dlg_target"
		sess.mu.Unlock()
		t.Cleanup(func() {
			sess.mu.Lock()
			sess.delegateController = ownController
			sess.owningDelegateID = ""
			sess.mu.Unlock()
		})
		anchor := responsesContinuationEligibleAssistantTurn("resp_watch_row_anchor")
		anchor.ResponseIDHash = "cont-handle-v1:response_id:watch_row"
		anchor.ResponseRequestFingerprint = "cont-req-v1:phase4d"
		anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:phase4d"
		sess.mu.Lock()
		sess.history = []schema.Turn{
			schema.NewTurn(schema.TurnUserInput, llm.User(priorText)),
			anchor,
		}
		sess.mu.Unlock()
		return sess
	}
	// prepare builds the unleased stable-child request and returns the request
	// plus the fallback source the next provider in the chain would receive.
	prepare := func(t *testing.T, sess *Session) (llm.Request, []llm.Message) {
		t.Helper()
		var timings events.RoundTimings
		// No delegate lease in context: this is the unleased stable-child turn.
		_, _, _, req, fullHistory, _, err := sess.prepareModelRequestWithError(context.Background(), 1, &timings)
		if err != nil {
			t.Fatalf("prepare model request: %v", err)
		}
		return req, fullHistory
	}
	// armDeliveryRebuild gives the session one pending delivery obligation that
	// cannot succeed, so prepareSkillDelivery appends a notification turn and the
	// request rebuilds through rebuildForAppendedSkillTurns.
	armDeliveryRebuild := func(t *testing.T, sess *Session) {
		t.Helper()
		sess.mu.Lock()
		sess.skillLifecycle.Obligations = append(sess.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
			InvocationID: "inv-watch-row",
			Route:        "model_tool",
		})
		sess.skillLifecycle.Revision++
		sess.mu.Unlock()
	}

	t.Run("rebuild does not carry a watch row that landed after the snapshot", func(t *testing.T) {
		t.Parallel()
		sess := newContinuationSession(t)
		const watchID = stableWatchAttentionIDPrefix + "watch:phase1:job:phase1"
		armDeliveryRebuild(t, sess)
		// The row lands exactly where the review names: after the round's history
		// snapshot, before the rebuild re-snapshots.
		var landOnce sync.Once
		updateSessionTestConfig(sess, func(cfg *testConfig) {
			cfg.afterModelHistorySnapshot = func() {
				landOnce.Do(func() {
					if _, err := sess.appendDelegateNotificationDurably(watchID, watchText); err != nil {
						t.Errorf("persist watch attention: %v", err)
					}
				})
			}
		})
		req, fallback := prepare(t, sess)
		// Positive control: the row really is pending and really is in the
		// resident history, so an empty request here means the filter worked and
		// not that the fixture never created the row.
		pending, err := sess.pendingDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read pending attention: %v", err)
		}
		if !slices.Contains(pending, watchID) {
			t.Fatalf("setup: the watch row is not pending (pending=%v)", pending)
		}
		if indexOfTurnText(currentHistory(t, sess), watchText) < 0 {
			t.Fatal("setup: the watch row never reached the resident history")
		}
		if requestMessagesContainText(req.Messages, watchText) {
			t.Fatal("the rebuilt unleased request carried an unresolved watch row: the row must wait for the leased turn that presents it")
		}
		if requestMessagesContainText(fallback, watchText) {
			t.Fatal("the fallback source carried an unresolved watch row: a later provider in the chain would see it")
		}
		// Dropping the row must not cost the conversation: the anchor's own
		// history is still carried.
		if !requestMessagesContainText(req.Messages, priorText) {
			t.Fatal("the request lost its conversation history while dropping the watch row")
		}
	})

	t.Run("ordinary continuation still plans a delta", func(t *testing.T) {
		t.Parallel()
		sess := newContinuationSession(t)
		sess.mu.Lock()
		sess.history = append(sess.history, schema.NewTurn(schema.TurnEnvironment, llm.User(envText)))
		sess.mu.Unlock()
		req, _ := prepare(t, sess)
		if req.HistoryMode != llm.HistoryModeResponsesDelta {
			t.Fatalf("HistoryMode = %q, want %q: with nothing pending the anchor must still be usable", req.HistoryMode, llm.HistoryModeResponsesDelta)
		}
		if req.PreviousResponseID == "" {
			t.Fatal("PreviousResponseID is empty: the delta did not continue from the stored anchor")
		}
		if req.Continuation == nil || req.Continuation.DeltaTurnCount == 0 {
			t.Fatalf("Continuation = %+v, want a non-empty delta: the anchor was selected but carried no turns", req.Continuation)
		}
		if !requestMessagesContainText(req.Messages, envText) {
			t.Fatal("the planned delta did not carry the turn appended after the anchor")
		}
	})

	// A watch notice has two durable families: the send rail's frame
	// ("watch:<delivery>:<seq>") and the diagnostic ("watchdiag:..."). Filtering
	// only the diagnostic family left the send-rail rows in the projection, so a
	// sidecar frame that reached the child's transcript before this drive
	// snapshotted history went to the provider on an unleased turn — and the
	// drive cannot settle it, so the leased turn presented it again
	// (#2340 review M1).
	t.Run("send-rail frame waits for its leased turn too", func(t *testing.T) {
		t.Parallel()
		sess := newContinuationSession(t)
		const watchFrameID = "watch:dlv_m1:3"
		const frameText = "pending watch send frame marker"
		// The row is already resident when the round snapshots history: only the
		// projection filter can keep it out of the request.
		if _, err := sess.appendDelegateNotificationDurably(watchFrameID, frameText); err != nil {
			t.Fatalf("persist the watch send frame: %v", err)
		}
		req, fallback := prepare(t, sess)
		pending, err := sess.pendingDelegateAttentionIDs()
		if err != nil {
			t.Fatalf("read pending attention: %v", err)
		}
		if !slices.Contains(pending, watchFrameID) {
			t.Fatalf("setup: the send-rail row is not pending (pending=%v)", pending)
		}
		if indexOfTurnText(currentHistory(t, sess), frameText) < 0 {
			t.Fatal("setup: the send-rail row never reached the resident history")
		}
		if requestMessagesContainText(req.Messages, frameText) {
			t.Fatal("the unleased request carried an unresolved send-rail watch row: it must wait for the leased turn that presents and settles it")
		}
		if requestMessagesContainText(fallback, frameText) {
			t.Fatal("the fallback source carried an unresolved send-rail watch row")
		}
		if !requestMessagesContainText(req.Messages, priorText) {
			t.Fatal("the request lost its conversation history while dropping the send-rail row")
		}
	})
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
	sess, _ := newCompactableSession(t)
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
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
	// outcome, leaving the aggregate at that generation. The three durable
	// facts the derivation reads are seeded directly — the journal's own
	// settle path is pinned end to end by the release-restore test, and a
	// journaled finish here would queue the run's terminal delivery, which
	// the session's teardown then waits out.
	controller.mu.Lock()
	aggregate := controller.durable["dlg_hold"]
	aggregate.Generation = 1
	aggregate.Trigger = delegatestore.TriggerAttention
	aggregate.CurrentRunOpen = false
	aggregate.LatestOutcome = &delegatestore.Outcome{
		Status:  outcome,
		Reason:  reason,
		EndedAt: time.Unix(20, 0).UTC(),
	}
	controller.durable["dlg_hold"] = aggregate
	controller.mu.Unlock()
	reopenedID := delegateAttentionReopenID(sourceID, 1)
	if _, err := sess.appendDelegateNotificationDurably(reopenedID, acceptedTriggerText); err != nil {
		t.Fatalf("append the reopened row: %v", err)
	}
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

// fencedCompactionRig builds a stable child whose automatic compaction reaches
// both providers, with the counts on separate adapters so neither assertion can
// pass by never calling the model at all.
//
// Pressure comes from a request-sized system prompt (sysPromptChars is a real
// production input: the caller's own prompt length) rather than from a synthetic
// token measurement, because the checkpoint clears the measurement before the
// summarizer threshold is evaluated. That is exactly what made the earlier
// fixture vacuous (review L1): zero summarizer calls passed there even with the
// fence removed. Every test below therefore asserts BOTH directions on the same
// fixture -- a control arm that must reach the summarizer and a fenced arm that
// must not.
const (
	fencedCompactionWindow       = 60_000
	fencedCompactionSysPrompt    = 240_000
	fencedCompactionDelegateName = "dlg_compaction_target"
)

func fencedCompactionRig(t *testing.T) (*Session, *delegateTreeController, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var elicited, summarized atomic.Int32
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, controller, fencedCompactionDelegateName, "")
	main := newFakeAdapter("openai", func(llm.Request) llm.Response {
		elicited.Add(1)
		return agenttest.FinalResponse("a pinned note")
	})
	// The cheap route also serves the session namer, which is not a
	// summarizer, so the count must attribute a request structurally rather
	// than by its prompt copy (see isSummarizerRequest).
	cheap := &agenttest.ScriptedAdapter{Provider: "compact-cheap", Responder: func(req llm.Request) llm.Response {
		if isSummarizerRequest(req) {
			summarized.Add(1)
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nsummary\n[END SUMMARY]")}
	}}
	client := llm.NewClient()
	client.Register(main)
	client.Register(cheap)
	stateDir := t.TempDir()
	// 60k keeps summarizerInputLimit above minSummarizingWindow: at 50k the
	// summarize layer is unusable and the fenced arm's zero would mean nothing.
	profile := withContextWindow(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "compact-cheap/model"), fencedCompactionWindow)
	sess := newSession(t, withClient(client), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))
	sess.delegateController = controller
	sess.owningDelegateID = fencedCompactionDelegateName
	controller.mu.Lock()
	controller.live[fencedCompactionDelegateName].binding.runtime = sess
	controller.mu.Unlock()
	seedNumberedSessionHistory(t, sess, 12)
	return sess, controller, &elicited, &summarized
}

// isSummarizerRequest reports whether req is one of the context manager's own
// compaction calls: the summarizer, or the note elicitor that shares its
// request shape.
//
// The discriminator is structural, not textual. completeSummarization builds a
// bare llm.Request (summarize_route.go:161) and sends it through
// client.Complete or the cheap caller, so it carries NO ResponseFormat. The
// other cheap-route caller, the session namer, goes through llm.GenerateObject
// (session_namer.go), which stamps a json_schema ResponseFormat
// (generate_object.go's generateOptions). A namer call therefore cannot be
// counted here, and a refused summarizer cannot be masked by one.
//
// Elicitation is indistinguishable from summarization by this test -- both are
// bare completeSummarization requests. In the two fixtures below that ambiguity
// is closed by construction: either note elicitation is muted
// (muteNoteElicitation) or it is routed through the MAIN adapter, which is the
// elicited counter, so the only bare cheap-route request is the summarizer's.
func isSummarizerRequest(req llm.Request) bool {
	return req.ResponseFormat == nil
}

// fencedCompactionLeaseCtx is the run lease the child's own generation holds.
func fencedCompactionLeaseCtx() context.Context {
	return context.WithValue(context.Background(), delegateRunLeaseContextKey{},
		delegateLease{delegateID: fencedCompactionDelegateName, generation: 1})
}

// TestStableChildCompactionStopsSpendingAfterAFencedStop pins the elicitation
// half of LIFE-01 (#2340): automatic compaction reaches a provider TWICE, once
// in the note elicitor and again in the summarizer. A stop recorded while the
// elicitor runs lands after the elicitor's own admission and before the
// summarizer's call. The summarizer must not spend a request on a generation the
// user has already stopped.
//
// The control arm is what makes the fenced arm evidence (review L1).
func TestStableChildCompactionStopsSpendingAfterAFencedStop(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		name   string
		fence  bool
		wantSu bool
	}{
		{name: "control_no_stop", wantSu: true},
		{name: "fenced_during_elicitation", fence: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			sess, controller, elicited, summarized := fencedCompactionRig(t)
			if arm.fence {
				// Record the stop BETWEEN the two provider admissions: after the
				// elicitor asks the model, before the summarizer does.
				// PendingStopSeq is the state admitLeaseLocked refuses on.
				sess.elicitNoteFn = func(ctx context.Context, turns []schema.Turn) (string, error) {
					resp, err := sess.client.Complete(ctx, llm.Request{
						Model:    sess.profile.Model(),
						Messages: []llm.Message{llm.User("elicit a note")},
					})
					recordSubtreeStop(t, controller, fencedCompactionDelegateName)
					if err != nil {
						return "", err
					}
					return resp.Text(), nil
				}
			}
			sess.foldHistoryBeforeRequest(fencedCompactionLeaseCtx(), 0, fencedCompactionSysPrompt)
			if arm.fence && elicited.Load() != 1 {
				t.Fatalf("the elicitor made %d provider request(s), want exactly 1: without it the test cannot tell a refused summarizer from a fold that never reached it", elicited.Load())
			}
			assertFencedCompactionArms(t, arm.wantSu, summarized.Load())
		})
	}
}

// checkpointStopStrategy interposes on ManageContext's emit stream so the test
// records the subtree stop at the DETERMINISTIC CHECKPOINT's own emit -- after the
// checkpoint layer folded, before the summarize layer is decided. That instant is
// inside the window review M6 names: the summarizer's verdict was taken before
// ManageContext ran, and a stop recorded here must still fence the request.
// Everything else is the production strategy, unchanged.
type checkpointStopStrategy struct {
	contextmgr.Strategy
	record func()
}

func (s *checkpointStopStrategy) ManageContext(ctx context.Context, history *[]schema.Turn, sysPromptChars int, emitFn func(events.EventKind, events.EventData)) error {
	return s.Strategy.ManageContext(ctx, history, sysPromptChars, func(kind events.EventKind, data events.EventData) {
		emitFn(kind, data)
		if record := s.record; record != nil {
			if ccd, ok := data.(events.ContextCompactionData); ok && ccd.Layer == "checkpoint" {
				s.record = nil
				record()
			}
		}
	})
}

// TestStableChildCompactionFencesTheSummarizerAtItsDispatchBoundary is review
// M6's window: the summarizer's lease verdict is taken BEFORE ManageContext runs
// the deterministic checkpoint (and its PreCompact hook). A subtree stop recorded
// during that work is not visible to a verdict taken earlier, so the summarize
// layer would dispatch on a generation the user has already stopped.
//
// The stop is recorded DURING the checkpoint, not before it, and the control arm
// proves the same fixture reaches the summarizer when no stop is recorded.
func TestStableChildCompactionFencesTheSummarizerAtItsDispatchBoundary(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		name   string
		fence  bool
		wantSu bool
	}{
		{name: "control_no_stop", wantSu: true},
		{name: "fenced_during_checkpoint", fence: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			sess, controller, elicited, summarized := fencedCompactionRig(t)
			muteNoteElicitation(sess)
			inner := sess.strategy
			decorator := &checkpointStopStrategy{Strategy: inner}
			if arm.fence {
				decorator.record = func() { recordSubtreeStop(t, controller, fencedCompactionDelegateName) }
			}
			sess.strategy = decorator
			sess.foldHistoryBeforeRequest(fencedCompactionLeaseCtx(), 0, fencedCompactionSysPrompt)
			if arm.fence && elicited.Load() != 0 {
				t.Fatalf("the elicitor made %d provider request(s), want 0: this arm is about the summarizer, not the elicitor", elicited.Load())
			}
			assertFencedCompactionArms(t, arm.wantSu, summarized.Load())
		})
	}
}

// TestStableChildRecoveryCompactionFencesTheSummarizer is review M5's other
// window: automatic RECOVERY compaction (a local context-budget failure or a
// provider context-length rejection) reaches the same summarize layer through
// forceCompactForModelRecovery, which passed no live admission at all.
//
// So a subtree stop recorded during that fold's deterministic checkpoint was
// invisible to the summarize layer, and the summarizer dispatched on a
// generation the user had already stopped. The per-request fold installed the
// predicate; this path did not.
//
// WHERE THE STOP IS RECORDED MATTERS. The first version of this test decorated
// s.strategy, whose ManageContext the ForceCompact path never calls -- so the
// stop was never recorded and the fenced arm passed for the wrong reason, which
// is the trap the reviewer named. The seam here is the fold's own compaction-turn
// callback: the deterministic checkpoint reports its turn through it INSIDE the
// fold, before the summarize layer dispatches, so the stop lands in the window
// this finding is about and nowhere earlier.
//
// Both arms run the same fixture and the same recovery fold; only the stop
// differs. The control arm is what makes the fenced arm's zero evidence.
func TestStableChildRecoveryCompactionFencesTheSummarizer(t *testing.T) {
	t.Parallel()
	for _, arm := range []struct {
		name   string
		fence  bool
		wantSu bool
	}{
		{name: "control_no_stop", wantSu: true},
		{name: "fenced_during_checkpoint", fence: true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			sess, controller, _, summarized := fencedCompactionRig(t)
			muteNoteElicitation(sess)
			if arm.fence {
				// Record the stop INSIDE the window under test: after the
				// deterministic checkpoint has run and before the summarize layer
				// dispatches. That window is the PreCompact hook, and the runner
				// reports its start before it executes. OnCompactionTurn cannot
				// serve here: production installs a per-call context callback, and
				// handleCompactionTurn prefers it over the Manager field, so a
				// test-set field never fires.
				runner := hooks.NewRunner(nil, "")
				var once sync.Once
				runner.SetEventCallback(func(kind events.EventKind, data events.EventData) {
					start, ok := data.(events.HookStartData)
					if !ok || start.Event != string(plugin.HookPreCompact) {
						return
					}
					once.Do(func() { recordSubtreeStop(t, controller, fencedCompactionDelegateName) })
				})
				runner.Add(plugin.HookPreCompact, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "compact"})
				sess.hookRunner = runner
			}
			sess.forceCompactForModelRecovery(fencedCompactionLeaseCtx())
			if arm.fence {
				// Non-vacuity of the FENCE arm itself: the stop must really be
				// recorded, or a zero suffices for the wrong reason.
				if admitted := sess.delegateController.admitModelRequestLease(delegateLease{delegateID: fencedCompactionDelegateName, generation: 1}); admitted == nil {
					t.Fatal("setup: the stop fence was never recorded, so the fenced arm proves nothing")
				}
			}
			assertFencedCompactionArms(t, arm.wantSu, summarized.Load())
		})
	}
}

// assertFencedCompactionArms states the both-directions rule once: the control
// arm must reach the summarize layer, and the fenced arm must not.
func assertFencedCompactionArms(t *testing.T, wantSummarizer bool, summarized int32) {
	t.Helper()
	if wantSummarizer {
		if summarized == 0 {
			t.Fatal("the control arm made no summarizer request: the fixture never reached the summarize layer, so the fenced arm's zero would prove nothing about the fence")
		}
		return
	}
	if summarized != 0 {
		t.Fatalf("the summarizer spent %d provider request(s) after the stop was recorded, want 0: the fence verdict was taken before ManageContext and never re-asked at the dispatch boundary", summarized)
	}
}

// recordSubtreeStop durably records a subtree stop for id, the state
// admitLeaseLocked refuses on.
func recordSubtreeStop(t *testing.T, controller *delegateTreeController, id string) {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if _, err := controller.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateSubtreeStopRequested,
		DelegateID: id,
		TS:         time.Unix(20, 0).UTC(),
		SubtreeStopRequested: &delegatestore.SubtreeStopRequested{
			TargetDelegateID: id,
		},
	}); err != nil {
		t.Fatalf("record the stop fence: %v", err)
	}
}
