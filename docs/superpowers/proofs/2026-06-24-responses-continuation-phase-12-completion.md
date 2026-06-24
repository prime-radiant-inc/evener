# Responses Continuation Phase 12 Completion Audit

## Scope

Audited the Responses continuation project through Phase 12 against the endpoint-family phase matrix in `docs/superpowers/specs/2026-06-23-openai-responses-continuation-design.md`.

Phase 12 endpoint-family outcomes:

- Public OpenAI `/v1/responses`: Phase 12A passed, Phase 12B enabled.
- ChatGPT/Codex backend `/backend-api/codex/responses`: Phase 12A no-go, Phase 12B not enabled.

## Requirement Audit

| Requirement | Evidence | Status |
|---|---|---|
| Public OpenAI 12A live proof exists and records accepted continuation semantics. | `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-public.md` | Satisfied |
| Public OpenAI 12A records request-payload reduction and provider token observations. | `phase-12a-public.md` records both the first live run and completion-audit rerun. | Satisfied |
| Public OpenAI 12A includes numeric rollout thresholds. | `phase-12a-public.md` has eligible-hit-rate, prompt-cache, storage/error, provider-token/cost, and rate-limit thresholds. | Satisfied |
| Public OpenAI 12B flips only the public registry row. | `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12b-public.md`; `llm/responses_continuation.go`. | Satisfied |
| Codex 12A is not treated as passing without accepted `previous_response_id`. | `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-codex.md` records the current live rejection. | Satisfied |
| Codex 12B does not enable the Codex registry row after the failed 12A gate. | `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12b-codex.md`; registry test covers public enabled plus Codex disabled; upstream Codex source shows `previous_response_id` is WebSocket-only, not an HTTP `/backend-api/codex/responses` field. | Satisfied |
| Default tests do not require provider credentials. | Phase 12 live harness skips unless explicit opt-in env vars are set. | Satisfied |

## Current Live Evidence

Public OpenAI Phase 12 live proof:

```sh
set -a; . ../../.env; set +a; SERF_LOG_RAW_HTTP=1 SERF_OPENAI_RESPONSES_PHASE12_E2E=1 GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof' -count=1 -v
```

Result:

```text
=== RUN   TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof
    session_openai_continuation_phase12_live_test.go:196: phase12_public_live model=gpt-5.2 delta_bytes=54734 full_history_shadow_bytes=55047 omitted_input_item_bytes=406 continuation_overhead_bytes=81 net_body_byte_saving=313 provider_input_tokens=11127 full_history_shadow_tokens=13196
--- PASS: TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof (24.00s)
PASS
ok  	primeradiant.com/serf/agent	24.332s
```

Codex Phase 12 live gate:

```sh
SERF_OPENAI_CODEX_DISCOVERY_E2E=1 SERF_OPENAI_CODEX_DISCOVERY_MODEL=gpt-5.4 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run 'TestAdapter_E2E_CodexResponsesContinuationDiscovery' -count=1 -v
```

Result:

```text
=== RUN   TestAdapter_E2E_CodexResponsesContinuationDiscovery
    responses_continuation_discovery_e2e_test.go:64: codex_backend valid previous_response_id request failed: openai error (status=400): responses.create(stream) failed: map[detail:Unsupported parameter: previous_response_id]
--- FAIL: TestAdapter_E2E_CodexResponsesContinuationDiscovery (1.85s)
FAIL
FAIL	primeradiant.com/serf/llm/providers/openai	2.039s
FAIL
```

The Codex command is expected to fail on the HTTP backend shape. Upstream Codex source confirms `previous_response_id` is only used on Responses WebSocket `response.create`, so Codex continuation would require a separate endpoint-specific WebSocket design.

## Decision

Phase 12 is closed with public OpenAI enabled and Codex disabled.

This is the only safe end state supported by current provider behavior. Enabling Codex would violate the Phase 12A/12B gate because the backend rejects valid continuation handles.
