package contextmgr

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/internal/sessionlog"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

// communicateCall builds a communicate tool call carrying the given message and
// a terminal reply, matching the wire shape the compaction code parses. It
// mirrors the agent package's test helper; the checkpoint conversation
// extraction reads only the message and end_turn fields.
func communicateCall(id, message string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: raw,
		Type:      "function",
	}
}

// NewOpenAIProfile re-exports the provider constructor so tests need not qualify
// every call, mirroring the agent package's profile test shims.
var NewOpenAIProfile = provider.NewOpenAIProfile

// WithCheapModel re-exports the provider override for the same reason.
var WithCheapModel = provider.WithCheapModel

// testProfile builds a profile for the given provider type and model, with the
// context window overridden when contextWindow > 0. The common fixture for
// context-manager and strategy tests in this package.
func testProfile(providerType, model string, contextWindow int) *provider.Profile {
	var p *provider.Profile
	if providerType == "openai" {
		p = provider.NewOpenAIProfile(model)
	} else {
		cfg := providercfg.Config{Instances: []providercfg.InstanceConfig{{Name: providerType, Type: providercfg.Type(providerType)}}}
		var err error
		p, err = provider.ResolveProfileFromConfig(cfg, providerType+"/"+model)
		if err != nil {
			panic("testProfile: " + err.Error())
		}
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
