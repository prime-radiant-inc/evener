# Responses Continuation Phase 12B Public Enablement Proof

## Scope

Enabled the default Responses continuation registry row for public OpenAI only, after the Phase 12A live proof artifact recorded accepted anchor semantics, payload reduction, invalid-anchor rejection, provider token observations, and rollout thresholds.

Codex backend continuation remains disabled.

## Registry Values

Public OpenAI `/v1/responses`:

```go
ResponsesEndpointFamilyOpenAIPublic: {
	EndpointFamily:        ResponsesEndpointFamilyOpenAIPublic,
	StorageShapeProven:    true,
	ProductionPathProven:  true,
	Enabled:               true,
	MaxAnchorAgeSeconds:   3600,
	StorageShapeProofID:   "2026-06-24-responses-continuation-phase-0b",
	ProductionPathProofID: "2026-06-24-responses-continuation-phase-12a-public",
}
```

Codex backend `/backend-api/codex/responses`:

```go
ResponsesEndpointFamilyOpenAICodex: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex)
```

## Proof Dependency

Phase 12A artifact:

- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-public.md`

That artifact records:

- endpoint family: `openai_public`;
- model: `gpt-5.2`;
- accepted `responses_delta` request with `previous_response_id`;
- invalid-anchor rejection classified as `continuation_rejected`;
- delta raw request bytes: `54736`;
- full-history shadow raw request bytes: `55049`;
- omitted serialized input-item bytes: `406`;
- continuation overhead bytes: `81`;
- net request-body byte saving: `313`;
- first-run delta provider input tokens: `372`;
- first-run full-history shadow input-token estimate: `13197`;
- completion-audit delta provider input tokens: `11127`;
- completion-audit full-history shadow input-token estimate: `13196`;
- proposed `MaxAnchorAgeSeconds`: `3600`;
- numeric rollout thresholds for hit rate, prompt cache telemetry, storage/continuation errors, provider token cost, and rate limits.

## Evidence

```sh
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestDefaultResponsesContinuationSupportRegistry|TestDecideResponsesContinuation|TestSession_OpenAIResponsesContinuationPhase9|TestSession_OpenAIResponsesContinuationPhase10' -count=1 -v
```

Result:

```text
ok  	primeradiant.com/serf/llm	0.328s
ok  	primeradiant.com/serf/agent	1.567s
```

## Contracts Proven

- The default registry enables public OpenAI with the exact Phase 0B and Phase 12A proof IDs.
- The default registry keeps Codex disabled and unproven.
- `auto` mode with default public OpenAI support selects `responses_delta`.
- `auto` mode with default Codex support uses `full_history`.
- Explicit `ConversationID` still disables continuation for a request.
- Existing Phase 9 and Phase 10 deterministic session continuation contracts still pass after the default public enablement.
