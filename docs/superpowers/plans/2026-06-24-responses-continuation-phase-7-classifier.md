# Responses Continuation Phase 7 Classifier Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development for each behavior change. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add continuation-error classification and OpenAI Chat fallback ordering for continuation attempts.

**Architecture:** Add a narrow `ResponsesErrorClass` enum and OpenAI classifier. Public OpenAI `previous_response_not_found` from the Phase 0B live proof is classified as `continuation_rejected` only when the request used `PreviousResponseID`. OpenAI model/endpoint mismatch remains `model_endpoint`. The adapter checks this classifier before applying Responses-to-Chat fallback whenever `PreviousResponseID` is present. Empty-stream fallback is disabled for continuation attempts because an empty stream is not a proven model/endpoint incompatibility signal.

**Tech Stack:** Go, deterministic OpenAI adapter unit tests with `httptest`, Phase 0B public OpenAI proof artifact.

---

## Dependency Recheck

Phase 7 depends on:

- Phase 0B proof: public OpenAI invalid anchors returned `previous_response_not_found`; Codex rejected `previous_response_id` and remains blocked.
- Phase 5B fallback attempt recorder.
- Phase 6 Chat fallback clone.
- Existing `llm.ErrorCode()` extraction from structured provider errors.

## File Structure

- Modify: `llm/responses_continuation.go`
  - Add `ResponsesErrorClass` constants.
- Modify: `llm/providers/openai/adapter.go`
  - Add `ClassifyResponsesError`.
  - Apply classification before Chat fallback when `PreviousResponseID` is present.
  - Surface empty-stream continuation attempts instead of falling back.
- Modify: `llm/providers/openai/adapter_test.go`
  - Add classifier fixture tests and fallback-ordering tests.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-7.md`

## Non-Goals

- Do not add same-Responses-endpoint full-history retry; Phase 8 owns it.
- Do not enable real-session delta selection for public OpenAI.
- Do not add Codex classifier fixtures beyond the Phase 0B blocked verdict.
- Do not make live provider calls.

---

### Task 1: Add Phase 7 RED Tests

**Files:**
- Modify: `llm/providers/openai/adapter_test.go`

- [x] **Step 1: Classifier fixtures**

Assert:

- `previous_response_not_found` with `PreviousResponseID` -> `continuation_rejected`;
- generic schema/content invalid request with `PreviousResponseID` -> not continuation rejection;
- `model_not_found` with `PreviousResponseID` -> `model_endpoint`;
- `previous_response_not_found` without `PreviousResponseID` -> not continuation rejection.

- [x] **Step 2: Immediate fallback ordering**

Use an httptest server where `/v1/responses` returns `previous_response_not_found` and `/v1/chat/completions` would succeed. Assert `Stream` returns the Responses error and Chat is not called.

- [x] **Step 3: Model endpoint fallback still works**

Use a request with `PreviousResponseID` and a `model_not_found` Responses error. Assert Chat fallback still runs.

- [x] **Step 4: Empty-stream continuation does not fallback**

Use a request with `PreviousResponseID` and an empty 200 Responses stream. Assert the adapter returns an error and Chat is not called.

- [x] **Step 5: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_ClassifyResponsesError|TestAdapter_Stream_Continuation.*Fallback|TestAdapter_Stream_ModelEndpointContinuationFallback' -count=1 -v
```

Expected: fail because the classifier does not exist and continuation attempts still fall back on endpoint/empty-stream paths.

---

### Task 2: Implement Classifier and Ordering

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/providers/openai/adapter.go`

- [x] **Step 1: Add classifier enum**

Add `ResponsesErrorContinuationRejected`, `ResponsesErrorModelEndpoint`, `ResponsesErrorTransient`, and `ResponsesErrorPermanentOther`.

- [x] **Step 2: Implement OpenAI classifier**

Use structured `llm.ErrorCode()` first. Use substring matching only for the exact continuation relationship if structured data is absent.

- [x] **Step 3: Apply fallback ordering**

When `PreviousResponseID` is present:

- `continuation_rejected`: surface the Responses error;
- `model_endpoint`: allow Chat fallback;
- empty stream: surface the empty-stream error.

- [x] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_ClassifyResponsesError|TestAdapter_Stream_Continuation.*Fallback|TestAdapter_Stream_ModelEndpointContinuationFallback|TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts' -count=1 -v
```

- [x] **Step 5: Commit implementation**

```sh
git status --short
git add llm/responses_continuation.go llm/client.go llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-7-classifier.md
git commit -m "feat(openai): classify responses continuation errors"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-7.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-7-classifier.md`

- [ ] **Step 1: Add proof artifact**

Record Phase 0B dependency, RED, GREEN, `git diff --check`, and remaining Phase 8 retry work.

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_ClassifyResponsesError|TestAdapter_Stream_Continuation.*Fallback|TestAdapter_Stream_ModelEndpointContinuationFallback|TestAdapter_Stream_ChatFallbackUsesFullHistoryFallbackMessages|TestAdapter_Stream_Records.*FallbackAttempts' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit proof**

```sh
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-7-classifier.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-7.md
git commit -m "docs: record responses continuation phase 7 proof"
```
