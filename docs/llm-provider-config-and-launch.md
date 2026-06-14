# LLM Provider Configuration, Credentials & Launch Architecture

How serf stores provider credentials, signs you in to OpenAI, and how the
**hub** turns a launch request into a running model session. This documents the
**current** implementation (as of 2026-05, after Phase 1c all-config-driven /
hub materialization) so you can navigate it quickly.

Companion docs:

- [`llm-providers.md`](llm-providers.md) — provider *architecture*: how a
  `provider/model` string is routed to an adapter, profiles, wire protocols.
- [`ollama.md`](ollama.md) — the Ollama adapter and its env-var discovery.

The model to carry over from `llm-providers.md`: a provider has **two
identities** — the instance **name** (`req.Provider`/`profile.ID()`/the adapter
registration name) routes and identifies, while the behavior **tag**
(`profile.BehaviorTag()`) drives provider-conditional behavior. For the default
env-seeded instances they coincide (one instance per type, name == type), so the
credential lookup below is keyed by the type name; a `providers.toml` (Phase 1b+)
adds named/custom instances, each with its own credentials and — for the `openai`
tag — its own OAuth record. This doc is about *where the credentials live* and
*which process reads them*.

---

## Overview

There are two credential stores plus environment variables, and — with custom
instances — a `providers.toml`:

| Store | File | Format | Owner | Used for |
|-------|------|--------|-------|----------|
| Credentials store | `<hubStateRoot>/credentials.toml` (chmod 600) | TOML | `internal/credentials` | API keys, keyed by provider type |
| OpenAI OAuth record | `<stateDir>/auth/<instance>.json` (chmod 600) | JSON | `internal/auth/openai` | OpenAI ChatGPT/Codex OAuth tokens, per instance |
| Providers config | `<stateRoot>/providers.toml` | TOML | `internal/providerconfig` | descriptors-only instance list (type, apiStyle, base URL, quirks — never `api_key`) |
| Process environment | n/a | env vars | the OS | fallback / base URLs / tuning |

The **hub process never runs a model**. To validate or list models it spawns
`serf launch-check` as a short-lived subprocess; to run a session it spawns the
`serf serve` daemon. API keys reach those subprocesses **through the environment**
(one key per launch). The one config file that *does* cross the boundary is
`providers.toml`: when the hub has loaded one it passes the path as
`SERF_PROVIDERS_CONFIG` (`launchconfig.ToEnv:65`) and the child re-reads it; OAuth
records are likewise re-read from disk by the child (via `SERF_STATE_DIR`).

---

## Credentials store (`internal/credentials/store.go`)

`credentials.toml` holds one section per provider:

```toml
schema = 1
[providers.openai]
api_key = "sk-..."
[providers.anthropic]
api_key = "sk-ant-..."
```

- `LoadStore` (`store.go:89`) reads the file. A missing file yields an empty
  store (no error). A present file **must** be mode `0600` — group/world bits set
  cause a hard error (`store.go:98`). Writes go through a temp-file + rename with
  `0600` (`store.go:184`).
- **The fixed provider set is defined in code, not config.** Two hardcoded maps
  are the source of truth:
  - `providerEnvVars` (`store.go:37`) — provider → the env var(s) checked as a
    fallback (first non-empty wins).
  - `providerAuthModes` (`store.go:58`) — provider → supported auth flows
    (`apiKey` / `oauth` / `none`). `List()` (`store.go:163`) iterates *this* map,
    so it is effectively the definition of "what providers exist" to the hub.
- `Get(provider)` (`store.go:116`) resolves a key with lookup order **file → env
  → absent**, returning the value and a `Source` (`SourceFile` / `SourceEnv` /
  `SourceAbsent`).
- `Layers(provider)` (`store.go:131`) reports the file and env layers
  *independently* of priority, so the UI can show a stored key shadowed by (or
  shadowing) an env var.
- `Set` / `Clear` / `List` / `APIKeyFor` round out the API. `APIKeyFor`
  (`store.go:179`) is the `launchconfig.CredentialResolver` implementation the
  spawner uses.
- `ollama` is special: it maps to `nil` env vars and auth mode `{"none"}`;
  `List()` reports it as `SourceNone` (`store.go:167`).

The hub loads exactly one store at `filepath.Join(hubStateRoot, "credentials.toml")`
(`cmd/serf-hub/main.go:103`) and hands it to the spawner and the auth controller.

> Note: the UI copy and several code comments say keys live in
> `~/.serf/credentials.toml` (e.g. `cmd/serf-hub/templates/partials/credentials.html:9`,
> the package doc at `store.go:1`). That is the *documented default home*; the
> hub actually uses whatever `hubStateRoot` is configured to. The hub auth
> controller's fallback constructors derive the path as
> `filepath.Join(filepath.Dir(stateDir), "credentials.toml")`
> (`cmd/serf-hub/app_auth.go:57,88`).

---

## Environment-variable reference

Every variable below is read by name somewhere in the code. "Read by" cites the
file that calls `os.Getenv` (or the map that lists it).

### API keys

| Env var | Provider(s) | Read by |
|---------|-------------|---------|
| `OPENAI_API_KEY` | openai | `credentials/store.go:38`; `llm/providers/openai/adapter.go:123`; `auth/openai/service.go:364,386` |
| `ANTHROPIC_API_KEY` | anthropic | `credentials/store.go:39`; `launchconfig/env.go:32` |
| `GEMINI_API_KEY` | google, gemini | `credentials/store.go:40-41` (primary) |
| `GOOGLE_API_KEY` | google, gemini | `credentials/store.go:40-41` (fallback after `GEMINI_API_KEY`) |
| `MINIMAX_API_KEY` | minimax | `credentials/store.go:42` |
| `OPENROUTER_API_KEY` | openrouter, openrouter-anthropic | `credentials/store.go:43-44`; `cmdutil/cmdutil.go:273` |
| `KIMI_API_KEY` | kimi | `credentials/store.go:45`; `cmdutil/cmdutil.go:271` |
| `KIMI_CODING_API_KEY` | kimi-anthropic (Kimi coding plan) | `credentials/store.go`; `launchconfig/env.go` |
| `GLM_API_KEY` | glm | `credentials/store.go:46`; `cmdutil/cmdutil.go:272` |
| `OPENAI_COMPATIBLE_API_KEY` | openai-compatible | `credentials/store.go:47`; `llm/providers/openaicompat/adapter.go:107` |
| `OLLAMA_API_KEY` | ollama (optional) | `llm/providers/ollama/adapter.go:189` |

Note that `credentials.toml` only stores keys (no per-provider base URLs). Both
the credentials store and the `launchconfig` injector key off provider name, but
they are **separate maps**: `providerEnvVars` (`store.go:37`, supports the
`GOOGLE_API_KEY` fallback) vs. `providerEnvVar` (`launchconfig/env.go:30`, single
canonical var per provider, no `ollama` entry). The launchconfig map is what
actually gets injected into the spawned subprocess.

### Base URLs

| Env var | Effect | Read by |
|---------|--------|---------|
| `OPENAI_BASE_URL` | API-key OpenAI backend base (default `https://api.openai.com`) | `llm/providers/openai/adapter.go:125` |
| `OPENAI_CHATGPT_BASE_URL` | OAuth ChatGPT/Codex backend base (default `https://chatgpt.com`) | `llm/providers/openai/adapter.go:95` |
| `ANTHROPIC_BASE_URL` | Anthropic base override | `llm/providers/anthropic/adapter.go:44` |
| `GEMINI_BASE_URL` | Google/Gemini base override | `llm/providers/google/adapter.go:51` |
| `MINIMAX_BASE_URL` | MiniMax base override | `llm/providers/minimax/adapter.go:47` |
| `OPENROUTER_BASE_URL` | OpenRouter base override (default `https://openrouter.ai/api/v1`) | `cmdutil/cmdutil.go:273` |
| `KIMI_BASE_URL` | Kimi base override (default `https://api.moonshot.ai/v1`) | `cmdutil/cmdutil.go:271` |
| `KIMI_CODING_BASE_URL` | Kimi coding-plan base override (default `https://api.kimi.com/coding`, Anthropic-compatible) | `cmdutil/seed.go`; `llm/providers/kimi_anthropic/adapter.go` |
| `GLM_BASE_URL` | GLM base override (default `https://api.z.ai/api/paas/v4`) | `cmdutil/cmdutil.go:272` |
| `OPENAI_COMPATIBLE_BASE_URL` | **required** — its presence gates openai-compatible registration | `llm/providers/openaicompat/adapter.go:90,103` |
| `OLLAMA_BASE_URL` | Ollama base, used as-is (must include `/v1`) | `llm/providers/ollama/adapter.go:187` |
| `OLLAMA_HOST` | Ollama canonical host var, normalized to a `/v1` URL | `llm/providers/ollama/adapter.go:188` |

The Kimi/GLM/OpenRouter base URLs appear in two places: the adapters use them for
chat, and `cmdutil.providerEnvConfig` (`cmdutil/cmdutil.go:266`) also reads them
to query a provider's `/models` endpoint for context-window sizing
(`queryModelContextWindow`, `cmdutil.go:280`).

### Other tuning

| Env var | Effect | Read by |
|---------|--------|---------|
| `OPENAI_ORG_ID` | OpenAI org header (API-key path only) | `llm/providers/openai/adapter.go:133` |
| `OPENAI_PROJECT_ID` | OpenAI project header (API-key path only) | `llm/providers/openai/adapter.go:134` |
| `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` | selects a quirks preset for the openai-compatible adapter | `llm/providers/openaicompat/adapter.go:110` |

### Process-coordination vars (set by the hub, read by the subprocess)

These are injected by the spawner (see below) — not provider credentials:
`SERF_HUB_SPAWNED`, `SERF_RUN_DIR`, `SERF_STATE_DIR`, `SERF_HUB_TOKEN`
(`launchconfig/env.go:54-63`). `serf serve` reads `SERF_HUB_SPAWNED` /
`SERF_RUN_DIR` to label its rendezvous entry (`cmd/serf/serve.go:376-385`), and
`llm.NewFromEnv` reads `SERF_STATE_DIR` / `XDG_STATE_HOME` to locate the OpenAI
OAuth record (`llm/env_registry.go:51-52`).

---

## OpenAI OAuth (`internal/auth/openai/*`, `cmd/serf-hub/app_auth.go`)

OpenAI is the only provider with two backends, selected by *how* you are
authenticated:

- **OAuth → ChatGPT/Codex backend** (`OPENAI_CHATGPT_BASE_URL`, default
  `https://chatgpt.com`, Codex responses path) — `adapter.go:86-121`.
- **API key → standard OpenAI API** (`OPENAI_BASE_URL`, default
  `https://api.openai.com`, with org/project headers) — `adapter.go:123-138`.

### The OAuth record

- One JSON file **per instance**: `AuthFilePath(stateDir, instanceName)` →
  `<stateDir>/auth/<instanceName>.json` (`storage.go:42`), written `0600`. The
  default `openai` instance keeps `auth/openai.json`; a custom `openai`-tag
  instance named `work` uses `auth/work.json`.
- `AuthRecord.Validate` (`storage.go:140`) requires `version == 1` and non-empty
  source/access-token/refresh-token/token-type/expiry/obtained-at. The old
  `provider == "openai"` check was **dropped** in 1b so a record under a custom
  instance name validates — the behavior *tag*, not the record, decides
  openai-ness.
- The default state dir (when not overridden) is XDG-based:
  `$XDG_STATE_HOME/serf` or `~/.local/state/serf`. The hub overrides this from its
  own environment.

### Precedence (standalone `Service`, `service.go`)

`Service.ResolveRuntimeCredentials` (`service.go:378`) and `Service.Status`
(`service.go:350`) both implement: **stored OAuth record > `OPENAI_API_KEY`
env**. Crucially, if a record *exists but cannot be refreshed*, the service
surfaces a re-login error (`ErrLoginRequired`, `service.go:392,406,414`) rather
than silently falling back to the env key — an explicit sign-in wins. Tokens are
refreshed automatically with a 5-minute skew (`refreshSkew`, `service.go:16`;
`needsRefresh`, `service.go:493`).

`llm.NewFromEnv` for OpenAI mirrors this: it checks `Service.Status`, and if the
active source is OAuth it routes through the ChatGPT/Codex adapter; otherwise it
falls back to `OPENAI_API_KEY` (`llm/providers/openai/adapter.go:77-140`).

### Precedence (hub controller, `app_auth.go`)

The hub adds a third layer. `openAIStatus` (`app_auth.go:320`) resolves
**stored OAuth record > credentials.toml file key > `OPENAI_API_KEY` env**
(`app_auth.go:341-350`). The file layer exists because the hub can store an
OpenAI API key in `credentials.toml` like any other provider — the standalone
`Service` knows nothing about that file, only the hub controller does.

### How a user signs in

The hub auth controller (`hubAuthController`, `app_auth.go:19`) gates every OAuth
RPC on `provider == "openai"` and exposes:
`Status` / `List` / `Logout` / `LoginStart` / `LoginComplete` / `DeviceStart` /
`DevicePoll` / `ApiKeySet`.

The web Credentials screen drives this (see Web/TUI surfaces below):

1. **Device-code flow (primary).** `DeviceStart` (`app_auth.go:428`) requests a
   device code and returns a user code + verification URL. The browser polls
   `DevicePoll` (`app_auth.go:459`) until the user authorizes on a second device;
   on success the controller saves the OAuth record and returns updated status.
   Flows expire after 15 minutes (`app_auth.go:471`).
2. **Paste-back redirect (fallback).** If device-code isn't enabled
   (`ErrDeviceCodeNotEnabled`, `app_auth.go:435`), `DeviceStart` returns
   `Fallback: true`; the UI then calls `LoginStart` (`app_auth.go:149`) to get an
   authorize URL, the user authorizes in a browser, and pastes the full redirect
   URL back into `LoginComplete` (`app_auth.go:189`), which does the PKCE code
   exchange.

The CLI equivalent is `serf openai login` (`cmd/serf/openai_login.go:51`),
which auto-selects browser vs. device flow based on whether a graphical session
is detected (`SSH_CONNECTION`/`SSH_TTY`/`DISPLAY`/`WAYLAND_DISPLAY`, overridable
with `SERF_LOGIN_HEADLESS`); subcommands `login` / `logout` / `status`. The CLI
device flow also watches for a *concurrent* `serf openai login` writing fresh
state and exits gracefully if it sees one (`service.go:308-348`).

**Two homes, by design:** OpenAI API keys live in `credentials.toml`; OAuth
tokens live in `openai.json`. `Logout` for OpenAI clears the effective layer —
it deletes the OAuth record if present, otherwise clears the file key; the env
layer cannot be cleared (`app_auth.go:257-282`).

---

## Hub launch / spawn process model

This is the part most worth internalizing: **the hub orchestrates, separate
`serf` processes do the work.**

```
                        ┌──────────────────────────────────────────────┐
                        │  serf-hub process                            │
                        │  (cmd/serf-hub)                              │
                        │                                              │
   credentials.toml ───▶│  credentials.Store (main.go:103)            │
   openai.json     ───▶│  hubAuthController (app_auth.go)             │
   providers.toml  ─────▶  MaterializeProvidersConfig (main.go:120)   │
                        │  HubSpawner (spawn.go)                       │
                        └───────────────┬──────────────┬──────────────┘
                                        │              │
                  SERF_PROVIDERS_CONFIG │              │  SERF_PROVIDERS_CONFIG
                  + one API key         │              │  + one API key
                                        ▼              ▼
              ┌─────────────────────────────┐   ┌─────────────────────────────┐
              │ serf launch-check           │   │ serf serve  (the daemon)    │
              │ (subprocess, short-lived)   │   │ (subprocess, long-lived)    │
              │ cmd/serf/launch_check.go    │   │ cmd/serf/serve.go           │
              │                             │   │                             │
              │ cmdutil.LoadClient()        │   │ cmdutil.LoadClient()        │
              │ → ProviderNames / ListModels│   │ → runs the session          │
              │ validates one model         │   │ writes a rendezvous entry   │
              └─────────────────────────────┘   └──────────────┬──────────────┘
                                                                │
                                                  rendezvous file (run dir)
                                                  carries modelRef.Provider
                                                                │
                        ┌───────────────────────────────────────┘
                        ▼
                hub reads rendezvous for roster / resume
```

### Building the subprocess environment (`internal/launchconfig/env.go`)

`ToEnv` (`env.go:53`) produces the env slice for a spawned subprocess. Starting
from the parent env (`os.Environ()`), it sets, in increasing priority:

1. Process-coordination vars: `SERF_HUB_SPAWNED=1`, `SERF_RUN_DIR`,
   `SERF_STATE_DIR`, `SERF_HUB_TOKEN` (`env.go:55-63`).
2. `SERF_PROVIDERS_CONFIG`, when the hub loaded a `providers.toml`
   (`env.go:65-66`) — the only config file that crosses the boundary; the child
   re-reads it.
3. **Exactly one** provider API key — `providerEnvVar[in.Provider]` resolved via
   `Creds.APIKeyFor` (`env.go:70-74`). Only the launched provider's key is
   injected; nothing else from the credentials store crosses the boundary.
4. Per-launch env overrides from `Resolved.Effective.Env`, applied last
   (sorted, last-write-wins) so they win over everything (`env.go:78-85`).

OpenAI OAuth tokens are **not** in this list — the comment at `env.go:44-49`
notes the on-disk OAuth state is "handled by serf itself": the subprocess re-reads
the per-instance `auth/<instance>.json` via its client builder using the injected
`SERF_STATE_DIR`.

### Spawning `serf launch-check` (model discovery / validation)

`HubSpawner` builds env with `ToEnv`, then runs the checker as a subprocess:

- `listSerfLaunchModelContract` (`spawn.go:601`) runs
  `serf launch-check --protocol <v> --json --models` with `cmd.Env = env`
  (`spawn.go:607-608`) and decodes the JSON contract.
- `validateSerfLaunchContract` (`spawn.go:558`) runs the same binary with
  `--model <provider/model>` to validate a specific model (`spawn.go:562-570`).
- Both have a timeout (`serfLaunchCheckTimeout`) and redact secrets out of error
  output via `redactEnvSecrets` (`spawn.go:655`).

Inside the checker, `serf launch-check` (`cmd/serf/launch_check.go`) is
config-aware:

- **Profile validation is credential-free.** `validateLaunchCheckProfile`
  (`launch_check.go:119`) loads just the config (`launchCheckLoadConfig:35`, same
  `SERF_PROVIDERS_CONFIG`-else-default path resolution as `LoadClient`): it always
  resolves via `agent.ResolveProfileFromConfig` (`:125`) so **custom instance names
  are valid**. It needs no API keys, so the launch contract resolves even with no
  credentials present.
- **Model listing** (`launchCheckModels:132`) builds a live client via
  `launchCheckLoadClient` (`:28` → `cmdutil.LoadClient`, always config-driven),
  enumerates `client.ProviderNames()`, and filters by **behavior tag**
  (`client.BehaviorTagOf(provider)`, `:146`) — `openrouter-anthropic` is skipped,
  non-chat model IDs are dropped.
- `validateLaunchCheckModel` (`:198`) then confirms the requested model is
  actually offered.

### Spawning `serf serve` (the session daemon)

`HubSpawner.Spawn` (`spawn.go:110`) and `.Resume` (`spawn.go:154`):

1. resolve the state dir and build env via `ToEnv` (`spawn.go:129,172`),
2. run a **credential pre-check** `validateProviderCredentials`
   (`spawn.go:138,182` → defined `spawn.go:466`) — it short-circuits OK for a
   usable OpenAI OAuth record (`openAIStoredOAuthUsable`, `spawn.go:515`), for
   openai-compatible when `OPENAI_COMPATIBLE_BASE_URL` is set (`spawn.go:507`),
   and for `ollama` (no creds); otherwise it requires either a stored key or the
   matching env var or it fails the launch,
3. run `validateSerfLaunchContract` (the launch-check subprocess) against the
   model,
4. `SpawnDaemon` / `ResumeDaemon` exec `serf serve` with `cmd.Env = req.Env`
   (`spawn.go:276-277`), binding an ephemeral port (`--addr 127.0.0.1:0`,
   `spawn.go:247`), and wait for the daemon's rendezvous file to appear.

`serf serve` (`cmd/serf/serve.go`) builds its client with `cmdutil.LoadClient`
(via the `serveLoadClient` test hook, `serve.go:39`), resolves the model with
`cmdutil.ResolveModelRef` (`serve.go:155`) + `buildInitialProfile`
(`serve.go:171,440`) — which always uses `ResolveProfileFromConfig` (config is
always present after `LoadClient`) and re-applies the output-schema/decisions
overrides — and on startup writes a rendezvous entry carrying `modelRef.Provider`
(`serve.go:404`). The standalone `serf run` (`cmd/serf/run.go:127,138`) and the
hub's own live-models endpoint behind `/api/models` (`cmd/serf-hub/web.go:2027`)
likewise build via `cmdutil.LoadClient`, so all three see custom instances.

`cmdutil.LoadClient` (`cmdutil/load_client.go:31`) is **always** `NewFromProviders`
— it loads `providers.toml` when present, or seeds the config in memory from the
environment via `cmdutil.seedConfigFromEnv` (`cmdutil/materialize.go:18`) when
absent, then injects credentials via `credentials.Store.ResolveKey(name, typ)`
(`internal/credentials/store.go:184`) into the in-memory config and calls
`llm.NewFromProviders`. No disk write ever occurs in `LoadClient`; persisting
`providers.toml` is the hub's responsibility. The hub (`cmd/serf-hub/main.go:120–131`)
materializes the file on startup when absent via
`cmdutil.MaterializeProvidersConfig` (`cmdutil/materialize.go:54`) and passes the
path to spawned children via `SERF_PROVIDERS_CONFIG`. `llm.NewFromEnv` is retained
as the seed's detection input and for `launch_check.go`'s read-only validator, but
is no longer a runtime default. `ProviderNames()` (`llm/client.go:63`) returns
only the registered instances.

---

## Resume & persistence

- Session metadata persists `ProfileID`, which equals `profile.ID()` — i.e. the
  instance name (the type name by default, or a custom instance name when a
  `providers.toml` is in play). Written by `agent/snapshot.go` and
  `agent/transcript.go`.
- On **hub resume**, `resumeRequestForConfig` (`cmd/serf-hub/app_rpc.go`) reads
  the past index entry and **passes the stored `ProfileID` through as the
  provider** (PRI-1880); it errors on an empty `ProfileID` rather than silently
  dropping it. The old hardcoded whitelist (which omitted `openai-compatible` and
  would silently drop unknown names) was **removed** — downstream `serf` always
  resolves via `ResolveProfileFromConfig` (so a legacy `ProfileID == "gemini"`
  still resolves to the `google` adapter). The resolved provider + model then drive
  the spawn.
- On **CLI resume**, the stored `meta.ProfileID` is fed into
  `cmdutil.ResolveModelRef` → `buildInitialProfile` (`cmd/serf/serve.go`,
  `cmd/serf/run.go`).

> A resume whose `ProfileID` names a custom instance relies on that instance still
> being defined in `providers.toml`; a vanished instance surfaces the resolver's
> "unknown instance" error (which lists the configured names) rather than being
> silently dropped.

---

## Web / TUI provider surfaces

### Two web settings screens (both render the same data)

- **Providers** (`cmd/serf-hub/templates/partials/settings/providers.html`) —
  **read-only** status. It calls `launchconfig.authList()` and shows each
  provider's `activeSource` badge and auth modes (`providers.html:27`). It links
  out to the credentials page for edits.
- **Credentials** (`cmd/serf-hub/templates/partials/credentials.html`) —
  **read-write**. Same `authList()` source (`credentials.html:247`), plus buttons
  to set/replace an API key (`authApiKeySet`), clear credentials
  (`authLogout`), and the OpenAI OAuth device-code / paste-back flows
  (`authDeviceStart` / `authDevicePoll` / `authLoginStart` /
  `authLoginComplete`, `credentials.html:58-107,324-341`). It renders shadowed
  vs. effective layers (oauth > file > env) so you can see, e.g., an OAuth
  sign-in shadowing a stored key (`credentials.html:131-155`).

Both screens are duplicative by intent (both render the `authList` response);
Providers is the at-a-glance view, Credentials is the editor. Both still enumerate
the fixed provider **type** set (not config instances); **Phase 2** replaces them
with one instance-aware CRUD screen — in the web hub **and** `serf-tui`.

### Model strings and display

Model strings are always `provider/model`. Pickers group by provider. Display
code strips a **hardcoded** provider-prefix allowlist before showing the model:

- TUI: `abbreviateModel` (`cmd/serf-tui/model_display.go:7`) strips
  `anthropic/`, `openai/`, `google/`, `openrouter/`, `openai-compatible/` and a
  trailing `-YYYYMMDD` suffix.
- Web: `abbreviateModel` (`cmd/serf-hub/assets/spawn.js:291`) strips the same
  prefixes **except** `openai-compatible/` (`spawn.js:295`), plus the date
  suffix.

> Minor discrepancy: the two allowlists differ — the web one omits
> `openai-compatible/`, so an `openai-compatible/...` model keeps its prefix in
> the web UI but not in the TUI.
>
> The picker/launch **behavior** filters that used to key on a literal provider
> name (`launch_check.go:146`, `web.go:2035`, `app_rpc.go
> launchProviderAllowsUnreportedModels:1543`) were **re-keyed by behavior tag in
> Phase 1b** (via `BehaviorTagOf` / `NameToTag`). The **display** allowlists above
> (`abbreviateModel` in both the TUI and the web) still strip a hardcoded
> provider-prefix set and remain a **Phase 2** item — to be made instance-aware in
> both surfaces.

The web picker classifies a stored model as valid / stale / unknown by comparing
against the live enumerated list (`spawn.js:119-139`); providers that can't be
enumerated (OAuth-only, `openrouter-anthropic`) fall into "unknown" rather than
being shown as broken.
