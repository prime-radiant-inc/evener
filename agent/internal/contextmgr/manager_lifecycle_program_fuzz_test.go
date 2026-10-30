//go:build evenerfuzz

package contextmgr

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// FuzzManagerLifecycleProgram drives the Manager's public lifecycle through
// the same narrow LLM boundary used by production. The adapters are scripted:
// compaction, force-compaction, and note elicitation all exercise real manager
// behavior without provider credentials or network access.
//
// Oracles check that compaction preserves a valid framed head, callbacks and
// events agree with the history mutation, fallback routing reaches the active
// model, and metric/accounting operations remain internally coherent.
func FuzzManagerLifecycleProgram(f *testing.F) {
	f.Add([]byte("manager lifecycle seed"))
	f.Add([]byte{0x00, 0xff, 0x31, 0x42})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64]
		}
		cmgpRunManagerLifecycle(t, program)
	})
}

func cmgpRunManagerLifecycle(t *testing.T, program []byte) {
	t.Helper()
	ctx := context.Background()
	token := cmgpToken(program)
	history := cmgpHistory(token)

	cheap := &cmgpAdapter{
		name: "anthropic",
		respond: func(req llm.Request) (llm.Response, error) {
			if strings.Contains(req.Messages[0].Text(), "MUST survive VERBATIM") {
				return llm.Response{}, llm.ErrorFromHTTPStatus("anthropic", 400, "unsupported model", nil, nil)
			}
			return llm.Response{Message: llm.Assistant("## Progress\ncheap summary " + token)}, nil
		},
	}
	active := &cmgpAdapter{
		name: "openai",
		respond: func(req llm.Request) (llm.Response, error) {
			if strings.Contains(req.Messages[0].Text(), "MUST survive VERBATIM") {
				return llm.Response{Message: llm.Assistant("  - retain " + token + "\n")}, nil
			}
			return llm.Response{Message: llm.Assistant("## Progress\nactive summary " + token)}, nil
		},
	}
	client := cmgpClient(active, cheap)
	profile := WithCheapModel(testOpenAIProfileWithContextWindow(64), "anthropic/cheap")
	cm := NewManager(profile, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.Meta = CompactionMeta{SessionID: "cmgp-" + token, ActivatedSkills: []string{"testing", "fuzzing"}}

	if !cm.HasClient() || NewManager(profile, nil, cheapmodel.New(nil)).HasClient() {
		t.Fatal("HasClient did not reflect the configured client")
	}
	if cm.resultToolName() != "communicate" {
		t.Fatalf("default result tool = %q", cm.resultToolName())
	}
	cm.ResultToolName = "result"
	if cm.resultToolName() != "result" {
		t.Fatalf("configured result tool = %q", cm.resultToolName())
	}
	cm.ResultToolName = ""

	cm.SetCumulativeUsage(llm.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10})
	cm.AddUsage(llm.Usage{InputTokens: 2, OutputTokens: 5, TotalTokens: 7})
	if got := cm.CumulativeUsage(); got.InputTokens != 9 || got.OutputTokens != 8 || got.TotalTokens != 17 {
		t.Fatalf("cumulative usage = %+v", got)
	}
	cm.RecordInputTokens(80, len(history))
	if cm.LastInputTokens() != 80 {
		t.Fatalf("recorded input tokens = %d", cm.LastInputTokens())
	}
	metrics := cm.EstimateUsage(history, 32)
	if metrics.Window != 64 || metrics.Used < 80 || metrics.Remaining < 0 || metrics.Remaining > metrics.Window {
		t.Fatalf("invalid context metrics: %+v", metrics)
	}
	if pressure := cm.Pressure(history, 32); pressure <= 0 || pressure != pressure {
		t.Fatalf("invalid pressure: %v", pressure)
	}
	cm.SetProfile(profile)

	cmgpCheckThresholdScaling(t, profile)
	cmgpCheckPureManagerHelpers(t, token, history, profile)
	cmgpCheckSummarizerBehavior(t, ctx, token, profile, client, cheap)

	// Tiny thresholds force the automatic checkpoint and summary layers, on
	// the smallest window compaction summarizes on.
	summaryProfile := provider.WithContextWindow(profile, minSummarizingWindow)
	cm.SetProfile(summaryProfile)
	cm.CheckpointThreshold = 0.0001
	cm.SummarizeThreshold = 0.0001
	cm.RecordInputTokens(999, len(history))
	var compactTurns []schema.Turn
	cm.OnCompactionTurn = func(turn schema.Turn) { compactTurns = append(compactTurns, turn) }
	var automatic cmgpEventLog
	cm.MaybeCompact(ctx, &history, 0, automatic.emit)
	if len(history) < 1 || history[0].Kind != schema.TurnSummary {
		t.Fatalf("automatic compaction head = %v, want summary", cmgpHeadKind(history))
	}
	if !automatic.hasLayer("checkpoint") || !automatic.hasLayer("summarize") {
		t.Fatalf("automatic compaction layers = %v", automatic.layers)
	}
	if len(compactTurns) != 2 || compactTurns[0].Kind != schema.TurnCheckpoint || compactTurns[1].Kind != schema.TurnSummary {
		t.Fatalf("automatic callbacks = %v", cmgpKinds(compactTurns))
	}
	if cm.LastInputTokens() != 0 {
		t.Fatalf("automatic compaction retained stale input measurement: %d", cm.LastInputTokens())
	}
	if !strings.HasPrefix(history[0].Message.Text(), "[CONTEXT SUMMARY]\n") {
		t.Fatalf("automatic summary missing frame: %q", history[0].Message.Text())
	}

	// The summarization error remains non-fatal and leaves a deterministic
	// checkpoint in place while emitting a warning.
	failureClient := cmgpClient(&cmgpAdapter{
		name:    "openai",
		respond: func(llm.Request) (llm.Response, error) { return llm.Response{}, errors.New("scripted summary failure") },
	}, nil)
	failureHistory := cmgpHistory(token + "-failure")
	failureCM := NewManager(testOpenAIProfileWithContextWindow(minSummarizingWindow), failureClient, cheapmodel.New(failureClient))
	failureCM.PreserveRecentTurns = 2
	failureCM.CheckpointThreshold = 0.0001
	failureCM.SummarizeThreshold = 0.0001
	var failureEvents cmgpEventLog
	failureCM.MaybeCompact(ctx, &failureHistory, 0, failureEvents.emit)
	if len(failureHistory) == 0 || failureHistory[0].Kind != schema.TurnCheckpoint || failureEvents.warnings != 1 {
		t.Fatalf("failed automatic compaction = head:%v warnings:%d", cmgpHeadKind(failureHistory), failureEvents.warnings)
	}

	// ForceCompact must apply the same two layers regardless of pressure.
	forceHistory := cmgpHistory(token + "-force")
	forceCM := NewManager(summaryProfile, client, cheapmodel.New(client))
	forceCM.PreserveRecentTurns = 2
	var forced cmgpEventLog
	var forcedTurns []schema.Turn
	forceCM.OnCompactionTurn = func(turn schema.Turn) { forcedTurns = append(forcedTurns, turn) }
	if summarized := forceCM.ForceCompact(ctx, &forceHistory, "retain exact token "+token, forced.emit); !summarized {
		t.Fatal("ForceCompact did not report a generated summary")
	}
	if len(forceHistory) == 0 || forceHistory[0].Kind != schema.TurnSummary || !forced.hasLayer("checkpoint") || !forced.hasLayer("summarize") {
		t.Fatalf("ForceCompact did not produce checkpoint+summary: head=%v layers=%v", cmgpHeadKind(forceHistory), forced.layers)
	}
	if len(forcedTurns) != 2 || forcedTurns[0].Kind != schema.TurnCheckpoint || forcedTurns[1].Kind != schema.TurnSummary {
		t.Fatalf("ForceCompact callbacks = %v", cmgpKinds(forcedTurns))
	}

	nilHistory := cmgpHistory(token + "-nil")
	nilCM := NewManager(profile, nil, cheapmodel.New(nil))
	nilCM.PreserveRecentTurns = 2
	if nilCM.ForceCompact(ctx, &nilHistory, "", cmgpNoopEmit) {
		t.Fatal("ForceCompact reported a summary without an LLM client")
	}
	if len(nilHistory) == 0 || nilHistory[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("ForceCompact without client head = %v", cmgpHeadKind(nilHistory))
	}
	shortSummaryHistory := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nprior\n[END SUMMARY]")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent "+token)),
	}
	shortSummaryCM := NewManager(profile, client, cheapmodel.New(client))
	shortSummaryCM.PreserveRecentTurns = len(shortSummaryHistory)
	if shortSummaryCM.ForceCompact(ctx, &shortSummaryHistory, "", cmgpNoopEmit) {
		t.Fatal("ForceCompact reported a pre-existing short summary as newly generated")
	}

	forceFailureHistory := cmgpHistory(token + "-force-failure")
	forceFailureCM := NewManager(testOpenAIProfileWithContextWindow(minSummarizingWindow), failureClient, cheapmodel.New(failureClient))
	forceFailureCM.PreserveRecentTurns = 2
	var forceFailure cmgpEventLog
	summarized := forceFailureCM.ForceCompact(ctx, &forceFailureHistory, "", forceFailure.emit)
	if summarized || forceFailure.warnings != 1 {
		t.Fatalf("failed ForceCompact = summarized:%v warnings:%d", summarized, forceFailure.warnings)
	}

	cmgpCheckElicitNote(t, ctx, token, cm, cheap, active, profile, client)

	zeroCM := NewManager(&provider.Profile{}, nil, cheapmodel.New(nil))
	zeroHistory := cmgpHistory(token + "-zero")
	zeroCM.MaybeCompact(ctx, &zeroHistory, 0, cmgpNoopEmit)
	// Unknown means assumed (issue #4564): a profile with no window budgets
	// against the conservative default, so the metrics carry the assumption
	// instead of reporting nothing.
	zeroMetrics := zeroCM.EstimateUsage(zeroHistory, 0)
	if zeroMetrics.Window != AssumedContextWindow || zeroMetrics.Used <= 0 {
		t.Fatalf("unknown-window manager metrics = %+v, want the assumed window %d with a used count", zeroMetrics, AssumedContextWindow)
	}
	if p := zeroCM.Pressure(zeroHistory, 0); p <= 0 {
		t.Fatalf("unknown-window manager pressure = %v, want > 0 against the assumed window", p)
	}
}

func cmgpCheckThresholdScaling(t *testing.T, profile *provider.Profile) {
	t.Helper()
	for _, scale := range []float64{0, 1, 0.25, 0.5} {
		cm := NewManager(profile, nil, cheapmodel.New(nil))
		ApplyThresholdScale(cm, scale)
		if scale == 0 || scale == 1 {
			if cm.ObservationMaskThreshold != 0.60 || cm.SummarizeThreshold != 0.95 {
				t.Fatalf("scale %v changed defaults: %+v", scale, cm)
			}
			continue
		}
		if cm.ObservationMaskThreshold < 0.20 || cm.ThinkingClearThreshold < 0.20 || cm.WarnThreshold < 0.20 || cm.CheckpointThreshold < 0.20 || cm.SummarizeThreshold < 0.20 {
			t.Fatalf("scale %v violated threshold floor", scale)
		}
	}
}

func cmgpCheckElicitNote(t *testing.T, ctx context.Context, token string, cm *Manager, cheap, active *cmgpAdapter, profile *provider.Profile, client *llm.Client) {
	t.Helper()
	history := cmgpHistory(token)
	if _, err := NewManager(profile, nil, cheapmodel.New(nil)).ElicitNote(ctx, history, nil); err == nil {
		t.Fatal("ElicitNote without a client unexpectedly succeeded")
	}
	if _, err := NewManager(&provider.Profile{}, client, cheapmodel.New(client)).ElicitNote(ctx, history, nil); err == nil {
		t.Fatal("ElicitNote without a model unexpectedly succeeded")
	}
	note, err := cm.ElicitNote(ctx, history, nil)
	if err != nil || note != "- retain "+token {
		t.Fatalf("ElicitNote = %q, %v", note, err)
	}
	if len(cheap.requests) == 0 || len(active.requests) == 0 {
		t.Fatal("ElicitNote did not try cheap then active fallback routes")
	}
	prompt := active.requests[len(active.requests)-1].Messages[0].Text()
	if !strings.Contains(prompt, token) || !strings.Contains(prompt, "Tool result read_file") {
		t.Fatalf("ElicitNote prompt lost exact history detail: %q", prompt)
	}
}

func cmgpCheckPureManagerHelpers(t *testing.T, token string, history []schema.Turn, profile *provider.Profile) {
	t.Helper()
	if routes := summarizationModels(profile); len(routes) != 2 || routes[0].provider != "anthropic" || routes[1].provider != "openai" {
		t.Fatalf("summary routes = %+v", routes)
	}
	if routes := summarizationModels(nil); len(routes) != 0 {
		t.Fatalf("nil profile routes = %+v", routes)
	}
	if routes := summarizationModels(&provider.Profile{}); len(routes) != 0 {
		t.Fatalf("zero profile routes = %+v", routes)
	}
	if shouldFallbackSummarizationModel(context.Background(), nil) ||
		shouldFallbackSummarizationModel(context.Background(), context.Canceled) ||
		shouldFallbackSummarizationModel(context.Background(), context.DeadlineExceeded) ||
		shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 503, "busy", nil, nil)) ||
		!shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 404, "/v1/responses unavailable", nil, nil)) ||
		!shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 400, "bad", nil, nil)) ||
		!shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 404, "gone", nil, nil)) ||
		!shouldFallbackSummarizationModel(context.Background(), llm.ErrorFromHTTPStatus("openai", 403, "denied", nil, nil)) {
		t.Fatal("unexpected summarization fallback classification")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldFallbackSummarizationModel(canceled, errors.New("ignored after cancellation")) {
		t.Fatal("cancelled context triggered a fallback")
	}

	if prompt := buildSummaryPrompt("history", ""); !strings.Contains(prompt, "Conversation Timeline") {
		t.Fatalf("default summary prompt missing default instructions: %q", prompt)
	}
	if prompt := buildSummaryPrompt("history", "retain "+token); !strings.Contains(prompt, "CALLER INSTRUCTIONS") || !strings.Contains(prompt, token) {
		t.Fatalf("steered summary prompt lost caller instruction: %q", prompt)
	}
	rendered := renderHistoryForElicit(history, 128)
	if !strings.Contains(rendered, token) {
		t.Fatalf("elicit renderer lost recent token: %q", rendered)
	}
	if got := renderTurnForElicit(schema.Turn{}); got != "" {
		t.Fatalf("empty turn rendered as %q", got)
	}
	call := cmgpCallTurn("render", "shell", `{"command":"echo `+token+`"}`)
	if got := renderTurnForElicit(call); !strings.Contains(got, "Assistant: Tool call shell") || !strings.Contains(got, token) {
		t.Fatalf("tool-call turn rendered as %q", got)
	}
	result := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("render", "shell", "exact "+token, false))
	if got := renderTurnForElicit(result); !strings.Contains(got, "Tool result shell") || !strings.Contains(got, token) {
		t.Fatalf("tool-result turn rendered as %q", got)
	}
	if got := renderTurnForElicit(schema.NewTurn(schema.TurnSteering, llm.User("guide "+token))); !strings.Contains(got, "Context: guide "+token) {
		t.Fatalf("steering turn rendered as %q", got)
	}

	mutated := cmgpHistory(token)
	clearThinking(mutated, 2)
	if !strings.Contains(mutated[4].Message.Content[1].Thinking.Text, "[thinking:") || mutated[4].Message.Content[2].Thinking.Text != "redacted evidence" {
		t.Fatal("thinking clear did not preserve redacted blocks while masking visible blocks")
	}
	maskObservations(mutated, 2, "communicate")
	if got := fmt.Sprint(mutated[3].Message.Content[0].ToolResult.Content); !strings.HasPrefix(got, "[read_file:") {
		t.Fatalf("observation mask result = %q", got)
	}
	if got := fmt.Sprint(mutated[6].Message.Content[0].ToolResult.Content); got != "delivered "+token {
		t.Fatalf("communicate result was masked: %q", got)
	}

	for _, tc := range []struct {
		name    string
		content string
		args    string
	}{
		{"read_file", "line\nline", `{"file_path":"a.go"}`},
		{"shell", "exit_code=0\npass", `{"command":"go test ./..."}`},
		{"grep", "one\ntwo", `{"pattern":"needle"}`},
		{"glob", "a.go\n\nb.go", `{"pattern":"*.go"}`},
		{"edit_file", "ok", `{"file_path":"a.go"}`},
		{"apply_patch", "ok", `{}`},
		{"write_file", "ok", `{"file_path":"a.go"}`},
		{"web_fetch", "body", `{"url":"https://example.test"}`},
		{"delegate", `{"job_id":"job-1"}`, `{}`},
		{"task_list", `[1,2]`, `{"action":"list"}`},
		{"use_skill", "body", `{"skill_name":"testing"}`},
		{"communicate", "body", `{}`},
		{"other", "body", `{}`},
	} {
		if got := summarizeToolResult(tc.name, tc.content, false, json.RawMessage(tc.args)); got == "" {
			t.Fatalf("empty summary for %s", tc.name)
		}
	}
	if parseExitCode("exit 4") != "4" || parseExitCode("nothing") != "?" || extractJSONField(`{"x":"y"}`, "x") != "y" || extractJSONField("bad", "x") != "" || countJSONArrayElements(`[1,2]`) != 2 || countJSONArrayElements("bad") != 0 {
		t.Fatal("pure helper contracts diverged")
	}
}

func cmgpCheckSummarizerBehavior(t *testing.T, ctx context.Context, token string, profile *provider.Profile, client *llm.Client, cheap *cmgpAdapter) {
	t.Helper()
	// The summarizer sizes its history budget from the model's window (#4321);
	// a window this large leaves the full 80k budget these renderings assume.
	profile = provider.WithContextWindow(profile, 1_000_000)
	input := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("u", 5_100)+token)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("assistant evidence "+token)),
		cmgpCallTurn("status", "communicate", `{"message":"status `+token+`","end_turn":false}`),
		cmgpCallTurn("message", "communicate", `{"message":"message `+token+`","end_turn":true}`),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("shell", "shell", strings.Repeat("r", 250)+token, false)),
		schema.NewTurn(schema.TurnSteering, llm.User("normal steering "+token)),
		schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nprior "+token)),
		schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nprior "+token)),
		schema.NewTurn(schema.TurnSteering, llm.User(strings.Repeat("s", 80_100))),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("tail assistant "+token)),
		schema.NewTurn(schema.TurnUserInput, llm.User("tail user "+token)),
	}
	cm := NewManager(profile, client, cheapmodel.New(client))
	result, err := cm.summarizeFold(ctx, input, 2, "retain "+token, nil)
	if err != nil || len(result) != 3 || result[0].Kind != schema.TurnSummary || result[1].Message.Text() != input[len(input)-2].Message.Text() || result[2].Message.Text() != input[len(input)-1].Message.Text() {
		t.Fatalf("scripted summary result = %#v, err=%v", cmgpKinds(result), err)
	}
	prompt := cheap.requests[len(cheap.requests)-1].Messages[0].Text()
	for _, want := range []string{"CALLER INSTRUCTIONS", "User: ", "Assistant: assistant evidence", "Agent Status: status", "Agent Message: message", "Tool(shell):", "System: normal steering", "Previous compaction:", "[... truncated ...]"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("summary prompt missing %q", want)
		}
	}

	unsafe := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("unsafe")),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("unsafe", "shell", "output", false)),
	}
	unchanged, err := cm.summarizeFold(ctx, unsafe, 1, "", nil)
	if err != nil || len(unchanged) != len(unsafe) || unchanged[0].Kind != schema.TurnUserInput {
		t.Fatalf("unsafe summary cutoff = %#v, err=%v", cmgpKinds(unchanged), err)
	}
	if _, err := NewManager(&provider.Profile{}, client, cheapmodel.New(client)).summarizeFold(ctx, input, 2, "", nil); err == nil {
		t.Fatal("summary without a model unexpectedly succeeded")
	}

	fallbackCheap := &cmgpAdapter{name: "anthropic", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("anthropic", 400, "unsupported model", nil, nil)
	}}
	fallbackActive := &cmgpAdapter{name: "openai", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("## Progress\nfallback " + token)}, nil
	}}
	fallbackCM := NewManager(profile, cmgpClient(fallbackActive, fallbackCheap), cheapmodel.New(cmgpClient(fallbackActive, fallbackCheap)))
	if got, err := fallbackCM.summarizeFold(ctx, input, 2, "", nil); err != nil || len(got) == 0 || got[0].Kind != schema.TurnSummary || len(fallbackCheap.requests) != 1 || len(fallbackActive.requests) != 1 {
		t.Fatalf("summary fallback = head:%v cheap:%d active:%d err:%v", cmgpHeadKind(got), len(fallbackCheap.requests), len(fallbackActive.requests), err)
	}
	stopCheap := &cmgpAdapter{name: "anthropic", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("do not fall back")
	}}
	stopActive := &cmgpAdapter{name: "openai", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("should not run")}, nil
	}}
	if _, err := NewManager(profile, cmgpClient(stopActive, stopCheap), cheapmodel.New(cmgpClient(stopActive, stopCheap))).summarizeFold(ctx, input, 2, "", nil); err == nil || len(stopActive.requests) != 0 {
		t.Fatalf("non-fallback summary error used active route: err=%v active=%d", err, len(stopActive.requests))
	}
}

// FuzzStrategyContractProgram exercises strategy constructors, contracts, and
// best-effort lifecycle boundaries. It deliberately includes both successful
// and failed scripted LLM calls: a strategy surfaces a failed auxiliary call
// as an error for the caller to warn about (the session keeps the turn alive),
// banking no partial state while preserving its injected context markers.
func FuzzStrategyContractProgram(f *testing.F) {
	f.Add([]byte("strategy contract seed"))
	f.Add([]byte{0x1, 0x2, 0x3, 0x4})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64]
		}
		cmgpRunStrategyContracts(t, program)
	})
}

func cmgpRunStrategyContracts(t *testing.T, program []byte) {
	t.Helper()
	ctx := context.Background()
	token := cmgpToken(program)
	profile := WithCheapModel(testOpenAIProfileWithContextWindow(1_000_000), "cheap")
	adapter := &cmgpAdapter{name: "openai", respond: cmgpStrategyResponder(token)}
	client := cmgpClient(adapter, nil)
	history := cmgpHistory(token)

	cmgpCheckCompactAndCheckpointStrategies(t, ctx, token, profile, client, history)
	cmgpCheckSessionLogAndOODAStrategies(t, ctx, token, profile, client, history)
	cmgpCheckForkSummarize(t, ctx, token, profile, client, history)
}

func cmgpCheckCompactAndCheckpointStrategies(t *testing.T, ctx context.Context, token string, profile *provider.Profile, client *llm.Client, history []schema.Turn) {
	t.Helper()
	compactCM := NewManager(profile, client, cheapmodel.New(client))
	compact := NewCompactStrategy(compactCM)
	if compact.Name() != "compact" || len(compact.Tools()) != 0 || compact.AfterAction(ctx, history, client) != nil {
		t.Fatal("compact strategy contract changed")
	}
	compactHistory := append([]schema.Turn(nil), history...)
	if err := compact.ManageContext(ctx, &compactHistory, 0, cmgpNoopEmit); err != nil {
		t.Fatalf("compact ManageContext: %v", err)
	}
}

func cmgpCheckSessionLogAndOODAStrategies(t *testing.T, ctx context.Context, token string, profile *provider.Profile, client *llm.Client, history []schema.Turn) {
	t.Helper()
	if err := (&SessionLogStrategy{}).ManageContext(ctx, &history, 0, cmgpNoopEmit); err != nil {
		t.Fatalf("nil session-log strategy: %v", err)
	}
	zeroHost := &fakeStrategyHost{stateDir: t.TempDir(), id: "zero", profile: &provider.Profile{}}
	zeroLog, err := NewSessionLogStrategy(NewManager(&provider.Profile{}, nil, cheapmodel.New(nil)), zeroHost)
	if err != nil {
		t.Fatalf("NewSessionLogStrategy zero profile: %v", err)
	}
	if err := zeroLog.ManageContext(ctx, &history, 0, cmgpNoopEmit); err != nil {
		t.Fatalf("zero-window session-log strategy: %v", err)
	}

	host := &fakeStrategyHost{stateDir: t.TempDir(), id: "session-log", profile: profile}
	sls, err := NewSessionLogStrategy(NewManager(profile, client, cheapmodel.New(client)), host)
	if err != nil {
		t.Fatalf("NewSessionLogStrategy: %v", err)
	}
	if sls.Name() != "session-log" || len(sls.Tools()) != 0 {
		t.Fatal("session-log strategy contract changed")
	}
	if err := sls.log.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "shell", Summary: "ran test " + token, Outcome: "success"}); err != nil {
		t.Fatalf("append session log: %v", err)
	}
	checkpointHistory := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("p", 600)+token)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("worked")),
		schema.NewTurn(schema.TurnUserInput, llm.User("recent "+token)),
	}
	checkpointed := sls.sessionLogCheckpoint(checkpointHistory, 1)
	if len(checkpointed) != 2 || checkpointed[0].Kind != schema.TurnCheckpoint || !strings.Contains(checkpointed[0].Message.Text(), "[CONTEXT CHECKPOINT - SESSION LOG]") || !strings.Contains(checkpointed[0].Message.Text(), "...") {
		t.Fatalf("session-log checkpoint malformed: %+v", checkpointed)
	}
	if got := extractOriginalPrompt([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("[CONTEXT CHECKPOINT]\nOriginal prompt: original\n")),
		schema.NewTurn(schema.TurnUserInput, llm.User("plain")),
	}); got != "original" {
		t.Fatalf("checkpoint original prompt = %q", got)
	}
	if got := extractOriginalPrompt([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("[CONTEXT CHECKPOINT - SESSION LOG]\nOriginal task: original-task\n")),
	}); got != "original-task" || extractOriginalPromptLine("no marker", "Original prompt: ") != "" || extractOriginalPromptLine("Original prompt: final", "Original prompt: ") != "final" {
		t.Fatal("original prompt extraction changed")
	}

	if err := (&SessionLogStrategy{}).AfterAction(ctx, history, client); err != nil {
		t.Fatalf("nil host AfterAction: %v", err)
	}
	noProfile := &SessionLogStrategy{session: &fakeStrategyHost{profile: nil}}
	if err := noProfile.AfterAction(ctx, history, client); err != nil {
		t.Fatalf("nil profile AfterAction: %v", err)
	}
	badHost := &fakeStrategyHost{stateDir: t.TempDir(), id: "bad", profile: profile}
	badForkClient := cmgpClient(&cmgpAdapter{name: "openai", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("not JSON")}, nil
	}}, nil)
	badSLS, err := NewSessionLogStrategy(NewManager(profile, badForkClient, cheapmodel.New(badForkClient)), badHost)
	if err != nil {
		t.Fatalf("NewSessionLogStrategy bad response: %v", err)
	}
	// A failed fork is surfaced to the caller as a strategy error (the session
	// warns and keeps the turn alive) and appends nothing, as CORE-10 requires.
	if err := badSLS.AfterAction(ctx, cmgpLongHistory(token, 12), badForkClient); err == nil || badHost.sideFx != 0 || badSLS.log.Len() != 0 {
		t.Fatalf("failed fork result = sidefx:%d log:%d err:%v", badHost.sideFx, badSLS.log.Len(), err)
	}
	if err := sls.AfterAction(ctx, cmgpLongHistory(token, 12), client); err != nil || host.sideFx != 1 || sls.log.Len() != 2 {
		t.Fatalf("session log AfterAction = sidefx:%d log:%d err:%v", host.sideFx, sls.log.Len(), err)
	}

	oodaHost := &fakeStrategyHost{stateDir: t.TempDir(), id: "ooda", profile: profile}
	ooda, err := NewOODAStrategy(NewManager(profile, client, cheapmodel.New(client)), oodaHost)
	if err != nil {
		t.Fatalf("NewOODAStrategy: %v", err)
	}
	if ooda.Name() != "ooda" {
		t.Fatalf("OODA name = %q", ooda.Name())
	}
	oodaHistory := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("orient "+token))}
	if err := ooda.ManageContext(ctx, &oodaHistory, 0, cmgpNoopEmit); err != nil || cmgpMarkerCount(oodaHistory, "[SESSION ORIENTATION]") != 0 {
		t.Fatalf("empty OODA orientation = err:%v count:%d", err, cmgpMarkerCount(oodaHistory, "[SESSION ORIENTATION]"))
	}
	if err := ooda.log.Append(sessionlog.SessionLogEntry{Turn: 2, Action: "edit_file", Summary: "changed " + token, Outcome: "success"}); err != nil {
		t.Fatalf("append OODA log: %v", err)
	}
	if err := ooda.ManageContext(ctx, &oodaHistory, 0, cmgpNoopEmit); err != nil {
		t.Fatalf("OODA orientation: %v", err)
	}
	if err := ooda.ManageContext(ctx, &oodaHistory, 0, cmgpNoopEmit); err != nil || cmgpMarkerCount(oodaHistory, "[SESSION ORIENTATION]") != 1 {
		t.Fatalf("OODA orientation idempotence = err:%v count:%d", err, cmgpMarkerCount(oodaHistory, "[SESSION ORIENTATION]"))
	}
	if err := ooda.log.Append(sessionlog.SessionLogEntry{Turn: 3, Action: "shell", Summary: strings.Repeat("x", 80_100), Outcome: "success"}); err != nil {
		t.Fatalf("append large OODA log: %v", err)
	}
	if err := ooda.ManageContext(ctx, &oodaHistory, 0, cmgpNoopEmit); err != nil {
		t.Fatalf("large OODA orientation: %v", err)
	}
	if !cmgpHistoryContains(oodaHistory, "session log truncated") {
		t.Fatal("large OODA log did not use its truncation marker")
	}
}

func cmgpCheckForkSummarize(t *testing.T, ctx context.Context, token string, profile *provider.Profile, client *llm.Client, history []schema.Turn) {
	t.Helper()
	entry, err := forkSummarize(ctx, cheapmodel.New(client), profile, history, 17)
	if err != nil || entry.Turn != 17 || entry.Action != "shell" {
		t.Fatalf("fork summary = %+v, %v", entry, err)
	}
	badJSON := cmgpClient(&cmgpAdapter{name: "openai", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Assistant("not JSON")}, nil
	}}, nil)
	if _, err := forkSummarize(ctx, cheapmodel.New(badJSON), profile, history, 1); err == nil {
		t.Fatal("invalid fork JSON unexpectedly succeeded")
	}
	failed := cmgpClient(&cmgpAdapter{name: "openai", respond: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, errors.New("fork failure")
	}}, nil)
	if _, err := forkSummarize(ctx, cheapmodel.New(failed), profile, history, 1); err == nil {
		t.Fatal("failed fork completion unexpectedly succeeded")
	}
	prompt := buildSummarizePrompt([]schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("u", 600))),
		cmgpCallTurn("fork", "shell", `{"command":"echo `+token+`"}`),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("fork", "shell", strings.Repeat("r", 400), true)),
		schema.NewTurn(schema.TurnSteering, llm.User("keep "+token)),
	})
	if !strings.Contains(prompt, "User: ") || !strings.Contains(prompt, "Tool(shell) ERROR") || !strings.Contains(prompt, "System: keep "+token) || !strings.Contains(prompt, "...") {
		t.Fatalf("fork prompt omitted a turn shape: %q", prompt)
	}
	if truncate("short", 10) != "short" || truncate("abcdef", 3) != "abc..." || stripCodeFence("```json\n{}\n```") != "{}" {
		t.Fatal("fork formatting helpers changed")
	}
}

type cmgpAdapter struct {
	name     string
	respond  func(llm.Request) (llm.Response, error)
	requests []llm.Request
}

func (a *cmgpAdapter) Name() string { return a.name }

func (a *cmgpAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.requests = append(a.requests, req)
	if a.respond == nil {
		return llm.Response{Message: llm.Assistant("scripted")}, nil
	}
	return a.respond(req)
}

func (a *cmgpAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func cmgpClient(openai, anthropic *cmgpAdapter) *llm.Client {
	client := llm.NewClient()
	if openai != nil {
		client.Register(openai)
	}
	if anthropic != nil {
		client.Register(anthropic)
	}
	return client
}

func cmgpStrategyResponder(token string) func(llm.Request) (llm.Response, error) {
	entryJSON, err := json.Marshal(sessionlog.SessionLogEntry{
		Action:       "shell",
		Summary:      "ran tests " + token,
		Outcome:      "success",
		FilesTouched: []string{"/tmp/" + token + ".go"},
	})
	if err != nil {
		panic(err)
	}
	return func(req llm.Request) (llm.Response, error) {
		prompt := req.Messages[0].Text()
		switch {
		case strings.Contains(prompt, "Summarize the most recent action"):
			return llm.Response{Message: llm.Assistant("```json\n" + string(entryJSON) + "\n```")}, nil
		case strings.Contains(prompt, "Extract the key facts"):
			return llm.Response{Message: llm.Assistant("fact " + token)}, nil
		case strings.Contains(prompt, "Consolidate these action summaries"):
			return llm.Response{Message: llm.Assistant("macro " + token)}, nil
		case strings.Contains(prompt, "Summarize these coding agent actions"):
			return llm.Response{Message: llm.Assistant("micro " + token)}, nil
		default:
			return llm.Response{Message: llm.Assistant("## Progress\nsummary " + token)}, nil
		}
	}
}

type cmgpEventLog struct {
	layers   []string
	warnings int
}

func (e *cmgpEventLog) emit(kind events.EventKind, data events.EventData) {
	switch d := data.(type) {
	case events.ContextCompactionData:
		e.layers = append(e.layers, d.Layer)
	case events.WarningData:
		e.warnings++
	}
}

func (e *cmgpEventLog) hasLayer(want string) bool {
	for _, got := range e.layers {
		if got == want {
			return true
		}
	}
	return false
}

func cmgpNoopEmit(events.EventKind, events.EventData) {}

func cmgpToken(program []byte) string {
	if len(program) == 0 {
		return "seed"
	}
	if len(program) > 24 {
		program = program[:24]
	}
	return hex.EncodeToString(program)
}

func cmgpHistory(token string) []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("start task "+token)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("analysis "+strings.Repeat("a", 80))),
		cmgpCallTurn("read", "read_file", `{"file_path":"/tmp/`+token+`.txt"}`),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("read", "read_file", "exact value "+token+"\n"+strings.Repeat("detail line\n", 12), false)),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "reason about " + token},
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "visible reasoning " + token}},
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "redacted evidence", Redacted: true}},
		}}),
		cmgpCallTurn("reply", "communicate", `{"message":"answer `+token+`","end_turn":true}`),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("reply", "communicate", "delivered "+token, false)),
		schema.NewTurn(schema.TurnUserInput, llm.User("follow up "+token)),
		cmgpCallTurn("shell", "shell", `{"command":"go test ./..."}`),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("shell", "shell", "exit_code=0\npass "+token, false)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("final state "+token)),
		schema.NewTurn(schema.TurnUserInput, llm.User("preserve "+token)),
	}
}

func cmgpLongHistory(token string, count int) []schema.Turn {
	history := make([]schema.Turn, 0, count)
	for i := 0; i < count; i++ {
		if i%3 == 0 {
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("user %d %s", i, token))))
			continue
		}
		if i%3 == 1 {
			history = append(history, cmgpCallTurn(fmt.Sprintf("call-%d", i+1), "shell", `{"command":"echo summary"}`))
			continue
		}
		history = append(history, schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(fmt.Sprintf("call-%d", i), "shell", "output "+token, false)))
	}
	return history
}

func cmgpCallTurn(id, name, args string) schema.Turn {
	return schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        id,
			Name:      name,
			Arguments: json.RawMessage(args),
			Type:      "function",
		},
	}}})
}

func cmgpHeadKind(history []schema.Turn) schema.TurnKind {
	if len(history) == 0 {
		return ""
	}
	return history[0].Kind
}

func cmgpKinds(history []schema.Turn) []schema.TurnKind {
	kinds := make([]schema.TurnKind, len(history))
	for i, turn := range history {
		kinds[i] = turn.Kind
	}
	return kinds
}

func cmgpMarkerCount(history []schema.Turn, marker string) int {
	count := 0
	for _, turn := range history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), marker) {
			count++
		}
	}
	return count
}

func cmgpHistoryContains(history []schema.Turn, want string) bool {
	for _, turn := range history {
		if strings.Contains(turn.Message.Text(), want) {
			return true
		}
	}
	return false
}
