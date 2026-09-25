package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// executionAdapter answers model requests from a script of (response, error)
// steps; with the script exhausted it answers a terminal communicate. A step
// may block until its request's context ends.
type executionAdapter struct {
	mu    sync.Mutex
	steps []func(ctx context.Context) (llm.Response, error)
}

func (a *executionAdapter) Name() string { return "openai" }

func (a *executionAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *executionAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	var step func(context.Context) (llm.Response, error)
	if len(a.steps) > 0 {
		step, a.steps = a.steps[0], a.steps[1:]
	}
	a.mu.Unlock()
	resp, err := communicateResponse(true, "done"), error(nil)
	if step != nil {
		resp, err = step(ctx)
	}
	resp.Provider, resp.Model = "openai", req.Model
	return resp, err
}

func (a *executionAdapter) script(steps ...func(ctx context.Context) (llm.Response, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps = append(a.steps, steps...)
}

func newExecutionSession(t *testing.T) (*Session, *executionAdapter) {
	t.Helper()
	adapter := &executionAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	s := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		LLMRetryPolicy:   &llm.RetryPolicy{MaxRetries: 2},
		LLMSleep:         func(context.Context, time.Duration) error { return nil },
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	return s, adapter
}

// entriesByTurn groups a transcript's entries by TurnID, in first-seen order.
func entriesByTurn(entries []schema.Turn) (order []string, byTurn map[string][]schema.Turn) {
	byTurn = map[string][]schema.Turn{}
	for _, entry := range entries {
		if _, seen := byTurn[entry.TurnID]; !seen {
			order = append(order, entry.TurnID)
		}
		byTurn[entry.TurnID] = append(byTurn[entry.TurnID], entry)
	}
	return order, byTurn
}

func transcriptTurnsOf(t *testing.T, s *Session) []schema.Turn {
	t.Helper()
	var turns []schema.Turn
	for _, entry := range transcriptEntries(t, s) {
		turns = append(turns, entry.Turn)
	}
	return turns
}

// requireExecution checks one execution turn's span: it opens with its
// TurnKind and ends with exactly one completion of status.
func requireExecution(t *testing.T, span []schema.Turn, status schema.TurnCompletionStatus) {
	t.Helper()
	if len(span) < 2 || span[0].TurnKind != schema.TurnSpanExecution {
		t.Fatalf("execution span = %d entries, first TurnKind %q", len(span), span[0].TurnKind)
	}
	for i, entry := range span[1:] {
		if entry.TurnKind != "" {
			t.Fatalf("entry %d of the span restamps TurnKind %q", i+1, entry.TurnKind)
		}
	}
	last := span[len(span)-1]
	if last.Kind != schema.TurnCompletion || last.Completion == nil || last.Completion.Status != status {
		t.Fatalf("span ends with %s %+v, want a %s completion", last.Kind, last.Completion, status)
	}
	if last.Completion.DurationMS < 0 || last.Completion.CompletedAt.IsZero() {
		t.Fatalf("completion timing = %+v", last.Completion)
	}
	for _, entry := range span[:len(span)-1] {
		if entry.Kind == schema.TurnCompletion {
			t.Fatal("a completion before the end of the span")
		}
	}
}

func TestUserInputRunsAsOneExecutionTurn(t *testing.T) {
	s, _ := newExecutionSession(t)
	if _, err := s.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	order, byTurn := entriesByTurn(transcriptTurnsOf(t, s))
	var execution string
	for _, id := range order {
		if byTurn[id][0].TurnKind == schema.TurnSpanExecution {
			execution = id
		}
	}
	if !strings.HasPrefix(execution, "t_") {
		t.Fatalf("turns %v hold no t_ execution", order)
	}
	span := byTurn[execution]
	requireExecution(t, span, schema.TurnCompleted)
	sawInput := false
	for _, entry := range span {
		sawInput = sawInput || entry.Kind == schema.TurnUserInput
	}
	if !sawInput {
		t.Fatal("the user input is not in the execution turn")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.Kind.TranscriptOnly() {
			t.Fatalf("a %s entry entered history", turn.Kind)
		}
	}
}

func TestClientMutationStartRunsUnderItsReservedTurnID(t *testing.T) {
	s, _ := newExecutionSession(t)
	s.SetClientMutationStartWakeFunc(func() {})
	accepted, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-1", ExpectedInstanceID: s.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "start"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	turnID := accepted.Turn.ID
	if !strings.HasPrefix(turnID, "turn_m") {
		t.Fatalf("reserved id %q", turnID)
	}
	_, byTurn := entriesByTurn(transcriptTurnsOf(t, s))
	requireExecution(t, byTurn[turnID], schema.TurnCompleted)
}

func TestTerminalFailureCompletesTheTurnFailed(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "bad key", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "fail", nil); err == nil {
		t.Fatal("the failing request succeeded")
	}
	order, byTurn := entriesByTurn(transcriptTurnsOf(t, s))
	span := byTurn[order[len(order)-1]]
	requireExecution(t, span, schema.TurnFailed)
	if span[len(span)-2].Kind != schema.TurnFailure {
		t.Fatalf("the completion follows %s, want the TURN_FAILURE", span[len(span)-2].Kind)
	}
}

func TestInterruptedTurnCompletesInterrupted(t *testing.T) {
	s, adapter := newExecutionSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	adapter.script(func(reqCtx context.Context) (llm.Response, error) {
		cancel()
		<-reqCtx.Done()
		return llm.Response{}, reqCtx.Err()
	})
	if _, err := s.ProcessInput(ctx, "interrupt me", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted input returned %v", err)
	}
	order, byTurn := entriesByTurn(transcriptTurnsOf(t, s))
	span := byTurn[order[len(order)-1]]
	requireExecution(t, span, schema.TurnInterrupted)
}

func TestRetriedRoundStaysInOneExecution(t *testing.T) {
	s, adapter := newExecutionSession(t)
	adapter.script(func(context.Context) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "overloaded", nil, nil)
	})
	if _, err := s.ProcessInput(context.Background(), "retry", nil); err != nil {
		t.Fatal(err)
	}
	completions := 0
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.Kind == schema.TurnCompletion {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("%d completions, want one", completions)
	}
}

func TestIdleAnnouncementsShareOneGapTurn(t *testing.T) {
	s, _ := newExecutionSession(t)
	if _, err := s.ProcessInput(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModel("gpt-5.2"); err != nil {
		t.Fatal(err)
	}
	turns := transcriptTurnsOf(t, s)
	first, second := turns[len(turns)-2], turns[len(turns)-1]
	if first.Kind != schema.TurnModelSwitch || second.Kind != schema.TurnModelSwitch {
		t.Fatalf("last entries = %s, %s", first.Kind, second.Kind)
	}
	if first.TurnKind != schema.TurnSpanGap || second.TurnID != first.TurnID || second.TurnKind != "" {
		t.Fatalf("model switches = (%q %q), (%q %q); want one gap turn", first.TurnID, first.TurnKind, second.TurnID, second.TurnKind)
	}
}

func TestExecutionTurnIDKeepsOnlyReservedSpellings(t *testing.T) {
	if got := executionTurnID("turn_m7"); got != "turn_m7" {
		t.Fatalf("turn_m7 -> %q", got)
	}
	for _, name := range []string{"", "turn_11", "q_1_x"} {
		if got := executionTurnID(name); !strings.HasPrefix(got, "t_") {
			t.Fatalf("%q -> %q, want a fresh t_ id", name, got)
		}
	}
}
