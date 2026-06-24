# Responses Continuation Phase 4A Storage Scope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add storage-scope fingerprinting, storage-policy labels, and continuation-owned storage override helpers for OpenAI Responses continuation planning.

**Architecture:** Keep runtime continuation disabled. Extend the planner result with a sanitized `ContinuationStorageScope` computed by the provider/auth layer from endpoint facts, sanitized auth identity hashes, conversation hash, and storage policy. Keep request fingerprint compatibility separate from storage compatibility, and represent continuation-owned `store:true` mutations with a clone plus ownership sidecar so explicit user/provider storage settings survive cleanup.

**Tech Stack:** Go, `llm` pure helpers, OpenAI provider adapter planner, deterministic unit tests.

---

## File Structure

- `llm/responses_continuation.go`: add `ContinuationStorageScope`, `ContinuationStoreOverride`, pure storage override helpers, and planner result fields.
- `llm/responses_continuation_test.go`: add pure storage-scope and storage-override tests.
- `llm/continuation_secret.go`: add storage-scope fingerprint HMAC helper using the scope subkey.
- `llm/continuation_secret_test.go`: prove storage-scope fingerprints are stable, secret-scoped, versioned, and distinct from handle hashes.
- `llm/providers/openai/adapter.go`: retain the continuation hasher on the adapter and compute storage scope in `PlanResponsesContinuation`.
- `llm/providers/openai/responses_continuation_fingerprint.go`: make request-fingerprint exclusions endpoint-family-specific.
- `llm/providers/openai/adapter_test.go`: add OpenAI storage-scope, policy, request-fingerprint exclusion-table, constructor, and raw-secret non-leakage tests.
- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4a.md`: record evidence.

## Non-Goals

- Do not select anchors or send `responses_delta`.
- Do not send `previous_response_id`.
- Do not dispatch continuation-owned `store:true` from the session path.
- Do not persist anchor metadata.
- Do not enable either endpoint-family registry entry.
- Do not add provider live tests.
- Do not assume Codex backend storage semantics beyond fields already proven by discovery.

## Storage Policy Labels

Use these labels in Phase 4A:

- `public-openai-store`: public OpenAI request shape with `store:true`; storage can produce future anchors.
- `public-openai-no-store`: public OpenAI request shape with `store:false` or omitted store; storage cannot produce future anchors.
- `codex-storage-unproven`: Codex backend shape before a Codex storage proof; continuation storage is not allowed.

`ContinuationStorageAllowed` is true only for `public-openai-store` in this phase.

## Request Fingerprint Exclusion Table

Phase 3B currently removes `store` for all endpoint families. Phase 4A must replace that broad rule with the spec table:

| Endpoint family | Top-level fields excluded from request fingerprint |
|---|---|
| Public OpenAI `/v1/responses` | `previous_response_id`, `conversation`, `store` |
| ChatGPT/Codex backend `/backend-api/codex/responses` | `previous_response_id`, `conversation` |

Any future Codex storage/retention field requires a table update, deterministic fixture, and request-fingerprint version bump before Codex runtime enablement.

### Task 1: Endpoint-Specific Request Fingerprint Exclusions

**Files:**
- Modify: `llm/providers/openai/responses_continuation_fingerprint.go`
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Add failing exclusion-table tests**

Add tests proving:

- public OpenAI `Store` changes do not change `RequestFingerprint`;
- Codex backend `Store` changes do change `RequestFingerprint`;
- both endpoint families still exclude `ConversationID` and `PreviousResponseID`;
- public OpenAI prompt-cache controls still change the request fingerprint.

Suggested test names:

```go
func TestAdapter_PlanResponsesContinuation_PublicFingerprintExcludesStore(t *testing.T)
func TestAdapter_PlanResponsesContinuation_CodexFingerprintKeepsStoreUntilStorageShapeProven(t *testing.T)
func TestAdapter_PlanResponsesContinuation_FingerprintExcludesContinuationHandlesByEndpoint(t *testing.T)
```

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation_.*Fingerprint' -count=1
```

Expected: FAIL because Codex currently also drops `store`.

- [ ] **Step 2: Implement endpoint-family exclusion table**

Change the helper signature to accept the endpoint family:

```go
func requestFingerprintForResponsesBody(family llm.ResponsesEndpointFamily, body map[string]any) (string, error)
```

Add a local table helper:

```go
func responsesRequestFingerprintExcludedFields(family llm.ResponsesEndpointFamily) map[string]bool {
	excluded := map[string]bool{
		"previous_response_id": true,
		"conversation":         true,
	}
	if family == llm.ResponsesEndpointFamilyOpenAIPublic {
		excluded["store"] = true
	}
	return excluded
}
```

Update `(*Adapter).PlanResponsesContinuation` to call the helper with the already resolved endpoint family. Keep the fingerprint version `cont-req-v1`; the public hash semantics stay compatible, and Codex is not runtime-enabled yet.

### Task 2: Storage Scope Types and HMAC

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/responses_continuation_test.go`
- Modify: `llm/continuation_secret.go`
- Modify: `llm/continuation_secret_test.go`

- [ ] **Step 1: Add failing pure storage-scope tests**

Add a `ContinuationStorageScope` value type:

```go
type ContinuationStorageScope struct {
	Fingerprint        string
	HashVersion        string
	Provider           string
	EndpointFamily     string
	BaseURL            string
	Path               string
	AuthSource         string
	OrgIDHash          string
	ProjectIDHash      string
	AccountHash        string
	WorkspaceHash      string
	CredentialHash     string
	ConversationIDHash string
	StoragePolicy      string
}
```

Add tests proving:

- the storage-scope input type exposes only sanitized hashes, not raw API keys, bearer tokens, OAuth tokens, org IDs, project IDs, accounts, workspaces, or conversation IDs;
- storage-scope fingerprints are stable for equivalent scopes;
- changing base URL, path, endpoint family, org hash, project hash, auth source, credential hash, account hash, workspace hash, conversation hash, or storage policy changes the fingerprint;
- storage-scope fingerprints use the scope subkey and do not equal provider-handle redaction hashes for the same value;
- changing the root secret changes the storage-scope fingerprint.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuationStorageScope|TestContinuationHasher_StorageScope' -count=1
```

Expected: FAIL because the type and hash helper do not exist.

- [ ] **Step 2: Implement storage scope HMAC helper**

Add an exported method:

```go
func (h *ContinuationHasher) HashContinuationStorageScope(scope ContinuationStorageScope) (string, error)
```

Implementation requirements:

- return `ErrContinuationSecretUnavailable` when `h == nil`;
- set no raw values in the fingerprint input;
- canonicalize with `encoding/json` over a small struct or map with stable field names;
- return `cont-scope-v1:storage_scope:<base64url(hmac(scope_subkey, canonical_json))>`;
- set `HashVersion` to `cont-scope-v1` in callers, not by parsing the fingerprint.

### Task 3: Continuation-Owned Storage Override Helpers

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/responses_continuation_test.go`

- [ ] **Step 1: Add failing storage override tests**

Add:

```go
type ContinuationStoreOverride struct {
	StoreSetByContinuation           bool
	OriginalStore                    *bool
	ProviderOptionKeysByContinuation []string
	StoragePolicy                    string
}
```

Add tests proving:

- applying `public-openai-store` clones the request and sets `Store=true` when the base request had nil `Store`;
- clearing that override restores nil `Store`;
- applying the override to an explicit `Store=false` request restores explicit false on clear;
- applying the override to an explicit `Store=true` request records no continuation-owned store field and clear preserves explicit true;
- the original base request is not mutated;
- provider options are cloned enough that later mutations to the clone do not mutate the base request.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuationStoreOverride' -count=1
```

Expected: FAIL because the helpers do not exist.

- [ ] **Step 2: Implement storage override helpers**

Add:

```go
const (
	ResponsesStoragePolicyPublicOpenAIStore   = "public-openai-store"
	ResponsesStoragePolicyPublicOpenAINoStore = "public-openai-no-store"
	ResponsesStoragePolicyCodexUnproven       = "codex-storage-unproven"
)

func ApplyResponsesContinuationStoreOverride(req Request, policy string) (Request, ContinuationStoreOverride)
func ClearResponsesContinuationStoreOverride(req Request, override ContinuationStoreOverride) Request
```

For Phase 4A, only `public-openai-store` changes request fields, and it changes only `Request.Store`. Preserve explicit user/provider `Store=true` as not continuation-owned. Preserve explicit `Store=false` by saving `OriginalStore`.

### Task 4: OpenAI Planner Storage Scope

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Modify: `llm/providers/openai/adapter_test.go`

- [ ] **Step 1: Add failing OpenAI planner storage-scope tests**

Add tests proving:

- public OpenAI plans include `StorageScopeFingerprint`, `StoragePolicyLabel`, and `StorageScope` when constructed with a `ContinuationHasher`;
- public OpenAI `Store=true` plans use `public-openai-store` and `ContinuationStorageAllowed=true`;
- public OpenAI `Store=false` or omitted store plans use `public-openai-no-store` and `ContinuationStorageAllowed=false`;
- Codex backend plans use `codex-storage-unproven` and `ContinuationStorageAllowed=false`;
- public OpenAI and Codex scopes differ even when model and request fingerprint match;
- changing base URL, path, org hash, project hash, credential hash, account/workspace hash, conversation hash, or storage policy changes the storage-scope fingerprint;
- raw API keys, bearer tokens, OAuth tokens, raw org/project IDs, raw account/workspace IDs, and raw conversation IDs do not appear in the plan dump.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation_.*StorageScope|TestNewForInstance_ContinuationHasher' -count=1
```

Expected: FAIL because planner storage scope is empty.

- [ ] **Step 2: Thread the continuation hasher into adapters**

Add `ContinuationHasher *llm.ContinuationHasher` to `Adapter`.

Set it in `NewForInstance` from `OpenAIInstanceParams.ContinuationHasher`.

For env/config factory paths, create the hasher from the resolved OpenAI state dir and pass it into `OpenAIInstanceParams`:

```go
hasher, err := llm.ContinuationHasherForStateDir(authopenai.DefaultStateDirWithStateHome(stateHome))
if err != nil {
	return nil, err
}
params.ContinuationHasher = hasher
```

Do not auto-create a hasher from `NewForInstance` when tests call it with empty `StateHome` and nil `ContinuationHasher`; direct tests should pass a hasher explicitly.

- [ ] **Step 3: Compute OpenAI storage scope in the planner**

In `PlanResponsesContinuation`:

- build the request body;
- compute request fingerprint with endpoint-family-specific exclusions;
- hash `req.ConversationID` with `HashContinuationScopeValue("conversation_id", req.ConversationID)` when present;
- choose storage policy from endpoint family and built body:
  - public + body `store == true` -> `public-openai-store`, allowed true;
  - public + body `store != true` -> `public-openai-no-store`, allowed false;
  - Codex -> `codex-storage-unproven`, allowed false;
- build `ContinuationStorageScope` with normalized provider `"openai"`, endpoint family, `BaseURL`, `ResponsesPath`, sanitized auth hashes, conversation hash, and policy;
- call `ContinuationHasher.HashContinuationStorageScope`;
- populate `plan.StorageScope`, `plan.StorageScopeFingerprint`, `plan.StoragePolicyLabel`, and `plan.ContinuationStorageAllowed`.

If the hasher is missing or scope hashing fails, return the error so session code can fail closed to full history in later phases. Do not silently produce an eligible-looking plan without a storage fingerprint.

### Task 5: Proof, Verification, Commit

- [ ] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4a.md` with:

- scope;
- evidence commands;
- contracts proven;
- explicit statement that runtime continuation, anchor selection, persistence, and endpoint enablement remain deferred.

- [ ] **Step 2: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuationStorageScope|TestContinuationHasher_StorageScope|TestContinuationStoreOverride|TestPlanResponsesContinuation|TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields|TestClient_PlanResponsesContinuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation|TestNewForInstance_Continuation|TestNewFromEnv_Continuation|TestInstanceFactory_Continuation' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestOpenAIResponsesContinuationFingerprint_' -count=1 -v
git diff --check
```

- [ ] **Step 3: Commit**

```sh
git status --short
git add llm/responses_continuation.go llm/responses_continuation_test.go llm/continuation_secret.go llm/continuation_secret_test.go llm/providers/openai/adapter.go llm/providers/openai/responses_continuation_fingerprint.go llm/providers/openai/adapter_test.go agent/responses_continuation_fingerprint_test.go docs/superpowers/proofs/2026-06-24-responses-continuation-phase-4a.md
git commit -m "feat(llm): add responses continuation storage scope planning"
```

If `agent/responses_continuation_fingerprint_test.go` does not need changes, omit it from `git add`.

## Self-Review

- Spec coverage: covers Phase 4A storage-scope fingerprinting, storage-policy labels, request-fingerprint storage-field exclusions, and continuation-owned storage override ownership.
- Safety: keeps runtime continuation disabled; no `previous_response_id`, anchor selection, persistence, registry enablement, or live provider calls.
- Test quality: tests assert structured planner/scope/override contracts rather than full request JSON snapshots.
