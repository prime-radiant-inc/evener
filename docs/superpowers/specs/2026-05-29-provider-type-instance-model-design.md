# Provider Type/Instance Model

Date: 2026-05-29
Status: design approved; phased build (Phase 1 first)
Ticket: PRI-1880

## 1. Goal

Turn serf's hardcoded provider registry into a **type/instance** model: provider
*types* (code-backed adapter kinds) that users *instantiate* into named,
configured providers. Let users **create, edit, and remove** providers —
including arbitrary OpenAI-compatible and Anthropic-compatible endpoints and
multiple instances of the same vendor — and replace the two duplicative web
settings screens (Providers + Credentials) with one CRUD screen.

## 2. Non-Goals

- **New per-vendor protocols.** We keep the existing adapters; we do not add
  support for vendors whose wire protocol no adapter already speaks.
- **Encryption at rest.** API keys stay verbatim in a `chmod 600` file, same
  threat model as today's `credentials.toml`.
- **Breaking existing setups.** Existing `credentials.toml` + `openai.json`
  must keep working with zero user action (migration, §4.5).
- **Removing env-var configuration.** Process env (`OPENAI_API_KEY`, …) remains
  honored as a fallback and migration source.

## 3. Background (current architecture)

- Provider adapters live in `llm/providers/*` and self-register via
  `RegisterEnvAdapterFactory` (called from each package `init()`); `NewFromEnv`
  builds a `Client` from every factory that finds its env vars
  (`llm/env_registry.go`). The `Client` registers adapters by `Name()` and
  routes a `provider/model` string to the adapter of that name.
- The provider set is fixed: `openai`, `anthropic`, `google`/`gemini`,
  `openrouter`, `openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`,
  `openai-compatible`. `openaicompat` is a single endpoint
  (`OPENAI_COMPATIBLE_BASE_URL`/`_API_KEY`).
- Credentials: `internal/credentials/store.go` hardcodes `providerEnvVars` and
  `providerAuthModes`; keys live in `~/.serf/credentials.toml`; OpenAI OAuth in
  `~/.serf/auth/openai.json`.
- Spawn: `internal/launchconfig/ToEnv` injects one env var per provider into
  spawned `serf serve` sessions.
- Wire protocols differ: `openai` → Responses API (`/v1/responses`, or
  `/backend-api/codex/responses` for OAuth); `openaicompat` → Chat Completions
  (`/chat/completions`); `anthropic` → Messages (`/v1/messages`, base URL
  already overridable via `ANTHROPIC_BASE_URL`).

## 4. Design

### 4.1 Provider types

A **type** is a code-backed descriptor of a protocol/adapter plus the knobs an
instance may set. The type set is the current vendors **minus** the two
`-compatible` types:

`openai`, `anthropic`, `google` (alias `gemini`), `openrouter`,
`openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`.

Each type declares:
- `adapter` — which adapter constructs the runtime client;
- `defaultBaseURL`;
- `authModes` — subset of `apiKey`, `oauth`, `none`;
- `editableFields` — which instance fields the UI exposes (always `baseURL`
  except where a vendor requires its own; `apiStyle` for `openai`);
- `envVars` — legacy env var name(s), used only for migration/fallback.

### 4.2 The `openai` `apiStyle` fold-in; `anthropic` base URL

The two `-compatible` types collapse into knobs on the vendor types:

- **`openai`** gains `apiStyle ∈ { responses, chat-completions }` (default
  `responses`). The type's factory builds the existing Responses adapter
  (`llm/providers/openai`) for `responses`, or the existing chat-completions
  adapter (`llm/providers/openaicompat`) for `chat-completions`, in both cases
  with the instance's base URL + credential. "openai-compatible" is thus an
  `openai` instance with `apiStyle=chat-completions` + a base URL. `openaicompat`
  stays as an internal adapter; it is no longer a user-facing type.
- **`anthropic`** instances set their base URL (same `/v1/messages` protocol).
  "anthropic-compatible" is an `anthropic` instance with a base URL. No new
  adapter.

### 4.3 Instances

An **instance** is a persisted, named configuration of a type:

```
{ name: string (unique; the model-routing key),
  type: string (a type name),
  baseURL: string (optional; defaults to the type's),
  apiStyle: string (openai only; default "responses"),
  credential: { apiKey } | { oauth } | none }
```

Every type is **multi-instance**: any number of instances per type, each with a
distinct `name`.

### 4.4 Model routing & default instance

- Models are addressed as **`instanceName/model`**. Because the runtime
  registers one adapter per instance whose `Name()` is the instance name, the
  existing `Client` prefix routing works unchanged.
- **Back-compat:** the default instances created by migration are named after
  their type (`openai`, `anthropic`, …), so existing `openai/gpt-5`-style
  strings keep resolving.
- A single instance is marked **default** (used for bare model names and as the
  fallback). Migration marks the instance that today's "first-registered-wins"
  would have selected; absent that, the first configured instance. The default
  is recorded in the config (§4.5).

### 4.5 Storage & migration

- **`~/.serf/providers.toml`** (hub-owned, `chmod 600`): `schema`, a `default`
  instance name, and one `[instances.<name>]` table per instance with `type`,
  `base_url`, `api_style`, and an inline `api_key` (omitted for OAuth/none).
- **OAuth token state** is per instance at `~/.serf/auth/<name>.json`. The
  default `openai` instance keeps using the existing `~/.serf/auth/openai.json`
  path (its name is `openai`), so no file move is needed; additional OAuth
  instances use their own name.
- **Migration** (run once when `providers.toml` is absent): for each provider
  with a key in `credentials.toml` or a recognized env var, create a default
  instance named after the provider with that credential; if `openai.json`
  exists, the `openai` instance is OAuth-backed. Write `providers.toml`. The old
  files are left in place (read-only fallback); nothing is deleted.

### 4.6 LLM core changes

- A loader reads `providers.toml` into a typed config. `NewFromEnv` (or a new
  `NewFromProviders`) registers **one adapter per instance**, keyed by instance
  name, using the type's adapter with the instance's base URL + credential +
  (for `openai`) `apiStyle`. The default instance is registered as the client
  default.
- When `providers.toml` is absent, fall back to today's env-factory behavior
  (so a fresh checkout with only env vars still works), then migration writes
  the file on the hub's next run.
- The per-instance credential resolution reuses the existing OpenAI resolution
  (OAuth record → key) keyed by the instance's OAuth path / api_key.

### 4.7 Spawn plumbing

- The hub writes/owns `providers.toml` and passes its path to spawned
  `serf serve` via **`SERF_PROVIDERS_CONFIG`** (instead of N per-provider env
  vars). The spawned session's LLM client loads instances from that path.
- `launchconfig.ToEnv` sets `SERF_PROVIDERS_CONFIG`; the existing per-provider
  env injection remains for the fallback/no-config path.

### 4.8 OAuth per instance

- OAuth is a capability of the `openai` type (today's device-code + paste-back
  flows). An OAuth-backed instance stores tokens at `~/.serf/auth/<name>.json`.
- A custom `openai` instance (e.g. `apiStyle=chat-completions` against a
  third-party) uses an API key, not OAuth — the UI offers OAuth only for
  instances of types whose `authModes` include `oauth` (currently `openai`).

### 4.9 Unified UI (Phase 2)

One settings screen replaces both Providers and Credentials:
- Lists instances grouped by type, each showing its effective credential
  source/state and a "default" marker.
- **Create:** pick a type → name → base URL / `apiStyle` / credential.
- **Edit / Remove / Set-default** per instance.
- Per-instance credential management: set/clear API key, or OAuth sign-in via
  the device-code flow built in PRI-1878 (for OAuth-capable types).

## 5. Phasing

- **Phase 1 — core, config-driven, no UI.** Type registry; `providers.toml`
  loader + migration; LLM multi-instance registration + instance-name routing +
  default instance; the `openai` `apiStyle` fold-in (reusing `openaicompat`);
  `anthropic` base-URL instances; `SERF_PROVIDERS_CONFIG` spawn plumbing. Custom
  endpoints work end-to-end via a hand-edited `providers.toml`; existing setups
  migrate untouched. This is the architectural core and is independently
  testable.
- **Phase 2 — unified CRUD UI.** RPCs for instance CRUD + set-default over the
  Phase 1 config, and the single management screen replacing Providers +
  Credentials.

Each phase is its own implementation plan; Phase 2 builds on Phase 1's config
and RPC surface.

## 6. Error handling / edge cases

- **Duplicate / invalid instance names:** names must be unique and usable as a
  model prefix (no `/`); the loader rejects duplicates and reserves nothing.
- **Unknown type in config:** the loader errors clearly, naming the instance and
  type; the hub logs and skips that instance rather than failing to start.
- **Missing credential:** an instance with no usable credential is registered
  but reports "not configured"; launch gating surfaces the missing-credential
  error (as today).
- **Default points at a removed instance:** fall back to the first configured
  instance and log.
- **Corrupt `providers.toml`:** treated like a load error — the hub logs and
  falls back to env-factory behavior rather than crashing.

## 7. Testing strategy

- **Config loader / migration:** unit tests — parse `providers.toml`,
  round-trip, and migrate a fixture `credentials.toml` (+ `openai.json`) into
  the expected instances and default. (`internal/...`)
- **LLM core:** registering N instances of a type yields N adapters routed by
  instance name; the `openai` `apiStyle` selects the Responses vs
  chat-completions adapter; default-instance resolution. (`llm/...`)
- **Spawn:** `ToEnv` sets `SERF_PROVIDERS_CONFIG`; a spawned session loads the
  instances from it. (`internal/launchconfig`, `cmd/serf-hub`)
- **Adapters:** existing `openai`/`openaicompat`/`anthropic` adapter tests stay
  green; add base-URL-override coverage for `anthropic`.
- **Phase 2:** RPC round-trip + JSDOM tests for the management screen.

## 8. Risks / open questions

- **Model-selection UX:** instance-name addressing changes how models are named
  across the TUI/web model pickers (`model_display.go`, composer). Phase 1 keeps
  default-instance names = type names for back-compat; the pickers' handling of
  custom instance names is a Phase 2 concern to verify.
- **Vendor quirks:** `openrouter` (extra headers), `ollama` (local), etc. remain
  their own thin types, so their behavior is unchanged; only base-URL/credential
  become instance config.
- **Config ownership / races:** the hub owns `providers.toml`; spawned sessions
  read it. Concurrent hub edits (Phase 2) must write atomically (temp + rename),
  matching `credentials.toml`.
