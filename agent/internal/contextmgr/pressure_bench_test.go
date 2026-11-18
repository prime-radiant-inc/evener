package contextmgr

// Benchmark and equivalence pins for the context-pressure history estimator.
//
// The estimator is the "fallback" branch of estimateUsedTokensFor and the
// incremental walk behind the memo: both reach estimateTokenPartsFor, which
// walks the turns and prices each one in place. The request path takes several
// readings per request (the fold's note elicitation, the full-history estimate
// carried into admission, and the status sample), so the walk is on a hot path
// and must not allocate per reading.
//
// TestEstimateTokenPartsForDoesNotAllocate is that invariant's guard and runs in
// the default suite. The benchmarks beside it measure the walk at 50, 200 and
// 800 turns; their numbers are a record, not a check.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// pressureBenchmarkTurns are the history sizes the audit measured: 50, 200 and
// 800 turns.
var pressureBenchmarkTurns = []int{50, 200, 800}

// pressureBenchmarkHistory builds a history shaped like a real session's: user
// input, an assistant reply that calls a tool, the tool's result, and the
// assistant's answer, repeated. Tool results are present because the estimator
// bills their bytes separately from text, and a plain text-only fixture would
// not exercise that branch.
func pressureBenchmarkHistory(turns int) []schema.Turn {
	history := make([]schema.Turn, 0, turns)
	for i := range turns {
		switch i % 4 {
		case 0:
			history = append(history, schema.NewTurn(schema.TurnUserInput, llm.User(
				fmt.Sprintf("turn %d: read the configuration and report the section it names", i))))
		case 1:
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        fmt.Sprintf("call_%d", i),
						Name:      "read_file",
						Arguments: []byte(fmt.Sprintf(`{"path":"config/section-%d.yaml"}`, i)),
					},
				}},
			}))
		case 2:
			history = append(history, schema.NewTurn(schema.TurnToolResults, llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{{
					Kind: llm.ContentToolResult,
					ToolResult: &llm.ToolResultData{
						ToolCallID: fmt.Sprintf("call_%d", i-1),
						Name:       "read_file",
						Content:    strings.Repeat("section: value\n", 20),
					},
				}},
			}))
		default:
			history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(
				fmt.Sprintf("turn %d: the section declares a threshold of %d", i, i))))
		}
	}
	return history
}

// pressureBenchmarkManager is the manager the benchmark estimates through: the
// package's standard OpenAI test profile, so the resolved target's rules match
// the ones the compaction paths use.
func pressureBenchmarkManager() *Manager {
	return NewManager(testProfile("openai", "gpt-5.2", 1_000_000), nil, cheapmodel.New(nil))
}

// BenchmarkEstimateTokenPartsFor measures the whole-history estimate that the
// context-pressure fallback performs: one pass over every turn, projecting and
// pricing it.
func BenchmarkEstimateTokenPartsFor(b *testing.B) {
	for _, turns := range pressureBenchmarkTurns {
		b.Run(strconv.Itoa(turns), func(b *testing.B) {
			cm := pressureBenchmarkManager()
			history := pressureBenchmarkHistory(turns)
			prof := cm.currentProfile()
			b.ReportAllocs()
			for b.Loop() {
				_ = estimateTokenPartsFor(prof, history)
			}
		})
	}
}

// BenchmarkEstimateUsageFallback measures the reader the request path calls:
// EstimateUsage with no recorded API measurement, which is the fallback branch
// that walks the whole history.
func BenchmarkEstimateUsageFallback(b *testing.B) {
	const sysChars = 40_000
	for _, turns := range pressureBenchmarkTurns {
		b.Run(strconv.Itoa(turns), func(b *testing.B) {
			cm := pressureBenchmarkManager()
			history := pressureBenchmarkHistory(turns)
			b.ReportAllocs()
			for b.Loop() {
				_ = cm.EstimateUsage(history, sysChars)
			}
		})
	}
}

// TestEstimateTokenPartsForDoesNotAllocate pins the walk's cost: one reading of
// a history allocates nothing, at every size the request path meets.
//
// The property is what makes the walk safe on a hot path — the request path
// reads the estimate several times per round over the session's whole history —
// so a regression is a failing test, not a worse benchmark line. It must stay
// serial (no t.Parallel): AllocsPerRun reads the process-wide allocator
// counters, so a concurrent test in this package would perturb the count.
func TestEstimateTokenPartsForDoesNotAllocate(t *testing.T) {
	t.Run("one serving deployment", func(t *testing.T) {
		res := registry.Resolved{Instance: "gateway", ModelID: "current", Protocol: registry.ProtocolOpenAIResponses,
			Transport: registry.Transport{BaseURL: "https://gateway.example.test", Endpoint: "/responses"}}
		prof := provider.FromResolved(res, nil)
		turn := schema.Turn{Kind: schema.TurnAssistant, ResponseProvider: res.Instance, ResponseProtocol: res.Protocol,
			ResponseServingOrigin: llm.ConfiguredServingRecipient(res), ResponseReasoningRoute: prof.ReasoningReplayRoute(),
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: "sealed-48"}}}},
		}
		history := []schema.Turn{turn, turn}
		if allocs := testing.AllocsPerRun(20, func() { _ = estimateTokenPartsFor(prof, history) }); allocs != 0 {
			t.Fatalf("pricing one serving deployment allocates %.0f times per reading", allocs)
		}
	})
	for _, turns := range pressureBenchmarkTurns {
		t.Run(strconv.Itoa(turns), func(t *testing.T) {
			cm := pressureBenchmarkManager()
			history := pressureBenchmarkHistory(turns)
			prof := cm.currentProfile()
			if allocs := testing.AllocsPerRun(20, func() { _ = estimateTokenPartsFor(prof, history) }); allocs != 0 {
				t.Fatalf("estimateTokenPartsFor over %d turns allocates %.0f times per reading, want 0", turns, allocs)
			}
		})
	}
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol+" after model switch", func(t *testing.T) {
			res := registry.Resolved{Instance: "gateway", ModelID: "current", Protocol: protocol, Caps: registry.Caps{ReplayReasoningText: new(true)},
				Transport: registry.Transport{BaseURL: "https://gateway.example.test", StreamEndpoint: "/stream"},
			}
			prof := provider.FromResolved(res, nil)
			history := []schema.Turn{{Kind: schema.TurnAssistant, ResponseProvider: res.Instance, ResponseProtocol: protocol, ResponseRequestModel: "other", ResponseReasoningRoute: "old-route",
				Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "prior work", EncryptedContent: "sealed-48"}}}},
			}}
			if allocs := testing.AllocsPerRun(20, func() { _ = estimateTokenPartsFor(prof, history) }); allocs != 0 {
				t.Fatalf("pricing a prior model's reasoning allocates %.0f times per reading", allocs)
			}
		})
	}
}

// referenceEstimateTokens is the message-list estimator written out in full: it
// projects every turn into an llm.Message list and prices that list with the
// public whole-list estimate, which walks the list rather than the turns. It is
// the oracle for TestEstimateTokenPartsMatchesFrozenMessageList — do not change
// it to match a change in production. A change that makes the production
// estimator differ from this function is a defect in that change, not a stale
// expectation.
func referenceEstimateTokens(res registry.Resolved, turns []schema.Turn) int {
	dropPlainThinking := !llm.ReplaysAssistantThinking(res)
	messages := make([]llm.Message, 0, len(turns))
	for _, t := range turns {
		if t.Kind == schema.TurnAttentionResolution {
			continue
		}
		messages = append(messages, llm.TrimThinkingForReplayMessage(t.Message, dropPlainThinking))
	}
	return llm.EstimateMessagesInputTokensForResolved(res, messages).Tokens
}

// referenceEstimateUsedTokens is the used-token accounting the request path
// reads, expressed over referenceEstimateTokens. It freezes the verdict a
// history produces — the token total, and so the pressure fraction and the
// compaction layers that fire — independently of the estimator's
// implementation.
func referenceEstimateUsedTokens(res registry.Resolved, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	if lastTokens > 0 && measuredLen <= len(history) {
		return lastTokens + referenceEstimateTokens(res, history[measuredLen:])
	}
	return referenceEstimateTokens(res, history) + sysPromptChars/4
}

// TestEstimateTokenPartsMatchesFrozenMessageList pins that the production
// history estimate returns the token total the message-list implementation
// returns, across the shapes a session's history actually holds. The estimator
// decides when context pressure fires, so a history that prices differently here
// moves the round at which a session compacts.
//
// The pin is on the total because that is the only part of the decomposition
// production reads: llm.TokenParts is added across slices and Total is called
// once. The split between its two fields is not observable outside package llm,
// and TestTokenPartEstimatorMatchesMessageList in that package pins the
// per-message walk against the whole-list estimate.
//
// Every case asserts the same three facts: the token total, the pressure
// fraction it produces against a fixed window, and the manager's own reading of
// the same history. The pressure fraction is compared as a float because it is
// what the thresholds are compared against.
func TestEstimateTokenPartsMatchesFrozenMessageList(t *testing.T) {
	t.Parallel()
	const (
		window   = 200_000
		sysChars = 40_000
	)
	cases := []struct {
		name    string
		history []schema.Turn
	}{
		{name: "empty", history: nil},
		{name: "empty slice", history: []schema.Turn{}},
		{
			name: "tool results present",
			history: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("read the config")),
				schema.NewTurn(schema.TurnAssistant, llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{{
						Kind: llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{
							ID: "call_1", Name: "read_file",
							Arguments: []byte(`{"path":"config.yaml"}`),
						},
					}},
				}),
				schema.NewTurn(schema.TurnToolResults, llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{{
						Kind: llm.ContentToolResult,
						ToolResult: &llm.ToolResultData{
							ToolCallID: "call_1", Name: "read_file",
							Content: strings.Repeat("key: value\n", 64),
						},
					}},
				}),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("the file declares three keys")),
			},
		},
		{
			name: "empty turns",
			history: []schema.Turn{
				{Kind: schema.TurnUserInput},
				{Kind: schema.TurnAssistant},
				schema.NewTurn(schema.TurnUserInput, llm.User("still here")),
				{Kind: schema.TurnToolResults},
			},
		},
		{
			name: "huge single turn",
			history: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("summarize this")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("x", 400_000))),
			},
		},
		{
			name: "attention resolution is not billed",
			history: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("steer")),
				schema.NewTurn(schema.TurnAttentionResolution, llm.User(strings.Repeat("r", 4_000))),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("acknowledged")),
			},
		},
		{
			name:    "mixed",
			history: pressureBenchmarkHistory(64),
		},
		{
			name: "thinking replayed by the target",
			history: []schema.Turn{
				schema.NewTurn(schema.TurnAssistant, llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("t", 2_000)}},
						{Kind: llm.ContentText, Text: "answer"},
					},
				}),
			},
		},
	}
	targets := []struct {
		name    string
		profile *provider.Profile
	}{
		{name: "openai responses", profile: testProfile("openai", "gpt-5.2", window)},
		{name: "anthropic", profile: provider.FromResolved(registry.Resolved{
			Instance: "anthropic", ModelID: "claude-test", Protocol: registry.ProtocolAnthropic,
			Caps: registry.Caps{ContextWindow: new(window)},
		}, nil)},
		{name: "openai chat", profile: provider.FromResolved(registry.Resolved{
			Instance: "gw", ModelID: "chat-test", Protocol: registry.ProtocolOpenAIChat,
			Caps: registry.Caps{ContextWindow: new(window)},
		}, nil)},
	}
	for _, target := range targets {
		for _, tc := range cases {
			t.Run(target.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				cm := NewManager(target.profile, nil, cheapmodel.New(nil))
				res := target.profile.Resolved()

				want := referenceEstimateTokens(res, tc.history)
				got := estimateTokenPartsFor(target.profile, tc.history)
				if got.Total() != want {
					t.Fatalf("estimateTokenPartsFor.Total = %d, want %d: the estimator no longer prices this history the way the message-list implementation does", got.Total(), want)
				}

				// The verdict: the numbers the compaction thresholds are compared
				// against, from the manager's own reading of the same history.
				metrics := cm.EstimateUsage(tc.history, sysChars)
				wantUsed := referenceEstimateUsedTokens(res, 0, 0, tc.history, sysChars)
				if metrics.Used != wantUsed {
					t.Fatalf("EstimateUsage.Used = %d, want %d", metrics.Used, wantUsed)
				}
				wantPressure := float64(wantUsed) / float64(window)
				_, pressure, _ := cm.EstimateStatusFor(tc.history, sysChars)
				if pressure != wantPressure {
					t.Fatalf("pressure = %v, want %v", pressure, wantPressure)
				}
			})
		}
	}
}
