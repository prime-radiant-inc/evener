package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_TranscriptAPILogSeparationAndAttemptGroupJoin(t *testing.T) {
	const credentialSentinel = "credential_phase5b_must_not_persist"
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"wire_response_sentinel","code":"model_not_found","type":"invalid_request_error"}}`))
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
		APIKey:  credentialSentinel,
		BaseURL: srv.URL,
		Client:  srv.Client(),
	})
	apiLogger, err := llm.NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	client.Use(apiLogger)

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
	if err := apiLogger.Close(); err != nil {
		t.Fatalf("close API logger: %v", err)
	}

	data, err := readTranscriptFull(tpath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var assistant schema.Turn
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			if assistant.Kind != "" {
				t.Fatal("transcript contains more than one assistant turn")
			}
			assistant = entry.Turn
		}
	}
	groupID := assistant.AttemptGroupID
	if !strings.HasPrefix(groupID, "ag_") {
		t.Fatalf("assistant attempt group id = %q", groupID)
	}
	transcriptBytes, err := os.ReadFile(tpath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcriptBytes), "wire_response_sentinel") || strings.Contains(string(transcriptBytes), `"stream":true`) {
		t.Fatalf("transcript contains wire-only evidence: %s", transcriptBytes)
	}
	if strings.Contains(string(transcriptBytes), credentialSentinel) {
		t.Fatalf("transcript contains credential sentinel: %s", transcriptBytes)
	}

	apiPath := filepath.Join(dir, "sessions", sess.id+".api.jsonl")
	info, err := os.Stat(apiPath)
	if err != nil {
		t.Fatalf("stat canonical API log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("canonical API log mode = %04o, want 0600", got)
	}
	apiBytes, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatalf("read canonical API log: %v", err)
	}
	if strings.Contains(string(apiBytes), credentialSentinel) {
		t.Fatalf("canonical API log contains credential sentinel: %s", apiBytes)
	}

	f, err := os.Open(apiPath)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	var attempts []apilog.APIAttemptRecord
	var settlement apilog.APIAttemptGroupSettlement
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode canonical API log: %v", err)
		}
		switch record := record.(type) {
		case apilog.APIAttemptRecord:
			attempts = append(attempts, record)
		case apilog.APIAttemptGroupSettlement:
			settlement = record
		}
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2: %+v", len(attempts), attempts)
	}
	if attempts[0].AttemptGroupID != groupID || attempts[1].AttemptGroupID != groupID ||
		attempts[0].AttemptIndex != 1 || attempts[1].AttemptIndex != 2 ||
		attempts[0].Request.HistoryMode != string(llm.HistoryModeFullHistory) ||
		attempts[1].Request.HistoryMode != string(llm.HistoryModeChatFallback) ||
		attempts[0].Outcome != apilog.AttemptProviderReject || attempts[1].Outcome != apilog.AttemptSuccess {
		t.Fatalf("canonical attempts = %+v", attempts)
	}
	if attempts[0].Response == nil || !strings.Contains(attempts[0].Response.Body.Data, "wire_response_sentinel") ||
		!strings.Contains(attempts[0].Request.Body.Data, `"stream":true`) {
		t.Fatalf("canonical wire evidence missing: %+v", attempts[0])
	}
	if settlement.AttemptGroupID != groupID || settlement.FinalAttemptID != attempts[1].AttemptID || settlement.FinalAttemptCount != 2 {
		t.Fatalf("settlement = %+v", settlement)
	}
}
