# Responses Continuation Phase 12A Public Proof

## Scope

Proved the public OpenAI `/v1/responses` production session path can create a stored anchor, send a follow-up `responses_delta` request with `previous_response_id`, reject invalid anchors, and reduce the second-turn provider request payload relative to a full-history shadow request.

Endpoint family: `openai_public`

Model: `gpt-5.2`

Proposed `MaxAnchorAgeSeconds`: `3600`

## Evidence

```sh
set -a; . ../../.env; set +a; SERF_LOG_RAW_HTTP=1 SERF_OPENAI_RESPONSES_PHASE12_E2E=1 GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof' -count=1 -v
```

Observed result:

```text
=== RUN   TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof
    session_openai_continuation_phase12_live_test.go:196: phase12_public_live model=gpt-5.2 delta_bytes=54736 full_history_shadow_bytes=55049 omitted_input_item_bytes=406 continuation_overhead_bytes=81 net_body_byte_saving=313 provider_input_tokens=372 full_history_shadow_tokens=13197
--- PASS: TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof (6.21s)
PASS
ok  	primeradiant.com/serf/agent	6.546s
```

## Contracts Proven

- The first real `agent.Session` turn created a public OpenAI Responses anchor.
- The second real `agent.Session` turn selected `responses_delta`.
- The delta request included `previous_response_id`.
- The delta request omitted the first-turn proof marker from provider `input`.
- The paired full-history shadow request included both proof markers.
- Public OpenAI accepted the delta request.
- A direct invalid `previous_response_id` request failed and was classified as `continuation_rejected`.
- The transcript persisted a hashed previous response handle and a positive full-history input-token estimate.

## Payload Metrics

- `responses_delta` raw request body bytes: `54736`
- full-history shadow raw request body bytes: `55049`
- omitted serialized input-item bytes: `406`
- continuation overhead bytes: `81`
- net request-body byte saving: `313`
- observed delta provider input tokens: `372`
- full-history shadow input-token estimate: `13197`
- observed delta token ratio versus full-history estimate: `2.82%`

## Rollout Thresholds

Initial public OpenAI enablement should be reverted or disabled if any threshold is breached over a meaningful production sample:

- eligible-hit-rate floor: at least `70%` of otherwise eligible post-anchor public OpenAI text rounds select `responses_delta`.
- prompt-cache hit-rate floor: at least `0%` cached-token rate; continuation does not require prompt-cache hits, but cache telemetry must remain present when the provider reports it.
- storage-quota/error ceiling: at most `1%` of eligible public OpenAI rounds fail due to provider storage, expired anchor, missing anchor, or continuation rejection; invalid anchors must never be accepted as fresh context.
- provider-token/cost ceiling: average delta provider input tokens must stay at or below `50%` of the paired full-history estimate for continuation-selected rounds.
- rate-limit ceiling: continuation-attributed `429` or quota errors must stay at or below `1%` of eligible public OpenAI rounds.

## Follow-Up

Phase 12B may enable only the public OpenAI registry row with:

- `StorageShapeProven: true`
- `ProductionPathProven: true`
- `Enabled: true`
- `MaxAnchorAgeSeconds: 3600`
- `StorageShapeProofID: "2026-06-24-responses-continuation-phase-0b"`
- `ProductionPathProofID: "2026-06-24-responses-continuation-phase-12a-public"`

The Codex backend registry row remains disabled until a separate Codex live proof exists.
