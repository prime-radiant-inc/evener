package agent

import (
	"context"
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
