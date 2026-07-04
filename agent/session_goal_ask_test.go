package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// --- Task 6: goal engine holds while the session awaits the user (spec §5.3, §8) ---
//
// Four kick paths could otherwise drive a goal continuation past an unanswered
// ask_user question: armGoalContinuation's single call site, settleGoalOnIdle,
// the haveDeferredCont inline-run site, and SetGoal's own idle-kick. All four
// must arm (store/leave the goal active) rather than kick while SessionAwaiting;
// the armed goal resumes at the first non-awaiting boundary. armGoalContinuation
// needs no new guard of its own — its only call site (session_lifecycle.go, the
// drain-loop gate) is already skipped while awaiting by Task 5's skipGoalGate,
// which folds in the Awaiting flag; TestGoalHoldsAwaiting_ContinuationEndsViaAsk
// below pins that as a regression rather than re-implementing the guard.

// TestGoalHoldsAwaiting_ContinuationEndsViaAsk covers the first kick path: a
// running goal's own continuation turn ends by asking a question. The turn
// ends SessionAwaiting; armGoalContinuation's single call site is gated by
// Task 5's skipGoalGate (which folds in Awaiting), so it neither folds this
// turn's progress into the no-progress breaker nor arms a further
// continuation, and settleGoalOnIdle — reached regardless, at the same drain
// tail, once the gate's fold is skipped — must not kick either.
func TestGoalHoldsAwaiting_ContinuationEndsViaAsk(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a second LLM call: a continuation that asks must end the turn at the ask boundary, and the goal hold must not kick a further continuation")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))

	kicks := 0
	sess.SetKickFunc(func(string) { kicks++ })
	sess.getOrCreateGoalStore().Set("ship the feature", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if kicks != 0 {
		t.Fatalf("kick count at the ask boundary = %d, want 0 (arm, don't kick)", kicks)
	}
	if got := len(f.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1 (no further continuation offered at the ask boundary)", got)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		t.Fatalf("goal snapshot = %+v ok=%v, want still active (unresolved, not folded)", snap, ok)
	}
	if snap.Iterations != 0 || snap.NoProgressStreak != 0 {
		t.Fatalf("goal snapshot = %+v, want Iterations=0 NoProgressStreak=0 (the asking turn must not fold into the breaker)", snap)
	}
}

// TestGoalHoldsAwaiting_DeferredContinuationHeldPastNotificationAsk covers a
// distinct gap flagged during Task 5's review, in the haveDeferredCont
// inline-run site (agent/session_lifecycle.go), not the direct
// armGoalContinuation call: a continuation turn can fold and DEFER a further
// continuation (haveDeferredCont=true) and then, in the SAME drain-tail
// iteration, hand off to an interleaved notification turn (spec §5.3's
// notification-before-deferred-continuation ordering) BEFORE ever reaching
// the inline-run check. If that notification turn itself asks a question, the
// still-true haveDeferredCont flag survives the notification turn's own gate
// (which only skips ITS OWN fold, via skipGoalGate) and, without a guard at
// the inline-run site, would drive the deferred continuation past the
// notification turn's unanswered ask on the very next check.
func TestGoalHoldsAwaiting_DeferredContinuationHeldPastNotificationAsk(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	enqueueNotif := llm.ToolCallData{ID: "notif1", Name: "enqueue_test_notification_goal", Arguments: json.RawMessage(`{}`), Type: "function"}
	comm := communicateCall("c1", "made some progress")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// Turn A: the continuation's own turn — makes progress (folding and
			// deferring a further continuation), enqueues a notification mid-round
			// (simulating a job finishing during this turn), and ends normally
			// (not an ask) via communicate.
			func(req llm.Request) llm.Response { return toolCallResponse(enqueueNotif, comm) },
			// Turn B: the interleaved notification's own turn asks a question.
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response {
				t.Fatalf("should not reach a third LLM call: the continuation deferred before the notification interleaved must not run past the notification turn's own unanswered ask")
				return llm.Response{}
			},
		},
	}
	sess := newSession(t, withAdapter(f))
	sess.RegisterTool("enqueue_test_notification_goal",
		"test-only: enqueues a job notification mid-round, simulating a job finishing during the goal's own continuation turn",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(context.Context, any) (any, error) {
			sess.enqueueJobNotification(watchNotification("job_during_goal_turn", "output_match: done"))
			return "queued", nil
		})

	kicks := 0
	sess.SetKickFunc(func(string) { kicks++ })
	sess.getOrCreateGoalStore().Set("ship the feature", time.Now())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}

	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q", got, SessionAwaiting)
	}
	if got := len(f.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2 (the continuation turn + the interleaved notification turn that asked — no further continuation past the ask)", got)
	}
	if kicks != 0 {
		t.Fatalf("kick count = %d, want 0 (arm, don't kick)", kicks)
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		t.Fatalf("goal snapshot = %+v ok=%v, want still active", snap, ok)
	}
}

// TestGoalHoldsAwaiting_SetGoalArmsWithoutKick covers the SetGoal-idle-kick
// path: /goal issued while the session is resting SessionAwaiting must arm
// (store the goal, active) rather than kick — there is no in-flight turn for
// a drain-loop gate to back it, so without this guard SetGoal's own idle-kick
// branch would drive a turn straight past the pending question. The armed
// goal then kicks at the first non-awaiting settle: settleGoalOnIdle is the
// same seam TestSettleGoalOnIdleKicksWindowGoal exercises directly for the
// turn-tail race window; here it stands in for "a later drain tail runs after
// the reply has resolved the ask and left the session no longer awaiting."
func TestGoalHoldsAwaiting_SetGoalArmsWithoutKick(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	var kicked []string
	sess.SetKickFunc(func(prompt string) { kicked = append(kicked, prompt) })

	sess.mu.Lock()
	sess.state = SessionAwaiting
	sess.mu.Unlock()

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal while awaiting should report started=false (arm, don't kick)")
	}
	if len(kicked) != 0 {
		t.Fatalf("SetGoal while awaiting must NOT kick, got %d kicks", len(kicked))
	}
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive || snap.Objective != "ship the feature" {
		t.Fatalf("SetGoal must still store the goal while awaiting, got %+v ok=%v", snap, ok)
	}

	// The reply resolves the ask: the session leaves awaiting, and the next
	// drain-tail settle (settleGoalOnIdle) kicks the armed goal.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.mu.Unlock()
	sess.settleGoalOnIdle()

	if len(kicked) != 1 {
		t.Fatalf("kick count at the post-reply boundary = %d, want exactly 1 (the armed goal resumes)", len(kicked))
	}
	if kicked[0] != goal.Render("ship the feature") {
		t.Fatalf("kick prompt = %q, want the rendered continuation prompt", kicked[0])
	}
}

// TestGoalHoldsAwaiting_RestoredActiveGoalNoStartupKick covers the fourth kick
// path: a restored active goal. RestoreSessionFromMeta loads an active goal
// into the store "loaded but idle" (agent/session_init.go:405-407 — restore
// itself contains no kick call at all, confirmed by inspection: the only
// production sink for a kick is server.Server.SubmitContinuation, invoked
// solely through the Session.kickFunc callback wired by bridgeSession AFTER
// restore returns, in cmd/serf/serve.go). This test additionally proves that
// even once the restored session reaches SessionAwaiting, the same
// settleGoalOnIdle guard used elsewhere still refuses to kick a goal that was
// merely loaded, not folded — so there is no distinct "startup kick" site to
// guard beyond the settle/arm guards already covering the other three paths.
// State-from-transcript re-derivation on restore is a separate, not-yet-built
// seam (spec §5.4); this test forces the state directly to isolate the
// goal-hold guarantee under test here.
func TestGoalHoldsAwaiting_RestoredActiveGoalNoStartupKick(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	now := time.Now()
	meta := schema.SessionMeta{
		ID:        "resume-goal-awaiting",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{}).toSnapshot(),
		Goal: &schema.GoalSnapshot{
			Objective: "finish the migration",
			Status:    string(goal.StatusActive),
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive || snap.Objective != "finish the migration" {
		t.Fatalf("restore must load the active goal, got %+v ok=%v", snap, ok)
	}

	// Wire the kick callback the way bridgeSession does post-restore (production
	// order: restore first, then SetKickFunc) and confirm restore itself kicked
	// nothing before this callback even existed.
	kicked := 0
	sess.SetKickFunc(func(string) { kicked++ })
	if kicked != 0 {
		t.Fatalf("kick count immediately after restore + wiring = %d, want 0 (no startup kick)", kicked)
	}

	// Force the awaiting state (state-from-transcript re-derivation is a
	// separate seam) and confirm a drain-tail settle still holds.
	sess.mu.Lock()
	sess.state = SessionAwaiting
	sess.mu.Unlock()

	sess.settleGoalOnIdle()
	if kicked != 0 {
		t.Fatalf("kick count while the restored session stays awaiting = %d, want 0 (no startup kick)", kicked)
	}
	snap, ok = sess.getOrCreateGoalStore().Snapshot()
	if !ok || snap.Status != goal.StatusActive {
		t.Fatalf("goal must remain active while held, got %+v ok=%v", snap, ok)
	}

	// A reply resolves the ask: the session leaves awaiting, and the next
	// drain-tail settle kicks the restored goal.
	sess.mu.Lock()
	sess.state = SessionIdle
	sess.mu.Unlock()
	sess.settleGoalOnIdle()
	if kicked != 1 {
		t.Fatalf("kick count after leaving awaiting = %d, want exactly 1 (the restored goal resumes)", kicked)
	}
}

// TestGoalHoldsAwaiting_NoProgressBreakerUnaffected proves the awaiting span
// itself never counts against the no-progress breaker. RecordContinuation
// runs only from armGoalContinuation's wasContinuation branch: the asking
// continuation never reaches it (Task 5's skipGoalGate skips the fold while
// awaiting — see TestGoalHoldsAwaiting_ContinuationEndsViaAsk), so
// Iterations/NoProgressStreak read right after the ask must equal their value
// from before it started. The reply then resolves the ask and the resumed
// continuation completes the goal via update_goal — that continuation's own
// fold is ALSO skipped, because update_goal has already moved the goal off
// StatusActive before the gate runs (mirroring TestArmGoalContinuation's
// existing precedent for a model-declared terminal status) — so the counters
// must still be unchanged after the entire ask-to-reply-to-completion cascade.
func TestGoalHoldsAwaiting_NoProgressBreakerUnaffected(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	completeGoal := llm.ToolCallData{ID: "ug1", Name: "update_goal", Arguments: json.RawMessage(`{"status":"complete"}`), Type: "function"}
	comm := communicateCall("c1", "goal complete")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return finalResponse("thanks, going with Postgres") },
			func(req llm.Request) llm.Response { return toolCallResponse(completeGoal, comm) },
		},
	}
	sess := newSession(t, withAdapter(f))
	sess.getOrCreateGoalStore().Set("ship the feature", time.Now())

	before, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("precondition: goal must be set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInputKind(ctx, "continue toward the goal", nil, EntryContinuation); err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after the asking continuation = %q, want %q", got, SessionAwaiting)
	}
	afterAsk, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing after the ask")
	}
	if afterAsk.Iterations != before.Iterations || afterAsk.NoProgressStreak != before.NoProgressStreak {
		t.Fatalf("snapshot after the ask = %+v, want unchanged from before = %+v", afterAsk, before)
	}

	if _, err := sess.ProcessInput(ctx, "let's go with Postgres", nil); err != nil {
		t.Fatalf("reply ProcessInput: %v", err)
	}

	afterReply, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("goal snapshot missing after the reply")
	}
	if afterReply.Status != goal.StatusComplete {
		t.Fatalf("status after the reply = %q, want complete (the resumed continuation must have run)", afterReply.Status)
	}
	if afterReply.Iterations != before.Iterations || afterReply.NoProgressStreak != before.NoProgressStreak {
		t.Fatalf("snapshot after the reply = %+v, want Iterations/NoProgressStreak unchanged from before = %+v across the whole awaiting span (both the asking and the completing continuation short-circuit before RecordContinuation)", afterReply, before)
	}
	if got := len(f.Requests()); got != 3 {
		t.Fatalf("requests = %d, want 3 (ask + reply + the resumed completing continuation)", got)
	}
}
