package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesContinuationPhase5BRecordsEndpointFallbackAttempts(t *testing.T) {
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"model not found","code":"model_not_found","type":"invalid_request_error"}}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunks := []string{
				`data: {"id":"cc_phase5b","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_phase5b","type":"function","function":{"name":"communicate","arguments":""}}]},"finish_reason":null}]}`,
				`data: {"id":"cc_phase5b","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"message\":\"fallback response\",\"end_turn\":true,\"output\":{\"message\":\"\",\"data\":{},\"artifacts\":[]}}"}}]},"finish_reason":null}]}`,
				`data: {"id":"cc_phase5b","model":"gpt-4.1-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
				`data: [DONE]`,
			}
			for _, c := range chunks {
				_, _ = fmt.Fprintln(w, c)
				_, _ = fmt.Fprintln(w)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := llm.NewClient()
	client.Register(&openai.Adapter{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})

	sess, err := NewSession(client, NewOpenAIProfile("gpt-4.1-mini"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "trigger endpoint fallback", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	tpath := sess.TranscriptPath()
	sess.Close()

	data, err := readTranscriptFull(tpath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if len(data.APICalls) != 2 {
		t.Fatalf("api_calls = %d, want 2", len(data.APICalls))
	}
	groupID := data.APICalls[0].AttemptGroupID
	if !strings.HasPrefix(groupID, "ag_") {
		t.Fatalf("first attempt group id = %q", groupID)
	}
	if data.APICalls[1].AttemptGroupID != groupID {
		t.Fatalf("attempt group ids differ: %q/%q", groupID, data.APICalls[1].AttemptGroupID)
	}
	if data.APICalls[0].AttemptIndex != 1 ||
		data.APICalls[0].AttemptCount != 0 ||
		data.APICalls[0].FinalAttemptCount != nil ||
		data.APICalls[0].HistoryMode != llm.HistoryModeFullHistory ||
		data.APICalls[0].Response != nil ||
		data.APICalls[0].Error == "" ||
		data.APICalls[0].Request.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("responses attempt = %+v", data.APICalls[0])
	}
	if data.APICalls[1].AttemptIndex != 2 ||
		data.APICalls[1].AttemptCount != 2 ||
		data.APICalls[1].FinalAttemptCount == nil ||
		*data.APICalls[1].FinalAttemptCount != 2 ||
		data.APICalls[1].HistoryMode != llm.HistoryModeChatFallback ||
		data.APICalls[1].Response == nil ||
		data.APICalls[1].Error != "" ||
		data.APICalls[1].Request.HistoryMode != llm.HistoryModeChatFallback {
		t.Fatalf("chat fallback attempt = %+v", data.APICalls[1])
	}
	var assistantCount int
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("assistant turns = %d, want 1", assistantCount)
	}
}
