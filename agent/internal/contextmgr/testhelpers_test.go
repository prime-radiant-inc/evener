package contextmgr

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// NewOpenAIProfile re-exports the provider constructor so tests need not qualify
// every call, mirroring the agent package's profile test shims.
var NewOpenAIProfile = provider.NewOpenAIProfile

// WithCheapModel re-exports the provider override for the same reason.
var WithCheapModel = provider.WithCheapModel

// testProfile builds a profile for the given curated instance and model on the
// embedded registry, with the context window overridden when contextWindow > 0.
// The common fixture for context-manager and strategy tests in this package.
func testProfile(instance, model string, contextWindow int) *provider.Profile {
	p, err := provider.Resolve(provider.EmbeddedRegistry(), instance+"/"+model)
	if err != nil {
		// A reference the registry will not parse — the summarization-route
		// fuzz feeds the empty model id — still has to yield a profile: what
		// is under test is what the manager does with the id, not how it
		// resolved.
		p = provider.FromResolved(registry.Resolved{Instance: instance, ModelID: model, WireID: model}, nil)
	}
	if contextWindow > 0 {
		p = provider.WithContextWindow(p, contextWindow)
	}
	return p
}

// testOpenAIProfileWithContextWindow builds an OpenAI-tagged profile with the
// given context window.
func testOpenAIProfileWithContextWindow(contextWindow int) *provider.Profile {
	return testProfile("openai", "test", contextWindow)
}

// newCompactionManager builds a Manager over an OpenAI test profile with the
// given context window and the four compaction thresholds, named here in
// call order: observation mask, thinking clear, checkpoint, summarize. A
// test names exactly which layers can fire; PreserveRecentTurns is 2.
func newCompactionManager(client *llm.Client, contextWindow int, observationMask, thinkingClear, checkpoint, summarize float64) *Manager {
	cm := NewManager(testOpenAIProfileWithContextWindow(contextWindow), client, cheapmodel.New(client))
	cm.ObservationMaskThreshold = observationMask
	cm.ThinkingClearThreshold = thinkingClear
	cm.CheckpointThreshold = checkpoint
	cm.SummarizeThreshold = summarize
	cm.PreserveRecentTurns = 2
	return cm
}

// newTestSessionLogStrategy pairs cm with a session log in a fresh temp dir
// and returns both, so a test can append entries before driving the strategy.
func newTestSessionLogStrategy(t *testing.T, cm *Manager) (*SessionLogStrategy, *sessionlog.SessionLog) {
	t.Helper()
	log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "test.log.jsonl"))
	return &SessionLogStrategy{cm: cm, log: log}, log
}

// pingPongHistory builds an alternating user/assistant history from messages:
// even indexes are user turns, odd indexes assistant turns.
func pingPongHistory(messages ...string) []schema.Turn {
	turns := make([]schema.Turn, len(messages))
	for i, text := range messages {
		kind := schema.TurnUserInput
		msg := llm.User(text)
		if i%2 == 1 {
			kind = schema.TurnAssistant
			msg = llm.Assistant(text)
		}
		turns[i] = schema.Turn{Kind: kind, Message: msg}
	}
	return turns
}

// stubSummarizeAdapter is a minimal llm provider adapter stub for summarization
// and fork-summarize tests.
type stubSummarizeAdapter struct {
	name    string
	respFn  func(req llm.Request) (llm.Response, error)
	lastReq llm.Request
}

func (a *stubSummarizeAdapter) Name() string { return a.name }
func (a *stubSummarizeAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.lastReq = req
	return a.respFn(req)
}
func (a *stubSummarizeAdapter) Stream(_ context.Context, _ llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// mustNewSessionLog is a test helper that creates a sessionlog.SessionLog or
// fails the test.
func mustNewSessionLog(t *testing.T, path string) *sessionlog.SessionLog {
	t.Helper()
	log, err := sessionlog.NewSessionLog(path)
	if err != nil {
		t.Fatalf("NewSessionLog(%q): %v", path, err)
	}
	return log
}

// fakeAdapter is a scripted llm provider stub: each Complete call returns the
// next response from steps and records the request for later assertions. Used by
// the strategy tests that drive multi-call summarization/distillation flows.
type fakeAdapter struct {
	name string

	mu       sync.Mutex
	requests []llm.Request
	steps    []func(req llm.Request) llm.Response
	i        int
}

func (a *fakeAdapter) Name() string { return a.name }

func (a *fakeAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, req)
	if a.i >= len(a.steps) {
		return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("done")}, nil
	}
	resp := a.steps[a.i](req)
	a.i++
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *fakeAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func (a *fakeAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request{}, a.requests...)
}
