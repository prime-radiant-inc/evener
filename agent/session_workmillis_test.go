package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// TestWorkMillis_CompletedTurnCounts pins the WS2 A4 core accumulation
// behavior: a clean, completed turn's wall-clock elapsed — from the
// processing-begin transition (processOneInput) to the terminal boundary
// (finishProcessingAtBoundary, reached here via deliverIfCommunicated) — is
// added to Session.workMillis and surfaced via Meta().WorkMillis.
func TestWorkMillis_CompletedTurnCounts(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	const delta = 2500 * time.Millisecond
	sess := newSession(t, withConfig(SessionConfig{clock: clk}), withSteps(
		func(req llm.Request) llm.Response {
			clk.Advance(delta)
			return finalResponse("ok")
		},
	))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	if got, want := sess.Meta().WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("WorkMillis = %d, want %d", got, want)
	}
}

// TestWorkMillis_InterruptedTurnCounts pins Decision 4: an interrupted turn's
// elapsed wall-clock still counts. The turn is cut short by cancelling the
// caller's context mid-flight (the blockingAdapter blocks until ctx.Done());
// the interrupt path (processInputKindWithProvenance) reaches
// finishProcessingAtBoundary the same as a clean completion.
func TestWorkMillis_InterruptedTurnCounts(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	blocked := make(chan struct{})
	sess := newSession(t, withConfig(SessionConfig{clock: clk}),
		withAdapter(&blockingAdapter{name: "openai", blocked: blocked}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "hello", nil)
		done <- err
	}()

	<-blocked // LLM call is in-flight.
	const delta = 1500 * time.Millisecond
	clk.Advance(delta)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ProcessInput err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after cancel")
	}

	if got, want := sess.Meta().WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("WorkMillis = %d, want %d", got, want)
	}
}

// TestWorkMillis_FailedTurnCounts drives a terminal, recoverable model error
// (not a cancellation, not a close-triggering unrecoverable error) and asserts
// WorkMillis still advances. LLMRetryPolicy.MaxRetries=0 makes the plain
// errors.New("boom") — which Classify treats as retryable — surface after
// exactly one attempt (matching the documented recipe in
// TestCancelAgent_GenuineFailureRacingCancelStaysFailed), so the adapter's
// single clock-advancing step runs exactly once with no retry sleeps.
func TestWorkMillis_FailedTurnCounts(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	const delta = 800 * time.Millisecond
	noRetry := llm.RetryPolicy{MaxRetries: 0}
	sess := newSession(t, withConfig(SessionConfig{clock: clk, LLMRetryPolicy: &noRetry}),
		withAdapter(&fakeErrAdapter{
			name: "openai",
			steps: []func(req llm.Request) (llm.Response, error){
				func(req llm.Request) (llm.Response, error) {
					clk.Advance(delta)
					return llm.Response{}, errors.New("boom")
				},
			},
		}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err == nil {
		t.Fatal("ProcessInput: want error, got nil")
	}
	if got := sess.State(); got == SessionClosed {
		t.Fatalf("State() = %s, want session to stay open on a recoverable model error", got)
	}

	if got, want := sess.Meta().WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("WorkMillis = %d, want %d", got, want)
	}
}

// TestWorkMillis_MultiTurnDrainEachCounts drives a user input followed by a
// queued FollowUp (two processOneInput calls in the same ProcessInput drain
// loop) and asserts each drained turn's elapsed contributes — not just the
// last one.
func TestWorkMillis_MultiTurnDrainEachCounts(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	const delta1 = 1000 * time.Millisecond
	const delta2 = 1500 * time.Millisecond
	sess := newSession(t, withConfig(SessionConfig{clock: clk}), withSteps(
		func(req llm.Request) llm.Response {
			clk.Advance(delta1)
			return finalResponse("first reply")
		},
		func(req llm.Request) llm.Response {
			clk.Advance(delta2)
			return finalResponse("second reply")
		},
	))
	sess.FollowUp("do second")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do first", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	want := (delta1 + delta2).Milliseconds()
	if got := sess.Meta().WorkMillis; got != want {
		t.Fatalf("WorkMillis = %d, want %d (sum of both turns)", got, want)
	}
}

// TestWorkMillis_CloseMidTurnCounts pins the L3 regression: when a terminal
// model error (or any other path) causes Close() to fire while the session is
// still SessionProcessing with turnStartedAt set, Close() must accumulate the
// dying turn's elapsed BEFORE flipping to SessionClosed — otherwise that
// turn's work silently vanishes (finishProcessingAtBoundary never runs on this
// path, since Close() bypasses it).
func TestWorkMillis_CloseMidTurnCounts(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk}))

	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.turnStartedAt = clk.Now()
	sess.mu.Unlock()

	const delta = 3 * time.Second
	clk.Advance(delta)

	sess.Close()

	if got := sess.State(); got != SessionClosed {
		t.Fatalf("State() = %s, want %s", got, SessionClosed)
	}
	if got, want := sess.Meta().WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("WorkMillis = %d, want %d (dying turn's elapsed must count)", got, want)
	}
}

// TestWorkMillis_InterruptThenCloseFlushesToDisk pins the persistence half of
// Decision 4/L3 that TestWorkMillis_InterruptedTurnCounts does not cover: an
// interrupted turn's accumulated work is correct in memory immediately (proven
// by that sibling test), but the accumulation happens in the interrupt-handling
// wrapper (processInputKindWithProvenance), AFTER processOneInput's own
// deferred autosave has already fired with a stale, pre-accumulation value.
// Nothing autosaves again afterward, so if the session is later closed with no
// further turn to trigger another deferred autosave, the correct in-memory
// total never reaches meta.json. Live-reproduced via e2e testing: work_millis
// was present and correct via the API but absent from the on-disk file both
// right after the interrupt and after a subsequent clean shutdown.
func TestWorkMillis_InterruptThenCloseFlushesToDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := agenttest.NewFakeClock()
	blocked := make(chan struct{})
	sess := newSession(t, withConfig(SessionConfig{clock: clk, StateDir: dir}),
		withAdapter(&blockingAdapter{name: "openai", blocked: blocked}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "hello", nil)
		done <- err
	}()

	<-blocked // LLM call is in-flight.
	const delta = 1500 * time.Millisecond
	clk.Advance(delta)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ProcessInput err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessInput did not return after cancel")
	}

	// Sanity check matching TestWorkMillis_InterruptedTurnCounts: the interrupt
	// is already correctly accounted for in memory before Close ever runs.
	if got, want := sess.Meta().WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("in-memory WorkMillis after interrupt = %d, want %d", got, want)
	}

	sess.Close() // e.g. an explicit /shutdown with no further turn.

	reloaded, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got, want := reloaded.WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("on-disk WorkMillis after Close = %d, want %d (Close must autosave the already-accumulated total)", got, want)
	}
}

// TestClose_MidTurnFlushesWorkAndUsageToDisk extends
// TestWorkMillis_CloseMidTurnCounts (which pins only the in-memory L3
// accumulation) to disk: Close must also autosave, or a session that dies
// mid-turn loses both its dying turn's work time and its last recorded token
// usage once the daemon restarts, since restore seeds strictly from the
// persisted meta.json. WorkMillis and CumulativeUsage are both mapped through
// the same Meta()/autosave route, so a turn's usage (recorded mid-round, before
// the turn's own boundary) is exposed to the identical gap.
func TestClose_MidTurnFlushesWorkAndUsageToDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := agenttest.NewFakeClock()
	sess := newSession(t, withConfig(SessionConfig{clock: clk, StateDir: dir}))

	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.turnStartedAt = clk.Now()
	sess.mu.Unlock()
	// Simulate a round's usage already recorded mid-turn (recordResponseUsage),
	// before the turn ever reaches a boundary that would otherwise autosave it.
	sess.contextMgr.AddUsage(llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15})

	const delta = 3 * time.Second
	clk.Advance(delta)

	sess.Close()

	reloaded, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got, want := reloaded.WorkMillis, delta.Milliseconds(); got != want {
		t.Fatalf("on-disk WorkMillis after Close = %d, want %d (dying turn's elapsed must be flushed)", got, want)
	}
	wantUsage := schema.CumulativeUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	if reloaded.CumulativeUsage != wantUsage {
		t.Fatalf("on-disk CumulativeUsage after Close = %+v, want %+v (dying turn's usage must be flushed)", reloaded.CumulativeUsage, wantUsage)
	}
}

// TestRestoreThenTurnAutosaveKeepsPriorTotals is the WS2 K2 integration test:
// restoring a session from a meta carrying prior WorkMillis/CumulativeUsage,
// then running one turn and autosaving, must persist prior-plus-turn totals —
// not overwrite them with turn-only values. This exercises the same
// restore-then-accumulate path as TestRestoreSeedsMetricsIntoMeta (A3) plus
// this task's new accumulation logic together, end to end through disk.
func TestRestoreThenTurnAutosaveKeepsPriorTotals(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := agenttest.NewFakeClock()
	const delta = 2000 * time.Millisecond
	turnUsage := llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				clk.Advance(delta)
				resp := finalResponse("ok")
				resp.Usage = turnUsage
				return resp
			},
		},
	})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())

	meta := schema.SessionMeta{
		ID:         "01RESTOREK2AUTOSAVE000001",
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		CreatedAt:  time.Now().UTC(),
		WorkMillis: 5000,
		CumulativeUsage: schema.CumulativeUsage{
			InputTokens:     100,
			OutputTokens:    200,
			CacheReadTokens: 50,
			TotalTokens:     300,
		},
	}

	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), env, meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	// Restore has no public hook to inject a fake clock (RestoreSessionConfig
	// carries no clock field; applyDefaults always seeds cfg.clock with
	// clock.Real() before construction), so this test overrides the unexported
	// field directly. Safe here: restore ran with deferRestoreSideEffects=false
	// (the default), so every restore-time side effect already ran synchronously
	// above, and the only production goroutine that reads sclock() (the initial
	// prompt namer) is launched from acceptUserInput — i.e. from the
	// ProcessInput call below, strictly after this assignment.
	sess.clock = clk

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	sess.maybeAutoSave()

	reloaded, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}

	wantWorkMillis := int64(5000) + delta.Milliseconds()
	if reloaded.WorkMillis != wantWorkMillis {
		t.Fatalf("reloaded WorkMillis = %d, want %d (prior 5000 + turn %d)", reloaded.WorkMillis, wantWorkMillis, delta.Milliseconds())
	}
	wantUsage := schema.CumulativeUsage{
		InputTokens:     100 + 10,
		OutputTokens:    200 + 5,
		CacheReadTokens: 50, // turn carried no cache-read delta
		TotalTokens:     300 + 15,
	}
	if reloaded.CumulativeUsage != wantUsage {
		t.Fatalf("reloaded CumulativeUsage = %+v, want %+v (prior + turn, not turn-only)", reloaded.CumulativeUsage, wantUsage)
	}
}

// TestForkStartsMetricsAtZero is a PIN test, not new-behavior-driving: it
// documents that ForkSession (fork.go) already builds a fresh child meta that
// never copies the parent's WorkMillis/CumulativeUsage, so a forked child's
// persisted work/token totals start at zero regardless of how much the parent
// had accumulated. Uses buildParentSession from fork_test.go (same package)
// to avoid duplicating that fixture.
func TestForkStartsMetricsAtZero(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	// Give the parent non-zero metrics before forking, so a bug that copied
	// them into the child would be caught rather than passing vacuously.
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	parentMeta.WorkMillis = 5000
	parentMeta.CumulativeUsage = schema.CumulativeUsage{
		InputTokens:     100,
		OutputTokens:    200,
		CacheReadTokens: 50,
		TotalTokens:     300,
	}
	if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
		t.Fatalf("SaveSessionMeta(parent): %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}

	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.WorkMillis != 0 {
		t.Fatalf("child WorkMillis = %d, want 0 (fork must not inherit parent work time)", childMeta.WorkMillis)
	}
	if childMeta.CumulativeUsage != (schema.CumulativeUsage{}) {
		t.Fatalf("child CumulativeUsage = %+v, want zero value (fork must not inherit parent token totals)", childMeta.CumulativeUsage)
	}
}
