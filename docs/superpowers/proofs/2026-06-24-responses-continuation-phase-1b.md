# Responses Continuation Phase 1B Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 1B-secret and handle hashing

## Local Secret

Checkable line: continuation hashing uses a local root secret stored outside session transcripts under the supplied state dir.

Evidence:
- `llm/continuation_secret_test.go:TestContinuationSecretLoadOrCreateCreatesPrivateFile`
- `llm/continuation_secret_test.go:TestContinuationSecretLoadOrCreateReusesExistingSecret`

Verdict: the secret path is `<stateDir>/continuation/local_scope_secret`; the directory is private and the secret file is mode `0600`.

## Fail Closed

Checkable line: missing state dir or unsafe secret file permissions return `ErrContinuationSecretUnavailable`.

Evidence:
- `llm/continuation_secret_test.go:TestContinuationSecretRequiresStateDir`
- `llm/continuation_secret_test.go:TestContinuationSecretRejectsWrongPermissions`
- `llm/continuation_secret_test.go:TestContinuationHasherForStateDirUnavailableWithoutState`

Verdict: later runtime callers can treat this as a no-anchor/full-history condition while ordinary model calls continue.

## Versioned Hashes

Checkable line: provider-state handle hashes and scope hashes are versioned, stable for normalized input, and use separate derived subkeys.

Evidence:
- `llm/continuation_secret_test.go:TestContinuationHandleHashFormatAndStability`
- `llm/continuation_secret_test.go:TestContinuationScopeHashUsesSeparateSubkey`
- `llm/continuation_secret_test.go:TestContinuationHashDoesNotLeakRawValue`
- `llm/continuation_secret_test.go:TestContinuationHashRejectsUnknownKind`
- `llm/continuation_secret_test.go:TestContinuationHasherForStateDirLoadsSecret`

Verdict:
- Provider handles use `cont-handle-v1:<kind>:<base64url(hmac(redaction_subkey, normalized_value))>`.
- Scope identifiers use `cont-scope-v1:<kind>:<base64url(hmac(scope_subkey, normalized_value))>`.
- Raw provider handles are not returned by the helper.

## Runtime Status

Checkable line: Phase 1B adds helpers only; no session path selects `responses_delta`, no provider payload changes, and no assistant turn becomes anchor-eligible solely from this phase.

Evidence:
- `GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuation' -count=1 -v`
- Runtime-disabled scan in this phase found `responses_delta` only in the continuation decision layer and tests.

Verdict: runtime continuation remains disabled.
