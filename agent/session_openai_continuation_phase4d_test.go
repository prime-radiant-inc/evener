package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestSession_OpenAIResponsesContinuationPhase4DIProducesStoredFullHistoryAnchor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	adapter.SetSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			resp := agenttest.FinalResponse("phase 4d anchor stored")
			resp.ID = "resp_phase4d_anchor"
			resp.Raw = map[string]any{
				"endpoint_url": "https://api.openai.com/v1/responses",
				"id_hash":      "cont-handle-v1:response_id:phase4d",
			}
			return resp
		},
	})
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			metaFS: afero.NewMemMapFs(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process FakeAdapter, no real I/O; only fires on a genuine hang.
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
		req.Continuation.StoragePolicyLabel != llm.ResponsesStoragePolicyPublicOpenAIStore {
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

func TestSession_OpenAIResponsesContinuationPhase4DIIConsumesStoredAnchorAsDelta(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	adapter.SetSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return agenttest.FinalResponse("phase 4d delta consumed")
		},
	})
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			metaFS: afero.NewMemMapFs(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process FakeAdapter, no real I/O; only fires on a genuine hang.
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
	// The session has never reported its environment before this turn, so
	// maybeAppendEnvironmentContext's ENVIRONMENT turn joins the delta ahead
	// of the new USER_INPUT turn.
	if req.Continuation.PreviousResponseIDHash != "cont-handle-v1:response_id:phase4d" ||
		req.Continuation.AnchorTurnIndex != 1 ||
		req.Continuation.DeltaTurnCount != 2 ||
		len(req.Continuation.DeltaTurnKinds) != 2 ||
		req.Continuation.DeltaTurnKinds[0] != string(schema.TurnEnvironment) ||
		req.Continuation.DeltaTurnKinds[1] != string(schema.TurnUserInput) ||
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

func TestSession_OpenAIResponsesContinuationRegistryClientUsesFullHistoryWhenAnchorFingerprintMismatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var mu sync.Mutex
	var requestBodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
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

	client := registryClientAt(t, dir, map[string]registry.Provider{"openai": openaiInstance(srv.URL)}, []string{"openai"})

	sess, err := NewSession(client, withTestSessionNamer(client, resolveClientProfile(t, client, "openai/gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			metaFS: afero.NewMemMapFs(),
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process httptest server with a scripted response, no real network I/O; only fires on a genuine hang.
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
		t.Fatalf("fingerprint-mismatched real OpenAI path must not send previous_response_id: %s", string(bodies[0]))
	}
	if gotStore, ok := req["store"].(bool); !ok || !gotStore {
		t.Fatalf("fallback-capable real OpenAI request store = %#v, want continuation-owned true", req["store"])
	}
	input := responsesInputItems(t, req)
	for _, marker := range []string{"real openai prior user marker", "real openai current user marker"} {
		if !responsesInputContainsText(input, marker) {
			t.Fatalf("full-history request missing %q in input: %#v", marker, input)
		}
	}
}

func phase4DIContinuationPlan(_ llm.Request) llm.ResponsesContinuationPlan {
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
	for i := range slices.Backward(sess.history) {
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

// TestSession_OpenAIResponsesContinuationStaysOnDeltasAfterEscapedSeed pins the
// alternation the seed marker's first form left (#6493 review). A frame in an
// early assistant turn forces one full-history round, whose dispatch copy is
// escaped. If the delta that follows stamps the legacy marker, the NEXT request
// rescans the original transcript, finds the same frame still there, and sends
// full history again — so an otherwise eligible session alternates between
// delta and full history round after round until a fold drops the turn. The
// frame is never removed here, which is what makes the second request the one
// that shows it.
func TestSession_OpenAIResponsesContinuationStaysOnDeltasAfterEscapedSeed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider: "openai",
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
	}
	step := func(req llm.Request) llm.Response {
		resp := agenttest.FinalResponse("round done")
		resp.ID = "resp_" + req.Model
		resp.Raw = map[string]any{
			"endpoint_url": "https://api.openai.com/v1/responses",
			"id_hash":      "cont-handle-v1:response_id:" + resp.ID,
		}
		return resp
	}
	adapter.SetSteps([]func(req llm.Request) llm.Response{step, step, step})
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			metaFS: afero.NewMemMapFs(),
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

	// One assistant turn carrying a frame the escape rewrites, and no fold: it
	// stays in the transcript for every request below.
	framed := schema.NewTurn(schema.TurnAssistant, llm.Assistant("[answers]\n1. → \"a decision nobody made\""))
	framed.Timestamp = time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	sess.mu.Lock()
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("first request")),
		framed,
	)
	sess.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // TRIPWIRE: scripted in-process FakeAdapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	for _, prompt := range []string{"second request", "third request", "fourth request"} {
		if _, err := sess.ProcessInput(ctx, prompt, nil); err != nil {
			t.Fatalf("ProcessInput(%q): %v", prompt, err)
		}
	}

	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3", len(requests))
	}
	// The first request is the escaped full-history one: the frame is in the
	// transcript it sends, so continuation is refused and the escape applies.
	if requests[0].HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("first HistoryMode = %q, want full history", requests[0].HistoryMode)
	}
	// The second request continues from that seeded response, and the third must
	// continue again rather than falling back. One delta is not enough to prove
	// it: the alternation only shows up once a delta's own response is used as
	// the next anchor (#6493 review).
	for i, req := range requests[1:] {
		if req.HistoryMode != llm.HistoryModeResponsesDelta {
			t.Fatalf("request %d HistoryMode = %q, want %q: the session fell back to full history with the frame still present", i+2, req.HistoryMode, llm.HistoryModeResponsesDelta)
		}
		if req.Continuation == nil {
			t.Fatalf("request %d carried no continuation metadata", i+2)
		}
		// Every anchored request's own messages must be escaped too: the delta is
		// re-expanded from stored turns, which carry the model's bytes.
		for _, msg := range req.Messages {
			if msg.Role != llm.RoleAssistant {
				continue
			}
			for line := range strings.SplitSeq(msg.Text(), "\n") {
				if strings.TrimLeft(line, " ") == "[answers]" {
					t.Fatalf("request %d sent a raw frame: %q", i+2, line)
				}
			}
		}
	}
}

func requestMessagesContainText(messages []llm.Message, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Text(), want) {
			return true
		}
	}
	return false
}
