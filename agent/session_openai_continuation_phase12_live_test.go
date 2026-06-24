package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/envvars"
	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof(t *testing.T) {
	if os.Getenv("SERF_OPENAI_RESPONSES_PHASE12_E2E") != "1" {
		t.Skip("set SERF_OPENAI_RESPONSES_PHASE12_E2E=1 to run live public OpenAI Responses continuation proof")
	}
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Skip("OPENAI_API_KEY is required for public OpenAI phase 12 proof")
	}
	if !llm.RawBodyEnabled() {
		t.Skip(envvars.SERFLogRawHTTP.Name + "=1 must be set before go test starts so raw request bodies are captured")
	}

	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_RESPONSES_PHASE12_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}

	stateDir := t.TempDir()
	rawPath := filepath.Join(stateDir, "api-raw.jsonl")
	apiLog, err := llm.NewAPILogger(filepath.Join(stateDir, "api.jsonl"))
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	if err := apiLog.EnableRawLogging(rawPath); err != nil {
		t.Fatalf("EnableRawLogging: %v", err)
	}
	t.Cleanup(func() { _ = apiLog.Close() })

	adapter, err := openai.NewFromEnv(openai.Config{StateHome: stateDir})
	if err != nil {
		t.Fatalf("openai.NewFromEnv: %v", err)
	}
	capture := &phase12PublicOpenAIAdapter{inner: adapter}
	client := llm.NewClient()
	client.Register(capture)
	client.Use(apiLog)

	runID := strings.ToLower(ulid.Make().String())
	priorMarker := "phase12-prior-" + runID
	currentMarker := "phase12-current-" + runID

	sess, err := NewSession(client, NewOpenAIProfile(model), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
		StateDir:                    stateDir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: {
					EndpointFamily:        llm.ResponsesEndpointFamilyOpenAIPublic,
					StorageShapeProven:    true,
					ProductionPathProven:  true,
					Enabled:               true,
					MaxAnchorAgeSeconds:   3600,
					StorageShapeProofID:   "2026-06-24-responses-continuation-phase-0b",
					ProductionPathProofID: "2026-06-24-responses-continuation-phase-12a-public-live-harness",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "Reply exactly: anchor ok. Marker: "+priorMarker, nil); err != nil {
		t.Fatalf("anchor ProcessInput: %v", err)
	}
	if _, err := sess.ProcessInput(ctx, "Reply exactly: delta ok. Marker: "+currentMarker, nil); err != nil {
		t.Fatalf("delta ProcessInput: %v", err)
	}

	deltaReq, ok := capture.firstRequestWithHistoryMode(llm.HistoryModeResponsesDelta)
	if !ok {
		t.Fatalf("captured requests did not include responses_delta: %+v", capture.historyModes())
	}
	if strings.TrimSpace(deltaReq.PreviousResponseID) == "" {
		t.Fatal("delta request missing PreviousResponseID")
	}
	if requestMessagesContainText(deltaReq.Messages, priorMarker) {
		t.Fatalf("delta request messages include prior marker: %+v", deltaReq.Messages)
	}
	if !requestMessagesContainText(deltaReq.FullHistoryFallbackMessages, priorMarker) ||
		!requestMessagesContainText(deltaReq.FullHistoryFallbackMessages, currentMarker) {
		t.Fatalf("delta FullHistoryFallbackMessages missing proof markers: %+v", deltaReq.FullHistoryFallbackMessages)
	}

	store := true
	shadowReq := deltaReq
	shadowReq.Messages = append([]llm.Message(nil), deltaReq.FullHistoryFallbackMessages...)
	shadowReq.PreviousResponseID = ""
	shadowReq.HistoryMode = llm.HistoryModeFullHistory
	shadowReq.Continuation = nil
	shadowReq.FullHistoryFallbackMessages = nil
	shadowReq.Store = &store
	shadowReq.ContinuationDiagnostic = "phase12_full_history_shadow"
	if _, err := client.Complete(llm.WithAPILogAttemptContext(ctx, llm.APILogContext{
		SessionID:   "phase12-shadow-" + runID,
		Round:       1,
		HistoryMode: llm.HistoryModeFullHistory,
	}), shadowReq); err != nil {
		t.Fatalf("full-history shadow request: %v", err)
	}

	invalidReq := llm.Request{
		Provider:           "openai",
		Model:              model,
		Messages:           []llm.Message{llm.User("This invalid anchor request should fail clearly.")},
		PreviousResponseID: "resp_serf_invalid_" + runID,
		Store:              &store,
	}
	if _, err := adapter.Complete(ctx, invalidReq); err == nil {
		t.Fatal("invalid previous_response_id was accepted")
	} else if got := adapter.ClassifyResponsesError(invalidReq, err); got != llm.ResponsesErrorContinuationRejected {
		t.Fatalf("ClassifyResponsesError = %q, want %q; err=%v", got, llm.ResponsesErrorContinuationRejected, err)
	}

	sess.Close()
	if err := apiLog.Close(); err != nil {
		t.Fatalf("apiLog Close: %v", err)
	}

	transcriptPath := filepath.Join(stateDir, sessionsSubdir, sess.id+".transcript.jsonl")
	data, err := readTranscriptFull(transcriptPath)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var deltaCall *llm.APILogRequest
	var providerInputTokens int
	for i := range data.APICalls {
		call := data.APICalls[i]
		if call.HistoryMode == llm.HistoryModeResponsesDelta || call.Request.HistoryMode == llm.HistoryModeResponsesDelta {
			req := call.Request
			deltaCall = &req
			if call.Response != nil {
				providerInputTokens = call.Response.Usage.InputTokens
			}
			break
		}
	}
	if deltaCall == nil {
		t.Fatalf("transcript has no responses_delta api_call; api_calls=%d", len(data.APICalls))
	}
	if deltaCall.PreviousResponseIDHash == "" {
		t.Fatalf("delta api_call missing PreviousResponseIDHash: %+v", *deltaCall)
	}
	if deltaCall.FullHistoryInputTokensEstimate <= 0 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want positive", deltaCall.FullHistoryInputTokensEstimate)
	}

	rawEntries := phase12ReadRawLogEntries(t, rawPath)
	deltaRaw := phase12FindRawRequest(t, rawEntries, llm.HistoryModeResponsesDelta)
	shadowRaw := phase12FindRawRequestWithSession(t, rawEntries, "phase12-shadow-"+runID)
	if !strings.Contains(deltaRaw.RequestBody, `"previous_response_id"`) {
		t.Fatalf("delta raw request missing previous_response_id")
	}
	if strings.Contains(deltaRaw.RequestBody, priorMarker) {
		t.Fatalf("delta raw request included prior marker")
	}
	if !strings.Contains(deltaRaw.RequestBody, currentMarker) {
		t.Fatalf("delta raw request missing current marker")
	}
	if !strings.Contains(shadowRaw.RequestBody, priorMarker) || !strings.Contains(shadowRaw.RequestBody, currentMarker) {
		t.Fatalf("full-history shadow raw request missing proof markers")
	}
	if len(deltaRaw.RequestBody) >= len(shadowRaw.RequestBody) {
		t.Fatalf("delta raw request bytes = %d, want smaller than full-history shadow bytes %d", len(deltaRaw.RequestBody), len(shadowRaw.RequestBody))
	}
	t.Logf("phase12_public_live model=%s delta_bytes=%d full_history_shadow_bytes=%d provider_input_tokens=%d full_history_shadow_tokens=%d",
		model,
		len(deltaRaw.RequestBody),
		len(shadowRaw.RequestBody),
		providerInputTokens,
		deltaCall.FullHistoryInputTokensEstimate,
	)
}

type phase12PublicOpenAIAdapter struct {
	inner *openai.Adapter
	mu    sync.Mutex
	reqs  []llm.Request
}

func (a *phase12PublicOpenAIAdapter) Name() string { return a.inner.Name() }

func (a *phase12PublicOpenAIAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.record(req)
	return a.inner.Complete(ctx, req)
}

func (a *phase12PublicOpenAIAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	a.record(req)
	return a.inner.Stream(ctx, req)
}

func (a *phase12PublicOpenAIAdapter) record(req llm.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, phase12CloneRequest(req))
}

func (a *phase12PublicOpenAIAdapter) firstRequestWithHistoryMode(mode llm.HistoryMode) (llm.Request, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, req := range a.reqs {
		if req.HistoryMode == mode {
			return phase12CloneRequest(req), true
		}
	}
	return llm.Request{}, false
}

func (a *phase12PublicOpenAIAdapter) historyModes() []llm.HistoryMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	modes := make([]llm.HistoryMode, 0, len(a.reqs))
	for _, req := range a.reqs {
		modes = append(modes, req.HistoryMode)
	}
	return modes
}

func phase12CloneRequest(req llm.Request) llm.Request {
	out := req
	out.Messages = append([]llm.Message(nil), req.Messages...)
	out.FullHistoryFallbackMessages = append([]llm.Message(nil), req.FullHistoryFallbackMessages...)
	if req.Store != nil {
		store := *req.Store
		out.Store = &store
	}
	return out
}

func phase12ReadRawLogEntries(t *testing.T, path string) []llm.APIRawLogEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open raw log: %v", err)
	}
	defer f.Close() //nolint:errcheck

	var entries []llm.APIRawLogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry llm.APIRawLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("parse raw log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read raw log: %v", err)
	}
	return entries
}

func phase12FindRawRequest(t *testing.T, entries []llm.APIRawLogEntry, mode llm.HistoryMode) llm.APIRawLogEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.HistoryMode == mode && entry.RequestBody != "" {
			return entry
		}
	}
	t.Fatalf("raw log has no request for history mode %q; entries=%d", mode, len(entries))
	return llm.APIRawLogEntry{}
}

func phase12FindRawRequestWithSession(t *testing.T, entries []llm.APIRawLogEntry, sessionID string) llm.APIRawLogEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.SessionID == sessionID && entry.RequestBody != "" {
			return entry
		}
	}
	t.Fatalf("raw log has no request for session %q; entries=%d", sessionID, len(entries))
	return llm.APIRawLogEntry{}
}
