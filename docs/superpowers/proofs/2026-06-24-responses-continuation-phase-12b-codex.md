# Responses Continuation Phase 12B Codex Non-Enablement Proof

## Scope

Closed the Phase 12B-codex gate without enabling the ChatGPT/Codex backend registry entry, because the matching Phase 12A-codex live proof did not pass.

Endpoint family: `openai_codex`

## Proof Dependency

Phase 12A-codex artifact:

- `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-codex.md`

That artifact records a fresh live run where the Codex backend created an anchor response but rejected the valid continuation request with:

```text
Unsupported parameter: previous_response_id
```

## Registry State

Codex backend `/backend-api/codex/responses` remains disabled:

```go
ResponsesEndpointFamilyOpenAICodex: disabledResponsesContinuationSupport(ResponsesEndpointFamilyOpenAICodex)
```

The public OpenAI row remains independently enabled by the public Phase 12A/12B artifacts.

## Evidence

Deterministic registry coverage asserts this split:

- public OpenAI is enabled with proof IDs and a bounded anchor age;
- Codex is present but disabled, unproven, and has no max anchor age or proof IDs.

Test:

```sh
GOCACHE=/tmp/serf-gocache go test ./llm -run 'TestDefaultResponsesContinuationSupportRegistryPublicEnabledCodexDisabled|TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge' -count=1 -v
```

## Decision

Do not flip `openai_codex` to `Enabled=true`.

Codex backend continuation remains a future endpoint-family project. A future enablement attempt must start by re-running or replacing the Phase 12A-codex live proof and must only proceed if valid `previous_response_id` is accepted.
