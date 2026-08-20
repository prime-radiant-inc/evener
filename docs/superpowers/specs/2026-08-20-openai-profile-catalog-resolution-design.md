# Catalog Resolution for the OpenAI Responses Profile

## Goal

Make `NewOpenAIProfile` resolve its model metadata from the model catalog, and
give Bedrock's OpenAI inference-profile IDs a catalog key to resolve to.

One root cause produces four wrong values today. `NewOpenAIProfile`
(`agent/provider/profile.go:814`) consults the catalog for exactly one field —
effort levels, via `resolveEffortLevels` (`profile.go:16-24`) — and hardcodes
the rest: `contextWindow: 400_000` (`:820`) and `webSearch: true` (`:824`).
Separately, Bedrock addresses models as inference-profile IDs that LiteLLM has
no key for, so even the one catalog-driven field misses.

The immediate symptom is that Evener cannot talk to Bedrock at all:

```
bedrock error (status=0): responses.create(stream): web search is not supported
for this request
```

But web search is the least of it. On a Bedrock instance today:

| Value | Now | Correct |
| --- | --- | --- |
| Reasoning effort `max` | silently clamped to `xhigh` | `max` |
| Context window | 400,000 | 272,000 |
| Max output tokens | 0 | 128,000 |
| Pricing | absent (`cost_usd` null) | $1.10/$6.60 per M |
| Web search | requested, request rejected | not requested |

The effort clamp is the dangerous one. `resolveEffortLevels` falls back to the
profile default `low/medium/high/xhigh` (`:827`), which has no `max`;
`ClampReasoningEffort` (`llm/types.go:699`) finds no level of rank ≥ `max` (6)
and returns the highest available, `xhigh` (5). A run labelled "luna-max" would
silently be an xhigh run.

## Design

### 1. Give Bedrock's OpenAI IDs a catalog key

LiteLLM already carries the data, under `bedrock_mantle/openai.gpt-5.6-luna`:
pricing, `max_input_tokens: 272000`, `max_output_tokens: 128000`,
`supports_reasoning`, `supports_prompt_caching`,
`supported_endpoints: ["/v1/responses"]`.

The gap is naming, and it is narrow. LiteLLM files Anthropic's Bedrock models
under region-prefixed keys that match what Bedrock advertises, but files
OpenAI's under a `bedrock_mantle/` key with no such alias:

| Bedrock inference-profile ID | resolves today |
| --- | --- |
| `us.anthropic.claude-opus-5` | yes |
| `global.anthropic.claude-opus-5` | yes |
| `us.anthropic.claude-sonnet-4-6` | yes |
| `us.openai.gpt-5.6-luna` | **no** |
| `global.openai.gpt-5.6-luna` | **no** |

So this is not a general "normalize Bedrock IDs" problem. Add one alias step to
`LookupModelInfo`'s existing fallback chain (`llm/model_catalog.go:121-150`),
after the exact lookup and the last-path-segment retry: an ID matching
`us.openai.<rest>` or `global.openai.<rest>` retries as
`bedrock_mantle/openai.<rest>`.

Deliberately not a general region-prefix stripper. Exact match runs first, so a
general rule would never fire for the Anthropic IDs anyway, and a rule broad
enough to rewrite arbitrary `us.*` IDs risks shadowing a legitimate model whose
name happens to start that way. Refreshing the vendored snapshot does not remove
the need for this: upstream LiteLLM still has no `us.openai.*` keys.

### 2. Resolve context window and web search from the catalog

Extend `NewOpenAIProfile` to take both from the catalog when it has an opinion,
mirroring `resolveEffortLevels` exactly: a catalog value wins, silence falls
back to the constructor default. Two small helpers alongside the existing one,
not a new mechanism.

Web search is safe to make catalog-driven: across both catalog files, 55
OpenAI-family models declare `supports_web_search: true` and **zero** declare
false. The change is a no-op for every model shipping today and can only alter
what we explicitly ship an entry for.

Context window is **not** a no-op, and that is the point — see "Behavior change"
below.

### 3. Ship the two fields LiteLLM lacks

`applyOverrides` (`llm/model_catalog_embedded.go:64-70`) overlays per field onto
a matching entry, so an override keyed to the Bedrock LiteLLM ID adds metadata
without discarding upstream pricing or context:

```json
"bedrock_mantle/openai.gpt-5.6-luna": {
  "_note": "Bedrock serves these over /v1/responses without OpenAI's hosted tools.",
  "reasoning_effort_levels": ["low", "medium", "high", "xhigh", "max"],
  "supports_web_search": false
}
```

Same for `gpt-5.6-sol` and `gpt-5.6-terra`. `refresh-model-catalog.sh` forbids
hand-editing the vendored snapshot and names this file as where Evener-specific
metadata belongs, so this is the sanctioned layer.

Note what is *not* in that entry: context window and pricing. Those come from
upstream and stay current through the refresh script.

### 4. Context window stays per serving path

`gpt-5.6-luna` has three different real context windows depending on how it is
served: 272,000 on Codex/OAuth, 1,050,000 on the public API, 272,000 on Bedrock.
A single catalog number cannot be right for all three, and the existing
`gpt-5.6-luna` override records the OAuth figure per Jesse's 2026-07-28
decision, sourced from codex-cli 0.145.0's `models_cache.json`.

That override is unchanged. Bedrock does not read it — the alias resolves
Bedrock IDs to `bedrock_mantle/openai.*`, a different key, which carries
Bedrock's own 272,000. Each path gets its own answer from its own entry, with no
new precedence rule.

## Behavior change

Making context window catalog-driven changes the OAuth path: `gpt-5.6-luna` goes
from the hardcoded 400,000 to the override's 272,000.

This is a correction, not a regression. The catalog `context_window` is consumed
at `profile.go:956` (kimi), `:1051-1076` (openai-compat), and `:449`
(`WithLiveModelInfo`, which never runs for the `openai` tag because
`isOpenAICompatTag` excludes it, `cmdutil/cmdutil.go:41-47`). None of those
reach `NewOpenAIProfile`. The 2026-07-28 decision has therefore never been in
force on the openai/responses path, which has been over-reporting its window by
47% — the direction that risks building a request the model rejects.

It is still a live behavior change to the path the Terminal-Bench baseline runs
on, so it must not land mid-measurement. Land it after the in-flight 89-task run
completes, and treat the next full run as a new baseline rather than a
continuation.

## Validation

- `LookupModelInfo` table test: `us.openai.gpt-5.6-luna` and
  `global.openai.gpt-5.6-luna` resolve to the Bedrock entry; `us.anthropic.*`
  and `global.anthropic.*` still resolve by exact match and are byte-identical
  to today; an unrelated `us.`-prefixed ID that matches nothing still returns
  nil. The alias must not shadow an exact hit.
- Effort resolution through `NewOpenAIProfile` for a Bedrock ID yields levels
  including `max`, and `llm.ClampReasoningEffort("max", levels)` returns `max`.
  This is the failing test that proves the clamp; it fails today.
- `NewOpenAIProfile` context window and web search: catalog value wins, catalog
  silence falls back to the constructor default. Assert an explicit `true` for
  web search as well as `false`, so the resolution is proven to carry a value
  rather than only ever suppressing.
- A regression test pinning OAuth `gpt-5.6-luna` to 272,000 through the profile,
  so the 2026-07-28 decision is enforced by a test rather than by a catalog
  entry nothing reads.
- `applyOverrides` overlay test: the Bedrock entry gains effort levels and
  `supports_web_search` while retaining LiteLLM's pricing and
  `max_input_tokens`.
- Confirm no other model's resolution moves: run the existing catalog and
  profile suites and diff resolved metadata for a sample across providers.
- `go test ./llm/... ./agent/provider/... -count=1`, then `make lint` and the
  full gate.

Acceptance is a live Bedrock session on an unpatched binary, asserting from the
API log that the request carries `reasoning_effort: max` and no `web_search`
tool. The earlier rehearsal used a throwaway patch to `profile.go:824` and
proved only that the endpoint works once web search is gone; it exercised
neither the catalog path nor the effort ladder.

## Follow-ups, deliberately not bundled

- **Refresh the vendored LiteLLM snapshot.** `--check` reports +72 added, −1
  removed (`replicateopenai/gpt-oss-20b`), ~430 changed — clean, and it brings
  Bedrock pricing current. Its own commit, after the benchmark, so a measurement
  is never taken across a moving catalog.
- **Terminal-Bench cannot use Bedrock yet.** The eval adapter uploads only the
  binary, OAuth record, and CA bundle
  (`harbor-runner/src/harbor_runner/serf_agent.py:59-70`), sets only
  `SSL_CERT_FILE` and `XDG_STATE_HOME`, and hard-validates
  `model_name == "openai/gpt-5.6-luna"`. No providers.toml reaches the
  container. That is harbor-runner work.
- **Credential hazard.** `CredentialTag` (`llm/providercfg/providercfg.go:177-186`)
  returns the behavior tag unless it is `openai-compatible`; for `responses` the
  tag is `openai`, so the base_url-aware escape hatch never applies and
  `cmdutil/load_client.go:98` assigns the operator's OpenAI credential to the
  instance. An openai/responses instance with a non-OpenAI `base_url` must set
  `api_key` or `credential_headers` explicitly or it bearer-sends an OpenAI key
  to a third party. Pre-existing; documentation fix.
- **Bedrock model-shape gating.** `responsesLiteModel`
  (`llm/providers/openai/responses.go:956`) and
  `openAIModelSupports24hPromptCache` (`agent/session.go:1212`) match bare
  `gpt-5.6`/`gpt-5` prefixes, which Bedrock inference-profile IDs never match, so
  this model gets neither the gpt-5.6 request shape nor an Evener-sent
  `prompt_cache_key`. Bedrock's own automatic caching covered the latter in
  testing (uncached prompt tokens fell from 13,882 to 60-160 across ten steps),
  so this is a correctness tidy, not a cost problem.

## Out of scope

Anthropic models on Bedrock. Their catalog IDs already resolve, but Bedrock's
OpenAI-compatible endpoint returns 404 for them on both `/responses` and
`/chat/completions` — it serves OpenAI models only. Reaching Claude on Bedrock
needs a native Converse/InvokeModel adapter with SigV4, which is a provider
adapter project, not a catalog change.

Also out of scope: making Bedrock a first-class provider type, Bedrock SigV4
authentication (the endpoint accepts a bearer API key), and any `providers.toml`
surface for web search — the catalog already models this capability, and a
second source of truth beside it would owe a precedence rule against
`resolveOpenRouterAnthropicWebSearch` (`agent/provider/profile.go:997-1035`).
