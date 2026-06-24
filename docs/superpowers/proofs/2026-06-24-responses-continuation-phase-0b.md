# Responses Continuation Phase 0B Discovery Proof

Date: 2026-06-24
Scope: OpenAI Responses continuation Phase 0B-discovery

## Deterministic Request-Shape Matrix

Checkable line: public OpenAI and Codex backend adapter fixtures cover `previous_response_id` only, `conversation` only, and both handles present.

Evidence:
- `llm/providers/openai/responses_continuation_discovery_test.go:TestResponsesContinuationDiscovery_RequestShapeMatrix`
- `llm/responses_continuation_test.go:TestDecideResponsesContinuationForRequestDisablesExplicitConversationID`
- `llm/responses_continuation_test.go:TestDecideResponsesContinuationForRequestAllowsNoConversationID`

Current deterministic matrix:
- Public OpenAI default emits `store:false` and no provider-state handles.
- Public OpenAI serializes trimmed `previous_response_id`.
- Public OpenAI serializes trimmed `conversation`.
- Public OpenAI serializes both handles together and preserves explicit `store:true`.
- Request-level continuation selection keeps explicit `ConversationID` traffic on `full_history` even when the endpoint family otherwise supports `responses_delta`.
- Request-level continuation selection permits `responses_delta` for public OpenAI traffic without an explicit `ConversationID` when endpoint support is enabled and bounded by max anchor age.
- Codex backend serializes trimmed `previous_response_id`, trimmed `conversation`, and both handles together.
- Codex backend preserves explicit `store:true` in deterministic adapter body construction; live discovery must decide whether that shape is accepted by the backend.
- Codex backend streaming is a dispatch-layer behavior and is covered by existing adapter tests, not by this `buildRequestBody` matrix.

## Deterministic Payload-Size Probe

Checkable line: scripted malformed-tool-call recovery delta body omits the historical `function_call`, includes the linked `function_call_output`, includes `previous_response_id`, and has positive net body-size reduction in the deterministic fixture.

Evidence:
- `llm/providers/openai/responses_continuation_discovery_test.go:TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe`

Payload-size result:
- `full_history_bytes`: 1360
- `responses_delta_bytes`: 281
- `gross_omitted_historical_item_bytes`: 1002
- `added_continuation_overhead_bytes`: 48
- `net_body_size_delta_bytes`: 1079

## Live Discovery Status

Checkable line: live discovery is explicit opt-in and blocks treating Phases 1A-11 as committed implementation work for a target endpoint family until the target endpoint family has accepted valid anchors, rejected invalid anchors clearly, resolved co-present `previous_response_id` plus `conversation`, and shown net request-payload reduction on the scripted probe.

Commands:
- Public OpenAI: `SERF_OPENAI_RESPONSES_DISCOVERY_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestAdapter_E2E_PublicResponsesContinuationDiscovery -count=1 -v`
- Codex backend: `SERF_OPENAI_CODEX_DISCOVERY_E2E=1 GOCACHE=/tmp/serf-gocache go test ./llm/providers/openai -run TestAdapter_E2E_CodexResponsesContinuationDiscovery -count=1 -v`

Observed status:
- Public OpenAI: run with an SSM-loaded API key and `SERF_OPENAI_RESPONSES_DISCOVERY_E2E=1`.
- Public OpenAI: valid `previous_response_id` was accepted, a second branch from the same anchor was accepted, and an invalid anchor returned an explicit `previous_response_not_found` error.
- Public OpenAI: co-present `previous_response_id` plus `conversation` was rejected with `mutually_exclusive_parameters`; the selected V1 runtime resolution is to use `full_history` whenever an explicit `ConversationID` is present.
- Codex backend: run with stored OAuth. `gpt-5.2` failed before anchor creation with `The 'gpt-5.2' model is not supported when using Codex with a ChatGPT account.`
- Codex backend: rerun with `gpt-5.4` after preserving streaming response metadata. Anchor creation returned a response ID, but the valid continuation request failed with `Unsupported parameter: previous_response_id`.

Go/no-go:
- Public OpenAI may proceed into deterministic Phases 1A-11 for the no-explicit-conversation V1 path, with runtime continuation still disabled until Phase 12A-public/12B-public.
- Codex backend is blocked for runtime enablement because the live endpoint rejected `previous_response_id`.

## SystemPromptAsUser Inventory

Checkable line: `SessionConfig.SystemPromptAsUser` is a runtime/session setting, not a property set by `NewOpenAIProfile`; continuation must remain full-history for any launch path that sets it true.

Evidence:
- `agent/session_model_call.go` branches on `s.cfg.SystemPromptAsUser` while building model messages.
- `agent/provider/profile.go` profiles do not carry a `SystemPromptAsUser` field.

Current inventory:
- Static code inventory found no OpenAI Responses profile constructor that forces `SystemPromptAsUser=true`.
- Real launch/profile usage for intended V1-public traffic is not measured by default tests. Runtime enablement remains blocked until the intended rollout path records whether `SystemPromptAsUser=true` is prevalent.

## Rough Eligible-Hit-Rate Blockers

Checkable line: broad runtime enablement is not committed until rollout traffic has an eligible-hit-rate expectation that clears the future Phase 12A floor, or Jesse explicitly accepts a parity-first narrow rollout.

Known blockers from the design:
- `SystemPromptAsUser=true`
- date-boundary prompt changes from `Today`
- unsupported item kinds such as media, provider-hosted files, reasoning items, or web-search inputs
- model changes
- storage-scope mismatches
- missing or expired anchor metadata

Current Phase 0B verdict:
- Deterministic adapter fixtures can land.
- Runtime continuation remains disabled.
- Public OpenAI no-explicit-conversation Phases 1A-11 can proceed.
- Public OpenAI requests with explicit `ConversationID` must use `full_history` in V1.
- Codex backend continuation remains blocked.
