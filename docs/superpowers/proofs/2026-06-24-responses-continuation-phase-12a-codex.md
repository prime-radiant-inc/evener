# Responses Continuation Phase 12A Codex No-Go Proof

## Scope

Checked whether the ChatGPT/Codex backend `/backend-api/codex/responses` can satisfy the Phase 12A production-path live proof requirements for Responses continuation.

Endpoint family: `openai_codex`

Model: `gpt-5.4`

Result: no-go for runtime continuation. The backend still rejects valid `previous_response_id` requests with an explicit unsupported-parameter error.

## Evidence

```sh
SERF_OPENAI_CODEX_DISCOVERY_E2E=1 SERF_OPENAI_CODEX_DISCOVERY_MODEL=gpt-5.4 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_CodexResponsesContinuationDiscovery' -count=1 -v
```

Observed result:

```text
=== RUN   TestAdapter_E2E_CodexResponsesContinuationDiscovery
    responses_continuation_discovery_e2e_test.go:64: codex_backend valid previous_response_id request failed: openai error (status=400): responses.create(stream) failed: map[detail:Unsupported parameter: previous_response_id]
--- FAIL: TestAdapter_E2E_CodexResponsesContinuationDiscovery (1.85s)
FAIL
FAIL	primeradiant.com/serf/llm/providers/openai	2.039s
FAIL
```

## Gate Evaluation

Phase 12A-codex requires the live proof to show:

- valid stored-anchor reuse through `previous_response_id`;
- two successful branches from one stored anchor;
- explicit invalid-anchor behavior;
- observed provider token and payload behavior for a delta versus a full-history shadow;
- concrete rollout thresholds before registry enablement.

The live run did not reach those later checks because the first valid continuation request failed. This is not a missing credential or test harness issue: the backend created an anchor response, then rejected the valid `previous_response_id` follow-up as unsupported.

## Decision

The Codex backend registry entry must remain disabled.

Phase 12B-codex must not enable `openai_codex` unless a future provider behavior change or endpoint-specific design produces a new Phase 12A-codex proof where valid `previous_response_id` is accepted and the full proof matrix passes.

No Codex storage/retention request-fingerprint fields were discovered from a successful continuation path, so the existing Codex storage policy remains `codex-storage-unproven`.
