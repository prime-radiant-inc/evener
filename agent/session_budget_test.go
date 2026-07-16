package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/goal"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func newBudgetSession(
	t *testing.T,
	cfg SessionConfig,
	responder func(llm.Request) llm.Response,
) (*Session, *agenttest.ScriptedAdapter, *llm.Client) {
	t.Helper()
	if responder == nil {
		responder = func(llm.Request) llm.Response {
			return communicateResponse(true, "done")
		}
	}
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: responder}
	client := llm.NewClient()
	client.Register(adapter)
	cfg.NoProjectPrompts = true
	cfg.clock = agenttest.NewFakeClock()
	cfg.testOnly.skipGitSnapshot = true
	cfg.testOnly.minimalSystemPrompt = true
	cfg.testOnly.noSyncJobStore = true
	sess, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		cfg,
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, adapter, client
}

func restoreBudgetSession(
	t *testing.T,
	client *llm.Client,
	meta schema.SessionMeta,
	stateDir string,
	resumeHistory []schema.Turn,
) *Session {
	t.Helper()
	restoreCfg := RestoreSessionConfig{
		StateDir:      stateDir,
		resumeHistory: resumeHistory,
		clock:         agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}
	sess, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		restoreCfg,
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

func budgetHistory(sess *Session) []schema.Turn {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return append([]schema.Turn(nil), sess.history...)
}

func countBudgetSteering(history []schema.Turn, text string) int {
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && turn.Message.Text() == text {
			count++
		}
	}
	return count
}

func requestHasExactUserMessage(req llm.Request, text string) bool {
	for _, message := range req.Messages {
		if message.Role == llm.RoleUser && message.Text() == text {
			return true
		}
	}
	return false
}

func requireBudgetExhaustion(
	t *testing.T,
	err error,
	wantBudget exhaustedBudget,
	wantLimit int,
	wantResumable bool,
) *budgetExhaustionError {
	t.Helper()
	var exhausted *budgetExhaustionError
	if !errors.As(err, &exhausted) {
		t.Fatalf("error = %v, want budgetExhaustionError", err)
	}
	if exhausted.Budget != wantBudget || exhausted.Limit != wantLimit || exhausted.Resumable != wantResumable {
		t.Fatalf(
			"budget exhaustion = %+v, want budget=%q limit=%d resumable=%t",
			exhausted,
			wantBudget,
			wantLimit,
			wantResumable,
		)
	}
	wantReason := "tool_round_budget_exhausted"
	if wantBudget == exhaustedBudgetTurns {
		wantReason = "turn_budget_exhausted"
	}
	if got := exhausted.reason(); got != wantReason {
		t.Fatalf("budget exhaustion reason = %q, want %q", got, wantReason)
	}
	if got, want := exhausted.Error(), fmt.Sprintf("%s exhausted at limit %d", wantBudget, wantLimit); got != want {
		t.Fatalf("budget exhaustion error = %q, want %q", got, want)
	}
	return exhausted
}

func requireGoalBudgetStateUnchanged(t *testing.T, before, after goal.Snapshot) {
	t.Helper()
	if after.Objective != before.Objective ||
		after.Status != before.Status ||
		after.Iterations != before.Iterations ||
		after.NoProgressStreak != before.NoProgressStreak ||
		after.StopReason != before.StopReason {
		t.Fatalf("goal after budget exhaustion = %+v, want unchanged from %+v", after, before)
	}
}

func nonterminalBudgetResponse(callID, text string) llm.Response {
	resp := communicateResponse(false, text)
	resp.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentText, Text: text}}, resp.Message.Content...)
	resp.Message.Content[len(resp.Message.Content)-1].ToolCall.ID = callID
	return resp
}

func TestSession_TurnBudgetWarningAtFiveRemainingOnceAndDoesNotConsumeTurn(t *testing.T) {
	t.Parallel()
	sess, adapter, _ := newBudgetSession(t, SessionConfig{MaxTurns: 7}, nil)

	for _, input := range []string{"first", "second"} {
		if _, err := sess.ProcessInput(context.Background(), input, nil); err != nil {
			t.Fatalf("ProcessInput(%q): %v", input, err)
		}
	}
	sess.mu.Lock()
	acceptedBefore := sess.turns
	sess.mu.Unlock()
	if acceptedBefore != 2 {
		t.Fatalf("accepted turns before threshold input = %d, want 2", acceptedBefore)
	}

	if _, err := sess.ProcessInput(context.Background(), "third", nil); err != nil {
		t.Fatalf("threshold ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("model requests = %d, want 3", len(requests))
	}
	if !requestHasExactUserMessage(requests[2], rootTurnBudgetWarning) {
		t.Fatalf("threshold request missing root warning user message: %+v", requests[2].Messages)
	}
	if got := countBudgetSteering(budgetHistory(sess), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("root warning count = %d, want 1", got)
	}
	sess.mu.Lock()
	acceptedAfter := sess.turns
	sess.mu.Unlock()
	if acceptedAfter != acceptedBefore+1 {
		t.Fatalf("accepted turns = %d, want %d; warning consumed a turn", acceptedAfter, acceptedBefore+1)
	}
}

func TestSession_TurnBudgetWarningAfterThresholdOnNextAcceptedTurn(t *testing.T) {
	t.Parallel()
	_, adapter, client := newBudgetSession(t, SessionConfig{}, nil)
	meta := schema.SessionMeta{
		ID:                 "01KBUDGETAFTERTHRESHOLD",
		ProfileID:          "openai",
		Model:              "gpt-5.2",
		Config:             (SessionConfig{MaxTurns: 7}).toSnapshot(),
		AcceptedInputTurns: 4,
	}
	sess := restoreBudgetSession(t, client, meta, "", []schema.Turn{})

	if _, err := sess.ProcessInput(context.Background(), "next accepted", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	if !requestHasExactUserMessage(requests[0], rootTurnBudgetWarning) {
		t.Fatalf("post-threshold request missing root warning user message: %+v", requests[0].Messages)
	}
	if got := countBudgetSteering(budgetHistory(sess), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("root warning count = %d, want 1", got)
	}
	sess.mu.Lock()
	acceptedAfter := sess.turns
	sess.mu.Unlock()
	if acceptedAfter != 5 {
		t.Fatalf("restored accepted turns after input = %d, want 5", acceptedAfter)
	}
}

func TestSession_TurnBudgetWarningRestoresWithoutDuplication(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sess, adapter, client := newBudgetSession(t, SessionConfig{MaxTurns: 7, StateDir: stateDir}, nil)
	for _, input := range []string{"first", "second", "third"} {
		if _, err := sess.ProcessInput(context.Background(), input, nil); err != nil {
			t.Fatalf("ProcessInput(%q): %v", input, err)
		}
	}
	if got := countBudgetSteering(budgetHistory(sess), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("pre-restore root warning count = %d, want 1", got)
	}
	meta := sess.Meta()
	if meta.AcceptedInputTurns != 3 || !meta.TurnBudgetWarningEmitted {
		t.Fatalf("warning metadata = accepted:%d emitted:%t, want accepted:3 emitted:true", meta.AcceptedInputTurns, meta.TurnBudgetWarningEmitted)
	}
	meta.TurnBudgetWarningEmitted = false
	sess.Close()

	restored := restoreBudgetSession(t, client, meta, stateDir, nil)
	if _, err := restored.ProcessInput(context.Background(), "after restore", nil); err != nil {
		t.Fatalf("restored ProcessInput: %v", err)
	}
	if got := countBudgetSteering(budgetHistory(restored), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("restored root warning count = %d, want 1", got)
	}
	restored.mu.Lock()
	acceptedAfterRestore := restored.turns
	restored.mu.Unlock()
	if acceptedAfterRestore != 4 {
		t.Fatalf("accepted turns after transcript restore = %d, want 4", acceptedAfterRestore)
	}
	if got := len(adapter.Requests()); got != 4 {
		t.Fatalf("model requests across restore = %d, want 4", got)
	}
}

func TestSession_TurnBudgetWarningRootAndChildWording(t *testing.T) {
	t.Parallel()
	t.Run("root", func(t *testing.T) {
		sess, _, _ := newBudgetSession(t, SessionConfig{MaxTurns: 5}, nil)
		if _, err := sess.ProcessInput(context.Background(), "root input", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		history := budgetHistory(sess)
		if got := countBudgetSteering(history, rootTurnBudgetWarning); got != 1 {
			t.Fatalf("root warning count = %d, want 1", got)
		}
		if got := countBudgetSteering(history, childTurnBudgetWarning); got != 0 {
			t.Fatalf("child warning count in root = %d, want 0", got)
		}
	})

	t.Run("spawned child", func(t *testing.T) {
		cfg := SessionConfig{MaxTurns: 5}
		cfg.spawn.parentSessionID = "parent-session"
		sess, _, _ := newBudgetSession(t, cfg, nil)
		if _, err := sess.ProcessInput(context.Background(), "child input", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		history := budgetHistory(sess)
		if got := countBudgetSteering(history, childTurnBudgetWarning); got != 1 {
			t.Fatalf("child warning count = %d, want 1", got)
		}
		if got := countBudgetSteering(history, rootTurnBudgetWarning); got != 0 {
			t.Fatalf("root warning count in child = %d, want 0", got)
		}
	})

	t.Run("restored child", func(t *testing.T) {
		_, _, client := newBudgetSession(t, SessionConfig{}, nil)
		meta := schema.SessionMeta{
			ID:         "01KBUDGETRESTOREDCHILD00",
			ProfileID:  "openai",
			Model:      "gpt-5.2",
			Config:     (SessionConfig{MaxTurns: 5}).toSnapshot(),
			IsSubagent: true,
		}
		sess := restoreBudgetSession(t, client, meta, "", []schema.Turn{})
		if _, err := sess.ProcessInput(context.Background(), "restored child input", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		history := budgetHistory(sess)
		if got := countBudgetSteering(history, childTurnBudgetWarning); got != 1 {
			t.Fatalf("restored child warning count = %d, want 1", got)
		}
		if got := countBudgetSteering(history, rootTurnBudgetWarning); got != 0 {
			t.Fatalf("root warning count in restored child = %d, want 0", got)
		}
	})
}

func TestSession_UnlimitedNeverWarns(t *testing.T) {
	t.Parallel()
	sess, _, _ := newBudgetSession(t, SessionConfig{MaxTurns: 0}, nil)
	for i := 0; i < 8; i++ {
		if _, err := sess.ProcessInput(context.Background(), fmt.Sprintf("input %d", i), nil); err != nil {
			t.Fatalf("ProcessInput(%d): %v", i, err)
		}
	}
	history := budgetHistory(sess)
	if got := countBudgetSteering(history, rootTurnBudgetWarning); got != 0 {
		t.Fatalf("root warning count = %d, want 0", got)
	}
	if got := countBudgetSteering(history, childTurnBudgetWarning); got != 0 {
		t.Fatalf("child warning count = %d, want 0", got)
	}
	sess.mu.Lock()
	accepted := sess.turns
	sess.mu.Unlock()
	if accepted != 8 {
		t.Fatalf("accepted turns = %d, want 8", accepted)
	}
}

func TestSession_BudgetExhaustionLeavesActiveGoalUnchanged(t *testing.T) {
	t.Parallel()
	t.Run("lifetime turns", func(t *testing.T) {
		sess, adapter, _ := newBudgetSession(t, SessionConfig{MaxTurns: 1}, nil)
		sess.mu.Lock()
		sess.turns = 1
		sess.mu.Unlock()
		store := sess.getOrCreateGoalStore()
		store.Set("ship it", sess.sclock().Now())
		before, ok := store.Snapshot()
		if !ok {
			t.Fatal("goal snapshot missing before exhaustion")
		}

		out, err := sess.processInputKindWithProvenance(context.Background(), "over budget", nil, EntryUserInput, nil)
		if out != "" {
			t.Fatalf("lifetime exhaustion output = %q, want empty", out)
		}
		requireBudgetExhaustion(t, err, exhaustedBudgetTurns, 1, false)
		if got := len(adapter.Requests()); got != 0 {
			t.Fatalf("model requests = %d, want 0", got)
		}
		after, ok := store.Snapshot()
		if !ok {
			t.Fatal("goal snapshot missing after exhaustion")
		}
		requireGoalBudgetStateUnchanged(t, before, after)
	})

	t.Run("tool rounds", func(t *testing.T) {
		calls := 0
		responder := func(llm.Request) llm.Response {
			calls++
			return nonterminalBudgetResponse(fmt.Sprintf("tool-budget-%d", calls), "partial tool-round output")
		}
		sess, adapter, _ := newBudgetSession(t, SessionConfig{MaxToolRoundsPerInput: 1}, responder)
		store := sess.getOrCreateGoalStore()
		store.Set("ship it", sess.sclock().Now())
		before, ok := store.Snapshot()
		if !ok {
			t.Fatal("goal snapshot missing before exhaustion")
		}

		out, err := sess.processInputKindWithProvenance(context.Background(), "use tools", nil, EntryUserInput, nil)
		if out != "partial tool-round output" {
			t.Fatalf("tool-round exhaustion output = %q, want partial output", out)
		}
		requireBudgetExhaustion(t, err, exhaustedBudgetToolRounds, 1, true)
		if got := len(adapter.Requests()); got != 1 {
			t.Fatalf("model requests = %d, want 1", got)
		}
		after, ok := store.Snapshot()
		if !ok {
			t.Fatal("goal snapshot missing after exhaustion")
		}
		requireGoalBudgetStateUnchanged(t, before, after)
	})
}

func TestSession_GoalRoundCapExitIsNotToolBudgetExhaustion(t *testing.T) {
	t.Parallel()
	for _, configured := range []int{-1, 200} {
		t.Run(fmt.Sprintf("configured_%d", configured), func(t *testing.T) {
			calls := 0
			lastPartial := ""
			responder := func(llm.Request) llm.Response {
				calls++
				lastPartial = fmt.Sprintf("partial round %d", calls)
				return nonterminalBudgetResponse(fmt.Sprintf("goal-round-%d", calls), lastPartial)
			}
			sess, adapter, _ := newBudgetSession(t, SessionConfig{MaxToolRoundsPerInput: configured}, responder)
			store := sess.getOrCreateGoalStore()
			store.Set("ship it", sess.sclock().Now())
			before, ok := store.Snapshot()
			if !ok {
				t.Fatal("goal snapshot missing before goal round cap")
			}

			out, progressed, err := sess.processOneInput(context.Background(), "continue", nil, EntryContinuation, nil)
			if err != nil {
				t.Fatalf("processOneInput error = %v, want nil", err)
			}
			if out != lastPartial || out == "" {
				t.Fatalf("partial output = %q, want final non-empty partial %q", out, lastPartial)
			}
			if progressed {
				t.Fatal("non-terminal communicate calls unexpectedly counted as mutating progress")
			}
			if got := len(adapter.Requests()); got != goal.GoalTurnMaxRounds {
				t.Fatalf("model requests = %d, want %d", got, goal.GoalTurnMaxRounds)
			}
			after, ok := store.Snapshot()
			if !ok || after.Status != goal.StatusActive {
				t.Fatalf("goal after round cap = %+v ok=%t, want active", after, ok)
			}
			requireGoalBudgetStateUnchanged(t, before, after)
		})
	}
}
