# Reasoning effort & thinking config — move from provider-level to model-level

## Status

**Ready for implementation.** Revised 2026-04-10 after verification pass.

### Revision notes (2026-04-10)

Corrections from verification against live Anthropic docs and current codebase:

1. **Opus 4.5 supports `output_config.effort`** — Anthropic docs explicitly list Opus 4.5
   as supporting the effort parameter, even though it uses manual (not adaptive) thinking.
   This means we need TWO independent catalog fields, not one:
   - `SupportsAdaptiveThinking` — controls thinking shape (`adaptive` vs `enabled`)
   - `SupportsEffortParameter` — whether to emit `output_config.effort`
   
2. **OpenRouter Anthropic endpoint IS documented** — The references section claimed it was
   "undocumented but confirmed working." In fact, OpenRouter documents `thinking:{type:"adaptive"}`
   and `output_config.effort` with enum `{low, medium, high, max}` at their API reference.
   The probe steps (4 and 7) are unnecessary; fold verification into unit tests.

3. **Line numbers have drifted** — File references updated to match current main.

4. **Parser code style mismatch** — The proposed `case "reasoning_effort_levels":` switch
   doesn't match the existing parser which uses direct field access. Updated.

5. **Vocabulary mismatch only affects OpenAI-compat** — The "max → xhigh" translation is
   only needed for the OpenAI-compat path. The openrouter-anthropic path uses the same
   `{low, medium, high, max}` enum as our internal vocabulary — no translation needed.

## Summary

Our profile system hardcodes reasoning effort levels (`low/medium/high/max/xhigh`) per
**provider**, but reasoning support is a **model** property, not a provider property.
Different models from the same provider support different effort levels, and the same
model accessed via different paths (e.g. MiniMax M2.7 direct vs via OpenRouter) should
expose the same effort vocabulary. This is tech debt that will grow as more reasoning
models land. Additionally, Anthropic introduced an `effort` + `adaptive thinking` API
in Claude Opus/Sonnet 4.6 that supersedes the `budget_tokens` mechanism we currently
use — we're still on the deprecated shape.

The work is to: (1) move effort-level metadata from profile factories into the model
catalog, (2) upgrade our Anthropic adapter to support the new `output_config.effort`
+ `thinking: {type: "adaptive"}` shape alongside the legacy `budget_tokens` shape for
older models, and (3) propagate these changes through the openrouter-anthropic path
we just added for MiniMax.

## Why this matters now

We added a new `openrouter-anthropic` provider to work around MiniMax M2.7 emitting
Claude-XML syntax inside JSON tool arguments on the OpenAI-compat endpoint. That new
profile factory (`agent.NewOpenRouterAnthropicProfile`) hardcoded effort levels as
`{"low", "medium", "high", "max"}` to match `NewMiniMaxProfile`'s levels, because both
paths route to the same underlying model. This is the right tactical answer for today
but makes the underlying problem more obvious: we now have **two profiles** whose
effort levels must stay in sync because they reference the same model. A third path
(e.g. MiniMax via Fireworks if that ever lands) would make it three.

The other forcing function is Anthropic's new API shape. Claude Opus 4.6 and Sonnet
4.6 treat `thinking: {type: "enabled", budget_tokens: N}` as **deprecated** and the
path we use today. The replacement is `thinking: {type: "adaptive"}` with
`output_config: {effort: "low|medium|high|max"}` as a separate top-level parameter.
Migrating while the old path is still functional is cheaper than migrating under
pressure when it's removed.

## Current state

### Effort levels are hardcoded in profile factories

Each profile factory in `agent/profile.go` hardcodes its effort level vocabulary:

| Factory | File:Line | Effort levels | Notes |
|---|---|---|---|
| `NewOpenAIProfile` | `agent/profile.go:296` | `{low, medium, high, xhigh}` | `xhigh` is a user-facing alias we translate later |
| `NewAnthropicProfile` | `agent/profile.go:401` | `{low, medium, high, max}` | Used for real Claude |
| `NewGeminiProfile` | `agent/profile.go:443` | `{low, medium, high}` | |
| `NewMiniMaxProfile` | `agent/profile.go:494` | `{low, medium, high, max}` | Direct Anthropic-compat to `api.minimax.io/anthropic` |
| `NewOpenRouterAnthropicProfile` | `agent/profile.go:540` | `{low, medium, high, max}` | MiniMax via OpenRouter Anthropic endpoint |
| `NewOpenAICompatProfile` | `agent/profile.go:593` | `{low, medium, high}` | Kimi, GLM, OpenRouter OpenAI path |

The `effortLevels` field on `baseProfile` feeds two consumers:

1. **Tool schema enum**: `defTaskList(effortLevels []string)` in
   `agent/profile.go:1038` injects the list into the `reasoning_effort` field of
   the `task_list` tool schema so the model can only emit valid values.
2. **Budget mapping**: `llm.ReasoningBudget(effort)` in `llm/types.go:392` maps
   strings to integer token counts (`low=1024, medium=8192, high=32768,
   max=131072`). This is our convention, not an API convention.

### Anthropic adapter still on deprecated `budget_tokens` path

`llm/providers/anthropic/adapter.go:242` sets `body["thinking"] = map[string]any{...}`
using the old `{"type": "enabled", "budget_tokens": N}` shape. Our
`anthropicProviderOpts` helper (`agent/profile.go:339`) only carries `max_tokens` and
an optional 1M-context beta header. Nowhere do we emit
`output_config.effort` or `thinking: {type: "adaptive"}`.

Per [Anthropic's docs](https://platform.claude.com/docs/en/build-with-claude/adaptive-thinking),
as of Opus/Sonnet 4.6:

- `thinking.type: "enabled"` with `budget_tokens` is **deprecated** and will be
  removed in a future model release.
- The recommended shape is `thinking: {"type": "adaptive"}` combined with
  `output_config: {"effort": "low|medium|high|max"}`.
- `effort` is supported on Claude Mythos Preview, Opus 4.6, Sonnet 4.6, and
  Opus 4.5. Opus 4.5 uses manual thinking (not adaptive) but still accepts the
  effort parameter — meaning it needs `thinking: {type: "enabled", budget_tokens: N}`
  **combined with** `output_config: {effort: "..."}`.
- Older models (Sonnet 4.5, Sonnet 3.7, Haiku, etc.) only support `budget_tokens`
  and do not accept `effort`.
- `effort` works **without** thinking enabled — it also affects text responses
  and tool calls, not just thinking depth.
- `effort: high` is the default and equivalent to omitting the parameter.
- `effort: max` is only available on Mythos / Opus 4.6 / Sonnet 4.6.
- Adaptive thinking auto-enables interleaved thinking (required for tool-calling
  agentic workflows on Opus 4.6 — manual mode does NOT support interleaved
  thinking on Opus 4.6, which is a reason to migrate).

### What APIs actually expose

Researched across OpenAI, Anthropic, Gemini, MiniMax, OpenRouter. None of them
expose per-model effort levels in a machine-readable way. Some exposure exists:

- OpenRouter's `/api/v1/models/{id}/endpoints` returns `supported_parameters`,
  which includes the string `"reasoning"` if the model supports the unified
  reasoning parameter — but **not** which effort levels are supported.
- Anthropic's `/v1/models/{id}` returns basic metadata but nothing about effort
  or thinking support.
- OpenAI's `/v1/models` is similarly silent.

**Conclusion: per-model effort levels must be manually curated in our catalog.**
This is not going to change; the APIs give us no hook.

### The OpenRouter effort vocabulary mismatch

[OpenRouter's unified reasoning docs](https://openrouter.ai/docs/guides/best-practices/reasoning-tokens)
canonicalize effort to a 6-level vocabulary: `none`, `minimal`, `low`, `medium`,
`high`, `xhigh`. If a model doesn't support a level, OpenRouter silently maps to
the nearest supported level. Our internal vocabulary is `{low, medium, high, max,
xhigh}` depending on the profile. These don't line up:

- We use `max`, OpenRouter uses `xhigh` for the same meaning.
- We don't expose `minimal` or `none`.
- OpenRouter never exposes `max`.

Our code emits `reasoning_effort: "max"` to OpenRouter for the MiniMax path,
which OpenRouter then treats as an unknown string and silently maps (probably to
`xhigh`). This works but is undocumented behavior. The unified mapping belongs
in the adapter, not the profile.

## Research notes — provider-by-provider

### OpenAI

- `reasoning_effort: "low|medium|high"` on o-series and gpt-5 series.
- `"minimal"` and `"xhigh"` added on newer models (exact list not documented
  programmatically).
- Passed as a top-level field in Chat Completions requests.
- Older non-reasoning models reject the parameter entirely.

### Anthropic (direct)

**Newer models (Opus 4.6, Sonnet 4.6, Mythos Preview):**

```json
{
  "model": "claude-opus-4-6",
  "max_tokens": 16000,
  "thinking": {"type": "adaptive"},
  "output_config": {"effort": "medium"},
  "messages": [...]
}
```

Effort values: `low`, `medium`, `high` (default), `max`. `max` only on 4.6 / Mythos.
Effort works with `thinking: {type: "disabled"}` too — it affects all token spending,
not just thinking depth.

**Opus 4.5 (hybrid — manual thinking + effort):**

```json
{
  "thinking": {"type": "enabled", "budget_tokens": 8192},
  "output_config": {"effort": "medium"}
}
```

Manual thinking only (not adaptive), but **does** accept `output_config.effort`.
Effort levels: `low`, `medium`, `high` (no `max`). No interleaved thinking support
in manual mode.

**Older models (Sonnet 4.5, Sonnet 3.7, Haiku, etc.):**

```json
{
  "thinking": {"type": "enabled", "budget_tokens": 8192}
}
```

Only `budget_tokens` (integer). No `effort`. No adaptive mode. Sonnet 4.5 supports
interleaved thinking via `interleaved-thinking-2025-05-14` beta header.

### Gemini

`thinking_budget: N` (integer). No effort-level concept. `N = -1` means
dynamic/adaptive. `N = 0` disables thinking on models that support it.

### MiniMax (direct Anthropic-compat at `api.minimax.io/anthropic`)

Accepts Anthropic's format. Uses `thinking: {type: "enabled", budget_tokens: N}`
shape (not the adaptive shape). Effort-level concept is ours to define.

### MiniMax via OpenRouter Anthropic endpoint (`openrouter.ai/api/v1/messages`)

Accepts `thinking: {type: "enabled", budget_tokens: N}`. Returns standard
Anthropic format with `thinking` and `redacted_thinking` blocks. The underlying
model's native format is XML tool calls — OpenRouter translates internally.
I haven't tested whether it accepts the new `output_config.effort` shape — the
adapter work should probe and handle gracefully.

### OpenRouter unified (OpenAI-compat path)

Top-level `reasoning: {effort: "xhigh|high|medium|low|minimal|none"}` or
`reasoning: {max_tokens: N}`. Silent nearest-level mapping when the underlying
model doesn't support a requested level.

## Design

### Part 1 — catalog-driven effort levels and thinking modes

Add new fields to `llm.ModelInfo` in `llm/model_catalog.go`:

```go
type ModelInfo struct {
    // ... existing fields (including SupportsReasoning bool) ...
    ReasoningEffortLevels    []string `json:"reasoning_effort_levels,omitempty"`
    SupportsAdaptiveThinking bool     `json:"supports_adaptive_thinking,omitempty"`
    SupportsEffortParameter  bool     `json:"supports_effort_parameter,omitempty"`
}
```

Two independent flags because Opus 4.5 is the hybrid case: manual thinking + effort.

Extend the JSON parser in `parseLiteLLMCatalog` (around line 179, after the existing
field assignments) to match the existing style:

```go
// After the ModelInfo struct is populated from the standard fields...
if arr, ok := v["reasoning_effort_levels"].([]any); ok {
    for _, item := range arr {
        if s, ok := item.(string); ok {
            info.ReasoningEffortLevels = append(info.ReasoningEffortLevels, s)
        }
    }
}
info.SupportsAdaptiveThinking = parseBool(v["supports_adaptive_thinking"])
info.SupportsEffortParameter = parseBool(v["supports_effort_parameter"])
```

Populate in `llm/data/litellm_model_catalog.json` for the models we actually care
about. Leave others absent — the profile factory falls back to provider default
(see below). Initial curation:

```json
{
  "anthropic/claude-opus-4-6": {
    "reasoning_effort_levels": ["low", "medium", "high", "max"],
    "supports_adaptive_thinking": true,
    "supports_effort_parameter": true
  },
  "anthropic/claude-sonnet-4-6": {
    "reasoning_effort_levels": ["low", "medium", "high", "max"],
    "supports_adaptive_thinking": true,
    "supports_effort_parameter": true
  },
  "anthropic/claude-opus-4-5": {
    "reasoning_effort_levels": ["low", "medium", "high"],
    "supports_adaptive_thinking": false,
    "supports_effort_parameter": true
  },
  "anthropic/claude-sonnet-4-5": {
    "reasoning_effort_levels": ["low", "medium", "high"],
    "supports_adaptive_thinking": false,
    "supports_effort_parameter": false
  },
  "openai/gpt-5.4-mini": {
    "reasoning_effort_levels": ["low", "medium", "high"]
  },
  "openai/gpt-5.4": {
    "reasoning_effort_levels": ["low", "medium", "high", "xhigh"]
  },
  "minimax/minimax-m2.7": {
    "reasoning_effort_levels": ["low", "medium", "high", "max"],
    "supports_adaptive_thinking": false,
    "supports_effort_parameter": false
  },
  "google/gemini-3-flash-preview": {
    "reasoning_effort_levels": ["low", "medium", "high"]
  }
}
```

Key distinctions:
- **Opus 4.6 / Sonnet 4.6**: adaptive thinking + effort (full new path)
- **Opus 4.5**: manual thinking + effort (hybrid — needs both `budget_tokens` and `output_config`)
- **Sonnet 4.5 and older**: manual thinking only (legacy path)

### Part 2 — profile factories consult the catalog

Each profile factory gets a helper that reads from the catalog with a
provider-default fallback:

```go
func resolveEffortLevels(model string, providerDefault []string) []string {
    if cat := llm.EmbeddedModelCatalog(); cat != nil {
        if mi := cat.GetModelInfo(model); mi != nil && len(mi.ReasoningEffortLevels) > 0 {
            return append([]string(nil), mi.ReasoningEffortLevels...)
        }
    }
    return providerDefault
}
```

Every `NewXxxProfile` replaces its hardcoded `efforts` list with a call to
`resolveEffortLevels(model, providerDefault)`. The provider default is still
defined per factory as a safety net.

Specifically for `NewOpenRouterAnthropicProfile` and `NewMiniMaxProfile`:
both take a `model` argument and should look up by bare model name. For MiniMax,
the catalog key is `minimax/minimax-m2.7`. For the OpenRouter path, the model
string from the caller is already `minimax/minimax-m2.7` (openrouter-anthropic
prefix is stripped upstream). Both should find the same catalog entry.

### Part 3 — Anthropic adapter adopts `output_config.effort` + adaptive thinking

The adapter at `llm/providers/anthropic/adapter.go:242` should decide which
thinking shape to emit based on the model. Three cases:

- **Opus 4.6 / Sonnet 4.6 / Mythos Preview** → emit `thinking: {type: "adaptive"}`
  and `output_config: {effort: "<level>"}`. Do not emit `budget_tokens`.
- **Opus 4.5** → emit `thinking: {type: "enabled", budget_tokens: N}` **and**
  `output_config: {effort: "<level>"}`. Hybrid path.
- **Older models** (Sonnet 4.5, Sonnet 3.7, Haiku, etc.) → keep the current
  `thinking: {type: "enabled", budget_tokens: N}` shape only. No `output_config`.

The adapter uses two catalog fields: `SupportsAdaptiveThinking` and
`SupportsEffortParameter`.

Adapter logic:

```go
var adaptiveThinking, supportsEffort bool
if cat := llm.EmbeddedModelCatalog(); cat != nil {
    if mi := cat.GetModelInfo(req.Model); mi != nil {
        adaptiveThinking = mi.SupportsAdaptiveThinking
        supportsEffort = mi.SupportsEffortParameter
    }
}

if adaptiveThinking {
    // New path: Opus 4.6, Sonnet 4.6, Mythos
    body["thinking"] = map[string]any{"type": "adaptive"}
    if req.ReasoningEffort != nil {
        body["output_config"] = map[string]any{"effort": *req.ReasoningEffort}
    }
} else if req.ReasoningEffort != nil {
    // Legacy manual thinking path
    budget := llm.ReasoningBudget(*req.ReasoningEffort)
    if budget > 0 {
        body["thinking"] = map[string]any{
            "type":          "enabled",
            "budget_tokens": budget,
        }
    }
    // Hybrid: Opus 4.5 accepts effort even with manual thinking
    if supportsEffort {
        body["output_config"] = map[string]any{"effort": *req.ReasoningEffort}
    }
}
```

`llm.ReasoningBudget()` stays as the budget mapping for manual-thinking models.

### Part 4 — OpenRouter-Anthropic path

The openrouter-anthropic provider wraps the Anthropic adapter with a different
BaseURL/API key. It inherits the adapter's thinking logic automatically.

OpenRouter's Anthropic Messages endpoint documents `thinking:{type:"adaptive"}`
and `output_config.effort` with enum `{low, medium, high, max}`. Whether the
underlying MiniMax model honors adaptive thinking is a separate question — but
since MiniMax uses its own thinking implementation (not Anthropic's), the safe
default is `supports_adaptive_thinking: false` in the catalog.

The adapter will emit `thinking: {type: "enabled", budget_tokens: N}` for MiniMax
via this path, which is correct for now.

### Part 5 — OpenAI-compat translation

The OpenAI-compat adapter (`llm/providers/openaicompat/adapter.go`) already emits
`reasoning_effort` verbatim. Our vocabulary (`low|medium|high|max|xhigh`)
doesn't match OpenRouter's (`low|medium|high|xhigh|minimal|none`) — specifically
we emit `max` which OpenRouter doesn't know.

Add a translation table in the adapter for provider id == "openrouter" (or
whenever `BaseURL` contains `openrouter`):

| Our value | OpenRouter value |
|---|---|
| `minimal` | `minimal` (pass-through if we ever emit it) |
| `low` | `low` |
| `medium` | `medium` |
| `high` | `high` |
| `max` | `xhigh` |
| `xhigh` | `xhigh` |

This lives in `buildRequestBody` next to the existing `reasoning_effort` line
(`llm/providers/openaicompat/adapter.go:525`). Non-OpenRouter compat providers
(Kimi, GLM) can keep the raw value; if they reject unknown values, add entries.

Note: this is only for the OpenAI-compat **path**. The openrouter-anthropic path
goes through the Anthropic adapter which speaks its own effort vocabulary and
doesn't need this translation.

### Part 6 — `task_list` tool schema cleanup

`defTaskList(effortLevels)` at `agent/profile.go:1035` injects the effort levels
as an `enum` in the tool parameter schema. Today this is one list per provider.
After the refactor it will be one list per **model** (via `resolveEffortLevels`).
No direct code change needed — the function already takes the list as a
parameter — but the test cases should verify that catalog-driven values show up
in the schema for a representative model.

## Implementation tasks

Ordered so each step leaves the tree in a working state.

1. **Catalog schema**: Add `ReasoningEffortLevels []string`,
   `SupportsAdaptiveThinking bool`, and `SupportsEffortParameter bool` to
   `ModelInfo`. Extend parser in `parseLiteLLMCatalog`. Add a test that loads
   a sample JSON with these fields and verifies they round-trip.

2. **Catalog data**: Populate the fields in `llm/data/litellm_model_catalog.json`
   for the models listed in the Design section. Verify via `go test ./llm/...`
   that the new JSON still parses.

3. **Profile factory helper**: Add `resolveEffortLevels(model, providerDefault)`
   in `agent/profile.go`. Update all six factories
   (`NewOpenAIProfile`, `NewAnthropicProfile`, `NewGeminiProfile`,
   `NewMiniMaxProfile`, `NewOpenAICompatProfile`, `NewOpenRouterAnthropicProfile`)
   to use it. Keep the existing provider defaults inline as fallbacks. Add
   unit tests that cover catalog-hit and catalog-miss cases.

4. **Anthropic adapter — adaptive + effort paths**: Update
   `llm/providers/anthropic/adapter.go` near line 242 to branch on
   `mi.SupportsAdaptiveThinking` and `mi.SupportsEffortParameter`. Add unit
   tests using the HTTP mock pattern covering:
   - `claude-opus-4-6` + effort `medium` → emits adaptive + output_config
   - `claude-opus-4-5` + effort `medium` → emits enabled + budget_tokens + output_config (hybrid)
   - `claude-sonnet-4-5` + effort `medium` → emits enabled + budget_tokens only (no output_config)
   - `claude-opus-4-6` + no effort → emits adaptive alone (effort defaults to high server-side)
   - Effort clamping: if catalog says model only supports up-to-high but request
     asks for `max`, clamp to `high` rather than 400. Add a clamp helper.

5. **OpenAI-compat effort translation**: Add an OpenRouter-detection check and
   effort vocabulary translation in
   `llm/providers/openaicompat/adapter.go:buildRequestBody`. Unit-test with a
   fake request: `max` input with BaseURL containing `openrouter.ai` → body
   has `reasoning_effort: "xhigh"`.

6. **Integration test against `task_list` schema**: Write a test that builds
   a profile for `minimax/minimax-m2.7` (both via `openrouter-anthropic` and
   via direct `minimax`), extracts the `task_list` tool definition, and
   verifies the `reasoning_effort` enum matches the catalog entry. Both paths
   should produce the same enum.

7. **Ripple check**: Grep for any remaining hardcoded effort lists outside
   of provider-default fallbacks. Verify the test suite
   (`go test ./llm/... ./agent/... ./cmdutil/...`) is green.

8. **Document it**: Add a short note to `docs/experiments/prompt-lessons.md`
   or similar explaining that effort levels are catalog-driven, with a
   pointer to this spec as rationale.

## Test plan

Automated (must pass in CI):

- `go test ./llm/...` — catalog round-trip, adapter thinking shape selection.
- `go test ./agent/...` — profile factories use catalog; task_list schema
  matches; tests for clamp behavior.
- `go test ./llm/providers/openaicompat/` — OpenRouter effort translation.
- `go test ./llm/providers/anthropic/` — adaptive vs manual thinking emission.

Integration (manual, one-time):

- Live call to `claude-opus-4-6` with adaptive thinking: confirms the new shape
  is accepted.
- Live call to `claude-sonnet-4-5` with `budget_tokens`: confirms the old path
  still works for older models.
- Live call to `openrouter-anthropic/minimax/minimax-m2.7` with the shape
  appropriate to that entry's `supports_adaptive_thinking`.
- Eval wave: run 3 reps of a task set (say 8 discriminator tasks) against
  `anthropic/claude-opus-4-6` and compare to the previous behavior. We should
  see equivalent or better scores; the API migration should be neutral.

## Out of scope / future work

- **OpenAI `reasoning_effort`**: we don't need per-model effort lists for OpenAI
  models right now (gpt-5.4-mini is the only one we ship). When we add other
  OpenAI reasoning models (o3, o4), add their catalog entries then.
- **Gemini thinking budget**: Gemini uses an integer budget, not an effort
  level. Out of scope until we actually ship a Gemini reasoning model.
- **OpenRouter auto-discovery**: it would be nice to call
  `/api/v1/models/{id}/endpoints` and auto-populate effort levels from
  `supported_parameters`. OpenRouter's endpoint doesn't actually list effort
  levels — just whether `reasoning` is accepted at all — so this wouldn't give
  us per-level data. Skip until OpenRouter's API improves.
- **xhigh on OpenAI direct**: our current `NewOpenAIProfile` uses `xhigh`.
  Translate to OpenAI's actual values in the adapter if/when we add
  `reasoning_effort: xhigh` as a distinct value. Today it's just documented
  at the profile layer and not something we actually emit in practice.
- **MiniMax direct path**: already works with manual `budget_tokens` on the
  Anthropic adapter. If MiniMax ships a model that supports adaptive thinking
  at `api.minimax.io/anthropic`, add the catalog entry and the direct profile
  will pick it up via the same adapter.

## References

- Anthropic adaptive thinking: https://platform.claude.com/docs/en/build-with-claude/adaptive-thinking
- Anthropic effort parameter: https://platform.claude.com/docs/en/build-with-claude/effort
- OpenRouter unified reasoning (OpenAI-compat path): https://openrouter.ai/docs/guides/best-practices/reasoning-tokens
- OpenRouter Anthropic Messages endpoint: https://openrouter.ai/docs/api/api-reference/anthropic-messages/create-messages
  Documents `thinking:{type:"adaptive"}` and `output_config.effort` with enum `{low, medium, high, max}`.
- MiniMax M2.5 tool calling guide (explains the XML format that caused the original
  rabbit hole): https://github.com/MiniMax-AI/MiniMax-M2.5/blob/main/docs/tool_calling_guide.md
- zeroclaw-labs/zeroclaw PR #1189: content-level parser for the same MiniMax XML
  format, inspiration for our rescue regexes.
- Existing Claude-XML rescue in `llm/providers/openaicompat/adapter.go`:
  `rescueClaudeXMLArgs`. Stays in place regardless of this spec — it handles
  the OpenAI-compat path's corruption, which is orthogonal to effort levels.
