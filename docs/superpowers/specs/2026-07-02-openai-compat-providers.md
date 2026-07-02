# OpenAI-Compatible Provider Support: Thinking Levels + Compat Layer

Status: **Approved by Jesse 2026-07-02** ("do all of that + the other compat
quirks") and **implemented** on this branch. See "Implementation notes" (§8)
for where the shipped code diverges from the design below, and the annotated
open questions (§6) and Pi-sweep table (§5) for what actually landed.
Branch: `bot/openaicompat-provider-design` (worktree `.worktrees/openaicompat-providers`).

## 1. Problem

Serf's OpenAI-compatible provider path (glm, kimi, openrouter, ollama, and
`type="openai"` + `api_style="chat-completions"` instances) handles reasoning
poorly for the growing class of gateway-served models (z.ai GLM, lunaroute,
LiteLLM proxies, vLLM):

1. **No per-model thinking-level mapping.** Serf clamps effort against a
   model's `ReasoningEffortLevels`, but has no way to say "user asks `xhigh`,
   this model wants the wire string `max`" or "this model can't turn thinking
   off". For uncataloged models the openai-compat profile defaults to
   `["low","medium","high"]` (`agent/provider/profile.go:1115`) — no
   `minimal`, no `xhigh`/`max`, ever.
2. **One wire format.** The adapter only emits top-level `reasoning_effort`
   (`llm/providers/openaicompat/request.go:60-67`). z.ai wants
   `thinking: {"type":"enabled"}` (its GLM models don't accept
   `reasoning_effort` at all except glm-5.2); DeepSeek wants
   `thinking:{"type":"enabled"}` too; OpenRouter wants `reasoning:{"effort"}`.
   Serf's GLM provider today never enables thinking explicitly and sends a
   field z.ai may reject or ignore.
3. **`reasoning_effort` is emitted unconditionally** whenever the session set
   an effort — there is no per-model "supports the parameter" gate on this
   path (the anthropic builder has one).
4. **No user-side model definitions.** `providers.toml` instances carry only
   `type/api_style/base_url/api_key/quirks` (`llm/providercfg/providercfg.go`).
   A new gateway model absent from the embedded catalog silently gets 128K
   context, 3 effort levels, and the default wire format. No knob can fix it
   without a serf release.
5. **Quirks are hardcoded presets.** `QuirksPreset(name)` recognizes a fixed
   set of names; nothing is composable from config.
6. **Streaming loses thinking** for providers that emit `reasoning` or
   `reasoning_text` deltas (serf only parses `reasoning_content`;
   `llm/providers/openaicompat/response.go:73-78`) or that stream
   `reasoning_details` (parsed non-stream only).

Reference user config we want to be able to express (from Pi):

```json
"lunaroute": {
  "baseUrl": "https://gw.lunaroute.com/v1",
  "api": "openai-completions",
  "apiKey": "$LUNAROUTE_API_KEY",
  "models": [{
    "id": "glm-5.2-nvfp4",
    "contextWindow": 1048576,
    "reasoning": true,
    "thinkingLevelMap": {"off": null, "minimal": "high", "low": "high",
                          "medium": "high", "high": "high", "xhigh": "max"},
    "compat": {"thinkingFormat": "zai", "maxTokensField": "max_tokens",
               "supportsReasoningEffort": false}
  }]
}
```

## 2. What Pi has (studied: `inspo/pi`)

- Canonical ladder `off | minimal | low | medium | high | xhigh`
  (`packages/ai/src/types.ts:74-76`). No `max` level — `"max"` appears only
  as a provider-side mapped value.
- `thinkingLevelMap: Partial<Record<level, string|null>>` per model:
  translates a Pi level to the provider's wire string; `null` = unsupported;
  `xhigh` is opt-in (supported only when explicitly mapped); unmapped levels
  pass through by name. `clampThinkingLevel` walks up-then-down the ladder.
- A per-model `compat` struct, discriminated by API type, auto-detected from
  `baseUrl` substrings and overlaid field-by-field from config
  (`openai-completions.ts:1179-1293`). ~18 flags for the completions API,
  including 10 `thinkingFormat` wire variants (`openai`, `zai`, `deepseek`,
  `openrouter`, `together`, `qwen`, `qwen-chat-template`, `chat-template`,
  `string-thinking`, `ant-ling`).
- User config (`models.json`): providers with `baseUrl/apiKey/api/headers/
  compat`, plus `models[]` (full defs) and `modelOverrides{}` (partial
  overrides of built-ins). Custom wins over built-in on (provider, id).
- Streaming: reasoning deltas read from the first non-empty of
  `reasoning_content` / `reasoning` / `reasoning_text`; the winning field
  name is remembered per block and replayed to the same field next turn.

## 3. What serf has

- Vocabulary centralized in `llm` (`llm/types.go:528-621`):
  `minimal/low/medium/high/xhigh/max` with **xhigh ≡ max** (both rank 5),
  disable-aliases (`none/off/...` → `""`), `ClampReasoningEffort` against a
  per-model `ReasoningEffortLevels` list, `ReasoningBudget` for token-budget
  providers. So the *ladder* is fine — the missing piece is per-model
  translation + provisioning of the levels list for compat models.
- Effort flows: session snapshot → `ClampReasoningEffort(effort,
  profile.ReasoningEffortLevels())` (`agent/session_model_call.go:636-642`)
  → `req.ReasoningEffort *string` → each provider's request builder.
- Model metadata: embedded LiteLLM catalog + serf overrides overlay
  (`llm/model_catalog_embedded.go`), which can materialize serf-only models
  (kimi-for-coding). Live enrichment via `ListModels` → `WithLiveModelInfo`.
- Per-instance `ProviderQuirks` presets (`llm/providers/openaicompat/quirks.go`):
  param locks, `ToolChoiceAutoOnly`, `MaxStopSequences`, `StripEmptyContent`,
  `NoJSONSchema`, `FinishReasonMap`, `TranslateMaxToXHigh`.
- Divergence bug found during this study: the anthropic builder's private
  `clampEffort` ladder (`llm/providers/anthropic/response.go:183`) is
  `low/medium/high/max` only — it passes `minimal`/`xhigh` through unclamped.

## 4. Design

Principles: keep serf's shipped vocabulary (xhigh≡max aliases); express
provider divergence as **data** (per-model/per-instance compat) with built-in
defaults, user-overridable in `providers.toml`; keep `llm.ClampReasoningEffort`
the single clamp — the new map layer *translates after clamping*, it does not
re-clamp.

### 4.1 Per-instance model definitions in providers.toml

```toml
[instances.lunaroute]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw.lunaroute.com/v1"
api_key = "$LUNAROUTE_API_KEY"        # see 4.5

[instances.lunaroute.models."glm-5.2-nvfp4"]
context_window = 1048576
max_output_tokens = 131072
reasoning = true
thinking_levels = { minimal = "high", low = "high", medium = "high",
                    high = "high", xhigh = "max" }   # off omitted = can disable by omission

[instances.lunaroute.models."glm-5.2-nvfp4".compat]
thinking_format = "zai"
supports_reasoning_effort = false
```

- Map-keyed tables (`models."<id>"`), not array-of-tables — no separate `id`
  field, natural TOML.
- A model entry both **overlays the catalog** (context window, max output,
  reasoning capability → `ModelInfo`, so `/api/models`, the effort chip, and
  the session clamp all see it with zero new plumbing) and **carries
  compat/levels to the adapter**.
- Precedence: instance model config > serf catalog overrides > LiteLLM
  catalog > profile defaults. Same "materialize if unknown" rule the serf
  overlay already uses.
- Instance-level `[instances.X.compat]` also allowed; per-model compat
  overlays it field-by-field.

### 4.2 Thinking-level map

New per-model field (config + `llm.ModelInfo`):
`ThinkingLevels map[string]*string` — key = canonical serf level
(`minimal/low/medium/high/xhigh`, plus optional `off`), value = wire string,
or explicit null = unsupported.

Semantics (deliberately simpler than Pi's partial-map defaults):

- **When a map is present it is the complete authority**: the supported
  ladder is exactly the keys with non-null values, in serf rank order. From
  it we *derive* `ReasoningEffortLevels`, so the existing clamp, spawn-form
  chip, task_list enum, and appwire validation keep working unchanged.
- **`off` key**: absent → thinking disabled by omitting the field (today's
  behavior); string (e.g. `"none"`) → send that value to disable; null →
  model cannot disable thinking (an always-on reasoner: serf treats
  effort="" as "send nothing", letting the provider default rule).
- **At request build** (adapter): after the session's clamp picks a level,
  the adapter translates `map[level]` → wire value. No map → level passes
  through by name (today's behavior, fully backward compatible).
- `max` accepted as an alias of `xhigh` in config keys (normalized at load),
  matching serf's rank table.
- Pi's "xhigh is opt-in" rule falls out naturally: no map → default levels
  (`low/medium/high`) as today; a model earns `xhigh`/`max` by declaring it.

### 4.3 Compat struct (evolve ProviderQuirks)

Extend the existing `ProviderQuirks` struct (no rewrite) with the
protocol-shape fields, and make it settable from TOML:

```go
type ProviderQuirks struct {
    // existing: LockTemperature, LockTopP, LockFrequencyPenalty,
    // LockPresencePenalty, ToolChoiceAutoOnly, MaxStopSequences,
    // StripEmptyContent, NoJSONSchema, FinishReasonMap, TranslateMaxToXHigh
    ThinkingFormat          string // "", "zai", "openrouter", "deepseek"
    SupportsReasoningEffort *bool  // nil = format default
    MaxTokensField          string // "" (=max_tokens) | "max_completion_tokens"
    ToolStream              bool   // z.ai tool_stream:true when tools present
}
```

Wire behavior per `thinking_format` (effort = post-clamp, post-map value):

| format | thinking ON | thinking OFF |
|---|---|---|
| `""`/`openai` (default) | `reasoning_effort: <effort>` if supports_reasoning_effort (default true here) | omit (or `reasoning_effort: <off-value>` if mapped) |
| `zai` | `thinking: {"type":"enabled"}` + `reasoning_effort: <effort>` only if supports_reasoning_effort (default false) | `thinking: {"type":"disabled"}` |
| `openrouter` | `reasoning: {"effort": <effort>}` | `reasoning: {"effort":"none"}` or omit per off-mapping |
| `deepseek` | `thinking: {"type":"enabled"}` + optional `reasoning_effort` | `thinking: {"type":"disabled"}` |

Initial scope: `openai`, `zai`, `openrouter`, `deepseek` — formats serf has
(or is about to have) real providers for. Pi's other six variants (qwen,
chat-template, together, string-thinking, ant-ling) are cheap to add later
inside the same switch; YAGNI now.

Layering: `QuirksPreset(name)` stays as the built-in base (the `glm-5`
preset gains `ThinkingFormat:"zai"`, `ToolStream:true`; `openrouter` preset
gains `ThinkingFormat:"openrouter"`), then instance `[instances.X.compat]`
overlays, then per-model compat overlays. No baseURL sniffing (Pi does this;
serf keys on `type` + preset, which is explicit and already there).

The `openrouter` preset's `TranslateMaxToXHigh` becomes redundant where a
model has a thinking_levels map, but stays as the blanket fallback.

### 4.4 Adapter plumbing

The adapter is constructed per-instance and needs per-model compat at
request time. Construction params grow a
`Models map[string]ModelCompat` (compat + thinking-levels keyed by model id)
from the instance config; `buildRequestBody` resolves the effective quirks for `req.Model` = instance
quirks overlaid by that model's entry. The
existing `ProviderOptions["openai-compatible"]` passthrough is unchanged and
still covers arbitrary body fields (so we do NOT need Pi's
openRouterRouting/vercelGatewayRouting structs).

`max_tokens` naming honors `MaxTokensField`. `max_output_tokens` from the
model entry becomes the default `max_tokens` when the request doesn't set
one — today compat requests omit it entirely, which several gateways treat
as "provider-side small default".

### 4.5 API-key env references

Adopt Pi's `$ENV_VAR` form for `providers.toml api_key` (literal `$` via
`$$`). Today the field is literal-only; gateway keys want env indirection
and Jesse's example config already assumes it. No `!command` execution
(serf has the credentials store for real secret management; running shell
commands from providers.toml is surface we don't need).

### 4.6 Streaming/response parsing

- Reasoning deltas: accept `reasoning_content`, `reasoning`,
  `reasoning_text` — first non-empty wins per Pi's dedup rationale
  (chutes.ai sends two with identical content). Remember the winning field
  name and replay assistant thinking to the same field on subsequent turns
  (serf currently hardcodes `reasoning_content` replay).
- Parse streamed `reasoning_details` deltas (currently non-stream only) so
  OpenRouter/MiniMax reasoning isn't lost in the streaming path.

### 4.7 Adjacent fixes (in scope, small)

- Unify the anthropic builder's private `clampEffort` ladder with the
  canonical vocabulary (it currently passes `minimal`/`xhigh` through
  unclamped — latent 400s).
- Gate `reasoning_effort` emission on the resolved
  `SupportsReasoningEffort` for compat models that declare it false.

## 5. Pi feature sweep — adopt / later / already-have / skip

| Pi feature | Verdict |
|---|---|
| thinkingLevelMap | **Adopt** (4.2) |
| thinkingFormat (10 variants) | **Adopt 4** now (4.3), rest later |
| supportsReasoningEffort, maxTokensField | **Adopt** (4.3) |
| Per-provider user model defs + overrides | **Adopt** (4.1, TOML-shaped) |
| zaiToolStream | **Adopt** (`ToolStream`, GLM tool streaming) |
| $ENV api keys | **Adopt** (4.5) |
| Multi-field reasoning delta parsing + same-field replay | **Adopt** (4.6) |
| contextWindow/maxTokens per model | **Adopt** (4.1; catalog overlay exists) |
| requiresAssistantAfterToolResult, requiresToolResultName, requiresThinkingAsText, requiresReasoningContentOnAssistantMessages | **Implemented this pass** (superseding the "Later" verdict below) — shipped as `CompatConfig.RequiresAssistantAfterToolResult` / `RequiresToolResultName` / `RequiresThinkingAsText` / `RequiresReasoningContentOnAssistant` (`llm/providercfg/providercfg.go`), wired through `ApplyCompatConfig` to `ProviderQuirks.RequireAssistantAfterToolResult` / `RequireToolResultName` / `ThinkingAsText` / `EmptyReasoningContentOnAssistant` (`llm/providers/openaicompat/compat.go`) and consumed in `toChatMessages` (`request.go`) |
| supportsStore / supportsDeveloperRole / stream_options gate | **Implemented this pass** — `CompatConfig.SupportsStore` / `SupportsDeveloperRole` / `SupportsUsageInStreaming`, applied as `ProviderQuirks.SendStoreFalse` / `UseDeveloperRole` / `OmitStreamUsage` (see §8 on the action-named field mismatch) |
| cacheControlFormat:"anthropic", supportsLongCacheRetention, session-affinity headers | ALL **implemented** across the two passes: `cache_control_format = "anthropic"` markers; then the follow-up waves added `supports_long_cache_retention` (prompt_cache_key derived from the session id + prompt_cache_retention 24h + cache_control ttl 1h) and `send_session_affinity_headers` (session_id/x-client-request-id/x-session-affinity) — `req.SessionID` was already on every request, so no new session plumbing was needed |
| chat_template_kwargs formats (local vLLM/qwen) | **Later** |
| baseURL compat auto-detection | **Skip** — serf's explicit type+preset covers it; less magic |
| openRouterRouting/vercelGatewayRouting structs | **Skip** — ProviderOptions passthrough already emits arbitrary body fields |
| !command key resolution | **Skip** — credentials store exists; don't execute config |
| Cost fields per user model | **Skip for now** — serf catalog has pricing; gateway models are usually flat/zero; add to model entry later if wanted |
| Per-model headers | **Skip** — instance DefaultHeaders exist; no known need |

## 6. Open questions for Jesse

Jesse's answer (2026-07-02): "do all of that + the other compat quirks."
Decisions taken, one per question:

1. **Config shape**: inline per-instance models in `providers.toml`
   (proposed) vs a separate models file. Inline keeps one config surface;
   a separate file would mirror Pi.
   **Decision: yes, inline** — `[instances.X.models."<id>"]` map-keyed
   tables, as proposed. No separate models file was built.
2. **Map authority**: proposed "map present = complete authority" (vs Pi's
   partial-map-with-defaults). Simpler, more explicit; costs a few extra
   keys in config.
   **Decision: yes** — shipped exactly as proposed
   (`providercfg.ModelConfig.ThinkingLevels` doc comment,
   `llm/providercfg/providercfg.go:95-98`); see §8 for the one refinement
   (no null values — TOML can't express them cleanly, so "absent key" is the
   unsupported marker instead of an explicit null).
3. **Initial format scope**: openai/zai/openrouter/deepseek — right cut?
   **Decision: grew twice.** The initial pass shipped seven formats
   (`openai/zai/deepseek/openrouter/together/qwen/string-thinking`); the
   follow-up waves added `qwen-chat-template` and `chat-template` (with a
   verbatim `chat_template_kwargs` compat table) for nine total
   (`llm/providercfg/load.go` `validThinkingFormats`;
   `llm/providers/openaicompat/request.go applyThinkingFormat`). Only Pi's
   `ant-ling` stayed out.
4. **$ENV in api_key**: comfortable adopting? (Marshal already never emits
   api_key, so no round-trip risk.)
   **Decision: yes, adopted** — `$VAR`, `${VAR}`, and `$$` (literal `$`),
   resolved at the point of use (adapter construction and live `/models`
   probes) rather than at `Load`, so one instance's missing variable errors
   only that instance (`llm/providercfg/apikey.go ResolveAPIKey`). The
   round-trip model changed during the refine loop: `Marshal` now emits
   `api_key` verbatim, and the on-disk guarantee lives in `WriteFile`, which
   scrubs struct-held keys and restores what the existing file already
   carried — hand-authored keys survive hub rewrites and injected
   credentials can never land on disk (`llm/providercfg/mutate.go`).
5. Does the `type="glm"` built-in provider also get thinking_format="zai"
   by default via its preset (proposed: yes — fixes GLM thinking today with
   zero user config)?
   **Decision: yes** — `QuirksPreset("glm-5")` now sets `ThinkingFormat:
   "zai"` (`llm/providers/openaicompat/quirks.go:88`). Note §4.3's layering
   paragraph also proposed `ToolStream: true` on the same preset and
   `ThinkingFormat: "openrouter"` on the `openrouter` preset — **neither
   shipped**; see §8.

## 7. Size estimate

~1,400–1,700 LoC including tests: providercfg schema+validation ~250,
catalog/profile merge ~150, quirks/compat + thinking formats ~250,
streaming fields + replay ~150, anthropic clamp unification ~50,
tests ~600–850. Plus a docs/llm-providers.md section.

## 8. Implementation notes (deviations from §4)

- **`ThinkingLevels` has no null values.** §4.2 proposed
  `map[string]*string` with an explicit `null` marking a level as
  unsupported. The shipped type is `map[string]string`
  (`providercfg.ModelConfig.ThinkingLevels`) — there is no null slot. A level
  is unsupported by being **absent from the map**, which is both simpler and
  the only form TOML expresses cleanly (TOML has no map-value null literal
  short of an awkward inline-table sentinel). The "map present = complete
  authority" semantics from open question 2 are unaffected.
- **The `off` key is rejected, not supported.** §4.2 proposed a three-way
  `off` slot (absent = disable-by-omission, string = explicit disable value,
  null = cannot-disable). None of that shipped: `thinking_levels` may only
  contain the five real effort levels (`minimal`/`low`/`medium`/`high`/
  `xhigh`, `max` folded into `xhigh`); a key literally named `off` is a load
  error (`llm/providercfg/load.go:149-152`). Serf's existing `none` effort
  already clears the setting to the provider default, so a per-model `off`
  wire value would have been a second, redundant way to say the same thing.
- **The `ProviderQuirks` field names are action-named, not
  capability-named.** The TOML/`CompatConfig` keys describe what the
  *provider supports* (`supports_store`, `supports_developer_role`,
  `supports_usage_in_streaming`); `ApplyCompatConfig`
  (`llm/providers/openaicompat/compat.go:60-136`) translates each into a
  `ProviderQuirks` field that describes what *serf does* about it:
  `SupportsStore` → `SendStoreFalse`, `SupportsDeveloperRole` →
  `UseDeveloperRole`, `SupportsUsageInStreaming` → `OmitStreamUsage` (and this
  last one **inverts**: `supports_usage_in_streaming = true` sets
  `OmitStreamUsage = false`). Anyone adding a new compat flag should keep this
  split — the config vocabulary is about the provider, the quirks vocabulary
  is about the request builder's behavior.
- **Preset layering gap vs §4.3/§6.5.** §4.3's layering paragraph proposed
  giving the `glm-5` preset both `ThinkingFormat:"zai"` *and*
  `ToolStream:true`, and giving the `openrouter` preset
  `ThinkingFormat:"openrouter"`. Only `glm-5`'s `ThinkingFormat:"zai"`
  shipped (`llm/providers/openaicompat/quirks.go:77-89`); `ToolStream` stays
  off by default even for `type = "glm"` instances (opt in via
  `compat.tool_stream`), and the `openrouter` preset is unchanged
  (`TranslateMaxToXHigh` only) — an `openrouter` instance that wants
  `reasoning: {"effort": ...}` must set `compat.thinking_format =
  "openrouter"` explicitly.
- **Pre-existing `WithModel` shallow-clone staleness bug, found and fixed.**
  While wiring instance model definitions through `WithModel` rebuilds
  (`agent/provider/profile.go`), this pass found that
  `rebuildOnSameProviderChange` was missing the `"openai-compatible"`
  behavior tag from its case list. A same-instance model switch (e.g. via
  `/model` or a fallback) on an `openai`-typed, `chat-completions`-style
  instance took the shallow-clone path instead of reconstructing the
  profile, so the **old** model's effort levels and context window survived
  onto the new model. Fixed by adding `"openai-compatible"` to
  `rebuildOnSameProviderChange`'s case list (commit `8d2762a7`). The bug
  predates this branch; it surfaced because §4.1's `instModels` plumbing
  needed `WithModel` to actually rebuild on every openai-compat behavior tag
  to re-resolve the new model against the instance's `models` table.
