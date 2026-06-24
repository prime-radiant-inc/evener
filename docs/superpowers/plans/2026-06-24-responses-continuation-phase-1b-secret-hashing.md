# Responses Continuation Phase 1B Secret And Hashing Plan

> **For agentic workers:** Use superpowers:subagent-driven-development or superpowers:executing-plans task-by-task. Keep this phase deterministic and offline.

**Goal:** Add the local continuation secret and versioned HMAC helpers needed to redact provider-state handles and scope identifiers.

**Non-goals:** Do not enable `responses_delta`, do not persist anchor-eligible assistant turns, do not compute OpenAI auth/storage scope, do not change provider wire payloads, and do not export raw provider handles.

**Architecture:** This phase adds a pure hashing utility plus private local secret persistence. Session/runtime integration remains later. If a caller has no durable state directory or the secret cannot be loaded/created, callers receive a typed unavailable error and must use full history without anchor metadata.

---

## File Structure

- Create `llm/continuation_secret.go`
  - Load/create a local root secret under a supplied private state dir.
  - Derive separate redaction and scope subkeys.
  - Compute versioned provider-handle and scope-identity HMAC strings.
- Create `llm/continuation_secret_test.go`
  - Deterministic tests for file permissions, stable hashes, distinct labels, empty-state fail-closed behavior, and no raw handle leakage.
- Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1b.md`
  - Record secret path, permissions, hash formats, and fail-closed behavior.

## Task 1: Secret Store Contract

**Files:**
- Create: `llm/continuation_secret.go`
- Create: `llm/continuation_secret_test.go`

- [ ] Add failing tests:
  - `TestContinuationSecretLoadOrCreateCreatesPrivateFile`
  - `TestContinuationSecretLoadOrCreateReusesExistingSecret`
  - `TestContinuationSecretRequiresStateDir`
  - `TestContinuationSecretRejectsWrongPermissions`
- [ ] Implement:
  - `ErrContinuationSecretUnavailable`
  - `ContinuationSecretPath(stateDir string) string`
  - `LoadOrCreateContinuationSecret(stateDir string) ([]byte, error)`
  - Secret path: `<stateDir>/continuation/local_scope_secret`
  - Directory mode `0700`, file mode `0600`, 32 random bytes.
- [ ] Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuationSecret' -count=1 -v
git diff --check
```

- [ ] Commit:

```sh
git status --short
git add llm/continuation_secret.go llm/continuation_secret_test.go
git commit -m "feat(llm): add continuation local secret store" -m "Add private local continuation root-secret creation and loading under the supplied state dir. Missing state dirs and unsafe permissions fail closed; no runtime continuation path is enabled."
```

## Task 2: Versioned HMAC Helpers

**Files:**
- Modify: `llm/continuation_secret.go`
- Modify: `llm/continuation_secret_test.go`

- [ ] Add failing tests:
  - `TestContinuationHandleHashFormatAndStability`
  - `TestContinuationScopeHashUsesSeparateSubkey`
  - `TestContinuationHashDoesNotLeakRawValue`
  - `TestContinuationHashRejectsUnknownKind`
- [ ] Implement:
  - `ContinuationHashKey` or equivalent value type wrapping the loaded secret.
  - `HashContinuationHandle(kind, value string) (string, error)` for `response_id`, `previous_response_id`, `conversation_id`.
  - `HashContinuationScopeValue(kind, value string) (string, error)` for known scope identity fields used later.
  - Format:
    - `cont-handle-v1:<kind>:<base64url(hmac(redaction_subkey, normalized_value))>`
    - `cont-scope-v1:<kind>:<base64url(hmac(scope_subkey, normalized_value))>`
  - Derive subkeys with HMAC labels `serf-continuation-redaction-v1` and `serf-continuation-scope-v1`.
- [ ] Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuation.*Hash|TestContinuationSecret' -count=1 -v
git diff --check
```

- [ ] Commit:

```sh
git status --short
git add llm/continuation_secret.go llm/continuation_secret_test.go
git commit -m "feat(llm): add continuation hmac helpers" -m "Add versioned redaction and scope HMAC helpers for Responses continuation provider-state handles. The helper uses separate derived subkeys and never returns raw provider handles."
```

## Task 3: Fail-Closed Integration Helper

**Files:**
- Modify: `llm/continuation_secret.go`
- Modify: `llm/continuation_secret_test.go`

- [ ] Add failing tests:
  - `TestContinuationHasherForStateDirUnavailableWithoutState`
  - `TestContinuationHasherForStateDirLoadsSecret`
- [ ] Implement `ContinuationHasherForStateDir(stateDir string) (*ContinuationHasher, error)` as the single public convenience constructor used by later session/provider phases.
- [ ] Do not call it from session runtime yet.
- [ ] Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuation' -count=1 -v
rg -n 'ContinuationHasherForStateDir|HashContinuationHandle|HashContinuationScopeValue' --glob '*.go'
git diff --check
```

- [ ] Commit:

```sh
git status --short
git add llm/continuation_secret.go llm/continuation_secret_test.go
git commit -m "feat(llm): expose continuation hasher constructor" -m "Expose a state-dir-backed continuation hasher constructor for later session/provider phases. The constructor fails closed when no private durable state dir is available."
```

## Task 4: Proof

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1b.md`

- [ ] Record:
  - secret path and permissions;
  - handle/scope hash formats;
  - separate subkey proof;
  - fail-closed behavior;
  - no runtime continuation enablement.
- [ ] Run:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuation' -count=1 -v
rg -n 'DefaultResponsesContinuationSupportRegistry|ResponsesContinuationAuto|HistoryModeResponsesDelta|responses_delta' --glob '*.go'
git diff --check
```

- [ ] Commit:

```sh
git status --short
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-1b.md
git commit -m "docs: record responses continuation phase 1b proof" -m "Record local continuation secret handling, versioned HMAC helper behavior, and fail-closed semantics. Runtime continuation remains disabled."
```

## Review Notes

- The local root secret is not the provider credential and must never be stored in transcripts, API logs, raw HTTP logs, exports, or proof artifacts.
- Missing or unusable secret means no anchor eligibility metadata; normal full-history model calls can still proceed.
- This phase intentionally does not decide provider auth/storage identity. That belongs in later adapter/provider planning work.
