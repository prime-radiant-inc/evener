package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// A competing source pump may transfer firing custody, but cannot take the
// callback producer's co-produced budget notice or bypass its completion hold.
func TestWatchCallbackBatchOwnsBudgetDuringSourcePump(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			testWatchCallbackBatchOwnsBudgetDuringSourcePump(t, persistent)
		})
	}
}

// Reuse the existing signal/tripwire helper for every gate and join, including
// cleanup, without borrowing its other callers' wider deadlines.
func callbackBatchAwait(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	// TRIPWIRE: actual shell, journal and pump signals normally arrive within milliseconds, only a hang reaches the original five-second ceiling.
	awaitWithin(t, 5*time.Second, what, func() { <-signal })
}

func testWatchCallbackBatchOwnsBudgetDuringSourcePump(t *testing.T, persistent bool) {
	s, adapter := newTerminalFlushSession(t, persistent)
	jm := s.jobManager
	gate := filepath.Join(t.TempDir(), "release")
	entered := make(chan struct{})
	var gated jobstore.WatchSendState
	resume := make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'released\\n'")
	done, liveJob := shellDoneChannel(jm, jobID)
	// Register release and a bounded actual-finalization join before any fatal.
	defer func() {
		release()
		if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
			t.Error(err)
		}
		callbackBatchAwait(t, done, "shell finalization cleanup")
	}()
	if !liveJob {
		t.Fatal("gated shell is not running")
	}
	watch, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}})
	if err != nil {
		t.Fatal(err)
	}
	var gateOnce sync.Once
	jm.mu.Lock()
	_, cfg, found := jm.watchConfigByIDLocked(watch.WatchID)
	if found {
		cfg.conditionFires, cfg.deliveries = watchDeliveryBudget-1, watchDeliveryBudget-1
		cfg.testOnlyAfterCallbackPending = func(state jobstore.WatchSendState) {
			if state.TriggerReason == "event: JOB_FINISHED" {
				gateOnce.Do(func() { gated = state; close(entered); <-resume })
			}
		}
	}
	jm.mu.Unlock()
	if !found {
		t.Fatal("watch missing")
	}
	var wakes atomic.Int32
	woke := make(chan struct{}, 1)
	var wakeMu sync.Mutex
	var handoffs [][]string
	var handoffErrors []error
	s.SetNotifyFunc(func() {
		ids, err := s.pendingNotificationIDs()
		wakeMu.Lock()
		handoffs = append(handoffs, ids)
		if err != nil {
			handoffErrors = append(handoffErrors, err)
		}
		wakeMu.Unlock()
		wakes.Add(1)
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	callbackBatchAwait(t, entered, "actual callback post-persistence gate")
	jm.mu.Lock()
	_, current, live := jm.watchConfigByIDLocked(watch.WatchID)
	fires, tripped, pending, rejecting := cfg.conditionFires, cfg.budgetTripped, len(cfg.pending), cfg.rejectingDelivery
	jm.mu.Unlock()
	s.pendingJobNotifsMu.Lock()
	holds := s.notifyWakeHolds
	s.pendingJobNotifsMu.Unlock()
	if !live || current != cfg || fires != watchDeliveryBudget || !tripped || pending != 1 || rejecting || holds == 0 {
		t.Fatalf("producer gate state: live=%v sameConfig=%v fires=%d tripped=%v pending=%d rejecting=%v completionHolds=%d", live, current == cfg, fires, tripped, pending, rejecting, holds)
	}
	source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(source) != 1 || source[0].WatchSend.DeliveryID != gated.DeliveryID || source[0].WatchSend.UpdateSeq != gated.UpdateSeq || gated.TriggerReason != "event: JOB_FINISHED" {
		t.Fatalf("gate did not observe the actual final source firing: gated=%+v source=%+v", gated, source)
	}
	if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("premature receiver custody: ids=%v err=%v", ids, err)
	}
	t.Logf("actual producer gated: firing=%s:%d live=%v fires=%d tripped=%v pending=%d rejecting=%v completionHolds=%d", gated.DeliveryID, gated.UpdateSeq, live, fires, tripped, pending, rejecting, holds)
	// The same pump is used by another session event's tail and source retry.
	pumpDone := make(chan struct{})
	defer func() {
		release()
		callbackBatchAwait(t, pumpDone, "competing source pump cleanup")
	}()
	go func() { defer close(pumpDone); jm.dispatchProducedWatchSends() }()
	callbackBatchAwait(t, pumpDone, "competing source pump")
	ids, err := s.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("competing source pump returned: receiver=%v earlyRawWakes=%d", ids, wakes.Load())
	if source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending); len(source) != 1 {
		t.Errorf("competing source pump produced callback-owned source rows=%d, want only the final firing", len(source))
	}
	if wakes.Load() != 0 {
		t.Errorf("competing source pump emitted %d raw wakes during completion hold, want0", wakes.Load())
	}
	release()
	callbackBatchAwait(t, done, "shell finalization")
	callbackBatchAwait(t, woke, "final handoff")
	source = producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(source) != 2 {
		t.Fatalf("source rows=%d want2", len(source))
	}
	firing, budget := *source[0].WatchSend, *source[1].WatchSend
	if firing.DeliveryID != gated.DeliveryID || firing.UpdateSeq != gated.UpdateSeq || budget.TriggerReason != "budget_exhausted" || firing.Key == budget.Key || firing.DeliveryID == budget.DeliveryID {
		t.Fatalf("lost distinct final firing/budget identities: firing=%+v budget=%+v", firing, budget)
	}
	record := loadShellRecord(t, jm, jobID)
	want := []string{fmt.Sprintf("watch:%s:%d", firing.DeliveryID, firing.UpdateSeq), fmt.Sprintf("watch:%s:%d", budget.DeliveryID, budget.UpdateSeq), "shell:" + jobID + ":" + record.TerminalGen}
	ids, err = s.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !equalNotificationIDs(ids, want) {
		t.Errorf("receiver order=%v, want firing,budget,shell=%v", ids, want)
	}
	wakeMu.Lock()
	if len(handoffErrors) != 0 || len(handoffs) != 1 || !equalNotificationIDs(handoffs[0], want) {
		t.Errorf("handoff IDs=%v errors=%v, want one final ordered handoff=%v", handoffs, handoffErrors, want)
	}
	wakeMu.Unlock()
	if wakes.Load() != 1 {
		t.Errorf("raw wakes=%d want1", wakes.Load())
	}
	for _, state := range []jobstore.WatchSendState{firing, budget} {
		requireWatchSettlement(t, jm, state, 1)
		requireCallbackBatchSource(t, s, state)
	}
	if jm.watchCount() != 0 {
		t.Error("budget watch remains live")
	}
	requireCallbackBatchReceipts(t, s, want, 0)
	adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
		requireCallbackBatchRequest(t, s, request, want)
		requireCallbackBatchReceipts(t, s, want, 0)
		return finalResponse("callback-pump-response-918")
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(adapter.Requests()) != 1 {
		t.Errorf("actual provider requests=%d want1", len(adapter.Requests()))
	}
	requireCallbackBatchReceipts(t, s, want, 1)
	if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
		t.Errorf("pending after successful response=%v err=%v", ids, err)
	}
	t.Logf("completed proof: source=[firing,budget] receiver=%v rawWakes=%d actualProviderRequests=%d each ID observed once after successful response", ids, wakes.Load(), len(adapter.Requests()))
}

func callbackBatchWatch(t *testing.T, jm *jobManager, prior int) *watchConfig {
	t.Helper()
	watch, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	_, cfg, found := jm.watchConfigByIDLocked(watch.WatchID)
	if found {
		cfg.conditionFires, cfg.deliveries = prior, prior
	}
	jm.mu.Unlock()
	if !found {
		t.Fatal("callback watch missing")
	}
	return cfg
}

func callbackBatchStart(t *testing.T, release func(), work func()) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() {
		release()
		callbackBatchAwait(t, done, "callback producer cleanup")
	})
	go func() { defer close(done); work() }()
	return done
}

func requireCallbackBatchSourceRequest(t *testing.T, request llm.Request, states ...jobstore.WatchSendState) {
	t.Helper()
	// Different source IDs may carry identical text. Check exact source-payload
	// multiplicity here, while the independent transcript checks distinguish IDs.
	wantPayloads := make(map[string]int)
	for _, state := range states {
		if state.Frame == "" {
			t.Fatalf("source-backed ID %s has no frame", state.DeliveryID)
		}
		wantPayloads[state.Frame]++
	}
	for text, want := range wantPayloads {
		matches := 0
		for _, message := range request.Messages {
			if message.Role == llm.RoleUser && strings.Contains(message.Text(), text) {
				matches++
			}
		}
		if matches != want {
			t.Fatalf("provider request contains source-backed payload %d times, want%d", matches, want)
		}
	}
}

func TestWatchCallbackBatchEarlierProducerCannotTakeBudget(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		for _, olderReturnsAt := range []string{"final-firing", "nested-notice"} {
			t.Run(fmt.Sprintf("persistent=%t/%s", persistent, olderReturnsAt), func(t *testing.T) {
				s, adapter := newTerminalFlushSession(t, persistent)
				jm := s.jobManager
				cfg := callbackBatchWatch(t, jm, watchDeliveryBudget-2)
				olderEntered, finalEntered, noticeEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
				olderResume, finalResume, noticeResume := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseOlder := sync.OnceFunc(func() { close(olderResume) })
				releaseFinal := sync.OnceFunc(func() { close(finalResume) })
				releaseNotice := sync.OnceFunc(func() { close(noticeResume) })
				releaseAll := func() { releaseOlder(); releaseFinal(); releaseNotice() }
				var older, final, nested jobstore.WatchSendState
				jm.mu.Lock()
				cfg.testOnlyAfterCallbackPending = func(state jobstore.WatchSendState) {
					switch state.UpdateSeq {
					case 1:
						older = state
						close(olderEntered)
						<-olderResume
					case 2:
						final = state
						close(finalEntered)
						<-finalResume
					}
				}
				jm.mu.Unlock()
				appendEvent := jm.appendEvent
				jm.appendEvent = func(event jobstore.Event) error {
					if event.Kind == jobstore.EventWatchSendPending && event.WatchSend.TriggerReason == watchBudgetEndTrigger {
						nested = *event.WatchSend
						close(noticeEntered)
						<-noticeResume
					}
					return appendEvent(event)
				}
				var wakes atomic.Int32
				s.SetNotifyFunc(func() { wakes.Add(1) })
				releaseWake := sync.OnceFunc(s.holdJobNotificationWake())
				defer releaseWake()
				fire := func() {
					onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "callback-owner-event-613"})
				}
				olderDone := callbackBatchStart(t, releaseAll, fire)
				callbackBatchAwait(t, olderEntered, "older actual source commit")
				finalDone := callbackBatchStart(t, releaseAll, fire)
				callbackBatchAwait(t, finalEntered, "final actual source commit")
				// The second event's pre-production pump accepted the older ID
				// before recording its own firing. That custody must survive too.
				olderID, finalID := stableWatchAttentionID(older), stableWatchAttentionID(final)
				if ids, err := s.pendingNotificationIDs(); err != nil || !equalNotificationIDs(ids, []string{olderID}) {
					t.Fatalf("second event did not retain earlier firing custody: ids=%v err=%v", ids, err)
				}
				if olderReturnsAt == "nested-notice" {
					releaseFinal()
					callbackBatchAwait(t, noticeEntered, "nested budget append entry")
				}
				releaseOlder()
				callbackBatchAwait(t, olderDone, "older producer return")
				before := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(before) != 2 || before[0].WatchSend.DeliveryID != older.DeliveryID || before[1].WatchSend.DeliveryID != final.DeliveryID || older.DeliveryID == final.DeliveryID {
					t.Fatalf("older/final source identities changed: %+v", before)
				}
				if ids, err := s.pendingNotificationIDs(); err != nil || !equalNotificationIDs(ids, []string{olderID, finalID}) {
					t.Fatalf("older return did not preserve final firing custody: ids=%v err=%v", ids, err)
				}
				if olderReturnsAt == "final-firing" {
					if jm.watchCount() != 1 {
						t.Fatal("older producer took the final producer's budget teardown")
					}
					releaseFinal()
					callbackBatchAwait(t, noticeEntered, "nested budget append entry")
				}
				// Source IDs and custody above are independent of this implementation
				// invariant. The nested append has not yet committed its distinct ID.
				jm.mu.Lock()
				owner := cfg.callbackBudgetOwner
				jm.mu.Unlock()
				if owner != final.DeliveryID || nested.DeliveryID == final.DeliveryID || nested.Key == final.Key {
					t.Fatalf("nested source tail lost exact owner: owner=%s final=%s notice=%s", owner, final.DeliveryID, nested.DeliveryID)
				}
				if wakes.Load() != 0 {
					t.Fatalf("earlier/nested production emitted %d raw wakes under hold", wakes.Load())
				}
				releaseNotice()
				callbackBatchAwait(t, finalDone, "final producer with nested notice")
				after := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(after) != 3 || after[2].WatchSend.DeliveryID != nested.DeliveryID {
					t.Fatalf("nested notice source identity changed: %+v", after)
				}
				want := []string{olderID, finalID, stableWatchAttentionID(nested)}
				if ids, err := s.pendingNotificationIDs(); err != nil || !equalNotificationIDs(ids, want) {
					t.Fatalf("final/nested receiver order=%v want=%v err=%v", ids, want, err)
				}
				for _, state := range []jobstore.WatchSendState{older, final, nested} {
					requireWatchSettlement(t, jm, state, 1)
					requireCallbackBatchSource(t, s, state)
				}
				releaseWake()
				if wakes.Load() != 1 || jm.watchCount() != 0 {
					t.Fatalf("wakes=%d live=%d, want one handoff and exhausted registration", wakes.Load(), jm.watchCount())
				}
				adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
					requireCallbackBatchSourceRequest(t, request, older, final, nested)
					requireCallbackBatchReceipts(t, s, want, 0)
					return finalResponse("nested-source-response-716")
				}}
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				requireCallbackBatchReceipts(t, s, want, 1)
				if len(adapter.Requests()) != 1 {
					t.Fatalf("actual requests=%d want1", len(adapter.Requests()))
				}
				t.Logf("older source=%s retained before final=%s, nested source=%s, ordered custody=%v one response, older returned at %s", older.DeliveryID, final.DeliveryID, nested.DeliveryID, want, olderReturnsAt)
			})
		}
	}
}

func TestWatchCallbackBatchOwnerFailureUnwind(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		for _, fault := range []string{"firing-append", "teardown", "notice-append"} {
			t.Run(fmt.Sprintf("persistent=%t/%s", persistent, fault), func(t *testing.T) {
				clock := agenttest.NewFakeClock()
				adapter := &fakeAdapter{name: "openai"}
				config := SessionConfig{clock: clock}
				if persistent {
					config.StateDir = t.TempDir()
				}
				s := newSession(t, withAdapter(adapter), withConfig(config))
				jm := s.jobManager
				cfg := callbackBatchWatch(t, jm, watchDeliveryBudget-1)
				var fail atomic.Bool
				fail.Store(true)
				var failures atomic.Int32
				var failed jobstore.WatchSendState
				injected := errors.New("callback source failure")
				appendEvent, appendEvents := jm.appendEvent, jm.appendEvents
				jm.appendEvent = func(event jobstore.Event) error {
					if fail.Load() && event.Kind == jobstore.EventWatchSendPending && ((fault == "firing-append" && callbackFiringReason(event.WatchSend.TriggerReason)) || (fault == "notice-append" && event.WatchSend.TriggerReason == watchBudgetEndTrigger)) {
						failed = *event.WatchSend
						failures.Add(1)
						return injected
					}
					return appendEvent(event)
				}
				jm.appendEvents = func(batch []jobstore.Event) error {
					for _, event := range batch {
						if fail.Load() && fault == "teardown" && event.Kind == jobstore.EventWatchCleared {
							failures.Add(1)
							return injected
						}
					}
					return appendEvents(batch)
				}
				fire := func() {
					onSessionEventKD(jm, events.EventCommunicate, events.CommunicateData{Message: "callback-failure-event-714"})
				}
				fire()
				if failures.Load() == 0 {
					t.Fatal("intended callback source fault never fired")
				}
				jm.mu.Lock()
				owner, fires := cfg.callbackBudgetOwner, cfg.conditionFires
				jm.mu.Unlock()
				if owner != "" {
					t.Fatalf("returned producer retained owner %s after %s failure", owner, fault)
				}
				if fault == "firing-append" && fires != watchDeliveryBudget-1 {
					t.Fatalf("failed source charged condition: fires=%d", fires)
				}
				fail.Store(false)
				if fault == "teardown" {
					fire() // Existing next-condition retry, never condition 51.
				} else {
					clock.Advance(jobNotificationRetryInitialDelay)
					clock.Drain()
				}
				source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(source) != 2 || source[1].WatchSend.TriggerReason != watchBudgetEndTrigger {
					t.Fatalf("failure recovery did not preserve firing plus budget: %+v", source)
				}
				if fault != "teardown" {
					index := 0
					if fault == "notice-append" {
						index = 1
					}
					if source[index].WatchSend.DeliveryID != failed.DeliveryID || source[index].WatchSend.UpdateSeq != failed.UpdateSeq || source[index].WatchSend.Key != failed.Key {
						t.Fatal("recovery changed failed source identity")
					}
				}
				jm.mu.Lock()
				owner, fires = cfg.callbackBudgetOwner, cfg.conditionFires
				jm.mu.Unlock()
				if owner != "" || fires != watchDeliveryBudget || jm.watchCount() != 0 {
					t.Fatalf("recovery owner=%s fires=%d live=%d", owner, fires, jm.watchCount())
				}
				ids, err := s.pendingNotificationIDs()
				if err != nil || len(ids) != 2 {
					t.Fatalf("recovered custody IDs=%v err=%v", ids, err)
				}
				for _, event := range source {
					requireWatchSettlement(t, jm, *event.WatchSend, 1)
					requireCallbackBatchSource(t, s, *event.WatchSend)
				}
				adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
					requireCallbackBatchRequest(t, s, request, ids)
					requireCallbackBatchReceipts(t, s, ids, 0)
					return finalResponse("source-failure-response-246")
				}}
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				requireCallbackBatchReceipts(t, s, ids, 1)
				if len(adapter.Requests()) != 1 || jm.hasPendingWatchSends() {
					t.Fatal("failure recovery duplicated request or retained source debt")
				}
				t.Logf("fault=%s injectedFailures=%d recovered source IDs=%s,%s, custody=%v, fires=%d, one successful response", fault, failures.Load(), source[0].WatchSend.DeliveryID, source[1].WatchSend.DeliveryID, ids, fires)
			})
		}
	}
}

func equalNotificationIDs(got, want []string) bool {
	return slices.Equal(got, want)
}

func TestCompletionProducerDeliveryReservation(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			testCompletionDeliveryOwnership(t, persistent)
		})
	}
}

func testCompletionDeliveryOwnership(t *testing.T, persistent bool) {
	s, adapter := newTerminalFlushSession(t, persistent)
	jm := s.jobManager
	finalGate := filepath.Join(t.TempDir(), "final")
	jobID := startBackgroundShellJob(t, s, "while [ ! -f "+finalGate+" ]; do sleep 0.02; done")
	finalDone, _ := shellDoneChannel(jm, jobID)
	finalEntered, finalResume := make(chan struct{}), make(chan struct{})
	releaseFinal := sync.OnceFunc(func() { close(finalResume) })
	defer func() {
		releaseFinal()
		if err := os.WriteFile(finalGate, nil, 0o600); err != nil {
			t.Error(err)
		}
		callbackBatchAwait(t, finalDone, "final shell cleanup")
	}()
	args := map[string]any{"operation": "create", "source": "self", "events": []any{"job.notification"}}
	if _, err := jobWatchTool(s, args, jobToolResultDefaultMaxChar); err != nil {
		t.Fatal(err)
	}
	var final jobstore.WatchSendState
	var claimsMu sync.Mutex
	var finalClaims []bool
	var finalOnce sync.Once
	jm.mu.Lock()
	var cfg *watchConfig
	for _, candidate := range jm.watches {
		cfg = candidate
	}
	if cfg == nil {
		jm.mu.Unlock()
		t.Fatal("public completion watch missing")
	}
	cfg.conditionFires, cfg.deliveries = watchDeliveryBudget-1, watchDeliveryBudget-1
	cfg.testOnlyAfterSendPending = func(state jobstore.WatchSendState) {
		if state.TriggerReason == "event: JOB_FINISHED" {
			finalOnce.Do(func() { final = state; close(finalEntered); <-finalResume })
		}
	}
	cfg.testOnlyAfterDeliveryClaim = func(state jobstore.WatchSendState, claimed bool) {
		if state.TriggerReason == "event: JOB_FINISHED" {
			claimsMu.Lock()
			finalClaims = append(finalClaims, claimed)
			claimsMu.Unlock()
		}
	}
	jm.mu.Unlock()
	var wakes atomic.Int32
	woke := make(chan struct{}, 1)
	var handoffsMu sync.Mutex
	var handoffs [][]string
	s.SetNotifyFunc(func() {
		ids, err := s.pendingNotificationIDs()
		if err != nil {
			t.Error(err)
		}
		handoffsMu.Lock()
		handoffs = append(handoffs, ids)
		handoffsMu.Unlock()
		wakes.Add(1)
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	if err := os.WriteFile(finalGate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	callbackBatchAwait(t, finalEntered, "actual final F source commit")
	jm.mu.Lock()
	run := jm.running[jobID]
	production := run.finalize.production
	jm.mu.Unlock()
	pumpDone := callbackBatchStart(t, releaseFinal, jm.dispatchProducedWatchSends)
	callbackBatchAwait(t, pumpDone, "competing real pump returns while producer paused")
	ids, err := s.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	claimsMu.Lock()
	attempts := slices.Clone(finalClaims)
	claimsMu.Unlock()
	t.Logf("paused producer: F=%s actual competing claims=%v receiver=%v rawWakes=%d", final.DeliveryID, attempts, ids, wakes.Load())
	var prefix []string
	if len(attempts) == 0 || slices.Contains(attempts, true) {
		t.Errorf("actual competing F claim results=%v, want observed stand-down only", attempts)
	}
	if !slices.Equal(ids, prefix) {
		t.Errorf("paused producer custody=%v, want independent older prefix only=%v", ids, prefix)
	}
	if source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending); len(source) != 1+len(prefix)/2 {
		t.Errorf("competing executor escaped budget source production: rows=%d", len(source))
	}
	if wakes.Load() != 0 {
		t.Errorf("raw wakes while producer held=%d", wakes.Load())
	}
	releaseFinal()
	callbackBatchAwait(t, finalDone, "final completion")
	callbackBatchAwait(t, woke, "complete handoff")
	source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(source) != 2+len(prefix)/2 {
		t.Fatalf("source rows=%d", len(source))
	}
	budget := *source[len(source)-1].WatchSend
	if budget.TriggerReason != watchBudgetEndTrigger || final.Key == budget.Key || final.DeliveryID == budget.DeliveryID {
		t.Fatal("distinct source firing and budget identities lost")
	}
	record := loadShellRecord(t, jm, jobID)
	want := append(slices.Clone(prefix), stableWatchAttentionID(final), stableWatchAttentionID(budget), stableShellAttentionID(jobID, record.TerminalGen))
	ids, err = s.pendingNotificationIDs()
	if err != nil || !slices.Equal(ids, want) {
		t.Errorf("final receiver=%v want=%v err=%v", ids, want, err)
	}
	handoffsMu.Lock()
	if len(handoffs) != 1 || !slices.Equal(handoffs[0], want) {
		t.Errorf("complete raw handoffs=%v want one=%v", handoffs, want)
	}
	handoffsMu.Unlock()
	jm.mu.Lock()
	held, sourceOwner, fires := len(jm.deliveringWatchSends), cfg.callbackBudgetOwner, cfg.conditionFires
	jm.mu.Unlock()
	if held != 0 || sourceOwner != "" || fires != watchDeliveryBudget {
		t.Errorf("completed claims=%d sourceOwner=%s fires=%d", held, sourceOwner, fires)
	}
	if run.terminal.emittingProduction != nil {
		t.Error("completion emission bridge retained")
	}
	if !jm.claimWatchSendDelivery(final.DeliveryID) {
		t.Fatal("completed producer did not release F")
	}
	func() {
		defer jm.releaseWatchSendDeliveryClaim(final.DeliveryID)
		jm.drainWatchDeliveryProduction(production)
		// Repeated old-scope cleanup must not release this successor's claim.
		if jm.claimWatchSendDelivery(final.DeliveryID) {
			t.Error("old lexical cleanup released a successor claim")
		}
	}()
	for _, event := range source {
		requireWatchSettlement(t, jm, *event.WatchSend, 1)
	}
	adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
		requireCallbackBatchRequest(t, s, request, want)
		requireCallbackBatchReceipts(t, s, want, 0)
		return finalResponse("completion-owner-response-921")
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCallbackBatchReceipts(t, s, want, 1)
	if len(adapter.Requests()) != 1 {
		t.Errorf("actual provider requests=%d", len(adapter.Requests()))
	}
	t.Logf("final IDs=%v rawWakes=%d providerRequests=%d", ids, wakes.Load(), len(adapter.Requests()))
}

func TestCompletionTerminalDeliveryReservation(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		for _, send := range []bool{false, true} {
			t.Run(fmt.Sprintf("persistent=%t/send=%t", persistent, send), func(t *testing.T) {
				s, adapter := newTerminalFlushSession(t, persistent)
				jm := s.jobManager
				gate := filepath.Join(t.TempDir(), "terminal")
				jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done")
				done, _ := shellDoneChannel(jm, jobID)
				entered, resume := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(resume) })
				delayed, allowHandoff := make(chan struct{}), make(chan struct{})
				releaseHandoff := sync.OnceFunc(func() { close(allowHandoff) })
				var handoffsDone <-chan struct{}
				defer func() {
					release()
					releaseHandoff()
					if err := os.WriteFile(gate, nil, 0o600); err != nil {
						t.Error(err)
					}
					callbackBatchAwait(t, done, "terminal source cleanup")
					if handoffsDone != nil {
						callbackBatchAwait(t, handoffsDone, "terminal callback cleanup after its handoff record")
					}
				}()
				args := watchArgs{Target: jobID, OutputMatch: "unmatched-terminal-962"}
				if send {
					// Exercise the existing concrete terminal-send contract, not a
					// claim about public wildcard/send_to creation support.
					args.Send = &watchSendArgs{To: runtimeMessageAliasCaller}
				}
				watch, err := jm.configureWatch(args)
				if err != nil {
					t.Fatal(err)
				}
				var terminal jobstore.WatchSendState
				var mu sync.Mutex
				var attempts []bool
				jm.mu.Lock()
				_, cfg, found := jm.watchConfigByIDLocked(watch.WatchID)
				if found {
					cfg.testOnlyAfterSendPending = func(state jobstore.WatchSendState) {
						terminal = state
						close(entered)
						<-resume
					}
					cfg.testOnlyAfterDeliveryClaim = func(_ jobstore.WatchSendState, claimed bool) {
						mu.Lock()
						attempts = append(attempts, claimed)
						mu.Unlock()
					}
				}
				jm.mu.Unlock()
				if !found {
					t.Fatal("terminal watch missing")
				}
				var wakes atomic.Int32
				var handoffs [][]string
				wantHandoffs := int32(1)
				if send {
					wantHandoffs = 2
				}
				callbackDone := make(chan struct{})
				handoffsDone = callbackDone
				var callbacksStarted atomic.Int32
				s.SetNotifyFunc(func() {
					last := callbacksStarted.Add(1) == wantHandoffs
					if last {
						// A test-only wrapper delays the final raw callback after
						// run.done, before its handoff is recorded. No runtime seam.
						close(delayed)
						<-allowHandoff
					}
					ids, err := s.pendingNotificationIDs()
					if err != nil {
						t.Error(err)
					}
					mu.Lock()
					handoffs = append(handoffs, ids)
					mu.Unlock()
					wakes.Add(1)
					if last {
						close(callbackDone)
					}
				})
				if err := os.WriteFile(gate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				callbackBatchAwait(t, entered, "actual terminal member commit")
				pump := callbackBatchStart(t, release, jm.dispatchProducedWatchSends)
				callbackBatchAwait(t, pump, "terminal competing pump returns")
				mu.Lock()
				got := slices.Clone(attempts)
				mu.Unlock()
				if len(got) == 0 || slices.Contains(got, true) {
					t.Errorf("terminal claim attempts=%v, want stand-down", got)
				}
				if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
					t.Errorf("terminal premature custody=%v err=%v", ids, err)
				}
				if terminal.CallbackFrame == send || (send && !terminal.EndNotice) {
					t.Errorf("wrong terminal route: %+v", terminal)
				}
				if wakes.Load() != 0 {
					t.Errorf("terminal early wakes=%d", wakes.Load())
				}
				release()
				callbackBatchAwait(t, done, "terminal finalization")
				callbackBatchAwait(t, delayed, "final terminal callback deliberately paused before handoff")
				joining := make(chan struct{})
				joined := callbackBatchStart(t, releaseHandoff, func() {
					close(joining)
					callbackBatchAwait(t, callbackDone, "required terminal callbacks recorded")
				})
				callbackBatchAwait(t, joining, "terminal assertion barrier entered")
				select {
				case <-joined:
					t.Fatal("terminal assertion barrier passed a delayed handoff")
				default:
				}
				if wakes.Load() != wantHandoffs-1 {
					t.Fatalf("delayed terminal wakes=%d want=%d before final handoff", wakes.Load(), wantHandoffs-1)
				}
				t.Logf("run.done closed, final callback gated, recordedHandoffs=%d required=%d assertionBarrierBlocked=true", wakes.Load(), wantHandoffs)
				releaseHandoff()
				callbackBatchAwait(t, joined, "terminal assertions join all handoff records")
				source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(source) != 1 || source[0].WatchSend.DeliveryID != terminal.DeliveryID {
					t.Fatalf("terminal source=%+v", source)
				}
				want := []string{stableWatchAttentionID(terminal), stableShellAttentionID(jobID, loadShellRecord(t, jm, jobID).TerminalGen)}
				ids, err := s.pendingNotificationIDs()
				// Preserve the concrete-send legacy flush kick, in addition to the
				// held canonical wake. Neither may precede E and SHELL custody.
				wantWakes := int32(1)
				if send {
					wantWakes = 2
				}
				if err != nil || !slices.Equal(ids, want) || wakes.Load() != wantWakes {
					t.Fatalf("terminal custody=%v want=%v wakes=%d wantWakes=%d err=%v", ids, want, wakes.Load(), wantWakes, err)
				}
				mu.Lock()
				for _, handoff := range handoffs {
					if !slices.Equal(handoff, want) {
						t.Errorf("incomplete terminal handoff=%v want=%v", handoff, want)
					}
				}
				mu.Unlock()
				requireWatchSettlement(t, jm, terminal, 1)
				requireCompletionClaimsReleased(t, jm)
				adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
					requireCallbackBatchRequest(t, s, request, want)
					return finalResponse("terminal-reservation-response-963")
				}}
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				requireCallbackBatchReceipts(t, s, want, 1)
				if len(adapter.Requests()) != 1 {
					t.Fatal("terminal request duplicated")
				}
				t.Logf("terminal send=%t attemptedClaims=%v receiver=%v wakes=%d providerRequests=1", send, got, ids, wakes.Load())
			})
		}
	}
}

func requireCompletionClaimsReleased(t *testing.T, jm *jobManager) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.deliveringWatchSends) != 0 {
		t.Errorf("residual completion claims=%v", jm.deliveringWatchSends)
	}
	for _, run := range jm.running {
		if run.terminal != nil && run.terminal.emittingProduction != nil {
			t.Error("emission bridge survived producer return")
		}
	}
}

func TestCompletionDeliveryLifecycleCleanup(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		for _, action := range []string{"stop", "close", "close-teardown-error", "abandon-one", "abandon-all", "supersede"} {
			t.Run(fmt.Sprintf("persistent=%t/%s", persistent, action), func(t *testing.T) {
				s, _ := newTerminalFlushSession(t, persistent)
				jm := s.jobManager
				gate := filepath.Join(t.TempDir(), "lifecycle")
				jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done")
				done, _ := shellDoneChannel(jm, jobID)
				entered, resume := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(resume) })
				var attemptDone <-chan struct{}
				defer func() {
					release()
					if err := os.WriteFile(gate, nil, 0o600); err != nil {
						t.Error(err)
					}
					callbackBatchAwait(t, done, "lifecycle shell cleanup")
					if attemptDone != nil {
						callbackBatchAwait(t, attemptDone, "entered producer cleanup, even after abandonment")
					}
				}()
				watch, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"job.notification"}})
				if err != nil {
					t.Fatal(err)
				}
				var first jobstore.WatchSendState
				var observed atomic.Bool
				jm.mu.Lock()
				_, cfg, _ := jm.watchConfigByIDLocked(watch.WatchID)
				cfg.testOnlyAfterSendPending = func(state jobstore.WatchSendState) {
					if observed.CompareAndSwap(false, true) {
						first = state
						close(entered)
						<-resume
					}
				}
				jm.mu.Unlock()
				if err := os.WriteFile(gate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				callbackBatchAwait(t, entered, "entered lifecycle producer owns F")
				jm.mu.Lock()
				run := jm.running[jobID]
				attemptDone = run.finalize.done
				jm.mu.Unlock()
				var teardownFailures atomic.Int32
				switch action {
				case "stop":
					if _, err := jm.stop(jobID); err != nil {
						t.Fatal(err)
					}
				case "abandon-one":
					jm.abandonRunningJob(jobID)
				case "abandon-all":
					jm.abandonRunningJobs()
				case "close", "close-teardown-error":
					appendEvents := jm.appendEvents
					if action == "close-teardown-error" {
						jm.appendEvents = func(batch []jobstore.Event) error {
							for _, event := range batch {
								if event.Kind == jobstore.EventWatchCleared && teardownFailures.CompareAndSwap(0, 1) {
									return errors.New("controlled close teardown failure")
								}
							}
							return appendEvents(batch)
						}
					}
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					if err := jm.closeRuntimeState(ctx); err == nil {
						t.Fatal("close must report unfinished entered producer")
					}
					if got := runningJobCompletion(run.completion.Load()); got != runningJobCompletionAbandoned {
						t.Fatalf("close completion=%v, want abandoned before producer resumes", got)
					}
					select {
					case <-done:
					default:
						t.Fatal("bounded close did not settle abandoned job")
					}
					select {
					case <-attemptDone:
						t.Fatal("entered producer returned before its source gate opened")
					default:
					}
					if action == "close-teardown-error" && teardownFailures.Load() != 1 {
						t.Fatal("close teardown failure did not fire")
					}
				case "supersede":
					// Another real shell completion coalesces the same fixed watch
					// key. Its producer and SHELL must finish while F stays paused.
					otherGate := filepath.Join(t.TempDir(), "other")
					otherID := startBackgroundShellJob(t, s, "while [ ! -f "+otherGate+" ]; do sleep 0.02; done")
					otherDone, _ := shellDoneChannel(jm, otherID)
					defer func() {
						if err := os.WriteFile(otherGate, nil, 0o600); err != nil {
							t.Error(err)
						}
						callbackBatchAwait(t, otherDone, "superseding shell own gate release and cleanup")
					}()
					if err := os.WriteFile(otherGate, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					callbackBatchAwait(t, otherDone, "independent real completion while first producer paused")
					ids, err := s.pendingNotificationIDs()
					if err != nil || len(ids) != 2 || slices.Contains(ids, stableWatchAttentionID(first)) {
						t.Fatalf("supersession custody=%v err=%v", ids, err)
					}
				}
				if jm.claimWatchSendDelivery(first.DeliveryID) {
					jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
					t.Error("lifecycle revoked an entered producer claim")
				}
				release()
				callbackBatchAwait(t, attemptDone, "entered lifecycle producer unwinds")
				requireCompletionClaimsReleased(t, jm)
				if run.terminal.emittingProduction != nil {
					t.Error("removed run retained emission bridge")
				}
				if !jm.claimWatchSendDelivery(first.DeliveryID) {
					t.Error("lifecycle leaked F claim")
				} else {
					jm.releaseWatchSendDeliveryClaim(first.DeliveryID)
				}
				wantCompletion := runningJobCompletionDurable
				if action == "close" || action == "close-teardown-error" || strings.HasPrefix(action, "abandon-") {
					wantCompletion = runningJobCompletionAbandoned
				}
				if got := runningJobCompletion(run.completion.Load()); got != wantCompletion {
					t.Errorf("lifecycle completion=%v want=%v", got, wantCompletion)
				}
				t.Logf("action=%s producer returned, claims released, completion=%v teardownFailures=%d", action, wantCompletion, teardownFailures.Load())
			})
		}
	}
}

func TestCompletionDeliveryFailureRecovery(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		for _, fault := range []string{"firing-append", "notice-append", "teardown", "source-ack", "receiver-sync"} {
			if fault == "receiver-sync" && !persistent {
				continue
			} // No receiver filesystem exists in memory-only mode.
			t.Run(fmt.Sprintf("persistent=%t/%s", persistent, fault), func(t *testing.T) {
				clock := agenttest.NewFakeClock()
				config := SessionConfig{clock: clock}
				if persistent {
					config.StateDir = t.TempDir()
				}
				adapter := &fakeAdapter{name: "openai"}
				s := newSession(t, withAdapter(adapter), withConfig(config))
				jm := s.jobManager
				var fs *observationRetainedFS
				if fault == "receiver-sync" {
					fs = observationRetainedWriter(t, s, "watch:")
				}
				gate := filepath.Join(t.TempDir(), "failure")
				jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done")
				done, _ := shellDoneChannel(jm, jobID)
				entered, resume := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(resume) })
				var recovering atomic.Bool
				defer func() {
					recovering.Store(true)
					if fs != nil {
						fs.mu.Lock()
						fs.failing = false
						fs.target = nil
						fs.mu.Unlock()
					}
					release()
					if err := os.WriteFile(gate, nil, 0o600); err != nil {
						t.Error(err)
					}
					callbackBatchAwait(t, done, "failure shell cleanup")
				}()
				if _, err := jobWatchTool(s, map[string]any{"operation": "create", "source": "self", "events": []any{"job.notification"}}, jobToolResultDefaultMaxChar); err != nil {
					t.Fatal(err)
				}
				jm.mu.Lock()
				var cfg *watchConfig
				for _, c := range jm.watches {
					cfg = c
				}
				cfg.conditionFires, cfg.deliveries = watchDeliveryBudget-1, watchDeliveryBudget-1
				jm.mu.Unlock()
				var failures atomic.Int32
				var appendAttempts atomic.Int32
				appendFault := fault == "firing-append" || fault == "notice-append"
				var diagnosticsMu sync.Mutex
				var diagnostics []jobNotification
				enqueue := jm.enqueue
				jm.enqueue = func(n jobNotification) {
					if n.Kind == jobNotificationKindWatch && n.OriginWatchID == cfg.watchID && n.callbackConfig == nil {
						diagnosticsMu.Lock()
						diagnostics = append(diagnostics, n)
						diagnosticsMu.Unlock()
					}
					enqueue(n)
				}
				appendEvent := jm.appendEvent
				jm.appendEvent = func(event jobstore.Event) error {
					match := false
					switch fault {
					case "firing-append":
						match = event.Kind == jobstore.EventWatchSendPending && event.WatchSend.TriggerReason == "event: JOB_FINISHED"
					case "notice-append":
						match = event.Kind == jobstore.EventWatchSendPending && event.WatchSend.TriggerReason == watchBudgetEndTrigger
					case "teardown":
						match = event.Kind == jobstore.EventWatchCleared
					case "source-ack":
						match = event.Kind == jobstore.EventWatchSendDelivered
					}
					if match {
						appendAttempts.Add(1)
					}
					if match && !recovering.Load() && (appendFault || fault == "source-ack" || failures.Load() == 0) {
						failures.Add(1)
						return errors.New("controlled completion " + fault)
					}
					return appendEvent(event)
				}
				appendEvents := jm.appendEvents
				jm.appendEvents = func(batch []jobstore.Event) error {
					for _, event := range batch {
						if fault == "teardown" && event.Kind == jobstore.EventWatchCleared && failures.CompareAndSwap(0, 1) {
							return errors.New("controlled completion teardown")
						}
					}
					return appendEvents(batch)
				}
				jm.testOnlyAfterNotifyPendingAppend = func(id string) {
					if id == jobID {
						close(entered)
						<-resume
					}
				}
				if err := os.WriteFile(gate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				callbackBatchAwait(t, entered, "completion emission drained before shell custody")
				requireCompletionClaimsReleased(t, jm)
				var failedMember jobstore.WatchSendState
				var attemptDone <-chan struct{}
				if appendFault {
					pending := jm.pendingWatchSendDeliveries(nil)
					wantTrigger, wantSource, wantFires := "event: JOB_FINISHED", 0, watchDeliveryBudget-1
					if fault == "notice-append" {
						wantTrigger, wantSource, wantFires = watchBudgetEndTrigger, 1, watchDeliveryBudget
					}
					if len(pending) != 1 || pending[0].state.TriggerReason != wantTrigger {
						t.Fatalf("pre-SHELL exact append debt=%+v want one %s", pending, wantTrigger)
					}
					failedMember = pending[0].state
					fold, err := jm.store.LoadWatchSends()
					if err != nil || fold.Pending[failedMember.Key] != nil {
						t.Fatalf("failed member unexpectedly journaled: fold=%+v err=%v", fold, err)
					}
					before := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
					var wantCustody []string
					if wantSource == 1 && len(before) == 1 {
						wantCustody = []string{stableWatchAttentionID(*before[0].WatchSend)}
					}
					ids, err := s.pendingNotificationIDs()
					if err != nil || len(before) != wantSource || !slices.Equal(ids, wantCustody) {
						t.Fatalf("pre-SHELL source=%+v custody=%v want=%v err=%v", before, ids, wantCustody, err)
					}
					jm.mu.Lock()
					fires, retryArmed := cfg.conditionFires, jm.watchTransportRetry.active && jm.watchTransportRetryTimer != nil
					attemptDone = jm.running[jobID].finalize.done
					jm.mu.Unlock()
					if fires != wantFires || !retryArmed || failures.Load() != 2 || appendAttempts.Load() != 2 {
						t.Fatalf("pre-SHELL fires=%d want=%d retryArmed=%t failures=%d appendAttempts=%d", fires, wantFires, retryArmed, failures.Load(), appendAttempts.Load())
					}
					diagnosticsMu.Lock()
					gotDiagnostics := slices.Clone(diagnostics)
					diagnosticsMu.Unlock()
					if len(gotDiagnostics) != 2 {
						t.Fatalf("injected append diagnostics=%+v, want initial source failure and pump retry failure", gotDiagnostics)
					}
					for _, diagnostic := range gotDiagnostics {
						if diagnostic.Reason != "watch send pending state failed: controlled completion "+fault || diagnostic.JobID != failedMember.Key.ResolvedWatchedIdentity || diagnostic.WatchID != "" || diagnostic.Terminal {
							t.Fatalf("injected append diagnostic=%+v", diagnostic)
						}
					}
					t.Logf("pre-SHELL fault=%s failures=%d appendAttempts=%d debt=%s source=%d custody=%v retryArmed=%t diagnostics=%d", fault, failures.Load(), appendAttempts.Load(), failedMember.DeliveryID, len(before), ids, retryArmed, len(gotDiagnostics))
				}
				if fs != nil {
					fs.mu.Lock()
					n := fs.syncFailures
					fs.mu.Unlock()
					if n < 2 {
						t.Fatalf("real receiver sync failures=%d", n)
					}
					failures.Store(int32(n))
					fs.release(t)
				} else if failures.Load() == 0 {
					t.Fatal("requested source fault did not fire")
				}
				if !appendFault {
					recovering.Store(true)
				}
				release()
				callbackBatchAwait(t, done, "healthy shell completes after failed member")
				if appendFault {
					callbackBatchAwait(t, attemptDone, "source producer returns with append fault still active")
					if ids, err := s.pendingNotificationIDs(); err != nil || slices.Contains(ids, stableWatchAttentionID(failedMember)) || failures.Load() != 2 || appendAttempts.Load() != 2 {
						t.Fatalf("append recovered before timer: custody=%v failures=%d attempts=%d err=%v", ids, failures.Load(), appendAttempts.Load(), err)
					}
					recovering.Store(true)
				}
				// The existing timer recovers leftover transport without a pump,
				// manual kick or model boundary. Failed source teardown instead
				// requires its documented next matching condition, below.
				clock.Advance(jobNotificationRetryInitialDelay)
				clock.Drain()
				if appendFault && appendAttempts.Load() != 3 {
					t.Fatalf("timer must perform the third actual append attempt: got=%d failures=%d", appendAttempts.Load(), failures.Load())
				}
				if fault == "teardown" {
					// Failed teardown rearms the breaker. Its existing retry is the
					// next matching condition, not transport of an already settled F.
					jm.mu.Lock()
					fires, owner, tripped := cfg.conditionFires, cfg.callbackBudgetOwner, cfg.budgetTripped
					jm.mu.Unlock()
					if fires != watchDeliveryBudget || owner != "" || tripped {
						t.Fatalf("teardown rollback fires=%d owner=%s tripped=%t", fires, owner, tripped)
					}
					before := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
					if failures.Load() != 1 || len(before) != 1 || before[0].WatchSend.TriggerReason != "event: JOB_FINISHED" {
						t.Fatalf("teardown before next condition: counted failures=%d source=%+v", failures.Load(), before)
					}
					onSessionEventKD(jm, events.EventJobFinished, events.JobFinishedData{JobID: jobID})
				}
				source := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
				if len(source) != 2 || source[1].WatchSend.TriggerReason != watchBudgetEndTrigger {
					t.Fatalf("recovered source=%+v", source)
				}
				if appendFault {
					failedIndex := 0
					if fault == "notice-append" {
						failedIndex = 1
					}
					if source[failedIndex].WatchSend.DeliveryID != failedMember.DeliveryID {
						t.Fatal("timer replaced the failed source identity")
					}
					diagnosticsMu.Lock()
					diagnosticCount := len(diagnostics)
					diagnosticsMu.Unlock()
					if diagnosticCount != 2 {
						t.Fatalf("unexpected recovery diagnostics=%d, want the two already checked failures", diagnosticCount)
					}
				}
				for _, event := range source {
					requireWatchSettlement(t, jm, *event.WatchSend, 1)
				}
				requireCompletionClaimsReleased(t, jm)
				jm.mu.Lock()
				fires, owner := cfg.conditionFires, cfg.callbackBudgetOwner
				jm.mu.Unlock()
				if fires != watchDeliveryBudget || owner != "" || jm.hasPendingWatchSends() {
					t.Fatalf("recovery fires=%d owner=%s pending=%t", fires, owner, jm.hasPendingWatchSends())
				}
				ids, err := s.pendingNotificationIDs()
				if err != nil || len(ids) != 3 {
					t.Fatalf("recovered custody=%v err=%v", ids, err)
				}
				adapter.steps = []func(llm.Request) llm.Response{func(request llm.Request) llm.Response {
					requireCallbackBatchRequest(t, s, request, ids)
					return finalResponse("completion-failure-response-964")
				}}
				if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
					t.Fatal(err)
				}
				requireCallbackBatchReceipts(t, s, ids, 1)
				if len(adapter.Requests()) != 1 {
					t.Fatal("recovery duplicated request")
				}
				t.Logf("fault=%s injectedFailures=%d appendAttempts=%d source=%s,%s receiver=%v fires=%d providerRequests=1", fault, failures.Load(), appendAttempts.Load(), source[0].WatchSend.DeliveryID, source[1].WatchSend.DeliveryID, ids, fires)
			})
		}
	}
}

// Exercise the existing private source-commit protocol with the real stable
// foreign receiver fixture. This is not a new public send/watch shape.
func TestCompletionDeliveryForeignReceiverProgress(t *testing.T) {
	t.Parallel()
	f := newStableWatchRuntimeFixture(t, nil)
	jm := f.sourceJM
	foreignCfg := f.onlyWatchConfig(t)
	writer, err := transcript.NewWriter(transcriptPath(f.source.stateDir, f.source.ID()), transcript.Header{SessionID: f.source.ID()})
	if err != nil {
		t.Fatal(err)
	}
	f.source.attachTranscript(writer)
	t.Cleanup(func() {
		if err := f.source.closeAttachedTranscript(); err != nil {
			t.Error(err)
		}
	})
	jm.mu.Lock()
	jm.stableOwner, jm.retirementOwner = f.source, f.source
	production := jm.newWatchDeliveryProductionLocked()
	jm.mu.Unlock()
	defer jm.drainWatchDeliveryProduction(production)
	ownWatch, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
	if err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var own jobstore.WatchSendState
	var attemptsMu sync.Mutex
	var attempts []bool
	jm.mu.Lock()
	_, ownCfg, found := jm.watchConfigByIDLocked(ownWatch.WatchID)
	if !found {
		jm.mu.Unlock()
		t.Fatal("own callback config missing")
	}
	ownCfg.testOnlyAfterSendPending = func(state jobstore.WatchSendState) { own = state; close(entered); <-resume }
	ownCfg.testOnlyAfterDeliveryClaim = func(_ jobstore.WatchSendState, claimed bool) {
		attemptsMu.Lock()
		attempts = append(attempts, claimed)
		attemptsMu.Unlock()
	}
	jm.mu.Unlock()
	ownNotice := jm.watchNotificationFromWatch(ownCfg, "own-source", "event: COMMUNICATE", nil)
	ownNotice.production = production
	producer := callbackBatchStart(t, release, func() {
		if _, err := jm.produceCallbackNotifications([]jobNotification{ownNotice}); err != nil {
			t.Error(err)
		}
		jm.drainWatchDeliveryProduction(production)
	})
	callbackBatchAwait(t, entered, "actual owned callback commit before foreign progress")
	foreignNotice := jm.watchNotificationFromWatch(foreignCfg, "foreign-source", "event: COMMUNICATE", nil)
	foreignNotice.production = production
	if _, err := jm.produceCallbackNotifications([]jobNotification{foreignNotice}); err != nil {
		t.Fatal(err)
	}
	var foreign jobstore.WatchSendState
	for _, event := range producerSourceEvents(t, jm, jobstore.EventWatchSendPending) {
		if event.WatchSend.ReceiverSessionID == f.root.ID() {
			foreign = *event.WatchSend
		}
	}
	if foreign.DeliveryID == "" {
		t.Fatal("foreign source did not commit")
	}
	pump := callbackBatchStart(t, release, jm.dispatchProducedWatchSends)
	callbackBatchAwait(t, pump, "foreign custody while exact own member stays paused")
	attemptsMu.Lock()
	got := slices.Clone(attempts)
	attemptsMu.Unlock()
	if len(got) == 0 || slices.Contains(got, true) {
		t.Errorf("owned actual claim attempts=%v", got)
	}
	if ids, err := f.root.pendingNotificationIDs(); err != nil || !slices.Equal(ids, []string{stableWatchAttentionID(foreign)}) {
		t.Fatalf("foreign custody=%v err=%v", ids, err)
	}
	if ids, err := f.source.pendingNotificationIDs(); err != nil || len(ids) != 0 {
		t.Fatalf("premature own custody=%v err=%v", ids, err)
	}
	requireWatchSettlement(t, jm, foreign, 1)
	release()
	callbackBatchAwait(t, producer, "owned producer resumes after foreign settlement")
	requireWatchSettlement(t, jm, own, 1)
	requireCompletionClaimsReleased(t, jm)
	t.Logf("owned=%s attemptedClaims=%v foreign=%s settled once while owned source paused", own.DeliveryID, got, foreign.DeliveryID)
}

// A private persistence-protocol control, not a proposed public wildcard send.
// The existing nonbatch fallback can commit pending before eviction append fails.
func TestCompletionDeliveryCommittedPrefix(t *testing.T) {
	t.Parallel()
	for _, persistent := range []bool{true, false} {
		t.Run(fmt.Sprintf("persistent=%t", persistent), func(t *testing.T) {
			s, _ := newTerminalFlushSession(t, persistent)
			jm := s.jobManager
			watch, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Send: &watchSendArgs{To: runtimeMessageAliasCaller}})
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, cfg, found := jm.watchConfigByIDLocked(watch.WatchID)
			jm.mu.Unlock()
			if !found {
				t.Fatal("prefix config missing")
			}
			build := func(target string) watchSendDelivery {
				jm.mu.Lock()
				d := jm.watchSendSnapshot(cfg, target, "test", events.SessionEvent{SessionID: jm.sessionID})
				jm.mu.Unlock()
				d.frame = "committed-prefix-" + target
				return d
			}
			for i := range defaultWatchSendPendingCap {
				d := build(fmt.Sprintf("old-%d", i))
				if !jm.claimWatchSendDelivery(d.deliveryID) {
					t.Fatal("old prefix ID already claimed")
				}
				t.Cleanup(func() { jm.releaseWatchSendDeliveryClaim(d.deliveryID) })
				if _, _, ok, err := jm.recordWatchSend(d); err != nil || !ok {
					t.Fatalf("fill %d ok=%t err=%v", i, ok, err)
				}
			}
			jm.mu.Lock()
			production := jm.newWatchDeliveryProductionLocked()
			jm.mu.Unlock()
			defer jm.drainWatchDeliveryProduction(production)
			originalAppend, originalBatch := jm.appendEvent, jm.appendEvents
			defer func() { jm.appendEvent, jm.appendEvents = originalAppend, originalBatch }()
			injected := errors.New("controlled committed-prefix eviction append failure")
			var failures atomic.Int32
			jm.appendEvents = nil // Exercise the existing supported sequential fallback.
			jm.appendEvent = func(event jobstore.Event) error {
				if event.Kind == jobstore.EventWatchSendEvicted && failures.CompareAndSwap(0, 1) {
					return injected
				}
				return originalAppend(event)
			}
			d := build("new")
			d.production = production
			state, _, ok, err := jm.recordWatchSend(d)
			if !ok || !errors.Is(err, injected) || failures.Load() != 1 {
				t.Fatalf("durable-prefix result ok=%t err=%v failures=%d", ok, err, failures.Load())
			}
			fold, err := jm.store.LoadWatchSends()
			if err != nil || fold.Pending[state.Key] == nil || fold.Pending[state.Key].DeliveryID != state.DeliveryID {
				t.Fatalf("committed prefix absent from independent durable fold: err=%v", err)
			}
			var attempts []bool
			jm.mu.Lock()
			cfg.testOnlyAfterDeliveryClaim = func(s jobstore.WatchSendState, claimed bool) {
				if s.DeliveryID == state.DeliveryID {
					attempts = append(attempts, claimed)
				}
			}
			jm.mu.Unlock()
			jm.dispatchProducedWatchSends()
			if len(attempts) == 0 || slices.Contains(attempts, true) {
				t.Fatalf("committed-prefix actual claims=%v, want stand-down despite outer error", attempts)
			}
			if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
				t.Fatalf("premature prefix custody=%v err=%v", ids, err)
			}
			jm.drainWatchDeliveryProduction(production)
			requireWatchSettlement(t, jm, state, 1)
			if ids, err := s.pendingNotificationIDs(); err != nil || !slices.Equal(ids, []string{stableWatchAttentionID(state)}) {
				t.Fatalf("prefix custody=%v err=%v", ids, err)
			}
			if !jm.claimWatchSendDelivery(state.DeliveryID) {
				t.Fatal("prefix claim leaked")
			} else {
				jm.releaseWatchSendDeliveryClaim(state.DeliveryID)
			}
			t.Logf("outer ok=true with counted eviction failure=%d, reserved=%s actualClaims=%v, committed prefix accepted once", failures.Load(), state.DeliveryID, attempts)
		})
	}
}

// Check accepted rows and assistant receipts independently, including the
// lifetime-local history used when StateDir is empty.
func requireCallbackBatchReceipts(t *testing.T, s *Session, ids []string, receipts int) {
	t.Helper()
	s.mu.Lock()
	history := append([]schema.Turn(nil), s.history...)
	s.mu.Unlock()
	requireCallbackBatchTurnCounts(t, history, ids, receipts)
	if s.stateDir != "" {
		var turns []schema.Turn
		for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
			turns = append(turns, entry.Turn)
		}
		requireCallbackBatchTurnCounts(t, turns, ids, receipts)
	}
}

func requireCallbackBatchRequest(t *testing.T, s *Session, request llm.Request, ids []string) {
	t.Helper()
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	fold = fold.clone()
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		matches := 0
		for _, message := range request.Messages {
			if message.Role == llm.RoleUser && strings.Contains(message.Text(), fold.content[id].Text()) {
				matches++
			}
		}
		if fold.content[id].Text() == "" || matches != 1 {
			t.Fatalf("provider request contains source-backed ID %s payload %d times, want1", id, matches)
		}
	}
}

func requireLegacyBudgetBatch(t *testing.T, jm *jobManager, cfg *watchConfig, notices []jobNotification) {
	t.Helper()
	produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(produced) != 2 {
		t.Fatalf("source callback rows=%d want2", len(produced))
	}
	firing, budget := *produced[0].WatchSend, *produced[1].WatchSend
	if !strings.HasPrefix(firing.TriggerReason, "event: ") || budget.TriggerReason != "budget_exhausted" || firing.Key == budget.Key || firing.DeliveryID == budget.DeliveryID {
		t.Fatalf("source lost distinct final firing/budget identities: firing=%+v budget=%+v", firing, budget)
	}
	if len(notices) != 2 || notices[0].callbackConfig != cfg || notices[1].callbackConfig != cfg || notices[0].callbackTrigger != firing.TriggerReason || notices[1].callbackTrigger != budget.TriggerReason {
		t.Fatalf("legacy batch is not source firing then machine budget notice: %+v", notices)
	}
	for i, state := range []jobstore.WatchSendState{firing, budget} {
		frame := (&Session{}).formatJobNotificationReminder([]deliverableJobNotification{{notification: notices[i]}})
		if state.Frame != frame {
			t.Fatalf("legacy payload %d differs from independently journaled source", i)
		}
	}
	jm.mu.Lock()
	fires, deliveries := cfg.conditionFires, cfg.deliveries
	jm.mu.Unlock()
	if fires != watchDeliveryBudget || deliveries != watchDeliveryBudget || jm.watchCount() != 0 {
		t.Fatalf("fires=%d deliveries=%d live=%d", fires, deliveries, jm.watchCount())
	}
	cleared := producerSourceEvents(t, jm, jobstore.EventWatchCleared)
	if len(cleared) != 1 {
		t.Fatalf("teardown rows=%d want1", len(cleared))
	}
}

func requireCallbackBatchSource(t *testing.T, s *Session, state jobstore.WatchSendState) {
	t.Helper()
	id := fmt.Sprintf("watch:%s:%d", state.DeliveryID, state.UpdateSeq)
	s.attentionMu.Lock()
	fold, err := s.delegateAttentionFoldLocked()
	fold = fold.clone()
	s.attentionMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if state.Frame == "" || fold.content[id].Text() != state.Frame {
		t.Fatalf("canonical payload %s differs from independent source frame", id)
	}
}

func requireCallbackBatchTurnCounts(t *testing.T, turns []schema.Turn, ids []string, receipts int) {
	t.Helper()
	for _, id := range ids {
		accepted, observed := 0, 0
		for _, turn := range turns {
			if turn.AttentionID == id {
				accepted++
			}
			for _, seen := range turn.ObservedNotificationIDs {
				if seen == id {
					observed++
					if turn.Kind != schema.TurnAssistant {
						t.Fatalf("ID %s observed outside a successful assistant response", id)
					}
				}
			}
		}
		if accepted != 1 || observed != receipts {
			t.Fatalf("ID %s accepted=%d observed=%d want1,%d", id, accepted, observed, receipts)
		}
	}
}
