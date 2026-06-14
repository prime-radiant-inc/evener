# Design scope: cross-provider fast/cheap model

Status: **scoping — not implemented.** Decision pending (Jesse).

## Problem

The fast/cheap model serf uses for side calls (session naming, web_fetch Q&A,
summarization, memory crystals, checkpoint prediction, eval probes) is locked to
the **same provider** as the main model. Two enforcement points:

1. `applyFastCheapModel` (`cmd/serf/serve.go:491`) hard-rejects a cheap model
   whose provider differs from the active provider:
   ```
   --fast-cheap-model provider %q must match active provider %q
   ```
2. Even without that gate, the cheap model is only a *model name* swapped within
   the **same profile**: `CheapModel()` returns a bare string
   (`agent/provider/profile.go:337`), `WithCheapModel` stores a name on a clone
   of the main profile (`agent/provider/profile_overrides.go`), and every
   side-call site issues its request with `Provider: <mainProfile>.ID()`:

   | Site | file:line |
   |---|---|
   | session namer | `agent/session_namer.go:65` |
   | web_fetch Q&A | `agent/tool_web_fetch.go:124-125` |
   | fork summarize | `agent/internal/contextmgr/fork_summarize.go:23-24` |
   | memory crystals | `agent/internal/contextmgr/strategy_memory_crystals.go:148-149` |
   | recursive distill | `agent/internal/contextmgr/strategy_recursive_distill.go:163-164, 193-194` |
   | checkpoint prediction | `agent/internal/contextmgr/strategy_checkpoint_pred.go:220-221` |
   | summarize (context mgr) | `agent/internal/contextmgr/context_manager.go:1135` |
   | eval probes | `agent/eval_probes.go:82-83` |

So the cheap model always routes through the main model's adapter.

## Motivation

This is acutely wrong for the **Kimi coding plan**: it exposes exactly one model
(`kimi-for-coding`). There is no cheap Kimi model, so `CheapModel()` falls
through to `default: p.model` (`profile.go:351`) — every side call runs on the
full, expensive coding model. A user running main=`kimi-anthropic` wants the
cheap calls on, say, a cheap Anthropic/OpenAI/Google model. More generally,
"different models should be able to have their own providers" (Jesse).

## Non-goal / do not conflate

This is **not** `model_fallbacks`. The cross-provider fallback guard
(`agent/session_init.go:599-609`) deliberately forbids a *fallback* model from
switching behavior tags because the main turn's prompt/tool surface is
tag-specific. The cheap model is different: side calls are **self-contained**
(their own short system prompt, no main tool surface), so cross-provider is safe
for them in a way it is not for fallbacks. Keep the fallback guard as-is.

## What a side call actually needs

Every site above builds a plain `llm.Request{Provider, Model, Messages}` (the
namer and eval probes additionally pass a JSON schema via
`llm.GenerateObject`, `llm/generate_object.go:26`). They do **not** use the main
tool surface, quirks, or `ProviderOptions`. To run a side call on another
provider you need only: (a) that provider's adapter registered in the client
(true when it is configured), and (b) the right `Provider`+`Model` on the
request. No full profile is required for correctness.

## Options

**Option A (recommended): cheap model carries an optional provider+model ref.**
- Replace the bare `cheapModel string` on the profile with an optional
  `cheapProvider` + `cheapModel`. Add `CheapModelRef() (provider, model string)`
  returning `(cheapProvider, cheapModel)` when a cross-provider cheap is set,
  else `(p.ID(), p.CheapModel())` — preserving today's behavior by default.
- Each side-call site switches from `Provider: p.ID(), Model: p.CheapModel()` to
  `prov, model := p.CheapModelRef(); Provider: prov, Model: model`.
- `applyFastCheapModel` stops rejecting cross-provider; it parses the ref and
  validates the cheap **provider is resolvable** via the same config path the
  main model uses (`provider.ResolveProfileFromConfig` / the registered client
  providers) instead of string-matching the active provider.
- `WithCheapModel(p, "provider/model")` stores both halves.

**Option B: cheap model is a full resolved profile.** Resolve a second profile
via `SessionConfig.ResolveProfile` and route side calls through it. Heavier
(threads a profile through the context-manager strategies) and only buys
provider-specific quirks/options the side calls don't use. Reach for B only if a
future side call needs the cheap provider's tool surface or provider options.

Recommendation: **A.** Smallest change that delivers the requirement; B is
over-engineering for self-contained side calls.

## Touch points (Option A)

- `agent/provider/profile.go`: add `cheapProvider` field + `CheapModelRef()`;
  keep `CheapModel()`/`ConfiguredCheapModel()` for the model-name half.
- `agent/provider/profile_overrides.go`: `WithCheapModel` accepts `provider/model`.
- The 8 side-call sites above: route via `CheapModelRef()`.
- `cmd/serf/serve.go:491` `applyFastCheapModel`: replace the equality rejection
  with provider-resolvability validation; store the ref.
- Tests: `cmd/serf/serve_fast_cheap_model_test.go` (the
  `RejectsCrossProvider` test inverts to `AllowsCrossProviderWhenConfigured`),
  plus a routing test per representative side call.
- Hub/TUI: the `fast_cheap_model` launch control already exists
  (`cmd/serf-hub/internal/launchconfig/schema.go:84`,
  `cmd/serf-tui/internal/launchconfig/launch_settings_panel.go:324`). It feeds
  `--fast-cheap-model`; once cross-provider is accepted, no schema change is
  needed, though the picker could offer cross-provider models (follow-up).

## Validation & edge cases

- **Cheap provider not configured / no credential.** Validate at launch
  (`applyFastCheapModel` resolves the cheap provider like the main one and errors
  early with a clear message). At call time, defensively fall back to the main
  provider's `CheapModel()` rather than hard-failing a summarization mid-session.
- **Structured output.** The namer/eval probes use `GenerateObject`; confirm the
  chosen cheap provider supports JSON-schema structured output (all current
  adapters do; document the requirement).
- **Identity/labels.** `resp.Provider`/error labels are stamped centrally to the
  request's provider, so a cheap call already reports the cheap provider once
  routed correctly — no extra work.

## Effort

Small: one profile field + accessor, a one-line change at 8 call sites, the
`applyFastCheapModel` validation swap, and tests. No new adapters, no protocol
work. Estimate ~150–250 LOC including tests.

## Open questions for Jesse

1. Launch-time hard error vs. call-time graceful fallback when the cheap
   provider is misconfigured? (Recommend: both — error at launch, fall back at
   call time.)
2. Should `--fast-cheap-model` accept a bare model (current behavior, same
   provider) **and** `provider/model`? (Recommend: yes — bare stays
   same-provider; `provider/model` selects a cross-provider cheap.)
3. Per-instance default cheap model in `providers.toml` (so e.g. a `kimi-anthropic`
   instance defaults its cheap calls to a configured Anthropic instance), or
   launch-only? (Recommend: launch-only for now — YAGNI.)
