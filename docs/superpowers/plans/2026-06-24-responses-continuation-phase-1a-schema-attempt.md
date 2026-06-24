# Responses Continuation Phase 1A Schema And Attempt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add optional continuation schema and single-attempt metadata plumbing without enabling Responses continuation at runtime.

**Architecture:** This phase is schema and metadata only. It adds typed `history_mode`, redacted provider-handle fields, and the single-attempt metadata path used by later retry/fallback phases, but it must not select `responses_delta`, enable any endpoint-family registry entry, generate HMACs, or add attempt-group logging. `attempt_group_id` and adapter fallback attempt records remain Phase 5A work.

**Tech Stack:** Go structs and deterministic tests in `llm`, `agent/schema`, `agent/transcript`, and `agent`.

---

## File Structure

- Modify `llm/responses_continuation.go`
  - Add `ContinuationMetadata`.
- Modify `llm/types.go`
  - Add out-of-band request control-plane fields: `HistoryMode`, `Continuation`, `FullHistoryFallbackMessages`.
- Modify `llm/apilog.go`
  - Add optional single-attempt/history/provider-handle fields to `APILogContext`, `APILogEntry`, `APILogRequest`, and `APILogResponse`.
  - Add `WithAPILogAttemptContext`.
- Modify `llm/apilog_test.go`
  - Add serialization/projection tests for continuation metadata.
- Modify `agent/schema/turn.go`
  - Add optional assistant response metadata fields.
- Modify `agent/transcript/transcript.go`
  - Add optional single-attempt/history/provider-handle fields to transcript `APICall`.
- Modify `agent/transcript_test.go`
  - Add round-trip tests for optional turn and `api_call` metadata.
- Modify `agent/session_model_call.go`
  - Add `ModelAttemptMetadata`.
  - Stamp the existing no-retry/no-continuation dispatch path as attempt index/count 1.
  - Thread single-attempt metadata into transcript API-call logging.
- Modify `agent/session_lifecycle.go`
  - Pass final attempt metadata into assistant persistence.
- Modify `agent/session.go`
  - Change `appendAssistantTurn` to require final attempt metadata and persist optional response metadata.
- Modify `agent/session_test.go`
  - Add one focused real-session scripted-provider test proving attempt and assistant metadata are written.
- Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1a.md`
  - Record schema compatibility and append-assistant call-site audit.

## Task 1: LLM Control Plane And API Log Schema

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/types.go`
- Modify: `llm/apilog.go`
- Modify: `llm/apilog_test.go`

- [ ] **Step 1: Add failing API-log schema tests**

Append these tests to `llm/apilog_test.go`:

```go
func TestBuildAPILogRequest_IncludesContinuationMetadata(t *testing.T) {
	req := Request{
		Model:       "gpt-5.2",
		Provider:    "openai",
		Messages:    []Message{User("hi")},
		HistoryMode: HistoryModeResponsesDelta,
		Continuation: &ContinuationMetadata{
			PreviousResponseIDHash:  "cont-handle-v1:response_id:abc",
			ConversationIDHash:      "cont-handle-v1:conversation_id:def",
			AnchorTurnIndex:         3,
			DeltaTurnCount:          1,
			DeltaTurnKinds:          []string{"TOOL_RESULTS"},
			EndpointFamily:          string(ResponsesEndpointFamilyOpenAIPublic),
			RequestFingerprint:      "cont-req-v1:abc",
			ContextMarker:           "cont-ctx-v1",
			StoragePolicyLabel:      "public-openai-store",
			StorageScopeFingerprint: "cont-scope-v1:abc",
			ChatFallbackHistoryLen:  7,
		},
	}

	got := BuildAPILogRequest(req)

	if got.HistoryMode != HistoryModeResponsesDelta {
		t.Fatalf("HistoryMode = %q", got.HistoryMode)
	}
	if got.PreviousResponseIDHash != "cont-handle-v1:response_id:abc" ||
		got.ConversationIDHash != "cont-handle-v1:conversation_id:def" ||
		got.AnchorTurnIndex != 3 ||
		got.DeltaTurnCount != 1 ||
		got.ChatFallbackHistoryLen != 7 {
		t.Fatalf("continuation counters/handles not copied: %+v", got)
	}
	if got.EndpointFamily != string(ResponsesEndpointFamilyOpenAIPublic) ||
		got.RequestFingerprint != "cont-req-v1:abc" ||
		got.ContextMarker != "cont-ctx-v1" ||
		got.StoragePolicyLabel != "public-openai-store" ||
		got.StorageScopeFingerprint != "cont-scope-v1:abc" {
		t.Fatalf("continuation metadata not copied: %+v", got)
	}
	if len(got.DeltaTurnKinds) != 1 || got.DeltaTurnKinds[0] != "TOOL_RESULTS" {
		t.Fatalf("DeltaTurnKinds = %#v", got.DeltaTurnKinds)
	}
}

func TestAPILogEntry_AttemptFieldsRoundTrip(t *testing.T) {
	finalCount := 1
	entry := APILogEntry{
		SessionID:         "sess",
		Round:             2,
		AttemptIndex:      1,
		AttemptCount:      1,
		FinalAttemptCount: &finalCount,
		HistoryMode:       HistoryModeFullHistory,
		Request: APILogRequest{
			Model:       "gpt-5.2",
			Provider:    "openai",
			HistoryMode: HistoryModeFullHistory,
		},
		Response: &APILogResponse{
			ID:     "resp_raw_local",
			IDHash: "cont-handle-v1:response_id:abc",
			Model:  "gpt-5.2",
		},
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got APILogEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.AttemptIndex != 1 || got.AttemptCount != 1 || got.HistoryMode != HistoryModeFullHistory {
		t.Fatalf("attempt fields = %+v", got)
	}
	if got.FinalAttemptCount == nil || *got.FinalAttemptCount != 1 {
		t.Fatalf("FinalAttemptCount = %v", got.FinalAttemptCount)
	}
	if got.Response == nil || got.Response.IDHash != "cont-handle-v1:response_id:abc" {
		t.Fatalf("response = %+v", got.Response)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_IncludesContinuationMetadata|TestAPILogEntry_AttemptFieldsRoundTrip' -count=1 -v
```

Expected: FAIL with missing fields/types.

- [ ] **Step 3: Add control-plane types and API-log fields**

In `llm/responses_continuation.go`, add:

```go
type ContinuationMetadata struct {
	PreviousResponseIDHash  string
	ConversationIDHash      string
	AnchorTurnIndex         int
	DeltaTurnCount          int
	DeltaTurnKinds          []string
	EndpointFamily          string
	RequestFingerprint      string
	ContextMarker           string
	StoragePolicyLabel      string
	StorageScopeFingerprint string
	ChatFallbackHistoryLen  int
}
```

In `llm/types.go`, add these fields near the end of `Request`, before `AdapterTimeout`:

```go
	HistoryMode                 HistoryMode           `json:"-"`
	Continuation                *ContinuationMetadata `json:"-"`
	FullHistoryFallbackMessages []Message             `json:"-"`
```

In `llm/apilog.go`, extend `APILogContext`:

```go
	AttemptIndex      int
	AttemptCount      int
	FinalAttemptCount *int
	HistoryMode       HistoryMode
```

Add:

```go
func WithAPILogAttemptContext(ctx context.Context, meta APILogContext) context.Context {
	if existing, ok := getAPILogContext(ctx); ok {
		if meta.SessionID == "" {
			meta.SessionID = existing.SessionID
		}
		if meta.Round == 0 {
			meta.Round = existing.Round
		}
	}
	return context.WithValue(ctx, apiLogKey{}, meta)
}
```

Extend `APILogEntry`:

```go
	AttemptIndex      int         `json:"attempt_index,omitempty"`
	AttemptCount      int         `json:"attempt_count,omitempty"`
	FinalAttemptCount *int        `json:"final_attempt_count,omitempty"`
	HistoryMode       HistoryMode `json:"history_mode,omitempty"`
```

Extend `APILogRequest`:

```go
	HistoryMode              HistoryMode `json:"history_mode,omitempty"`
	PreviousResponseIDHash   string      `json:"previous_response_id_hash,omitempty"`
	ConversationIDHash       string      `json:"conversation_id_hash,omitempty"`
	AnchorTurnIndex          int         `json:"anchor_turn_index,omitempty"`
	DeltaTurnCount           int         `json:"delta_turn_count,omitempty"`
	DeltaTurnKinds           []string    `json:"delta_turn_kinds,omitempty"`
	EndpointFamily           string      `json:"endpoint_family,omitempty"`
	RequestFingerprint       string      `json:"request_fingerprint,omitempty"`
	ContextMarker            string      `json:"context_marker,omitempty"`
	StoragePolicyLabel       string      `json:"storage_policy_label,omitempty"`
	StorageScopeFingerprint  string      `json:"storage_scope_fingerprint,omitempty"`
	ChatFallbackHistoryLen   int         `json:"chat_fallback_history_len,omitempty"`
```

Extend `APILogResponse`:

```go
	IDHash string `json:"id_hash,omitempty"`
```

In `buildAPILogEntry`, copy `AttemptIndex`, `AttemptCount`, `FinalAttemptCount`, and `HistoryMode` from `APILogContext`.

In `BuildAPILogRequest`, copy `req.HistoryMode`. When `req.Continuation != nil`, copy every continuation field into the request log and copy `DeltaTurnKinds` with `append([]string(nil), ...)`.

In `buildLogResponse`, set `IDHash` from `resp.Raw["id_hash"]` only when the value is a string. Phase 1B will provide real hashes; this phase only makes the field round-trip.

- [ ] **Step 4: Run focused tests**

Run:

```sh
gofmt -w llm/responses_continuation.go llm/types.go llm/apilog.go llm/apilog_test.go
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_IncludesContinuationMetadata|TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILoggerWritesJSONL|TestAPILoggerWrapStreamWritesRawLogOnFinish' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```sh
git status --short
git add llm/responses_continuation.go llm/types.go llm/apilog.go llm/apilog_test.go
git commit -m "feat(llm): add continuation request and api log schema" -m "Add optional Responses continuation control-plane metadata to llm.Request and API log records. This is schema-only: fields default empty, no endpoint registry entry is enabled, and no runtime delta path is selected."
```

## Task 2: Transcript Turn And API Call Schema

**Files:**
- Modify: `agent/schema/turn.go`
- Modify: `agent/transcript/transcript.go`
- Modify: `agent/transcript_test.go`

- [ ] **Step 1: Add failing transcript round-trip test**

Append this test to `agent/transcript_test.go` near the existing `APICall` tests:

```go
func TestTranscriptContinuationMetadataRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "sess"})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	finalCount := 1
	turn := schema.Turn{
		Kind:                            schema.TurnAssistant,
		Message:                         llm.Assistant("ok"),
		Timestamp:                       time.Unix(1, 0).UTC(),
		ResponseID:                      "resp_raw_local",
		ResponseIDHash:                  "cont-handle-v1:response_id:abc",
		ResponseProvider:                "openai",
		ResponseModel:                   "gpt-5.2",
		ResponseRequestModel:            "gpt-5.2",
		ResponseEndpoint:                "https://api.openai.com/v1/responses",
		ResponseStorageScopeFingerprint: "cont-scope-v1:abc",
		ResponseRequestFingerprint:      "cont-req-v1:abc",
		ResponseContextMarker:           "cont-ctx-v1",
	}
	if err := w.Append(turn); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.AppendAPICall(transcript.APICall{
		Round:                  1,
		AttemptIndex:           1,
		AttemptCount:           1,
		FinalAttemptCount:      &finalCount,
		HistoryMode:            llm.HistoryModeFullHistory,
		PreviousResponseIDHash: "cont-handle-v1:previous_response_id:def",
		ConversationIDHash:     "cont-handle-v1:conversation_id:ghi",
		Request: llm.APILogRequest{
			Model:       "gpt-5.2",
			Provider:    "openai",
			HistoryMode: llm.HistoryModeFullHistory,
		},
		Response: &llm.APILogResponse{
			ID:     "resp_raw_local",
			IDHash: "cont-handle-v1:response_id:abc",
			Model:  "gpt-5.2",
		},
	}); err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if len(data.Entries) != 1 || len(data.APICalls) != 1 {
		t.Fatalf("entries/api_calls = %d/%d", len(data.Entries), len(data.APICalls))
	}

	gotTurn := data.Entries[0].Turn
	if gotTurn.ResponseIDHash != "cont-handle-v1:response_id:abc" ||
		gotTurn.ResponseContextMarker != "cont-ctx-v1" ||
		gotTurn.ResponseRequestFingerprint != "cont-req-v1:abc" {
		t.Fatalf("turn metadata = %+v", gotTurn)
	}

	gotCall := data.APICalls[0]
	if gotCall.AttemptIndex != 1 ||
		gotCall.AttemptCount != 1 ||
		gotCall.HistoryMode != llm.HistoryModeFullHistory ||
		gotCall.PreviousResponseIDHash != "cont-handle-v1:previous_response_id:def" ||
		gotCall.ConversationIDHash != "cont-handle-v1:conversation_id:ghi" {
		t.Fatalf("api_call metadata = %+v", gotCall)
	}
	if gotCall.FinalAttemptCount == nil || *gotCall.FinalAttemptCount != 1 {
		t.Fatalf("FinalAttemptCount = %v", gotCall.FinalAttemptCount)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestTranscriptContinuationMetadataRoundTrips -count=1 -v
```

Expected: FAIL with missing fields.

- [ ] **Step 3: Add optional transcript fields**

In `agent/schema/turn.go`, extend `Turn` after `ResponseID`:

```go
	ResponseIDHash                  string `json:"response_id_hash,omitempty"`
	ResponseProvider                string `json:"response_provider,omitempty"`
	ResponseModel                   string `json:"response_model,omitempty"`
	ResponseRequestModel            string `json:"response_request_model,omitempty"`
	ResponseEndpoint                string `json:"response_endpoint,omitempty"`
	ResponseStorageScopeFingerprint string `json:"response_storage_scope_fingerprint,omitempty"`
	ResponseRequestFingerprint      string `json:"response_request_fingerprint,omitempty"`
	ResponseContextMarker           string `json:"response_context_marker,omitempty"`
```

In `agent/transcript/transcript.go`, extend `APICall` after `Round`:

```go
	AttemptIndex           int             `json:"attempt_index,omitempty"`
	AttemptCount           int             `json:"attempt_count,omitempty"`
	FinalAttemptCount      *int            `json:"final_attempt_count,omitempty"`
	HistoryMode            llm.HistoryMode `json:"history_mode,omitempty"`
	PreviousResponseIDHash string          `json:"previous_response_id_hash,omitempty"`
	ConversationIDHash     string          `json:"conversation_id_hash,omitempty"`
```

- [ ] **Step 4: Run focused tests**

Run:

```sh
gofmt -w agent/schema/turn.go agent/transcript/transcript.go agent/transcript_test.go
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestTranscriptContinuationMetadataRoundTrips|TestTranscriptWriter_AppendAPICallWritesValidLine|TestReadTranscriptFull_ParsesAllLineTypes' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

Run:

```sh
git status --short
git add agent/schema/turn.go agent/transcript/transcript.go agent/transcript_test.go
git commit -m "feat(agent): add continuation transcript schema fields" -m "Add optional assistant-turn and transcript api_call metadata fields for Responses continuation. Existing transcripts remain readable because every new field is omitempty and default-empty."
```

## Task 3: Single-Attempt Metadata Thread

**Files:**
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session.go`
- Modify: `agent/session_test.go`

- [ ] **Step 1: Add failing session metadata test**

Append this test to `agent/session_test.go` near the existing transcript/API-call session tests:

```go
func TestSession_SingleAttemptMetadataRecorded(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient(streamingAdapter{
		completeFunc: func(ctx context.Context, req llm.Request) (llm.Response, error) {
			if req.HistoryMode != llm.HistoryModeFullHistory {
				t.Fatalf("HistoryMode = %q, want full_history", req.HistoryMode)
			}
			return llm.Response{
				ID:       "resp_1",
				Provider: "openai",
				Model:    req.Model,
				Message:  llm.Assistant("ok"),
				Finish:   llm.FinishReason{Reason: "stop"},
				Raw:      map[string]any{"endpoint_url": "https://api.openai.com/v1/responses"},
			}, nil
		},
	})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(context.Background(), "hi"); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	data, err := readTranscriptFull(sess.TranscriptPath())
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if len(data.APICalls) != 1 {
		t.Fatalf("api_calls = %d", len(data.APICalls))
	}
	call := data.APICalls[0]
	if call.AttemptIndex != 1 || call.AttemptCount != 1 {
		t.Fatalf("attempt fields = %+v", call)
	}
	if call.FinalAttemptCount == nil || *call.FinalAttemptCount != 1 {
		t.Fatalf("FinalAttemptCount = %v", call.FinalAttemptCount)
	}
	if call.HistoryMode != llm.HistoryModeFullHistory || call.Request.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("history modes = api_call:%q request:%q", call.HistoryMode, call.Request.HistoryMode)
	}

	if len(data.Entries) == 0 {
		t.Fatalf("no transcript entries")
	}
	assistant := data.Entries[len(data.Entries)-1].Turn
	if assistant.ResponseID != "resp_1" {
		t.Fatalf("ResponseID = %q", assistant.ResponseID)
	}
	if assistant.ResponseProvider != "openai" ||
		assistant.ResponseModel != "gpt-5.2" ||
		assistant.ResponseRequestModel != "gpt-5.2" ||
		assistant.ResponseEndpoint != "https://api.openai.com/v1/responses" {
		t.Fatalf("assistant response metadata = %+v", assistant)
	}
	if assistant.ResponseIDHash != "" ||
		assistant.ResponseContextMarker != "" ||
		assistant.ResponseRequestFingerprint != "" ||
		assistant.ResponseStorageScopeFingerprint != "" {
		t.Fatalf("anchor eligibility metadata should stay empty in Phase 1A: %+v", assistant)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_SingleAttemptMetadataRecorded -count=1 -v
```

Expected: FAIL because history mode, attempt fields, and response metadata are not threaded.

- [ ] **Step 3: Add `ModelAttemptMetadata` and single-attempt helpers**

In `agent/session_model_call.go`, add:

```go
type ModelAttemptMetadata struct {
	HistoryMode             llm.HistoryMode
	EndpointFamily          string
	EndpointURL             string
	RequestModel            string
	RequestFingerprint      string
	StorageScopeFingerprint string
	ContextMarker           string
	AttemptIndex            int
	AttemptCount            int
	FinalAttemptCount       *int
	PreviousResponseIDHash  string
	ConversationIDHash      string
	ResponseIDHash          string
	StoragePolicyLabel      string
}

func singleAttemptRequestMetadata(req llm.Request) (llm.Request, ModelAttemptMetadata) {
	if req.HistoryMode == "" {
		req.HistoryMode = llm.HistoryModeFullHistory
	}
	finalCount := 1
	meta := ModelAttemptMetadata{
		HistoryMode:       req.HistoryMode,
		RequestModel:      req.Model,
		AttemptIndex:      1,
		AttemptCount:      1,
		FinalAttemptCount: &finalCount,
	}
	if req.Continuation != nil {
		meta.EndpointFamily = req.Continuation.EndpointFamily
		meta.RequestFingerprint = req.Continuation.RequestFingerprint
		meta.StorageScopeFingerprint = req.Continuation.StorageScopeFingerprint
		meta.ContextMarker = req.Continuation.ContextMarker
		meta.PreviousResponseIDHash = req.Continuation.PreviousResponseIDHash
		meta.ConversationIDHash = req.Continuation.ConversationIDHash
		meta.StoragePolicyLabel = req.Continuation.StoragePolicyLabel
	}
	return req, meta
}

func completeAttemptMetadata(meta ModelAttemptMetadata, resp llm.Response) ModelAttemptMetadata {
	if resp.Raw != nil {
		if endpoint, _ := resp.Raw["endpoint_url"].(string); endpoint != "" {
			meta.EndpointURL = endpoint
		}
		if idHash, _ := resp.Raw["id_hash"].(string); idHash != "" {
			meta.ResponseIDHash = idHash
		}
	}
	return meta
}
```

- [ ] **Step 4: Thread metadata through model calls, API logging, and assistant append**

In `callModelWithFallback`, call `singleAttemptRequestMetadata(req)` before the first model call. Replace `llm.WithAPILogContext(ctx, s.id, round)` with `llm.WithAPILogAttemptContext` using the single-attempt metadata:

```go
	req, attempt := singleAttemptRequestMetadata(req)
	callCtx := llm.WithAPILogAttemptContext(ctx, llm.APILogContext{
		SessionID:         s.id,
		Round:             round,
		AttemptIndex:      attempt.AttemptIndex,
		AttemptCount:      attempt.AttemptCount,
		FinalAttemptCount: attempt.FinalAttemptCount,
		HistoryMode:       attempt.HistoryMode,
	})
```

When a configured model fallback succeeds in the existing fallback loop, keep the phase narrow: update `req = fbReq` as today, set `attempt.RequestModel = fbReq.Model`, and set `attempt.HistoryMode = llm.HistoryModeFullHistory`. Phase 5A owns separate attempt records for adapter/model fallback; Phase 1A only preserves the current single final-response path.

Change the return signature to:

```go
func (s *Session) callModelWithFallback(ctx context.Context, profile *provider.Profile, req llm.Request, requestedEffort string, round int) (sessionModelResponse, llm.Request, ModelAttemptMetadata, error)
```

Return `completeAttemptMetadata(attempt, modelResp.Response)` on success. Return the request-level `attempt` on error.

Change `logAPICall` signature to:

```go
func (s *Session) logAPICall(round int, roundStart time.Time, llmLatency time.Duration, sys string, historyLen int, req llm.Request, resp llm.Response, err error, attempt ModelAttemptMetadata)
```

Populate transcript `APICall` fields from `attempt`: `AttemptIndex`, `AttemptCount`, `FinalAttemptCount`, `HistoryMode`, `PreviousResponseIDHash`, and `ConversationIDHash`.

Change `appendAssistantTurn` signature to:

```go
func (s *Session) appendAssistantTurn(resp llm.Response, finalAttempt ModelAttemptMetadata)
```

Keep `ResponseID: resp.ID` for compatibility. Populate new assistant metadata fields from `resp` and `finalAttempt`:

```go
ResponseIDHash:                  finalAttempt.ResponseIDHash,
ResponseProvider:                resp.Provider,
ResponseModel:                   resp.Model,
ResponseRequestModel:            finalAttempt.RequestModel,
ResponseEndpoint:                finalAttempt.EndpointURL,
ResponseStorageScopeFingerprint: finalAttempt.StorageScopeFingerprint,
ResponseRequestFingerprint:      finalAttempt.RequestFingerprint,
ResponseContextMarker:           finalAttempt.ContextMarker,
```

Change `emitAssistantResponse` to accept `finalAttempt ModelAttemptMetadata` and pass it to `appendAssistantTurn`.

- [ ] **Step 5: Run focused tests**

Run:

```sh
gofmt -w agent/session_model_call.go agent/session_lifecycle.go agent/session.go agent/session_test.go
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_SingleAttemptMetadataRecorded|TestAssistantTurn_CapturesUsageAndResponseID|TestSession_TranscriptAPICallRecordsFullToolDefinitions' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

Run:

```sh
git status --short
git add agent/session_model_call.go agent/session_lifecycle.go agent/session.go agent/session_test.go
git commit -m "feat(agent): thread single-attempt response metadata" -m "Add the Phase 1A single-attempt metadata thread through transcript api_call logging and assistant-turn persistence. This preserves existing ResponseID behavior while keeping anchor-eligibility fields empty until later phases provide fingerprints, storage scopes, and redacted handle hashes."
```

## Task 4: Phase 1A Proof And Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1a.md`

- [ ] **Step 1: Audit append-assistant call sites**

Run:

```sh
rg -n "appendAssistantTurn\\(" agent --glob '*.go'
```

Expected: every call passes `ModelAttemptMetadata`; direct calls in tests either pass an explicit metadata value or use the session path.

- [ ] **Step 2: Create proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1a.md`:

```markdown
# Responses Continuation Phase 1A Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 1A-schema and Phase 1A-attempt

## Schema Compatibility

Checkable line: continuation schema fields are optional and old transcripts/API logs without them remain readable.

Evidence:
- `agent/transcript_test.go:TestTranscriptContinuationMetadataRoundTrips`
- existing transcript read/write tests run in Phase 1A verification.

Verdict: old records are non-anchorable by default because anchor-eligibility fields are absent.

## Request And API Log Control Plane

Checkable line: `llm.Request` carries continuation control-plane metadata out-of-band, and `BuildAPILogRequest` projects only structured/redacted provider-state metadata.

Evidence:
- `llm/apilog_test.go:TestBuildAPILogRequest_IncludesContinuationMetadata`
- `llm/apilog_test.go:TestAPILogEntry_AttemptFieldsRoundTrip`

Verdict: Phase 1A adds schema and projection only; it does not generate hashes, add attempt groups, or select `responses_delta`.

## Single-Attempt Metadata

Checkable line: ordinary single-attempt model calls stamp `AttemptIndex=1`, `AttemptCount=1`, `FinalAttemptCount=1`, `HistoryMode=full_history`, and assistant turns receive response metadata from the successful final attempt.

Evidence:
- `agent/session_test.go:TestSession_SingleAttemptMetadataRecorded`

Append-assistant audit:
- `agent/session_lifecycle.go:emitAssistantResponse` is the session path that calls `appendAssistantTurn`.
- `appendAssistantTurn` requires `ModelAttemptMetadata`.

Verdict: `attempt_group_id`, adapter fallback records, retry/fallback classifiers, HMAC generation, and real `responses_delta` attempts remain in later phases.
```

- [ ] **Step 3: Run verification**

Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_IncludesContinuationMetadata|TestAPILogEntry_AttemptFieldsRoundTrip|TestAPILoggerWritesJSONL|TestAPILoggerWrapStreamWritesRawLogOnFinish' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestTranscriptContinuationMetadataRoundTrips|TestSession_SingleAttemptMetadataRecorded|TestAssistantTurn_CapturesUsageAndResponseID|TestSession_TranscriptAPICallRecordsFullToolDefinitions' -count=1 -v
rg -n 'DefaultResponsesContinuationSupportRegistry|ResponsesContinuationAuto|HistoryModeResponsesDelta' --glob '*.go'
git diff --check
```

Expected:
- Tests pass.
- Registry remains production-disabled.
- `HistoryModeResponsesDelta` appears only in type definitions/tests; session runtime does not select it.
- `git diff --check` reports no whitespace errors.

- [ ] **Step 4: Commit proof**

Run:

```sh
git status --short
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1a.md
git commit -m "docs: record responses continuation phase 1a proof" -m "Record Phase 1A schema compatibility, request/API-log control-plane projection, single-attempt metadata stamping, and the appendAssistantTurn call-site audit. Runtime continuation remains disabled."
```

## Self-Review Notes

- Phase 1A does not implement HMAC generation; `ResponseIDHash`, `PreviousResponseIDHash`, and `ConversationIDHash` are schema/projection fields only.
- Phase 1A does not select `responses_delta`.
- Phase 1A does not change public OpenAI from `store:false` to `store:true`.
- Phase 1A does not enable public OpenAI or Codex endpoint-family registry entries.
- Phase 1A does not add `attempt_group_id`; Phase 5A owns attempt-group identity and adapter fallback attempt records.
- Explicit `ConversationID` remains a full-history-only V1 path per the Phase 0B proof.
