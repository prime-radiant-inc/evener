package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
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
		func(llm.Request) llm.Response { return finalResponse("answered the update") },
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
	sess := newSession(t, withClient(client), withProfile(profile), withoutGitSnapshot(), withDir(stateDir), withConfig(cfg))
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
	if _, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), make(chan time.Time), func(context.Context) error { return nil }, process); err != nil {
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
	if err := sess.requestNotificationWake(); err != nil {
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
	if err := sess.requestNotificationWake(); err != nil {
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
		if err := sess.requestNotificationWake(); err != nil {
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
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
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

// Accepting a generation is not observation. A fold before its first request
// must keep the trigger and the later pending body.
func TestAcceptedDelegateAttentionTriggerSurvivesCompactionBeforeItsFirstRequest(t *testing.T) {
	t.Parallel()
	sess, _, _ := acceptedTriggerSession(t)
	appendAcceptedTriggerLater(t, sess)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if indexOfTurnText(currentHistory(t, sess), acceptedTriggerText) < 0 {
		t.Fatal("the fold dropped the accepted generation's trigger before any request presented it")
	}
	assertAcceptedTriggerLaterPending(t, sess)
	observationReceipts(t, sess, nil)
}

// Once a settled request has presented the trigger, a later fold may summarize
// it away like any other consumed turn.
func TestAcceptedDelegateAttentionTriggerFoldsAwayOncePresented(t *testing.T) {
	t.Parallel()
	sess, main, ctx := acceptedTriggerSession(t)
	main.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{"TRIGGER_ITEM"}, []string{"LATER_ITEM"})
		appendAcceptedTriggerLater(t, sess)
		return finalResponse("trigger response")
	}}
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("trigger request: %v", err)
	}
	if len(main.Requests()) != 1 {
		t.Fatalf("model requests = %d, want 1", len(main.Requests()))
	}
	observationReceipts(t, sess, [][]string{{acceptedTriggerID}})
	// Move both bodies out of the recent tail so retention cannot pass merely
	// because the fold never reached the later pending item.
	seedNumberedSessionHistory(t, sess, 12)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if indexOfTurnText(currentHistory(t, sess), acceptedTriggerText) >= 0 {
		t.Fatal("the fold kept a trigger a settled request already presented")
	}
	assertAcceptedTriggerLaterPending(t, sess)
}

func TestAcceptedDelegateAttentionTriggerFailedResponseDoesNotObserve(t *testing.T) {
	t.Parallel()
	sess, _, ctx := acceptedTriggerSession(t)
	main := &skillContinuationAdapter{steps: []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
		observationRequest(t, req, []string{"TRIGGER_ITEM"}, []string{"LATER_ITEM"})
		appendAcceptedTriggerLater(t, sess)
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
	}}}
	sess.client.Register(main)
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil {
		t.Fatal("failed provider response succeeded")
	}
	if len(main.Requests()) != 1 {
		t.Fatalf("model requests = %d, want 1 failed request", len(main.Requests()))
	}
	observationReceipts(t, sess, nil)
	seedNumberedSessionHistory(t, sess, 12)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if indexOfTurnText(currentHistory(t, sess), acceptedTriggerText) < 0 {
		t.Fatal("the fold dropped the trigger after a failed response")
	}
	assertAcceptedTriggerLaterPending(t, sess)
}

const (
	acceptedTriggerID   = "delegate:dlg_worker/delivery/1"
	acceptedTriggerText = `<delegate-notification delegate_id="dlg_worker" kind="update">TRIGGER_ITEM</delegate-notification>`
	acceptedLaterID     = "delegate:dlg_worker/delivery/2"
	acceptedLaterText   = `<delegate-notification delegate_id="dlg_worker" kind="update">LATER_ITEM</delegate-notification>`
)

func appendAcceptedTriggerLater(t *testing.T, sess *Session) {
	t.Helper()
	if _, err := sess.appendDelegateNotificationDurably(acceptedLaterID, acceptedLaterText); err != nil {
		t.Fatal(err)
	}
	sess.delegateController.noteDelegateAttention("dlg_target", acceptedLaterID)
}

func assertAcceptedTriggerLaterPending(t *testing.T, sess *Session) {
	t.Helper()
	if indexOfTurnText(currentHistory(t, sess), acceptedLaterText) < 0 {
		t.Fatal("fold dropped the later unobserved body")
	}
	fold, err := readDelegateAttentionFold(sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, observed := fold.observed[acceptedLaterID]; observed {
		t.Fatal("later body was observed without appearing in a request")
	}
	if _, pending := fold.content[acceptedLaterID]; !pending {
		t.Fatal("later body has no durable custody")
	}
	if !slices.Contains(fold.pendingIDs(), acceptedLaterID) {
		t.Fatalf("later body is not pending: %v", fold.pendingIDs())
	}
}

// acceptedTriggerSession is a delegate runtime that has accepted an attention
// generation whose trigger is old enough for a fold to summarize away. The
// caller controls when a later item arrives relative to the real request.
func acceptedTriggerSession(t *testing.T) (*Session, *fakeAdapter, context.Context) {
	t.Helper()
	sess, main := newCompactableSession(t)
	if err := sess.closeOwnedDelegateStore(); err != nil {
		t.Fatal(err)
	}
	sess.ownsDelegateController = false
	controller, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, controller, "dlg_target", "")
	controller.mu.Lock()
	controller.live["dlg_target"] = &delegateLiveState{runtime: sess}
	controller.mu.Unlock()
	sess.delegateController = controller
	sess.owningDelegateID = "dlg_target"

	if _, err := sess.appendDelegateNotificationDurably(acceptedTriggerID, acceptedTriggerText); err != nil {
		t.Fatalf("append trigger: %v", err)
	}
	controller.noteDelegateAttention("dlg_target", acceptedTriggerID)
	seedNumberedSessionHistory(t, sess, 12)

	reservation, err := controller.ReserveAttention(sess, acceptedTriggerID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := sess.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("acceptDelegateAttention: %v", err)
	}
	commit, err := controller.CommitStart(reservation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	t.Cleanup(func() {
		if _, err := controller.FinishGeneration(commit.lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "cancelled"}); err != nil {
			t.Errorf("finish fixture generation: %v", err)
		}
	})
	ctx := context.WithValue(commit.ctx, delegateRunLeaseContextKey{}, commit.lease)
	return sess, main, ctx
}
