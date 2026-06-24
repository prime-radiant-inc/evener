# Responses Continuation Phase 3B Request Fingerprint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete Phase 3B by adding an adapter-owned OpenAI Responses request fingerprint to `llm.Client.PlanResponsesContinuation`. The fingerprint must be stable for equivalent provider-visible request bodies, change when the request contract changes, and be computed from the same OpenAI/Codex body-building path used by real requests.

**Architecture:** The OpenAI adapter planner calls `buildRequestBody(req)`, removes continuation/storage handle fields that must not affect request compatibility, canonicalizes the remaining body to deterministic JSON, and hashes it with a versioned prefix. This keeps request-shape ownership in the provider adapter and avoids a parallel compatibility model. Storage-scope fingerprints stay empty until Phase 4A.

**Tech Stack:** Go, `llm.Client`, `llm/providers/openai.Adapter`, deterministic unit tests, production prompt rendering tests.

---

## File Structure

- `llm/providers/openai/adapter.go`: compute request fingerprints in `PlanResponsesContinuation`.
- `llm/providers/openai/responses_continuation_fingerprint.go` or nearby OpenAI provider file: add canonical JSON hash helper and continuation-field filtering.
- `llm/providers/openai/adapter_test.go`: add OpenAI adapter planner fingerprint tests.
- `agent/responses_continuation_fingerprint_test.go` or `agent/profile_test.go`: add production system prompt determinism tests through `llm.Client.PlanResponsesContinuation`.
- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3b.md`: record evidence.

## Non-Goals

- Do not compute storage-scope fingerprints; Phase 4A owns exact storage-scope enforcement.
- Do not select anchors, persist continuation metadata, or enable runtime `responses_delta`.
- Do not send `previous_response_id`.
- Do not change OpenAI `store:false`.
- Do not add provider live tests.
- Do not duplicate OpenAI Responses request body construction outside the adapter.

## Fingerprint Contract

Include fields from the real OpenAI Responses request body after adapter-specific filtering, including Codex unsupported-field filtering. Exclude only fields that identify or force a specific continuation/storage attempt rather than the reusable request contract:

- `previous_response_id`
- `conversation`
- `store`

Keep prompt-cache controls such as `prompt_cache_key` and `prompt_cache_retention` in the request fingerprint. They are provider-visible request-shape controls today. If a later phase introduces continuation-owned retention knobs, those future fields can be excluded in that phase with a version bump.

The fingerprint string format is:

```text
cont-req-v1:<base64url-sha256>
```

Use canonical JSON over the filtered body so map ordering cannot affect fingerprints. Preserve array ordering because message, tool, include, and stop-sequence order is provider-visible.

### Task 1: OpenAI Adapter Fingerprint Tests

**Files:**
- Modify: `llm/providers/openai/adapter_test.go`

- [x] **Step 1: Add failing request fingerprint tests**

Add deterministic tests proving:

- `PlanResponsesContinuation` returns a non-empty `RequestFingerprint` with `cont-req-v1:` prefix.
- Equivalent bodies with different Go map insertion order produce the same fingerprint.
- request-shape changes produce different fingerprints for:
  - system instructions/message content;
  - tool definitions;
  - tool choice;
  - reasoning effort;
  - provider options;
  - prompt-cache key/retention.
- `previous_response_id`, `conversation`, and `store` do not affect the request fingerprint.
- Codex fingerprints are computed after Codex-specific filtering, so fields that Codex drops from the wire body do not change the Codex fingerprint.
- storage-scope fields on the plan remain empty/false until Phase 4A.

Expected first run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation_.*Fingerprint' -count=1
```

Expected: FAIL because `RequestFingerprint` is still empty.

- [x] **Step 2: Keep assertions on contracts, not full rendered JSON**

Tests should compare fingerprint equality/inequality and prefixes. Do not snapshot the full request body or large rendered JSON.

### Task 2: OpenAI Adapter Fingerprint Implementation

**Files:**
- Modify: `llm/providers/openai/adapter.go`
- Add or modify: `llm/providers/openai/responses_continuation_fingerprint.go`

- [x] **Step 1: Add canonical hash helper**

Add a helper in the OpenAI provider package:

```go
func requestFingerprintForResponsesBody(body map[string]any) (string, error)
```

The helper should:

- deep-copy/canonicalize maps with sorted keys;
- remove `previous_response_id`, `conversation`, and `store` from the top-level body before hashing;
- marshal canonical data with `encoding/json`;
- return `cont-req-v1:<base64.RawURLEncoding(sha256)>`.

Keep the helper provider-local unless another provider needs it later.

- [x] **Step 2: Compute the fingerprint in the adapter planner**

Update `(*Adapter).PlanResponsesContinuation(req)` to:

- call `a.buildRequestBody(req)`;
- compute the request fingerprint from the built body;
- pass sanitized scope through the existing pure planner boundary;
- set `plan.RequestFingerprint` from the helper result;
- return build/canonicalization errors instead of silently planning an eligible-looking request.

Do not call `Complete`, `Stream`, middleware, raw logging, or provider network paths.

### Task 3: Production Prompt Determinism Through Client Planner

**Files:**
- Add: `agent/responses_continuation_fingerprint_test.go`

- [x] **Step 1: Add production prompt determinism tests**

Use the real embedded system prompt renderer with fixed environment data:

- `WorkingDir`
- `Platform`
- `OSVersion`
- `Today`
- `Model`
- `KnowledgeCutoff`

Then build an `llm.Request` with the rendered system prompt and call `llm.Client.PlanResponsesContinuation` through a real OpenAI adapter planner. Use a test API key and temp state home; no network request is made.

Tests should prove:

- two prompts rendered with the same fixed environment produce the same request fingerprint;
- changing `Today` produces a different request fingerprint;
- the planner is reached through `llm.Client.PlanResponsesContinuation`, not by calling OpenAI helper functions directly.

Expected first run before Task 2 implementation:

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestOpenAIResponsesContinuationFingerprint_' -count=1
```

Expected: FAIL because `RequestFingerprint` is empty.

### Task 4: Proof, Verification, Commit

- [x] **Step 1: Add proof artifact**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3b.md` with:

- scope;
- evidence commands;
- contracts proven;
- explicit statement that storage-scope, anchor selection, persistence, and runtime continuation remain deferred.

- [x] **Step 2: Run focused tests**

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation_.*Fingerprint' -count=1 -v
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestOpenAIResponsesContinuationFingerprint_' -count=1 -v
git diff --check
```

- [x] **Step 3: Commit**

```sh
git status --short
git add llm/providers/openai/adapter.go llm/providers/openai/responses_continuation_fingerprint.go llm/providers/openai/adapter_test.go agent/responses_continuation_fingerprint_test.go docs/superpowers/proofs/2026-06-24-responses-continuation-phase-3b.md
git commit -m "feat(llm): fingerprint openai responses continuation requests"
```

If the helper lands in an existing file instead of a new file, omit `responses_continuation_fingerprint.go` from `git add`.

## Self-Review

- Spec coverage: completes Phase 3B request fingerprinting and production prompt determinism checks.
- Safety: no runtime continuation enablement, no `previous_response_id`, no `store:true`, and no provider live calls.
- Test quality: tests assert stable behavior contracts via planner output rather than brittle full-body snapshots.
