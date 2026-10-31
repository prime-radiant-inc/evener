package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/execenv"
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
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 {
		t.Fatalf("preceding turn error=%v sync failures=%d, want error at the first failed resolution sync", err, fs.injectedFailures())
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
	faults := &receiptRecoveryFaults{}
	sess, adapter, id := drainingDelegateWithUpdate(t, faults.configure)
	recoverReceiptThroughDrain(t, sess, id, faults, func() []llm.Request { return adapter.requestsFor("root") })
}

type receiptRecoveryFaults struct {
	failing      atomic.Pointer[rootAttentionResolutionFailFS]
	readFailures atomic.Int32
}

func (faults *receiptRecoveryFaults) configure(cfg *SessionConfig) {
	cfg.testOnly.delegateAttentionReadFold = faults.readFold
}

func (faults *receiptRecoveryFaults) readFold(path, sessionID string) (delegateAttentionFold, error) {
	if fs := faults.failing.Load(); fs != nil && fs.injectedFailures() == 2 && faults.readFailures.CompareAndSwap(0, 1) {
		return delegateAttentionFold{}, errors.New("transient transcript read failure")
	}
	return readDelegateAttentionFold(path, sessionID)
}

func recoverReceiptThroughDrain(t *testing.T, sess *Session, id string, faults *receiptRecoveryFaults, requests func() []llm.Request) {
	t.Helper()
	fs := installResolutionSyncFailuresWriter(t, sess, 2)
	faults.failing.Store(fs)
	before := len(requests())
	// No clock advance or external hint drives this drain.
	// TRIPWIRE: the receiver's local retry should finish well before this ceiling.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || faults.readFailures.Load() != 0 {
		t.Fatalf("response error=%v sync failures=%d read failures=%d, want error at the first failed resolution sync before read failure", err, fs.injectedFailures(), faults.readFailures.Load())
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
	t.Logf("drain answer=%q err=%v extra turns=%d requests=%d sync failures=%d read failures=%d", answer, err, turns, len(requests())-before, fs.injectedFailures(), faults.readFailures.Load())
	// Read the durable result even on a timeout, so RED distinguishes a lost
	// progress wake from failed recovery or a duplicate provider execution.
	requireRecoveredReceipt(t, sess, id)
	if err != nil || answer != "" || turns != 0 || len(requests()) != before+1 || fs.injectedFailures() != 2 || faults.readFailures.Load() != 1 {
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
	faults := &receiptRecoveryFaults{}
	root, err := RestoreSessionFromMetaWithConfig(
		fixture.client,
		fixture.profile,
		execenv.NewLocalExecutionEnvironment(fixture.workspace),
		fixture.meta,
		RestoreSessionConfig{
			StateDir:    fixture.stateDir,
			ForceRealIO: true,
			testOnly: testConfig{
				skipGitSnapshot:            true,
				minimalSystemPrompt:        true,
				sandboxProber:              bwrapCapableProber(fixture.workspace),
				disableDelegateIdleRelease: true,
				delegateAttentionReadFold:  faults.readFold,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	warmStableDelegateUnservedRoot(t, root, fixture)
	pending := waitForRootAttention(t, root, 1)
	fold := rootAttentionFold(t, root)
	if !strings.Contains(fold.content[pending[0]].Text(), fixture.delegateID) || !strings.Contains(fold.content[pending[0]].Text(), "OWNED_SOURCE_913") {
		t.Fatalf("actual owned delegate did not supply the source: %+v", fold.content)
	}
	recoverReceiptThroughDrain(t, root, pending[0], faults, fixture.adapter.Requests)
}

// Every negative timer assertion joins the fake clock's callbacks. No sleep,
// polling loop, manufactured notification or new observation drives recovery.
func TestNotificationReceiptRetryCompletion(t *testing.T) {
	for _, mode := range []string{"stale", "stop", "close", "failed_repair"} {
		t.Run(mode, func(t *testing.T) {
			var logged diagnosticLogCapture
			var sessionID string
			checkLogs := func(wantWarning bool) {
				t.Helper()
				type logRecord struct {
					Level     string `json:"level"`
					SessionID string `json:"session_id"`
					Error     string `json:"error"`
				}
				var records []logRecord
				decoder := json.NewDecoder(strings.NewReader(logged.String()))
				for {
					var record logRecord
					if err := decoder.Decode(&record); errors.Is(err, io.EOF) {
						break
					} else if err != nil {
						t.Errorf("decode daemon log: %v", err)
						return
					}
					records = append(records, record)
				}
				if wantWarning {
					if len(records) != 1 || records[0].Level != slog.LevelWarn.String() || records[0].SessionID != sessionID || records[0].Error == "" {
						t.Errorf("daemon records = %+v, want exactly one WARN for session %s with a nonempty error", records, sessionID)
					}
				} else if len(records) != 0 {
					t.Errorf("unexpected daemon records: %+v", records)
				}
			}
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
			t.Cleanup(func() {
				defer slog.SetDefault(previous)
				checkLogs(mode == "failed_repair")
			})
			clock := agenttest.NewFakeClock()
			sess, adapter, id := drainingDelegateWithUpdate(t, func(cfg *SessionConfig) { cfg.clock = clock })
			sessionID = sess.ID()
			failures := 1
			if mode == "failed_repair" {
				failures = 2
			}
			fs := installResolutionSyncFailuresWriter(t, sess, failures)
			// TRIPWIRE: an in-process scripted receipt must not wait for time.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := sess.ProcessInputKind(ctx, "", nil, EntryNotification); err == nil || fs.injectedFailures() != 1 || len(adapter.requestsFor("root")) != 1 {
				t.Fatalf("response error=%v sync failures=%d requests=%d, want error at the first failed resolution sync and one request", err, fs.injectedFailures(), len(adapter.requestsFor("root")))
			}
			checkLogs(false)
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
				if fs.injectedFailures() != 2 || !sess.notificationExecutionState().recovering || wakes.Load() != 0 || len(adapter.requestsFor("root")) != 1 {
					t.Fatalf("failed repair did not re-arm without wake: failures=%d recovering=%v wakes=%d requests=%d", fs.injectedFailures(), sess.notificationExecutionState().recovering, wakes.Load(), len(adapter.requestsFor("root")))
				}
				checkLogs(true)
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
