# Provider Type/Instance Model

Date: 2026-05-29
Status: v5 — switching dropped, migration replaced by a flag day; pending re-review
Ticket: PRI-1880

> **Revision history.**
> - **v1** assumed routing keyed on adapter `Name()`. Wrong.
> - **v2** corrected routing but missed the `agent/profile.go` id-value branches,
>   mis-described `resp.Provider`, and ignored the `launch-check` subprocess.
> - **v3** fixed those; a second review found ~12 more (system-prompt section
>   coupling, resume allowlist, catalog aliases, error labels, picker branches,
>   migration holes, 3 contradictions).
> - **v4** introduced the **behavior tag** and unified storage; review confirmed
>   all 12 v3 findings resolved but found ~11 new issues in three clusters:
>   (A) package layering, (B) model-prefix resolution, (C) migration determinism.
> - **v5** (this) applies two decisions that dissolve the hard clusters:
>   **(1) drop `WithModel`'s cross-provider switch** — all provider/instance
>   selection goes through one top-level selector (kills the selector import-cycle
>   and the meta-provider name collision); **(2) no migration — flag day.** With
>   zero deployed instances we zap old configs instead of migrating (kills the
>   env-determinism, lock, and lossless-conversion problems). v5 also closes the
>   residual inventory misses and specifies a leaf config package so the behavior
>   tag is reachable everywhere.

## 1. Goal

Turn serf's hardcoded provider registry into a **type/instance** model: provider
*types* (code-backed adapter + profile recipes) that users *instantiate* into
named, multi-instance, configured providers — including arbitrary
OpenAI-compatible and Anthropic-compatible endpoints and multiple instances of
one vendor. Replace the two duplicative web settings screens (Providers +
Credentials) with one CRUD screen. Unify credential storage so the hub and
standalone `serf` read the same provider config.

## 2. Non-Goals

- **New wire protocols.** No endpoint whose protocol no adapter speaks.
- **Per-instance protocol overrides** beyond `apiStyle` (auth scheme, version
  header, arbitrary quirks) — deferred.
- **Encryption at rest** (keys verbatim, `chmod 600`).
- **Cross-provider switching via a model string.** Provider/instance selection
  happens once, at the top level, by instance name (§4.6). `WithModel` only
  changes the model within an instance.
- **Migration / backward compatibility.** There are **zero deployed instances**,
  so we declare a **flag day**: old `credentials.toml` / provider env vars /
  `openai.json` are not converted. `providers.toml` (or the ephemeral env
  fallback, §4.10) is the only config the new code reads.
- **Model-prefix aliasing.** Instances route by name only (no `gemini` alias).

## 3. Background — the real architecture

Verified line-by-line against the code on this branch (`file:line`). This is the
authoritative map the design re-keys.

### 3.1 Routing and request construction

`agent` builds requests with `req.Provider = s.profile.ID()`. The **primary**
request is in `processOneInput` at `session.go:2662`; the others are the vision
side-channel `describeImage` (`session.go:1460`, consumed by `Complete` at
`:1492`) and the fallback (`:2709`). Prompt data is stamped at `session.go:3873`
(`PromptData.Provider`) and `:3972` (`SectionResolver.provider`).

`llm.Client.Complete/Stream` looks up
`c.providers[normalizeProviderName(req.Provider)]` (`client.go:90`); adapters
register under `Name()` (`Register`, `client.go:39`), each a hardcoded constant.
The default is the first non-`NonDefaultEligible` adapter (`client.go:41`).
`normalizeProviderName` (`client.go:236`, called `:91,122,207,224`) lowercases,
trims, **and** rewrites `gemini`→`google`.

### 3.2 Profile selection and the model catalog

`cmdutil.SelectProfile(provider, model, outputSchema)` is a hardcoded switch
(`cmdutil.go:57-73`) rejecting unknown names; both `google`/`gemini` →
`NewGeminiProfile` (`:62-63`); `kimi`/`glm`/`openrouter`/`ollama` →
`NewOpenAICompatProfile(provider, …)` (`:69-71`). `NewGeminiProfile` stamps
`id: "gemini"` (`profile.go:699`) while the google adapter's `Name()` is
`"google"` (`google/adapter.go:63`) — the mismatch the gemini→google rewrite
papers over. `queryModelContextWindow` (`cmdutil.go:69`) reads `KIMI_*`/`GLM_*`/
`OPENROUTER_*` env at selection time (`cmdutil.go:266-292`). The compat catalog
lookup keys on the constructor `id`: `resolveOpenAICompatCatalogModel(get, id,
model)` looks up `id+"/"+model` then bare `model` (`profile.go:954-966`), with
`suppressBareCatalogLookup(id)` = `id=="ollama"` (`:929`).

### 3.3 Everything keyed on the provider/id string

**`agent/profile.go` (id value):** `CheapModel` `switch p.id` incl. `case
"gemini"` (`:344-355`); `decidePrefixAction(id, prefix)` outer `switch id`
(`:397`) + inner `switch prefix` (`:405-408`); `rebuildOnSameProviderChange(id)`
(`:498-500`); `WithModel` (`:519,533,562-568`, switch path calls
`NewOpenAIProfile` etc. that hardcode `id` — `:579,678,699,732,893`);
`anthropicProfile.WithModel` (`:649`); `id=="ollama"` (`:930`); catalog prefix
(`:955,964`); `id=="openrouter" && minimax/` (`:1007`).

**`agent/session.go`:** prompt-cache `req.Provider=="openai"` (`:1382`, run at
`:2685` on the 2662 request); gemini `profile.ID()=="gemini"` (`:4785`);
fallback guard `fbProfile.ID()!=profile.ID()` (`:4140`); prompt sections
`:3873`/`:3972` (filenames `"%s.provider-%s"`, `section_resolver.go:79-84`; real
file `agent/prompts/sections/tools.provider-openai_append.md`).

**`llm/`:** `normalizeProviderName` gemini→google (`client.go:236`);
`model_catalog.go:67` + `normalizeCatalogProvider` (`:243-249`); catalog stores
`ModelInfo.Provider` = type (`openai/adapter.go:2117`). `normalizeFinish`
(`types.go:259`) switches on its arg, but adapters pass a **static literal**
(`google:514`, `openaicompat:359` `""`, `anthropic:642`) — **already correct, no
change.** `ProviderOptions` is a behavior-level contract: recipe sets
`providerOpts[<tag>]` (`profile.go:590,710,905,1008`), adapter reads the same
(`openai:296,1244`; `google:217,222` reads both `"google"`+`"gemini"`;
`anthropic:286,812`; `openaicompat:535,591`) — **no per-instance change.**

**Identity (hardcoded by adapters):** `resp.Provider` (`openai:1049,1934`,
`anthropic:111/737/1096`, `google:120/523/855`, `openaicompat:164/365/1208`);
`session.go:3511` fills it only when empty (dead). `resp.Provider` has **almost
no production reader** (only `cmd/llmcall`; the API log uses `req.Provider`,
`apilog.go:165`). **Error labels** are the real identity surface: ~20 hardcoded
type literals across `WrapContextError`/`ErrorFromHTTPStatus`/`NewStreamError`/
`NewUnsupportedToolChoiceError` in openai/anthropic/google/openaicompat
adapters; `RewriteErrorProvider` (`errors.go:115`, no-ops on empty Provider) is
used **only** by ollama.

### 3.4 Launch gate, picker, resume

The hub launch gate is a **subprocess**: `validateSerfLaunchContract` runs
`serf launch-check --model` (no `--models`) on **every spawn** (`spawn.go:558-564`,
called `:141,186`), and `--models` for the picker (`:607`). Inside, `runLaunchCheck`
calls `SelectProfile` (`launch_check.go:68`) and `llm.NewFromEnv` (`:94`, `:159`).
Type-literal branches over `client.ProviderNames()` (instance names): skip
`openrouter-anthropic` (`launch_check.go:104`); `openrouter` tools filter (`:222`);
web picker `web.go:2038,2064` (`:2064` **hides** non-tool models for openrouter),
client via `NewFromEnv` (`web.go:2024`). Hub launch validation also has
`launchProviderAllowsUnreportedModels` = `EqualFold(provider,
"openrouter-anthropic")` (`app_rpc.go:1526`, called `:1518` with the instance
name). Resume: `resumeProviderFromProfileID` is a hardcoded 10-name allowlist fed
`Meta.ProfileID` (`app_rpc.go:1735-1742`).

### 3.5 Credentials and OAuth

**API keys are fragmented:** the hub reads `~/.serf/credentials.toml`
(`app_auth.go:58`); standalone `serf` reads **only env** (`serve.go:39`/
`run.go:127` → `NewFromEnv`; no `credentials` import under `cmd/serf/`). Type→env/
mode maps (FIVE): `credentials/store.go:38,60`, `launchconfig/env.go:30`,
`app_auth.go:133`, `cmdutil.go:266`. `openai`-literal auth branches:
`app_auth.go:108,151,191,249,292,430,461`, `serf-tui/auth.go:26,27,80`,
`spawn.go:466 validateProviderCredentials`, and `AuthRecord.Validate()` rejects
`Provider != "openai"` (`storage.go:142-143`, called by `LoadAuth` `:77`).

**OAuth is machine-global, not per-workspace:** `serf openai login`'s
`resolveOpenAIStateDir` ignores workDir, returns `DefaultStateDir()` =
`$XDG_STATE_HOME/serf` (`openai_login.go:216-220`); the adapter reads it via
`DefaultStateDirWithStateHome(cfg.StateHome ← env.StateHome ← XDG_STATE_HOME)`
(`openai/adapter.go:80`, `env_registry.go:52`). The per-project `RuntimeDir`
(`runtime_dir.go:18`) governs transcripts only (`WithStateDir` sets `cfg.StateDir`,
`env_registry.go:16-18`). So one global `~/.local/state/serf/auth/openai.json`.

**Construction:** `NewFromEnv` consumers: `serve.go:39`, `run.go:127`,
`web.go:2024`, `launch_check.go:94,159`, `llmcall/main.go:220`,
`serfeval/main.go:196`, `generate.go:158 DefaultClient()`.

## 4. Design

### 4.1 Three concepts (+ a leaf config package)

1. **Instance name** — routing key. Unique, no `/`, **lowercased on create**
   (§6). Equals `profile.ID()`, the adapter registration name, and `req.Provider`.
2. **Type (+ `apiStyle` for openai)** — the user-facing config choice selecting
   the code recipe. Types: `openai`, `anthropic`, `google`, `openrouter`,
   `openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`. Generic
   `openai-compatible` folds into `openai` via `apiStyle=chat-completions` (§4.7).
3. **Behavior tag** — the internal identity **every provider-conditional behavior
   keys on**. Pure function `BehaviorTag(type, apiStyle)`:

   | type | apiStyle | tag |
   |---|---|---|
   | openai | responses (default) | `openai` |
   | openai | chat-completions | `openai-compatible` |
   | anthropic | — | `anthropic` |
   | google | — | `google` |
   | openrouter / openrouter-anthropic / kimi / glm / minimax / ollama | — | (same as type) |

**Leaf config package** `internal/providerconfig` (no deps on `llm`/`agent`, so
both import it without a cycle): defines `InstanceConfig {name, type, apiStyle,
baseURL, credential}`, the parsed `Config` (instances + default), the loader, and
the pure `BehaviorTag(type, apiStyle)` / `NameToTag(cfg)` helpers. `llm` builds
adapters from it; `agent`/`cmdutil` build profiles from it; `cmd/serf-hub`
edits it; the client-only picker/launch sites read its `NameToTag` map (resolving
finding A#2 — the tag is reachable everywhere without a `*llm.Client` accessor).

### 4.2 Behavior tag is the single behavior key — complete inventory

A `behaviorTag` travels on the profile (`BehaviorTag()`) and on the request
(`req.BehaviorTag`, set wherever `req.Provider` is — `session.go:2662` primary,
`:1460` vision, `:2709` fallback, `:3873` prompt data). Every site:

| Site | Today | After |
|---|---|---|
| `session.go:1382` prompt-cache | `req.Provider == "openai"` | `req.BehaviorTag == "openai"` |
| `session.go:3873/3972` prompt sections | `= profile.ID()` | `= profile.BehaviorTag()` |
| `session.go:4785` gemini | `profile.ID() == "gemini"` | `profile.BehaviorTag() == "google"` |
| `session.go:4140` fallback guard | `fbProfile.ID() != profile.ID()` | `fbProfile.BehaviorTag() != profile.BehaviorTag()` |
| `profile.go:344` CheapModel | `switch p.id` incl `case "gemini"` | `switch p.behaviorTag`, `case "google"` |
| `profile.go:396` decidePrefixAction | id+prefix literals | `behaviorTag`; **strip/keep only**, no switch (§4.6) |
| `profile.go:498` rebuildOnSameProviderChange | id literals | `behaviorTag` |
| `profile.go:519/533/562` WithModel | `p.id`, switch-via-constructor-table | `p.behaviorTag`, **switch path removed** (§4.6) |
| `profile.go:649` anthropic WithModel | literals | `behaviorTag` |
| `profile.go:699` NewGeminiProfile | `id: "gemini"` | `id = instanceName`; behavior tag `google` (see note) |
| `profile.go:930` suppress-bare | `id == "ollama"` | `behaviorTag == "ollama"` |
| `profile.go:955/964` catalog lookup | `id + "/" + model` | `behaviorTag + "/" + model` (catalog keys on the tag, not the instance name) |
| `profile.go:1007` Codex gate | `id == "openrouter" && minimax/` | `behaviorTag == "openrouter" && minimax/` |
| `client.go:236` normalizeProviderName | lowercase/trim **+ gemini→google** | lowercase/trim **only** (keep the 4 call sites; drop the alias) |
| `model_catalog.go:67/243` | gemini→google | key on behavior tag; drop the alias |
| `launch_check.go:104/222`, `web.go:2038/2064` | `provider == "openrouter[-anthropic]"` | `NameToTag[instance]` (config-derived, §4.1) |
| `app_rpc.go:1526` launchProviderAllowsUnreportedModels | `EqualFold(provider, "openrouter-anthropic")` | `NameToTag[instance] == "openrouter-anthropic"` |

**Gemini note (finding #8):** because `NewGeminiProfile` hardcodes `id:"gemini"`
and the adapter `Name()` is `"google"`, simply deleting the alias would break
routing. The instance-aware recipe sets `id = instanceName` (default Gemini
instance is named `google`) **and** registers the google adapter under that name,
so routing needs no rewrite. `CheapModel`'s `case "gemini"` becomes `case
"google"` (the tag). `ProviderOptions` keeps the `gemini` key (the adapter reads
both — `google/adapter.go:217,222`).

**No change:** finish normalization (in-adapter static literal); `ProviderOptions`
keys (behavior-tag contract).

### 4.3 Identity = instance name

`resp.Provider` and **error labels** carry the **instance name**. `resp.Provider`
is low-value (§3.3) but cheap to set right: in `callModel`, after the streaming
arm (`consumeModelStream`, `session.go:3511`) and the non-streaming `Complete`
arm (`:3358`) return, set `resp.Provider = req.Provider`; same for the vision
side-channel (`:1492`). **Error labels are the real surface:** rewrite the
provider on errors **centrally at the `llm.Client` boundary** — wrap adapter
errors in `Complete`/`Stream` with `RewriteErrorProvider(err, req.Provider)`
(removing its empty-Provider no-op so it always stamps the instance name). This
replaces the ~20 hardcoded adapter literals with one chokepoint per client entry,
no per-adapter churn.

### 4.4 Instance-settable profile id, adapter name, behavior tag

Profile constructors take an explicit instance-name parameter (or a
`WithProviderID(profile, name)` wrapper); each recipe stamps the behavior tag.
Adapters are built per instance by `adapterRecipe(instanceName, cfg)` with the
instance's base URL / key / apiStyle / OAuth state; `Name()` returns the instance
name. `WithModel` preserves the instance name + tag (it never switches, §4.6).

### 4.5 Instance-aware selection; resume by lookup

One selector (in `cmdutil`/`agent`, which already import both `llm` and
`internal/providerconfig`): instance name → config → build the type's profile via
`profileRecipe(id=instanceName, model, cfg)`, stamping the behavior tag. Unknown
names error, listing instances. Used by the daemon **and** the `serf launch-check`
subprocess (config via env, §4.12). This is the **only** path that maps a
provider/instance reference to a profile — there is no second switch path in
`WithModel` (§4.6), so no selector closure is injected into profiles and there is
no `llm`→`agent` cycle (finding A#1 dissolved).

**Resume:** delete the `resumeProviderFromProfileID` allowlist (`app_rpc.go:1735`).
`Meta.ProfileID` is the instance name; reconstruct the ref by looking it up in the
instance config (the hub's `WebConfig` carries it). A vanished instance errors
clearly with re-selection, not a silent empty provider.

### 4.6 Model addressing (no in-profile switching)

Models are `instanceName/model`; `ParseModelRef` cuts the first slash. The first
segment resolves **only** to a configured instance name (the `gemini` alias is
gone). Bare models → the default instance.

`WithModel(model)` changes the model **within the current instance only**:
- **strip** when the prefix equals the profile's own instance name (redundant
  self-prefix);
- **keep** when the behavior tag is a meta-provider (`openrouter`,
  `openrouter-anthropic`, `minimax`) and the slash is part of the upstream wire
  model (e.g. `openrouter/anthropic/claude…` → `anthropic/claude…`);
- otherwise the model is taken verbatim within the instance.

`WithModel` **never switches providers.** A model string implying a different
provider is not a switch: cross-provider `model_fallbacks` are already rejected by
the guard (`session.go:4140`, re-keyed to behavior tag), and interactive
provider/instance changes re-resolve through the top-level selector (§4.5). This
removes the duplicated constructor table (`profile.go:533/562`) and the
instance-name-vs-upstream-namespace ambiguity (a default `anthropic` instance no
longer hijacks `openrouter/anthropic/…`, finding B#3).

### 4.7 `openai` `apiStyle`

`apiStyle ∈ { responses (default), chat-completions }` selects the recipe and the
behavior tag: `responses` → `NewOpenAIProfile` + `openai` adapter (Responses /
Codex / OAuth), tag `openai`, `ProviderOptions["openai"]`; `chat-completions` →
`NewOpenAICompatProfile` + `openaicompat` adapter, tag `openai-compatible`,
`ProviderOptions["openai-compatible"]`. `chat-completions` is not
feature-equivalent (no Codex surface, no reasoning `encrypted_content`) —
documented in the UI.

### 4.8 `anthropic` instances

`anthropic` instances set a base URL (`NewAnthropicProfile` + `anthropic` adapter,
which already takes one), targeting Anthropic-API-compatible servers (same
`/v1/messages`, `anthropic-version`, `x-api-key`, `cache_control`/`thinking`).
Bearer auth / different version header / `cache_control`-rejecting endpoints are
out of scope for Phase 1 (§2).

### 4.9 Per-instance OAuth (keyed by instance name)

OAuth is keyed by **instance name** end to end (resolving the v4 §4.9↔§4.11
contradiction — there is no "by behavior tag" variant):
`AuthFilePath(stateHome, instanceName)` → `$XDG_STATE_HOME/serf/auth/
<instanceName>.json` (same global root OAuth already uses; per-instance filename).
The default `openai` instance keeps `auth/openai.json`. Thread the instance name +
`cfg.StateHome` through `LoadAuth`/`SaveAuth`/`DeleteAuth`, the
`Service`/`ResolveRuntimeCredentials`, the adapter recipe (`openai/adapter.go:80`),
the hub auth controller (`app_auth.go`'s `openai` branches), the TUI
(`serf-tui/auth.go:26,27,80` — its `authStatus` gains the behavior tag), and
`validateProviderCredentials` (`spawn.go:466`, which resolves the instance and
reads `auth/<instance>.json`). **`AuthRecord.Validate()`** stops hardcoding
`Provider == "openai"` (`storage.go:142-143`): it validates against behavior tag
`openai`. OAuth is offered only for behavior tag `openai`.

### 4.10 Storage — `providers.toml`, ephemeral env fallback, flag day

- **`~/.serf/providers.toml`** (`chmod 600`): `schema`, `default` (instance
  name), one `[instances.<name>]` per instance with `type`, `base_url`,
  `api_style`, and inline `api_key` (omitted for OAuth/none). Read by **both** the
  hub and standalone `serf`.
- **No migration (flag day).** With zero deployed instances, old
  `credentials.toml` / provider env vars / `openai.json` are **not** converted;
  they are abandoned. `providers.toml` is created by the hub UI / a `serf
  providers` CLI / hand-editing. (This removes all of v4's migration machinery —
  no env-determinism, no lock, no atomic conversion, no duplicate-name/quirks/
  openrouter-dual handling.)
- **Ephemeral env fallback.** When `providers.toml` is **absent**, serf builds an
  **in-memory** config from env vars for that run (the existing `NewFromEnv`
  behavior) and **writes nothing**. So zero-config `OPENAI_API_KEY=… serf run`
  still works; the hub UI is how you persist a `providers.toml`. Env is never a
  layer *when the file exists* (the file is authoritative).
- **Explicit default:** the file's `default` field; for the ephemeral env case,
  the first registered eligible adapter (today's behavior).

### 4.11 Client / daemon / hub / standalone construction

- New `llm.NewFromProviders(cfg providerconfig.Config)`: per instance build
  `adapterRecipe(name, cfg)`, register under the instance name, set the default.
- **Load rule (one helper, used by every consumer):** if `~/.serf/providers.toml`
  exists → `NewFromProviders(load())`; else → `NewFromEnv` (ephemeral, §4.10).
  Consumers converted: `serve.go:39`, `run.go:127`, `web.go:2024`,
  `launch_check.go:94` **and** `:159`, `DefaultClient()` (`generate.go:158`);
  `llmcall`/`serfeval` adopt the same helper.
- Launch gating/credential validation read the **instance set**, not the five
  type maps (§3.5); `validateProviderCredentials` resolves the instance named by
  `req.Provider` and checks its credential (inline key, or OAuth by instance name
  §4.9).
- The client-only picker/launch sites get the config's `NameToTag` map alongside
  the client (§4.1) to re-key their behavior filters.

### 4.12 Spawn plumbing

The hub threads `SERF_PROVIDERS_CONFIG=<path>` into `req.Env` via
`launchconfig.ToEnv`, reaching **both** spawned `serf serve` and the `serf
launch-check` subprocess (the `--model` validate path and the `--models` picker
path). The single-provider env injection is removed. Standalone serf with no hub
reads `~/.serf/providers.toml`, or the ephemeral env fallback (§4.10).

### 4.13 Unified UI (Phase 2)

One screen replacing Providers + Credentials: instances grouped by type, each
showing credential source/state + a default marker; create (type → name → base
URL / apiStyle / credential), edit, remove, set-default; per-instance credentials
(key set/clear; OAuth sign-in via the PRI-1878 device-code flow for behavior tag
`openai`). RPCs operate on the instance config. Pickers route/display by instance
name; the behavior filters (`web.go:2064` hides non-tool openrouter models;
`launch_check.go:104/222`) are re-keyed via `NameToTag` (§4.2) — behavior, not
just display, so it lands with the picker work.

## 5. Phasing

- **Phase 1a — behavior-tag separation (pure refactor).** Add `BehaviorTag()` /
  `req.BehaviorTag`; introduce `internal/providerconfig` with the tag function;
  re-key every §4.2 site (incl. the gemini-id fix, catalog-by-tag, picker/launch
  filters via `NameToTag`, `launchProviderAllowsUnreportedModels`); central error
  identity + `resp.Provider` (§4.3); instance-settable id/name/tag (§4.4); remove
  `WithModel`'s switch path (§4.6); replace the resume allowlist with a lookup
  (§4.5). Proven with existing providers (instance == type), behavior identical.
  **Acceptance:** suite green, zero behavior change; tests assert every behavior
  site survives a *renamed* instance and that a `chat-completions` instance does
  **not** get the `openai` section/cache.
- **Phase 1b — config-driven instances.** `internal/providerconfig` loader +
  `providers.toml` + `NewFromProviders` + the load-or-ephemeral-env helper
  (§4.10-4.11); per-instance OAuth incl. `AuthRecord.Validate` (§4.9); the openai
  apiStyle recipe + anthropic base-URL instances (§4.7-4.8); spawn/launch-check
  config plumbing (§4.12); standalone serf reads the shared store. **Acceptance:**
  custom endpoints and multiple instances of a type work end-to-end through the
  real launch path; absent `providers.toml` falls back to env.
- **Phase 2 — unified CRUD UI + picker behavior** (§4.13).

## 6. Error handling

- **Duplicate/invalid names:** unique, no `/`, **lowercased on create** (so the
  registration name matches `normalizeProviderName`'s lowercased lookup — finding
  #13); loader rejects violations.
- **Unknown type:** loader errors, naming instance + type.
- **Missing credential:** instance registers but reports "not configured"; launch
  gating surfaces it.
- **Default points at a removed instance:** fall back to first by sorted name, log.
- **Corrupt `providers.toml`:** fail loudly; do **not** fall back to env (which
  would silently change which model answers). (An *absent* file uses the ephemeral
  env fallback, §4.10 — that's the intended bootstrap, not an error.)
- **Unknown instance / retired `gemini/` prefix:** clear error listing instances.
- **Vanished persisted instance (resume):** clear error + re-selection (§4.5).

## 7. Testing strategy

- **Behavior-tag separation (1a):** for an `openai`-type instance named `work`,
  assert prompt-cache, the `tools.provider-openai_append.md` section, cheap model,
  fallback guard, Codex/minimax gate, **and the compat catalog context-window
  lookup** all key on the tag; a `chat-completions` instance gets none of the
  `openai`-tag behavior. Two same-tag instances aren't a cross-provider fallback.
- **Gemini:** a `google`-type instance named `google` routes (adapter registered
  under `google`); `CheapModel`/sections fire on tag `google`; no `gemini` alias.
- **Identity:** a response and an **error** from instance `work` report `work`
  (error via the central client-boundary rewrite), on streaming, non-streaming,
  and vision paths.
- **No switching:** `WithModel` on an openrouter instance keeps
  `openrouter/anthropic/…` verbatim and never yields a different instance; a
  cross-provider `model_fallbacks` entry still errors (`session.go:4140`).
- **Selection + resume:** the selector resolves a custom instance and rejects
  unknowns; resume reconstructs a custom instance's ref; a removed instance errors.
- **Picker/launch filters:** `NameToTag`-keyed filters still skip
  `openrouter-anthropic` and hide non-tool openrouter models for a *renamed*
  openrouter instance.
- **Storage:** parse/round-trip `providers.toml`; absent file → ephemeral env
  config (no file written); corrupt file → loud failure (no env fallback).
- **OAuth per instance:** `auth/<name>.json` round-trips; default `openai` keeps
  `openai.json`; `AuthRecord.Validate` accepts a non-`openai`-named openai-tag
  instance; `validateProviderCredentials` resolves it.
- **Consumers + spawn:** standalone `serf run`/`serve`, both launch-check paths,
  and the web picker build via the load-or-env helper and see custom instances;
  `ToEnv` sets `SERF_PROVIDERS_CONFIG`.
- **Phase 2:** RPC round-trip + JSDOM for the management screen and pickers.

## 8. v4-review findings → v5 resolutions

- **A#1 selector import cycle** → §4.6 drops in-profile switching, so no selector
  is injected into profiles; §4.5 is the sole selector, living in `cmdutil`/
  `agent`. Dissolved.
- **A#2 tag unreachable at client sites** → §4.1 leaf package exposes
  `NameToTag`; §4.2/§4.11/§4.13 read it at those sites.
- **B#3 meta-provider name collision** → §4.6 no instance-name switching;
  meta-provider prefixes stay "keep". Dissolved.
- **B#4 catalog keyed on id** → §4.2 catalog lookup keys on the behavior tag
  (`profile.go:955/964`); §4.4 threads the tag to the recipe.
- **C#5 env-per-process determinism, C#6 no lock, C#7 openrouter dual-route** →
  §4.10 no migration (flag day). All dissolved.
- **#8 gemini id literal** → §4.2 gemini note: recipe sets `id=instanceName`,
  registers the google adapter under it; `CheapModel` `case "google"`.
- **#9 error labels (~20)** → §4.3 central client-boundary rewrite via
  `RewriteErrorProvider(err, req.Provider)`.
- **#10 launchProviderAllowsUnreportedModels** → §4.2 re-keyed via `NameToTag`.
- **#11 auth surface + OAuth keying contradiction** → §4.9 OAuth keyed by
  instance name throughout (no "by tag" variant); enumerates `serf-tui/auth.go`,
  `spawn.go validateProviderCredentials`, and the 5 maps replaced by the instance
  set.
- **#12 resp.Provider chokepoint** → §4.3 names both functions; resp.Provider is
  low-value but set correctly; error label is the real fix.
- **#13 mixed-case names** → §6 lowercase instance names on create.
- **#14 diagnostic provider list** → bare-string classification keeps the
  type-literal list (structured `llm.Error` fast-path carries the instance name);
  noted as an accepted minor in §9.

**Verified-resolved v3 findings carry forward** (both v4 reviewers confirmed):
main-request site, system-prompt coupling, resume-by-lookup, `normalizeProviderName`
trim/lower, per-instance OAuth threading, the every-spawn launch-check, the
OAuth-is-global correction, finish-norm needs no change.

## 9. Risks / open questions

- **Behavior-tag breadth (§4.2):** the inventory is now large but bounded; the 1a
  renamed-instance test matrix is the completeness guard. A missed site silently
  regresses a renamed instance — the recurring failure mode across reviews.
- **Ephemeral env fallback (§4.10):** a deliberate reversal of v4's "remove env
  entirely," chosen to keep zero-config dev working without migration. If the file
  exists it is authoritative; env matters only when the file is absent.
- **`internal/diagnostic` bare-string path (#14):** custom-instance error strings
  reaching `Classify` without a structured `llm.Error` would misclassify; the
  structured fast-path (`FromError`) mitigates. Accepted minor.
- **`chat-completions` feature gap (§4.7)** and **Anthropic-compatible protocol
  variance (§4.8)** are accepted Phase 1 limitations.
- **Scope:** Phase 1 is multi-week even sub-phased; 1a is an independently
  testable, behavior-preserving landing.
