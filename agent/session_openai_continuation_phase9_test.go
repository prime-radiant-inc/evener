package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSession_OpenAIResponsesContinuationPhase9FallbackCapableFakePathCarriesFullHistorySidecar(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return agenttest.FinalResponse("phase 9 delta consumed")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	anchor := responsesContinuationEligibleAssistantTurn("resp_phase9_anchor")
	anchor.ResponseIDHash = "cont-handle-v1:response_id:phase9"
	anchor.ResponseRequestFingerprint = "cont-req-v1:phase4d"
	anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:phase4d"
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		anchor,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase9 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.HistoryMode != llm.HistoryModeResponsesDelta {
		t.Fatalf("HistoryMode = %q, want %q", req.HistoryMode, llm.HistoryModeResponsesDelta)
	}
	if req.PreviousResponseID != "resp_phase9_anchor" {
		t.Fatalf("PreviousResponseID = %q, want resp_phase9_anchor", req.PreviousResponseID)
	}
	if !requestMessagesContainText(req.Messages, "phase9 current user marker") {
		t.Fatalf("delta request missing current marker: %+v", req.Messages)
	}
	if requestMessagesContainText(req.Messages, "phase9 prior user marker") {
		t.Fatalf("delta request included prior marker: %+v", req.Messages)
	}
	if !requestMessagesContainText(req.FullHistoryFallbackMessages, "phase9 prior user marker") ||
		!requestMessagesContainText(req.FullHistoryFallbackMessages, "phase9 current user marker") {
		t.Fatalf("FullHistoryFallbackMessages = %+v, want prior and current markers", req.FullHistoryFallbackMessages)
	}
}

func TestSession_OpenAIResponsesContinuationPhase9RetryThroughRealAnchorSelection(t *testing.T) {
	dir := t.TempDir()
	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "Previous response not found",
			"type":    "invalid_request_error",
		},
	}, nil)
	adapter := &phase9RetryAdapter{
		provider:          "openai",
		canFallbackToChat: true,
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, continuationErr
			},
			func(req llm.Request) (llm.Response, error) {
				return agenttest.FinalResponse("phase 9 retry recovered"), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		phase9MatchingAnchor("resp_phase9_retry"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase9 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta ||
		requests[0].PreviousResponseID != "resp_phase9_retry" {
		t.Fatalf("delta request = %+v", requests[0])
	}
	if requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback ||
		requests[1].PreviousResponseID != "" ||
		requests[1].Continuation != nil ||
		requestMessagesContainText(requests[1].Messages, "PHASE9_DELTA_ONLY_MARKER") ||
		!requestMessagesContainText(requests[1].Messages, "phase9 prior user marker") ||
		!requestMessagesContainText(requests[1].Messages, "phase9 current user marker") {
		t.Fatalf("full-history retry request = %+v", requests[1])
	}
}

func TestSession_OpenAIResponsesContinuationPhase9OrphanedToolResultGateUsesFullHistory(t *testing.T) {
	anchor := phase9MatchingAnchor("resp_phase9_orphan")
	anchor.Message = llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:   "call_anchor",
			Name: "shell",
			Type: "function",
		},
	}}}
	req := runPhase9GateSession(t, []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		anchor,
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_other", "shell", "ok", false)),
	})
	assertPhase9GateUsedFullHistory(t, req)
}

type phase9RetryAdapter struct {
	provider          string
	canFallbackToChat bool
	steps             []func(req llm.Request) (llm.Response, error)

	mu       sync.Mutex
	requests []llm.Request
	i        int
}

func (a *phase9RetryAdapter) Name() string { return a.provider }

func (a *phase9RetryAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	a.mu.Lock()
	a.requests = append(a.requests, req)
	i := a.i
	a.i++
	a.mu.Unlock()
	if i >= len(a.steps) {
		return agenttest.FinalResponse("done"), nil
	}
	resp, err := a.steps[i](req)
	if err != nil {
		return resp, err
	}
	resp.Provider = a.provider
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *phase9RetryAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func (a *phase9RetryAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	plan := phase4DIContinuationPlan(req)
	plan.CanFallbackToChat = a.canFallbackToChat
	return plan, nil
}

func (a *phase9RetryAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

func TestSession_OpenAIResponsesContinuationPhase9MediaGateUsesFullHistory(t *testing.T) {
	req := runPhase9GateSession(t, []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		phase9MatchingAnchor("resp_phase9_media"),
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "see image"},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/image.png"}},
		}}),
	})
	assertPhase9GateUsedFullHistory(t, req)
}

func TestSession_OpenAIResponsesContinuationPhase9InterveningAssistantGateUsesFullHistory(t *testing.T) {
	req := runPhase9GateSession(t, []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		phase9MatchingAnchor("resp_phase9_intervening"),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("non anchor assistant")),
	})
	assertPhase9GateUsedFullHistory(t, req)
}

func runPhase9GateSession(t *testing.T, history []schema.Turn) llm.Request {
	t.Helper()
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return agenttest.FinalResponse("phase 9 full history gate")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.history = append(sess.history, history...)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase9 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	return requests[0]
}

func phase9MatchingAnchor(responseID string) schema.Turn {
	anchor := responsesContinuationEligibleAssistantTurn(responseID)
	anchor.ResponseIDHash = "cont-handle-v1:response_id:phase9"
	anchor.ResponseRequestFingerprint = "cont-req-v1:phase4d"
	anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:phase4d"
	return anchor
}

func assertPhase9GateUsedFullHistory(t *testing.T, req llm.Request) {
	t.Helper()
	if req.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("HistoryMode = %q, want %q", req.HistoryMode, llm.HistoryModeFullHistory)
	}
	if req.PreviousResponseID != "" {
		t.Fatalf("PreviousResponseID = %q, want empty", req.PreviousResponseID)
	}
	if req.Continuation != nil && req.Continuation.PreviousResponseIDHash != "" {
		t.Fatalf("Continuation = %+v, want no previous response hash", req.Continuation)
	}
}
