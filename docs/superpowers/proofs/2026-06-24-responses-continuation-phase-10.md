# Responses Continuation Phase 10 Proof

## Scope

Phase 10 adds deterministic token accounting and rollout diagnostics for Responses continuation paths.

This phase keeps production continuation registry entries disabled. It does not add live provider token-counting, Phase 11 raw-local export, or Phase 12 rollout activation thresholds.

## Substrate Recheck

- Phase 9 real-session delta requests preserve a full-history fallback sidecar and retry same-model full history on continuation rejection.
- `llm.Request` is the shared request metadata path for adapter API logs and transcript API-call records.
- `recordResponseUsage` is the session path that records provider usage and updates context-pressure input-token baselines.
- `agent/doctor.APILog` already summarizes transcript API-call records and can carry narrow rollout diagnostics without adding a new runtime metrics service.

## RED Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_RecordsContinuationTokenEstimates' -count=1 -v
```

Initial result: failed to compile because `Request` and `APILogRequest` did not expose token-estimate or continuation diagnostic fields.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10DeltaCarriesFullHistoryShadowEstimate' -count=1 -v
```

Initial result: failed to compile because the deterministic shadow-estimate test hook did not exist. After adding the hook, the test proved the estimator sees the full-history request containing both prior and current markers while the dispatched request remains `responses_delta`.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10PressureUsesFullHistoryShadowWhenLarger' -count=1 -v
```

Initial result: failed because `LastInputTokens` was `10`, the provider-reported input usage, instead of the full-history shadow estimate `900`.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent/doctor -run 'TestAPILogContinuationCountsByEndpointFamily' -count=1 -v
```

Initial result: failed to compile because `APILogTotals` did not include continuation history-mode counts by endpoint family.

The unavailable-shadow test passed after the shadow-estimate helper was introduced, confirming the short-circuit was already in place.

## GREEN Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestBuildAPILogRequest_RecordsContinuationTokenEstimates' -count=1 -v
```

Result: pass.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase10|TestSession_OpenAIResponsesContinuationPhase9|TestFallbackChain_Continuation' -count=1 -v
```

Result: pass.

```sh
GOCACHE=/tmp/serf-gocache go test ./agent/doctor -run 'TestAPILogContinuationCountsByEndpointFamily|TestAPILog' -count=1 -v
```

Result: pass.

```sh
git diff --check
```

Result: pass.

## Commits

- `8e1e5909 feat(llm): expose responses continuation token estimates`
- `288cac57 feat(agent): compute responses continuation shadow estimates`
- `22559376 test(agent): prove continuation shadow unavailable fallback`
- `7fba0652 feat(agent): account continuation shadow pressure`
- `3a440bdb feat(agent): summarize responses continuation history modes`

## Contracts Proven

- API-log and transcript request metadata expose dispatched input-token estimates, full-history shadow estimates, and continuation accounting diagnostics.
- The full-history shadow estimate is computed before delta shaping from the single expanded full-history request for the round.
- The shadow estimate is not computed from the delta-only request and not from `FullHistoryFallbackMessages`.
- If the shadow estimate is unavailable, the session sends `full_history`, clears continuation handles, and records `continuation_shadow_estimate_unavailable`.
- Context-pressure accounting records the larger of provider input usage and the full-history shadow estimate.
- Existing provider usage remains recorded on the response for billing/accounting.
- Doctor API-log totals include `responses_delta`, `full_history`, and `full_history_fallback` counts by endpoint family.
- Aggregate continuation diagnostic keys include endpoint family only, not attempt ids, response hashes, storage-scope fingerprints, or raw provider handles.

## Remaining Work

- Phase 11 must complete explicit local diagnostic raw export for provider-state handles.
- Phase 12 must record production-path proof artifacts and then enable endpoint-family registry entries in small follow-up commits.
- Runtime continuation registry entries remain disabled until the matching Phase 12 enablement proof lands.
