package agent

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSessionTokenBudgetPrimaryUsesFullHistoryEstimate(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "budget-gw", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ok") },
	}}
	client.Register(adapter)

	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = profile.WithResolved(resolved)

	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		NoProjectPrompts: true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil

	sess.history = []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("task"))}
	// The context manager's prior API measurement is the larger supplied estimate
	// the agent must carry into the newly built full-history request.
	sess.contextMgr.RecordInputTokens(500_000, len(sess.history))
	ctx := context.Background()
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(ctx, 0, new(events.RoundTimings))
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	if req.MaxTokens == nil || *req.MaxTokens <= 0 || *req.MaxTokens > 19_045 {
		t.Fatalf("prepared MaxTokens = %v, want positive reduced allocation within total cap", req.MaxTokens)
	}
	if _, _, _, err := sess.callModelWithFallback(ctx, profile, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}

	requests := adapter.Requests()
	preparedMax, providerMax := 0, 0
	if req.MaxTokens != nil {
		preparedMax = *req.MaxTokens
	}
	if requests[0].MaxTokens != nil {
		providerMax = *requests[0].MaxTokens
	}
	t.Logf("caps: context=%d output=%d; prepared request: input=%d full=%d max=%d; provider request: input=%d full=%d max=%d", profile.ContextWindowSize(), profile.MaxOutputTokens(), req.InputTokensEstimate, req.FullHistoryInputTokensEstimate, preparedMax, requests[0].InputTokensEstimate, requests[0].FullHistoryInputTokensEstimate, providerMax)
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	if requests[0].MaxTokens == nil || *requests[0].MaxTokens <= 0 {
		t.Fatalf("provider MaxTokens = %v, want positive reduced allocation", requests[0].MaxTokens)
	}
	if *requests[0].MaxTokens > 19_045 {
		t.Fatalf("provider MaxTokens = %d, want positive allocation satisfying 524288 total cap", *requests[0].MaxTokens)
	}
}

func TestSessionContinuationTokenBudgetShadowBlocksUnsafeDelta(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "budget-gw", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("unsafe") },
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(llm.Request) (int, bool) { return 1, true }
	req := llm.Request{
		Provider:                       profile.ID(),
		Model:                          profile.Model(),
		Messages:                       []llm.Message{llm.User("tiny delta")},
		MaxTokens:                      new(131_072),
		HistoryMode:                    llm.HistoryModeResponsesDelta,
		PreviousResponseID:             "resp-anchor",
		FullHistoryInputTokensEstimate: 524_000,
	}
	req = sess.applyResponsesContinuationShadowEstimate(req)
	t.Logf("continuation request full=%d input=%d max=%d", req.FullHistoryInputTokensEstimate, req.InputTokensEstimate, *req.MaxTokens)
	if _, _, _, err := sess.callModelWithFallback(context.Background(), profile, req, nil, "", 0); err == nil {
		t.Fatal("unsafe continuation delta was accepted")
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("unsafe continuation provider calls = %d, want 0", len(requests))
	}
}

func TestSessionContinuationTokenBudgetPreClientCarriesAdmittedShadow(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "budget-cont-observe", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("continuation") },
	}}
	client.Register(adapter)
	var mu sync.Mutex
	var observed []llm.Request
	client.Use(llm.MiddlewareFunc{Complete: func(ctx context.Context, req llm.Request, next llm.CompleteFunc) (llm.Response, error) {
		mu.Lock()
		observed = append(observed, req)
		mu.Unlock()
		return next(ctx, req)
	}})
	profile := testOpenAICompatProfile("budget-cont-observe", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(llm.Request) (int, bool) { return 400_000, true }
	req := llm.Request{Provider: profile.ID(), Model: profile.Model(), Messages: []llm.Message{llm.User("tiny delta")}, MaxTokens: new(131_072), HistoryMode: llm.HistoryModeResponsesDelta, PreviousResponseID: "resp-anchor"}
	req = sess.applyResponsesContinuationShadowEstimate(req)
	if _, _, _, err := sess.callModelWithFallback(context.Background(), profile, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 1 {
		t.Fatalf("middleware observations = %d, want one admitted continuation request", len(observed))
	}
	got := observed[0]
	if got.HistoryMode != llm.HistoryModeResponsesDelta || got.FullHistoryInputTokensEstimate != 400_000 {
		t.Fatalf("continuation handoff = mode %q full=%d, want responses_delta/full=400000", got.HistoryMode, got.FullHistoryInputTokensEstimate)
	}
	if got.MaxTokens == nil || *got.MaxTokens <= 0 || *got.MaxTokens >= 131_072 {
		t.Fatalf("continuation admitted MaxTokens = %v, want positive reduction below primary cap", got.MaxTokens)
	}
}

func TestSessionAnchorTokenBudgetRecoveryClearsPrimaryAllocation(t *testing.T) {
	primary := new(131_072)
	req := llm.Request{
		Provider:           "budget-gw",
		Model:              "test",
		Messages:           []llm.Message{llm.User("delta")},
		MaxTokens:          primary,
		HistoryMode:        llm.HistoryModeResponsesDelta,
		PreviousResponseID: "resp-anchor",
	}
	fullHistory := []llm.Message{llm.User(strings.Repeat("history ", 100))}
	fallback := responsesContinuationFullHistoryFallbackRequest(req, fullHistory)
	if fallback.MaxTokens != nil {
		t.Fatalf("anchor recovery MaxTokens = %d, want cleared before re-budgeting", *fallback.MaxTokens)
	}
}

func TestSessionFallbackTokenBudgetUsesFallbackCap(t *testing.T) {
	client := llm.NewClient()
	primaryAdapter := &fakeErrAdapter{name: "budget-gw", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-gw", 403, "primary rejected", nil, nil)
		},
	}}
	fallbackAdapter := &fakeAdapter{name: "budget-fallback", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("fallback") },
	}}
	client.Register(primaryAdapter)
	client.Register(fallbackAdapter)

	primary := testOpenAICompatProfile("budget-gw", "primary", 0)
	primaryResolved := primary.Resolved()
	primaryResolved.Caps.ContextWindow = new(524_288)
	primaryResolved.Caps.MaxOutputTokens = new(131_072)
	primary = primary.WithResolved(primaryResolved)
	fallback := testOpenAICompatProfile("budget-fallback", "fallback", 0)
	fallbackResolved := fallback.Resolved()
	fallbackResolved.Caps.ContextWindow = new(65_536)
	fallbackResolved.Caps.MaxOutputTokens = new(8_192)
	fallback = fallback.WithResolved(fallbackResolved)

	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-fallback/fallback"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }

	req := llm.Request{
		Provider:  primary.ID(),
		Model:     primary.Model(),
		Messages:  []llm.Message{llm.User("task")},
		MaxTokens: new(131_072),
	}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primary, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	requests := fallbackAdapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("fallback provider requests = %d, want 1", len(requests))
	}
	if requests[0].MaxTokens == nil || *requests[0].MaxTokens > 8_192 {
		t.Fatalf("fallback MaxTokens = %v, want <= 8192", requests[0].MaxTokens)
	}
	if requests[0].HistoryMode != llm.HistoryModeFullHistory || len(requests[0].Messages) != 1 || requests[0].Messages[0].Text() != "task" {
		t.Fatalf("fallback history = mode %q messages %+v, want original full-history task", requests[0].HistoryMode, requests[0].Messages)
	}
}

func TestSessionDeltaWithoutFullHistoryDoesNotDispatchModelFallback(t *testing.T) {
	client := llm.NewClient()
	primaryAdapter := &fakeErrAdapter{name: "delta-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("delta-primary", 403, "primary rejected", nil, nil)
		},
	}}
	fallbackAdapter := &fakeAdapter{name: "delta-fallback", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("unsafe fallback") },
	}}
	client.Register(primaryAdapter)
	client.Register(fallbackAdapter)
	primary := testOpenAICompatProfile("delta-primary", "primary", 0)
	fallback := testOpenAICompatProfile("delta-fallback", "fallback", 0)
	sess, err := NewSession(client, primary, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.cfg.ModelFallbacks = []string{"delta-fallback/fallback"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallback, nil }
	req := llm.Request{
		Provider:           primary.ID(),
		Model:              primary.Model(),
		Messages:           []llm.Message{llm.User("delta only")},
		HistoryMode:        llm.HistoryModeResponsesDelta,
		PreviousResponseID: "resp-anchor",
	}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primary, req, nil, "", 0); err == nil {
		t.Fatal("callModelWithFallback succeeded by relabeling delta messages as full history")
	}
	if got := len(primaryAdapter.Requests()); got != 1 {
		t.Fatalf("primary provider requests = %d, want 1", got)
	}
	if got := len(fallbackAdapter.Requests()); got != 0 {
		t.Fatalf("fallback provider requests = %d, want 0 without full history", got)
	}
}

func TestSessionContextBudgetCompactRetry(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "budget-gw", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "recovered") },
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(131_072)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	sess.contextMgr.RecordInputTokens(500_000, 0)
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if requests := adapter.Requests(); len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 after one local recovery", len(requests))
	}
}

func TestSessionContextBudgetCompactRetryTerminalNoProgress(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeAdapter{name: "budget-gw", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "unexpected") },
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(100)
	resolved.Caps.MaxOutputTokens = new(8)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded despite unrecoverable local budget")
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want 0 when both preparations fail", len(requests))
	}
}

func TestSessionContextBudgetProviderContextRetry(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "budget-gw", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-gw", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) { return communicateResponse(true, "recovered"), nil },
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(8_192)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if requests := adapter.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests = %d, want exactly one compacted retry", len(requests))
	}
}

func TestSessionContextBudgetProviderContextTerminal(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "budget-gw", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-gw", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-gw", 413, "context length exceeded", nil, nil)
		},
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-gw", "test", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(524_288)
	resolved.Caps.MaxOutputTokens = new(8_192)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded after second provider context error")
	}
	if requests := adapter.Requests(); len(requests) != 2 {
		t.Fatalf("provider requests = %d, want exactly one retry then terminal", len(requests))
	}
}

func TestSessionProviderContextRecoveryPrecedesConfiguredFallback(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-primary", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) { return communicateResponse(true, "recovered"), nil },
	}}
	fallback := &fakeAdapter{name: "budget-fallback", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "fallback") },
	}}
	client.Register(primary)
	client.Register(fallback)
	profile := testOpenAICompatProfile("budget-primary", "primary", 0)
	fbProfile := testOpenAICompatProfile("budget-fallback", "fallback", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-fallback/fallback"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fbProfile, nil }
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(primary.Requests()); got != 2 {
		t.Fatalf("primary requests = %d, want one context retry", got)
	}
	if got := len(fallback.Requests()); got != 0 {
		t.Fatalf("fallback requests = %d, want zero before primary recovery", got)
	}
}

func TestSessionProviderContextRecoverySecondErrorSkipsConfiguredFallback(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-primary-terminal", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-primary-terminal", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-primary-terminal", 413, "context length exceeded", nil, nil)
		},
	}}
	fallback := &fakeAdapter{name: "budget-fallback-terminal", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "unexpected fallback") },
	}}
	client.Register(primary)
	client.Register(fallback)
	profile := testOpenAICompatProfile("budget-primary-terminal", "primary", 0)
	fbProfile := testOpenAICompatProfile("budget-fallback-terminal", "fallback", 0)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-fallback-terminal/fallback"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fbProfile, nil }
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded after second provider context error")
	}
	if got := len(primary.Requests()); got != 2 {
		t.Fatalf("primary requests = %d, want one retry then terminal", got)
	}
	if got := len(fallback.Requests()); got != 0 {
		t.Fatalf("fallback requests = %d, want zero after terminal primary context error", got)
	}
}

func TestSessionFallbackBudgetObservationBeforeClientAdmission(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-observe-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-observe-primary", 403, "primary rejected", nil, nil)
		},
	}}
	fallback := &fakeAdapter{name: "budget-observe-fallback", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("fallback") },
	}}
	client.Register(primary)
	client.Register(fallback)
	var mu sync.Mutex
	var observed []llm.Request
	client.Use(llm.MiddlewareFunc{Complete: func(ctx context.Context, req llm.Request, next llm.CompleteFunc) (llm.Response, error) {
		mu.Lock()
		observed = append(observed, req)
		mu.Unlock()
		return next(ctx, req)
	}})
	primaryProfile := testOpenAICompatProfile("budget-observe-primary", "primary", 0)
	fallbackProfile := testOpenAICompatProfile("budget-observe-fallback", "fallback", 0)
	fallbackResolved := fallbackProfile.Resolved()
	fallbackResolved.Caps.ContextWindow = new(65_536)
	fallbackResolved.Caps.MaxOutputTokens = nil
	fallbackProfile = fallbackProfile.WithResolved(fallbackResolved)
	sess, err := NewSession(client, primaryProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-observe-fallback/fallback"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallbackProfile, nil }
	req := llm.Request{Provider: primaryProfile.ID(), Model: primaryProfile.Model(), Messages: []llm.Message{llm.User("task")}, MaxTokens: new(131_072)}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primaryProfile, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 2 {
		t.Fatalf("middleware observations = %d, want primary and fallback", len(observed))
	}
	got := observed[1]
	if got.Provider != fallbackProfile.ID() || got.Model != fallbackProfile.Model() {
		t.Fatalf("fallback handoff target = %s/%s, want %s/%s", got.Provider, got.Model, fallbackProfile.ID(), fallbackProfile.Model())
	}
	if got.MaxTokens == nil || *got.MaxTokens <= 0 || *got.MaxTokens > 65_536 {
		t.Fatalf("fallback handoff MaxTokens = %v, want positive allocation within fallback context", got.MaxTokens)
	}
}

func TestSessionFallbackRecomputesProviderSensitiveFullHistoryEstimate(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-media-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-media-primary", 403, "primary rejected", nil, nil)
		},
	}}
	fallback := &fakeAdapter{name: "budget-gemini", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("fallback") },
	}}
	client.Register(primary)
	client.Register(fallback)
	primaryProfile := testOpenAICompatProfile("budget-media-primary", "gpt-primary", 0)
	fallbackProfile := testOpenAICompatProfile("budget-gemini", "gemini-test", 0)
	fallbackResolved := fallbackProfile.Resolved()
	fallbackResolved.Caps.ContextWindow = new(65_536)
	fallbackResolved.Caps.MaxOutputTokens = nil
	fallbackProfile = fallbackProfile.WithResolved(fallbackResolved)
	sess, err := NewSession(client, primaryProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-gemini/gemini-test"}
	sess.resolveProfile = func(string) (*provider.Profile, error) { return fallbackProfile, nil }
	fullHistory := []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: task4LargePNG()}}}}}
	req := llm.Request{Provider: primaryProfile.ID(), Model: primaryProfile.Model(), Messages: fullHistory, MaxTokens: new(131_072), FullHistoryInputTokensEstimate: 100_000}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primaryProfile, req, fullHistory, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	requests := fallback.Requests()
	if len(requests) != 1 {
		t.Fatalf("fallback requests = %d, want one", len(requests))
	}
	if requests[0].FullHistoryInputTokensEstimate <= 0 || requests[0].FullHistoryInputTokensEstimate >= req.FullHistoryInputTokensEstimate {
		t.Fatalf("fallback full-history estimate = %d, want recomputed value below stale primary estimate %d", requests[0].FullHistoryInputTokensEstimate, req.FullHistoryInputTokensEstimate)
	}
	if requests[0].InputTokensEstimate != requests[0].FullHistoryInputTokensEstimate {
		t.Fatalf("fallback input/full estimates = %d/%d, want consistent full-history values", requests[0].InputTokensEstimate, requests[0].FullHistoryInputTokensEstimate)
	}
}

func TestSessionAnchorRejectionRebudgetsFullHistoryRequest(t *testing.T) {
	client := llm.NewClient()
	adapter := &fakeErrAdapter{name: "budget-anchor", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-anchor", 404, "previous response not found", map[string]any{"code": "previous_response_not_found"}, nil)
		},
		func(llm.Request) (llm.Response, error) { return communicateResponse(true, "recovered"), nil },
	}}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-anchor", "gpt-anchor", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(20_000)
	resolved.Caps.MaxOutputTokens = new(1_000)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	req := llm.Request{Provider: profile.ID(), Model: profile.Model(), Messages: []llm.Message{llm.User("delta")}, MaxTokens: new(1_000), HistoryMode: llm.HistoryModeResponsesDelta, PreviousResponseID: "resp-anchor", FullHistoryInputTokensEstimate: 18_000}
	fullHistory := []llm.Message{llm.User("small history")}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), profile, req, fullHistory, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("anchor requests = %d, want rejected anchor plus recovery", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("primary HistoryMode = %q, want responses_delta", requests[0].HistoryMode)
	}
	if requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback || requests[1].MaxTokens == nil || *requests[1].MaxTokens != 1_000 {
		t.Fatalf("anchor recovery request = mode %q max %v, want full-history fallback with max 1000", requests[1].HistoryMode, requests[1].MaxTokens)
	}
}

func TestSessionUnsafeFallbackSkippedBeforeLaterFallbackSucceeds(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-chain-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-chain-primary", 403, "primary rejected", nil, nil)
		},
	}}
	unsafe := &fakeAdapter{name: "budget-chain-unsafe", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("must not run") },
	}}
	later := &fakeAdapter{name: "budget-chain-later", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("later fallback") },
	}}
	client.Register(primary)
	client.Register(unsafe)
	client.Register(later)
	primaryProfile := testOpenAICompatProfile("budget-chain-primary", "primary", 0)
	unsafeProfile := testOpenAICompatProfile("budget-chain-unsafe", "unsafe", 0)
	unsafeResolved := unsafeProfile.Resolved()
	unsafeResolved.Caps.ContextWindow = new(100)
	unsafeResolved.Caps.MaxOutputTokens = new(8)
	unsafeProfile = unsafeProfile.WithResolved(unsafeResolved)
	laterProfile := testOpenAICompatProfile("budget-chain-later", "later", 0)
	sess, err := NewSession(client, primaryProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.cfg.ModelFallbacks = []string{"budget-chain-unsafe/unsafe", "budget-chain-later/later"}
	sess.resolveProfile = func(ref string) (*provider.Profile, error) {
		if strings.Contains(ref, "unsafe") {
			return unsafeProfile, nil
		}
		return laterProfile, nil
	}
	req := llm.Request{Provider: primaryProfile.ID(), Model: primaryProfile.Model(), Messages: []llm.Message{llm.User("task")}, MaxTokens: new(131_072)}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primaryProfile, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if got := len(unsafe.Requests()); got != 0 {
		t.Fatalf("unsafe fallback requests = %d, want zero", got)
	}
	if got := len(later.Requests()); got != 1 {
		t.Fatalf("later fallback requests = %d, want one", got)
	}
}

func TestSessionStreamTokenBudgetAdmissionMatchesComplete(t *testing.T) {
	client := llm.NewClient()
	adapter := &streamingAdapter{name: "budget-stream", streamErr: llm.ErrStreamUnsupported, completeResult: finalResponse("unexpected")}
	client.Register(adapter)
	profile := testOpenAICompatProfile("budget-stream", "glm-5", 0)
	resolved := profile.Resolved()
	resolved.Caps.ContextWindow = new(100)
	resolved.Caps.MaxOutputTokens = new(8)
	profile = profile.WithResolved(resolved)
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	req := llm.Request{Provider: profile.ID(), Model: profile.Model(), Messages: []llm.Message{llm.User("task")}, InputTokensEstimate: 100, MaxTokens: new(8)}
	if _, err := sess.callModel(context.Background(), llm.DefaultRetryPolicy(), profile, req, &groupRecord{}); err == nil {
		t.Fatal("Stream succeeded despite local token-budget overflow")
	}
	if complete, stream := adapter.Counts(); complete != 0 || stream != 0 {
		t.Fatalf("adapter calls = complete %d stream %d, want zero", complete, stream)
	}
}

func TestSessionFallbackContextRecoveryPrecedesLaterFallback(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-round2-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-primary", 403, "primary rejected", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-primary", 403, "primary rejected", nil, nil)
		},
	}}
	fallbackA := &fakeErrAdapter{name: "budget-round2-fallback-a", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-fallback-a", 413, "context length exceeded", nil, nil)
		},
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-fallback-a", 413, "context length exceeded", nil, nil)
		},
	}}
	fallbackB := &fakeAdapter{name: "budget-round2-fallback-b", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("must not bypass context recovery") },
	}}
	client.Register(primary)
	client.Register(fallbackA)
	client.Register(fallbackB)
	primaryProfile := testOpenAICompatProfile("budget-round2-primary", "primary", 0)
	fallbackAProfile := testOpenAICompatProfile("budget-round2-fallback-a", "fallback-a", 0)
	fallbackBProfile := testOpenAICompatProfile("budget-round2-fallback-b", "fallback-b", 0)
	sess, err := NewSession(client, primaryProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-round2-fallback-a/fallback-a", "budget-round2-fallback-b/fallback-b"}
	sess.resolveProfile = func(ref string) (*provider.Profile, error) {
		switch {
		case strings.Contains(ref, "fallback-a"):
			return fallbackAProfile, nil
		case strings.Contains(ref, "fallback-b"):
			return fallbackBProfile, nil
		default:
			return primaryProfile, nil
		}
	}
	if _, err := sess.ProcessInput(context.Background(), "task", nil); err == nil {
		t.Fatal("ProcessInput succeeded after second fallback context error")
	}
	if got := len(primary.Requests()); got != 2 {
		t.Fatalf("primary requests = %d, want two primary attempts around one outer recovery", got)
	}
	if got := len(fallbackA.Requests()); got != 2 {
		t.Fatalf("fallback A requests = %d, want one per bounded primary attempt", got)
	}
	if got := len(fallbackB.Requests()); got != 0 {
		t.Fatalf("fallback B requests = %d, want zero after fallback A context errors", got)
	}
}

func TestSessionFallbackNonContextErrorContinuesConfiguredChain(t *testing.T) {
	client := llm.NewClient()
	primary := &fakeErrAdapter{name: "budget-round2-chain-primary", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-chain-primary", 403, "primary rejected", nil, nil)
		},
	}}
	fallbackA := &fakeErrAdapter{name: "budget-round2-chain-a", steps: []func(llm.Request) (llm.Response, error){
		func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("budget-round2-chain-a", 404, "fallback unavailable", nil, nil)
		},
	}}
	fallbackB := &fakeAdapter{name: "budget-round2-chain-b", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("ordinary fallback") },
	}}
	client.Register(primary)
	client.Register(fallbackA)
	client.Register(fallbackB)
	primaryProfile := testOpenAICompatProfile("budget-round2-chain-primary", "primary", 0)
	fallbackAProfile := testOpenAICompatProfile("budget-round2-chain-a", "fallback-a", 0)
	fallbackBProfile := testOpenAICompatProfile("budget-round2-chain-b", "fallback-b", 0)
	sess, err := NewSession(client, primaryProfile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{NoProjectPrompts: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	sess.strategy = nil
	sess.cfg.ModelFallbacks = []string{"budget-round2-chain-a/fallback-a", "budget-round2-chain-b/fallback-b"}
	sess.resolveProfile = func(ref string) (*provider.Profile, error) {
		if strings.Contains(ref, "chain-a") {
			return fallbackAProfile, nil
		}
		return fallbackBProfile, nil
	}
	req := llm.Request{Provider: primaryProfile.ID(), Model: primaryProfile.Model(), Messages: []llm.Message{llm.User("task")}, MaxTokens: new(131_072)}
	if _, _, _, err := sess.callModelWithFallback(context.Background(), primaryProfile, req, nil, "", 0); err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if got := len(fallbackA.Requests()); got != 1 {
		t.Fatalf("fallback A requests = %d, want one ordinary permanent-error attempt", got)
	}
	if got := len(fallbackB.Requests()); got != 1 {
		t.Fatalf("fallback B requests = %d, want one after ordinary fallback A error", got)
	}
}

func task4LargePNG() []byte {
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	if err := png.Encode(&out, img); err != nil {
		panic(err)
	}
	return out.Bytes()
}
