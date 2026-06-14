# Design: cross-provider fast/cheap model

Status: **implemented (2026-06-14)** after adversarial review. All eight review
findings addressed; see commit "feat(provider): allow the fast/cheap model to use
a different provider". Open question #4 (non-OpenAI structured-output degradation)
is accepted as graceful-skip, not enforced.

## Problem

The fast/cheap model serf uses for auxiliary "side calls" is locked to the
**same provider** as the main model. Two enforcement points:

1. `applyFastCheapModel` (`cmd/serf/serve.go:492`) rejects a cheap model whose
   provider differs from the active provider (error at `:501`). Today it also
   requires a fully-qualified `provider/model` — `cmdutil.ParseModelRef`
   (`cmdutil/cmdutil.go:113`) rejects a bare model name. So the current contract
   is: "`provider/model`, and the provider must equal the active provider."
2. The cheap model is only a *model name* swapped within the **same profile**:
   `CheapModel()` returns a bare string (`agent/provider/profile.go:337`),
   `WithCheapModel` stores a name on a clone of the main profile
   (`agent/provider/profile_overrides.go`), and side-call sites issue requests
   with `Provider: <mainProfile>.ID()`.

## Motivation

Acute for the **Kimi coding plan**: it exposes one model (`kimi-for-coding`), so
`CheapModel()` falls through to `default: p.model` (`profile.go:351`) and every
side call runs on the full, expensive coding model. A user on
main=`kimi-anthropic` wants cheap calls on a cheap Anthropic/OpenAI/Google model.

## Non-goal / do not conflate

Not `model_fallbacks`. The cross-provider fallback guard
(`agent/session_init.go:583-613`) forbids a *fallback* from switching behavior
tags because the main turn's prompt/tool surface is tag-specific. Side calls are
self-contained (own short system prompt, no main tool surface — verified: none
set `Tools`/`ProviderOptions`/`ReasoningEffort`), so cross-provider is safe for
them in a way it is not for fallbacks. Keep the fallback guard as-is.

## Side-call inventory (verified — 9 request constructions)

Two **distinct families** with different semantics — this distinction drove the
review and the design:

**A. `CheapModel()` sites (6) — provider-default fallback, always non-empty.**
Each builds `llm.Request{Provider: p.ID(), Model: p.CheapModel()}` + `Complete`:

| Site | file:line |
|---|---|
| web_fetch Q&A | `agent/tool_web_fetch.go:124-125` |
| fork summarize | `agent/internal/contextmgr/fork_summarize.go:23-24` |
| memory crystals | `agent/internal/contextmgr/strategy_memory_crystals.go:148-149` |
| recursive distill (×2) | `agent/internal/contextmgr/strategy_recursive_distill.go:163-164, 193-194` |
| checkpoint prediction | `agent/internal/contextmgr/strategy_checkpoint_pred.go:220-221` |

In each, the profile (`cp`/`p`) is already in hand, so adding a routing helper is
a local change.

**B. `ConfiguredCheapModel()` sites (2) — empty when unset; gate behavior.** These
do **not** use `CheapModel()` and must not be folded into a blanket swap:
- **Session namer** (`agent/session_namer.go`): the ONLY `GenerateObject`
  side call (`:62`, strict JSON-schema validation via `generate_object.go:55`).
  `sessionNamerEnabled` gates the namer on `ConfiguredCheapModel() != ""`
  (`:87-92`); `sessionNamerModel` falls back to `profile.Model()` (the MAIN
  model). Provider is `profile.ID()` (`:65`).
- **Summarizer** (`agent/internal/contextmgr/context_manager.go:1125-1145`):
  does NOT call `CheapModel()`. It calls `summarizationModels(sumProfile)`
  (`:961-977`), which keys on `ConfiguredCheapModel()` and returns a **fallback
  chain** `[configuredCheap, activeMain]`, then loops issuing
  `llm.Request{Model: model, Provider: sumProfile.ID()}` (`:1135`) — both models
  to the **same (main) provider**.

**Not a side call** (correctly excluded): eval probes
(`agent/eval_probes.go:81-92`) use a plain `Complete` + `parseBinaryJudge` text
parse — **no `GenerateObject`, no schema**. They are still a `CheapModel()` site
(`:82-83`) and route like family A, but carry no structured-output requirement.
`tool_web_search.go` and `session_tools.go` use `p.Model()` (main), excluded.

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
   model's window. With the default cheap models (≥128K) this is fine; a
   pathologically small cross-provider cheap model could overflow. Document a
   minimum-window expectation; do not size dynamically (YAGNI).

## Design

Profile carries an optional cheap **provider** alongside the cheap model. Keep
`CheapModel()` / `ConfiguredCheapModel()` semantics **unchanged**; add provider
routing on top so default behavior is preserved exactly.

- New field `cheapProvider string` on `Profile` (empty ⇒ same as main).
- `WithCheapModel(p, ref)`:
  - `ref` = `provider/model` ⇒ set `cheapProvider` + `cheapModel`.
  - `ref` = bare `model` ⇒ set `cheapModel`, leave `cheapProvider` empty
    (same-provider — newly accepted; today bare is rejected at the flag layer).
- `CheapProvider() string` ⇒ `cheapProvider` if set, else `p.ID()`.
- `CheapModelRef() (provider, model string)` ⇒ `(CheapProvider(), CheapModel())`
  — used by the **6 family-A sites** (one-line swap each).
- **Namer**: keep the `ConfiguredCheapModel() != ""` enable gate and
  `sessionNamerModel` unchanged; only change the request's `Provider` to
  `CheapProvider()`. Behavior preserved when no cheap is configured (namer still
  disabled).
- **Summarizer**: change `summarizationModels` to return `(provider, model)`
  pairs — `[(CheapProvider(), configuredCheap), (p.ID(), activeMain)]` — and have
  the loop route each pair to its own provider. The cheap entry goes to the cheap
  provider; the main-model fallback stays on the main provider.

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
- Call-time defense: if a side call's cheap provider is somehow unroutable, fall
  back to the main provider's `CheapModel()` rather than failing a summarization
  mid-session (cheap calls are advisory).

## Resume / persistence (must-have — feature evaporates without it)

Today the cheap setting is lost on hub resume: `cmd/serf-hub/spawn.go:256-258`
sets `resumeResolved.Effective.FastCheapModel = ""`, and `SessionMeta`
(`agent/schema/snapshot.go`) persists only `ProfileID`/`Model`. A resumed Kimi
session silently reverts side calls to the expensive main model.

Fix: persist the cheap ref and rebuild it on restore.
- Add a `CheapModel` (and provider) field to `SessionMeta`; write it at snapshot
  time; on `RestoreSessionFromMetaWithConfig` (`serve.go:219`) re-apply via
  `WithCheapModel`.
- Stop clearing `FastCheapModel` in `spawn.go` resume args (or re-pass it from
  the persisted launch config) so the relaunched `serf serve` re-applies it.
- Verify which mechanism is authoritative (launch-config replay vs. SessionMeta)
  and use one; do not double-apply.

## Touch points

- `agent/provider/profile.go`: `cheapProvider` field, `CheapProvider()`,
  `CheapModelRef()`.
- `agent/provider/profile_overrides.go`: `WithCheapModel` parses `provider/model`.
- 6 family-A sites: route via `CheapModelRef()`.
- `agent/session_namer.go`: request `Provider` ⇒ `CheapProvider()` (gate
  unchanged).
- `agent/internal/contextmgr/context_manager.go`: `summarizationModels` ⇒
  `(provider, model)` pairs + loop routing.
- `cmd/serf/serve.go:492` `applyFastCheapModel`: new signature + client-registered
  validation + relaxed provider-match; **parse** bare vs `provider/model`.
- Resume: `SessionMeta` field + restore re-apply + `spawn.go:256-258`.
- Tests: `cmd/serf/serve_fast_cheap_model_test.go` (the `RejectsCrossProvider`
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
