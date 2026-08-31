# Design: cross-provider fast/cheap model

Status: **implemented (2026-06-14)** after adversarial review. All eight review
findings addressed; see commit "feat(provider): allow the fast/cheap model to use
a different provider". Open question #4 (non-OpenAI structured-output degradation)
is accepted as graceful-skip, not enforced.

## Problem

At design time, the fast/cheap model evener used for auxiliary "side calls" was
locked to the **same provider** as the main model. Two enforcement points:

1. `applyFastCheapModel` (`cmd/evener/serve.go:492`) rejected a cheap model
   whose provider differed from the active provider (error at `:501`). It also
   required a fully-qualified `provider/model`: `cmdutil.ParseModelRef`
   (`cmdutil/cmdutil.go:113`) rejected a bare model name. The contract was
   "`provider/model`, and the provider must equal the active provider."
2. The cheap setting stored only a *model name* within the **same profile**.
   `WithCheapModel` cloned the main profile, and side-call sites paired that
   model with `Provider: <mainProfile>.ID()`.

## Motivation

Acute for the **Kimi coding plan**: it exposes one model (`kimi-for-coding`), so
an unset fast/cheap route uses the full coding model. A user on
main=`kimi-anthropic` may instead want auxiliary calls on an inexpensive
Anthropic/OpenAI/Google model.

## Non-goal / do not conflate

Not `model_fallbacks`. The cross-provider fallback guard
(`agent/session_init.go:583-613`) forbids a *fallback* from switching behavior
tags because the main turn's prompt/tool surface is tag-specific. Side calls are
self-contained (own short system prompt, no main tool surface — verified: none
set `Tools`/`ProviderOptions`/`ReasoningEffort`), so cross-provider is safe for
them in a way it is not for fallbacks. Keep the fallback guard as-is.

## Side-call inventory

Most auxiliary consumers—including web fetch, fork summaries, memory crystals,
recursive distillation, checkpoint prediction, and eval probes—route through the
session-scoped `cheapmodel.Caller`. `Caller.Complete` resolves
`Profile.CheapModelRef()`: an explicit fast/cheap route wins, while an unset
route uses the active provider and model. The caller also learns model refusals
and can retry an explicit cheap route on the active model.

Two consumers retain additional policy:

- **Session namer** (`agent/session_namer.go`) is the only `GenerateObject` side
  call. `sessionNamerEnabled` uses `ConfiguredCheapModel() != ""` as its enable
  gate, so naming remains disabled when no fast/cheap model was chosen.
- **Summarizer** (`agent/internal/contextmgr/context_manager.go`) uses
  `CompleteConfigured`, which reports whether the shared caller reached the
  active model. It may retry a broader class of eligible configured-route
  failures on that active model without repeating a route already attempted.

## What a side call actually needs — and the two real caveats

Routing only needs the right `Provider`+`Model` on the request (no quirks /
provider-options / tool surface / reasoning effort are used). **But** two
provider-specific behaviors do matter and the original spec missed both:

1. **Structured output is NOT uniform.** Only the namer uses `GenerateObject`
   with strict validation. Across adapters:
   - OpenAI: native `json_schema` enforcement.
   - Anthropic: **soft** — schema appended to the system prompt as text
     (`llm/providers/anthropic/request.go:173-182`), not enforced.
   - Kimi / GLM (openaicompat with `NoJSONSchema: true`,
     `openaicompat/quirks.go:33-52`): downgraded to `json_object`
     (`openaicompat/request.go:113-118`), no schema enforcement.
   So a cross-provider cheap model on Anthropic/Kimi/GLM makes the **namer** less
   reliable (it may fail validation and skip naming — advisory, non-fatal). The
   other 8 sites parse free-text/JSON loosely and are unaffected.
2. **Context window.** Side-call inputs are clamped to FIXED char budgets tuned
   for large windows (`maxHistoryChars = 80_000`, `webFetchMaxContent =
   100_000` ≈ 25-33K tokens; checkpoint pred 30_000), independent of the cheap
   model's window. Common configured cheap models have large windows, but a
   pathologically small cross-provider model could overflow. Document a
   minimum-window expectation; do not size dynamically (YAGNI).

## Design

Profile carries an optional cheap **provider** alongside the explicitly
configured cheap model. `ConfiguredCheapModel()` returns only that explicit
model; `CheapModelRef()` returns the configured route or the active
provider/model when no route was configured.

- New field `cheapProvider string` on `Profile` (empty ⇒ same as main).
- `WithCheapModel(p, ref)`:
  - `ref` = `provider/model` ⇒ set `cheapProvider` + `cheapModel`.
  - `ref` = bare `model` ⇒ set `cheapModel`, leave `cheapProvider` empty
    (same-provider; the flag layer accepts bare refs).
- `CheapProvider() string` ⇒ `cheapProvider` if set, else `p.ID()`.
- `CheapModelRef() (provider, model string)` returns
  `(CheapProvider(), ConfiguredCheapModel())` when configured and
  `(p.ID(), p.Model())` otherwise.
- **Namer**: keep the `ConfiguredCheapModel() != ""` enable gate and
  `sessionNamerModel` unchanged; only change the request's `Provider` to
  `CheapProvider()`. Behavior preserved when no cheap is configured (namer still
  disabled).
- **Summarizer**: call `cheapmodel.Caller.CompleteConfigured`, which resolves the
  configured route and reports whether it reached the active model. For a
  broader eligible configured-route failure, call the distinct active route
  once when the shared caller has not already done so.

Rejected: Option B (resolve a full second profile). Heavier; buys quirks/options
the side calls don't use. Only revisit if a side call later needs the cheap
provider's tool surface.

## Validation (credential-aware)

`ResolveProfileFromConfig` is **credential-blind** (`agent/provider/resolve.go:26`
checks only name+type; the launch-check path is explicitly network/credential
free, `launchcheck.go:123`). Routing actually requires the adapter to be
**registered** in the client (`llm/client.go:121` → "unknown provider"), which
happens per-instance only when the factory succeeds with credentials
(`llm/providers_config.go:77-103`).

Therefore `applyFastCheapModel` must validate against the **registered client
providers**, not `ResolveProfileFromConfig`:
- Change signature to `applyFastCheapModel(profile *provider.Profile, raw string,
  client *llm.Client) (*provider.Profile, error)`. The client is in scope at the
  call site (`serve.go:176` builds it before `:185`).
- If `ref` is cross-provider, require `client.ProviderNames()` to contain the
  cheap provider; else error early with a clear message naming it.
- Same-provider/bare refs need no provider check.
- Call-time defense: when an explicit cheap route is refused, the shared caller
  can retry on the active provider/model rather than failing an advisory side
  call immediately.

## Resume / persistence

`SessionMeta.CheapModel` is the authoritative persisted ref. Session snapshots
write `CheapModelRefString()`, and `RestoreSessionFromMetaWithConfig` reapplies a
non-empty value with `WithCheapModel`. Hub resume clears the launch-time
`FastCheapModel` override so restore does not apply the route twice.

## Touch points

- `agent/provider/profile.go`: `cheapProvider` field, `CheapProvider()`,
  `CheapModelRef()`.
- `agent/provider/profile_overrides.go`: `WithCheapModel` parses `provider/model`.
- Shared side-call consumers: route through `cheapmodel.Caller`, which resolves
  `CheapModelRef()`.
- `agent/session_namer.go`: request `Provider` ⇒ `CheapProvider()` (gate
  unchanged).
- `agent/internal/contextmgr/context_manager.go`: use `CompleteConfigured` for
  the first route and retain one active-model retry for the summarizer's broader
  eligible failure classes.
- `cmd/evener/serve.go:492` `applyFastCheapModel`: new signature + client-registered
  validation + relaxed provider-match; **parse** bare vs `provider/model`.
- Resume: `SessionMeta` field + restore re-apply + `spawn.go:256-258`.
- Tests: `cmd/evener/serve_fast_cheap_model_test.go` (the `RejectsCrossProvider`
  test becomes `AllowsCrossProviderWhenRegistered` and needs a real client with
  the cheap provider registered; add a `RejectsCrossProviderWhenNotRegistered`);
  routing tests for a family-A site, the namer, and the summarizer pairs; a
  resume round-trip test.

## Effort

Larger than first estimated. The 6 family-A sites + profile helpers are small;
the namer, the summarizer fallback-chain rework, the validation signature change
(+ its 3 existing tests), and resume persistence are the real work. Estimate
~350-500 LOC including tests. No new adapters / protocol work.

## Open questions for Jesse

1. Bare `--fast-cheap-model <model>` newly means "same provider, this model"?
   (Recommend yes — relaxes today's must-match while staying intuitive.)
2. Resume persistence via `SessionMeta` or launch-config replay? (Recommend
   whichever the hub already treats as authoritative for `Model`; mirror it.)
3. Per-instance default cheap model in `providers.toml`? (Recommend no — YAGNI;
   launch/flag only.)
4. Cross-provider namer on a non-OpenAI cheap provider degrades to best-effort
   JSON. Accept graceful skip, or restrict the namer's cheap model to
   schema-capable providers? (Recommend accept graceful skip — naming is
   advisory.)
