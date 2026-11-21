package agent

import (
	"context"
	"fmt"
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
		req.Continuation.ContextMarker != responseContextMarkerV2 ||
		req.Continuation.StoragePolicyLabel != llm.ResponsesStoragePolicyPublicOpenAIStore {
		t.Fatalf("Continuation metadata = %+v", req.Continuation)
	}

	assistant := latestAssistantTurn(t, sess)
	if assistant.ResponseID != "resp_phase4d_anchor" ||
		assistant.ResponseIDHash != "cont-handle-v1:response_id:phase4d" ||
		assistant.ResponseEndpoint != "https://api.openai.com/v1/responses" ||
		assistant.ResponseRequestFingerprint != "cont-req-v1:phase4d" ||
		assistant.ResponseStorageScopeFingerprint != "cont-scope-v1:phase4d" ||
		assistant.ResponseContextMarker != responseContextMarkerV2 ||
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
		req.Continuation.ContextMarker != responseContextMarkerV2 ||
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

// TestSession_OpenAIResponsesContinuationRegistryClientV2AnchorCutover drives
// the real registry client and planner over loopback SSE: the first turn
// creates a genuine v3 anchor, and a stored anchor whose only change is the v2
// request-fingerprint prefix then cannot continue the session. The next turn
// resends full history and re-anchors on the new v3 response, and the following
// turn continues from that fresh anchor. The v3 control keeps the created
// anchor and continues immediately, so the prefix alone is what the cut-over
// rejects.
func TestSession_OpenAIResponsesContinuationRegistryClientV2AnchorCutover(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		storedPrefixIsV2 bool
	}{
		{"v2 stored prefix resends full history then re-anchors", true},
		{"v3 control continues from the created anchor", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				n := len(requestBodies)
				mu.Unlock()

				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				args := mustJSON(t, map[string]any{
					"message":  "real openai turn done",
					"end_turn": true,
					"output": map[string]any{
						"message":   "",
						"data":      map[string]any{},
						"artifacts": []string{},
					},
				})
				writeResponsesFunctionCall(t, w, flusher, fmt.Sprintf("resp_v3_%d", n), fmt.Sprintf("call_done_%d", n), "communicate", args)
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

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process httptest server with a scripted response, no real network I/O; only fires on a genuine hang.
			defer cancel()
			if _, err := sess.ProcessInput(ctx, "real openai first user marker", nil); err != nil {
				t.Fatalf("first ProcessInput: %v", err)
			}

			anchor := latestAssistantTurn(t, sess)
			if !strings.HasPrefix(anchor.ResponseRequestFingerprint, "cont-req-v3:") || anchor.ResponseID != "resp_v3_1" {
				t.Fatalf("created anchor = %+v, want the real v3 fingerprint and resp_v3_1", anchor)
			}
			if tc.storedPrefixIsV2 {
				sess.mu.Lock()
				for i := range slices.Backward(sess.history) {
					if sess.history[i].Kind == schema.TurnAssistant {
						sess.history[i].ResponseRequestFingerprint = "cont-req-v2:" + strings.TrimPrefix(sess.history[i].ResponseRequestFingerprint, "cont-req-v3:")
						break
					}
				}
				sess.mu.Unlock()
			}

			if _, err := sess.ProcessInput(ctx, "real openai second user marker", nil); err != nil {
				t.Fatalf("second ProcessInput: %v", err)
			}
			if tc.storedPrefixIsV2 {
				if _, err := sess.ProcessInput(ctx, "real openai third user marker", nil); err != nil {
					t.Fatalf("third ProcessInput: %v", err)
				}
			}

			mu.Lock()
			bodies := append([][]byte(nil), requestBodies...)
			mu.Unlock()
			wantRequests := 2
			if tc.storedPrefixIsV2 {
				wantRequests = 3
			}
			if len(bodies) != wantRequests {
				t.Fatalf("OpenAI Responses request count = %d, want %d", len(bodies), wantRequests)
			}

			second := decodeResponsesRequest(t, bodies[1])
			secondInput := responsesInputItems(t, second)
			if tc.storedPrefixIsV2 {
				if _, ok := second["previous_response_id"]; ok {
					t.Fatalf("v2-prefixed anchor must not send previous_response_id: %s", string(bodies[1]))
				}
				if gotStore, ok := second["store"].(bool); !ok || !gotStore {
					t.Fatalf("fallback-capable full-history request store = %#v, want continuation-owned true: %s", second["store"], string(bodies[1]))
				}
				for _, marker := range []string{"real openai first user marker", "real openai second user marker"} {
					if !responsesInputContainsText(secondInput, marker) {
						t.Fatalf("full-history request missing %q in input: %#v", marker, secondInput)
					}
				}
				third := decodeResponsesRequest(t, bodies[2])
				if third["previous_response_id"] != "resp_v3_2" {
					t.Fatalf("third request previous_response_id = %#v, want resp_v3_2 re-anchored on the fresh response: %s", third["previous_response_id"], string(bodies[2]))
				}
				return
			}
			if second["previous_response_id"] != "resp_v3_1" {
				t.Fatalf("v3 control previous_response_id = %#v, want resp_v3_1: %s", second["previous_response_id"], string(bodies[1]))
			}
			if responsesInputContainsText(secondInput, "real openai first user marker") {
				t.Fatalf("v3 control delta replayed pre-anchor text: %#v", secondInput)
			}
		})
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

func requestMessagesContainText(messages []llm.Message, want string) bool {
	for _, msg := range messages {
		if strings.Contains(msg.Text(), want) {
			return true
		}
	}
	return false
}
