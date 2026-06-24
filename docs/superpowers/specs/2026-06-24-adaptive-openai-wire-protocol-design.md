# Adaptive OpenAI Wire Protocol Design

Date: 2026-06-24
Status: draft for Jesse review
Scope: OpenAI-shaped Serf providers that can plausibly speak Chat Completions, Responses, or both.

## Problem

Serf currently treats OpenAI-shaped providers as fixed-wire adapters:

- `openai` with `api_style="responses"` uses the OpenAI Responses adapter.
- `openai` with `api_style="chat-completions"` and the legacy `openai-compatible` provider use the Chat Completions adapter.
- Responses continuation enablement is keyed to hardcoded endpoint families: public OpenAI is enabled, Codex remains disabled, and there is no generic OpenAI-compatible Responses family.

That is too rigid for real OpenAI-compatible deployments. Some providers only expose `/chat/completions`; some expose `/responses`; some expose both but support different subsets by model, auth scope, or deployment. Serf should be able to adapt at runtime when configured to do so, while keeping failures observable and avoiding silent semantic loss.

## Goals

- Allow OpenAI-shaped providers to change runtime wire protocol between Responses and Chat Completions.
- Detect Responses availability and feature support per endpoint, auth scope, model, and relevant request shape.
- Prefer Responses when it is proven usable for the requested operation; fall back to Chat Completions when the provider clearly does not support it.
- Detect second-level Responses features separately: `previous_response_id`, continuation storage semantics, encrypted reasoning replay, prompt-cache controls, service tier, hosted tools, and other Responses-only fields.
- Keep provider identity stable in transcripts and API logs while recording the selected wire protocol and fallback reason.
- Keep default tests deterministic. Live probes must require explicit opt-in or be exercised through scripted fake providers.

## Non-Goals

- Do not silently downgrade Responses-only semantics when Chat Completions has no equivalent.
- Do not persist learned capabilities into `providers.toml`; that file remains declarative configuration, not mutable runtime state.
- Do not enable continuation for arbitrary compatible endpoints until storage and anchor semantics are independently proven for that endpoint scope.
- Do not make provider credentials alone trigger default live network tests.
- Do not redesign Anthropic, Google, or non-OpenAI-shaped providers.

## Configuration Model

Extend the existing OpenAI provider `api_style` field with an explicit adaptive value:

- `api_style = "responses"`: force Responses; fail if unsupported except for existing explicit OpenAI chat fallback behavior where already intended.
- `api_style = "chat-completions"`: force Chat Completions.
- `api_style = "auto"`: prefer Responses, detect support, and fall back to Chat Completions when safe.

The legacy env-seeded `openai-compatible` provider keeps the existing Chat Completions behavior for compatibility. Adaptive behavior requires a config-driven OpenAI-shaped instance with `api_style = "auto"` or a future explicit env flag that seeds that same config shape. Existing deployments must not unexpectedly hit `/responses`.

## Capability Scope

Capability results must be scoped tightly enough that one successful probe cannot overgeneralize:

- provider instance name;
- normalized base URL;
- auth scope fingerprint;
- model;
- selected endpoint path;
- relevant feature or feature set;
- request-shape fingerprint when feature support depends on fields.

The cache should live under Serf state, not the repo or provider config. It should include a timestamp, status, failure class, and proof metadata such as the probed endpoint and method. Use a bounded TTL so transient provider changes recover without manual cleanup.

## Runtime Flow

For adaptive `auto` profiles:

1. Build the Responses candidate request using the existing OpenAI Responses request builder.
2. Check the capability cache for a matching positive or negative result.
3. If unknown, attempt Responses on the real request rather than issuing a separate startup probe.
4. On success, cache Responses support and continue with that wire protocol.
5. On clear unsupported endpoint, unsupported model, unsupported parameter, or empty unsupported stream, cache the negative result and retry through Chat Completions when the request can be represented safely.
6. On transient errors, auth errors, quota errors, or ambiguous provider failures, do not mark Responses unsupported; return or retry according to existing error policy.
7. Record each attempt in API logging with provider identity, selected wire protocol, endpoint URL, capability cache state, and fallback reason.

Manual probe tooling can come later as a separate command, but the core behavior should be lazy and runtime-driven.

## Fallback Safety

Fallback is allowed only when the requested semantics have a known Chat Completions equivalent. Examples:

- Plain text, ordinary tool calls, temperature/top-p/max-token controls, and stop sequences can generally fall back.
- `previous_response_id`, provider-side conversation handles, encrypted reasoning replay, hosted web search, Responses-only file/content items, and continuation-owned storage cannot be silently downgraded.
- If a request depends on an unsupported Responses-only feature and no equivalent exists, Serf must fail clearly with the selected provider, endpoint, feature, and fallback decision.

This keeps adaptive transport from hiding correctness bugs behind a successful but semantically different Chat Completions response.

## Responses Continuation

Continuation is a feature layered on top of Responses support, not implied by it.

An adaptive provider can become eligible for continuation only after all of these are true for the scoped endpoint/model/auth shape:

- Responses requests are accepted.
- Full-history anchor requests return durable response IDs.
- Valid `previous_response_id` follow-up requests are accepted.
- Invalid anchors fail clearly rather than being silently treated as fresh context.
- Storage behavior is classified and compatible with Serf's continuation storage policy.
- Deterministic session tests cover request shaping and fallback, and live opt-in tests record the provider behavior.

Until that proof exists, adaptive providers may use Responses for ordinary requests but must keep continuation disabled.

## First-Party OpenAI

First-party API-key OpenAI should still default to Responses, because that is the already-proven path for public OpenAI continuation. Adaptive detection is still useful for:

- model-specific unsupported Responses behavior;
- newly added Responses fields;
- distinguishing transient failures from unsupported feature failures;
- avoiding hardcoded assumptions when the selected model or account lacks a capability.

ChatGPT/Codex backend remains a distinct case. Current upstream Codex source shows HTTP `/backend-api/codex/responses` does not accept `previous_response_id`; Codex continuation would require a separate WebSocket-specific implementation and proof.

## OpenAI-Compatible Providers

OpenAI-compatible providers should be allowed to change runtime protocol only in adaptive mode. The existing `openai-compatible` Chat Completions behavior should remain available and stable.

For adaptive compatible providers, Serf should try `/responses` first when the request is representable and the cache does not already say it is unsupported. If `/responses` is not available, Serf should fall back to `/chat/completions` for safe request shapes. This allows vLLM, LiteLLM, local gateways, and provider proxies to use richer Responses behavior when they expose it without breaking providers that only implement Chat Completions.

## Testing

Default tests:

- Use fake HTTP servers to cover Responses success, Responses unsupported then Chat fallback, transient failure with no unsupported cache write, and Responses-only feature rejection without fallback.
- Verify capability-cache scoping by model, base URL, auth scope, and feature.
- Verify API log records include selected wire protocol, endpoint URL, fallback reason, and cache status.
- Verify existing fixed Chat Completions and fixed Responses modes do not change.

Live tests:

- Keep first-party OpenAI and compatible-provider live probes opt-in with explicit `SERF_*_E2E=1` variables.
- Provider credentials alone must never trigger live detection tests.
- Live artifacts should record model, base URL family, selected transport, feature results, and downgrade reasons.

## Rollout

Start with adaptive detection disabled by default for legacy `openai-compatible` env registration. Add explicit config-driven adaptive mode first, then consider opt-in env wiring after the behavior is proven.

The first production-safe slice should support:

- ordinary Responses availability detection;
- safe Chat Completions fallback;
- capability cache and logging;
- no generic continuation enablement.

Continuation for adaptive compatible endpoints should be a later endpoint-family proof, not part of the initial transport adaptation.
