# Provider Type/Instance Model

Date: 2026-05-29
Status: v4 — behavior-tag model + unified storage; pending re-review
Ticket: PRI-1880

> **Revision history.**
> - **v1** assumed routing keyed on adapter `Name()`. Wrong.
> - **v2** corrected routing (keys on `req.Provider = profile.ID()`) but missed
>   the id-value branches inside `agent/profile.go`, mis-described `resp.Provider`,
>   and ignored the `launch-check` subprocess.
> - **v3** fixed those but a second review found ~12 more serious issues: the
>   "complete inventory" still missed the system-prompt section coupling, the
>   resume allowlist, two more gemini→google aliases, the error-label literals,
>   and the picker/launch-check type-branches; the no-back-compat migration story
>   had real holes; and three sections were internally contradictory.
> - **v4** (this) introduces a **behavior tag** as the single key for all
>   provider-conditional behavior (separate from both instance name and
>   user-facing type), carries the **complete, re-verified** coupling inventory,
>   and **unifies credential/OAuth storage** so standalone `serf` and the hub
>   share one machine-global provider store. It also **corrects** a v3-review
>   error: OAuth is already machine-global, not per-workspace.

## 1. Goal

Turn serf's hardcoded provider registry into a **type/instance** model: provider
*types* (code-backed adapter + profile recipes) that users *instantiate* into
named, multi-instance, configured providers — including arbitrary
OpenAI-compatible and Anthropic-compatible endpoints and multiple instances of
one vendor. Replace the two duplicative web settings screens (Providers +
Credentials) with one CRUD screen. Unify credential storage so the hub and
standalone `serf` read the same provider config.

## 2. Non-Goals

- **New wire protocols.** We keep existing adapters; no endpoint whose protocol
  no adapter speaks.
- **Per-instance protocol overrides** beyond `apiStyle` and the migrated quirks
  preset (auth scheme, version header, arbitrary new quirks) — deferred.
- **Encryption at rest** (keys stay verbatim, `chmod 600`).
- **Permanent backward-compatibility layers.** Existing local state is converted
  by a **one-time migration** (§4.10); no steady-state env/`credentials.toml`
  fallback, no `gemini` model-prefix alias.
- **Model-prefix aliasing.** Instances route by name only.
- **Per-project credential isolation.** Provider credentials are machine-global
  (they already are for OAuth; §4.9). Per-project *transcript* state is
  unchanged.

## 3. Background — the real architecture

Verified line-by-line against the code on this branch (citations `file:line`).
This section is the authoritative map the design re-keys.

### 3.1 Routing and request construction

`agent` builds requests with `req.Provider = s.profile.ID()`. The **primary
agentic request** is in `processOneInput` at `session.go:2662`; there are two
other request sites — the vision side-channel `describeImage` (`session.go:1460`,
consumed by `Complete` at `:1492`) and the fallback path (`:2709`). Prompt data
is stamped at `session.go:3873` (`PromptData.Provider`) and `:3972`
(`SectionResolver.provider`).

`llm.Client.Complete/Stream` looks up
`c.providers[normalizeProviderName(req.Provider)]` (`client.go:90`); adapters
register under `Name()` (`Register`, `client.go:39`), each a hardcoded constant
(`openai:143`, `anthropic:56`, `google:63`, `openaicompat:122`, `openrouter:22`,
`openrouter_anthropic:33`, `kimi:21`, `glm:21`, `minimax:22`, `ollama:41`). The
default is the first non-`NonDefaultEligible` adapter registered (`client.go:41`),
in `init()` import order. `normalizeProviderName` (`client.go:236`) lowercases,
trims, **and** rewrites `gemini`→`google`; it is called at `client.go:91,122,207,
224`.

### 3.2 Profile selection

`cmdutil.SelectProfile(provider, model, outputSchema)` is a hardcoded switch
(`cmdutil.go:57-73`) that rejects unknown names. Both `google` and `gemini` →
`NewGeminiProfile` (`:62-63`); `kimi`/`glm`/`openrouter`/`ollama` →
`NewOpenAICompatProfile(provider, …)` (`:69-71`). `queryModelContextWindow`
(`cmdutil.go:69`) reads `KIMI_*`/`GLM_*`/`OPENROUTER_*` env **at selection time**
(`cmdutil.go:266-292`).

### 3.3 Everything keyed on the provider/id string (the coupling surface)

**In `agent/profile.go` (keyed on the id *value*):** `CheapModel` `switch p.id`
(`:344`); `decidePrefixAction(id, prefix)` — outer `switch id` (`:397`) **and**
inner `switch prefix` over `ollama/kimi/glm/openrouter/openrouter-anthropic`
(`:405-408`); `rebuildOnSameProviderChange(id)` (`:498-500`); `baseProfile.
WithModel` (`:519,533,562-568`, where the switch path calls `NewOpenAIProfile`
etc. that **hardcode** `id` — `:579,678,699,732,893`); `anthropicProfile.
WithModel` (`:649`); `id == "ollama"` (`:930`); `id == "openrouter" && minimax/`
(`:1007`).

**In `agent/session.go`:** prompt-cache `req.Provider == "openai"` (`:1382`, run
via `applyModelRequestMetadata` at `:2685` on the **2662** request); gemini
handling `profile.ID() == "gemini"` (`:4785`); cross-provider-fallback guard
`fbProfile.ID() != profile.ID()` (`:4140`); prompt-section provider `:3873`/
`:3972` (filenames `fmt.Sprintf("%s.provider-%s", name, r.provider)`,
`section_resolver.go:79-84`; real file `agent/prompts/sections/tools.provider-
openai_append.md`).

**In `llm/`:** `normalizeProviderName` gemini→google (`client.go:236`);
`model_catalog.go:67` (`if p == "gemini" { p = "google" }`) and
`normalizeCatalogProvider` (`:243-249`); catalog `ModelInfo.Provider` stored as
the type (`openai/adapter.go:2117` etc.).

**Identity, hardcoded by adapters:** `resp.Provider` (`openai:1049,1934`,
`anthropic:111/737/1096`, `google:120/523/855`, `openaicompat:164/365/1208`;
`ollama:51` uses its const). `session.go:3511` only fills it when empty (dead).
Error labels: `NewStreamError("openai", …)` (`openai:495,1179`) and the
type-literal `Provider:` fields; `RewriteErrorProvider` is used **only** by ollama
(`ollama:52,58,117`).

**`ProviderOptions`** is a behavior-level contract: the recipe sets
`providerOpts["openai"|"anthropic"|"gemini"|"openai-compatible"]`
(`profile.go:590,710,905,1008`); the adapter reads the same literal
(`openai:296,1244`; `google:217,222` reads **both** `"google"` and `"gemini"`;
`anthropic:286,812`; `openaicompat:535,591`).

**Finish-reason normalization is already behavior-correct:** each adapter calls
`NormalizeFinishReason` with a **static literal** (`google:514,910`,
`openaicompat:359,1202` with `""`, `anthropic:642`), never from `req/resp.
Provider`. No change needed.

### 3.4 Launch gate, picker, resume

The hub launch gate is a **subprocess**: `validateSerfLaunchContract` runs
`serf launch-check --model` (no `--models`) on **every spawn** (`spawn.go:558-564`,
called `:141,186`), and `fetchSpawnScopedModels` runs `--models` for the picker
(`:607`). Inside, `runLaunchCheck` calls `cmdutil.SelectProfile` (`launch_check.
go:68`) and builds a client via `llm.NewFromEnv` (`:94` and `:159`).
`launch_check.go:104` skips `openrouter-anthropic`; `:222` applies an
`openrouter` tools-only filter — both over `client.ProviderNames()`. The web
picker repeats this: `web.go:2038,2064` (`:2064` **hides** non-tool models for
`openrouter`), client built via `NewFromEnv` at `web.go:2024`.

Resume/fork: `resumeProviderFromProfileID` (`app_rpc.go:1735-1742`) is a hardcoded
10-name allowlist fed the persisted `Meta.ProfileID`; its result becomes
`req.Provider` (`:1723-1726`).

### 3.5 Credentials and OAuth (corrected)

**API keys are fragmented.** The hub reads `~/.serf/credentials.toml` (via
`credentials.LoadStore`, `app_auth.go:58`). Standalone `serf` reads **only env**
(`serve.go:39`/`run.go:127` → `llm.NewFromEnv`; no `credentials` import under
`cmd/serf/`). The store hardcodes `providerEnvVars`/`providerAuthModes`
(`credentials/store.go:38,60`); `launchconfig/env.go:30 providerEnvVar` and
`app_auth.go:133 credentialAuthModes` are two more type→env/mode maps. OAuth/auth
branches on `provider == "openai"` at `app_auth.go:108,151,191,249,292,430,461`,
`serf-tui/auth.go:80`, and `AuthRecord.Validate()` rejects `Provider != "openai"`
(`storage.go:142-143`, called by `LoadAuth` `:77`).

**OAuth is machine-global, NOT per-workspace** (this corrects the v3 review):
`serf openai login` writes `resolveOpenAIStateDir`, which **ignores workDir** and
returns `DefaultStateDir()` = `$XDG_STATE_HOME/serf` (`openai_login.go:216-220`);
the adapter reads it via `DefaultStateDirWithStateHome(cfg.StateHome)` where
`cfg.StateHome ← env.StateHome ← XDG_STATE_HOME` (`openai/adapter.go:80`,
`env_registry.go:52`). `WithStateDir` sets the **transcript** `StateDir`
(`env_registry.go:16-18`), which the OAuth path never uses. So there is **one**
`~/.local/state/serf/auth/openai.json`, shared by standalone and hub-spawned
sessions. The per-project `RuntimeDir(origin,wd)` (`runtime_dir.go:18`) governs
transcripts only.

**Spawn/client construction.** Daemon: `serve.go:39 NewFromEnv`. Standalone CLI:
`run.go:127 NewFromEnv`. Other `NewFromEnv` consumers: `web.go:2024`
(fetchLiveModels), `launch_check.go:94,159`, `llmcall/main.go:220`,
`serfeval/main.go:196`, and `llm/generate.go:158 DefaultClient()`.
`launchconfig.ToEnv` injects one provider's key (`env.go:80-95`). No
`providers.toml`/loader exists.

## 4. Design

### 4.1 Three concepts

1. **Instance name** — the routing key. Unique, no `/`. Equals `profile.ID()`,
   the adapter registration name, and `req.Provider`. Free-form (user picks it).
2. **Type (+ `apiStyle` for the openai type)** — the user-facing config choice
   that selects the code recipe. Types: `openai`, `anthropic`, `google`,
   `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`. The
   generic `openai-compatible` is folded into the `openai` type via
   `apiStyle=chat-completions` (§4.7).
3. **Behavior tag** — the internal identity that **every provider-conditional
   behavior keys on**. Derived from type+apiStyle by a pure function:

   | type | apiStyle | behavior tag |
   |---|---|---|
   | openai | responses (default) | `openai` |
   | openai | chat-completions | `openai-compatible` |
   | anthropic | — | `anthropic` |
   | google | — | `google` |
   | openrouter | — | `openrouter` |
   | openrouter-anthropic | — | `openrouter-anthropic` |
   | kimi / glm / minimax / ollama | — | `kimi` / `glm` / `minimax` / `ollama` |

The behavior-tag set is exactly today's distinct profile behaviors. **Back-compat
intuition:** for a default instance named after its type, instance name == type
== behavior tag (except `google`, whose tag is `google` not `gemini`), so existing
behavior is preserved. A renamed or custom instance changes only the *name*;
behavior follows the tag.

This directly implements the "any *real* openai" rule: the OpenAI prompt section,
24h prompt-cache, etc. key on behavior tag `openai`, which a
`chat-completions` instance does **not** have.

### 4.2 Behavior tag is the single behavior key — complete inventory

A `behaviorTag` field travels on the profile (`Type()`→ rename to `BehaviorTag()`
or add alongside) and on the request (`req.BehaviorTag`, set wherever
`req.Provider` is set: **`session.go:2662`** (primary), `:1460` (vision),
`:2709` (fallback), and on `PromptData` `:3873`). Every site in §3.3 is re-keyed:

| Site | Today | After |
|---|---|---|
| `session.go:1382` prompt-cache | `req.Provider == "openai"` | `req.BehaviorTag == "openai"` |
| `session.go:3873/3972` prompt sections | `…= profile.ID()` | `= profile.BehaviorTag()` |
| `session.go:4785` gemini handling | `profile.ID() == "gemini"` | `profile.BehaviorTag() == "google"` |
| `session.go:4140` fallback guard | `fbProfile.ID() != profile.ID()` | `fbProfile.BehaviorTag() != profile.BehaviorTag()` |
| `profile.go:344` CheapModel | `switch p.id` | `switch p.behaviorTag` |
| `profile.go:397/405` decidePrefixAction | id + prefix literals | behavior tag (outer); inner prefix resolved via the selector (§4.6) |
| `profile.go:498` rebuildOnSameProviderChange | id literals | `behaviorTag` |
| `profile.go:519/533/562` WithModel | `p.id` | `p.behaviorTag`; switch path via selector (§4.6) |
| `profile.go:649` anthropic WithModel | literals | `behaviorTag` |
| `profile.go:930` | `id == "ollama"` | `behaviorTag == "ollama"` |
| `profile.go:1007` Codex gate | `id == "openrouter" && minimax/` | `behaviorTag == "openrouter" && minimax/` |
| `client.go:236` normalizeProviderName | lowercase/trim **+ gemini→google** | lowercase/trim **only** (keep the 4 call sites; drop the alias) |
| `model_catalog.go:67/243` | gemini→google | key on behavior tag; drop the alias |
| `launch_check.go:104/222`, `web.go:2038/2064` | `provider == "openrouter[-anthropic]"` | behavior tag of the instance |

**No change (already behavior-correct):** finish normalization (in-adapter static
literal, §3.3); `ProviderOptions` keys (recipe/adapter contract per behavior tag;
the recipe sets the tag's key — for `google` it stays `gemini`/both, matching the
adapter).

### 4.3 Instance name is identity; fix it on every path

`resp.Provider` and error labels are **identity** (which instance answered), so
they carry the **instance name**, not the behavior tag. Centralize: in
`callModel`, after the response returns from **either** the streaming arm
(`session.go:3511`) **or** the non-streaming `s.client.Complete` arm
(`session.go:3353-3358`), set `resp.Provider = req.Provider` unconditionally; do
the same for the vision side-channel (`:1492`). Adapters' hardcoded `resp.Provider`
and error literals are then overwritten centrally — no per-adapter churn for
identity. (Error labels: pass the instance name into each adapter so
`NewStreamError`/`RewriteErrorProvider` use it; or rewrite centrally where the
client wraps adapter errors. Phase 1a picks the smaller of the two.)

### 4.4 Instance-settable profile id, adapter name, behavior tag

- Profile constructors gain an explicit instance-name parameter (or a
  `WithProviderID(profile, name)` wrapper like the existing
  `WithCommunicateOutputSchema`); each recipe also stamps the behavior tag.
  `WithModel` rebuilds preserve **both** the instance name (as id) and the
  behavior tag (§4.6 covers the cross-provider switch).
- Adapters are built **per instance** by `adapterRecipe(instanceName, cfg)` with
  the instance's base URL / key / apiStyle / OAuth state, and `Name()` returns the
  instance name, so the client registers them under it.

### 4.5 Instance-aware selection; resume by lookup

Replace `SelectProfile`'s switch with an instance-aware selector: instance name →
config → build the type's profile via `profileRecipe(id=instanceName, model, cfg)`
with the instance's base URL / apiStyle / context, stamping the behavior tag.
Unknown names error, listing configured instances. Used by the daemon **and** the
`serf launch-check` subprocess (which gets the config via env, §4.12).

**Resume:** delete the `resumeProviderFromProfileID` allowlist (`app_rpc.go:1735`).
The persisted `Meta.ProfileID` **is** the instance name; reconstruct the model ref
by looking it up in the instance config. A persisted instance that no longer
exists yields a clear error (offer re-selection), not a silent empty provider.

### 4.6 Model addressing and the `WithModel` switch

Models are `instanceName/model`; `ParseModelRef` cuts the first slash
(`cmdutil.go:94`). The first segment resolves **only** to a configured instance
name. The `gemini`→`google` rewrite is deleted everywhere (§4.2); the migrated
Gemini instance is named `google`.

`WithModel`'s prefix handling needs the instance set, so a **selector closure**
`selectInstance(name, model) (ProviderProfile, error)` is injected into the
profile at construction (the layer that builds profiles — `NewFromProviders` /
the daemon — holds the instance set). `WithModel(model)`:
- **strip** when the prefix equals the profile's own instance name;
- **keep** when the behavior tag is a meta-provider (`openrouter`,
  `openrouter-anthropic`, `minimax`) and the prefix is an upstream namespace (not
  a configured instance) — the slash is part of the wire model;
- **switch** when the prefix names a *different configured instance* → re-select
  via the injected selector (the rebuilt profile has that instance's name + tag).

This replaces the duplicated constructor table at `profile.go:533/562`. In Phase
1a (instance == type, no config yet) the selector wraps the existing constructor
table; Phase 1b swaps in the real instance selector. This is the hardest
sub-problem (§9) and may be its own sub-phase.

### 4.7 `openai` `apiStyle`

`apiStyle ∈ { responses (default), chat-completions }` selects the full recipe and
sets the behavior tag:
- `responses` → `NewOpenAIProfile` + `openai` adapter (Responses API / Codex /
  OAuth), tag `openai`, `ProviderOptions["openai"]`.
- `chat-completions` → `NewOpenAICompatProfile` + `openaicompat` adapter, tag
  `openai-compatible`, `ProviderOptions["openai-compatible"]`.

`chat-completions` is not feature-equivalent to `responses` (no Codex surface, no
reasoning `encrypted_content`, no responses→chat fallback) — documented in the UI.

### 4.8 `anthropic` instances

`anthropic` instances set a base URL (`NewAnthropicProfile` + `anthropic` adapter,
which already takes one), targeting Anthropic-API-compatible servers (same
`/v1/messages`, `anthropic-version`, `x-api-key`, `cache_control`/`thinking`).
Endpoints needing bearer auth, a different version header, or that reject
`cache_control` are out of scope for Phase 1 (§2).

### 4.9 Unified credential & OAuth storage

Today API keys are split (hub `credentials.toml` vs standalone env) and OAuth is
machine-global under `XDG_STATE_HOME` (§3.5). v4 unifies on a single
machine-global store:

- **`~/.serf/providers.toml`** is the one provider/credential source, read by
  **both** the hub and standalone `serf`. (Standalone serf reading this shared
  store is the concrete fix for the fragmentation §3.5.)
- **OAuth per instance**: `$XDG_STATE_HOME/serf/auth/<instanceName>.json` — the
  **same global root OAuth already uses** (this is *not* a per-workspace change;
  it's a per-instance filename). The default `openai` instance keeps
  `auth/openai.json` (zero move). Thread the instance name + state home through
  `AuthFilePath(stateHome, instanceName)`, `LoadAuth`/`SaveAuth`/`DeleteAuth`, the
  `Service`/`ResolveRuntimeCredentials`, the adapter recipe (which needs
  `cfg.StateHome` + instance name at construction — `openai/adapter.go:80`), and
  the hub auth controller. **`AuthRecord.Validate()` must stop hardcoding
  `Provider == "openai"`** (`storage.go:142-143`): validate against the instance's
  behavior tag `openai` instead. OAuth is offered only for behavior tag `openai`.

### 4.10 `providers.toml`, single source of truth, migration

- **Format** (`chmod 600`): `schema`, `default` (instance name), one
  `[instances.<name>]` per instance with `type`, `base_url`, `api_style`,
  `quirks` (migrated from `OPENAI_COMPATIBLE_PROVIDER_QUIRKS`), and inline
  `api_key` (omitted for OAuth/none).
- **Single source of truth:** `providers.toml` is the only steady-state source.
- **Migration — one-time, on absence, locked, atomic.** The first process (hub
  **or** standalone serf) that finds no `providers.toml` runs migration under a
  `~/.serf/providers.lock` flock and writes via temp-file + atomic rename (so hub
  and standalone can't race or read a torn file). Sources are all **global**: env
  vars, `~/.serf/credentials.toml`, and the global `openai.json`. Because every
  source is global and the write is serialized, whichever process migrates
  produces the same file — no silent key loss.
- **Distinct instance names (no collisions):** `OPENAI_API_KEY` → instance
  `openai` (type openai, responses); `OPENAI_COMPATIBLE_*` → instance
  `openai-compatible` (type openai, chat-completions) — a **distinct** name, so
  the §6 uniqueness rule isn't violated; `ANTHROPIC_API_KEY` → `anthropic`; etc.
  An existing `openai.json` becomes the `openai` instance's OAuth credential.
- **Explicit deterministic default:** `openai` if present, else first by sorted
  name; recorded explicitly and user-editable. The one-time change away from
  today's import-order default is noted in the migration log.

### 4.11 Client / daemon / hub / standalone construction

- New `llm.NewFromProviders(config)`: per instance, build `adapterRecipe(name,
  cfg)`, register under the instance name, set the default. This is the **only**
  steady-state constructor.
- `NewFromEnv` is demoted to a migration helper (interprets env into instances).
- **All current `NewFromEnv` consumers convert to load `providers.toml`**
  (migrating on absence first): `serve.go:39`, `run.go:127`, `web.go:2024`,
  `launch_check.go:94` **and** `:159`, and `DefaultClient()` (`generate.go:158`).
  `cmd/llmcall` and `cmd/serfeval` are dev/eval tools — they adopt the same loader
  for consistency (open question §9 if eval reproducibility argues for env-only).
- Launch gating/credential validation (`spawn.go:138,182`) read the **instance
  set**; `validateProviderCredentials` resolves the instance named by
  `req.Provider` and checks its credential (inline key, or OAuth record by
  behavior tag). The four hardcoded type→env/mode maps (§3.5) are replaced by the
  instance set.

### 4.12 Spawn plumbing

The hub threads `SERF_PROVIDERS_CONFIG=<path>` into `req.Env` via
`launchconfig.ToEnv`, reaching **both** spawned `serf serve` and the `serf
launch-check` subprocess (both the `--model` validate path and the `--models`
picker path). API keys travel in `providers.toml` (`chmod 600`); OAuth stays in
`auth/<name>.json`. The single-provider env injection is removed. (Standalone
serf with no hub reads `~/.serf/providers.toml` directly and migrates on absence.)

### 4.13 Unified UI (Phase 2)

One screen replacing Providers + Credentials: instances grouped by type, each
showing credential source/state and a default marker; **create** (type → name →
base URL / apiStyle / credential), **edit**, **remove**, **set-default**;
per-instance credential management (key set/clear; OAuth sign-in via the PRI-1878
device-code flow for behavior tag `openai`). RPCs operate on the instance config.
The model pickers route/display by instance name, and the **behavior** filters
that currently special-case `openrouter`/`openrouter-anthropic` (`web.go:2064`
hides non-tool models; `launch_check.go:104/222`) are re-keyed on behavior tag —
this is behavior, not just display, so it lands with the picker work.

## 5. Phasing

- **Phase 1a — behavior-tag separation (pure refactor).** Add `BehaviorTag()` /
  `req.BehaviorTag`; re-key every §4.2 site; fix `resp.Provider`/error identity on
  all paths (§4.3); instance-settable id/name/tag (§4.4); replace the resume
  allowlist with a lookup against the (still type-named) instance set (§4.5).
  Proven with existing providers, where instance == type, so behavior is
  observably identical. The selector for `WithModel`'s switch wraps the existing
  constructor table (instance == type). **Acceptance:** full suite green, zero
  behavior change; new tests assert every behavior-tagged site survives a
  *renamed* instance (built directly in tests) — prompt-cache, prompt sections,
  cheap model, finish norm, fallback guard, resume, picker filters, `resp.
  Provider`/error identity.
- **Phase 1b — config-driven instances + unified storage.** `providers.toml` +
  `NewFromProviders` + loader + one-time locked/atomic migration + explicit
  default (§4.10-4.11); convert **all** `NewFromEnv` consumers (§4.11); unified
  per-instance OAuth incl. `AuthRecord.Validate` (§4.9); the openai apiStyle
  recipe + anthropic base-URL instances (§4.7-4.8); route `WithModel`'s switch
  through the instance selector (§4.6); spawn/launch-check config plumbing
  (§4.12); standalone serf reads the shared store. **Acceptance:** custom
  endpoints and multiple instances of a type work end-to-end through the real
  launch path; an existing env/`credentials.toml`/`openai.json` setup migrates and
  resolves the same models with the same credentials.
- **Phase 2 — unified CRUD UI + picker behavior** (§4.13).

Each phase/sub-phase is its own implementation plan.

## 6. Error handling

- **Duplicate/invalid names:** unique, no `/`; loader rejects.
- **Unknown type:** loader errors, naming instance + type.
- **Missing credential:** instance registers but reports "not configured"; launch
  gating surfaces it.
- **Default points at a removed instance:** fall back to first by sorted name, log.
- **Corrupt `providers.toml`:** fail loudly; do not fall back to env or re-migrate
  over it. (A genuinely absent file triggers one-time migration.)
- **Concurrent first run:** the `providers.lock` flock + atomic rename (§4.10)
  serialize migration; the loser sees the winner's file.
- **Retired `gemini/` prefix / unknown instance:** clear error listing configured
  instances.
- **Persisted instance no longer exists (resume):** clear error, offer
  re-selection (§4.5).

## 7. Testing strategy

- **Behavior-tag separation (1a):** for an `openai`-type instance named `work`,
  assert prompt-cache, the `tools.provider-openai_append.md` section, cheap model,
  fallback guard, and Codex/openrouter gates all fire by **tag**; assert a
  `chat-completions` instance does **not** get the `openai` section/cache (the
  "real openai" rule). Two same-tag instances are not a cross-provider fallback.
- **Identity:** a response/error from instance `work` reports `work` on both the
  streaming and non-streaming `Complete` paths and the vision path.
- **Resume:** a session persisted against a custom instance reconstructs its model
  ref via lookup; a removed instance errors clearly.
- **Selection + selector switch:** `SelectProfile`/launch gate resolve a custom
  instance and reject unknowns; `WithModel` switches to another configured
  instance via the selector and keeps upstream namespaces verbatim.
- **Storage/migration:** parse + round-trip `providers.toml`; migrate a fixture
  (env + `credentials.toml` + `OPENAI_COMPATIBLE_*` incl. quirks + `openai.json`)
  into distinct instances (`openai`, `openai-compatible`, …) with explicit
  default; concurrent migrate (two goroutines/processes) yields one valid file.
- **OAuth per instance:** `auth/<name>.json` round-trips; default `openai` keeps
  `openai.json`; `AuthRecord.Validate` accepts a non-`openai`-named openai-tag
  instance.
- **Consumers:** standalone `serf run`, `serve`, both launch-check paths, and the
  web picker all build via `NewFromProviders` and see custom instances.
- **Spawn:** `ToEnv` sets `SERF_PROVIDERS_CONFIG`; the subprocess validates/lists
  a custom instance.
- **Phase 2:** RPC round-trip + JSDOM for the management screen and pickers.

## 8. v3-review findings → v4 resolutions

1. Main-request site mis-cite → §3.1/§4.2 use `session.go:2662` (primary) + 1460 +
   2709 + 3873.
2. System-prompt section coupling → §4.2 re-keys `3873/3972` on behavior tag; the
   "any real openai" rule falls out of the tag (a chat-completions instance lacks
   tag `openai`).
3. Resume allowlist → §4.5 deletes it; resume looks up the persisted instance name.
4. gemini→google in 3 places → §4.2 drops all (client.go:236, model_catalog.go:67,
   :243); catalog keys on behavior tag.
5. `RewriteErrorProvider` overstated → §4.3 sets identity (instance name) centrally
   on all paths, not via one adapter.
6. Launch-check/picker type-literals → §4.2/§4.13 re-key `launch_check.go:104/222`,
   `web.go:2038/2064` on behavior tag (behavior, lands with picker work).
7. Four credential type-maps + ~9 `openai` branches → §4.9/§4.11 replace the maps
   with the instance set and re-key the auth branches + `AuthRecord.Validate` on
   behavior tag.
8. `queryModelContextWindow` request-time env read → §4.5 selection is
   instance-aware (the instance carries base URL/key; context from config/catalog).
9. Credential fragmentation (and the corrected, *not* per-workspace OAuth) →
   §4.9 unified machine-global store read by hub + standalone; §3.5 records the
   correction.
10. `NewFromEnv` consumers undercounted → §4.11 enumerates and converts all
    (serve, run, web fetchLiveModels, both launch_check paths, DefaultClient;
    llmcall/serfeval noted).
11. First-run race / no lock → §4.10 flock + atomic rename.
12. Duplicate-name migration → §4.10 `OPENAI_COMPATIBLE_*` → distinct
    `openai-compatible` instance.
13. Dropped quirks → §4.10 migrates `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` into the
    instance.
14. Env key rotation silently ineffective → accepted under no-back-compat;
    documented (change keys via UI/CLI, not env) — §4.11/§9.
15. `WithModel` switch contradiction → §4.6 concrete selector-injection design;
    flagged as the hardest sub-problem (§9).
16. `resp.Provider` only streaming → §4.3 fixes both paths centrally.
17. Per-instance OAuth unbuildable → §4.9 threads state home + instance name into
    the recipe and relaxes `AuthRecord.Validate`.
18. `normalizeProviderName` "removed" understated → §4.2 keeps lowercase/trim at
    all 4 call sites; only the gemini alias is dropped.

## 9. Risks / open questions

- **`WithModel` selector injection (§4.6)** remains the hardest piece: a profile
  method gaining a config-aware closure, used in fallback hot paths. Confirm the
  injection shape at the start of Phase 1b; it may be its own sub-phase.
- **Behavior-tag breadth (§4.2):** many sites; a miss silently regresses a renamed
  instance. The 1a renamed-instance test matrix is the guard.
- **`llmcall`/`serfeval` scope (§4.11):** adopt `providers.toml` for consistency,
  or keep env-only for eval reproducibility? Recommend adopting; confirm.
- **OAuth state-home vs hub state-root mismatch:** OAuth lives under
  `XDG_STATE_HOME/serf`, `providers.toml` under `~/.serf`. v4 keeps OAuth where it
  already works (per-instance filename only). If we later want one root, that's a
  separate move with its own migration.
- **`chat-completions` feature gap (§4.7)** and **Anthropic-compatible protocol
  variance (§4.8)** are accepted Phase 1 limitations.
- **Scope:** Phase 1 is multi-week even sub-phased; 1a is an independently
  testable, behavior-preserving landing.
