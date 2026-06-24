# Responses Continuation Phase 4D-ii Delta Consumption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the Phase 4D fake-provider slice so the next real `Session` request consumes the stored Phase 4D-i anchor as a `responses_delta`.

**Architecture:** Keep production endpoint-family registry entries disabled. Reuse the existing session full-history request expansion as the immutable base, then, only for a non-fallback-capable Responses fake behind an injected enabled registry, select a current anchor from session history and replace the provider request messages with system/developer messages plus the local delta turns after the anchor. Real OpenAI adapter paths remain full-history because they can fall back to Chat Completions and Phase 6 has not populated `FullHistoryFallbackMessages`.

**Tech Stack:** Go, deterministic `agent` unit tests, existing `selectResponsesContinuationAnchorCandidate`, existing `llm.Client.PlanResponsesContinuation`, existing continuation storage override helper.

---

## Dependency Recheck

Phase 4D-ii entry requirements are present:

- Phase 4D-i produces persisted anchor metadata on assistant turns.
- Phase 4B anchor candidate selection can identify the latest active eligible assistant and local delta turns.
- Phase 4C history-base reservation exists before anchor selection affects dispatch.
- The Phase 4D-i fallback-capability guard keeps real OpenAI adapter paths full-history until Phase 6.

## File Structure

- Modify: `agent/session_model_call.go`
  - Extend the Phase 4D-i planning helper so it can select `responses_delta` when a current eligible anchor exists.
  - Add small private helpers for delta message shaping and delta metadata.
- Modify: `agent/session_openai_continuation_phase4d_test.go`
  - Add the two-turn fake-provider delta-consumption test.
  - Add a real OpenAI adapter fallback-capable regression with an injected enabled registry.
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-ii.md`
  - Record proof commands and runtime-safety conclusions.

## Non-Goals

- Do not enable any production registry entry.
- Do not add Chat Completions fallback cloning.
- Do not implement continuation rejection classification or retry behavior.
- Do not implement multi-turn delta edge cases beyond the one-new-user-turn fake path.
- Do not make live provider requests.

---

### Task 1: Add Phase 4D-ii RED Tests

**Files:**
- Modify: `agent/session_openai_continuation_phase4d_test.go`

- [ ] **Step 1: Write the fake-provider delta consumption test**

Add:

```go
func TestSession_OpenAIResponsesContinuationPhase4DIIConsumesStoredAnchorAsDelta(t *testing.T)
```

The test should:

- use `agenttest.FakeAdapter` with two steps;
- run first `ProcessInput` to store a Phase 4D-i anchor with `ResponseID=resp_phase4d_anchor`;
- run second `ProcessInput` with a new user marker;
- assert the second request has:
  - `HistoryMode == llm.HistoryModeResponsesDelta`;
  - `PreviousResponseID == "resp_phase4d_anchor"`;
  - `Store == true`;
  - non-nil `Continuation`;
  - `Continuation.PreviousResponseIDHash == "cont-handle-v1:response_id:phase4d"`;
  - `Continuation.AnchorTurnIndex == 1`;
  - `Continuation.DeltaTurnCount == 1`;
  - `Continuation.DeltaTurnKinds == []string{string(schema.TurnUserInput)}`;
  - matching `RequestFingerprint`, `StorageScopeFingerprint`, `EndpointFamily`, `ContextMarker`, and `StoragePolicyLabel`;
- assert the second request messages contain the new user marker and do not contain the prior user marker or the prior assistant response text.

- [ ] **Step 2: Write the real OpenAI fallback-capable regression**

Add:

```go
func TestSession_OpenAIResponsesContinuationPhase4DIIRealOpenAIAdapterUsesFullHistoryUntilFallbackClone(t *testing.T)
```

The test should use `httptest.Server` and `openai.Adapter` with:

- `APIKey: "test-key"`;
- `BaseURL: srv.URL`;
- `Client: srv.Client()`;
- `ContinuationHasher: llm.NewContinuationHasher([]byte("01234567890123456789012345678901"))`.

Seed `sess.history` with:

- prior user marker;
- `responsesContinuationEligibleAssistantTurn("resp_existing_anchor")`;
- current user marker.

Run one `ProcessInput` through an injected enabled public OpenAI registry. Assert the captured `/v1/responses` JSON body:

- omits `previous_response_id`;
- includes both prior and current user markers in `input`;
- keeps `store:false` rather than continuation-owned `store:true`.

- [ ] **Step 3: Run RED tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4DII' -count=1 -v
```

Expected: fail because the second fake-provider request remains `full_history` and does not set `PreviousResponseID`.

---

### Task 2: Implement Delta Request Shaping

**Files:**
- Modify: `agent/session_model_call.go`

- [ ] **Step 1: Extend anchor planning to select candidates**

Inside `applyResponsesContinuationAnchorPlanning`, after the support/fallback/storage checks and reservation validation:

- call `selectResponsesContinuationAnchorCandidate(s.cfg, historyTurns)`;
- if the candidate decision is not `responses_delta`, keep the existing Phase 4D-i full-history anchor behavior;
- if the candidate exists, require:
  - `candidate.Turn.ResponseRequestFingerprint == plan.RequestFingerprint`;
  - `candidate.Turn.ResponseStorageScopeFingerprint == plan.StorageScopeFingerprint`;
  - `candidate.Turn.ResponseContextMarker == responseContextMarkerV1`;
  - non-empty `candidate.Turn.ResponseID`.

- [ ] **Step 2: Add delta message shaping helper**

Add:

```go
func responsesContinuationDeltaMessages(base []llm.Message, deltaTurns []schema.Turn) []llm.Message
```

The helper should preserve leading system/developer messages from `base`, then append `expandHistory(deltaTurns)`.

- [ ] **Step 3: Set delta request fields**

For an eligible candidate, apply storage override, then set:

```go
req.HistoryMode = llm.HistoryModeResponsesDelta
req.PreviousResponseID = strings.TrimSpace(candidate.Turn.ResponseID)
req.Messages = responsesContinuationDeltaMessages(req.Messages, candidate.Delta)
req.Continuation = &llm.ContinuationMetadata{
	PreviousResponseIDHash:  candidate.Turn.ResponseIDHash,
	AnchorTurnIndex:         candidate.TurnIndex,
	DeltaTurnCount:          len(candidate.Delta),
	DeltaTurnKinds:          responsesContinuationTurnKinds(candidate.Delta),
	EndpointFamily:          string(plan.EndpointFamily),
	RequestFingerprint:      plan.RequestFingerprint,
	ContextMarker:           responseContextMarkerV1,
	StoragePolicyLabel:      plan.StoragePolicyLabel,
	StorageScopeFingerprint: plan.StorageScopeFingerprint,
}
```

- [ ] **Step 4: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4D|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Expected: pass.

- [ ] **Step 5: Commit implementation**

```sh
git status --short
git add agent/session_model_call.go agent/session_openai_continuation_phase4d_test.go
git commit -m "feat(agent): consume responses continuation anchors in fake slice"
```

---

### Task 3: Proof and Verification

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-ii.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-4d-ii-delta-consumption.md`

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-ii.md`:

```markdown
# Responses Continuation Phase 4D-ii Proof

## Scope

Phase 4D-ii proves real-session second-turn delta consumption through a Responses-only fake provider and an injected enabled registry. Production runtime enablement remains disabled.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4D|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- The fake-provider second request uses `responses_delta`.
- The delta request sends `previous_response_id`.
- The delta request input contains only the new local user turn plus the system/developer prompt, not pre-anchor local history.
- Delta metadata includes previous-response hash, anchor index, delta count, delta turn kind, endpoint family, request fingerprint, storage-scope fingerprint, context marker, and storage policy.
- Real OpenAI adapter paths remain full-history while Chat Completions fallback cloning is absent.
- No production registry entry is enabled and no live provider calls are made.
```

- [ ] **Step 2: Run verification**

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase4D|TestSession_OpenAIResponsesContinuationDisabledUsesFullHistory|TestResponsesContinuationAnchorCandidate|TestResponsesContinuationHistoryReservation' -count=1 -v
git diff --check
```

- [ ] **Step 3: Mark this plan complete and commit**

Update all completed checkboxes in this plan, then:

```sh
git status --short
git add docs/superpowers/plans/2026-06-24-responses-continuation-phase-4d-ii-delta-consumption.md docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4d-ii.md
git commit -m "docs: record responses continuation phase 4d-ii proof"
```

## Self-Review

- Spec coverage: covers Phase 4D-ii fake-provider delta consumption, previous-response id dispatch, delta-only local input, metadata matching, and real OpenAI fallback-capable full-history behavior.
- Runtime safety: production endpoint-family entries remain disabled; live providers are not called.
- Test quality: tests inspect structured `llm.Request` fields for fake-provider behavior and actual OpenAI adapter JSON for the fallback-capable regression.
