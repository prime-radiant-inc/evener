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
//   - the scripted adapter returns a response with zero llm.Usage and records
//     that returned response's usage, so response usage contributes no input
//     tokens.
//   - exactly one ProcessInput round runs, so no prior measurement exists for
//     attachFullHistoryInputEstimate to carry (it also requires
//     LastInputTokens() > 0).
// The adapter records the actual request so the test can assert
// FullHistoryInputTokensEstimate == 0 and zero usage, which together are exactly
// the inputs effectiveRecordedInputTokens needs to return record=false.

import (
	"bufio"
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
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

const (
	contextFacetFixtureUserText      = "u" // repeated 1024 times below
	contextFacetFixtureAssistantText = "a" // repeated 1024 times below
	contextFacetFixtureTextLen       = 1024
	contextFacetFixtureCallID        = "call_fixture"
	contextFacetFixtureResultTool    = "communicate"
	contextFacetFixtureTitle         = "context facet fixture"
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
// text until the result tool is called, so
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
	resp := llm.Response{
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
	}
	a.mu.Lock()
	a.reqs = append(a.reqs, contextFacetFixtureRequest{
		fullHistoryEstimate: req.FullHistoryInputTokensEstimate,
		inputEstimate:       req.InputTokensEstimate,
		historyMode:         req.HistoryMode,
		usage:               resp.Usage,
	})
	a.mu.Unlock()
	return resp, nil
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
	sess             *agent.Session
	srv              *Server
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

	// Assign a title through the real Rename path before the first input.
	// launchInitialPromptNamer returns when naming.set or naming.value is
	// already set, so NameSource="user" suppresses the asynchronous namer and
	// its logging instead of letting them overlap the measured publications.
	// The recorded baseline fixture had no title; this changes that setup.
	if err := sess.Rename(contextFacetFixtureTitle); err != nil {
		t.Fatalf("Rename: %v", err)
	}

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
		sess:             sess,
		srv:              srv,
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
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	defer f.Close() //nolint:errcheck // read-only

	reader := bufio.NewReaderSize(f, 1<<20)
	if _, err := transcript.ReadHeader(context.Background(), reader, transcript.DefaultMaxLineBytes); err != nil {
		t.Fatalf("read transcript header: %v", err)
	}

	chars := 0
	sawResult := false
	for {
		line, complete, _, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			t.Fatalf("read transcript: %v", err)
		}
		if !complete {
			break
		}
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatalf("decode transcript entry: %v", err)
		}
		if entry.Turn.Kind == schema.TurnAttentionResolution {
			continue
		}
		for _, p := range entry.Turn.Message.Content {
			switch p.Kind {
			case llm.ContentText:
				chars += len(p.Text)
			case llm.ContentToolCall:
				if p.ToolCall != nil {
					chars += len(p.ToolCall.ID) + len(p.ToolCall.Name) + len(p.ToolCall.Arguments)
				}
			case llm.ContentToolResult:
				if p.ToolResult != nil {
					content, ok := p.ToolResult.Content.(string)
					if !ok {
						t.Fatalf("recorded %s result content has type %T, want string", p.ToolResult.Name, p.ToolResult.Content)
					}
					chars += len(p.ToolResult.ToolCallID) + len(p.ToolResult.Name) + len(content)
					if p.ToolResult.Name == contextFacetFixtureResultTool {
						sawResult = true
						if content != wantResult {
							t.Fatalf("recorded %s result = %q, want the production reply %q", contextFacetFixtureResultTool, content, wantResult)
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
