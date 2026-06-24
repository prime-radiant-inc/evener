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

## Upstream Codex Source Check

Checked the current OpenAI Codex source at upstream commit `3694b48a82343d94b58c62fa9af335710c3a6187`.

Relevant findings:

- The normal HTTP Responses request type, `ResponsesApiRequest`, has no `previous_response_id` field.
- `previous_response_id` exists only on the WebSocket `ResponseCreateWsRequest`.
- Codex sets `previous_response_id` from the previous completed WebSocket response when reusing a Responses WebSocket connection and sending an incremental delta.
- ChatGPT/Codex auth modes default to `https://chatgpt.com/backend-api/codex`, and HTTP requests append `responses`, producing `/backend-api/codex/responses`.

This means the live backend rejection is consistent with Codex's own HTTP request shape. Serf's HTTP Codex adapter must not send `previous_response_id` or `conversation` to `/backend-api/codex/responses`; a future Codex continuation project would need a separate Responses WebSocket transport and proof, not a public `/v1/responses` HTTP continuation clone.

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

Codex backend continuation remains a future endpoint-family project. A future enablement attempt must start by re-running or replacing the Phase 12A-codex live proof and must only proceed if valid continuation is accepted on the actual transport being implemented.
