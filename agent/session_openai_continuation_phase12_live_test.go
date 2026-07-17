package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
	"primeradiant.com/serf/llm/providers/openai"
)

func TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof(t *testing.T) {
	if os.Getenv("SERF_OPENAI_RESPONSES_PHASE12_E2E") != "1" {
		t.Skip("set SERF_OPENAI_RESPONSES_PHASE12_E2E=1 to run live public OpenAI Responses continuation proof")
	}
	if strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" {
		t.Skip("OPENAI_API_KEY is required for public OpenAI phase 12 proof")
	}

	model := strings.TrimSpace(os.Getenv("SERF_OPENAI_RESPONSES_PHASE12_MODEL"))
	if model == "" {
		model = "gpt-5.2"
	}

	stateDir := t.TempDir()
	apiLogger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = apiLogger.Close()
		}
	})

	adapter, err := openai.NewFromEnv(openai.Config{StateHome: stateDir})
	if err != nil {
		t.Fatalf("openai.NewFromEnv: %v", err)
	}
	capture := &phase12PublicOpenAIAdapter{inner: adapter}
	client := llm.NewClient()
	client.Register(capture)
	client.Use(apiLogger)

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
		t.Fatalf("captured requests did not include responses_delta: requests=%+v assistant_metadata=%+v", capture.requestSummaries(), phase12AssistantMetadataSummaries(sess.history))
	}
	if strings.TrimSpace(deltaReq.PreviousResponseID) == "" {
		t.Fatal("delta request missing PreviousResponseID")
	}
	if deltaReq.Continuation == nil || strings.TrimSpace(deltaReq.Continuation.PreviousResponseIDHash) == "" {
		t.Fatalf("delta request missing PreviousResponseIDHash: %+v", deltaReq.Continuation)
	}
	if deltaReq.FullHistoryInputTokensEstimate <= 0 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want positive", deltaReq.FullHistoryInputTokensEstimate)
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
	shadowSessionID := "phase12-shadow-" + runID
	if _, err := client.Complete(llm.WithAPILogContext(ctx, shadowSessionID), shadowReq); err != nil {
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

	transcriptPath := sess.TranscriptPath()
	sess.Close()
	if err := apiLogger.Close(); err != nil {
		t.Fatalf("API logger Close: %v", err)
	}
	closed = true

	transcriptData, err := readTranscriptFull(transcriptPath)
	if err != nil {
		t.Fatalf("read semantic transcript: %v", err)
	}
	var deltaAssistant schema.Turn
	for _, entry := range transcriptData.Entries {
		if entry.Turn.Kind == schema.TurnAssistant {
			deltaAssistant = entry.Turn
		}
	}
	if deltaAssistant.AttemptGroupID == "" {
		t.Fatalf("persisted delta assistant missing attempt group: assistant_metadata=%+v", phase12AssistantMetadataSummaries(sess.history))
	}

	transcriptBytes, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcriptBytes), `"kind":"api_call"`) || strings.Contains(string(transcriptBytes), `"previous_response_id"`) {
		t.Fatalf("semantic transcript contains provider wire evidence: %s", transcriptBytes)
	}

	sessionAPIPath := filepath.Join(stateDir, sessionsSubdir, sess.id+".api.jsonl")
	sessionAttempts, sessionSettlements := phase12ReadCanonicalAPILog(t, sessionAPIPath)
	deltaAttempt := phase12FindCanonicalAttempt(t, sessionAttempts, llm.HistoryModeResponsesDelta)
	if deltaAttempt.AttemptGroupID != deltaAssistant.AttemptGroupID {
		t.Fatalf("delta API attempt group = %q, assistant group = %q", deltaAttempt.AttemptGroupID, deltaAssistant.AttemptGroupID)
	}
	deltaSettlement := phase12FindCanonicalSettlement(t, sessionSettlements, deltaAttempt.AttemptGroupID)
	if deltaSettlement.FinalAttemptID != deltaAttempt.AttemptID ||
		deltaSettlement.Outcome != apilog.AttemptSuccess {
		t.Fatalf("delta settlement = %+v, attempt = %+v", deltaSettlement, deltaAttempt)
	}

	shadowAPIPath := filepath.Join(stateDir, sessionsSubdir, shadowSessionID+".api.jsonl")
	shadowAttempts, _ := phase12ReadCanonicalAPILog(t, shadowAPIPath)
	shadowAttempt := phase12FindCanonicalAttempt(t, shadowAttempts, llm.HistoryModeFullHistory)

	deltaBody := phase12CanonicalRequestBody(t, deltaAttempt)
	shadowBody := phase12CanonicalRequestBody(t, shadowAttempt)
	if !strings.Contains(string(deltaBody), `"previous_response_id"`) {
		t.Fatalf("delta canonical request missing previous_response_id: %s", deltaBody)
	}
	if strings.Contains(string(deltaBody), priorMarker) {
		t.Fatalf("delta canonical request included prior marker: %s", deltaBody)
	}
	if !strings.Contains(string(deltaBody), currentMarker) {
		t.Fatalf("delta canonical request missing current marker: %s", deltaBody)
	}
	if !strings.Contains(string(shadowBody), priorMarker) || !strings.Contains(string(shadowBody), currentMarker) {
		t.Fatalf("full-history shadow canonical request missing proof markers: %s", shadowBody)
	}
	if len(deltaBody) >= len(shadowBody) {
		t.Fatalf("delta canonical request bytes = %d, want smaller than full-history shadow bytes %d", len(deltaBody), len(shadowBody))
	}
	if deltaAttempt.Response == nil {
		t.Fatalf("delta canonical attempt has no response: %+v", deltaAttempt)
	}
	deltaResponseBody, err := apilog.DecodeBody(deltaAttempt.Response.Body)
	if err != nil {
		t.Fatalf("decode delta canonical response body: %v", err)
	}
	if len(deltaResponseBody) == 0 {
		t.Fatal("delta canonical response body is empty")
	}

	omittedInputItemBytes := phase12OmittedInputItemBytes(t, shadowBody, deltaBody)
	continuationOverheadBytes := phase12ContinuationOverheadBytes(t, deltaBody)
	netBodyByteSaving := len(shadowBody) - len(deltaBody)
	if omittedInputItemBytes <= continuationOverheadBytes {
		t.Fatalf("omitted input item bytes = %d, want greater than continuation overhead bytes %d", omittedInputItemBytes, continuationOverheadBytes)
	}
	t.Logf("phase12_public_live model=%s delta_bytes=%d full_history_shadow_bytes=%d omitted_input_item_bytes=%d continuation_overhead_bytes=%d net_body_byte_saving=%d provider_input_tokens=%d full_history_shadow_tokens=%d",
		model,
		len(deltaBody),
		len(shadowBody),
		omittedInputItemBytes,
		continuationOverheadBytes,
		netBodyByteSaving,
		deltaAttempt.Response.Usage.InputTokens,
		deltaReq.FullHistoryInputTokensEstimate,
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

func (a *phase12PublicOpenAIAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	return a.inner.PlanResponsesContinuation(req)
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

func (a *phase12PublicOpenAIAdapter) requestSummaries() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	summaries := make([]string, 0, len(a.reqs))
	for _, req := range a.reqs {
		diag := req.ContinuationDiagnostic
		if diag == "" && req.Continuation != nil {
			diag = req.Continuation.EndpointFamily + "/" + req.Continuation.StoragePolicyLabel
		}
		summaries = append(summaries, string(req.HistoryMode)+":"+diag)
	}
	return summaries
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

func phase12AssistantMetadataSummaries(history []schema.Turn) []string {
	summaries := []string{}
	for _, turn := range history {
		if turn.Kind != schema.TurnAssistant {
			continue
		}
		summaries = append(summaries,
			"attempt_group="+phase12EmptyMarker(turn.AttemptGroupID)+
				" response_hash="+phase12EmptyMarker(turn.ResponseIDHash)+
				" endpoint="+phase12EmptyMarker(turn.ResponseEndpoint)+
				" request_fp="+phase12EmptyMarker(turn.ResponseRequestFingerprint)+
				" storage_fp="+phase12EmptyMarker(turn.ResponseStorageScopeFingerprint)+
				" context="+phase12EmptyMarker(turn.ResponseContextMarker)+
				" request_model="+phase12EmptyMarker(turn.ResponseRequestModel),
		)
	}
	return summaries
}

func phase12EmptyMarker(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<empty>"
	}
	return value
}

func phase12ReadCanonicalAPILog(t *testing.T, path string) ([]apilog.APIAttemptRecord, []apilog.APIAttemptGroupSettlement) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log %q: %v", path, err)
	}
	defer f.Close() //nolint:errcheck

	var attempts []apilog.APIAttemptRecord
	var settlements []apilog.APIAttemptGroupSettlement
	decoder := apilog.NewDecoder(f, 64<<20)
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decode canonical API log %q: %v", path, err)
		}
		switch record := record.(type) {
		case apilog.APIAttemptRecord:
			attempts = append(attempts, record)
		case apilog.APIAttemptGroupSettlement:
			settlements = append(settlements, record)
		}
	}
	return attempts, settlements
}

func phase12FindCanonicalAttempt(t *testing.T, attempts []apilog.APIAttemptRecord, mode llm.HistoryMode) apilog.APIAttemptRecord {
	t.Helper()
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].Request.HistoryMode == string(mode) && attempts[i].Outcome == apilog.AttemptSuccess {
			return attempts[i]
		}
	}
	t.Fatalf("canonical API log has no successful request for history mode %q; attempts=%+v", mode, attempts)
	return apilog.APIAttemptRecord{}
}

func phase12FindCanonicalSettlement(t *testing.T, settlements []apilog.APIAttemptGroupSettlement, groupID string) apilog.APIAttemptGroupSettlement {
	t.Helper()
	for _, settlement := range settlements {
		if settlement.AttemptGroupID == groupID {
			return settlement
		}
	}
	t.Fatalf("canonical API log has no settlement for group %q; settlements=%+v", groupID, settlements)
	return apilog.APIAttemptGroupSettlement{}
}

func phase12CanonicalRequestBody(t *testing.T, attempt apilog.APIAttemptRecord) []byte {
	t.Helper()
	body, err := apilog.DecodeBody(attempt.Request.Body)
	if err != nil {
		t.Fatalf("decode canonical request body for attempt %q: %v", attempt.AttemptID, err)
	}
	return body
}

func phase12OmittedInputItemBytes(t *testing.T, fullHistoryBody, deltaBody []byte) int {
	t.Helper()
	deltaItems := map[string]int{}
	for _, item := range phase12InputItemJSON(t, deltaBody) {
		deltaItems[item]++
	}
	total := 0
	for _, item := range phase12InputItemJSON(t, fullHistoryBody) {
		if deltaItems[item] > 0 {
			deltaItems[item]--
			continue
		}
		total += len(item)
	}
	return total
}

func phase12InputItemJSON(t *testing.T, body []byte) []string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	items, ok := raw["input"].([]any)
	if !ok {
		t.Fatalf("request body input is not an array: %#v", raw["input"])
	}
	encoded := make([]string, 0, len(items))
	for _, item := range items {
		b, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal input item: %v", err)
		}
		encoded = append(encoded, string(b))
	}
	return encoded
}

func phase12ContinuationOverheadBytes(t *testing.T, body []byte) int {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("parse request body: %v", err)
	}
	if _, ok := raw["previous_response_id"]; !ok {
		t.Fatalf("request body missing previous_response_id")
	}
	withContinuation, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal request body with continuation: %v", err)
	}
	delete(raw, "previous_response_id")
	withoutContinuation, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal request body without continuation: %v", err)
	}
	return len(withContinuation) - len(withoutContinuation)
}
