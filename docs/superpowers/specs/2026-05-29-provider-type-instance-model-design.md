# Provider Type/Instance Model

Date: 2026-05-29
Status: design rewritten after adversarial review; pending re-review
Ticket: PRI-1880

> This is a v2 rewrite. The first draft assumed routing keyed on the registered
> adapter `Name()` prefix; two independent reviews showed routing actually keys
> on `req.Provider = profile.ID()` through a hardcoded profile whitelist, with
> hardcoded adapter names, a single OAuth file, no `providers.toml` consumer, and
> several behaviors hardwired to the provider string. §3 documents the real
> architecture; §8 maps each review finding to its resolution.

## 1. Goal

Turn serf's hardcoded provider registry into a **type/instance** model: provider
*types* (code-backed adapter + profile recipes) that users *instantiate* into
named, multi-instance, configured providers — including arbitrary
OpenAI-compatible and Anthropic-compatible endpoints and multiple instances of
one vendor. Replace the two duplicative web settings screens (Providers +
Credentials) with one CRUD screen.

## 2. Non-Goals

- **New wire protocols.** We keep existing adapters; we don't support endpoints
  whose protocol no adapter speaks.
- **Per-instance protocol/quirk overrides** (auth scheme, version header,
  disabling `cache_control`, custom quirks presets). Quirks remain a *type*
  property; deferred as a later enhancement.
- **Encryption at rest** (keys stay verbatim, `chmod 600`).
- **Breaking existing setups** (migration, §4.8).
- **Removing env-var configuration** (it remains the migration source and the
  no-config fallback).

## 3. Background — the real architecture

Verified against the code; this corrects the first draft.

**Routing.** `agent` builds a request with `req.Provider = s.profile.ID()`
(`agent/session.go:1460`). `llm.Client.Complete/Stream` looks up
`c.providers[normalizeProviderName(req.Provider)]` (`llm/client.go:90-96`);
adapters are registered under their `Name()` (`Register`, client.go:39), each a
**hardcoded constant** (`openai/adapter.go:143`, `anthropic:56`, etc.). The
default provider is the first non-`NonDefaultEligible` adapter registered
(client.go:41), and registration order is package-`init()` (import) order
(`llm/env_registry.go`).

**Profiles.** `cmdutil.SelectProfile(provider, model, …)` is a hardcoded
`switch provider` that **rejects unknown names** (`cmdutil/cmdutil.go:57-73`); it
builds a profile whose `id` is a hardcoded type name — except
`NewOpenAICompatProfile(id, model, ctxWindow)`, which already **takes `id` as a
parameter** (used for kimi/glm/openrouter/ollama). The profile carries
capabilities, tool surface, context window, quirks, and the `ProviderOptions`
key. The hub's launch gate also calls `SelectProfile` (`cmd/serf/launch_check.go`,
via `cmd/serf-hub/spawn.go`), so an unknown provider fails *before the daemon
starts*.

**Behaviors keyed on the provider string** (= profile id = type name today):
24h prompt-cache (`session.go:1382` `req.Provider == "openai"`), gemini handling
(`session.go:4785` `id == "gemini"`), finish-reason normalization
(`types.go:260` `switch provider`), `normalizeProviderName` gemini→google
(`client.go:236`), and the cross-provider-fallback guard (`session.go:4140`
`fbProfile.ID() != s.profile.ID()`). `resp.Provider` is set to `req.Provider`
(`session.go:3512`).

**OAuth.** `internal/auth/openai/storage.go` hardcodes `authFileName =
"openai.json"`; `AuthFilePath(stateDir)` is fixed; the hub auth controller
branches on `provider == "openai"`. There is exactly one OAuth slot.

**Client construction & spawn.** The daemon builds its client via
`llm.NewFromEnv` (`cmd/serf/serve.go`); the hub's model-listing/credential paths
also use `NewFromEnv`. `launchconfig.ToEnv` injects **one** provider's key
(`internal/launchconfig/env.go:66`). There is no `providers.toml`,
`SERF_PROVIDERS_CONFIG`, or config loader anywhere.

**Credentials.** `internal/credentials/store.go` hardcodes `providerEnvVars` /
`providerAuthModes`; keys live in `credentials.toml`. The store is read by hub
auth (`app_auth.go`), spawn injection, and launch gating.

## 4. Design

### 4.1 Types and instances

A **type** is a code descriptor:
`{ key, profileRecipe(instanceName, model, cfg) ProviderProfile,
   adapterRecipe(cfg) ProviderAdapter, defaultBaseURL, authModes, behaviorTag }`.
Types: `openai`, `anthropic`, `google` (alias `gemini`), `openrouter`,
`openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`. The
`openai-compatible` type folds into the `openai` type via `apiStyle` (§4.5).

An **instance** is persisted config:
`{ name (unique; routing key), type, baseURL?, apiStyle? (openai only),
   credential: apiKey | oauth | none }`. Any number of instances per type.

### 4.2 The two cross-cutting changes (the heart of this work)

**(a) Separate instance *name* (routing) from provider *type* (behavior).**
- The routing key stays the instance name: `profile.ID()` = instance name, the
  adapter's `Name()` = instance name, `req.Provider` = instance name.
- A new explicit **provider type** travels with the profile and request (e.g. a
  `Type` field on the profile, surfaced as `req.ProviderType`). Every behavior
  currently switched on the provider string (§3) is re-keyed on **type**:
  prompt-cache, finish normalization, gemini handling, `normalizeProviderName`,
  and the cross-provider-fallback guard (compare **type**, not id — two
  instances of the same type are *not* a cross-provider switch).
- **Back-compat:** migration names default instances after their type, so for
  existing users instance name == type and no behavior changes observably.

**(b) Make profile id and adapter name instance-settable.**
- Vendor profile constructors gain an explicit id/instance-name parameter (or a
  `WithProviderID(profile, name)` wrapper, matching the existing
  `WithCommunicateOutputSchema` wrapper pattern). `NewOpenAICompatProfile`
  already takes `id`.
- Adapters gain a constructor-set name (small field on each, or a thin naming
  wrapper) and are built per instance with the instance's base URL/key/style,
  then registered under the instance name. `resp.Provider` already mirrors
  `req.Provider` (the instance name); behavior keys on type, so this is safe.

### 4.3 Instance-aware profile selection

Replace `SelectProfile`'s hardcoded switch with an instance-aware selector:
given an instance name, look it up in the loaded provider config → its type +
config → build the type's profile via its recipe with `id = instanceName` and
the instance's base URL / apiStyle / context. Unknown names produce a clear
error listing configured instances. The hub launch gate uses the same selector,
so it passes for any configured instance. Default instances (named after their
type) keep resolving `openai/…`, `anthropic/…` unchanged.

### 4.4 Model addressing

Models are addressed `instanceName/model` (the model may itself contain slashes;
`ParseModelRef` already cuts on the first slash). The first segment is **always
the instance name**; it is resolved first, and the instance's type-profile's
`WithModel`/prefix logic interprets the remainder — so an `openrouter`-type
instance still parses `anthropic/claude`. A launch may also carry an explicit
instance + bare model. Bare/unqualified models resolve to the configured
**default instance**. (This generalizes today's prefix handling; the TUI/web
model pickers' hardcoded prefix allowlist is updated in Phase 2, §4.11.)

### 4.5 `openai` `apiStyle` fold-in

`apiStyle ∈ { responses (default), chat-completions }` selects the **full
triple**, not just a base URL:
- `responses` → `NewOpenAIProfile` + the `openai` adapter (Responses API /
  Codex backend / OAuth), `ProviderOptions["openai"]`.
- `chat-completions` → `NewOpenAICompatProfile` + the `openaicompat` adapter,
  `ProviderOptions["openai-compatible"]`.

So "openai-compatible" = an `openai` instance with `apiStyle=chat-completions` +
base URL. `chat-completions` is **not feature-equivalent** to `responses` (no
Codex tool surface, no reasoning `encrypted_content`, no responses→chat
fallback) — expected for generic compatible servers and documented in the UI.

### 4.6 `anthropic` instances and their limits

`anthropic` instances set a base URL (`NewAnthropicProfile` + the `anthropic`
adapter, which already takes a base URL). This targets **Anthropic-API-compatible**
servers: same `/v1/messages`, `anthropic-version` header, `x-api-key` auth, and
`cache_control`/`thinking` blocks. Endpoints needing bearer auth, a different
version header, or that reject `cache_control` are **out of scope for Phase 1**;
per-instance protocol toggles are a deferred enhancement (§2).

### 4.7 OAuth per instance

Re-plumb the OpenAI auth storage to key on an instance: `AuthFilePath(stateDir,
instanceName)` → `auth/<instanceName>.json`; thread the instance name through
`LoadAuth`/`SaveAuth`/`DeleteAuth`, the `Service`/`ResolveRuntimeCredentials`,
and the hub auth controller (replacing its `provider == "openai"` branch with an
instance lookup whose type advertises `oauth`). The default `openai` instance
(name `openai`) keeps `auth/openai.json` — **zero migration for the common
case**. OAuth is offered only for instances whose type's `authModes` include
`oauth` (today: `openai`).

### 4.8 Storage, single source of truth, migration

- **`~/.serf/providers.toml`** (hub-owned, `chmod 600`): `schema`, `default`
  (instance name), and one `[instances.<name>]` per instance with `type`,
  `base_url`, `api_style`, and an inline `api_key` (omitted for OAuth/none).
- **Single source of truth:** when `providers.toml` exists it is authoritative
  for the instance set and credentials; `credentials.toml` and provider env
  vars are no longer consulted for the provider set. When it is **absent**,
  one-time **migration** builds default instances from `credentials.toml`,
  recognized env vars, the single `OPENAI_COMPATIBLE_*` slot, and any existing
  `openai.json` (an OAuth-backed `openai` instance), then writes
  `providers.toml`. Old files are left in place but no longer consulted.
- **Default rule (explicit, deterministic):** migration sets `default` to the
  `openai` instance if present, else the first instance by sorted name, and
  records it explicitly (user-editable). We deliberately do **not** replicate
  today's import-order default (which, e.g., resolves to `anthropic` when both
  keys are set); the one-time change is documented in the migration log.

### 4.9 Client / daemon / hub construction

- New `llm.NewFromProviders(config)`: for each instance, construct the type's
  adapter with the instance's base URL/key/apiStyle, named by the instance,
  register it, and set the configured default. `NewFromEnv` stays as the
  no-config fallback and migration source.
- Daemon (`serve.go`): if `SERF_PROVIDERS_CONFIG` is set, build via
  `NewFromProviders(load(path))`; else `NewFromEnv`. The hub's own
  model-listing/credential clients load the same config.
- Launch gating / credential validation (`spawn.go`) read the **instance set**,
  not the fixed provider maps; an instance with no usable credential reports a
  clear missing-credential error (as today).

### 4.10 Spawn plumbing

The hub owns `providers.toml` and passes `SERF_PROVIDERS_CONFIG=<path>` to
spawned `serf serve`; the daemon loads it (§4.9). This replaces the
single-provider env injection (kept only for the no-config fallback). API keys
travel in the file (`chmod 600`); OAuth tokens stay in `auth/<name>.json`, which
the session reads via its state dir.

### 4.11 Unified UI (Phase 2)

One screen replacing Providers + Credentials: instances grouped by type, each
showing its credential source/state and a default marker; **create** (pick type
→ name → base URL / apiStyle / credential), **edit**, **remove**, **set-default**;
per-instance credential management (key set/clear; OAuth sign-in via the
PRI-1878 device-code flow for OAuth-capable types). RPCs operate on the instance
config. The TUI/web model pickers are updated to display and route by instance
name (generalizing the hardcoded prefix allowlist in
`cmd/serf-tui/model_display.go` and the web equivalent).

## 5. Phasing

- **Phase 1 — instance core (config-driven, no UI).** The full profile / routing
  / auth / config rework: type registry; the name-vs-type separation (§4.2a) and
  instance-settable profile id + adapter name (§4.2b); instance-aware
  `SelectProfile` + launch gate (§4.3); `NewFromProviders` + `providers.toml`
  loader + migration + explicit default (§4.8-4.9); per-instance OAuth storage
  (§4.7); daemon/hub/spawn/credential consumers (§4.9-4.10); the `openai`
  apiStyle triple (§4.5) and `anthropic` base-URL instances (§4.6). Acceptance:
  custom endpoints and multiple instances of a type work end-to-end through the
  real launch path via a hand-edited `providers.toml`, and existing setups
  migrate untouched. This is large and **sub-phased**:
  - **1a:** name/type separation, instance-settable profile id + adapter name,
    instance-aware `SelectProfile`/launch gate — proven with the *existing*
    env-driven providers (no new config yet). De-risks the routing/behavior
    rework in isolation.
  - **1b:** `providers.toml` + `NewFromProviders` + migration + per-instance
    OAuth + spawn/hub consumers + apiStyle/anthropic base-URL — enabling actual
    custom/multi instances.
- **Phase 2 — unified CRUD UI + model-picker updates** (§4.11) on Phase 1's
  config + RPC surface.

Each phase/sub-phase is its own implementation plan.

## 6. Error handling

- **Duplicate / invalid instance names:** names must be unique and contain no
  `/`; the loader rejects violations.
- **Unknown type:** the loader errors clearly, naming the instance and type.
- **Missing credential:** instance registers but reports "not configured";
  launch gating surfaces the error.
- **Default points at a removed instance:** fall back to the first instance by
  sorted name and log.
- **Corrupt `providers.toml`:** **fail loudly** — the hub logs a clear error and
  refuses to silently revert to env-only behavior (which would drop file/OAuth
  instances and could change which model answers). (This reverses the first
  draft's fail-open.) A fresh install with no `providers.toml` still uses the
  env fallback + migration as normal.

## 7. Testing strategy

- **Name/type separation:** unit tests that an instance named `work` of type
  `openai` still gets prompt-cache / finish-normalization / gemini-handling per
  *type*; that two same-type instances are not treated as a cross-provider
  fallback. (`agent`, `llm`)
- **Instance-aware selection:** `SelectProfile`/launch gate resolve a configured
  custom instance and reject unknown names. (`cmdutil`, `cmd/serf-hub`)
- **Loader / migration:** parse + round-trip `providers.toml`; migrate a fixture
  `credentials.toml` (+ env, + `openai.json`) into the expected instances and
  explicit default. (`internal/...`)
- **Multi-instance routing:** N instances of a type → N adapters routed by
  instance name; `apiStyle` selects the responses vs chat-completions triple;
  default-instance resolution. (`llm`)
- **OAuth per instance:** `auth/<name>.json` round-trips; the default `openai`
  instance still uses `openai.json`. (`internal/auth/openai`, `cmd/serf-hub`)
- **Spawn:** `ToEnv` sets `SERF_PROVIDERS_CONFIG`; a spawned session builds its
  client from it. (`internal/launchconfig`, `cmd/serf-hub`)
- **Adapters:** existing tests stay green; add `anthropic` base-URL override
  coverage.
- **Phase 2:** RPC round-trip + JSDOM tests for the management screen and pickers.

## 8. Review findings → resolutions

1. *Routing keys on `profile.ID()`, not adapter name* → §4.2: instance name *is*
   `profile.ID()`/`req.Provider`/adapter `Name()`; behavior re-keyed on type.
2. *`SelectProfile` rejects unknown providers* → §4.3 instance-aware selector.
3. *Launch-check gate also calls `SelectProfile`* → §4.3 (same selector used by
   the gate).
4. *Adapter `Name()` hardcoded; can't multi-instance* → §4.2b instance-settable
   adapter name.
5. *`SERF_PROVIDERS_CONFIG`/loader is net-new* → §4.9-4.10 spell out the loader
   and every consumer (daemon, hub, gate, spawn).
6. *OAuth hardwired to one `openai.json`* → §4.7 per-instance auth path.
7. *apiStyle drops provider options / ships wrong profile* → §4.5 apiStyle
   selects the full profile+adapter+options-key triple.
8. *Migration "first-registered" default is import-order/anthropic* → §4.8
   explicit deterministic default rule, documented one-time change.
9. *Split-brain credentials* → §4.8 single source of truth (providers.toml wins;
   others are migration source/fallback only).
10. *Single-provider spawn injection / unknown-provider don't-block* → §4.9-4.10
    instance-set-driven injection + gating.
11. *OAuth-vs-key hijack on shared state dir* → §4.7 per-instance auth state +
    explicit per-instance credential.
12. *Model pickers strip a hardcoded prefix allowlist* → §4.11 picker updates
    (Phase 2); §4.4 grammar.
13. *Phase 1 not shippable as scoped* → §5 re-scoped Phase 1 (includes the
    routing/profile/auth rework) and sub-phased 1a/1b.
14. *Multi-instancing meta-providers breaks slash model IDs* → §4.4 instance
    resolved first, type drives remainder.
15. *Duplicated provider maps across 5 files* → §4.9 consumers read the instance
    set; the fixed maps are replaced/migration-only.
16. *Type-keyed behaviors (`==openai`/`anthropic`/`gemini`, fallback guard)* →
    §4.2a re-key all of them on type.

## 9. Risks / open questions

- **Breadth of the behavior-keying migration (§4.2a):** every site that switches
  on the provider string must be found and re-keyed on type; a missed site
  silently regresses a renamed/custom instance. Phase 1a's tests target this.
- **Model-string grammar edge cases:** an instance named the same as a vendor
  prefix (e.g. an instance literally named `anthropic` under an `openrouter`
  type) — names are free-form, so document that instance names shadow nothing
  but themselves and the type drives interpretation.
- **`chat-completions` feature gap (§4.5)** and **Anthropic-compatible protocol
  variance (§4.6)** are accepted Phase 1 limitations.
- **Scope:** Phase 1 is multi-week even sub-phased; 1a alone is a meaningful,
  independently-testable landing.
