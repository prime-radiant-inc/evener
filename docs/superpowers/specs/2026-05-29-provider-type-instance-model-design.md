# Provider Type/Instance Model

Date: 2026-05-29
Status: v3 — re-grounded against code after a second adversarial review; pending re-review
Ticket: PRI-1880

> **Revision history.** v1 assumed routing keyed on the registered adapter
> `Name()` prefix — wrong. v2 corrected routing (keys on
> `req.Provider = profile.ID()`) but (a) missed that **`agent/profile.go` itself
> branches on the id *value*** in a dozen places, (b) wrongly claimed
> `resp.Provider` "already mirrors `req.Provider`", (c) wrongly listed
> finish-reason normalization among behaviors needing re-keying, and (d) never
> made the **`launch-check` subprocess** an explicit config consumer. v3 was
> re-verified line-by-line against the code on this branch; §3 states the real
> architecture, §4.2 carries the **complete** re-keying inventory, and §8 maps
> every v3 correction to its source site.
>
> **No back-compat.** Per decision, we do **one-time local migration** and keep
> **no permanent compatibility layers**: after migration, `providers.toml` is the
> sole source of truth. Env vars and `credentials.toml` are read *only* by the
> migration step, not as a steady-state fallback; the `gemini`→`google` model
> prefix is retired (the migrated instance is named for its type); and there is no
> dual client-construction path. This is folded into §4.4, §4.8-4.10, §5.

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
- **Permanent backward-compatibility layers.** Existing local state is converted
  by a **one-time migration** (§4.8); we do not keep env/`credentials.toml` as a
  steady-state fallback, nor preserve the `gemini` model prefix.
- **Model-prefix aliasing.** Instances route by name only; no aliases (the
  `gemini`→`google` rewrite is deleted, §4.4).

## 3. Background — the real architecture

Verified against the code on this branch. Citations are `file:line`.

**Routing.** `agent` builds a request with `req.Provider = s.profile.ID()`
(`agent/session.go:1460`, and the fallback path `:2662`/`:2709`).
`llm.Client.Complete/Stream` looks up
`c.providers[normalizeProviderName(req.Provider)]` (`llm/client.go:90`); adapters
register under their `Name()` (`Register`, `client.go:39`), each a **hardcoded
constant** (`openai/adapter.go:143`, `anthropic:56`, `google:63`,
`openaicompat:122`, `openrouter:22`, `openrouter_anthropic:33`, `kimi:21`,
`glm:21`, `minimax:22`, `ollama:41`). The default provider is the first
non-`NonDefaultEligible` adapter registered (`client.go:41`); registration order
is package-`init()` (import) order (`llm/env_registry.go`).

**`normalizeProviderName`** (`client.go:236`) rewrites `gemini`→`google` — a
workaround for the fact that the Gemini profile's id is `gemini` while its
adapter's `Name()` is `google`.

**Profiles.** `cmdutil.SelectProfile(provider, model, outputSchema)` is a
hardcoded `switch provider` (`cmdutil/cmdutil.go:57-73`) that **rejects unknown
names**. Both `google` and `gemini` map to `NewGeminiProfile` (`:62-63`);
`kimi`/`glm`/`openrouter`/`ollama` map to `NewOpenAICompatProfile(provider, …)`
(`:69-71`), which **already takes the id as a parameter**. The profile carries
capabilities, tool surface, context window, quirks, and the `ProviderOptions`
key.

**`agent/profile.go` branches on the id *value*** (this is what v2 missed):
- `CheapModel()` — `switch p.id { case "openai"/"anthropic"/"gemini"/"glm" }`
  (`:344`).
- `decidePrefixAction(id, prefix)` — the slash-prefix strip/keep/switch logic,
  `switch id { case "openrouter"/"openrouter-anthropic"/"minimax"/… }`
  (`:396-414`), with an inner `switch prefix` over `ollama/kimi/glm/openrouter…`
  (`:405-408`).
- `rebuildOnSameProviderChange(id)` — `case "kimi"/"glm"/"openrouter"/"ollama"/
  "openrouter-anthropic"` (`:498-500`).
- `baseProfile.WithModel` — calls `decidePrefixAction(p.id, …)` (`:519`),
  `switch provider { case "kimi"/"glm"/"openrouter"/"ollama" }` (`:533`), and a
  rebuild path `switch p.id { … NewOpenAICompatProfile(p.id, model, 0) }`
  (`:562-568`). `prefixActionSwitch` re-runs the constructor table — a duplicate
  of `SelectProfile`.
- `anthropicProfile.WithModel` — analogous literals (`:649`).
- helper at `:930` (`id == "ollama"`) and the OpenAI-Codex constructor gate at
  `:1007` (`id == "openrouter" && strings.HasPrefix(model, "minimax/")`).

**Behaviors keyed on the provider string in `session.go`:**
- 24h prompt-cache: `req.Provider == "openai"` (`:1382`).
- Gemini handling: `s.profile.ID() == "gemini"` (`:4785`).
- Cross-provider-fallback guard: `fbProfile.ID() != s.profile.ID()` (`:4140`).

**`resp.Provider` is the *type*, hardcoded by each adapter** — `openai:1049`,
`anthropic:111/737/1096`, `google:120/523/855`, `openaicompat:164/365/1208`;
`ollama:51` uses its `providerName` const. `session.go:3511` only sets
`resp.Provider = req.Provider` **when the adapter left it empty** — which never
happens. So today `resp.Provider == type`, and v2's "already mirrors
`req.Provider`" claim is false.

**Finish-reason normalization is already type-correct.** `NormalizeFinishReason`
(`llm/types.go:251`/`normalizeFinish:259`) `switch`es on its `provider` argument,
but that argument is passed **inside each adapter as a static type literal** —
`google/adapter.go:514,910 NormalizeFinishReason("google", …)`,
`openaicompat:359,1202 NormalizeFinishReason("", …)` (empty → OpenAI default),
anthropic likewise `"anthropic"`. It never sees `req.Provider`/`resp.Provider`.
**No change is needed here.**

**`ProviderOptions` is a type-level contract.** The profile recipe sets
`providerOpts["openai"|"anthropic"|"gemini"|"openai-compatible"]`
(`profile.go:590,710,905,1008`); the matching adapter reads the **same literal**
(`openai/adapter.go:296,1244`; `google:217,222` reads both `"google"` and
`"gemini"`; `anthropic:286,812`; `openaicompat:535,591`). The key follows the
type's (profile, adapter) pair, not the instance — **no per-instance change
needed.**

**OAuth.** `internal/auth/openai/storage.go` hardcodes `authFileName =
"openai.json"` (`:15`); `AuthFilePath(stateDir)` is fixed (`:40`); the hub auth
controller branches on `provider == "openai"`. Exactly one OAuth slot.

**Client construction & spawn.** The daemon builds its client via
`llm.NewFromEnv` (`cmd/serf/serve.go`). The hub's launch gate is a **subprocess**:
`spawn.go:607` runs `exec.CommandContext(serfBinary, "launch-check", …,
"--models")`; `validateSerfLaunchContract` (`spawn.go:141,186`) runs
`serf launch-check` and passes it `req.Env`. Inside that subprocess,
`runLaunchCheck` calls `cmdutil.SelectProfile` (`launch_check.go:68`) to validate
and builds a client via `llm.NewFromEnv` (`launch_check.go:94,159`) to list
models for the picker. `validateProviderCredentials(req.Provider, h.Creds,
req.Env)` (`spawn.go:138,182`) checks credentials. `launchconfig.ToEnv` injects
**one** provider's key (`internal/launchconfig/env.go`). There is no
`providers.toml`, `SERF_PROVIDERS_CONFIG`, or config loader anywhere.

**Credentials.** `internal/credentials/store.go` hardcodes `providerEnvVars` /
`providerAuthModes`; keys live in `credentials.toml`. Read by hub auth
(`app_auth.go`), spawn injection, and launch gating.

## 4. Design

### 4.1 Types and instances

A **type** is a code descriptor:
`{ key, profileRecipe(instanceName, model, cfg) ProviderProfile,
   adapterRecipe(instanceName, cfg) ProviderAdapter, defaultBaseURL, authModes,
   optionsKey }`.
Types: `openai`, `anthropic`, `google`, `openrouter`,
`openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`. The generic
`openai-compatible` type folds into `openai` via `apiStyle` (§4.5); the named
vendor types (`openrouter`, `kimi`, `glm`, `minimax`, `ollama`) remain thin
presets over the `openaicompat` adapter with built-in base URLs / quirks.

An **instance** is persisted config:
`{ name (unique; routing key; no "/"), type, baseURL?, apiStyle? (openai only),
   credential: apiKey | oauth | none }`. Any number of instances per type.

### 4.2 The core change: instance *name* (routing) vs provider *type* (behavior)

This is the heart of the work and the part v2 under-scoped.

**(a) An explicit `type` travels with the profile and the request.**
- Add `Type() string` to `ProviderProfile` (a `providerType` field on
  `baseProfile`/`anthropicProfile`, stamped by every constructor/recipe; carried
  through `WithModel` rebuilds — a model change is always within the same type).
- Add `req.ProviderType` (set wherever `req.Provider = profile.ID()` is set:
  `session.go:1460`, and the fallback `:2709`). `req.ProviderType =
  profile.Type()`.
- The routing key is unchanged and stays the **instance name**: `profile.ID()` =
  instance name = adapter registration name = `req.Provider`.

**(b) Re-key every id/provider-value branch onto type.** Complete inventory
(verified §3); each row moves from keying on the *id/provider value* to keying on
the *type*:

| Site | Today keys on | After |
|---|---|---|
| `session.go:1382` prompt-cache | `req.Provider == "openai"` | `req.ProviderType == "openai"` |
| `session.go:4785` gemini handling | `profile.ID() == "gemini"` | `profile.Type() == "google"` |
| `session.go:4140` fallback guard | `fbProfile.ID() != profile.ID()` | `fbProfile.Type() != profile.Type()` (same-type instances are **not** a cross-provider switch) |
| `profile.go:344` `CheapModel` | `switch p.id` | `switch p.providerType` |
| `profile.go:396` `decidePrefixAction(id,…)` | id literals | take + switch `providerType` |
| `profile.go:498` `rebuildOnSameProviderChange(id)` | id literals | `providerType` |
| `profile.go:519/533/562-568` `WithModel` | `p.id` | `p.providerType`; rebuild preserves **both** id=instance name and type (§4.4 for the switch path) |
| `profile.go:649` `anthropicProfile.WithModel` | literals | `providerType` |
| `profile.go:930` helper | `id == "ollama"` | `providerType == "ollama"` |
| `profile.go:1007` Codex gate | `id == "openrouter" && minimax/` | `providerType == "openrouter" && minimax/` |
| `client.go:236` `normalizeProviderName` | rewrites `gemini`→`google` | **removed**; no alias (§4.4) |

**(c) Three sites v2 got wrong — the correct dispositions:**
- **`resp.Provider`** (hardcoded to type by adapters; `session.go:3511` fallback
  is dead). It is used only for *identification* (transcript/display; not for any
  behavior switch — finish-norm is in-adapter, §3). Fix: set
  `resp.Provider = req.Provider` **unconditionally** at `session.go:3511-3512`
  so the response carries the **instance name**. Adapters' hardcoded line is
  harmlessly overwritten; left as-is to minimize churn.
- **Finish-reason normalization** — **no change** (already keyed on the adapter's
  static type literal, §3).
- **`ProviderOptions` keys** — **no change** (type-level contract between recipe
  and adapter, §3).

**(d) Instance-settable profile id + adapter name.**
- Vendor profile constructors gain an explicit instance-name parameter (or a
  `WithProviderID(profile, name)` wrapper matching the existing
  `WithCommunicateOutputSchema`/`WithAllowedDecisions` wrappers).
  `NewOpenAICompatProfile` already takes it.
- Adapters are built **per instance** by the type's `adapterRecipe` with the
  instance's base URL / key / apiStyle and a constructor-set name; `Name()`
  returns the instance name, so the client registers them under it. (Error
  labels via `RewriteErrorProvider` then also carry the instance name, which is
  the desired diagnostic.)

### 4.3 Instance-aware profile selection (daemon **and** launch-check subprocess)

Replace `SelectProfile`'s hardcoded switch with an instance-aware selector:
given an instance name, look it up in the loaded provider config → its type +
config → build the type's profile via `profileRecipe(id=instanceName, model,
cfg)` with the instance's base URL / apiStyle / context, stamping
`providerType = type.key`. Unknown names produce a clear error listing
configured instances.

Both consumers use this selector:
- The **daemon** (in-process).
- The **`serf launch-check` subprocess** (`launch_check.go:68`), which therefore
  must receive the instance config (§4.10 threads `SERF_PROVIDERS_CONFIG` into
  `req.Env`). Its `--models` path (`launchCheckModels`, `launch_check.go:90-117`)
  must build the client via `NewFromProviders` (§4.9) when config is present,
  since that subprocess output drives the web/TUI model picker.

Default instances (named after their type) keep resolving `openai/…`,
`anthropic/…` unchanged.

### 4.4 Model addressing

Models are addressed `instanceName/model`; `ParseModelRef` cuts on the first
slash (`cmdutil.go:94`). The first segment is resolved **only** as a configured
instance name. `normalizeProviderName`'s `gemini`→`google` rewrite
(`client.go:236`) is **deleted** — with no back-compat there is no alias. The
migrated default Gemini instance is named for its type (`google`), so models are
addressed `google/…`; the legacy `gemini/…` prefix is retired (the instance name
is user-editable in `providers.toml` for anyone who prefers it).

Once the instance is resolved, the instance's type-profile interprets the
remainder via `WithModel`/`decidePrefixAction` (now type-keyed) — so an
`openrouter`-type instance still parses `openrouter/anthropic/claude`. Bare,
unqualified models resolve to the configured **default instance**.

**`WithModel`'s cross-provider "switch" path** (`prefixActionSwitch`, which today
re-runs the constructor table, `profile.go:533/562`) is unified with the
instance-aware selector: a prefix that names a *different provider* re-selects
through §4.3 rather than the duplicated table. This needs the selector reachable
from the switch path; see §5 (Phase 1a keeps the table since instance==type
there; Phase 1b routes the switch through the selector) and §9.

### 4.5 `openai` `apiStyle` fold-in

`apiStyle ∈ { responses (default), chat-completions }` selects the **full
triple**, not just a base URL:
- `responses` → `NewOpenAIProfile` + the `openai` adapter (Responses API / Codex
  backend / OAuth), `ProviderOptions["openai"]`.
- `chat-completions` → `NewOpenAICompatProfile` + the `openaicompat` adapter,
  `ProviderOptions["openai-compatible"]`.

So "openai-compatible" = an `openai` instance with `apiStyle=chat-completions` +
base URL. `chat-completions` is **not feature-equivalent** to `responses` (no
Codex tool surface, no reasoning `encrypted_content`, no responses→chat
fallback) — expected for generic compatible servers and documented in the UI.

### 4.6 `anthropic` instances and their limits

`anthropic` instances set a base URL (`NewAnthropicProfile` + the `anthropic`
adapter, which already takes a base URL). This targets
**Anthropic-API-compatible** servers: same `/v1/messages`, `anthropic-version`
header, `x-api-key` auth, and `cache_control`/`thinking` blocks. Endpoints
needing bearer auth, a different version header, or that reject `cache_control`
are **out of scope for Phase 1**; per-instance protocol toggles are a deferred
enhancement (§2).

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
- **Single source of truth:** `providers.toml` is the *only* steady-state source
  for the instance set and credentials. **Migration is the only bridge and runs
  once, on absence:** the first hub (or standalone `serf`) startup that finds no
  `providers.toml` builds default instances from `credentials.toml`, recognized
  env vars, the single `OPENAI_COMPATIBLE_*` slot (→ an `openai` instance with
  `apiStyle=chat-completions`), and any existing `openai.json` (an OAuth-backed
  `openai` instance), then writes `providers.toml`. After that, `credentials.toml`
  and env vars are **never** consulted again (left in place, ignored) — there is
  no permanent fallback.
- **Default rule (explicit, deterministic):** migration sets `default` to the
  `openai` instance if present, else the first instance by sorted name, recorded
  explicitly (user-editable). We deliberately do **not** replicate today's
  import-order default (which resolves to `anthropic` when both keys are set);
  the one-time change is noted in the migration log.

### 4.9 Client / daemon / hub construction

- New `llm.NewFromProviders(config)`: for each instance, build the type's adapter
  via `adapterRecipe(instanceName, cfg)` with the instance's base URL / key /
  apiStyle, register it under the instance name, and set the configured default.
  This is the **only** steady-state constructor. `NewFromEnv` is demoted to an
  internal helper that the migration step (§4.8) uses to interpret env vars into
  instances; it is no longer a runtime path.
- Daemon (`serve.go`) and the hub's own model-listing/credential clients always
  build via `NewFromProviders(load(providersTOML))` (migration guarantees the
  file exists). No `SERF_PROVIDERS_CONFIG`-set-else-`NewFromEnv` branch.
- Launch gating / credential validation (`spawn.go:138,182`) read the
  **instance set**, not the fixed provider maps; `validateProviderCredentials`
  resolves the instance named by `req.Provider` and checks *its* credential
  (inline key in `providers.toml`, or OAuth record per the instance's type). An
  instance with no usable credential reports a clear missing-credential error (as
  today).

### 4.10 Spawn plumbing

The hub owns `providers.toml` and threads `SERF_PROVIDERS_CONFIG=<path>` into
`req.Env` via `launchconfig.ToEnv`, so it reaches **both** spawned `serf serve`
(the daemon, §4.9) **and** the `serf launch-check` subprocess (§4.3). This
replaces the single-provider env injection entirely. API keys travel in the file
(`chmod 600`); OAuth tokens stay in `auth/<name>.json`, which the session reads
via its state dir.

### 4.11 Unified UI (Phase 2)

One screen replacing Providers + Credentials: instances grouped by type, each
showing its credential source/state and a default marker; **create** (pick type
→ name → base URL / apiStyle / credential), **edit**, **remove**, **set-default**;
per-instance credential management (key set/clear; OAuth sign-in via the
PRI-1878 device-code flow for OAuth-capable types). RPCs operate on the instance
config. The TUI/web model pickers are updated to display and route by instance
name (generalizing the hardcoded prefix allowlist in
`cmd/serf-tui/model_display.go` and the web equivalent), driven by the
`launch-check --models` output (§4.3).

## 5. Phasing

- **Phase 1a — name/type separation as a pure refactor.** Add `Type()` /
  `req.ProviderType`; re-key every site in §4.2b onto type; apply the §4.2c
  corrections (unconditional `resp.Provider`); make profile id + adapter name
  instance-settable (§4.2d). **Proven with the existing env-driven providers,
  where instance name == type**, so behavior is observably identical — this
  de-risks the routing/behavior rework in isolation. `WithModel`'s switch path
  keeps the constructor table here (valid because instance == type).
  Acceptance: full suite green with no behavior change; new unit tests assert
  type-keyed behavior survives a *renamed* instance (constructed directly in
  tests).
- **Phase 1b — config-driven instances.** `providers.toml` + `NewFromProviders`
  + loader + one-time migration + explicit default (§4.8-4.9); **retire the
  steady-state env path** (`NewFromEnv` demoted to a migration helper);
  per-instance OAuth (§4.7); daemon/hub/spawn/launch-check consumers +
  `SERF_PROVIDERS_CONFIG` (§4.3, §4.9-4.10); the `openai` apiStyle triple (§4.5)
  and `anthropic` base-URL instances (§4.6); delete `normalizeProviderName`'s
  alias (§4.4); route `WithModel`'s switch path through the instance selector
  (§4.4). Acceptance: custom endpoints and multiple instances of a type work
  end-to-end through the real launch path via a hand-edited `providers.toml`; an
  existing env/`credentials.toml` setup migrates to `providers.toml` on first run
  and resolves the same models.
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
  refuses to fall back to env-only behavior (which would drop file/OAuth
  instances and could change which model answers). It does **not** silently
  re-migrate over a corrupt file. (A genuinely *absent* file still triggers the
  one-time migration, §4.8.)
- **Retired `gemini/` prefix:** resolves like any unknown instance name — a clear
  error naming the configured instances (the migrated instance is `google`).

## 7. Testing strategy

- **Name/type separation (1a):** unit tests that a profile of type `openai` with
  instance name `work` still gets prompt-cache / gemini-handling / cheap-model /
  prefix-action per *type*; that two same-type instances are not a cross-provider
  fallback (`session.go:4140`). Cover every §4.2b row. (`agent`, `llm`)
- **`resp.Provider`:** a response from instance `work` reports
  `resp.Provider == "work"`, and finish normalization is still correct (proving
  it is independent of `resp.Provider`). (`agent`, `llm`)
- **Instance-aware selection:** `SelectProfile`/launch gate resolve a configured
  custom instance and reject unknown names. (`cmdutil`, `cmd/serf-hub`)
- **Loader / migration:** parse + round-trip `providers.toml`; migrate a fixture
  `credentials.toml` (+ env, + `OPENAI_COMPATIBLE_*`, + `openai.json`) into the
  expected instances and explicit default. (`internal/...`)
- **Multi-instance routing:** N instances of a type → N adapters routed by
  instance name; `apiStyle` selects the responses vs chat-completions triple;
  default-instance resolution; a retired `gemini/` ref errors as an unknown
  instance. (`llm`, `cmdutil`)
- **OAuth per instance:** `auth/<name>.json` round-trips; the default `openai`
  instance still uses `openai.json`. (`internal/auth/openai`, `cmd/serf-hub`)
- **Spawn + launch-check subprocess:** `ToEnv` sets `SERF_PROVIDERS_CONFIG`; the
  `serf launch-check` subprocess validates a custom instance and lists its models
  via `NewFromProviders`; a spawned `serf serve` builds its client from the same
  file. (`internal/launchconfig`, `cmd/serf-hub`, `cmd/serf`)
- **Adapters:** existing tests stay green; add `anthropic` base-URL override
  coverage.
- **Phase 2:** RPC round-trip + JSDOM tests for the management screen and pickers.

## 8. v3 corrections & added scope (each verified against code)

1. **profile.go id-value branches** (`CheapModel:344`, `decidePrefixAction:396`,
   `rebuildOnSameProviderChange:498`, `WithModel:519/533/562-568/649`, `:930`,
   `:1007`) → §4.2b re-keys all onto `providerType`. *(v2 missed this entire
   category.)*
2. **`resp.Provider` is hardcoded to type, not mirrored** (`openai:1049` et al.;
   dead fallback at `session.go:3511`) → §4.2c sets it to the instance name
   unconditionally.
3. **Finish-norm is already type-correct** (in-adapter static literal,
   `google:514`, `openaicompat:359`) → §4.2c: **removed** from the re-key list.
   *(v2 wrongly listed it.)*
4. **`ProviderOptions` keys are a type-level contract** (`profile.go:590` vs
   `openai/adapter.go:296`, etc.) → §4.2c: **no change**. *(v2 flagged it as an
   open worry.)*
5. **`launch-check` is a subprocess** (`spawn.go:607`) running `SelectProfile`
   (`launch_check.go:68`) and `NewFromEnv` (`:94,159`) → §4.3/§4.10 thread
   `SERF_PROVIDERS_CONFIG` through `req.Env` and build `--models` via
   `NewFromProviders`. *(v2 never made the subprocess explicit.)*
6. **`WithModel`'s switch path duplicates `SelectProfile`** (`profile.go:533/562`)
   → §4.4 unifies it through the instance selector (Phase 1b); §4.2b keeps id +
   type both through rebuilds.
7. **`normalizeProviderName` `gemini`→`google`** (`client.go:236`) is an
   id-vs-adapter-name workaround → §4.2b/§4.4 **delete** it; no alias replaces it
   (no back-compat); the migrated default google instance is named `google`.

**No-back-compat simplifications** (per the migration decision): the permanent
env/`credentials.toml` fallback is removed (env is migration input only, §4.8);
the dual `SERF_PROVIDERS_CONFIG`-else-`NewFromEnv` construction path collapses to
`NewFromProviders`-only (§4.9); the `gemini` prefix is retired (§4.4).

**Carried forward from v2's round-1 resolutions** (still valid): routing on
`profile.ID()`; instance-aware `SelectProfile`; per-instance OAuth; apiStyle
triple; `providers.toml` single source of truth + explicit deterministic default;
instance-set-driven spawn injection/gating; model-picker prefix updates.

## 9. Risks / open questions

- **Breadth of the type-keying migration (§4.2b):** it now spans `session.go`,
  `llm/client.go`, and ~10 sites in `agent/profile.go`. A missed site silently
  regresses a renamed/custom instance. Phase 1a's per-row tests target this; the
  pure-refactor framing (instance==type) makes 1a provably behavior-preserving.
- **`WithModel` switch path needs the selector (§4.4).** `WithModel` is a profile
  method with no config handle today, and it runs in fallback hot paths. Routing
  its switch through the instance selector means injecting a selector (a function
  set at profile construction) or moving cross-provider model switches up into
  the agent layer (which holds the registry). Recommended: inject a selector
  closure at construction; decide concretely at the start of Phase 1b. This is
  the single hardest sub-problem.
- **`chat-completions` feature gap (§4.5)** and **Anthropic-compatible protocol
  variance (§4.6)** are accepted Phase 1 limitations.
- **Instance named like a vendor prefix** (e.g. an instance literally named
  `anthropic` under an `openrouter` type): names are free-form and shadow only
  themselves; the type drives model-string interpretation. Documented, not
  prevented.
- **Scope:** Phase 1 is multi-week even sub-phased; 1a alone is a meaningful,
  independently-testable, behavior-preserving landing.
