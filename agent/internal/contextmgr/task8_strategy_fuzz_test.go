package contextmgr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzTask8StrategyTransitions drives the real MemoryCrystals,
// RecursiveDistill, OODA, and fork-summary lifecycle paths through one bounded
// deterministic program. The LLM boundary is a scripted adapter: it neither
// observes nor reaches a provider, while still checking that every auxiliary
// request takes the profile's cheap-model route.
//
// The oracles deliberately cover cross-call behavior that individual strategy
// unit tests cannot see:
//   - marker injection is idempotent across repeated ManageContext calls;
//   - user and terminal evidence survives each strategy's injection;
//   - crystal pruning and recursive micro-to-macro folding remain bounded;
//   - OODA persists a fork summary through the Host side-effect boundary; and
//   - usage/metric accounting stays non-negative and bounded by its window.
func FuzzTask8StrategyTransitions(f *testing.F) {
	// First seed crosses both bounded-state transitions: 22 crystal actions
	// exercises the 20-entry prune cap, and six distillation steps exercise the
	// fifth-micro macro fold plus a subsequent micro summary.
	f.Add([]byte{21, 5, 0, 17}, "seed evidence")
	// This seed covers the markdown-fenced fork response and pre-macro route.
	f.Add([]byte{0, 1, 1}, "")
	// Quote-bearing evidence must remain semantic text, not an escaped JSON
	// representation inside the preservation oracle.
	f.Add([]byte("0"), "\"")

	f.Fuzz(func(t *testing.T, program []byte, evidence string) {
		if len(program) > 64 || len(evidence) > 256 {
			return
		}
		t8RunStrategyProgram(t, program, evidence)
	})
}

func t8RunStrategyProgram(t *testing.T, program []byte, evidence string) {
	t.Helper()
	ctx := context.Background()
	userEvidence := "USER_EVIDENCE_" + t8EvidenceToken(evidence)
	terminalEvidence := "TERMINAL_FAILURE_EVIDENCE"
	profile := WithCheapModel(testProfile("openai", "gpt-task8-main", 1_000_000), "gpt-task8-cheap")

	entryJSON, err := json.Marshal(sessionlog.SessionLogEntry{
		Action:       "shell",
		Summary:      userEvidence + "; " + terminalEvidence,
		Outcome:      "failure",
		FilesTouched: []string{"task8.go"},
		Failures:     []string{terminalEvidence},
	})
	if err != nil {
		t.Fatalf("marshal scripted fork entry: %v", err)
	}
	adapter := &t8StrategyAdapter{
		entryJSON: string(entryJSON),
		fenceJSON: t8ProgramByte(program, 2)%2 == 1,
		crystal:   "fact: " + terminalEvidence,
	}
	client := llm.NewClient()
	client.Register(adapter)
	history := t8StrategyHistory(userEvidence, terminalEvidence, 72)

	t8RunMemoryCrystals(ctx, t, profile, client, history, program, userEvidence, terminalEvidence)
	t8RunRecursiveDistill(ctx, t, profile, client, history, program, userEvidence, terminalEvidence)
	t8RunOODA(ctx, t, profile, client, history, userEvidence, terminalEvidence)
	t8AssertUsageAndMetrics(t, profile, history[:8], program)
	t8AssertCheapRouting(t, profile, adapter, userEvidence, terminalEvidence)
}

func t8RunMemoryCrystals(ctx context.Context, t *testing.T, profile *provider.Profile, client *llm.Client, history []schema.Turn, program []byte, userEvidence, terminalEvidence string) {
	t.Helper()
	strategy := NewMemoryCrystalsStrategy(NewManager(profile, client))
	steps := 1 + int(t8ProgramByte(program, 0)%24)
	for step := 1; step <= steps; step++ {
		if err := strategy.AfterAction(ctx, history[:step*3], client); err != nil {
			t.Fatalf("memory crystals AfterAction step %d: %v", step, err)
		}
	}
	wantCrystals := min(steps, 20)
	if len(strategy.crystals) != wantCrystals {
		t.Fatalf("memory crystals = %d, want bounded %d", len(strategy.crystals), wantCrystals)
	}

	managed := append([]schema.Turn(nil), history[:9]...)
	managed = append(managed, schema.NewTurn(schema.TurnSteering, llm.User("[MEMORY CRYSTALS] stale marker")))
	for i := range 2 {
		if err := strategy.ManageContext(ctx, &managed, 0, noopEmit); err != nil {
			t.Fatalf("memory crystals ManageContext %d: %v", i, err)
		}
	}
	t8AssertSingleMarker(t, managed, "[MEMORY CRYSTALS]")
	t8AssertHistoryEvidence(t, managed, userEvidence, terminalEvidence)
}

func t8RunRecursiveDistill(ctx context.Context, t *testing.T, profile *provider.Profile, client *llm.Client, history []schema.Turn, program []byte, userEvidence, terminalEvidence string) {
	t.Helper()
	strategy := NewRecursiveDistillStrategy(NewManager(profile, client))
	steps := 1 + int(t8ProgramByte(program, 1)%6)
	for step := 1; step <= steps; step++ {
		if err := strategy.AfterAction(ctx, history[:step*10], client); err != nil {
			t.Fatalf("recursive distill AfterAction step %d: %v", step, err)
		}
	}
	// Repeating an already-observed turn count must not re-summarize it.
	if err := strategy.AfterAction(ctx, history[:steps*10], client); err != nil {
		t.Fatalf("recursive distill repeated AfterAction: %v", err)
	}
	wantMacros, wantMicros := 0, steps
	if steps >= 5 {
		wantMacros = 1
		wantMicros = steps - 5
	}
	if len(strategy.macroSummaries) != wantMacros || len(strategy.microSummaries) != wantMicros {
		t.Fatalf("recursive summaries = macros:%d micros:%d, want macros:%d micros:%d", len(strategy.macroSummaries), len(strategy.microSummaries), wantMacros, wantMicros)
	}

	managed := append([]schema.Turn(nil), history[:9]...)
	managed = append(managed, schema.NewTurn(schema.TurnSteering, llm.User("[DISTILLED MEMORY] stale marker")))
	for i := range 2 {
		if err := strategy.ManageContext(ctx, &managed, 0, noopEmit); err != nil {
			t.Fatalf("recursive distill ManageContext %d: %v", i, err)
		}
	}
	t8AssertSingleMarker(t, managed, "[DISTILLED MEMORY]")
	t8AssertHistoryEvidence(t, managed, userEvidence, terminalEvidence)
}

func t8RunOODA(ctx context.Context, t *testing.T, profile *provider.Profile, client *llm.Client, history []schema.Turn, userEvidence, terminalEvidence string) {
	t.Helper()
	host := &fakeStrategyHost{stateDir: t.TempDir(), id: "task8-ooda", profile: profile}
	strategy, err := NewOODAStrategy(NewManager(profile, client), host)
	if err != nil {
		t.Fatalf("NewOODAStrategy: %v", err)
	}
	// The first nine turns include both supplied evidence, and are below the
	// SessionLogStrategy's ten-turn fork-summary truncation boundary.
	if err := strategy.AfterAction(ctx, history[:9], client); err != nil {
		t.Fatalf("OODA AfterAction: %v", err)
	}
	if host.sideFx != 1 || len(host.emitted) != 1 || host.emitted[0] != events.EventForkSummary {
		t.Fatalf("OODA fork side effects = %d/%v, want one fork-summary", host.sideFx, host.emitted)
	}
	if strategy.log.Len() != 1 {
		t.Fatalf("OODA log entries = %d, want 1", strategy.log.Len())
	}

	managed := append([]schema.Turn(nil), history[:9]...)
	managed = append(managed, schema.NewTurn(schema.TurnSteering, llm.User("[SESSION ORIENTATION] stale marker")))
	for i := range 2 {
		if err := strategy.ManageContext(ctx, &managed, 0, noopEmit); err != nil {
			t.Fatalf("OODA ManageContext %d: %v", i, err)
		}
	}
	t8AssertSingleMarker(t, managed, "[SESSION ORIENTATION]")
	t8AssertHistoryEvidence(t, managed, userEvidence, terminalEvidence)
	if logText := strategy.log.String(); !strings.Contains(logText, userEvidence) || !strings.Contains(logText, terminalEvidence) {
		t.Fatalf("fork summary lost evidence: %q", logText)
	}

	// The OODA orientation must survive a strategy restart, rather than only
	// being visible through the first strategy's in-memory SessionLog.
	logPath := filepath.Join(host.stateDir, "sessions", "task8-ooda.log.jsonl")
	persisted, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read persisted OODA log: %v", err)
	}
	if !strings.Contains(string(persisted), userEvidence) || !strings.Contains(string(persisted), terminalEvidence) {
		t.Fatalf("persisted OODA log lost evidence: %q", persisted)
	}
	reopened, err := NewOODAStrategy(NewManager(profile, client), host)
	if err != nil {
		t.Fatalf("reopen OODA strategy: %v", err)
	}
	if reopened.log.Len() != 1 {
		t.Fatalf("reopened OODA log entries = %d, want 1", reopened.log.Len())
	}
	restartedHistory := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("restart orientation probe")),
	}
	if err := reopened.ManageContext(ctx, &restartedHistory, 0, noopEmit); err != nil {
		t.Fatalf("reopened OODA ManageContext: %v", err)
	}
	t8AssertSingleMarker(t, restartedHistory, "[SESSION ORIENTATION]")
	t8AssertOrientationEvidence(t, restartedHistory, userEvidence, terminalEvidence)
}

func t8AssertUsageAndMetrics(t *testing.T, profile *provider.Profile, history []schema.Turn, program []byte) {
	t.Helper()
	cm := NewManager(profile, nil)
	base := int(t8ProgramByte(program, 3))
	cm.SetCumulativeUsage(llm.Usage{InputTokens: base, OutputTokens: base / 2, TotalTokens: base + base/2})
	cm.AddUsage(llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5})
	usage := cm.CumulativeUsage()
	if usage.InputTokens != base+3 || usage.OutputTokens != base/2+2 || usage.TotalTokens != base+base/2+5 {
		t.Fatalf("usage sum = %+v, base=%d", usage, base)
	}
	cm.RecordInputTokens(base, len(history))
	cm.SetProfile(profile)
	if got := cm.LastInputTokens(); got != base {
		t.Fatalf("LastInputTokens = %d, want %d", got, base)
	}
	metrics := cm.EstimateUsage(history, int(t8ProgramByte(program, 0)))
	if metrics.Window <= 0 || metrics.Used < 0 || metrics.Remaining < 0 || metrics.Remaining > metrics.Window {
		t.Fatalf("invalid context metrics: %+v", metrics)
	}
	pressure := cm.Pressure(history, 0)
	if pressure < 0 || pressure != pressure { // NaN is never a valid pressure.
		t.Fatalf("invalid pressure: %v", pressure)
	}
}

func t8AssertCheapRouting(t *testing.T, profile *provider.Profile, adapter *t8StrategyAdapter, userEvidence, terminalEvidence string) {
	t.Helper()
	providerName, model := profile.CheapModelRef()
	if len(adapter.requests) == 0 {
		t.Fatal("strategy program made no scripted model calls")
	}
	for i, req := range adapter.requests {
		if req.Provider != providerName || req.Model != model {
			t.Fatalf("request %d route = %s/%s, want %s/%s", i, req.Provider, req.Model, providerName, model)
		}
	}
	if len(adapter.forkPrompts) != 1 {
		t.Fatalf("fork summary calls = %d, want 1", len(adapter.forkPrompts))
	}
	if prompt := adapter.forkPrompts[0]; !strings.Contains(prompt, userEvidence) || !strings.Contains(prompt, terminalEvidence) {
		t.Fatalf("fork prompt did not preserve evidence: %q", prompt)
	}
}

func t8StrategyHistory(userEvidence, terminalEvidence string, n int) []schema.Turn {
	history := make([]schema.Turn, 0, n)
	for i := range n {
		switch i {
		case 0:
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(userEvidence)))
		case 2:
			history = append(history, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("task8-terminal", "shell", terminalEvidence, true)))
		case 1:
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("working task8 step")))
		default:
			if i%3 == 0 {
				history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant step "+strconv.Itoa(i))))
			} else {
				history = append(history, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(fmt.Sprintf("task8-%d", i), "shell", "step output "+strconv.Itoa(i), false)))
			}
		}
	}
	return history
}

func t8AssertSingleMarker(t *testing.T, history []schema.Turn, marker string) {
	t.Helper()
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), marker) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("marker %q count = %d, want 1", marker, count)
	}
}

func t8AssertHistoryEvidence(t *testing.T, history []schema.Turn, evidence ...string) {
	t.Helper()
	var text strings.Builder
	for _, turn := range history {
		for _, part := range turn.Message.Content {
			text.WriteString(part.Text)
			if part.ToolCall != nil {
				text.Write(part.ToolCall.Arguments)
			}
			if part.ToolResult != nil {
				_, _ = fmt.Fprint(&text, part.ToolResult.Content)
			}
		}
	}
	for _, want := range evidence {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("history lost evidence %q", want)
		}
	}
}

func t8AssertOrientationEvidence(t *testing.T, history []schema.Turn, evidence ...string) {
	t.Helper()
	for _, turn := range history {
		if turn.Kind != schema.TurnSteering || !strings.Contains(turn.Message.Text(), "[SESSION ORIENTATION]") {
			continue
		}
		for _, want := range evidence {
			if !strings.Contains(turn.Message.Text(), want) {
				t.Fatalf("reopened orientation lost evidence %q: %q", want, turn.Message.Text())
			}
		}
		return
	}
	t.Fatal("reopened OODA history has no orientation turn")
}

func t8ProgramByte(program []byte, index int) byte {
	if len(program) == 0 {
		return 0
	}
	return program[index%len(program)]
}

func t8EvidenceToken(s string) string {
	if s == "" {
		return "seed"
	}
	b := []byte(s)
	if len(b) > 32 {
		b = b[:32]
	}
	// Fuzz strings may contain invalid UTF-8. The real fork summary is JSON,
	// whose encoder correctly replaces invalid bytes, so use an ASCII evidence
	// token to keep the preservation oracle about strategy behavior rather than
	// JSON's byte-normalization policy.
	return hex.EncodeToString(b)
}

type t8StrategyAdapter struct {
	entryJSON   string
	fenceJSON   bool
	crystal     string
	requests    []llm.Request
	forkPrompts []string
}

func (a *t8StrategyAdapter) Name() string { return "openai" }

func (a *t8StrategyAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Text()
	}
	switch {
	case strings.Contains(prompt, "Extract the key facts"):
		return llm.Response{Message: llm.Assistant(a.crystal)}, nil
	case strings.Contains(prompt, "Consolidate these action summaries"):
		return llm.Response{Message: llm.Assistant("macro summary")}, nil
	case strings.Contains(prompt, "Summarize these coding agent actions"):
		return llm.Response{Message: llm.Assistant("micro summary")}, nil
	case strings.Contains(prompt, "Summarize the most recent action"):
		a.forkPrompts = append(a.forkPrompts, prompt)
		text := a.entryJSON
		if a.fenceJSON {
			text = "```json\n" + text + "\n```"
		}
		return llm.Response{Message: llm.Assistant(text)}, nil
	default:
		return llm.Response{}, fmt.Errorf("unexpected Task 8 strategy prompt: %q", prompt)
	}
}

func (a *t8StrategyAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
