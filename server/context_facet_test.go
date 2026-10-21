package server

// The real-session fixture for the context facet publication. The LLM boundary
// is scripted; every other layer is production code: agent.NewSession, the
// production append path, Session.ContextStatus, the server source adapter,
// and Server.refreshFacets(facetContext).
//
// The no-measurement state is constructed, not assumed:
//   - SessionConfig.OpenAIResponsesContinuation is "off" (the accepted
//     disabling value, agent/session_config.go:247-250), so
//     applyResponsesContinuationAnchorPlanning takes its != auto early return
//     and never applies a continuation shadow estimate.
//   - the scripted adapter returns zero llm.Usage, so response usage contributes
//     no input tokens.
//   - exactly one ProcessInput round runs, so no prior measurement exists for
//     attachFullHistoryInputEstimate to carry (it also requires
//     LastInputTokens() > 0).
// The adapter records the actual request so the test can assert
// FullHistoryInputTokensEstimate == 0 and zero usage, which together are exactly
// the inputs effectiveRecordedInputTokens needs to return record=false.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

const (
	contextFacetFixtureUserText      = "u" // repeated 1024 times below
	contextFacetFixtureAssistantText = "a" // repeated 1024 times below
	contextFacetFixtureTextLen       = 1024
	contextFacetFixtureCallID        = "call_fixture"
	contextFacetFixtureResultTool    = "communicate"
)

func contextFacetFixtureText(unit string) string {
	return strings.Repeat(unit, contextFacetFixtureTextLen)
}

// contextFacetFixtureRequest is one model request the session dispatched.
type contextFacetFixtureRequest struct {
	fullHistoryEstimate int
	inputEstimate       int
	historyMode         llm.HistoryMode
	usage               llm.Usage
}

// contextFacetScriptedAdapter is the LLM boundary. It records every request and
// returns one fixed assistant message with zero usage. The message carries a
// fixed 1024-char text part plus one communicate call: production retries bare
// text until the result tool is called (agent/session_tool_round.go:109-123), so
// the result call is forced by the production contract and is not a fixture
// choice. The round then appends the assistant turn and one tool-result turn.
type contextFacetScriptedAdapter struct {
	name string
	args []byte

	mu   sync.Mutex
	reqs []contextFacetFixtureRequest
}

func (a *contextFacetScriptedAdapter) Name() string { return a.name }

func (a *contextFacetScriptedAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, contextFacetFixtureRequest{
		fullHistoryEstimate: req.FullHistoryInputTokensEstimate,
		inputEstimate:       req.InputTokensEstimate,
		historyMode:         req.HistoryMode,
		usage:               llm.Usage{},
	})
	a.mu.Unlock()
	return llm.Response{
		Provider: a.name,
		Model:    req.Model,
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: contextFacetFixtureText(contextFacetFixtureAssistantText)},
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        contextFacetFixtureCallID,
						Name:      contextFacetFixtureResultTool,
						Arguments: a.args,
						Type:      "function",
					},
				},
			},
		},
	}, nil
}

func (a *contextFacetScriptedAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *contextFacetScriptedAdapter) requests() []contextFacetFixtureRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]contextFacetFixtureRequest, len(a.reqs))
	copy(out, a.reqs)
	return out
}

// contextFacetEnvelopeSource is the production seam (ThreadEnvelopeSource) over
// a real agent.Session. It counts ContextStatus with a dedicated counter so the
// fixture can state that one publication calls the combined sample exactly
// once, without an all-method counter.
type contextFacetEnvelopeSource struct {
	stubThreadEnvelopeSource
	sess *agent.Session

	contextCalls int
}

func (s *contextFacetEnvelopeSource) ContextStatus() (ContextMetrics, float64) {
	s.contextCalls++
	metrics, pressure := s.sess.ContextStatus()
	return ContextMetrics{
		Used:      metrics.Used,
		Window:    metrics.Window,
		Remaining: metrics.Remaining,
		Assumed:   metrics.Assumed,
	}, pressure
}

type contextFacetFixture struct {
	srv              *Server
	src              *contextFacetEnvelopeSource
	expectedMetrics  ContextMetrics
	expectedPressure float64
}

// newContextFacetFixture builds the real-session fixture and fails before any
// measurement if the no-measurement condition is not actually constructed.
func newContextFacetFixture(t testing.TB) *contextFacetFixture {
	t.Helper()

	userText := contextFacetFixtureText(contextFacetFixtureUserText)
	assistantText := contextFacetFixtureText(contextFacetFixtureAssistantText)
	callArgs, err := json.Marshal(map[string]any{
		"message":  assistantText,
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	if err != nil {
		t.Fatalf("marshal communicate args: %v", err)
	}
	resultOutput, err := json.Marshal(map[string]any{
		"accepted": true,
		"end_turn": true,
		"inbox":    []string{},
	})
	if err != nil {
		t.Fatalf("marshal communicate result: %v", err)
	}

	dir := t.TempDir()
	adapter := &contextFacetScriptedAdapter{name: "openai", args: callArgs}
	c := llm.NewClient()
	c.Register(adapter)
	profile := provider.NewOpenAIProfile("gpt-5.2")
	sess, err := agent.NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "off",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, userText, nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	reqs := adapter.requests()
	if len(reqs) == 0 {
		t.Fatal("the scripted adapter saw no request: fixture proves nothing")
	}
	for i, r := range reqs {
		if r.fullHistoryEstimate != 0 {
			t.Fatalf("request %d carried FullHistoryInputTokensEstimate=%d, want 0; the no-measurement pin is not constructed", i, r.fullHistoryEstimate)
		}
		if r.inputEstimate != 0 {
			t.Fatalf("request %d carried InputTokensEstimate=%d, want 0", i, r.inputEstimate)
		}
		// The continuation "off" early return sets full-history mode when the
		// shaping layer leaves it blank; the adapter may still see the blank
		// default. What matters is that no delta was planned.
		if r.historyMode == llm.HistoryModeResponsesDelta {
			t.Fatalf("request %d carried a responses delta (HistoryMode=%q) despite OpenAIResponsesContinuation=off", i, r.historyMode)
		}
		if r.usage.InputTokens != 0 || r.usage.OutputTokens != 0 || r.usage.TotalTokens != 0 ||
			(r.usage.CacheReadTokens != nil && *r.usage.CacheReadTokens != 0) ||
			(r.usage.CacheWriteTokens != nil && *r.usage.CacheWriteTokens != 0) {
			t.Fatalf("request %d response usage is not zero: %+v", i, r.usage)
		}
	}
	t.Logf("scripted boundary: %d request(s), every FullHistoryInputTokensEstimate=0, every usage zero (continuation off)", len(reqs))

	// Independently authored expectation: apply the documented chars/4 rule to
	// the turns the session durably recorded, without calling the estimator.
	// The production round also appends an ENVIRONMENT turn whose text carries
	// the temp cwd and the current date, so a hard-coded total would be wrong;
	// the durable transcript is the exact fixture-known input.
	used := contextFacetTranscriptCharsOver4(t, dir, string(resultOutput))
	window := profile.ContextWindowSize()
	if window <= 0 {
		t.Fatalf("profile window = %d, want a known positive window", window)
	}
	expected := ContextMetrics{Used: used, Window: window, Remaining: max(window-used, 0), Assumed: false}
	expectedPressure := float64(used) / float64(window)

	// Secondary publication-preservation check (not the oracle): the session's
	// own accessors must agree with the independently authored figures. This is
	// also the non-empty-history check: a measured baseline would report zero
	// here, and an empty history would report zero used tokens.
	gotMetrics, gotPressure := sess.ContextStatus()
	if gotMetrics != (agent.ContextMetrics{Used: expected.Used, Window: expected.Window, Remaining: expected.Remaining, Assumed: expected.Assumed}) {
		t.Fatalf("session ContextStatus metrics = %+v, want fixture-authored %+v", gotMetrics, expected)
	}
	if gotPressure != expectedPressure {
		t.Fatalf("session ContextStatus pressure = %v, want fixture-authored %v", gotPressure, expectedPressure)
	}

	srv := NewServer(ServerConfig{AppReplaySize: 100})
	srv.SetAppIdentity("local", "th_context_fixture")
	src := &contextFacetEnvelopeSource{sess: sess}
	srv.SetThreadEnvelopeSource(src)
	srv.refreshFacets(facetContext)

	if srv.appEnvelope.ContextMetrics != expected || srv.appEnvelope.ContextPressure != expectedPressure {
		t.Fatalf("published pair = %+v/%v, want fixture-authored %+v/%v",
			srv.appEnvelope.ContextMetrics, srv.appEnvelope.ContextPressure, expected, expectedPressure)
	}
	if src.contextCalls != 1 {
		t.Fatalf("source ContextStatus calls after one publication = %d, want 1", src.contextCalls)
	}

	return &contextFacetFixture{
		srv:              srv,
		src:              src,
		expectedMetrics:  expected,
		expectedPressure: expectedPressure,
	}
}

// contextFacetTranscriptCharsOver4 applies the documented chars/4 estimator rule
// to the turns the session recorded in its transcript. It is independent of the
// context manager and the session accessors: it reads the durable transcript
// JSONL and sums the model-facing characters itself.
func contextFacetTranscriptCharsOver4(t testing.TB, dir, wantResult string) int {
	t.Helper()
	var path string
	if err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(p, ".transcript.jsonl") {
			path = p
		}
		return nil
	}); err != nil {
		t.Fatalf("walk state dir: %v", err)
	}
	if path == "" {
		t.Fatal("no transcript jsonl in the session state dir")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}

	type part struct {
		Kind     string `json:"kind"`
		Text     string `json:"text"`
		ToolCall *struct {
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"tool_call"`
		ToolResult *struct {
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
			Content    string `json:"content"`
		} `json:"tool_result"`
	}
	type entry struct {
		Kind string `json:"kind"`
		Turn *struct {
			Kind    string `json:"kind"`
			Message struct {
				Content []part `json:"content"`
			} `json:"message"`
		} `json:"turn"`
	}

	chars := 0
	sawResult := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse transcript line: %v", err)
		}
		if e.Kind != "entry" || e.Turn == nil || e.Turn.Kind == "ATTENTION_RESOLUTION" {
			continue
		}
		for _, p := range e.Turn.Message.Content {
			switch p.Kind {
			case "text":
				chars += len(p.Text)
			case "tool_call":
				if p.ToolCall != nil {
					chars += len(p.ToolCall.ID) + len(p.ToolCall.Name) + len(p.ToolCall.Arguments)
				}
			case "tool_result":
				if p.ToolResult != nil {
					chars += len(p.ToolResult.ToolCallID) + len(p.ToolResult.Name) + len(p.ToolResult.Content)
					if p.ToolResult.Name == contextFacetFixtureResultTool {
						sawResult = true
						if p.ToolResult.Content != wantResult {
							t.Fatalf("recorded %s result = %q, want the production reply %q", contextFacetFixtureResultTool, p.ToolResult.Content, wantResult)
						}
					}
				}
			}
		}
	}
	if !sawResult {
		t.Fatalf("transcript holds no %s tool result: the fixture round did not finish through the result tool", contextFacetFixtureResultTool)
	}
	return chars / 4
}

// TestContextFacetAllocationBudget pins the committed publication cost on the
// real-session fixture. The budget is one history copy plus one []llm.Message
// estimate; the unchanged two-method baseline exceeded it through this same
// publication path (the retained fixture-proof evidence). Upper bound only.
func TestContextFacetAllocationBudget(t *testing.T) {
	fixture := newContextFacetFixture(t)
	pairOK := true
	allocs := testing.AllocsPerRun(20, func() {
		fixture.srv.refreshFacets(facetContext)
		if fixture.srv.appEnvelope.ContextMetrics != fixture.expectedMetrics || fixture.srv.appEnvelope.ContextPressure != fixture.expectedPressure {
			pairOK = false
		}
	})
	if !pairOK {
		t.Fatalf("a measured publication did not carry the fixture pair %+v/%v", fixture.expectedMetrics, fixture.expectedPressure)
	}
	if allocs > 2 {
		t.Fatalf("context facet allocates %v, budget 2", allocs)
	}
	t.Logf("fixture expected metrics=%+v pressure=%v; publication allocations=%v (budget 2)",
		fixture.expectedMetrics, fixture.expectedPressure, allocs)
}
