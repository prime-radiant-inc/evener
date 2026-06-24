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
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesContinuationOffUsesFullHistory(t *testing.T) {
	dir := t.TempDir()

	decision := llm.DecideResponsesContinuation(
		llm.ResponsesContinuationOff,
		llm.ResponsesContinuationSupportFor(
			llm.DefaultResponsesContinuationSupportRegistry(),
			llm.ResponsesEndpointFamilyOpenAIPublic,
		),
	)
	if decision.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("default registry decision = %+v, want full_history", decision)
	}

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
			"message":  "continued with full history",
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
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "off",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.history = append(sess.history,
		schema.Turn{
			Kind:       schema.TurnUserInput,
			Message:    llm.User("earlier user marker"),
			Timestamp:  time.Now().UTC().Add(-time.Minute),
			ResponseID: "resp_existing_user_field_ignored",
		},
		schema.Turn{
			Kind:       schema.TurnAssistant,
			Message:    llm.Assistant("earlier assistant marker"),
			Timestamp:  time.Now().UTC().Add(-time.Minute),
			ResponseID: "resp_existing_anchor",
		},
	)

	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := sess.ProcessInput(ctx, "new user marker", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !strings.Contains(got, "continued with full history") {
		t.Fatalf("ProcessInput output = %q, want server text", got)
	}

	sess.Close()
	<-eventsDone

	mu.Lock()
	bodies := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("OpenAI Responses request count = %d, want 1", len(bodies))
	}

	req := decodeResponsesRequest(t, bodies[0])
	if _, ok := req["previous_response_id"]; ok {
		t.Fatalf("off mode must not send previous_response_id: %s", string(bodies[0]))
	}
	if gotStore, ok := req["store"].(bool); !ok || gotStore {
		t.Fatalf("off mode request store = %#v, want explicit false", req["store"])
	}

	input := responsesInputItems(t, req)
	for _, marker := range []string{"earlier user marker", "earlier assistant marker", "new user marker"} {
		if !responsesInputContainsText(input, marker) {
			t.Fatalf("full-history request missing %q in input: %#v", marker, input)
		}
	}
}

func responsesInputContainsText(items []any, want string) bool {
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, partAny := range content {
			part, ok := partAny.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := part["text"].(string); ok && strings.Contains(text, want) {
				return true
			}
		}
	}
	return false
}
