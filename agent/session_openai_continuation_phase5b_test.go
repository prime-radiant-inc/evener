package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

// TestSession_TranscriptAPILogSeparationAndAttemptGroupJoin drives two attempts
// into one group — a continuation delta the endpoint rejects, then the
// full-history retry that answers — and pins that the semantic transcript keeps
// none of the wire evidence or the credential, while the canonical API log
// keeps both attempts joined to the assistant turn's group.
func TestSession_TranscriptAPILogSeparationAndAttemptGroupJoin(t *testing.T) {
	const credentialSentinel = "credential_phase5b_must_not_persist"
	dir := t.TempDir()
	var mu sync.Mutex
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		requests++
		index := requests
		mu.Unlock()
		switch index {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"wire_response_sentinel: Previous response not found","code":"previous_response_not_found","type":"invalid_request_error"}}`))
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			args := mustJSON(t, map[string]any{
				"message":  "full-history retry answered",
				"end_turn": true,
				"output": map[string]any{
					"message":   "",
					"data":      map[string]any{},
					"artifacts": []string{},
				},
			})
			writeResponsesFunctionCall(t, w, flusher, "resp_phase5b", "call_phase5b", "communicate", args)
		}
	}))
	t.Cleanup(srv.Close)

	instances := map[string]registry.Provider{"openai": {
		Base: "openai", APIKey: credentialSentinel,
		Transport: registry.Transport{BaseURL: srv.URL},
	}}
	dispatch := registryClientAt(t, dir, instances, []string{"openai"})
	client := registryClientAt(t, dir, instances, nil, &phase9PlanningOpenAIAdapter{inner: dispatch})
	apiLogger, err := llm.NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	client.Use(apiLogger)

	sess, err := NewSession(client, resolveClientProfile(t, client, "openai/gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	drainSessionEvents(sess)
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase5b prior user marker")),
		phase9MatchingAnchor("resp_phase5b_anchor"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process httptest server with a scripted response, no real network I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "trigger the anchor-rejection retry", nil); err != nil {
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
		attempts[0].Request.HistoryMode != string(llm.HistoryModeResponsesDelta) ||
		attempts[1].Request.HistoryMode != string(llm.HistoryModeFullHistoryFallback) ||
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
	// The turn records the wire protocol of the dispatch that answered it,
	// read back from the attempt group the retry settled.
	if assistant.ResponseProtocol != registry.ProtocolOpenAIResponses {
		t.Fatalf("assistant ResponseProtocol = %q, want %q", assistant.ResponseProtocol, registry.ProtocolOpenAIResponses)
	}
}
