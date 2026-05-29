# Provider Type/Instance Model

Date: 2026-05-29
Status: v6 — switching moved to the session, wiring pinned; pending re-review
Ticket: PRI-1880

> **Revision history.**
> - **v1** keyed routing on adapter `Name()`. Wrong.
> - **v2** corrected routing; missed profile.go id-branches, `resp.Provider`, the
>   launch-check subprocess.
> - **v3** fixed those; missed system-prompt sections, the resume allowlist,
>   catalog aliases, error labels, picker branches; migration holes.
> - **v4** added the behavior tag + unified storage; review confirmed all v3
>   findings resolved but found 3 clusters (layering, model-prefix, migration).
> - **v5** dropped `WithModel` switching and migration (flag day). Review showed
>   **dropping switching was wrong** — switching is load-bearing for interactive
>   `/model`, subagent overrides, and the cross-provider-fallback guard.
> - **v6** (this): **switching moves out of `WithModel` and up to the session**
>   (faithful to "drop *in-profile* switching"), via a plain
>   `agent.ResolveProfileFromConfig` + the session holding the config (cycle-free,
>   verified). Plus all v5 wiring fixes: streamed-error identity, profile-carried
>   tag (no `req.BehaviorTag`), synthesized env config, quirks-by-type, the
>   `classify.go` endpoint-fallback site, the pinned config path, catalog
>   ingest-vs-lookup, the resume signature.

## 1. Goal

Turn serf's hardcoded provider registry into a **type/instance** model: provider
*types* that users *instantiate* into named, multi-instance, configured providers
— including arbitrary OpenAI-compatible and Anthropic-compatible endpoints and
multiple instances of one vendor. Replace the two duplicative web settings
screens with one CRUD screen. Unify credential storage so the hub and standalone
`serf` read the same provider config.

## 2. Non-Goals

- **New wire protocols.** No endpoint whose protocol no adapter speaks.
- **Per-instance protocol overrides** beyond `apiStyle` (auth scheme, version
  header, arbitrary quirks) — deferred.
- **Encryption at rest** (keys verbatim, `chmod 600`).
- **In-profile provider switching.** `WithModel` no longer switches providers;
  cross-instance switching moves to the session via the selector (§4.5-4.6).
  Switching itself is still supported (it must be — interactive `/model`,
  subagents, fallbacks all use it).
- **Migration / backward compatibility.** Zero deployed instances → **flag day**:
  old `credentials.toml` / env / `openai.json` are not converted (§4.10).
- **Model-prefix aliasing.** Instances route by name only (no `gemini` alias).

## 3. Background — the real architecture

Verified line-by-line (`file:line`). The authoritative map the design re-keys.

### 3.1 Routing, request construction, model changes

`req.Provider = s.profile.ID()`. Primary request `session.go:2662`; vision
side-channel `describeImage` `:1460` (`Complete` `:1492`); fallback `:2709`.
Prompt data `:3873`/`:3972`. **There are 13+ other request-construction sites**
setting `Provider: profile.ID()` and calling `client.Complete` directly, several
**without** `applyModelRequestMetadata`: `context_manager.go:947`,
`fork_summarize.go:21`, `eval_probes.go:67,81`, `session_namer.go:56`, the
`strategy_*.go` files, `tool_web_fetch.go:124`, `tool_web_search.go:20`,
`diagnostics.go:96`.

**Interactive model change:** picker lists models across all providers
(`web.go:2031-2073`) → `POST /model` / `thread/model/set` → `local_daemon.go:201`
(no respawn) → `serve.go:292 SetModelFunc` → `Session.SetModel` (`session.go:1271`)
→ **`s.profile.WithModel(model)`** (`:1277`). **Subagents:**
`subagents.go:159,163 s.profile.WithModel(agent.Model)` with free-form
`provider/model` frontmatter (`plugin_agents.go:68`). **Fallbacks:**
`s.profile.WithModel(fbModel)` (`:2706`) then the guard
`fbProfile.ID() != s.profile.ID()` (`:4140`) — which **trips only because
`WithModel` switches the ID**. So `WithModel` cross-provider switching is
load-bearing for all three.

`llm.Client.Complete/Stream` → `c.providers[normalizeProviderName(req.Provider)]`
(`client.go:90`); register under `Name()` (`:39`); default = first
non-`NonDefaultEligible` (`:41`). `normalizeProviderName` (`:236`, called
`:91,122,207,224`) lowercases, trims, **+ gemini→google**.

### 3.2 Profile selection, catalog, quirks

`cmdutil.SelectProfile` (`cmdutil.go:57-73`) hardcoded switch; `google`/`gemini`
→ `NewGeminiProfile` (stamps `id:"gemini"`, `profile.go:699`) while the google
adapter `Name()` is `"google"` (`google/adapter.go:63`). `queryModelContextWindow`
(`cmdutil.go:69,266-292`) reads `KIMI_*`/etc env at selection. Compat catalog
lookup keys on `id`: `resolveOpenAICompatCatalogModel(get, id, model)` →
`id+"/"+model` then bare (`profile.go:954-966`); `suppressBareCatalogLookup(id)`
= `id=="ollama"` (`:929`). `QuirksPreset(name)` switches on literals
(`openaicompat:49-77`), called with hardcoded presets by the wrapper adapters
(`kimi:45`, `glm:45`, `openrouter:52`), each `Name()` hardcoded.

### 3.3 Everything keyed on the provider/id string

**`agent/profile.go` (id value):** `CheapModel switch p.id` incl `case "gemini"`
(`:344-355`); `decidePrefixAction(id,prefix)` outer `switch id` (`:397`) + inner
`switch prefix` (`:405-408`); `rebuildOnSameProviderChange(id)` (`:498-500`);
`WithModel` (`:519,533,562-568`); `anthropicProfile.WithModel` (`:649`);
`id=="ollama"` (`:930`); catalog prefix (`:955,964`); `id=="openrouter" &&
minimax/` (`:1007`).

**`agent/session.go`:** prompt-cache `req.Provider=="openai"` (`:1382`, via
`applyModelRequestMetadata` `:2685` on the 2662 request); gemini
`profile.ID()=="gemini"` (`:4785`); fallback guard (`:4140`); prompt sections
`:3873`/`:3972` (`"%s.provider-%s"`, `section_resolver.go:79-84`; real file
`tools.provider-openai_append.md`).

**`llm/`:** `normalizeProviderName` gemini→google (`client.go:236`);
`model_catalog.go:67` (lookup alias) + `normalizeCatalogProvider` (`:243-249`,
**ingest** normalization — distinct); `isEndpointFallbackSignal` gates the
Responses→chat fallback on `EqualFold(Provider(),"openai")` (`classify.go:114-117`
→ `:88-91` → `session.go:4148`). `normalizeFinish` (`types.go:259`) switches on
its arg but adapters pass **static literals** (`google:514`, `openaicompat:359`
`""`, `anthropic:642`) — **no change**. `ProviderOptions`: recipe sets
`providerOpts[<key>]`, adapter reads the same literal (`openai:296`; `google:217,
222` both `"google"`+`"gemini"`; `anthropic:286`; `openaicompat:535` — note
kimi/glm/openrouter use the **`"openai-compatible"`** key, not their own tag) —
**no per-instance change** (the key is the recipe↔adapter contract).

**Identity:** `resp.Provider` hardcoded by adapters; `session.go:3511` fills only
when empty (dead); near-zero readers (only `llmcall`; API log uses `req.Provider`).
**Error labels are the real surface:** ~20 hardcoded literals in
`WrapContextError`/`ErrorFromHTTPStatus`/`NewStreamError`/
`NewUnsupportedToolChoiceError`. Streamed errors are emitted into the event
channel (`StreamEvent{Type:StreamEventError}`: `anthropic:775`, `openai:911,1175`,
`openaicompat:515`, `google:559`) and surface at `session.go:3493-3497`,
**bypassing** the `Stream()` return value. `RewriteErrorProvider` (`errors.go:115`)
**deliberately no-ops on empty Provider** (so `context.Canceled`/no-object errors
aren't mislabeled) and is used only by ollama.

### 3.4 Launch gate, picker, resume

Launch gate is a **subprocess**: `validateSerfLaunchContract` runs `launch-check
--model` (no `--models`) on **every spawn** (`spawn.go:558-564`, `:141,186`),
`--models` for the picker (`:607`). Inside: `SelectProfile` (`launch_check.go:68`)
+ `NewFromEnv` (`:94,159`). Type-literal branches over `client.ProviderNames()`:
`launch_check.go:104` (skip `openrouter-anthropic`), `:222` (openrouter tools
filter); `web.go:2038,2064` (`:2064` hides non-tool openrouter models), client via
`NewFromEnv` (`:2024`). `launchProviderAllowsUnreportedModels =
EqualFold(provider,"openrouter-anthropic")` (`app_rpc.go:1526`, `:1518`). Resume:
`resumeProviderFromProfileID` allowlist (`app_rpc.go:1735`), consumed by
`resumeRequestForConfig` which returns a **value, no error** (`:1717-1729`).

### 3.5 Credentials, OAuth, config path

API keys fragmented: hub uses `cfg.CredsStore` (loaded from `hubStateRoot`,
default `~/.serf` — `config.go:51 DefaultHubStateRoot`, `app_rpc.go:103`);
standalone `serf` reads **only env** (`serve.go:39`/`run.go:127` → `NewFromEnv`).
(A pre-existing inconsistency: the `newHubAuthController` fallback computes a
*different*, XDG-derived `filepath.Dir(openAIStateDir)/credentials.toml` at
`app_auth.go:57` — the unified loader supersedes it.) FIVE type→env/mode maps:
`credentials/store.go:38,60`, `launchconfig/env.go:30`, `app_auth.go:133`,
`cmdutil.go:266`. `openai`-literal auth branches: `app_auth.go:108,151,191,249,
292,430,461`, `serf-tui/auth.go:26,27,80`, `spawn.go:466`, `openai_login.go:215`,
and `AuthRecord.Validate()` rejects `Provider != "openai"` (`storage.go:142-143`).

**OAuth is machine-global** at `$XDG_STATE_HOME/serf/auth/openai.json`:
`serf openai login`'s `resolveOpenAIStateDir` ignores workDir →
`DefaultStateDir()` (`openai_login.go:216-220`); the adapter reads it via
`cfg.StateHome ← XDG_STATE_HOME` (`openai/adapter.go:80`, `env_registry.go:52`).
The per-project `RuntimeDir` (`runtime_dir.go:18`) governs transcripts only.

There is a real registered `openai-compatible` adapter (`openaicompat:122`, env
factory `:90-110`) but `SelectProfile` has **no** case for it (already
unselectable, `cmdutil.go:72`). `NewFromEnv` consumers: `serve.go:39`,
`run.go:127`, `web.go:2024`, `launch_check.go:94,159`, `llmcall:220`,
`serfeval:196`, `generate.go:158 DefaultClient()`.

## 4. Design

### 4.1 Three concepts + the leaf package (cycle-free layering)

1. **Instance name** — routing key. Unique, no `/`, **lowercased on create**
   (§6). Equals `profile.ID()` = adapter registration name = `req.Provider`.
2. **Type (+ `apiStyle` for openai)** — user-facing config selecting the recipe.
   Types: `openai`, `anthropic`, `google`, `openrouter`, `openrouter-anthropic`,
   `kimi`, `glm`, `minimax`, `ollama`. Generic `openai-compatible` folds into
   `openai` via `apiStyle=chat-completions` (§4.7).
3. **Behavior tag** — the identity **every provider-conditional behavior keys
   on**. `BehaviorTag(type, apiStyle)`: openai+responses→`openai`;
   openai+chat-completions→`openai-compatible`; otherwise == type.

**Package layering** (verified: `llm` imports **zero** `serf` packages, so no
cycle):
- **`internal/providerconfig`** (leaf): `Config`, `InstanceConfig {name, type,
  apiStyle, baseURL, credential}`, the loader, `BehaviorTag(type,apiStyle)`,
  `NameToTag(cfg)`. No `llm`/`agent` deps.
- **`agent`**: profile recipes (existing constructors), the behavior tag on the
  profile (`BehaviorTag()`), and **`ResolveProfileFromConfig(cfg, ref)
  (ProviderProfile, error)`** — the single instance→profile resolver (agent holds
  the constructors and imports `llm` for the catalog + `providerconfig`).
- **`llm`**: `NewFromProviders(cfg)` builds per-instance adapters; `Client` holds
  `NameToTag` (from cfg) to key llm-layer logic.
- **`cmdutil`**: `SelectProfile` becomes a thin wrapper over
  `agent.ResolveProfileFromConfig` + the schema/decisions wrappers (for the CLI).
- **`cmd/serf`, `cmd/serf-hub`**: load the config once, call `NewFromProviders`,
  and pass the `Config` into `SessionConfig` so the session can re-resolve
  (§4.5). No closure injection; no `agent→cmdutil` cycle.

### 4.2 Behavior tag is the single behavior key — complete inventory

The behavior tag lives on the **profile** (`BehaviorTag()`); session-level checks
use `s.profile.BehaviorTag()`. There is **no `req.BehaviorTag` field** (it would
need stamping at 13+ request sites, §3.1). Where llm-layer logic needs the tag
(only `classify.go`, below), the **`Client` derives it from `req.Provider`/the
error's provider via `NameToTag`**.

| Site | Today | After |
|---|---|---|
| `session.go:1382` prompt-cache | `req.Provider == "openai"` | `s.profile.BehaviorTag() == "openai"` |
| `session.go:3873/3972` prompt sections | `= profile.ID()` | `= profile.BehaviorTag()` |
| `session.go:4785` gemini | `profile.ID() == "gemini"` | `profile.BehaviorTag() == "google"` |
| `session.go:4140` fallback guard | `fbProfile.ID() != profile.ID()` | `fbProfile.BehaviorTag() != profile.BehaviorTag()` (same-tag cross-instance fallback now allowed; cross-tag still errors) |
| `profile.go:344` CheapModel | `switch p.id` incl `"gemini"` | `switch p.behaviorTag`, `case "google"` |
| `profile.go:396` decidePrefixAction | id+prefix literals | takes **both** instance name (for self-prefix **strip**) and behavior tag (for meta-provider **keep**); **no switch** (§4.6) |
| `profile.go:498` rebuildOnSameProviderChange | id literals | `behaviorTag` |
| `profile.go:519/533/562` WithModel | `p.id`, switch-via-table | `p.behaviorTag`; **switch removed**, but **keep the same-instance catalog rebuild** at `:562` (recomputes context window/effort — removing it regresses to a stale window) |
| `profile.go:649` anthropic WithModel | literals | `behaviorTag` |
| `profile.go:699` NewGeminiProfile | `id: "gemini"` | `id = instanceName`; default Gemini instance named `google`, adapter registered under it |
| `profile.go:930` suppress-bare | `id == "ollama"` | `behaviorTag == "ollama"` |
| `profile.go:955/964` catalog lookup | `id + "/" + model` | `behaviorTag + "/" + model` |
| `profile.go:1007` Codex gate | `id == "openrouter" && minimax/` | `behaviorTag == "openrouter" && minimax/` |
| `client.go:236` normalizeProviderName | lowercase/trim **+ gemini→google** | lowercase/trim **only** |
| `model_catalog.go:67` lookup alias | gemini→google | **drop** (keep `:243 normalizeCatalogProvider` — ingest normalization that stores Gemini data under `google`) |
| `classify.go:114` endpoint fallback | `EqualFold(Provider(),"openai")` | behavior tag `openai` (via the tag stamped on the error, §4.3) |
| `launch_check.go:104/222`, `web.go:2038/2064`, `app_rpc.go:1526` | `provider == "openrouter[-anthropic]"` | `NameToTag[instance]` (config-derived, §4.1) |

**No change:** finish normalization (in-adapter static literal); `ProviderOptions`
keys (recipe↔adapter contract — for kimi/glm/openrouter the key stays
`"openai-compatible"`, **not** the behavior tag; do not re-key it).

### 4.3 Identity = instance name (both error paths)

`resp.Provider` and error labels carry the **instance name**.
- `resp.Provider`: set `= req.Provider` after the non-streaming `Complete` arm
  (`session.go:3358`) and inside `consumeModelStream` (`:3511`), and the vision
  path (`:1492`). Low value but cheap.
- **Error labels (the real surface), both paths:** stamp the provider centrally
  — (a) wrap the synchronous `Complete`/`Stream` returns in `llm.Client`, and (b)
  stamp streamed errors where the session consumes them (`session.go:3493`, or by
  wrapping the stream's `Events()`). Use `RewriteErrorProvider(err, req.Provider)`
  but **keep its empty-Provider no-op** (so cancellations / no-object errors stay
  unlabeled — removing it mislabels `context.Canceled`). Also stamp the
  **behavior tag** on the error here (from `NameToTag`) so `classify.go` (§4.2)
  can key on it.

### 4.4 Instance-settable profile id, adapter name, behavior tag; quirks by type

Profile constructors take an explicit instance-name parameter (or a
`WithProviderID` wrapper); each recipe stamps the behavior tag. **Adapters are
built per instance by `adapterRecipe(type, instanceName, cfg)`** — the default
base URL, quirks preset, and wrapper label come from the **type** (not the
instance name; a renamed `kimi` instance must still get kimi's `QuirksPreset` and
base URL, §3.2); `Name()` returns the instance name; the instance's `cfg` base
URL overrides the type default. `WithModel` preserves the instance name + tag (it
never switches, §4.6).

### 4.5 Instance-aware resolution; switching at the session; resume

`agent.ResolveProfileFromConfig(cfg, ref)` is the **single** instance→profile
resolver (§4.1): parse `ref`; the first segment is the instance name; look it up
in `cfg` → build the type's profile via its recipe with `id=instanceName`, the
instance's base URL/apiStyle, stamping the behavior tag; context window from the
instance config / catalog (not env). Unknown names error, listing instances.
Used by the daemon, the CLI (`cmdutil.SelectProfile` wraps it), and the
`launch-check` subprocess (config via env, §4.12).

**Switching lives at the session, not in the profile.** The session holds `cfg`
(via `SessionConfig`). `Session.SetModel(ref)`: if `ref`'s first segment names a
**different configured instance** → `ResolveProfileFromConfig(s.cfg, ref)` and
swap `s.profile` (all instances' adapters are already registered by
`NewFromProviders`, so routing works); else → `s.profile.WithModel(ref)`
within-instance. Subagent overrides (`subagents.go:159,163`) and `model_fallbacks`
(`session.go:2706`) resolve the same way; the fallback guard compares the
resolved **behavior tags** (§4.2). This keeps cross-provider `/model`, subagents,
and fallbacks working while `WithModel` itself never switches.

**Resume:** delete the `resumeProviderFromProfileID` allowlist; `Meta.ProfileID`
is the instance name; reconstruct via `cfg` lookup. **`resumeRequestForConfig`
gains an error return** (`app_rpc.go:1717`); a vanished instance errors clearly
with re-selection.

### 4.6 Model addressing (no in-profile switching)

`instanceName/model`; `ParseModelRef` cuts the first slash; first segment resolves
**only** to a configured instance name (the `gemini` alias is gone). Bare models →
default instance. `WithModel(model)` changes the model **within the current
instance**: **strip** when the prefix equals the profile's own **instance name**
(self-prefix); **keep** when the **behavior tag** is a meta-provider
(`openrouter`, `openrouter-anthropic`, `minimax`) and the slash is an upstream
namespace; else verbatim — and still **rebuild for the catalog** when the model
changes (preserve `profile.go:562`'s within-instance rebuild so the context
window/effort track the new model). `WithModel` never switches providers; that is
the session's job (§4.5). This removes the duplicated constructor table
(`profile.go:533`) and the instance-name-vs-namespace ambiguity (a configured
instance name is matched explicitly at the session; meta-provider namespaces stay
"keep").

### 4.7 `openai` `apiStyle`

`apiStyle ∈ {responses (default), chat-completions}` selects recipe + tag:
`responses` → `NewOpenAIProfile` + `openai` adapter, tag `openai`,
`ProviderOptions["openai"]`; `chat-completions` → `NewOpenAICompatProfile` +
`openaicompat` adapter, tag `openai-compatible`,
`ProviderOptions["openai-compatible"]`. The existing standalone
`openai-compatible` env provider (`OPENAI_COMPATIBLE_*`) is folded in: the env
synthesis (§4.10) produces an `openai`-type, `chat-completions` instance named
`openai-compatible`. `chat-completions` is not feature-equivalent (no Codex
surface / reasoning `encrypted_content`) — documented in the UI.

### 4.8 `anthropic` instances

`anthropic` instances set a base URL (`NewAnthropicProfile` + `anthropic` adapter,
which already takes one), targeting Anthropic-API-compatible servers. Bearer auth
/ different version header / `cache_control`-rejecting endpoints out of scope for
Phase 1 (§2).

### 4.9 Per-instance OAuth (keyed by instance name)

OAuth is keyed by **instance name** end to end (no "by behavior tag" variant):
`AuthFilePath(stateHome, instanceName)` → `$XDG_STATE_HOME/serf/auth/
<instanceName>.json` (same global root; per-instance filename). Default `openai`
instance keeps `auth/openai.json`. Thread instance name + `cfg.StateHome` through
`LoadAuth`/`SaveAuth`/`DeleteAuth`, the `Service`/`ResolveRuntimeCredentials`, the
adapter recipe (`openai/adapter.go:80`), the hub auth controller
(`app_auth.go`'s `openai` branches), the TUI (`serf-tui/auth.go:26,27,80` —
`authStatus` gains the tag), `validateProviderCredentials` (`spawn.go:466`), and
**`cmd/serf/openai_login.go`** (`resolveOpenAIStateDir` gains an instance-name
param). **`AuthRecord.Validate()`** validates against behavior tag `openai`
instead of hardcoding `Provider=="openai"`. OAuth offered only for tag `openai`.

### 4.10 Storage — pinned path, ephemeral env, flag day

- **`$hubStateRoot/providers.toml`** (default **`~/.serf/providers.toml`**,
  `config.go:51`; `chmod 600`), read by **both** the hub and standalone `serf`
  (standalone resolves `hubStateRoot` the same way). OAuth stays under
  `$XDG_STATE_HOME/serf` (§3.5) — separate root, unchanged. Format: `schema`,
  `default` (instance name), `[instances.<name>]` with `type`, `base_url`,
  `api_style`, inline `api_key` (omitted for OAuth/none).
- **No migration (flag day).** Old `credentials.toml`/env/`openai.json` are not
  converted (zero deployed instances).
- **Ephemeral env fallback = a synthesized real config.** When `providers.toml` is
  absent, build an in-memory **`providerconfig.Config`** from env (default
  instances named == behavior tag: `openai`, `anthropic`, …; `OPENAI_COMPATIBLE_*`
  → an `openai`/chat-completions instance named `openai-compatible`) and go
  through `NewFromProviders`. So `NameToTag` and the selector exist in **both**
  paths; nothing is written. Zero-config `OPENAI_API_KEY=… serf run` still works.
  The file, when present, is authoritative (env is not a layer).
- **Default:** the file's `default` field; for the env case, the first eligible
  instance (today's behavior).

### 4.11 Client / daemon / hub / standalone construction

One construction path: **load `providerconfig.Config`** (file at the pinned path,
else synthesized from env, §4.10) → `llm.NewFromProviders(cfg)`; pass `cfg` into
`SessionConfig` and `WebConfig`. Converted consumers: `serve.go:39`,
`run.go:127`, `web.go:2024`, `launch_check.go:94` **and** `:159`,
`DefaultClient()`; `llmcall`/`serfeval` adopt the same helper. The `Client` holds
`NameToTag`; the client-only picker/launch sites read it (§4.2). Launch
gating/credential validation read the **instance set** (not the five type maps);
`validateProviderCredentials` resolves the instance named by `req.Provider` and
checks its credential (inline key, or OAuth by instance name §4.9).

### 4.12 Spawn plumbing

The hub threads `SERF_PROVIDERS_CONFIG=<path>` into `req.Env` via
`launchconfig.ToEnv`, reaching **both** spawned `serf serve` and the `serf
launch-check` subprocess (validate + `--models` paths). The load helper (§4.11)
consults `SERF_PROVIDERS_CONFIG` first, then the default path, then env synthesis
— so the spawned daemon uses exactly the hub's config. The single-provider env
injection is removed.

### 4.13 Unified UI (Phase 2)

One screen replacing Providers + Credentials: instances grouped by type, each
showing credential source/state + default marker; create (type → name → base URL
/ apiStyle / credential), edit, remove, set-default; per-instance credentials (key
set/clear; OAuth sign-in via the PRI-1878 device-code flow for tag `openai`). RPCs
operate on the instance config. Pickers route/display by instance name; the
behavior filters (`web.go:2064` hides non-tool openrouter models;
`launch_check.go:104/222`; `app_rpc.go:1526`) re-key via `NameToTag` — behavior,
not just display, so it lands with the picker work.

## 5. Phasing

- **Phase 1a — behavior-tag separation + switching-to-session.** Add
  `BehaviorTag()`; `internal/providerconfig` (tag fn); re-key every §4.2 site
  (gemini-id, catalog-by-tag, `classify.go`, picker filters via `NameToTag`,
  `app_rpc.go:1526`); central error identity on **both** paths (§4.3);
  instance-settable id/name/tag + quirks-by-type (§4.4); **move switching to the
  session** (`SetModel`/subagents/fallbacks via `ResolveProfileFromConfig`,
  §4.5-4.6); resume via lookup with an error return. Proven with existing
  providers (instance == type). **Acceptance:** suite green; user-visible
  cross-provider `/model` still switches (now via the session); new
  renamed-instance tests assert every behavior site keys on the tag and a
  `chat-completions` instance gets none of the `openai`-tag behavior.
  **Note:** the `WithModel` cross-provider unit tests (`profile_test.go:366,379`)
  are **rewritten** to test session-level switching — `WithModel` no longer
  switches by design; this is the one intended behavior-location change in 1a.
- **Phase 1b — config-driven instances.** `providerconfig` loader +
  `providers.toml` + `NewFromProviders` + the load-or-synthesize helper
  (§4.10-4.12); per-instance OAuth incl. `AuthRecord.Validate` + `openai_login`
  (§4.9); the openai apiStyle recipe + anthropic base-URL instances + the
  `openai-compatible` fold-in (§4.7-4.8); spawn/launch-check config plumbing.
  **Acceptance:** custom endpoints and multiple instances of a type work
  end-to-end; absent file → env synthesis.
- **Phase 2 — unified CRUD UI + picker behavior** (§4.13).

## 6. Error handling

- **Duplicate/invalid names:** unique, no `/`, **lowercased on create** (matches
  `normalizeProviderName`'s lowercased lookup); loader rejects.
- **Unknown type / unknown instance / retired `gemini/`:** clear error listing
  instances.
- **Missing credential:** registers but "not configured"; launch gating surfaces.
- **Default points at a removed instance:** first by sorted name, log.
- **Corrupt `providers.toml`:** fail loudly; no env fallback (an *absent* file
  uses env synthesis, §4.10).
- **Vanished persisted instance (resume):** clear error + re-selection (§4.5).

## 7. Testing strategy

- **Behavior-tag + switching (1a):** for an `openai`-type instance named `work`,
  assert prompt-cache, the `tools.provider-openai_append.md` section, cheap model,
  fallback guard, Codex gate, **and the compat catalog context-window lookup** key
  on the tag; a `chat-completions` instance gets none of the `openai`-tag
  behavior. `SetModel("other-instance/model")` switches via the session;
  `WithModel` never changes the instance; a bare `/model` rebuilds the catalog
  window; cross-tag `model_fallbacks` errors, same-tag cross-instance is allowed.
- **Gemini:** `google`-named instance routes (adapter under `google`); sections/
  cheap-model on tag `google`; `ListModels("google")` still returns Gemini data
  (ingest normalization kept).
- **Identity:** a streamed **and** non-streamed error from instance `work`
  reports `work`; `context.Canceled` stays unlabeled.
- **Selection/resume:** resolver builds a custom instance, rejects unknowns;
  resume reconstructs a custom instance; removed instance errors.
- **Quirks:** a renamed `kimi` instance still gets kimi's `QuirksPreset` + base
  URL (recipe keys on type).
- **Storage:** parse/round-trip `providers.toml` at the pinned path; absent →
  synthesized env config (incl. `OPENAI_COMPATIBLE_*` → `openai-compatible`
  instance), no file written; corrupt → loud failure.
- **OAuth per instance:** `auth/<name>.json` round-trips; default `openai` keeps
  `openai.json`; `AuthRecord.Validate` accepts a non-`openai`-named openai-tag
  instance; `openai_login`/`validateProviderCredentials` resolve it.
- **Consumers/spawn:** standalone `run`/`serve`, both launch-check paths, web
  picker build via the helper and see custom instances; `ToEnv` sets
  `SERF_PROVIDERS_CONFIG`; the daemon uses it.
- **Phase 2:** RPC round-trip + JSDOM for the screen + pickers.

## 8. v5-review findings → v6 resolutions

- **Switching (v5 #1-4)** → §4.5-4.6 move switching to the session
  (`ResolveProfileFromConfig` + `SetModel`); `WithModel` within-instance only; the
  fallback guard compares tags; `profile_test.go:366/379` rewritten (§5).
- **#5 streamed errors** → §4.3 stamps both the return boundary and the stream
  consumption site.
- **#6 empty-Provider no-op** → §4.3 keeps it.
- **#7 13-site req.BehaviorTag** → §4.2 tag on the profile + `NameToTag` on the
  client; **no `req.BehaviorTag` field**.
- **#8 classify.go** → §4.2 keyed via the tag stamped on the error (§4.3).
- **#9 env-fallback has no config** → §4.10 synthesizes a real `Config`.
- **#10 decidePrefixAction needs name+tag** → §4.2 takes both.
- **#11 config path** → §3.5/§4.10 pin `$hubStateRoot/providers.toml`
  (`~/.serf` default); note the `app_auth.go:57` inconsistency superseded.
- **#12 quirks by type** → §4.4 `adapterRecipe(type, …)`.
- **#13 openai-compatible adapter** → §4.7/§4.10 fold `OPENAI_COMPATIBLE_*` into a
  synthesized `openai`/chat-completions instance.
- **#14 resume signature** → §4.5 `resumeRequestForConfig` returns an error.
- **#15 openai_login** → §4.9 included.
- **#16 catalog ingest vs lookup** → §4.2 keep `:243`, drop `:67` + `client.go:236`.

**Carried forward (verified):** leaf package is cycle-free (`llm` imports no serf
packages); finish-norm needs no change; catalog-by-tag works (type==tag for
kimi/glm/openrouter/ollama).

## 9. Risks / open questions

- **Inventory completeness:** five rounds have each found new *wiring* sites; the
  1a renamed-instance test matrix is the guard, but prose enumeration has been
  unreliable here — the compiler + tests during 1a are the real backstop.
- **Same-tag cross-instance fallback (§4.2):** the guard now allows it (safe —
  identical surface). Confirm no caller depended on the stricter same-id rule.
- **`config path` standalone resolution:** standalone serf must resolve
  `hubStateRoot` (default `~/.serf`) — a new path it doesn't read today; confirm
  the resolver matches the hub's exactly.
- **`chat-completions` feature gap (§4.7)** and **Anthropic-compatible variance
  (§4.8)** are accepted Phase 1 limitations.
- **Scope:** Phase 1 is multi-week even sub-phased; 1a is independently testable.
