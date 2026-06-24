# Responses Continuation Phase 8 Full-History Retry Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one same-Responses-endpoint full-history retry for continuation-specific rejections before configured model fallback.

**Architecture:** Keep the recovery in the session model-call fallback harness because Phase 7 deliberately surfaces continuation rejections from the OpenAI adapter. The session retries the same provider/model once with `history_mode=full_history_fallback`, clears `previous_response_id` and continuation metadata, and uses `FullHistoryFallbackMessages` before trying configured model fallbacks. Adapter-level Chat Completions fallback remains unchanged.

**Tech Stack:** Go, deterministic `agent` session tests with `agenttest.ModelTrackingAdapter`, existing `llm.Error` classification fields.

---

## Scope

Phase 8 covers only the test-only delta-shaped/session retry harness required by the Responses continuation spec.

Non-goals:

- Do not enable broad real OpenAI session `responses_delta` selection.
- Do not add same-endpoint retry inside the OpenAI adapter.
- Do not add storage-quota demotion retry.
- Do not add live provider calls.
- Do not change Chat Completions fallback clone behavior.

## File Structure

- Create: `agent/session_model_call_phase8_test.go`
  - Add package `agent` deterministic tests for continuation rejection recovery ordering and non-continuation errors.
- Modify: `agent/session_model_call.go`
  - Add a helper that detects continuation rejection errors from an existing `llm.Request`.
  - Add a helper that clones a delta request into a same-model `full_history_fallback` request.
  - Call the helper before configured model fallback.
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-8-full-history-retry.md`
  - Track checklist progress.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-8.md`
  - Record RED, GREEN, `git diff --check`, and remaining Phase 9 work.

## Required Behavior

Attempt order for a continuation-specific rejection with configured model fallback:

1. Primary model, `history_mode=responses_delta`, with `previous_response_id`.
2. Same primary model, `history_mode=full_history_fallback`, without `previous_response_id`, without continuation metadata, using `FullHistoryFallbackMessages`.
3. Configured fallback model only if the same-model full-history retry also fails.

Non-continuation permanent provider errors must skip the same-model history-mode retry and use the existing configured model fallback path.

---

### Task 1: Add Phase 8 RED Tests

**Files:**
- Create: `agent/session_model_call_phase8_test.go`

- [x] **Step 1: Add continuation retry ordering test**

Add this test near the existing fallback-chain tests:

```go
func TestFallbackChain_ContinuationRejectionRetriesFullHistoryBeforeModelFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{
			"code":    "previous_response_not_found",
			"message": "Previous response not found",
			"type":    "invalid_request_error",
		},
	}, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch len(req.Messages) {
			case 1:
				return llm.Response{}, continuationErr
			case 2:
				if req.Model != "primary" {
					t.Fatalf("same-endpoint recovery model = %q, want primary", req.Model)
				}
				return agenttest.FinalResponse("full-history recovery answered"), nil
			default:
				t.Fatalf("unexpected request messages: %+v", req.Messages)
				return llm.Response{}, nil
			}
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	req := phase8DeltaRequest()
	resp, usedReq, attempt, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), req, "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if resp.Response.Message.Text() != "full-history recovery answered" {
		t.Fatalf("response = %q, want full-history recovery answer", resp.Response.Message.Text())
	}
	if usedReq.HistoryMode != llm.HistoryModeFullHistoryFallback {
		t.Fatalf("used history mode = %q, want %q", usedReq.HistoryMode, llm.HistoryModeFullHistoryFallback)
	}
	if attempt.HistoryMode != llm.HistoryModeFullHistoryFallback {
		t.Fatalf("attempt history mode = %q, want %q", attempt.HistoryMode, llm.HistoryModeFullHistoryFallback)
	}
	requests := f.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].HistoryMode != llm.HistoryModeResponsesDelta || requests[0].PreviousResponseID != "resp_phase8_anchor" {
		t.Fatalf("first request = %+v", requests[0])
	}
	if requests[1].HistoryMode != llm.HistoryModeFullHistoryFallback ||
		requests[1].PreviousResponseID != "" ||
		requests[1].Continuation != nil ||
		requestMessagesContainText(requests[1].Messages, "PHASE8_DELTA_ONLY_MARKER") ||
		!requestMessagesContainText(requests[1].Messages, "PHASE8_FULL_HISTORY_MARKER") {
		t.Fatalf("full-history retry request = %+v", requests[1])
	}
}
```

- [x] **Step 2: Add model fallback ordering test**

Add this test after the first Phase 8 test:

```go
func TestFallbackChain_ContinuationRecoveryFailureThenModelFallback(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	continuationErr := llm.ErrorFromHTTPStatus("openai", 404, "Previous response not found", map[string]any{
		"error": map[string]any{"code": "previous_response_not_found", "message": "Previous response not found"},
	}, nil)
	recoveryErr := llm.ErrorFromHTTPStatus("openai", 403, "recovery denied", nil, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				if req.HistoryMode == llm.HistoryModeResponsesDelta {
					return llm.Response{}, continuationErr
				}
				if req.HistoryMode == llm.HistoryModeFullHistoryFallback {
					return llm.Response{}, recoveryErr
				}
			case "fallback-b":
				return agenttest.FinalResponse("fallback model answered"), nil
			}
			t.Fatalf("unexpected request: model=%q history_mode=%q", req.Model, req.HistoryMode)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	resp, usedReq, _, err := sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), phase8DeltaRequest(), "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	if resp.Response.Message.Text() != "fallback model answered" {
		t.Fatalf("response = %q, want fallback model answer", resp.Response.Message.Text())
	}
	if usedReq.Model != "fallback-b" || usedReq.HistoryMode != llm.HistoryModeFullHistory {
		t.Fatalf("used request = %+v", usedReq)
	}
	got := f.Models()
	want := []string{"primary", "primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}
```

- [x] **Step 3: Add non-continuation guard test and helper**

Add this test and helper:

```go
func TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()

	invalidErr := llm.ErrorFromHTTPStatus("openai", 422, "input item is invalid", map[string]any{
		"error": map[string]any{"code": "invalid_request_error", "message": "input item is invalid"},
	}, nil)

	f := &agenttest.ModelTrackingAdapter{
		Provider: "openai",
		Respond: func(req llm.Request) (llm.Response, error) {
			switch req.Model {
			case "primary":
				return llm.Response{}, invalidErr
			case "fallback-b":
				return agenttest.FinalResponse("fallback model answered"), nil
			}
			t.Fatalf("unexpected model %q", req.Model)
			return llm.Response{}, nil
		},
	}
	c.Register(f)

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("primary"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		LLMRetryPolicy: &policy,
		ModelFallbacks: []string{"fallback-b"},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	_, _, _, err = sess.callModelWithFallback(context.Background(), NewOpenAIProfile("primary"), phase8DeltaRequest(), "", 1)
	if err != nil {
		t.Fatalf("callModelWithFallback: %v", err)
	}
	got := f.Models()
	want := []string{"primary", "fallback-b"}
	if len(got) != len(want) {
		t.Fatalf("models = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("models = %v, want %v", got, want)
		}
	}
}

func phase8DeltaRequest() llm.Request {
	return llm.Request{
		Provider:           "openai",
		Model:              "primary",
		HistoryMode:        llm.HistoryModeResponsesDelta,
		PreviousResponseID: "resp_phase8_anchor",
		Continuation: &llm.ContinuationMetadata{
			PreviousResponseIDHash: "cont-handle-v1:response_id:phase8",
		},
		Messages: []llm.Message{
			llm.User("PHASE8_DELTA_ONLY_MARKER"),
		},
		FullHistoryFallbackMessages: []llm.Message{
			llm.User("PHASE8_FULL_HISTORY_MARKER"),
			llm.Assistant("prior assistant"),
		},
	}
}
```

- [x] **Step 4: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFallbackChain_Continuation|TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry' -count=1 -v
```

Expected: fail because continuation rejection goes directly to configured model fallback and never issues a same-model `full_history_fallback` retry.

---

### Task 2: Implement Same-Model Full-History Retry

**Files:**
- Modify: `agent/session_model_call.go`

- [x] **Step 1: Add helper functions**

Add these helpers near `callModelWithFallback`:

```go
func shouldRetryResponsesContinuationAsFullHistory(req llm.Request, err error) bool {
	if req.HistoryMode != llm.HistoryModeResponsesDelta {
		return false
	}
	if strings.TrimSpace(req.PreviousResponseID) == "" {
		return false
	}
	if len(req.FullHistoryFallbackMessages) == 0 {
		return false
	}
	var llmErr llm.Error
	if !errors.As(err, &llmErr) {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(llmErr.ErrorCode()))
	if code == "previous_response_not_found" {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(llmErr.Error()))
	return (strings.Contains(message, "previous_response") && strings.Contains(message, "not found")) ||
		(strings.Contains(message, "previous response") && (strings.Contains(message, "not found") || strings.Contains(message, "expired")))
}

func responsesContinuationFullHistoryFallbackRequest(req llm.Request) llm.Request {
	fallbackReq := req
	fallbackReq.HistoryMode = llm.HistoryModeFullHistoryFallback
	fallbackReq.Messages = append([]llm.Message(nil), req.FullHistoryFallbackMessages...)
	fallbackReq.PreviousResponseID = ""
	fallbackReq.ConversationID = ""
	fallbackReq.Continuation = nil
	fallbackReq.FullHistoryFallbackMessages = nil
	return fallbackReq
}
```

- [x] **Step 2: Call the helper before model fallback**

In `callModelWithFallback`, immediately after the primary `s.callModel(...)`, insert:

```go
	if err != nil && shouldRetryResponsesContinuationAsFullHistory(req, err) {
		retryReq := responsesContinuationFullHistoryFallbackRequest(req)
		modelResp, err = s.callModel(callCtx, policy, profile, retryReq)
		if err == nil {
			req = retryReq
			attempt.RequestModel = retryReq.Model
			attempt.HistoryMode = llm.HistoryModeFullHistoryFallback
		}
	}
```

The existing configured model fallback block stays after this new block and sees the retry error if same-model recovery failed.

- [x] **Step 3: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFallbackChain_Continuation|TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry|TestFallbackChain_PermanentErrorTriesNextModel|TestFallbackChain_EndpointFallbackErrorTriesNextModel' -count=1 -v
```

- [x] **Step 4: Commit implementation**

```sh
git status --short
git add agent/session_model_call.go agent/session_model_call_phase8_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-8-full-history-retry.md
git commit -m "feat(agent): retry responses continuation with full history"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-8.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-8-full-history-retry.md`

- [ ] **Step 1: Add proof artifact**

Record:

- Phase 7 dependency.
- RED command and failure.
- GREEN command and pass.
- `git diff --check` pass.
- Remaining Phase 9 real-session retry repetition.

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestFallbackChain_Continuation|TestFallbackChain_NonContinuationErrorSkipsFullHistoryRetry|TestFallbackChain_PermanentErrorTriesNextModel|TestFallbackChain_EndpointFallbackErrorTriesNextModel' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-8-full-history-retry.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-8.md
git commit -m "docs: record responses continuation phase 8 proof"
```

## Self-Review

- Spec coverage: covers same-Responses full-history retry before model fallback, non-continuation no-history-switch behavior, and configured model fallback after failed same-model recovery. Phase 9 still owns real session anchor selection and sanitizer repetition.
- Placeholder scan: no placeholder tasks or deferred implementation details.
- Type consistency: plan uses existing `llm.Request`, `llm.Error`, `HistoryModeResponsesDelta`, `HistoryModeFullHistoryFallback`, `FullHistoryFallbackMessages`, `agenttest.ModelTrackingAdapter`, and package-local `callModelWithFallback`.
