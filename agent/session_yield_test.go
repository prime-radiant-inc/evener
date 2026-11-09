package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// A no-timeout yield needs a source that can wake the session; without one the
// tool refuses and the model must pass a timeout or start the work.
func TestQuietYieldRequiresAWakeSourceWithoutATimeout(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	if _, err := quietYieldTool(sess, map[string]any{"intent": "waiting"}); err == nil || !strings.Contains(err.Error(), "running shell job") {
		t.Fatalf("no-timeout yield with no running work = %v, want a running-work refusal", err)
	}
	if _, err := quietYieldTool(sess, map[string]any{"timeout_seconds": float64(30)}); err != nil {
		t.Fatalf("timeout-only yield was refused: %v", err)
	}
	sess.cancelPendingQuietYield()
	for _, args := range []map[string]any{
		{"timeout_seconds": float64(0)},
		{"timeout_seconds": float64(quietYieldTimeoutMaxSeconds + 1)},
		{"timeout_seconds": 1.5},
		{"timeout_seconds": "30"},
	} {
		if _, err := quietYieldTool(sess, args); err == nil {
			t.Fatalf("timeout_seconds=%v was accepted, want a bounded-integer refusal", args["timeout_seconds"])
		}
	}
}

// A one-shot run has no session left to wake, so yield is refused with the
// message tool that can end that turn.
func TestQuietYieldIsRefusedWhenTheTurnEndsTheProcess(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	sess.cfg.TurnEndsProcess = true
	_, err := quietYieldTool(sess, map[string]any{"timeout_seconds": float64(30)})
	if err == nil || !strings.Contains(err.Error(), "outlives this turn") {
		t.Fatalf("one-shot yield = %v, want an outlives-this-turn refusal", err)
	}
}
