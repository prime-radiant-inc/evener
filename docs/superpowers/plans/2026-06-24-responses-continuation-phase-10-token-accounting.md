# Responses Continuation Phase 10 Token Accounting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add deterministic token-pressure accounting and rollout diagnostics for Responses continuation delta, full-history, and fallback paths.

**Architecture:** Compute a local full-history shadow input-token estimate once from the already-expanded full-history request before any delta shaping. Carry that estimate on `llm.Request` into API logs, transcript API-call records, and context-pressure accounting. Reuse the existing transcript doctor API-log summary for aggregate continuation diagnostics by endpoint family.

**Tech Stack:** Go, package-agent deterministic tests, package-llm API-log projection tests, package-doctor transcript-summary tests.

---

## Scope

Phase 10 implements the context/token accounting requirements from `docs/superpowers/specs/2026-06-23-openai-responses-continuation-design.md`:

- `responses_delta` records provider usage for billing through existing response usage fields.
- The shadow full-history estimate is computed from the single full-history expansion for the round, before delta shaping.
- The shadow estimate is not computed from `FullHistoryFallbackMessages` and not from a second history expansion.
- Compaction pressure uses the larger of provider input usage and the local full-history shadow estimate.
- API logs and transcript API-call records expose the dispatched estimate and full-history shadow estimate.
- If the shadow estimate cannot be computed before dispatch, continuation uses `full_history`, sends no delta, and logs `continuation_shadow_estimate_unavailable`.
- Rollout diagnostics expose `responses_delta`, `full_history`, and `full_history_fallback` counts by endpoint family without high-cardinality labels or raw provider handles.

Non-goals:

- Do not use live provider token-counting endpoints in default tests.
- Do not enable production continuation registry defaults.
- Do not implement Phase 11 raw-local export.
- Do not implement Phase 12 rollout activation thresholds.

## File Structure

- Modify: `llm/types.go`
  - Add request-local token estimate fields with `json:"-"`.
- Modify: `llm/apilog.go`
  - Add sanitized API-log request fields for dispatched and full-history shadow estimates plus the continuation accounting diagnostic.
- Modify: `agent/session_config.go`
  - Add a test-only shadow-estimator hook so the unavailable branch is deterministic.
- Modify: `agent/session_model_call.go`
  - Compute shadow estimates before continuation planning and record larger pressure values after responses.
- Modify: `agent/session_lifecycle.go`
  - Pass the final request into `recordResponseUsage`.
- Modify: `agent/session_openai_continuation_phase10_test.go`
  - Add session-level tests for delta shadow estimates, pressure accounting, and unavailable-shadow fallback.
- Modify: `agent/doctor/apilog.go`
  - Add continuation history-mode counts by endpoint family to `APILogTotals`.
- Modify: `agent/doctor/apilog_test.go`
  - Add diagnostic-summary tests.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-10.md`
  - Record proof after implementation.

---

### Task 1: Request and API-Log Estimate Fields

**Files:**
- Modify: `llm/types.go`
- Modify: `llm/apilog.go`
- Modify: `llm/apilog_test.go`

- [x] **Step 1: Add RED API-log projection test**

Add a test in `llm/apilog_test.go`:

```go
func TestBuildAPILogRequest_RecordsContinuationTokenEstimates(t *testing.T) {
	req := Request{
		Model:                         "gpt-5.4",
		Provider:                      "openai",
		HistoryMode:                   HistoryModeResponsesDelta,
		InputTokensEstimate:           42,
		FullHistoryInputTokensEstimate: 420,
		ContinuationDiagnostic:        "continuation_shadow_estimate_unavailable",
	}

	got := BuildAPILogRequest(req)
	if got.InputTokensEstimate != 42 {
		t.Fatalf("InputTokensEstimate = %d, want 42", got.InputTokensEstimate)
	}
	if got.FullHistoryInputTokensEstimate != 420 {
		t.Fatalf("FullHistoryInputTokensEstimate = %d, want 420", got.FullHistoryInputTokensEstimate)
	}
	if got.ContinuationDiagnostic != "continuation_shadow_estimate_unavailable" {
		t.Fatalf("ContinuationDiagnostic = %q", got.ContinuationDiagnostic)
	}
}
```

- [x] **Step 2: Run RED test**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_RecordsContinuationTokenEstimates' -count=1 -v
```

Expected: compile failure because the fields do not exist.

- [x] **Step 3: Add minimal fields**

In `llm/types.go`, add to `Request`:

```go
InputTokensEstimate            int    `json:"-"`
FullHistoryInputTokensEstimate int    `json:"-"`
ContinuationDiagnostic         string `json:"-"`
```

In `llm/apilog.go`, add to `APILogRequest`:

```go
InputTokensEstimate            int    `json:"input_tokens_estimate,omitempty"`
FullHistoryInputTokensEstimate int    `json:"full_history_input_tokens_estimate,omitempty"`
ContinuationDiagnostic         string `json:"continuation_diagnostic,omitempty"`
```

In `BuildAPILogRequest`, copy those fields from `req`.

- [x] **Step 4: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_RecordsContinuationTokenEstimates' -count=1 -v
```

Expected: pass.

- [x] **Step 5: Commit**

```sh
git status --short
git add llm/types.go llm/apilog.go llm/apilog_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md
git commit -m "feat(llm): expose responses continuation token estimates"
```

---

### Task 2: Shadow Estimate Before Delta Shaping

**Files:**
- Modify: `agent/session_config.go`
- Modify: `agent/session_model_call.go`
- Create: `agent/session_openai_continuation_phase10_test.go`

- [x] **Step 1: Add RED delta estimate test**

Create `agent/session_openai_continuation_phase10_test.go` with a fake adapter and this test:

```go
func TestSession_OpenAIResponsesContinuationPhase10DeltaCarriesFullHistoryShadowEstimate(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.HistoryMode != llm.HistoryModeResponsesDelta {
					t.Fatalf("HistoryMode = %q, want responses_delta", req.HistoryMode)
				}
				if req.FullHistoryInputTokensEstimate != 777 {
					t.Fatalf("FullHistoryInputTokensEstimate = %d, want 777", req.FullHistoryInputTokensEstimate)
				}
				if req.InputTokensEstimate <= 0 {
					t.Fatalf("InputTokensEstimate = %d, want positive dispatched estimate", req.InputTokensEstimate)
				}
				if requestMessagesContainText(req.Messages, "phase10 prior user marker") {
					t.Fatalf("delta request included prior marker: %+v", req.Messages)
				}
				if !requestMessagesContainText(req.FullHistoryFallbackMessages, "phase10 prior user marker") {
					t.Fatalf("fallback sidecar missing prior marker: %+v", req.FullHistoryFallbackMessages)
				}
				return agenttest.FinalResponse("phase 10 delta")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)

	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			responsesContinuationShadowEstimateFunc: func(req llm.Request) (int, bool) {
				if requestMessagesContainText(req.Messages, "phase10 prior user marker") &&
					requestMessagesContainText(req.Messages, "phase10 current user marker") {
					return 777, true
				}
				return 0, false
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	anchor := phase9MatchingAnchor("resp_phase10_shadow")
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase10 prior user marker")),
		anchor,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase10 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
}
```

- [x] **Step 2: Run RED test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10DeltaCarriesFullHistoryShadowEstimate' -count=1 -v
```

Expected: compile failure for the test-only hook and missing request fields, or runtime failure because estimates are zero.

- [x] **Step 3: Implement estimate helper**

Add to `testConfig` in `agent/session_config.go`:

```go
responsesContinuationShadowEstimateFunc func(llm.Request) (int, bool)
```

Add helpers in `agent/session_model_call.go`:

```go
func (s *Session) applyResponsesContinuationShadowEstimate(req llm.Request) llm.Request {
	shadowReq := req
	shadowReq.HistoryMode = llm.HistoryModeFullHistory
	shadowReq.PreviousResponseID = ""
	shadowReq.ConversationID = ""
	shadowReq.Continuation = nil
	shadowReq.FullHistoryFallbackMessages = nil
	tokens, ok := s.estimateResponsesContinuationShadow(shadowReq)
	if !ok {
		req.HistoryMode = llm.HistoryModeFullHistory
		req.ContinuationDiagnostic = "continuation_shadow_estimate_unavailable"
		return req
	}
	req.FullHistoryInputTokensEstimate = tokens
	req.InputTokensEstimate = llm.EstimateInputTokens(req).Tokens
	return req
}

func (s *Session) estimateResponsesContinuationShadow(req llm.Request) (int, bool) {
	if s.cfg.testOnly.responsesContinuationShadowEstimateFunc != nil {
		return s.cfg.testOnly.responsesContinuationShadowEstimateFunc(req)
	}
	count := llm.EstimateInputTokens(req)
	return count.Tokens, count.Tokens > 0
}
```

Call `applyResponsesContinuationShadowEstimate` immediately after `buildModelRequest` and before `applyResponsesContinuationAnchorPlanning`.

After delta shaping or full-history fallback planning, set `InputTokensEstimate = llm.EstimateInputTokens(req).Tokens` on the final request before dispatch.

- [x] **Step 4: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10DeltaCarriesFullHistoryShadowEstimate' -count=1 -v
```

Expected: pass.

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/session_config.go agent/session_model_call.go agent/session_openai_continuation_phase10_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md
git commit -m "feat(agent): compute responses continuation shadow estimates"
```

---

### Task 3: Shadow-Unavailable Fallback

**Files:**
- Modify: `agent/session_openai_continuation_phase10_test.go`
- Modify: `agent/session_model_call.go`

- [x] **Step 1: Add unavailable-shadow proof test**

Add:

```go
func TestSession_OpenAIResponsesContinuationPhase10ShadowUnavailableUsesFullHistory(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.HistoryMode != llm.HistoryModeFullHistory {
					t.Fatalf("HistoryMode = %q, want full_history", req.HistoryMode)
				}
				if req.PreviousResponseID != "" {
					t.Fatalf("PreviousResponseID = %q, want empty", req.PreviousResponseID)
				}
				if req.ContinuationDiagnostic != "continuation_shadow_estimate_unavailable" {
					t.Fatalf("ContinuationDiagnostic = %q", req.ContinuationDiagnostic)
				}
				if !requestMessagesContainText(req.Messages, "phase10 prior user marker") ||
					!requestMessagesContainText(req.Messages, "phase10 current user marker") {
					t.Fatalf("full-history request missing markers: %+v", req.Messages)
				}
				return agenttest.FinalResponse("phase 10 full history")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			responsesContinuationShadowEstimateFunc: func(llm.Request) (int, bool) {
				return 0, false
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase10 prior user marker")),
		phase9MatchingAnchor("resp_phase10_unavailable"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase10 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
}
```

- [x] **Step 2: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10ShadowUnavailableUsesFullHistory' -count=1 -v
```

Observed: passed after Task 2 because the short-circuit was implemented with the estimate helper.

- [x] **Step 3: Implement short-circuit**

In `applyResponsesContinuationAnchorPlanning`, before calling `PlanResponsesContinuation`, add:

```go
if req.ContinuationDiagnostic == "continuation_shadow_estimate_unavailable" {
	req.HistoryMode = llm.HistoryModeFullHistory
	return req
}
```

Ensure `PreviousResponseID`, `ConversationID`, `Continuation`, and `FullHistoryFallbackMessages` are empty on that request.

- [x] **Step 4: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10ShadowUnavailableUsesFullHistory' -count=1 -v
```

Expected: pass.

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/session_model_call.go agent/session_openai_continuation_phase10_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md
git commit -m "feat(agent): fall back when continuation shadow estimate is unavailable"
```

---

### Task 4: Context Pressure Uses Larger Input Count

**Files:**
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_model_call.go`
- Modify: `agent/session_openai_continuation_phase10_test.go`

- [x] **Step 1: Add RED pressure test**

Add:

```go
func TestSession_OpenAIResponsesContinuationPhase10PressureUsesFullHistoryShadowWhenLarger(t *testing.T) {
	dir := t.TempDir()
	adapter := &agenttest.FakeAdapter{
		Provider:          "openai",
		CanFallbackToChat: true,
		PlanResponsesContinuationFunc: func(req llm.Request) (llm.ResponsesContinuationPlan, error) {
			return phase4DIContinuationPlan(req), nil
		},
		Steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				resp := agenttest.FinalResponse("phase 10 pressure")
				resp.Usage = llm.Usage{InputTokens: 10, OutputTokens: 1, TotalTokens: 11}
				return resp
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.4"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:                    dir,
		OpenAIResponsesContinuation: "auto",
		testOnly: testConfig{
			responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
				llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport(),
			},
			responsesContinuationShadowEstimateFunc: func(llm.Request) (int, bool) {
				return 900, true
			},
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	drainSessionEvents(sess)
	sess.history = append(sess.history,
		schema.NewTurn(schema.TurnUserInput, llm.User("phase10 prior user marker")),
		phase9MatchingAnchor("resp_phase10_pressure"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "phase10 current user marker", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := sess.contextMgr.LastInputTokens(); got != 900 {
		t.Fatalf("LastInputTokens = %d, want shadow estimate 900", got)
	}
}
```

- [x] **Step 2: Run RED test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10PressureUsesFullHistoryShadowWhenLarger' -count=1 -v
```

Expected: fail because `recordResponseUsage` records provider input usage `10`.

- [x] **Step 3: Pass request into pressure accounting**

Change `recordResponseUsage` to:

```go
func (s *Session) recordResponseUsage(resp llm.Response, req llm.Request)
```

When computing `totalInput`, keep the existing provider usage calculation, then:

```go
if req.FullHistoryInputTokensEstimate > totalInput {
	totalInput = req.FullHistoryInputTokensEstimate
}
```

Update the call site in `processOneInput` to pass the final request returned from `callModelWithFallback`.

- [x] **Step 4: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10PressureUsesFullHistoryShadowWhenLarger' -count=1 -v
```

Expected: pass.

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/session_lifecycle.go agent/session_model_call.go agent/session_openai_continuation_phase10_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md
git commit -m "feat(agent): account continuation shadow pressure"
```

---

### Task 5: Aggregate Continuation Diagnostics

**Files:**
- Modify: `agent/doctor/apilog.go`
- Modify: `agent/doctor/apilog_test.go`

- [x] **Step 1: Add RED doctor summary test**

Add a test in `agent/doctor/apilog_test.go` that builds transcript API calls with:

```go
Request: llm.APILogRequest{
	Provider: "openai",
	Model: "gpt-5.4",
	HistoryMode: llm.HistoryModeResponsesDelta,
	EndpointFamily: "openai_public",
}
```

Also include one `full_history` and one `full_history_fallback` call for `openai_public`.

Assert `APILog(...).Totals.ContinuationByEndpointFamily["openai_public"]` contains:

```go
ResponsesDelta:      1
FullHistory:         1
FullHistoryFallback: 1
```

- [x] **Step 2: Run RED test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent/doctor -run 'TestAPILogContinuationCountsByEndpointFamily' -count=1 -v
```

Expected: compile failure because the summary fields do not exist.

- [x] **Step 3: Add narrow aggregate fields**

In `agent/doctor/apilog.go`, add:

```go
type ContinuationHistoryModeCounts struct {
	ResponsesDelta      int `json:"responses_delta,omitempty"`
	FullHistory         int `json:"full_history,omitempty"`
	FullHistoryFallback int `json:"full_history_fallback,omitempty"`
}
```

Add to `APILogTotals`:

```go
ContinuationByEndpointFamily map[string]ContinuationHistoryModeCounts `json:"continuation_by_endpoint_family,omitempty"`
```

In `APILog`, increment counts using only `call.Request.EndpointFamily` and `call.Request.HistoryMode`. Do not include attempt IDs, response hashes, storage-scope fingerprints, or raw provider handles in the key.

- [x] **Step 4: Run focused test**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent/doctor -run 'TestAPILogContinuationCountsByEndpointFamily' -count=1 -v
```

Expected: pass.

- [x] **Step 5: Commit**

```sh
git status --short
git add agent/doctor/apilog.go agent/doctor/apilog_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md
git commit -m "feat(agent): summarize responses continuation history modes"
```

---

### Task 6: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-10.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md`

- [x] **Step 1: Run full focused Phase 10 verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_RecordsContinuationTokenEstimates' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10|TestSession_OpenAIResponsesContinuationPhase9|TestFallbackChain_Continuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent/doctor -run 'TestAPILogContinuationCountsByEndpointFamily|TestAPILog' -count=1 -v
git diff --check
```

- [x] **Step 2: Add proof artifact**

Record RED/GREEN evidence for each task, list committed hashes, and state that runtime registry entries remain disabled until Phase 12.

- [x] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-10-token-accounting.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-10.md
git commit -m "docs: record responses continuation phase 10 proof"
```

## Self-Review

- Spec coverage: this plan covers Phase 10 context/token accounting, API-log exposure, unavailable-shadow fallback, and aggregate diagnostics. It leaves Phase 11 raw-local export and Phase 12 rollout activation untouched.
- Placeholder scan: no task uses TBD-style placeholders; each implementation task names exact files, tests, commands, and expected outcomes.
- Type consistency: request fields are `InputTokensEstimate`, `FullHistoryInputTokensEstimate`, and `ContinuationDiagnostic`; API-log fields mirror those names. Aggregate diagnostic type is `ContinuationHistoryModeCounts`.
