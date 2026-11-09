package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func quietYieldCall(id, args string) llm.ToolCallData {
	return llm.ToolCallData{ID: id, Name: "yield", Arguments: json.RawMessage(args), Type: "function"}
}

// historyHasToolResult reports whether the session's history carries a tool
// result for name whose content contains want.
func historyHasToolResult(sess *Session, name, want string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		for _, part := range turn.Message.Content {
			result := part.ToolResult
			if part.Kind == llm.ContentToolResult && result != nil && result.Name == name && strings.Contains(fmt.Sprint(result.Content), want) {
				return true
			}
		}
	}
	return false
}

// The yield tool ends the activation as soon as its result is durable: no
// second model round and no user-visible output (issue #832).
func TestQuietYieldEndsTheActivationWithoutAnotherModelRound(t *testing.T) {
	t.Parallel()
	var rounds atomic.Int32
	var secondRound atomic.Bool
	sess := newSession(t, withClock(agenttest.NewFakeClock()), withSteps(
		func(llm.Request) llm.Response {
			rounds.Add(1)
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"intent":"waiting for the tests","timeout_seconds":30}`))
		},
		func(llm.Request) llm.Response {
			secondRound.Store(true)
			return agenttest.FinalResponse("should not run")
		},
	))
	out, err := sess.ProcessInput(tripwireCtx(t), "start", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if secondRound.Load() {
		t.Fatal("a second model round ran after yield ended the activation")
	}
	if got := rounds.Load(); got != 1 {
		t.Fatalf("model rounds = %d, want 1", got)
	}
	if out != "" {
		t.Fatalf("yield returned user-visible output %q, want none", out)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after yield = %q, want idle", got)
	}
	if !historyHasToolResult(sess, "yield", "yielding") {
		t.Fatal("the yield tool result was not recorded in the transcript")
	}
}

// A yield ends the activation before another model request, so user steering
// that arrived during its round must stay pending for a carrier turn rather
// than being marked incorporated into a request that never happens.
func TestQuietYieldRunLeavesUserSteeringForACarrier(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	sess.mu.Lock()
	sess.steeringQueue = append(sess.steeringQueue, steeringMessage{Text: "also do this", Source: events.SteeringSourceUser})
	sess.mu.Unlock()
	sess.requestQuietYield("waiting", 0)
	var sigs []string
	var failed []bool
	if _, err := sess.injectPostToolSteering(context.Background(), nil, nil, &sigs, &failed); err != nil {
		t.Fatalf("injectPostToolSteering: %v", err)
	}
	if !sess.hasPendingUserSteering() {
		t.Fatal("the yield round drained user steering into a turn with no further model request")
	}
}

// The drain ladder must not start an autonomous continuation after a yielded
// turn; queued user work still runs.
func TestDrainSelectorStopsAfterAYieldedTurn(t *testing.T) {
	t.Parallel()
	action, skip := selectDrainNextAction(drainInputs{
		RanKind:          EntryUserInput,
		RanYielded:       true,
		HaveDeferredCont: true,
	})
	if action != goIdle || !skip {
		t.Fatalf("after a yielded turn with a deferred continuation: action=%v skipGoalGate=%t, want goIdle/true", action, skip)
	}
	if action, _ := selectDrainNextAction(drainInputs{RanKind: EntryUserInput, RanYielded: true, QueuedText: "next"}); action != runQueued {
		t.Fatalf("a queued user message after a yielded turn = %v, want runQueued", action)
	}
	if shouldRunDeferredContinuation(false, true, false, true) {
		t.Fatal("a continuation deferred across a yielded turn would run inline and cancel the fallback")
	}
	if !shouldRunDeferredContinuation(false, true, false, false) {
		t.Fatal("a continuation deferred by a non-yielded turn no longer runs")
	}
}

// A delegate that yields with a timeout must run its leased notification turn
// when the fallback becomes due, or its finalization drain waits forever.
func TestDelegateDrainDeliversATimeoutYieldFallback(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	var requests atomic.Int32
	sess := newSession(t, withClock(fake), withSteps(func(llm.Request) llm.Response {
		requests.Add(1)
		return agenttest.FinalResponse("resumed from the fallback")
	}))
	sess.requestQuietYield("waiting", 30*time.Second)
	sess.consumeQuietYieldRequest()
	sess.markQuietYield()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // TRIPWIRE: only detects a hung drain.
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.DrainJobTreeKeepingReport(ctx)
		done <- err
	}()
	fake.Advance(30 * time.Second)
	fake.Drain()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the drain never presented the timeout fallback")
	}
	if requests.Load() == 0 {
		t.Fatal("no model request presented the timeout fallback")
	}
}

// A no-timeout yield awaiting a child's report must run the notification turn
// that presents the durable attention row, or its finalization drain waits
// forever behind the quiet-yield blocker.
func TestDelegateDrainPresentsAChildReportDuringQuietYield(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	var requests atomic.Int32
	sess := newSession(t, withStateDir(t.TempDir()), withClock(fake), withSteps(func(llm.Request) llm.Response {
		requests.Add(1)
		return agenttest.FinalResponse("child report consumed")
	}))
	sess.requestQuietYield("waiting", 0)
	sess.consumeQuietYieldRequest()
	sess.markQuietYield()
	if _, err := sess.appendDelegateNotificationDurably(
		"delegate:child/delivery/1",
		`<delegate-notification delegate_id="dlg_child">done</delegate-notification>`,
	); err != nil {
		t.Fatalf("append child report: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // TRIPWIRE: only detects a hung drain.
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.DrainJobTreeKeepingReport(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("the drain never presented the child report")
	}
	if requests.Load() == 0 {
		t.Fatal("no model request presented the child report")
	}
}

// A timeout arms exactly one fallback wake, and the wake delivers bounded
// structured context naming the yield, trigger, and deadline.
func TestQuietYieldTimeoutWakesOnceWithBoundedContext(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	var mu sync.Mutex
	var requests []llm.Request
	sess := newSession(t, withClock(fake), withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"intent":"waiting for the tests","timeout_seconds":30}`))
		},
		func(req llm.Request) llm.Response {
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			return agenttest.FinalResponse("checked")
		},
	))
	var wakeMu sync.Mutex
	wakes := 0
	sess.SetNotifyFunc(func() {
		wakeMu.Lock()
		wakes++
		wakeMu.Unlock()
	})
	if _, err := sess.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fake.Advance(30 * time.Second)
	fake.Drain()
	wakeMu.Lock()
	got := wakes
	wakeMu.Unlock()
	if got != 1 {
		t.Fatalf("fallback wakes = %d, want exactly 1", got)
	}
	fake.Advance(24 * time.Hour)
	fake.Drain()
	wakeMu.Lock()
	got = wakes
	wakeMu.Unlock()
	if got != 1 {
		t.Fatalf("fallback fired again later: wakes = %d, want 1", got)
	}

	// The daemon's response to notify() is an EntryNotification turn.
	sess.yieldMu.Lock()
	t.Logf("before notification: due=%v armed=%v request=%v", sess.yieldDue, sess.yieldArmed, sess.yieldRequest)
	sess.yieldMu.Unlock()
	if _, err := sess.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	sess.mu.Lock()
	t.Logf("after notification: state=%v history=%d", sess.state, len(sess.history))
	sess.mu.Unlock()
	sess.yieldMu.Lock()
	t.Logf("after notification: due=%v", sess.yieldDue)
	sess.yieldMu.Unlock()
	mu.Lock()
	if len(requests) != 1 {
		mu.Unlock()
		t.Fatalf("timeout notification requests = %d, want 1", len(requests))
	}
	encoded, err := json.Marshal(requests[0])
	mu.Unlock()
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	for _, want := range []string{"trigger: timeout", "yield_id: y1", "deadline: ", "waiting for the tests"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("timeout wake context is missing %q:\n%s", want, encoded)
		}
	}
}

// Any ordinary turn resumes the session itself, so it cancels the outstanding
// fallback rather than letting a stale timer wake it later.
func TestOrdinaryTurnCancelsTheQuietYieldFallback(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	sess := newSession(t, withClock(fake), withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"timeout_seconds":30}`))
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("second turn") },
	))
	var mu sync.Mutex
	wakes := 0
	sess.SetNotifyFunc(func() {
		mu.Lock()
		wakes++
		mu.Unlock()
	})
	if _, err := sess.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("first input: %v", err)
	}
	fake.Advance(10 * time.Second)
	fake.Drain()
	if _, err := sess.ProcessInput(tripwireCtx(t), "next", nil); err != nil {
		t.Fatalf("second input: %v", err)
	}
	fake.Advance(30 * time.Second)
	fake.Drain()
	mu.Lock()
	defer mu.Unlock()
	if wakes != 0 {
		t.Fatalf("the canceled fallback woke the session %d time(s)", wakes)
	}
}

// A due fallback is canceled by the next ordinary turn: the timer already
// fired, but the user's own turn resumed the session, so its stale notification
// turn must not run a second model request. A Stop uses the same cancellation.
func TestOrdinaryTurnCancelsADueQuietYieldFallback(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	var rounds atomic.Int32
	var dueRan atomic.Bool
	sess := newSession(t, withClock(fake), withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"timeout_seconds":30}`))
		},
		func(llm.Request) llm.Response {
			rounds.Add(1)
			return agenttest.FinalResponse("second turn")
		},
		func(llm.Request) llm.Response {
			dueRan.Store(true)
			return agenttest.FinalResponse("stale fallback")
		},
	))
	if _, err := sess.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("first input: %v", err)
	}
	fake.Advance(30 * time.Second)
	fake.Drain()
	if _, err := sess.ProcessInput(tripwireCtx(t), "next", nil); err != nil {
		t.Fatalf("second input: %v", err)
	}
	if _, err := sess.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatalf("notification turn: %v", err)
	}
	if dueRan.Load() {
		t.Fatal("the stale due fallback ran a model turn after the user's own turn resumed the session")
	}
	if got := rounds.Load(); got != 1 {
		t.Fatalf("ordinary turns = %d, want 1", got)
	}
}

// A daemon wake that finds no deliverable notification work must preserve the
// armed fallback; cancelling it before admission would silently lose the
// timeout wake.
func TestNotificationStandDownKeepsTheQuietYieldFallback(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	sess := newSession(t, withClock(fake), withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"timeout_seconds":30}`))
		},
	))
	var wakes atomic.Int32
	sess.SetNotifyFunc(func() { wakes.Add(1) })
	if _, err := sess.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if _, err := sess.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatalf("stand-down notification: %v", err)
	}
	fake.Advance(30 * time.Second)
	fake.Drain()
	if got := wakes.Load(); got != 1 {
		t.Fatalf("fallback wakes after a stood-down notification = %d, want 1", got)
	}
}

// A quiet-yield fallback keeps the session alive: the idle retirement timer
// must not retire it while the armed or due wake is the only thing left.
func TestQuietYieldBlocksRetirement(t *testing.T) {
	t.Parallel()
	sess := newSession(t, withClock(agenttest.NewFakeClock()))
	sess.requestQuietYield("waiting", time.Minute)
	if !retirementHasCategory(sess.retirementLocalBlockers(), "autonomous") {
		t.Fatal("an armed quiet yield is not a retirement blocker")
	}
	sess.cancelPendingQuietYield()
	if retirementHasCategory(sess.retirementLocalBlockers(), "autonomous") {
		t.Fatal("a cancelled quiet yield still blocks retirement")
	}
}

// A due fallback whose reminder append failed must keep the paced notification
// retry alive: the retry's pending predicate has to see it.
func TestDueQuietYieldKeepsTheNotificationRetryAlive(t *testing.T) {
	t.Parallel()
	fake := agenttest.NewFakeClock()
	sess := newSession(t, withClock(fake), withSteps(
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(quietYieldCall("yield-1", `{"timeout_seconds":30}`))
		},
	))
	var wakes atomic.Int32
	sess.SetNotifyFunc(func() { wakes.Add(1) })
	if _, err := sess.ProcessInput(tripwireCtx(t), "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fake.Advance(30 * time.Second)
	fake.Drain()
	wakes.Store(0)
	// The append-failure path arms this retry; the due record is what makes
	// its firing notify instead of settling.
	sess.scheduleQuietYieldRetry()
	fake.Advance(jobNotificationRetryInitialDelay)
	fake.Drain()
	if got := wakes.Load(); got == 0 {
		t.Fatal("the paced notification retry did not fire for a retained due yield")
	}
}

func retirementHasCategory(blockers []RetirementBlocker, category string) bool {
	for _, blocker := range blockers {
		if blocker.Category == category {
			return true
		}
	}
	return false
}

// A no-timeout yield needs a source that can wake the session; without one the
// tool refuses and the model must pass a timeout or start the work.
func TestQuietYieldRequiresAWakeSourceWithoutATimeout(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	ctx := context.Background()
	if _, err := quietYieldTool(ctx, sess, map[string]any{"intent": "waiting"}); err == nil {
		t.Fatal("a no-timeout yield with no running work was accepted")
	}
	if sess.quietYieldActive() {
		t.Fatal("a refused yield armed a wait")
	}
	if _, err := quietYieldTool(ctx, sess, map[string]any{"timeout_seconds": float64(30)}); err != nil {
		t.Fatalf("timeout-only yield was refused: %v", err)
	}
	sess.cancelPendingQuietYield()
	for _, args := range []map[string]any{
		{"timeout_seconds": float64(0)},
		{"timeout_seconds": float64(quietYieldTimeoutMaxSeconds + 1)},
		{"timeout_seconds": 1.5},
		{"timeout_seconds": "30"},
	} {
		if _, err := quietYieldTool(ctx, sess, args); err == nil {
			t.Fatalf("timeout_seconds=%v was accepted, want a bounded-integer refusal", args["timeout_seconds"])
		}
		if sess.quietYieldActive() {
			t.Fatalf("timeout_seconds=%v was refused but armed a wait", args["timeout_seconds"])
		}
	}
}

// A one-shot run has no session left to wake, so yield is refused with the
// message tool that can end that turn.
func TestQuietYieldIsRefusedWhenTheTurnEndsTheProcess(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	sess.cfg.TurnEndsProcess = true
	if _, err := quietYieldTool(context.Background(), sess, map[string]any{"timeout_seconds": float64(30)}); err == nil {
		t.Fatal("a one-shot yield was accepted")
	}
	if sess.quietYieldActive() {
		t.Fatal("a refused one-shot yield armed a wait")
	}
}
