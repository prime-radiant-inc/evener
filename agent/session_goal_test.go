package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func nextGoalUpdated(t *testing.T, sess *Session) events.GoalUpdatedData {
	t.Helper()
	// TRIPWIRE: mutations emit synchronously; one second only bounds a wedged regression.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("session event stream closed before GOAL_UPDATED")
			}
			if ev.Kind != events.EventGoalUpdated {
				continue
			}
			data, ok := ev.Data.(events.GoalUpdatedData)
			if !ok {
				t.Fatalf("GOAL_UPDATED payload = %T, want events.GoalUpdatedData", ev.Data)
			}
			return data
		case <-timer.C:
			t.Fatal("timed out waiting for GOAL_UPDATED")
		}
	}
}

func assertNoGoalUpdated(t *testing.T, sess *Session) {
	t.Helper()
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return
			}
			if ev.Kind == events.EventGoalUpdated {
				t.Fatalf("unexpected extra GOAL_UPDATED: %+v", ev.Data)
			}
		default:
			return
		}
	}
}

func assertGoalUpdatedMatchesStore(t *testing.T, sess *Session, data events.GoalUpdatedData) {
	t.Helper()
	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		if data.Goal != nil {
			t.Fatalf("GOAL_UPDATED goal = %+v, want nil clear matching empty store", data.Goal)
		}
		return
	}
	if data.Goal == nil {
		t.Fatalf("GOAL_UPDATED goal = nil, want %+v", snap)
	}
	if data.Goal.Objective != snap.Objective || data.Goal.Status != string(snap.Status) || data.Goal.Iterations != snap.Iterations {
		t.Fatalf("GOAL_UPDATED goal = %+v, want store snapshot %+v", data.Goal, snap)
	}
}

func TestGoalUpdatedEventSetClearAndContinuation(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	if _, err := sess.SetGoal(context.Background(), "ship focus sentence"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)

	if _, continued := sess.armGoalContinuation(true, true); !continued {
		t.Fatal("active progressing goal should continue")
	}
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)

	sess.ClearGoal()
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
}

func TestGoalUpdatedEventErrorBlocking(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	if _, err := sess.SetGoal(context.Background(), "survive failure"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	_ = nextGoalUpdated(t, sess)

	sess.terminateGoalOnError(context.Background(), errors.New("provider failed"))
	assertGoalUpdatedMatchesStore(t, sess, nextGoalUpdated(t, sess))
	assertNoGoalUpdated(t, sess)
}

func TestGoalUpdatedEmissionDoesNotHoldSessionMutex(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	checked := make(chan bool, 1)
	sess.descendantEvent = func(ev events.SessionEvent) {
		if ev.Kind != events.EventGoalUpdated {
			return
		}
		unlocked := sess.mu.TryLock()
		if unlocked {
			sess.mu.Unlock()
		}
		checked <- unlocked
	}

	if _, err := sess.SetGoal(context.Background(), "check lock boundary"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if unlocked := <-checked; !unlocked {
		t.Fatal("GOAL_UPDATED was emitted while Session.mu was held")
	}
}

func TestConcurrentGoalMutationsEmitInCommittedOrder(t *testing.T) {
	sess := newGoalMethodSession(t)
	defer sess.Close()

	type observation struct {
		objective          string
		sessionMuUnlocked  bool
		goalSerializerHeld bool
	}
	observed := make(chan observation, 2)
	releaseFirstEmission := make(chan struct{})
	var firstEmission sync.Once
	sess.descendantEvent = func(ev events.SessionEvent) {
		if ev.Kind != events.EventGoalUpdated {
			return
		}
		data, ok := ev.Data.(events.GoalUpdatedData)
		if !ok || data.Goal == nil {
			return
		}

		sessionMuUnlocked := sess.mu.TryLock()
		if sessionMuUnlocked {
			sess.mu.Unlock()
		}
		goalSerializerHeld := !sess.goalUpdateMu.TryLock()
		if !goalSerializerHeld {
			sess.goalUpdateMu.Unlock()
		}

		block := false
		firstEmission.Do(func() { block = true })
		observed <- observation{
			objective:          data.Goal.Objective,
			sessionMuUnlocked:  sessionMuUnlocked,
			goalSerializerHeld: goalSerializerHeld,
		}
		if block {
			<-releaseFirstEmission
		}
	}

	type mutationResult struct {
		objective string
		err       error
	}
	mutate := func(objective string, started chan<- struct{}) <-chan mutationResult {
		done := make(chan mutationResult, 1)
		go func() {
			if started != nil {
				started <- struct{}{}
			}
			_, err := sess.SetGoal(context.Background(), objective)
			done <- mutationResult{objective: objective, err: err}
		}()
		return done
	}

	firstDone := mutate("first committed objective", nil)
	firstObserved := <-observed
	firstCommitted, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("first concurrent mutation did not commit a goal")
	}

	secondStarted := make(chan struct{})
	secondDone := mutate("second committed objective", secondStarted)
	<-secondStarted
	close(releaseFirstEmission)

	firstResult := <-firstDone
	secondResult := <-secondDone
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent SetGoal results = %q: %v, %q: %v", firstResult.objective, firstResult.err, secondResult.objective, secondResult.err)
	}
	secondObserved := <-observed
	secondCommitted, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("second concurrent mutation did not commit a goal")
	}

	committedOrder := []string{firstCommitted.Objective, secondCommitted.Objective}
	observedOrder := []string{firstObserved.objective, secondObserved.objective}
	if !slices.Equal(observedOrder, committedOrder) {
		t.Fatalf("GOAL_UPDATED order = %q, committed mutation order = %q", observedOrder, committedOrder)
	}
	for i, got := range []observation{firstObserved, secondObserved} {
		if !got.sessionMuUnlocked {
			t.Fatalf("GOAL_UPDATED %d (%q) was emitted while Session.mu was held", i, got.objective)
		}
		if !got.goalSerializerHeld {
			t.Fatalf("GOAL_UPDATED %d (%q) was emitted after goalUpdateMu was released", i, got.objective)
		}
	}
}

// newGoalMethodSession builds an idle session (no events drained) for testing the
// SetGoal/ClearGoal Session methods directly.
func newGoalMethodSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess
}

// TestSetGoalRejectsEmpty: SetGoal rejects an empty or whitespace objective and
// does not store a goal.
func TestSetGoalRejectsEmpty(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	for _, obj := range []string{"", "   ", "\t\n"} {
		started, err := sess.SetGoal(context.Background(), obj)
		if err == nil {
			t.Fatalf("SetGoal(%q) should reject empty objective", obj)
		}
		if started {
			t.Fatalf("SetGoal(%q) should not report started", obj)
		}
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("SetGoal with empty objective must not store a goal")
	}
}

// TestSetGoalIdleKicks: on an idle session with a kick callback wired, SetGoal
// stores the goal, kicks with the rendered first continuation prompt, and reports
// started=true.
func TestSetGoalIdleKicks(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	var mu sync.Mutex
	var kicks []string
	sess.SetKickFunc(func(prompt string) {
		mu.Lock()
		kicks = append(kicks, prompt)
		mu.Unlock()
	})

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if !started {
		t.Fatal("SetGoal on an idle session with a kick wired should report started=true")
	}

	snap, ok := sess.getOrCreateGoalStore().Snapshot()
	if !ok {
		t.Fatal("SetGoal should store an active goal")
	}
	if snap.Objective != "ship the feature" || snap.Status != goal.StatusActive {
		t.Fatalf("stored goal = {Objective:%q Status:%q}, want {ship the feature, active}", snap.Objective, snap.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(kicks) != 1 {
		t.Fatalf("kick count = %d, want exactly 1", len(kicks))
	}
	if kicks[0] != goal.Render("ship the feature") {
		t.Fatalf("kick prompt = %q, want the rendered continuation prompt", kicks[0])
	}
}

// TestSetGoalInTurnDefersToGate: while a turn is running (goalInTurn set), SetGoal
// stores the goal but does NOT kick — the running drain-loop gate will pick it up
// — and reports started=false.
func TestSetGoalInTurnDefersToGate(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	var kicked bool
	sess.SetKickFunc(func(prompt string) { kicked = true })

	// Simulate an in-flight turn.
	sess.mu.Lock()
	sess.goalInTurn = true
	sess.mu.Unlock()

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal during a running turn should report started=false (the gate backs it)")
	}
	if kicked {
		t.Fatal("SetGoal during a running turn must NOT kick")
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("SetGoal should still store the goal even when deferring to the gate")
	}
}

// TestSetGoalNoKickFuncDoesNotStart: with no kick callback wired, an idle SetGoal
// stores the goal but reports started=false (there is no way to start it
// immediately; a later turn's gate is the backstop).
func TestSetGoalNoKickFuncDoesNotStart(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	started, err := sess.SetGoal(context.Background(), "ship the feature")
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if started {
		t.Fatal("SetGoal with no kick callback should report started=false")
	}
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("SetGoal should still store the goal")
	}
}

// TestClearGoalRemovesGoal: ClearGoal removes a previously set goal.
func TestClearGoalRemovesGoal(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("some objective", time.Now())
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); !ok {
		t.Fatal("precondition: goal should be set")
	}

	sess.ClearGoal()
	if _, ok := sess.getOrCreateGoalStore().Snapshot(); ok {
		t.Fatal("ClearGoal should remove the goal")
	}
}

func TestGoalStatusReturnsStatusAndIterations(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set("ship the footer", time.Now())
	status, iterations, ok := sess.GoalStatus()
	if !ok || status != "active" || iterations != 0 {
		t.Fatalf("GoalStatus() = %q, %d, %v", status, iterations, ok)
	}
	meta := sess.Meta()
	if meta.Goal == nil || meta.Goal.Objective != "ship the footer" {
		t.Fatalf("Meta().Goal = %+v, want persisted objective", meta.Goal)
	}
}

// lastSteeringText returns the text of the final TurnSteering turn in the
// session history, or "" with ok=false when the last turn is not steering.
func lastSteeringText(sess *Session) (string, bool) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.history) == 0 {
		return "", false
	}
	last := sess.history[len(sess.history)-1]
	if last.Kind != schema.TurnSteering {
		return "", false
	}
	return last.Message.Text(), true
}

// compactSession builds a session with enough history to force a real
// compaction and returns it. The model is scripted to answer the LLM
// summarization call. PreserveRecentTurns is 1 so the trailing turns survive.
func compactSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("forced summary") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.contextMgr.PreserveRecentTurns = 1
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("I will inspect the project and report back with enough detail to become a working note.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	}
	return sess
}

// TestCompactionReInjectsActiveGoalObjective: with an active goal set, forcing a
// compaction must append a trailing TurnSteering turn carrying the rendered
// objective so the goal survives mid-turn compaction erosion.
func TestCompactionReInjectsActiveGoalObjective(t *testing.T) {
	t.Parallel()
	const objective = "REINJECTMARKER ship the payments refactor end to end"
	sess := compactSession(t)
	defer sess.Close()

	sess.getOrCreateGoalStore().Set(objective, time.Now())

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	text, ok := lastSteeringText(sess)
	if !ok {
		t.Fatalf("post-compaction history must end with a TurnSteering turn; kinds=%s", turnKinds(sess.history))
	}
	if !strings.Contains(text, "REINJECTMARKER") {
		t.Fatalf("trailing steering turn must contain the objective marker; got %q", text)
	}
	// It must be the full rendered continuation prompt, not a bare objective.
	if text != goal.Render(objective) {
		t.Fatalf("trailing steering text is not the rendered continuation prompt")
	}
}

// TestCompactionNoSteeringWithoutActiveGoal: with no goal set, forcing a
// compaction must NOT append a goal steering turn.
func TestCompactionNoSteeringWithoutActiveGoal(t *testing.T) {
	t.Parallel()
	sess := compactSession(t)
	defer sess.Close()

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if _, ok := lastSteeringText(sess); ok {
		t.Fatalf("compaction with no active goal must not append a steering turn; kinds=%s", turnKinds(sess.history))
	}
}

// TestCompactionNoSteeringForTerminalGoal: a terminal (complete/blocked) goal
// must NOT be re-injected on compaction — only an active goal is.
func TestCompactionNoSteeringForTerminalGoal(t *testing.T) {
	t.Parallel()
	sess := compactSession(t)
	defer sess.Close()

	store := sess.getOrCreateGoalStore()
	store.Set("COMPLETEDMARKER done work", time.Now())
	if !store.SetTerminal(goal.StatusComplete, "", time.Now()) {
		t.Fatal("precondition: SetTerminal(complete) should succeed")
	}

	if err := sess.Compact(context.Background()); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if _, ok := lastSteeringText(sess); ok {
		t.Fatalf("compaction with a terminal goal must not append a steering turn; kinds=%s", turnKinds(sess.history))
	}
}

// TestGoalCompactionSteering covers the helper directly: it returns the rendered
// objective for an active goal, and nil for no-goal and terminal-goal states.
func TestGoalCompactionSteering(t *testing.T) {
	t.Parallel()
	sess := newGoalMethodSession(t)
	defer sess.Close()

	if msgs := sess.goalCompactionSteering(); msgs != nil {
		t.Fatalf("no goal: want nil, got %v", msgs)
	}

	store := sess.getOrCreateGoalStore()
	store.Set("STEERMARKER objective", time.Now())
	msgs := sess.goalCompactionSteering()
	if len(msgs) != 1 || msgs[0] != goal.Render("STEERMARKER objective") {
		t.Fatalf("active goal: want one rendered-objective message, got %v", msgs)
	}

	if !store.SetTerminal(goal.StatusBlocked, "stuck", time.Now()) {
		t.Fatal("precondition: SetTerminal(blocked) should succeed")
	}
	if msgs := sess.goalCompactionSteering(); msgs != nil {
		t.Fatalf("terminal goal: want nil, got %v", msgs)
	}
}

// TestProcessInputDelegatesAsUserInput verifies that the unchanged
// ProcessInput(ctx, input, images) entry point still records a user-input turn
// (i.e. it delegates with kind=EntryUserInput). The model is scripted to finish
// the turn immediately via the result tool.
func TestProcessInputDelegatesAsUserInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("all done") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(context.Background(), "do the thing", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	var sawUserInput bool
	for _, turn := range sess.history {
		if turn.Kind == schema.TurnUserInput {
			sawUserInput = true
			break
		}
	}
	if !sawUserInput {
		t.Fatalf("ProcessInput should append a TurnUserInput turn; history kinds=%s", turnKinds(sess.history))
	}
}

// TestAcceptContinuationIsSteeringNotUser verifies that acceptContinuationInput
// appends a TurnSteering turn (not TurnUserInput), does not bump s.turns, emits
// EventGoalContinuation, and does NOT emit EventUserInput.
func TestAcceptContinuationIsSteeringNotUser(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	stop := drainEvents(sess)

	sess.mu.Lock()
	beforeTurns := sess.turns
	sess.mu.Unlock()

	sess.acceptContinuationInput(context.Background(), "CONTINUE", "")

	sess.mu.Lock()
	afterTurns := sess.turns
	last := sess.history[len(sess.history)-1]
	sess.mu.Unlock()

	if afterTurns != beforeTurns {
		t.Fatalf("acceptContinuationInput must not increment s.turns: before=%d after=%d", beforeTurns, afterTurns)
	}
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last history turn kind = %v, want TurnSteering", last.Kind)
	}

	sess.Close()
	evs := stop()

	var sawContinuation, sawUserInput bool
	for _, ev := range evs {
		switch ev.Kind {
		case events.EventGoalContinuation:
			sawContinuation = true
			if d, ok := ev.Data.(events.GoalContinuationData); ok {
				// No goal is set in this test, so the compact marker falls back to
				// the constant rather than echoing the raw input (/par B6).
				if d.Text != goalContinuationMarker {
					t.Fatalf("EventGoalContinuation text = %q, want %q", d.Text, goalContinuationMarker)
				}
			} else {
				t.Fatalf("EventGoalContinuation carried %T, want GoalContinuationData", ev.Data)
			}
		case events.EventUserInput:
			sawUserInput = true
		}
	}
	if !sawContinuation {
		t.Fatal("acceptContinuationInput should emit EventGoalContinuation")
	}
	if sawUserInput {
		t.Fatal("acceptContinuationInput must NOT emit EventUserInput")
	}
}

// TestContinuationEventOmitsFullPrompt pins /par B6: the EventGoalContinuation that
// drives the web systemMessage projection must carry a compact marker (the
// objective), never the full ~2.5KB rendered continuation prompt, which would flood
// the thread every iteration. The model still gets the full prompt via the steering
// turn (asserted by the last-history check).
func TestContinuationEventOmitsFullPrompt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	const objective = "make the tests pass"
	sess.getOrCreateGoalStore().Set(objective, time.Now())

	stop := drainEvents(sess)

	fullPrompt := "BEGIN " + strings.Repeat("scaffolding ", 300) + " END" // ~3.6KB
	sess.acceptContinuationInput(context.Background(), fullPrompt, "")

	sess.mu.Lock()
	last := sess.history[len(sess.history)-1]
	sess.mu.Unlock()
	sess.Close()
	evs := stop()

	var marker string
	var found bool
	for _, ev := range evs {
		if ev.Kind == events.EventGoalContinuation {
			d, ok := ev.Data.(events.GoalContinuationData)
			if !ok {
				t.Fatalf("EventGoalContinuation carried %T, want GoalContinuationData", ev.Data)
			}
			marker, found = d.Text, true
		}
	}
	if !found {
		t.Fatal("expected an EventGoalContinuation")
	}
	// The marker must contain the objective so the UI surfaces which goal is running,
	// but must not re-encode the exact prefix format (that is presentation, not behaviour).
	if !strings.Contains(marker, objective) {
		t.Fatalf("marker %q does not contain the objective %q", marker, objective)
	}
	if strings.Contains(marker, "scaffolding") {
		t.Fatalf("marker leaked the full prompt: %q", marker)
	}
	// The model must still receive the full prompt as the steering turn.
	if !strings.Contains(last.Message.Text(), "scaffolding") {
		t.Fatal("steering turn did not carry the full continuation prompt to the model")
	}
}
