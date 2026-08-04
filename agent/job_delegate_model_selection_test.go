package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providercfg"
)

const durablePluginAgentType = "test-plugin:reviewer"

type countingRestoreModelAdapter struct {
	fakeAdapter
	models          []llm.ModelInfo
	listModelsCalls int
}

func (a *countingRestoreModelAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listModelsCalls++
	return append([]llm.ModelInfo(nil), a.models...), nil
}

func (a *countingRestoreModelAdapter) ListModelsCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listModelsCalls
}

func newDurablePluginModelSession(
	t *testing.T,
	profile *provider.Profile,
	adapter *fakeEnumerableAdapter,
	pluginModel string,
) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	s := newSession(t,
		withClient(client),
		withProfile(profile),
		withConfig(SessionConfig{
			StateDir:         packageFixtureTempDir(t, "delegate-model-state-*"),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}),
	)
	s.pluginAgents[durablePluginAgentType] = plugin.Agent{
		Name:         "reviewer",
		Model:        pluginModel,
		SystemPrompt: "Review the code.",
		PluginName:   "test-plugin",
	}
	return s
}

func requireDurableModelDescriptor(t *testing.T, s *Session, result delegateResult) *jobstore.DelegateRestoreDescriptor {
	t.Helper()
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	rec := loadShellRecord(t, s.jobManager, result.StartedJobID)
	if rec.DelegateRestore == nil {
		t.Fatal("delegate job has no restore descriptor")
	}
	return rec.DelegateRestore
}

func TestCreateDelegate_UnavailablePluginModelPersistsExplicitFallback(t *testing.T) {
	t.Parallel()
	adapter := &fakeEnumerableAdapter{
		fakeAdapter: fakeAdapter{
			name: "kimi-anthropic-api",
			steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
			},
		},
		models: []llm.ModelInfo{{ID: "k3"}},
	}
	s := newDurablePluginModelSession(
		t,
		WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api"),
		adapter,
		"sonnet",
	)

	result := s.createDelegate(context.Background(), delegateArgs{
		Task:           "review with an explicit fallback",
		AgentType:      durablePluginAgentType,
		Model:          "k3",
		BlockTimeoutMS: 5000,
	})
	desc := requireDurableModelDescriptor(t, s, result)

	if desc.RequestedModel != "k3" {
		t.Errorf("RequestedModel = %q, want winning explicit fallback k3", desc.RequestedModel)
	}
	if desc.ResolvedProfileID != "kimi-anthropic-api" || desc.ResolvedModel != "k3" {
		t.Errorf("resolved descriptor profile = %s/%s, want kimi-anthropic-api/k3", desc.ResolvedProfileID, desc.ResolvedModel)
	}
	if result.Model != "kimi-anthropic-api/k3" {
		t.Errorf("result.Model = %q, want kimi-anthropic-api/k3", result.Model)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("child requests = %+v, want one", requests)
	}
	if requests[0].Provider != "kimi-anthropic-api" || requests[0].Model != "k3" {
		t.Errorf("child request provider/model = %s/%s, want kimi-anthropic-api/k3", requests[0].Provider, requests[0].Model)
	}
	warnings := warningMessages(s)
	if len(warnings) != 1 || !containsAll(warnings[0], "sonnet", "kimi-anthropic-api", "k3") {
		t.Errorf("warnings = %q, want one rejected-plugin diagnostic naming sonnet and kimi-anthropic-api/k3", warnings)
	}
}

func TestCreateDelegate_UnavailablePluginModelPersistsParentFallback(t *testing.T) {
	t.Parallel()
	adapter := &fakeEnumerableAdapter{
		fakeAdapter: fakeAdapter{
			name: "kimi-anthropic-api",
			steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
			},
		},
		models: []llm.ModelInfo{{ID: "k3"}},
	}
	s := newDurablePluginModelSession(
		t,
		WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api"),
		adapter,
		"sonnet",
	)

	result := s.createDelegate(context.Background(), delegateArgs{
		Task:           "review with the inherited fallback",
		AgentType:      durablePluginAgentType,
		BlockTimeoutMS: 5000,
	})
	desc := requireDurableModelDescriptor(t, s, result)

	if desc.RequestedModel != "" {
		t.Errorf("RequestedModel = %q, want empty for inherited parent fallback", desc.RequestedModel)
	}
	if desc.ResolvedProfileID != "kimi-anthropic-api" || desc.ResolvedModel != "k3" {
		t.Errorf("resolved descriptor profile = %s/%s, want kimi-anthropic-api/k3", desc.ResolvedProfileID, desc.ResolvedModel)
	}
	if result.Model != "kimi-anthropic-api/k3" {
		t.Errorf("result.Model = %q, want kimi-anthropic-api/k3", result.Model)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("child requests = %+v, want one", requests)
	}
	if requests[0].Provider != "kimi-anthropic-api" || requests[0].Model != "k3" {
		t.Errorf("child request provider/model = %s/%s, want kimi-anthropic-api/k3", requests[0].Provider, requests[0].Model)
	}
	warnings := warningMessages(s)
	if len(warnings) != 1 || !containsAll(warnings[0], "sonnet", "kimi-anthropic-api", "k3") {
		t.Errorf("warnings = %q, want one rejected-plugin diagnostic naming sonnet and inherited kimi-anthropic-api/k3", warnings)
	}
}

func TestCreateDelegate_AvailablePluginAliasPersistsAliasAndConcreteModel(t *testing.T) {
	t.Parallel()
	adapter := &fakeEnumerableAdapter{
		fakeAdapter: fakeAdapter{
			name: "anthropic",
			steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
			},
		},
		models: []llm.ModelInfo{
			{ID: "claude-opus-4-6"},
			{ID: "claude-sonnet-4-6"},
		},
	}
	s := newDurablePluginModelSession(t, newAnthropicProfile("claude-opus-4-6"), adapter, "sonnet")

	result := s.createDelegate(context.Background(), delegateArgs{
		Task:           "review with the plugin alias",
		AgentType:      durablePluginAgentType,
		Model:          "claude-opus-4-6",
		BlockTimeoutMS: 5000,
	})
	desc := requireDurableModelDescriptor(t, s, result)

	if desc.RequestedModel != "sonnet" {
		t.Errorf("RequestedModel = %q, want winning plugin alias sonnet", desc.RequestedModel)
	}
	if desc.ResolvedProfileID != "anthropic" || desc.ResolvedModel != "claude-sonnet-4-6" {
		t.Errorf("resolved descriptor profile = %s/%s, want anthropic/claude-sonnet-4-6", desc.ResolvedProfileID, desc.ResolvedModel)
	}
	if result.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("result.Model = %q, want anthropic/claude-sonnet-4-6", result.Model)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("child requests = %+v, want one", requests)
	}
	if requests[0].Provider != "anthropic" || requests[0].Model != "claude-sonnet-4-6" {
		t.Errorf("child request provider/model = %s/%s, want anthropic/claude-sonnet-4-6", requests[0].Provider, requests[0].Model)
	}
}

func TestCreateDelegate_PluginFallbackFailureHasNoSideEffects(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	r := newScriptedWtDlgRepo(t, client)
	r.s.pluginAgents[durablePluginAgentType] = plugin.Agent{
		Name:         "reviewer",
		Model:        "sonnet",
		SystemPrompt: "Review the code.",
		PluginName:   "test-plugin",
	}
	resolverErr := errors.New("explicit model resolver failed")
	r.s.resolveProfile = func(ref string) (*provider.Profile, error) {
		if ref != "anthropic/boom" {
			return nil, fmt.Errorf("unexpected profile ref %q", ref)
		}
		return nil, resolverErr
	}
	beforeGitCalls := len(r.git.calls)

	result := r.s.createDelegate(context.Background(), delegateArgs{
		Task:        "must fail before durable state",
		AgentType:   durablePluginAgentType,
		Model:       "anthropic/boom",
		Isolation:   "worktree",
		WatchParent: true,
	})

	if !errors.Is(result.Err, resolverErr) {
		t.Fatalf("createDelegate error = %v, want wrapped resolver error %v", result.Err, resolverErr)
	}
	if result.DelegateID != "" || result.StartedJobID != "" || result.TranscriptRef != "" {
		t.Errorf("failed result minted durable identifiers: %+v", result)
	}
	if got := len(r.git.calls); got != beforeGitCalls {
		t.Errorf("scripted git calls = %d, want unchanged %d", got, beforeGitCalls)
	}
	delegates, err := r.s.jobManager.store.LoadDelegates()
	if err != nil {
		t.Fatalf("LoadDelegates: %v", err)
	}
	if len(delegates) != 0 {
		t.Errorf("delegates = %+v, want none", delegates)
	}
	if jobs := r.s.jobManager.list(listFilter{Type: jobstore.JobDelegate}); len(jobs) != 0 {
		t.Errorf("delegate jobs = %+v, want none", jobs)
	}
	if children := r.s.subagents.directSubagents(); len(children) != 0 {
		t.Errorf("retained children = %+v, want none", children)
	}
	r.s.jobManager.mu.Lock()
	watchCount := len(r.s.jobManager.watches)
	r.s.jobManager.mu.Unlock()
	if watchCount != 0 {
		t.Errorf("parent watches = %d, want none", watchCount)
	}
	if requests := adapter.Requests(); len(requests) != 0 {
		t.Errorf("provider requests = %+v, want none", requests)
	}
}

func TestSendDelegateMessage_RestoreIgnoresChangedPluginModel(t *testing.T) {
	t.Parallel()
	openAIAdapter := &countingRestoreModelAdapter{
		fakeAdapter: fakeAdapter{name: "openai"},
		models:      []llm.ModelInfo{{ID: "gpt-5.2"}},
	}
	workAdapter := &countingRestoreModelAdapter{
		fakeAdapter: fakeAdapter{
			name: "work",
			steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("restored with frozen profile") },
			},
		},
		models: []llm.ModelInfo{{
			ID:                    "GPT-5.3",
			ContextWindow:         808_006,
			SupportsReasoning:     true,
			ReasoningEffortLevels: []string{"low", "high"},
		}},
	}
	client := llm.NewClient()
	client.Register(openAIAdapter)
	client.Register(workAdapter)
	s := newDelegateRestorePreflightSession(t, client)
	rec := seedStoppedDelegateRestoreRecord(t, s)
	markStoredDelegateResumable(t, s, rec)
	rec = loadShellRecord(t, s.jobManager, rec.JobID)
	rec.DelegateRestore.AgentType = durablePluginAgentType
	rec.DelegateRestore.RequestedModel = "sonnet"
	rec.DelegateRestore.ResolvedProfileID = "work"
	rec.DelegateRestore.ResolvedModel = "GPT-5.3"
	replaceStoredDelegateRecord(t, s, rec)
	s.pluginAgents[durablePluginAgentType] = plugin.Agent{
		Name:         "reviewer",
		Model:        "changed-unavailable-model",
		SystemPrompt: "Changed after the original delegate stopped.",
		PluginName:   "test-plugin",
	}
	reasoningOff := false
	configuredModels := map[string]providercfg.ModelConfig{
		"gpt-5.3": {
			ContextWindow: 654_321,
			Reasoning:     &reasoningOff,
		},
	}
	s.resolveProfile = func(ref string) (*provider.Profile, error) {
		if ref != "work/GPT-5.3" {
			return nil, fmt.Errorf("unexpected profile ref %q", ref)
		}
		return provider.ResolveProfileFromConfig(providercfg.Config{
			Instances: []providercfg.InstanceConfig{{
				Name:     "work",
				Type:     "openai",
				APIStyle: providercfg.StyleChatCompletions,
				Models:   configuredModels,
			}},
		}, ref)
	}
	openAIListCallsBeforeRestore := openAIAdapter.ListModelsCallCount()
	workListCallsBeforeRestore := workAdapter.ListModelsCallCount()

	result := s.sendDelegateMessage(context.Background(), sendMessageArgs{
		Target:         rec.DelegateID,
		Message:        "resume using the frozen descriptor",
		Background:     false,
		BackgroundSet:  true,
		BlockTimeoutMS: 5000,
	})

	if result.Err != nil {
		t.Fatalf("sendDelegateMessage: %v", result.Err)
	}
	child := s.subagents.get(rec.DelegateRestore.ChildSessionID)
	if child == nil || child.sess == nil {
		t.Fatal("restored child runtime is missing")
	}
	profile := child.sess.currentProfile()
	if profile.ID() != "work" || profile.Model() != "GPT-5.3" {
		t.Errorf("restored child profile = %s/%s, want work/GPT-5.3", profile.ID(), profile.Model())
	}
	if profile.ContextWindowSize() != 654_321 {
		t.Errorf("restored context window = %d, want configured 654321", profile.ContextWindowSize())
	}
	if profile.SupportsReasoning() {
		t.Error("restored SupportsReasoning = true, want configured reasoning=false")
	}
	if !profile.EffortLevelsConfigured() {
		t.Error("restored EffortLevelsConfigured = false, want configured reasoning flag retained")
	}
	requests := workAdapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("work requests = %+v, want one restored request", requests)
	}
	if requests[0].Provider != "work" || requests[0].Model != "GPT-5.3" {
		t.Errorf("restored request provider/model = %s/%s, want work/GPT-5.3", requests[0].Provider, requests[0].Model)
	}
	if requests := openAIAdapter.Requests(); len(requests) != 0 {
		t.Errorf("openai requests = %+v, want none", requests)
	}
	if got := openAIAdapter.ListModelsCallCount() - openAIListCallsBeforeRestore; got != 0 {
		t.Errorf("openai ListModels calls during restore = %d, want zero plugin-selection calls", got)
	}
	if got := workAdapter.ListModelsCallCount() - workListCallsBeforeRestore; got != 1 {
		t.Errorf("work ListModels calls during restore = %d, want one ordinary live-metadata refresh", got)
	}
	var fallbackWarnings []events.WarningData
drainWarnings:
	for {
		select {
		case event := <-s.Events():
			warning, ok := event.Data.(events.WarningData)
			if ok && (warning.Source == "plugin" || warning.Title == "plugin agent model unavailable") {
				fallbackWarnings = append(fallbackWarnings, warning)
			}
		default:
			break drainWarnings
		}
	}
	if len(fallbackWarnings) != 0 {
		t.Errorf("restore re-evaluated changed plugin model: warnings %+v", fallbackWarnings)
	}
}

func TestPluginAgentModelSelection_DirectAndDurableParity(t *testing.T) {
	t.Parallel()
	adapter := &fakeEnumerableAdapter{
		fakeAdapter: fakeAdapter{
			name: "kimi-anthropic-api",
			steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("direct done") },
				func(llm.Request) llm.Response { return communicateWithDefaultOutput("durable done") },
			},
		},
		models: []llm.ModelInfo{{ID: "k3"}},
	}
	s := newDurablePluginModelSession(
		t,
		WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api"),
		adapter,
		"sonnet",
	)

	spawned, err := s.spawnAgent(context.Background(), "direct review", "k3", "", 10, durablePluginAgentType, "", nil, nil)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	directID, err := parseSpawnedAgentID(spawned)
	if err != nil {
		t.Fatalf("parse direct child ID: %v", err)
	}
	waitForRuntimeSubagent(t, s, directID)

	durable := s.createDelegate(context.Background(), delegateArgs{
		Task:           "durable review",
		AgentType:      durablePluginAgentType,
		Model:          "k3",
		BlockTimeoutMS: 5000,
	})
	desc := requireDurableModelDescriptor(t, s, durable)

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("direct and durable requests = %+v, want two", requests)
	}
	for i, request := range requests {
		if request.Provider != "kimi-anthropic-api" || request.Model != "k3" {
			t.Errorf("request %d provider/model = %s/%s, want kimi-anthropic-api/k3", i, request.Provider, request.Model)
		}
	}
	if desc.ResolvedProfileID != requests[0].Provider || desc.ResolvedModel != requests[0].Model {
		t.Errorf("durable descriptor profile = %s/%s, want direct request profile %s/%s", desc.ResolvedProfileID, desc.ResolvedModel, requests[0].Provider, requests[0].Model)
	}
}
