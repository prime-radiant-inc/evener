# Model-level reasoning support and default effort

Status: approved design, 2026-08-30. Supersedes the "profile factories
hardcode `reasoning: true`" state described in
`reasoning-effort-per-model.md` and updates its "What APIs actually expose"
section (several endpoints now do expose per-model reasoning metadata).

## Problem

A lunaroute/glm-5.3 session launched with no `--reasoning-effort` sent no
reasoning object at all. The gateway let the model think for 25k tokens
(seven minutes) on its first turn, and evener rendered nothing because the
Responses decoder only knew OpenAI's `reasoning_summary_*` stream events.

The decoder fix is independent (`llm/providers/openai/responses.go` now maps
`response.reasoning_text.delta` / `response.reasoning_part.added` to
reasoning deltas and keeps `reasoning_text` content as a thinking part).

The "send a bounded default" fix exposed a design flaw: `Profile.reasoning`
is a per-provider *permission* hardcoded `true` in six constructors, not
knowledge about the model. Any default keyed on it reaches models that reject
a reasoning control (gpt-4.1, gemini-2.0-flash, bare openai-compatible
servers) and overrides well-defined provider defaults (adaptive Claude,
Gemini's dynamic budget). Reasoning is a model property; defaults must be
model-level and data-driven, with no per-provider branches.

## What the data sources carry

| Source | Supports reasoning | Effort levels | Default when unset |
|---|---|---|---|
| litellm catalog | `supports_reasoning` (absent = no) | synthesized from `supports_<level>_reasoning_effort` flags | no |
| evener overrides | `supports_reasoning`, `thinking_always_on` | `reasoning_effort_levels` | **new: `default_reasoning_effort`** |
| codex `/models` | yes | `supported_reasoning_levels` | `default_reasoning_level` (value currently dropped) |
| OpenRouter `/models` | `supported_parameters`, `reasoning.mandatory` | `reasoning.supported_efforts` | `reasoning.default_enabled` (on/off only) |
| lunaroute `/v1/models` | `capabilities.reasoning` | `client_compat.pi.thinkingLevelMap` | no |
| stock OpenAI, Anthropic, Google `/models` | no | no | no |

Parsing lunaroute's `capabilities` / `client_compat.pi` block is a separate
change (tracked as an issue). Nothing except the codex backend states the
provider's behavior when the effort field is omitted, so "default when unset"
is an evener policy informed by per-model data, not a lookup.

## Design

### 1. Reasoning support and levels are resolved from data, once

`buildBaseProfile` receives the catalog entry for the model (`catalogModel`,
nil when unknown or when bare lookups are suppressed for the behavior tag,
e.g. ollama) and derives:

- `reasoning`: providers.toml `reasoning` (explicit, either direction) →
  catalog `SupportsReasoning` when the model is cataloged → the constructor's
  provider default (`true`: an uncataloged model is permitted, not assumed
  non-reasoning).
- `effortLevels`: providers.toml `thinking_levels` → catalog levels → the
  constructor's provider vocabulary. A non-reasoning model gets an empty,
  non-nil level list so the `task_list` enum and the effort chip agree.
- `defaultEffort`: catalog `DefaultReasoningEffort` (override data or, later,
  live `/models`).

`WithLiveModelInfo` may turn reasoning on (as today) and, when the live entry
has `CapabilitiesAdvertised`, off; providers.toml `reasoning` still wins.
Live `DefaultReasoningEffort` overrides the catalog value.

Constructors keep a provider default for `reasoning` and a provider
vocabulary for levels only as the fallback for models nobody has data on.

### 2. One rule for the effort sent on a request

A pure function in `agent`:

```
resolveRequestEffort(configured, supportsReasoning, levels, modelDefault) *string
  !supportsReasoning                → nil
  configured == "none"              → "none" if the model lists it, else nil
  configured != ""                  → clamp(configured, levels)
  modelDefault != ""                → clamp(modelDefault, levels)
  otherwise                         → clamp("medium", levels)
```

`buildModelRequest` and `callModelWithFallback` both call it; the fallback
path keeps its catalog re-resolution of the fallback model's levels and
passes them in. `ThinkingAlwaysOn` stops being a branch: a mandatory-thinking
model is a reasoning model and gets the default like any other.

### 3. Explicit off survives as `none`

`NormalizeReasoningEffort` maps the disable aliases (`none|null|off|false|0`)
to `"none"` instead of `""`, so "the user turned it off" and "nothing
configured" stay distinct through `SessionConfig`, the runtime setter, the
appwire `thread/reasoningEffort/set` handler, and delegates.
`ValidateReasoningEffort` accepts `none`. `ClampReasoningEffort` already
passes it through. Loop-detector escalation treats `none` like `""`.

The catalog ladder includes `none` for models whose litellm entry sets
`supports_none_reasoning_effort` (gpt-5.1 and later), so the explicit-off
path has a level to send on exactly the models that accept one. `none` has
no rank, so the clamp ignores it; the hub palette already filters it out of
tier pickers, and a `task_list` task may select it to run without thinking.

The hub's launch chip value `none` therefore means "off", and its label
changes from "none (clear)" to "none (off)".

### 4. Per-model default data

`ModelInfo.DefaultReasoningEffort` (`default_reasoning_effort` in the
overrides file; codex `/models` `default_reasoning_level`). Anthropic adaptive
models we curate get `high`, matching the server-side default, so their
behavior with no effort configured is unchanged.

## Consequences

- glm-5.3 via lunaroute (uncataloged): permitted, `medium` clamped to the
  openai vocabulary → `reasoning.effort: medium` on the wire.
- gpt-4.1 / gpt-4o / gemini-2.0-flash (cataloged, non-reasoning): no reasoning
  control ever, even with `--reasoning-effort high`. Operators can force it
  with providers.toml `reasoning = true`.
- Adaptive Claude: `output_config.effort: high` explicitly (was implicit).
- Cataloged reasoning models whose provider default was "off" or "dynamic"
  (legacy Claude budgets, Gemini 2.5, zai/qwen thinking toggles) now run at
  `medium` when nothing is configured. That is the policy: a reasoning model
  reasons at medium unless told otherwise.
- `--reasoning-effort none` turns thinking off where the model has an off
  level, and omits the field otherwise.

## Out of scope

- Parsing lunaroute's `capabilities` / `client_compat.pi` (issue).
- Counting non-replayable `Thinking.Text` toward the context estimate (issue).
- A providers.toml `default_reasoning_effort` key (overrides file suffices).
