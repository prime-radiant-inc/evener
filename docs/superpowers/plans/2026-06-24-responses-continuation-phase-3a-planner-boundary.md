# Responses Continuation Phase 3A Planner Boundary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete Phase 3A by adding the pure Responses continuation planner boundary. The adapter auth-scope propagation landed earlier; this phase exposes sanitized auth scope to planning without giving the pure planner access to raw credentials, bearer/OAuth tokens, or raw org/project identifiers.

**Architecture:** Add a small `llm` planner input/result model, a pure helper that accepts only sanitized fields, a `llm.Client.PlanResponsesContinuation` dispatch method, and an OpenAI adapter method that maps adapter-owned sanitized scope into the pure helper. Storage-scope fields exist on the result but remain zero/stubbed until Phase 4A. Request fingerprinting remains Phase 3B.

**Tech Stack:** Go, `llm.Client`, `llm.ResponsesContinuationPlanner`, `llm/providers/openai.Adapter`, deterministic unit tests.

---

## File Structure

- `llm/responses_continuation.go`: add planner input/result types and the pure helper.
- `llm/responses_continuation_test.go`: add pure-helper security/zero-field tests.
- `llm/client.go`: add `PlanResponsesContinuation` dispatch through a provider adapter interface.
- `llm/client_test.go`: add client dispatch/error tests.
- `llm/providers/openai/adapter.go`: implement the adapter planner method using only sanitized adapter fields.
- `llm/providers/openai/adapter_test.go`: add OpenAI adapter planner tests for public and Codex endpoint families and no raw handle leakage.
- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3a.md`: record evidence.

## Non-Goals

- Do not compute request fingerprints; Phase 3B owns canonicalization and production-prompt determinism.
- Do not compute storage-scope fingerprints; Phase 4A owns exact storage-scope enforcement.
- Do not enable `responses_delta`.
- Do not send `previous_response_id`.
- Do not change OpenAI `store:false`.
- Do not add session-level anchor selection or persistence.
- Do not add provider live tests.

### Task 1: Pure Planner Types and Helper

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/responses_continuation_test.go`

- [ ] **Step 1: Add failing pure-helper tests**

Add tests proving:

- the planner input type exposes sanitized auth scope and hashed org/project fields, not raw credential/token/org/project fields;
- the pure helper copies sanitized `AuthScopeIdentity`, endpoint family, and hashed org/project fields into the result;
- storage-scope and request-fingerprint fields are present but empty until later phases.

Suggested type shape:

```go
type ResponsesContinuationPlanInput struct {
	EndpointFamily    ResponsesEndpointFamily
	AuthScopeIdentity AuthScopeIdentity
	OrgIDHash         string
	ProjectIDHash     string
	Request           Request
}

type ResponsesContinuationPlan struct {
	EndpointFamily             ResponsesEndpointFamily
	AuthScopeIdentity          AuthScopeIdentity
	OrgIDHash                  string
	ProjectIDHash              string
	RequestFingerprint         string
	StorageScopeFingerprint    string
	StoragePolicyLabel         string
	ContinuationStorageAllowed bool
}
```

The security test may use reflection over `ResponsesContinuationPlanInput` field names to reject names containing `APIKey`, `Bearer`, `Token`, `Raw`, `OrgID`, or `ProjectID` unless the field name ends with `Hash`.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestPlanResponsesContinuation|TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields' -count=1
```

Expected: FAIL because the planner types/helper do not exist yet.

- [ ] **Step 2: Implement pure helper**

Add:

```go
func PlanResponsesContinuation(input ResponsesContinuationPlanInput) ResponsesContinuationPlan {
	return ResponsesContinuationPlan{
		EndpointFamily:    input.EndpointFamily,
		AuthScopeIdentity: input.AuthScopeIdentity,
		OrgIDHash:         strings.TrimSpace(input.OrgIDHash),
		ProjectIDHash:     strings.TrimSpace(input.ProjectIDHash),
	}
}
```

Do not read credentials or provider adapter state in this helper.

### Task 2: Client Dispatch Boundary

**Files:**
- Modify: `llm/client.go`
- Modify: `llm/client_test.go`

- [ ] **Step 1: Add failing client dispatch tests**

Add a test adapter implementing:

```go
type ResponsesContinuationPlanner interface {
	PlanResponsesContinuation(Request) (ResponsesContinuationPlan, error)
}
```

Tests should prove:

- `Client.PlanResponsesContinuation(ctx, Request{Provider: "openai", ...})` resolves the provider and calls the adapter planner;
- default provider resolution works when `Request.Provider` is empty;
- unknown provider returns the existing configuration-error shape;
- adapters without the planner interface return a configuration error.

The method does not need to use `ctx` yet beyond matching `Client` API style; keep the adapter planner pure and synchronous.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestClient_PlanResponsesContinuation' -count=1
```

Expected: FAIL until the client method/interface exists.

- [ ] **Step 2: Implement client method**

Add a client method that mirrors provider resolution from `Complete`/`Stream`, normalizes provider names, stamps `req.Provider`, and calls the adapter planner interface. It must not call `Complete`, `Stream`, middleware, or provider network paths.

### Task 3: OpenAI Adapter Planner

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Add failing OpenAI adapter tests**

Add tests proving:

- public OpenAI adapters plan with `ResponsesEndpointFamilyOpenAIPublic`;
- Codex/OAuth adapters plan with `ResponsesEndpointFamilyOpenAICodex`;
- sanitized `AuthScopeIdentity`, `OrgIDHash`, and `ProjectIDHash` are present in the plan;
- raw API keys, bearer tokens, account IDs, workspace IDs, org IDs, and project IDs do not appear in any string field of the plan.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation' -count=1
```

Expected: FAIL until the adapter method exists.

- [ ] **Step 2: Implement adapter planner**

Add `PlanResponsesContinuation(req llm.Request) (llm.ResponsesContinuationPlan, error)` on `*Adapter`.

Endpoint-family mapping:

- `ResponsesPath == defaultCodexResponses` -> `llm.ResponsesEndpointFamilyOpenAICodex`;
- otherwise -> `llm.ResponsesEndpointFamilyOpenAIPublic`.

Call the pure helper with `AuthScopeIdentity`, `OrgIDHash`, and `ProjectIDHash`. Do not pass raw `APIKey`, raw bearer/OAuth token values, raw `OrgID`, raw `ProjectID`, or `ChatGPTAccountID` into the helper.

### Task 4: Proof, Verification, Commit

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3a.md` with:

- scope;
- evidence commands;
- contracts proven;
- explicit statement that request fingerprinting/storage-scope/runtime continuation remain deferred.

- [ ] **Step 2: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestPlanResponsesContinuation|TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields|TestClient_PlanResponsesContinuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit**

```sh
git status --short
git add llm/responses_continuation.go llm/responses_continuation_test.go llm/client.go llm/client_test.go llm/providers/openai/adapter.go llm/providers/openai/adapter_test.go docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3a.md
git commit -m "feat(llm): add responses continuation planner boundary"
```

## Self-Review

- Spec coverage: completes the pure planner helper boundary portion of Phase 3A; earlier commits already covered sanitized auth-scope propagation into the OpenAI adapter.
- Safety: no runtime continuation enablement, no `previous_response_id`, no `store:true`.
- Test quality: tests assert type/API contracts and sanitized planner data, not generated request bodies or large string snapshots.
