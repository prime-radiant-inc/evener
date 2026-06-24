package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
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

func TestSession_OpenAIResponsesContinuationPhase9FallbackReplaySanitizesMalformedToolCall(t *testing.T) {
	dir := t.TempDir()
	const malformedArgs = `{"value": broken`

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
		requestIndex := len(requestBodies)
		mu.Unlock()

		switch requestIndex {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Previous response not found","code":"previous_response_not_found","type":"invalid_request_error"}}`))
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			args := mustJSON(t, map[string]any{
				"message":  "phase 9 sanitizer recovered",
				"end_turn": true,
				"output": map[string]any{
					"message":   "",
					"data":      map[string]any{},
					"artifacts": []string{},
				},
			})
			writeResponsesFunctionCall(t, w, flusher, "resp_phase9_done", "call_phase9_done", "communicate", args)
		default:
			t.Errorf("unexpected request %d body: %s", requestIndex, string(body))
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient()
	client.Register(&phase9PlanningOpenAIAdapter{
		inner: &openai.Adapter{
			APIKey:  "test-key",
			BaseURL: srv.URL,
			Client:  srv.Client(),
		},
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

	anchor := phase9MatchingAnchor("resp_phase9_sanitizer")
	anchor.Message = llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "call_phase9_bad",
			Name:      "my_strict_tool",
			Type:      "function",
			Arguments: []byte(malformedArgs),
		},
	}}}
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		anchor,
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call_phase9_bad", "my_strict_tool", map[string]any{
			"is_error": true,
			"message":  "invalid tool arguments JSON",
		}, true)),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := sess.ProcessInput(ctx, "phase9 current user marker", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got != "phase 9 sanitizer recovered" {
		t.Fatalf("ProcessInput output = %q, want recovered message", got)
	}

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("OpenAI Responses request count = %d, want 2", len(bodies))
	}

	first := decodeResponsesRequest(t, bodies[0])
	if first["previous_response_id"] != "resp_phase9_sanitizer" {
		t.Fatalf("first request previous_response_id = %#v, want resp_phase9_sanitizer", first["previous_response_id"])
	}
	if call := findResponsesItem(t, responsesInputItems(t, first), "function_call", "call_id", "call_phase9_bad"); call != nil {
		t.Fatalf("delta request replayed pre-anchor malformed function_call: %#v", call)
	}

	second := decodeResponsesRequest(t, bodies[1])
	if _, ok := second["previous_response_id"]; ok {
		t.Fatalf("full-history fallback request must not carry previous_response_id: %s", string(bodies[1]))
	}
	input := responsesInputItems(t, second)
	replayedCall := findResponsesItem(t, input, "function_call", "call_id", "call_phase9_bad")
	if replayedCall == nil {
		t.Fatalf("fallback request missing replayed function_call for call_phase9_bad: %#v", input)
	}
	if gotArgs, _ := replayedCall["arguments"].(string); gotArgs != "{}" {
		t.Fatalf("fallback malformed function_call arguments = %q, want {}", gotArgs)
	}
	if output := findResponsesItem(t, input, "function_call_output", "call_id", "call_phase9_bad"); output == nil {
		t.Fatalf("fallback request missing linked function_call_output for call_phase9_bad: %#v", input)
	}
}

func TestSession_OpenAIResponsesContinuationPhase9DisabledStateUsesFullHistoryAfterRejection(t *testing.T) {
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
				return agenttest.FinalResponse("phase 9 disabled state recovered"), nil
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
	setPhase9ContinuationHistory(sess, phase9MatchingAnchor("resp_phase9_disabled_first"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase9 first current user marker", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}

	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("first request count = %d, want 2", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta ||
		requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback {
		t.Fatalf("first requests = %+v", requests)
	}

	setPhase9ContinuationHistory(sess, phase9MatchingAnchor("resp_phase9_disabled_second"))
	if _, err := sess.ProcessInput(ctx, "phase9 second current user marker", nil); err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	requests = adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("second request count = %d, want 3", len(requests))
	}
	if requests[2].HistoryMode != llm.HistoryModeFullHistory ||
		requests[2].PreviousResponseID != "" {
		t.Fatalf("disabled same-scope request = %+v, want full history without previous response", requests[2])
	}

	adapter.storageScopeFingerprint = "cont-scope-v1:phase9-other"
	otherScopeAnchor := phase9MatchingAnchor("resp_phase9_disabled_other_scope")
	otherScopeAnchor.ResponseStorageScopeFingerprint = adapter.storageScopeFingerprint
	setPhase9ContinuationHistory(sess, otherScopeAnchor)
	if _, err := sess.ProcessInput(ctx, "phase9 other scope current user marker", nil); err != nil {
		t.Fatalf("other-scope ProcessInput: %v", err)
	}
	requests = adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("other-scope request count = %d, want 4", len(requests))
	}
	if requests[3].HistoryMode != llm.HistoryModeResponsesDelta ||
		requests[3].PreviousResponseID != "resp_phase9_disabled_other_scope" {
		t.Fatalf("other-scope request = %+v, want fresh delta", requests[3])
	}

	adapter.storageScopeFingerprint = "cont-scope-v1:phase4d"
	adapter.storagePolicyLabel = llm.ResponsesStoragePolicyPublicOpenAINoStore
	setPhase9ContinuationHistory(sess, phase9MatchingAnchor("resp_phase9_disabled_other_policy"))
	if _, err := sess.ProcessInput(ctx, "phase9 other policy current user marker", nil); err != nil {
		t.Fatalf("other-policy ProcessInput: %v", err)
	}
	requests = adapter.Requests()
	if len(requests) != 5 {
		t.Fatalf("other-policy request count = %d, want 5", len(requests))
	}
	if requests[4].HistoryMode != llm.HistoryModeResponsesDelta ||
		requests[4].PreviousResponseID != "resp_phase9_disabled_other_policy" {
		t.Fatalf("other-policy request = %+v, want fresh delta", requests[4])
	}
}

func TestSession_OpenAIResponsesContinuationPhase9DisabledStateDoesNotLeakToNewSession(t *testing.T) {
	dir := t.TempDir()
	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "Previous response not found",
			"type":    "invalid_request_error",
		},
	}, nil)
	firstAdapter := &phase9RetryAdapter{
		provider:          "openai",
		canFallbackToChat: true,
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, continuationErr
			},
			func(req llm.Request) (llm.Response, error) {
				return agenttest.FinalResponse("phase 9 disabled state recovered"), nil
			},
		},
	}
	firstClient := llm.NewClient()
	firstClient.Register(firstAdapter)
	firstSess := newPhase9ContinuationSession(t, dir, firstClient)
	defer firstSess.Close()
	drainSessionEvents(firstSess)
	setPhase9ContinuationHistory(firstSess, phase9MatchingAnchor("resp_phase9_disabled_original"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := firstSess.ProcessInput(ctx, "phase9 original current user marker", nil); err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}

	nextAdapter := &phase9RetryAdapter{
		provider:          "openai",
		canFallbackToChat: true,
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return agenttest.FinalResponse("phase 9 new session delta"), nil
			},
		},
	}
	nextClient := llm.NewClient()
	nextClient.Register(nextAdapter)
	nextSess := newPhase9ContinuationSession(t, dir, nextClient)
	defer nextSess.Close()
	drainSessionEvents(nextSess)
	setPhase9ContinuationHistory(nextSess, phase9MatchingAnchor("resp_phase9_disabled_new_session"))
	if _, err := nextSess.ProcessInput(ctx, "phase9 new session current user marker", nil); err != nil {
		t.Fatalf("new-session ProcessInput: %v", err)
	}

	requests := nextAdapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("new-session request count = %d, want 1", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta ||
		requests[0].PreviousResponseID != "resp_phase9_disabled_new_session" {
		t.Fatalf("new-session request = %+v, want fresh delta", requests[0])
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
	provider                string
	canFallbackToChat       bool
	storageScopeFingerprint string
	storagePolicyLabel      string
	steps                   []func(req llm.Request) (llm.Response, error)

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
	if a.storageScopeFingerprint != "" {
		plan.StorageScopeFingerprint = a.storageScopeFingerprint
	}
	if a.storagePolicyLabel != "" {
		plan.StoragePolicyLabel = a.storagePolicyLabel
	}
	plan.CanFallbackToChat = a.canFallbackToChat
	return plan, nil
}

func (a *phase9RetryAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

type phase9PlanningOpenAIAdapter struct {
	inner *openai.Adapter
}

func (a *phase9PlanningOpenAIAdapter) Name() string { return a.inner.Name() }

func (a *phase9PlanningOpenAIAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return a.inner.Complete(ctx, req)
}

func (a *phase9PlanningOpenAIAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	return a.inner.Stream(ctx, req)
}

func (a *phase9PlanningOpenAIAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	plan := phase4DIContinuationPlan(req)
	plan.CanFallbackToChat = true
	return plan, nil
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

func setPhase9ContinuationHistory(sess *Session, anchor schema.Turn) {
	sess.history = []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("phase9 prior user marker")),
		anchor,
	}
}

func newPhase9ContinuationSession(t *testing.T, dir string, client *llm.Client) *Session {
	t.Helper()
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
	return sess
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
