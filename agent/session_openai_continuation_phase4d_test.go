package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSession_OpenAIResponsesContinuationPhase4DIProducesStoredFullHistoryAnchor(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				resp := agenttest.FinalResponse("phase 4d anchor stored")
				resp.ID = "resp_phase4d_anchor"
				resp.Raw = map[string]any{
					"endpoint_url": "https://api.openai.com/v1/responses",
					"id_hash":      "cont-handle-v1:response_id:phase4d",
				}
				return resp
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := sess.ProcessInput(ctx, "store a phase 4d anchor", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(out, "phase 4d anchor stored") {
		t.Fatalf("ProcessInput output = %q, want phase 4d response", out)
	}

	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("HistoryMode = %q, want %q", req.HistoryMode, llm.HistoryModeFullHistory)
	}
	if req.PreviousResponseID != "" {
		t.Fatalf("PreviousResponseID = %q, want empty", req.PreviousResponseID)
	}
	if req.Store == nil || !*req.Store {
		t.Fatalf("Store = %v, want continuation-owned true", req.Store)
	}
	if req.Continuation == nil {
		t.Fatal("Continuation metadata is nil")
	}
	if req.Continuation.EndpointFamily != string(llm.ResponsesEndpointFamilyOpenAIPublic) ||
		req.Continuation.RequestFingerprint != "cont-req-v1:phase4d" ||
		req.Continuation.StorageScopeFingerprint != "cont-scope-v1:phase4d" ||
		req.Continuation.ContextMarker != responseContextMarkerV1 ||
		req.Continuation.StoragePolicyLabel != llm.ResponsesStoragePolicyPublicOpenAIStore ||
		req.Continuation.ChatFallbackHistoryLen != 0 {
		t.Fatalf("Continuation metadata = %+v", req.Continuation)
	}

	assistant := latestAssistantTurn(t, sess)
	if assistant.ResponseID != "resp_phase4d_anchor" ||
		assistant.ResponseIDHash != "cont-handle-v1:response_id:phase4d" ||
		assistant.ResponseEndpoint != "https://api.openai.com/v1/responses" ||
		assistant.ResponseRequestFingerprint != "cont-req-v1:phase4d" ||
		assistant.ResponseStorageScopeFingerprint != "cont-scope-v1:phase4d" ||
		assistant.ResponseContextMarker != responseContextMarkerV1 ||
		assistant.ResponseRequestModel != "gpt-5.4" {
		t.Fatalf("assistant continuation fields = %+v", assistant)
	}
}

func TestSession_OpenAIResponsesContinuationPhase4DIFallbackCapablePathUsesFullHistory(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return agenttest.FinalResponse("fallback capable path stayed full history")
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "do not create a continuation anchor", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("HistoryMode = %q, want %q", req.HistoryMode, llm.HistoryModeFullHistory)
	}
	if req.Store != nil && *req.Store {
		t.Fatalf("Store = true, want no continuation-owned storage")
	}
	if req.Continuation != nil {
		t.Fatalf("Continuation = %+v, want nil", req.Continuation)
	}
	if req.PreviousResponseID != "" {
		t.Fatalf("PreviousResponseID = %q, want empty", req.PreviousResponseID)
	}
}

func phase4DIContinuationPlan(req llm.Request) llm.ResponsesContinuationPlan {
	return llm.ResponsesContinuationPlan{
		EndpointFamily:             llm.ResponsesEndpointFamilyOpenAIPublic,
		RequestFingerprint:         "cont-req-v1:phase4d",
		StorageScopeFingerprint:    "cont-scope-v1:phase4d",
		StoragePolicyLabel:         llm.ResponsesStoragePolicyPublicOpenAIStore,
		ContinuationStorageAllowed: true,
	}
}

func phase4DIEnabledSupport() llm.ResponsesContinuationSupport {
	return llm.ResponsesContinuationSupport{
		EndpointFamily:       llm.ResponsesEndpointFamilyOpenAIPublic,
		StorageShapeProven:   true,
		ProductionPathProven: true,
		Enabled:              true,
		MaxAnchorAgeSeconds:  3600,
	}
}

func latestAssistantTurn(t *testing.T, sess *Session) schema.Turn {
	t.Helper()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for i := len(sess.history) - 1; i >= 0; i-- {
		if sess.history[i].Kind == schema.TurnAssistant {
			return sess.history[i]
		}
	}
	t.Fatal("no assistant turn recorded")
	return schema.Turn{}
}

func drainSessionEvents(sess *Session) {
	go func() {
		for range sess.Events() {
		}
	}()
}
