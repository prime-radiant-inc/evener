package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
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

func TestSession_OpenAIResponsesContinuationPhase4DIIConsumesStoredAnchorAsDelta(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return agenttest.FinalResponse("phase 4d delta consumed")
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
	anchor := responsesContinuationEligibleAssistantTurn("resp_phase4d_anchor")
	anchor.ResponseIDHash = "cont-handle-v1:response_id:phase4d"
	anchor.ResponseRequestFingerprint = "cont-req-v1:phase4d"
	anchor.ResponseStorageScopeFingerprint = "cont-scope-v1:phase4d"
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("prior user marker")),
		anchor,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "new delta user marker", nil); err != nil {
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
	if req.PreviousResponseID != "resp_phase4d_anchor" {
		t.Fatalf("PreviousResponseID = %q, want resp_phase4d_anchor", req.PreviousResponseID)
	}
	if req.Store == nil || !*req.Store {
		t.Fatalf("Store = %v, want continuation-owned true", req.Store)
	}
	if req.Continuation == nil {
		t.Fatal("Continuation metadata is nil")
	}
	if req.Continuation.PreviousResponseIDHash != "cont-handle-v1:response_id:phase4d" ||
		req.Continuation.AnchorTurnIndex != 1 ||
		req.Continuation.DeltaTurnCount != 1 ||
		len(req.Continuation.DeltaTurnKinds) != 1 ||
		req.Continuation.DeltaTurnKinds[0] != string(schema.TurnUserInput) ||
		req.Continuation.EndpointFamily != string(llm.ResponsesEndpointFamilyOpenAIPublic) ||
		req.Continuation.RequestFingerprint != "cont-req-v1:phase4d" ||
		req.Continuation.StorageScopeFingerprint != "cont-scope-v1:phase4d" ||
		req.Continuation.ContextMarker != responseContextMarkerV1 ||
		req.Continuation.StoragePolicyLabel != llm.ResponsesStoragePolicyPublicOpenAIStore {
		t.Fatalf("Continuation metadata = %+v", req.Continuation)
	}
	if !requestMessagesContainText(req.Messages, "new delta user marker") {
		t.Fatalf("delta request missing new user marker: %+v", req.Messages)
	}
	for _, forbidden := range []string{"prior user marker", "phase 4d anchor stored"} {
		if requestMessagesContainText(req.Messages, forbidden) {
			t.Fatalf("delta request included pre-anchor text %q: %+v", forbidden, req.Messages)
		}
	}
}

func TestSession_OpenAIResponsesContinuationPhase4DIIRealOpenAIAdapterUsesFullHistoryUntilFallbackClone(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	var requestBodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		args := mustJSON(t, map[string]any{
			"message":  "real openai stayed full history",
			"end_turn": true,
			"output": map[string]any{
				"message":   "",
				"data":      map[string]any{},
				"artifacts": []string{},
			},
		})
		writeResponsesFunctionCall(t, w, flusher, "resp_new", "call_done", "communicate", args)
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient()
	client.Register(&openai.Adapter{
		APIKey:             "test-key",
		BaseURL:            srv.URL,
		Client:             srv.Client(),
		ContinuationHasher: llm.NewContinuationHasher([]byte("01234567890123456789012345678901")),
	})

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
		schema.NewTurn(schema.TurnUserInput, llm.User("real openai prior user marker")),
		responsesContinuationEligibleAssistantTurn("resp_existing_anchor"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "real openai current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("OpenAI Responses request count = %d, want 1", len(bodies))
	}
	req := decodeResponsesRequest(t, bodies[0])
	if _, ok := req["previous_response_id"]; ok {
		t.Fatalf("fallback-capable real OpenAI path must not send previous_response_id: %s", string(bodies[0]))
	}
	if gotStore, ok := req["store"].(bool); !ok || gotStore {
		t.Fatalf("fallback-capable real OpenAI request store = %#v, want explicit false", req["store"])
	}
	input := responsesInputItems(t, req)
	for _, marker := range []string{"real openai prior user marker", "real openai current user marker"} {
		if !responsesInputContainsText(input, marker) {
			t.Fatalf("full-history request missing %q in input: %#v", marker, input)
		}
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

func requestMessagesContainText(messages []llm.Message, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Text(), want) {
			return true
		}
	}
	return false
}
