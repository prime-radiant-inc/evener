# Catalog Resolution for the OpenAI Responses Profile

## Goal

Let Evener talk to Amazon Bedrock's OpenAI-compatible Responses endpoint, by
giving its inference-profile IDs a catalog key to resolve to and by taking the
web-search capability from the catalog instead of hardcoding it.

Two things break today. `NewOpenAIProfile` (`agent/provider/profile.go:841`)
hardcodes `webSearch: true`, and Bedrock rejects the entire request — not just
the tool — when OpenAI's hosted `web_search` is present:

```
bedrock error (status=0): responses.create(stream): web search is not supported
for this request
```

Underneath that sits a quieter failure. Bedrock's IDs resolve no catalog entry
at all, so `resolveEffortLevels` falls back to the profile default
`low/medium/high/xhigh` (`:854`), and `ClampReasoningEffort`
(`llm/types.go:699`) finds no level ranked at or above `max` (6) and returns the
highest it knows, `xhigh` (5). A run asked for `max` silently becomes an xhigh
run — a mislabeled measurement, which is worse than a failed one.

## Design

### 1. Give Bedrock's OpenAI IDs a catalog key

LiteLLM already carries the data under `bedrock_mantle/openai.gpt-5.6-luna`:
pricing, context, output cap, `supports_reasoning`, `supports_prompt_caching`,
`supported_endpoints: ["/v1/responses"]`.

The gap is naming, and it is narrow. LiteLLM files Anthropic's Bedrock models
under region-prefixed keys matching what Bedrock advertises, and files only
OpenAI's under a `bedrock_mantle/` key with no such alias:

| Bedrock inference-profile ID | resolved before |
| --- | --- |
| `us.anthropic.claude-opus-5` | yes |
| `global.anthropic.claude-opus-5` | yes |
| `us.openai.gpt-5.6-luna` | **no** |
| `global.openai.gpt-5.6-luna` | **no** |

`bedrockOpenAICatalogKey` (`llm/model_catalog.go`) maps `us.openai.<rest>` and
`global.openai.<rest>` to `bedrock_mantle/openai.<rest>`, wired as a fallback in
`LookupModelInfo` after the exact, last-segment, and dated-family lookups. Those
two scopes are the complete set for OpenAI, not a sample — verified against
`ListInferenceProfiles`, which returns only `us` and `global` for OpenAI models.
Anthropic's models do use further scopes (`eu.`/`apac.`/`au.`/`jp.`), which is
why this maps OpenAI's names specifically rather than region prefixes generally.

Refreshing the vendored snapshot does not remove the need for it: upstream still
publishes no `us.openai.*` keys.

### 2. Resolve web search from the catalog

`resolveWebSearch` mirrors the existing `resolveEffortLevels`: presence-aware,
so a catalog value wins and catalog silence keeps the provider default. Safe by
inspection — across both catalog files, 55 OpenAI-family models declare
`supports_web_search: true` and zero declare false, so this changes no model
shipping today and can only alter what we explicitly ship an entry for.

### 3. Ship the two fields LiteLLM lacks

`applyOverrides` overlays per field onto a matching entry, so an override keyed
to the Bedrock LiteLLM ID adds metadata without discarding upstream pricing or
context. `bedrock_mantle/openai.gpt-5.6-{luna,sol,terra}` each gain the effort
ladder including `max`, and `supports_web_search: false`.

Only those three. The other six `bedrock_mantle/openai.*` entries
(`gpt-5.5`, the `gpt-oss` family) are not reachable: probing
`us.openai.gpt-5.5` and `us.openai.gpt-oss-120b` returns *"The provided model
identifier is invalid"*, so they are catalog rows without a serving path. Adding
capability flags for them would be speculation.

### 4. Rebuild the profile on model change

`rebuildOnSameProviderChange` did not list `openai`, so `WithModel`
shallow-cloned and carried the old model's answers forward. That was already
wrong before this change — the effort ladder has been catalog-derived since
`resolveEffortLevels` existed — and adding a second catalog-derived field makes
it load-bearing. `openai` now rebuilds through `NewOpenAIProfile`, alongside the
openai-compat family and openrouter-anthropic.

Without this, both fixes fail open on every model switch, model-fallback hop
(`agent/session_model_call.go:862`), and subagent model override
(`agent/subagent_model_selection.go`): a switch onto a Bedrock model restores
the tool that makes the endpoint reject every request.

## Explicitly not in this change

**Context window.** An earlier draft also took `contextWindow` from the catalog,
to put the 272,000 effective Codex/OAuth limit into force. That was removed, for
two reasons found in review:

- It does not work. `fillLiveModelMetadata` (`agent/live_model_metadata.go:41`)
  runs unconditionally from `NewSession`, with no behavior-tag gate, and
  `WithLiveModelInfo` overwrites the catalog window with the live one. Evener's
  own Codex fixture reports `max_context_window: 400000`
  (`llm/providers/openai/adapter_test.go:4216`), so a live session would still
  have reported 400,000 while a constructor-level test claimed otherwise.
- It regressed the bare `gpt-5.6` slug from 400,000 to LiteLLM's public-API
  1,050,000, while Codex rewrites that slug to `gpt-5.6-sol` — which the same
  change pins at 272,000. It also raised ~18 other OpenAI models above 400,000,
  inconsistent with the override file's own stated Codex/OAuth policy.

Getting it right means settling live-metadata precedence against explicit
configuration and pinning the bare slug. That is its own change with its own
spec. The web-search and effort fixes are unaffected: the plain `/v1/models`
mapping (`llm/providers/openai/models.go:86-96`) populates neither
`SupportsWebSearch` nor `ReasoningEffortLevels`, so live enumeration cannot
clobber them.

**Anthropic on Bedrock.** Their catalog IDs already resolve, but Bedrock's
OpenAI-compatible endpoint returns 404 for Anthropic models on both
`/responses` and `/chat/completions` — it serves OpenAI models only. Reaching
Claude on Bedrock needs a native Converse/InvokeModel adapter with SigV4.

## Validation

- `LookupModelInfo`: `us.openai.*` and `global.openai.*` resolve to the Bedrock
  entry; `us.anthropic.*` and `global.anthropic.*` still resolve by exact match;
  an unknown `us.openai.*` still returns nil.
- `resolveWebSearch` presence-awareness, tested white-box with a provider
  default that *opposes* the catalog. Asserting a catalog `true` against a
  `true` default would pass even if the helper ignored the catalog entirely —
  the original version of this test did exactly that, and a mutation test
  caught it.
- Profile-level: all four Bedrock GPT-5.6 IDs report web search off; the effort
  ladder contains `max` and `ClampReasoningEffort("max", …)` returns `max`; an
  uncatalogued model keeps the constructor default.
- Model switch in both directions: onto a Bedrock model the capability must go
  off and the ladder must gain `max`; away from one it must come back.

Acceptance is a live Bedrock session on an unpatched binary, asserting from the
API log that the request carries `reasoning_effort: max` and no `web_search`
tool. The earlier rehearsal used a throwaway patch and proved only that the
endpoint works once web search is gone.

## Follow-ups

- **Context window per serving path**, as above — the substantive one.
- **Terminal-Bench cannot use Bedrock yet.** The eval adapter uploads only the
  binary, OAuth record, and CA bundle
  (`harbor-runner/src/harbor_runner/serf_agent.py:59-70`) and hard-validates
  `model_name == "openai/gpt-5.6-luna"`. No providers.toml reaches the
  container.
- **Credential hazard.** `CredentialTag` (`llm/providercfg/providercfg.go:177-186`)
  returns the behavior tag unless it is `openai-compatible`; for `responses` the
  tag is `openai`, so the base_url-aware escape hatch never applies and the
  operator's OpenAI credential is assigned to the instance. An openai/responses
  instance with a non-OpenAI `base_url` must set `api_key` or
  `credential_headers` explicitly or it bearer-sends an OpenAI key to a third
  party. Pre-existing; needs documenting.
- **Bedrock model-shape gating.** `responsesLiteModel`
  (`llm/providers/openai/responses.go:956`), `openAIModelSupports24hPromptCache`
  (`agent/session.go:1212`), and `reasoningSummaryLevel`
  (`responses.go:1009`) all match bare `gpt-5`/`gpt-5.6` prefixes that Bedrock
  IDs never match, so this model gets neither the gpt-5.6 request shape, nor an
  Evener-sent `prompt_cache_key`, nor `summary: "detailed"`. Bedrock's own
  automatic caching covered the second in testing.
