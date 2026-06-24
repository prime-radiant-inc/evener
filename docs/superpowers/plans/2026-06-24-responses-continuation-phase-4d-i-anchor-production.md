# Responses Continuation Phase 4D-i Anchor Production Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove that a real `Session` can produce a continuation-eligible full-history Responses anchor through an injected enabled registry and a Responses-only fake provider.

**Architecture:** Keep production endpoint-family registry entries disabled and do not send `previous_response_id`. Add the minimum session request-shaping path that, when an injected enabled test registry allows it, plans a full-history anchor request, applies continuation-owned public OpenAI storage, records continuation metadata on the request, and persists the returned assistant response as an eligible future anchor. Add a narrow fallback-capability guard so fallback-capable paths stay `full_history` until Phase 6 supplies `FullHistoryFallbackMessages`.

**Tech Stack:** Go, deterministic `agent` unit tests, existing `llm.ResponsesContinuationPlanner`, existing `llm.ApplyResponsesContinuationStoreOverride`, existing transcript/session metadata.

---

## Dependency Recheck

Phase 4D-i entry requirements are present:

- Phase 3B planner/fingerprint exists through `llm.Client.PlanResponsesContinuation`.
- Phase 4A storage override/scope helpers exist through `llm.ApplyResponsesContinuationStoreOverride` and adapter planner storage metadata.
- Phase 4B context-boundary eligibility exists through `selectResponsesContinuationAnchorCandidate`.
- Phase 4C history-base reservation exists through `reserveResponsesContinuationHistoryBase`.

## File Structure

- Modify: `llm/responses_continuation.go`
  - Add a tiny adapter capability interface for test/future runtime fallback safety.
- Modify: `agent/internal/agenttest/agenttest.go`
  - Let fake adapters opt into `PlanResponsesContinuation`.
  - Let fake adapters declare whether they can fall back to Chat Completions.
- Modify: `agent/session_config.go`
  - Add a test-only/private injected Responses continuation registry field on `SessionConfig`.
- Modify: `agent/session_model_call.go`
  - Add a guarded planning step inside `prepareModelRequest` after `buildModelRequest`.
  - Add private helpers that decide endpoint support, enforce fallback capability, apply storage override, and stamp `llm.ContinuationMetadata`.
- Create: `agent/session_openai_continuation_phase4d_test.go`
  - Prove the Phase 4D-i fake-provider anchor-production slice.
  - Prove fallback-capable paths stay full-history when `FullHistoryFallbackMessages` is absent.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-i.md`
  - Record focused verification and runtime-safety proof.

## Non-Goals

- Do not send `previous_response_id`.
- Do not build or dispatch `responses_delta`.
- Do not enable any production registry entry.
- Do not make live provider requests.
- Do not implement Chat Completions fallback cloning; that belongs to Phase 6.
- Do not implement Phase 4D-ii second-turn delta consumption.

---

### Task 1: Add Phase 4D-i Session Tests

**Files:**
- Create: `agent/session_openai_continuation_phase4d_test.go`
- Modify: `agent/internal/agenttest/agenttest.go`
- Modify: `llm/responses_continuation.go`

- [ ] **Step 1: Extend test fake capabilities**

Add optional fields and methods to `agent/internal/agenttest/agenttest.go`:

```go
	PlanResponsesContinuationFunc func(req llm.Request) (llm.ResponsesContinuationPlan, error)
	CanFallbackToChat            bool
```

Add matching methods to `FakeAdapter`:

```go
func (a *FakeAdapter) PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error) {
	if a.PlanResponsesContinuationFunc == nil {
		return llm.ResponsesContinuationPlan{}, fmt.Errorf("fake adapter missing PlanResponsesContinuationFunc")
	}
	return a.PlanResponsesContinuationFunc(req)
}

func (a *FakeAdapter) CanFallbackFromResponsesToChat() bool {
	return a.CanFallbackToChat
}
```

Add the public optional interface to `llm/responses_continuation.go`:

```go
type ResponsesChatFallbackCapable interface {
	CanFallbackFromResponsesToChat() bool
}
```

- [ ] **Step 2: Write failing anchor-production test**

Create `agent/session_openai_continuation_phase4d_test.go` with:

```go
func TestSession_OpenAIResponsesContinuationPhase4DIProducesStoredFullHistoryAnchor(t *testing.T)
```

The test must:

- register an `agenttest.FakeAdapter` named `openai`;
- configure `OpenAIResponsesContinuation: "auto"`;
- inject an enabled public OpenAI registry with non-zero `MaxAnchorAgeSeconds`;
- have the fake planner return:
  - `EndpointFamily: llm.ResponsesEndpointFamilyOpenAIPublic`;
  - `RequestFingerprint: "cont-req-v1:phase4d"`;
  - `StorageScopeFingerprint: "cont-scope-v1:phase4d"`;
  - `StoragePolicyLabel: llm.ResponsesStoragePolicyPublicOpenAIStore`;
  - `ContinuationStorageAllowed: true`;
- have the fake response return:
  - `ID: "resp_phase4d_anchor"`;
  - `Raw: map[string]any{"endpoint_url": "https://api.openai.com/v1/responses", "id_hash": "cont-handle-v1:response_id:phase4d"}`;
  - a `communicate` tool call so `ProcessInput` completes;
- assert the provider saw one full-history request with:
  - `HistoryMode == llm.HistoryModeFullHistory`;
  - `PreviousResponseID == ""`;
  - `Store == true`;
  - non-nil `Continuation`;
  - `Continuation.EndpointFamily == "openai_public"`;
  - `Continuation.RequestFingerprint == "cont-req-v1:phase4d"`;
  - `Continuation.StorageScopeFingerprint == "cont-scope-v1:phase4d"`;
  - `Continuation.ContextMarker == responseContextMarkerV1`;
  - `Continuation.StoragePolicyLabel == llm.ResponsesStoragePolicyPublicOpenAIStore`;
  - `Continuation.ChatFallbackHistoryLen == 0`;
- assert the appended assistant turn stores:
  - `ResponseID == "resp_phase4d_anchor"`;
  - `ResponseIDHash == "cont-handle-v1:response_id:phase4d"`;
  - `ResponseEndpoint == "https://api.openai.com/v1/responses"`;
  - `ResponseRequestFingerprint == "cont-req-v1:phase4d"`;
  - `ResponseStorageScopeFingerprint == "cont-scope-v1:phase4d"`;
  - `ResponseContextMarker == responseContextMarkerV1`;
  - `ResponseRequestModel == "gpt-5.4"`.

- [ ] **Step 3: Run test to verify it fails**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_OpenAIResponsesContinuationPhase4DIProducesStoredFullHistoryAnchor -count=1 -v
```

Expected: fail because `SessionConfig` cannot inject an enabled continuation registry and `prepareModelRequest` does not stamp continuation metadata.

- [ ] **Step 4: Write failing fallback-capability guard test**

Add:

```go
func TestSession_OpenAIResponsesContinuationPhase4DIFallbackCapablePathUsesFullHistory(t *testing.T)
```

The test must use the same enabled registry and planner but set `CanFallbackToChat: true` on the fake adapter. It must assert the request remains:

- `HistoryMode == llm.HistoryModeFullHistory`;
- `Store == nil` or `Store == false`;
- `Continuation == nil`;
- `PreviousResponseID == ""`.

- [ ] **Step 5: Run fallback guard test to verify it fails**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run TestSession_OpenAIResponsesContinuationPhase4DI -count=1 -v
```

Expected: anchor-production still fails before implementation.

---

### Task 2: Implement Phase 4D-i Anchor Planning

**Files:**
- Modify: `agent/session_config.go`
- Modify: `agent/session_model_call.go`

- [ ] **Step 1: Add private injected registry config**

Add a private field to `SessionConfig`:

```go
responsesContinuationSupportRegistry map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport
```

Keep it `json:"-"` if the surrounding config fields use tags. Do not expose it through public CLI/config restore paths.

- [ ] **Step 2: Add request planning helper**

Add a private helper in `agent/session_model_call.go`:

```go
func (s *Session) applyResponsesContinuationAnchorPlanning(ctx context.Context, req llm.Request, historyTurns []schema.Turn) llm.Request
```

The helper must:

- return the original request unless `OpenAIResponsesContinuation` resolves to `auto`;
- call `s.client.PlanResponsesContinuation(ctx, req)`;
- choose support from the injected registry when present, otherwise from `llm.DefaultResponsesContinuationSupportRegistry()`;
- call `llm.DecideResponsesContinuationForRequest`;
- return full history when the decision is not enabled;
- return full history when the selected adapter reports `CanFallbackFromResponsesToChat() == true` and the request lacks `FullHistoryFallbackMessages`;
- reserve and validate the history base with Phase 4C helpers;
- apply `llm.ApplyResponsesContinuationStoreOverride` using `plan.StoragePolicyLabel`;
- set:

```go
req.HistoryMode = llm.HistoryModeFullHistory
req.Continuation = &llm.ContinuationMetadata{
	EndpointFamily:          string(plan.EndpointFamily),
	RequestFingerprint:      plan.RequestFingerprint,
	ContextMarker:           responseContextMarkerV1,
	StoragePolicyLabel:      plan.StoragePolicyLabel,
	StorageScopeFingerprint: plan.StorageScopeFingerprint,
}
```

Do not set `PreviousResponseID`.

- [ ] **Step 3: Wire helper into `prepareModelRequest`**

After `buildModelRequest`, call:

```go
req = s.applyResponsesContinuationAnchorPlanning(ctx, req, historyTurns)
```

- [ ] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4DI|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Expected: pass.

- [ ] **Step 5: Commit implementation**

```sh
git status --short
git add llm/responses_continuation.go agent/internal/agenttest/agenttest.go agent/session_config.go agent/session_model_call.go agent/session_openai_continuation_phase4d_test.go
git commit -m "feat(agent): prove responses continuation anchor production"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-i.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-4d-i-anchor-production.md`

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-i.md`:

```markdown
# Responses Continuation Phase 4D-i Proof

## Scope

Phase 4D-i proves real-session full-history anchor production through a Responses-only fake provider and an injected enabled registry. Runtime delta dispatch remains disabled.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4DI|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The production default registry remains disabled.
- A test-injected enabled public OpenAI registry can produce a full-history anchor request without `previous_response_id`.
- The full-history anchor request uses continuation-owned `store:true`.
- The assistant turn persists response id, response id hash, endpoint, request fingerprint, storage-scope fingerprint, context marker, and request model.
- Fallback-capable paths without `FullHistoryFallbackMessages` stay full-history with no continuation metadata.
- No live provider calls are made.
```

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4DI|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
git diff --check
```

- [ ] **Step 3: Mark this plan complete and commit**

Update all completed checkboxes in this plan, then:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-4d-i-anchor-production.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-i.md
git commit -m "docs: record responses continuation phase 4d-i proof"
```

## Self-Review

- Spec coverage: covers Phase 4D-i fake-provider anchor production, injected enabled registry, continuation-owned public OpenAI storage, persisted anchor metadata, and the minimum fallback-capability guard.
- Runtime safety: does not send `previous_response_id`, does not build `responses_delta`, does not enable production registries, and does not make live provider requests.
- Test quality: uses a real `Session` and fake provider boundary; assertions inspect structured request and persisted turn fields instead of rendered JSON.
