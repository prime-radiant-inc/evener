# Responses Continuation Phase 4A Proof

## Scope

Phase 4A adds storage-scope metadata to Responses continuation planning while keeping runtime continuation disabled.

Covered changes:

- OpenAI adapters retain an explicit `ContinuationHasher`.
- Env/config construction paths pass a state-backed continuation hasher into OpenAI instance params.
- OpenAI planning computes storage policy labels and storage-scope fingerprints.
- Public OpenAI `store:true` is the only storage-allowed policy in this phase.
- Public OpenAI no-store requests and Codex backend requests remain storage-disallowed.
- Storage scope contains only sanitized hashes and endpoint facts.

Deferred by design:

- Runtime continuation dispatch.
- Anchor selection.
- Anchor metadata persistence.
- Endpoint-family registry enablement.
- Sending `previous_response_id` or `responses_delta`.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestContinuationStorageScope|TestContinuationHasher_StorageScope|TestContinuationStoreOverride|TestPlanResponsesContinuation|TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields|TestClient_PlanResponsesContinuation' -count=1 -v
```

Result: pass.

```sh
GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_PlanResponsesContinuation|TestNewForInstance_Continuation|TestNewFromEnv_Continuation|TestInstanceParamsFromConfig|TestInstanceFactory_EnvTunables' -count=1 -v
```

Result: pass.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run '^TestOpenAIResponsesContinuationFingerprint_' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Contracts Proven

- OpenAI planner output includes `StorageScope`, `StorageScopeFingerprint`, `StoragePolicyLabel`, and `ContinuationStorageAllowed` when a hasher is available.
- Missing OpenAI continuation hasher returns `ErrContinuationSecretUnavailable` instead of producing an eligible-looking plan.
- Public OpenAI `store:true` maps to `public-openai-store` with storage allowed.
- Public OpenAI `store:false` and omitted store map to `public-openai-no-store` with storage disallowed.
- Codex backend plans map to `codex-storage-unproven` with storage disallowed.
- Storage-scope fingerprints change when endpoint family, base URL, path, auth source, org/project hash, credential hash, account/workspace hash, conversation hash, or storage policy changes.
- Request fingerprints still follow the Phase 4A endpoint-family exclusion table.
- Public and Codex storage scopes are separated by endpoint-family scope fields; their request fingerprints are intentionally distinct for the default body because Codex retains `store:false`.
- Plan dumps do not include raw API keys, bearer tokens, OAuth tokens, org/project IDs, account/workspace IDs, or conversation IDs.
