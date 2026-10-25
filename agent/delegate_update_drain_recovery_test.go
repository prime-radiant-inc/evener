package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Receipt recovery starts with an actual successful provider response. Merely
// assigning attentionCoveredIDs does not create a canonical observation.
func drainingDelegateWithUpdate(t *testing.T, configure func(*SessionConfig)) (*Session, *messagingAdapter, string) {
	t.Helper()
	adapter := newMessagingAdapter()
	adapter.script("root", func(llm.Request) llm.Response { return finalResponse("RECEIPT_913") })
	sess := newMessagingRoot(t, adapter, configure)
	id := delegateAttentionID(delegatestore.UpdateDeliveryID("dlg_worker", 1))
	if _, err := sess.appendDelegateNotificationDurably(id, "UPDATE_PAYLOAD_913"); err != nil {
		t.Fatal(err)
	}
	return sess, adapter, id
}

func pendingUpdateCount(t *testing.T, sess *Session) int {
	t.Helper()
	pending, err := sess.pendingDelegateAttentionIDs()
	if err != nil {
		t.Fatalf("read pending attention: %v", err)
	}
	return len(pending)
}

func requireRecoveredReceipt(t *testing.T, sess *Session, id string) {
	t.Helper()
	fold, err := readDelegateAttentionFold(sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if _, observed := fold.observed[id]; !observed || len(fold.observed) != 1 || fold.resolutions[id] != delegateAttentionConsumed || len(fold.pendingIDs()) != 0 {
		t.Fatalf("receipt/index: observed=%v resolutions=%v pending=%v", fold.observed, fold.resolutions, fold.pendingIDs())
	}
}

// The preceding turn observed the update even though its resolution sync
// failed. Repairing its index must not run a continuation or replace its reply.
func TestDelegateUpdateShownByThePrecedingTurnIsNotRerunByTheDrain(t *testing.T) {
	t.Parallel()
	sess, adapter, id := drainingDelegateWithUpdate(t, nil)
	fs := installResolutionSyncFailuresWriter(t, sess, 1)
	// TRIPWIRE: the scripted provider and local storage need no elapsed time.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || !strings.Contains(err.Error(), "injected root attention resolution sync failure") {
		t.Fatalf("preceding turn error = %v, want injected resolution sync failure", err)
	}
	turns := 0
	answer, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), make(chan time.Time), func(context.Context) error { return nil }, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		return "", errors.New("unexpected duplicate model turn")
	})
	if err != nil || answer != "" || turns != 0 || len(adapter.requestsFor("root")) != 1 || fs.injectedFailures() != 1 {
		t.Fatalf("drain answer=%q err=%v extra turns=%d requests=%d sync failures=%d", answer, err, turns, len(adapter.requestsFor("root")), fs.injectedFailures())
	}
	if !requestsContain(adapter.requestsFor("root"), "UPDATE_PAYLOAD_913") {
		t.Fatal("successful request omitted the update payload")
	}
	requireRecoveredReceipt(t, sess, id)
	if got := pendingUpdateCount(t, sess); got != 0 {
		t.Fatalf("%d updates still pending after storage recovered", got)
	}
}

// The first resolution sync fails at the response boundary, the second at drain
// entry, followed by one failed fold read. The timer must release the drain when
// it repairs the index, without another model call or a test-manufactured hint.
func TestDelegateUpdateIsNotRerunWhenMarkersAndTheFollowingReadFail(t *testing.T) {
	t.Parallel()
	sess, adapter, id := drainingDelegateWithUpdate(t, nil)
	recoverReceiptThroughDrain(t, sess, id, func() []llm.Request { return adapter.requestsFor("root") })
}

func recoverReceiptThroughDrain(t *testing.T, sess *Session, id string, requests func() []llm.Request) {
	t.Helper()
	var failing atomic.Pointer[rootAttentionResolutionFailFS]
	var readFailures atomic.Int32
	sess.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		if fs := failing.Load(); fs != nil && fs.injectedFailures() == 2 && readFailures.CompareAndSwap(0, 1) {
			return delegateAttentionFold{}, errors.New("transient transcript read failure")
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	fs := installResolutionSyncFailuresWriter(t, sess, 2)
	failing.Store(fs)
	before := len(requests())
	// No clock advance or external hint drives this drain.
	// TRIPWIRE: the receiver's local retry should finish well before this ceiling.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || !strings.Contains(err.Error(), "injected root attention resolution sync failure") {
		t.Fatalf("response error = %v, want injected resolution sync failure", err)
	}
	observed := rootAttentionFold(t, sess)
	if _, ok := observed.observed[id]; !ok || len(observed.observed) != 1 {
		t.Fatalf("successful response left no canonical receipt: %v", observed.observed)
	}
	if len(requests()) != before+1 || !requestsContain(requests()[before:], observed.content[id].Text()) {
		t.Fatal("exactly one successful request must contain the original source payload")
	}
	turns := 0
	answer, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), make(chan time.Time), func(context.Context) error { return nil }, func(context.Context, string, []ImageAttachment, EntryKind) (string, error) {
		turns++
		return "", errors.New("unexpected duplicate model turn")
	})
	t.Logf("drain answer=%q err=%v extra turns=%d requests=%d sync failures=%d read failures=%d", answer, err, turns, len(requests())-before, fs.injectedFailures(), readFailures.Load())
	// Read the durable result even on a timeout, so RED distinguishes a lost
	// progress wake from failed recovery or a duplicate provider execution.
	requireRecoveredReceipt(t, sess, id)
	if err != nil || answer != "" || turns != 0 || len(requests()) != before+1 || fs.injectedFailures() != 2 || readFailures.Load() != 1 {
		t.Fatal("receipt recovery did not settle the drain under exactly two sync failures and one read failure")
	}
	warnings := warningEvents(collectStallWarnings(sess))
	if len(warnings) != 1 || warnings[0].Source != string(diagnostic.SourceEvener) || warnings[0].Code != "" {
		t.Fatalf("warning events = %+v, want only the injected receipt-index repair failure", warnings)
	}
}

// The same repair must work with a real owned stable delegate's journal and
// transport, not just a receiver-local append or a fabricated owningDelegateID.
func TestDelegateReceiptRepairReleasesDrainForOwnedStableDelegate(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("OWNED_SOURCE_913") },
		func(llm.Request) llm.Response { return finalResponse("OWNED_RECEIPT_913") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableDelegateUnservedRoot(t, root, fixture)
	pending := waitForRootAttention(t, root, 1)
	fold := rootAttentionFold(t, root)
	if !strings.Contains(fold.content[pending[0]].Text(), fixture.delegateID) || !strings.Contains(fold.content[pending[0]].Text(), "OWNED_SOURCE_913") {
		t.Fatalf("actual owned delegate did not supply the source: %+v", fold.content)
	}
	recoverReceiptThroughDrain(t, root, pending[0], fixture.adapter.Requests)
}

// Every negative timer assertion joins the fake clock's callbacks. No sleep,
// polling loop, manufactured notification or new observation drives recovery.
func TestNotificationReceiptRetryCompletion(t *testing.T) {
	for _, mode := range []string{"stale", "stop", "close", "failed_repair"} {
		t.Run(mode, func(t *testing.T) {
			var logged diagnosticLogCapture
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
			t.Cleanup(func() {
				defer slog.SetDefault(previous)
				text := logged.String()
				if mode == "failed_repair" {
					if strings.Count(text, "\n") != 1 || !strings.Contains(text, "injected root attention resolution sync failure") {
						t.Errorf("expected exactly one failed-repair warning through cleanup, got %s", text)
					}
				} else if text != "" {
					t.Errorf("unexpected daemon log through cleanup: %s", text)
				}
			})
			clock := agenttest.NewFakeClock()
			sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
			failures := 1
			if mode == "failed_repair" {
				failures = 2
			}
			fs := installResolutionSyncFailuresWriter(t, sess, failures)
			// TRIPWIRE: an in-process scripted receipt must not wait for time.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil {
				t.Fatal("real receipt did not encounter the first sync failure")
			}
			var wakes atomic.Int32
			var callbacksAtWake atomic.Int32
			sess.SetNotifyFunc(func() {
				sess.attentionMu.Lock()
				callbacksAtWake.Store(int32(sess.attentionCallbacks))
				sess.attentionMu.Unlock()
				wakes.Add(1)
			})
			if !sess.notificationExecutionState().recovering {
				t.Fatal("failed index repair did not retain paced recovery")
			}
			switch mode {
			case "stale":
				sess.suspendNotificationWake()
				sess.scheduleNotificationRetry()
				sess.mu.Lock()
				sess.notificationSchedule.hinted = true
				before := sess.notificationSchedule
				sess.mu.Unlock()
				clock.Advance(jobNotificationRetryInitialDelay)
				clock.Drain()
				sess.mu.Lock()
				after := sess.notificationSchedule
				sess.mu.Unlock()
				if after != before || wakes.Load() != 0 || fs.injectedFailures() != 1 {
					t.Fatalf("stale callback reset schedule or woke: before=%+v after=%+v wakes=%d", before, after, wakes.Load())
				}
			case "stop":
				if _, err := sess.InterruptClientMutation(ctx, appwire.TurnInterruptParams{ClientMutationID: "receipt-stop-913"}, func() {}); err != nil {
					t.Fatal(err)
				}
			case "close":
				sess.Close()
			}
			if mode == "stop" || mode == "close" {
				clock.Advance(jobNotificationRetryMaxDelay)
				clock.Drain()
				if !sess.notificationExecutionState().held || wakes.Load() != 0 || len(adapter.requestsFor("root")) != 1 {
					t.Fatalf("%s hold lost: wakes=%d requests=%d", mode, wakes.Load(), len(adapter.requestsFor("root")))
				}
				return
			}
			if mode == "failed_repair" {
				clock.Advance(jobNotificationRetryInitialDelay)
				clock.Drain()
				if fs.injectedFailures() != 2 || !sess.notificationExecutionState().recovering || wakes.Load() != 0 {
					t.Fatalf("failed repair did not re-arm without wake: failures=%d recovering=%v wakes=%d", fs.injectedFailures(), sess.notificationExecutionState().recovering, wakes.Load())
				}
			}
			clock.Advance(2 * jobNotificationRetryInitialDelay)
			clock.Drain()
			requireRecoveredReceipt(t, sess, id)
			if sess.notificationExecutionState().recovering || wakes.Load() != 1 || callbacksAtWake.Load() != 0 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("settled retry: recovering=%v wakes=%d retained callbacks at wake=%d requests=%d", sess.notificationExecutionState().recovering, wakes.Load(), callbacksAtWake.Load(), len(adapter.requestsFor("root")))
			}
		})
	}
}
