//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/task"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

type modelCallTailStrategy struct{}

func (modelCallTailStrategy) ManageContext(context.Context, *[]schema.Turn, int, func(events.EventKind, events.EventData)) error {
	return errors.New("tail strategy failure")
}

func (modelCallTailStrategy) AfterAction(context.Context, []schema.Turn, *llm.Client) error {
	return nil
}
func (modelCallTailStrategy) Tools() []tool.RegisteredTool { return nil }
func (modelCallTailStrategy) Name() string                 { return "tail" }

type modelCallTailAdapter struct {
	agenttest.ScriptedAdapter
	plan func(llm.Request) (llm.ResponsesContinuationPlan, error)
}

func (a *modelCallTailAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	return a.plan(req)
}

type modelCallTailEnv struct {
	*agenttest.DenyEnv
	wrapper *sandbox.Wrapper
}

func (e *modelCallTailEnv) KernelWrapper() *sandbox.Wrapper { return e.wrapper }

// FuzzModelCallTailCoverage exercises small, otherwise cold model-call decisions
// against a real Session whose only model boundary is a scripted adapter.
func FuzzModelCallTailCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Add(byte(1))
	f.Add(byte(2))
	f.Add(byte(3))

	f.Fuzz(func(t *testing.T, selector byte) {
		s := modelCallTailSession(t)

		// The nil context-manager path must remain a no-op.
		contextMgr := s.contextMgr
		s.contextMgr = nil
		s.recordResponseUsage(llm.Response{Usage: llm.Usage{InputTokens: int(selector) + 1}}, llm.Request{})
		s.contextMgr = contextMgr

		// Populate the per-session continuation circuit breaker from its nil map.
		meta := &llm.ContinuationMetadata{
			EndpointFamily:          string(llm.ResponsesEndpointFamilyOpenAIPublic),
			StorageScopeFingerprint: "scope",
			StoragePolicyLabel:      llm.ResponsesStoragePolicyPublicOpenAIStore,
		}
		req := llm.Request{Provider: "openai", Model: "tail-model", Continuation: meta}
		s.responsesContinuationDisabled = nil
		s.disableResponsesContinuationForRequest(req, selector&1 != 0)
		if len(s.responsesContinuationDisabled) != 1 {
			t.Fatalf("disabled continuation entries = %d, want 1", len(s.responsesContinuationDisabled))
		}

		// SystemPromptAsUser with no leading user message takes the prepend branch.
		s.cfg.SystemPromptAsUser = true
		built := s.buildModelRequest(s.currentProfile(), "tail system", []llm.Message{llm.Assistant("prior")}, nil, "")
		if len(built.Messages) != 2 || built.Messages[0].Role != llm.RoleUser || built.Messages[0].Text() != "tail system" {
			t.Fatalf("system-as-user request = %#v", built.Messages)
		}

		// Each guard in the continuation retry predicate is independently useful:
		// malformed requests and non-LLM errors must never trigger a replay.
		base := llm.Request{HistoryMode: llm.HistoryModeResponsesDelta, PreviousResponseID: "resp", FullHistoryFallbackMessages: []llm.Message{llm.User("full")}}
		cases := []struct {
			req llm.Request
			err error
		}{
			{llm.Request{}, errors.New("plain")},
			{llm.Request{HistoryMode: llm.HistoryModeResponsesDelta}, errors.New("plain")},
			{llm.Request{HistoryMode: llm.HistoryModeResponsesDelta, PreviousResponseID: "resp"}, errors.New("plain")},
			{base, errors.New("plain")},
		}
		for _, tc := range cases {
			if shouldRetryResponsesContinuationAsFullHistory(tc.req, tc.err) {
				t.Fatalf("unexpected continuation replay for %#v, %v", tc.req, tc.err)
			}
		}

		turns := []schema.Turn{
			schema.NewTurn(schema.TurnCheckpoint, llm.User("checkpoint")),
			schema.NewTurn(schema.TurnSummary, llm.User("summary")),
		}
		if got := expandHistory(turns, replayScope{}); len(got) != 2 || got[0].Text() != "checkpoint" || got[1].Text() != "summary" {
			t.Fatalf("expanded compaction history = %#v", got)
		}
	})
}

func FuzzModelCallExactCoverage(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, selector byte) {
		s := modelCallTailSession(t)
		s.cfg.testOnly.modelCallContextWindowFunc = func(*provider.Profile) int { return 0 }
		if s.maybeWarnContextUsage(s.currentProfile(), llm.Request{}) {
			t.Fatal("zero context window warned")
		}

		store := task.NewTaskStore(t.TempDir(), "tail").SetClock(s.sclock().Now)
		if _, err := store.Append([]task.TaskInput{{Type: task.TaskTypeImplement, Description: "tail", Prompt: "tail", ReasoningEffort: "high"}}); err != nil {
			t.Fatal(err)
		}
		if err := store.Update([]task.TaskUpdate{{ID: 1, Status: task.TaskInProgress}}); err != nil {
			t.Fatal(err)
		}
		s.taskStore = store
		s.strategy = modelCallTailStrategy{}
		var timings events.RoundTimings
		_, _, _, req, effort := s.prepareModelRequest(context.Background(), 1, &timings)
		if effort != "high" || req.ReasoningEffort == nil {
			t.Fatalf("task effort override = %q, request %#v", effort, req.ReasoningEffort)
		}

		modelCallTailPlanningCases(t)
		modelCallTailWebCases(t)
		modelCallTailTranscriptErrors(t, s)
		modelCallTailCatalogFallback(t, selector)
	})
}

func modelCallTailPlanningCases(t *testing.T) {
	t.Helper()
	support := llm.ResponsesContinuationSupport{EndpointFamily: llm.ResponsesEndpointFamilyOpenAIPublic, Enabled: true, StorageShapeProven: true, ProductionPathProven: true, MaxAnchorAgeSeconds: 3600}
	basePlan := llm.ResponsesContinuationPlan{EndpointFamily: llm.ResponsesEndpointFamilyOpenAIPublic, RequestFingerprint: "fp", StorageScopeFingerprint: "scope", StoragePolicyLabel: llm.ResponsesStoragePolicyPublicOpenAINoStore}
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("delta"))}
	req := llm.Request{Provider: "openai", Model: "tail-model", Messages: []llm.Message{llm.User("full")}}

	for _, mode := range []byte{0, 1, 2, 3, 4, 5} {
		calls := 0
		adapter := &modelCallTailAdapter{}
		adapter.Provider = "openai"
		adapter.Responder = func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") }
		adapter.plan = func(in llm.Request) (llm.ResponsesContinuationPlan, error) {
			calls++
			if mode == 0 {
				return llm.ResponsesContinuationPlan{}, errors.New("plan failure")
			}
			plan := basePlan
			if mode == 1 {
				plan.EndpointFamily = llm.ResponsesEndpointFamilyOpenAICodex
			}
			if mode == 2 && calls > 1 {
				plan.ContinuationStorageAllowed = true
				plan.StoragePolicyLabel = llm.ResponsesStoragePolicyPublicOpenAIStore
			}
			if mode == 3 || mode == 4 {
				plan.ContinuationStorageAllowed = true
				plan.StoragePolicyLabel = llm.ResponsesStoragePolicyPublicOpenAIStore
			}
			return plan, nil
		}
		client := llm.NewClient()
		client.Register(adapter)
		s := modelCallTailSessionWithClient(t, client, provider.NewOpenAIProfile("tail-model"))
		s.cfg.OpenAIResponsesContinuation = "auto"
		s.cfg.testOnly.responsesContinuationSupportRegistry = map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{llm.ResponsesEndpointFamilyOpenAIPublic: support}
		s.cfg.testOnly.responsesContinuationShadowEstimateFunc = func(llm.Request) (int, bool) { return 10, true }
		if mode == 3 {
			disabledPlan := basePlan
			disabledPlan.StoragePolicyLabel = llm.ResponsesStoragePolicyPublicOpenAIStore
			key := responsesContinuationDisabledKeyForPlan(req, disabledPlan, false)
			s.responsesContinuationDisabled[key] = true
		}
		if mode == 4 {
			s.cfg.testOnly.responsesContinuationHistoryCurrentFunc = func(responsesContinuationHistoryReservation, []schema.Turn) bool { return false }
		}
		out := s.applyResponsesContinuationAnchorPlanning(context.Background(), req, history, false)
		if out.HistoryMode == "" {
			t.Fatalf("planning mode %d returned empty history mode", mode)
		}
	}
	// Disabled registry return, including the empty-history-mode normalization.
	s := modelCallTailSession(t)
	s.cfg.OpenAIResponsesContinuation = "auto"
	s.cfg.testOnly.responsesContinuationSupportRegistry = map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{}
	if got := s.applyResponsesContinuationAnchorPlanning(context.Background(), req, nil, false); got.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("disabled registry mode = %q", got.HistoryMode)
	}
}

func modelCallTailWebCases(t *testing.T) {
	t.Helper()
	s := modelCallTailSession(t)
	noWeb := provider.NewOpenAIProfile("tail").WithLiveModelInfo(llm.ModelInfo{SupportsWebSearch: func() *bool { v := false; return &v }()})
	if s.providerWebSearchEnabled(noWeb) {
		t.Fatal("non-web profile enabled web search")
	}
	rp := sandbox.ResolvedPolicy{Backend: sandbox.BackendBwrap, Network: false}
	w, err := sandbox.NewWrapper(rp, "/bin/false", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := &modelCallTailEnv{DenyEnv: &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: 100}, wrapper: w}
	s.env = env
	_ = s.providerWebSearchEnabled(provider.NewOpenAIProfile("tail"))
}

func modelCallTailTranscriptErrors(t *testing.T, s *Session) {
	t.Helper()
	s.cfg.testOnly.appendModelAPICallFunc = func(transcript.APICall) error { return errors.New("append failure") }
	s.logAPICall(1, time.Unix(1, 0), time.Millisecond, "sys", 0, llm.Request{}, llm.Response{}, nil, ModelAttemptMetadata{})
	s.appendAdapterAttemptAPICall(1, time.Unix(1, 0), time.Millisecond, "sys", 0, llm.AdapterAttemptRecord{})
}

func modelCallTailCatalogFallback(t *testing.T, selector byte) {
	t.Helper()
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response { return agenttest.FinalResponse("ok") }}
	adapter.FaultResponder = func(req llm.Request) error {
		if req.Model != "gpt-5.4" {
			return llm.ErrorFromHTTPStatus("openai", 404, "missing", nil, nil)
		}
		return nil
	}
	client := llm.NewClient()
	client.Register(adapter)
	s := modelCallTailSessionWithClient(t, client, provider.NewOpenAIProfile("missing-primary"))
	policy := llm.RetryPolicy{MaxRetries: 0}
	s.cfg.LLMRetryPolicy = &policy
	s.cfg.ModelFallbacks = []string{"gpt-5.4"}
	req := llm.Request{Provider: "openai", Model: "missing-primary", Messages: []llm.Message{llm.User("tail")}}
	_, _, _, _ = s.callModelWithFallback(context.Background(), s.currentProfile(), req, []string{"high", "xhigh"}[int(selector)&1], 1)
}

func modelCallTailSession(t *testing.T) *Session {
	t.Helper()
	adapter := &agenttest.ScriptedAdapter{
		Provider:  "openai",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	client := llm.NewClient()
	client.Register(adapter)
	return modelCallTailSessionWithClient(t, client, provider.NewOpenAIProfile("tail-model"))
}

func modelCallTailSessionWithClient(t *testing.T, client *llm.Client, profile *provider.Profile) *Session {
	t.Helper()
	clock := agenttest.NewFakeClock()
	cfg := SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true, clock: clock}
	cfg.testOnly.skipGitSnapshot = true
	cfg.testOnly.minimalSystemPrompt = true
	cfg.testOnly.noSyncJobStore = true
	s, err := NewSession(client, profile, &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: 100}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}
