# Provider Instances — Phase 1c: all-config-driven (materialization) Design

Status: **DRAFT** (2026-05-29). Sub-project of PRI-1880, following Phase 1a
(behavior-tag separation) and Phase 1b (config-driven instances), both merged to
local `main`. **Supersedes the env/config split decided in the v7 design's
§4.10–4.11.**

Parent design record:
[`2026-05-29-provider-type-instance-model-design.md`](2026-05-29-provider-type-instance-model-design.md)
(v7). As-built architecture: [`../../llm-providers.md`](../../llm-providers.md) +
[`../../llm-provider-config-and-launch.md`](../../llm-provider-config-and-launch.md).

## Decisions captured (Jesse, 2026-05-29)

1. **Everything is a config instance.** Collapse the env-path/config-path duality.
   There is one model — instances declared in `providers.toml`. The env path stops
   being a *separate default*.
2. **Materialize on first run.** When no `providers.toml` exists, the hub writes
   one seeded from the env-detected providers (forward-only seed ≈ the ticket's
   original first-run migration). Trigger: **hub auto-materialize on first run**.
3. **Descriptors-only `providers.toml`.** It holds non-secret instance descriptors
   (`name`/`type`/`base_url`/`api_style`/`quirks`). Secrets stay out: per-instance
   keys in `credentials.toml` (looked up by instance name) + OAuth in
   `auth/<name>.json` + env vars as a type-keyed fallback.
4. This **deliberately reverses §4.11's "no synthesis."** The seed stays
   **shallow** (descriptors only; OAuth-backend selection and `openai-compatible`
   gating stay inside the adapters), honoring §4.11's valid concern: don't
   re-implement the env factories.

## 1. Goal

One coherent instance model that the runtime *and* the Phase 2 UI both bind to,
eliminating the env/config split that makes the hub's settings screens enumerate
fixed provider *types* instead of actual instances.

## 2. Why

Phase 1b added the config path (`NewFromProviders`) **alongside** the still-default
env path (`NewFromEnv`). With no `providers.toml`, serf runs the env path — implicit
one-instance-per-type. The hub's Providers/Credentials screens enumerate the
hardcoded type set, so a user sees "a column of unconfigured providers" and no way
to add an instance, because there are *two* sources of truth. Phase 1c makes
`providers.toml` the single source.

## 3. The model

- `providers.toml` at `<hubStateRoot>/providers.toml` is **always** the source of
  truth for which instances exist. Descriptors only — no secrets:

  ```toml
  default = "openai"
  [instances.openai]
  type      = "openai"
  api_style = "responses"
  ```

- **Construction** flows through one path: `cmdutil.LoadClient` resolves the
  config; if the file is **absent it is materialized first** (§4), then loaded.
  `LoadClient` then **injects resolved credentials** into the in-memory `Config`
  (§5) and calls `llm.NewFromProviders`. The env path as a *default* is retired;
  `llm.NewFromEnv` survives only as (a) the detection mechanism the materializer
  uses and (b) a transitional safety fallback if materialization fails (logged).
- **Secrets are never written to `providers.toml`** and are resolved per instance
  at load time (§5).

## 4. Materialization (`internal/providerconfig` + `cmdutil`)

- **Trigger:** when the resolved providers-config path has no file, `LoadClient`
  **writes one before loading**. A single shared helper produces it so the hub,
  `serf serve`, and `serf run` all materialize/consume the identical file.
  **Idempotent** — only writes when absent; never overwrites an existing file.
  In hub flows the hub materializes **before spawning**, so spawned `serf serve` /
  `launch-check` children find the file already present (the hub passes its path
  via `SERF_PROVIDERS_CONFIG`); a standalone `serf run` materializes at the default
  root. Writes are **atomic** (temp + rename), so the idempotent guard is race-safe.
- **Seed:** build a throwaway client via `NewFromEnv`, enumerate
  `ProviderNames()`, and emit one `[instances.<name>]` descriptor per configured
  provider. The descriptor carries only **non-secret** fields:
  - `type` = the provider type.
  - `base_url` — captured from the per-type base-URL env override **when set**
    (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `GEMINI_BASE_URL`, `KIMI_BASE_URL`,
    `GLM_BASE_URL`, `OPENROUTER_BASE_URL`, `MINIMAX_BASE_URL`,
    `OLLAMA_BASE_URL`/`OLLAMA_HOST`); omitted when the type default applies, so the
    adapter keeps using its default.
  - `api_style` — only for `openai`-family instances (see §7).
  - **No `api_key`, ever.**
- **`default`** = the env client's chosen default provider (today's
  first-registered, non-`NonDefaultEligible` pick), so bare-name resolution is
  unchanged.
- The materialized file is mode `0644` (it holds no secrets); `credentials.toml`
  remains `0600`.

## 5. Credential resolution (the core new mechanism)

The instance adapter factories (`llm/providers/*/adapter.go`
`RegisterInstanceAdapterFactory`) take the key straight from `inst.APIKey` with
**no env fallback** (verified: `openai/adapter.go:156-163`, `kimi/adapter.go:75-81`,
etc.). With descriptors-only `providers.toml`, `inst.APIKey` is always empty, so we
must resolve and inject credentials before adapter construction:

- **Where:** in `cmdutil.LoadClient`, which already holds the `credentials.Store`.
  After loading the descriptor `Config`, for each instance it resolves the key and
  sets `inst.APIKey` on the **in-memory** `Config` (the file stays descriptors-only),
  then calls `NewFromProviders`. `llm` gains no dependency on `internal/credentials`.
- **Resolution order, per instance `(name, type)`:**
  1. `credentials.toml[name]` — keyed by **instance name** (so a custom `work`
     instance has its own key).
  2. env var for the **type** (`providerEnvVar[type]`, e.g. `OPENAI_API_KEY`) — the
     type-keyed fallback that keeps materialized defaults (name == type) working
     with no `credentials.toml` entry.
  3. none → the adapter relies on OAuth (openai) or fails at construction as today.
  This requires a `credentials.Store` lookup that accepts `(name, type)` — the
  store is currently keyed by type only; Phase 1c adds the name-first/type-fallback
  lookup.
- **OpenAI OAuth is unchanged:** `openai.NewForInstance` resolves OAuth from
  `auth/<name>.json` via `StateHome` (`adapter.go:78-121`); the injected key is only
  the non-OAuth fallback.
- **Preserve OpenAI env tunables.** The current openai instance factory drops
  `OPENAI_ORG_ID` / `OPENAI_PROJECT_ID` / `OPENAI_CHATGPT_BASE_URL` (it only passes
  name/base/key — `adapter.go:156-163`). Phase 1c restores these on the config path
  (the factory reads them from env, since they are not yet per-instance descriptor
  fields) so the default openai instance behaves exactly as the env path did.

## 6. Code changes (inventory)

- `internal/providerconfig`: a `Materialize`/`Seed` helper (descriptor synthesis
  from an env client + the base-URL env map) and a `Marshal` for writing
  `providers.toml`.
- `cmdutil.LoadClient`: materialize-if-absent; inject resolved credentials into the
  in-memory `Config`; always go through `NewFromProviders`. (1b already routes
  serve/run/launch-check/`/api/models` through `LoadClient`, so they inherit this.)
- `internal/credentials`: `(name, type)` lookup (name-first, type-env fallback);
  keep type-keyed file entries working for default instances.
- `llm/providers/openai/adapter.go`: instance factory restores org/project/chatgpt
  env tunables.
- Retire env-path-as-default: `NewFromEnv` is no longer a runtime default, only a
  materialization input + logged fallback.

## 7. Edge cases

- **openai (OAuth or key):** `[instances.openai] type=openai api_style=responses`.
  OAuth via `auth/openai.json`; else injected key; else error. ✓
- **openai-compatible** (`OPENAI_COMPATIBLE_BASE_URL` set in env): materialize
  `[instances.openai-compatible] type=openai api_style=chat-completions
  base_url=<that>`. Routes through the `("openai","chat-completions")` factory →
  `openaicompat`. The base URL **is** captured (required, non-secret).
- **anthropic / google / kimi / glm / openrouter / openrouter-anthropic / minimax:**
  `type=<that>`; `base_url` only if the env override was set.
- **ollama:** `type=ollama`, `base_url` captured (required). Its
  `NonDefaultEligible` opt-out is **subsumed by the explicit `default` field** — the
  materializer simply never picks ollama as `default`.
- **gemini alias:** materialized as `type=google` (the canonical id since 1a);
  `gemini/...` still works as a `SelectProfile` input alias.
- **default selection parity:** with both `ANTHROPIC_API_KEY` and `OPENAI_API_KEY`
  set, the env default is `anthropic` (alphabetical first-registered) — the seeded
  `default` must match.

## 8. Out of scope

- **The unified CRUD UI = Phase 2.** 1c ships the model + materialization; instances
  are still managed by hand-editing `providers.toml` + the existing credential
  surfaces until Phase 2.
- **No migration of existing deployments** (flag-day; zero deployed configs). The
  materializer is a *forward* seed, not a migration of old on-disk state.
- **Per-instance org/project/chatgpt-base as descriptor fields** — later; for now
  the openai factory keeps reading them from env.

## 9. Testing strategy

- **Materialization:** absent file → a descriptors-only `providers.toml` is written
  seeded from a stub env client; round-trips through `Load`; **idempotent** (present
  file is never overwritten); **no `api_key` ever written**; `0644`.
- **Seed fidelity:** for each provider type, the seeded descriptor + injected
  credential produces an adapter equivalent to the env-path adapter (same base URL,
  same key source); `openai-compatible` → `openai`/`chat-completions` with the env
  base URL; `default` matches the env default; ollama never chosen as default.
- **Credential injection:** `credentials.toml[name]` wins; absent → env-by-type;
  a custom-named instance (`work`, type openai) resolves `OPENAI_API_KEY`;
  descriptors-only file never carries the key.
- **OpenAI parity:** OAuth instance still routes to Codex; org/project/chatgpt-base
  env tunables are honored on the config path.
- **Behavior preservation:** the **renamed-instance integration test** (§7 of the
  v7 spec, `agent/provider_instance_integration_test.go`) and the full suite stay
  green; a from-env materialized setup behaves identically to today's env path.
- **Pristine output:** the materialization-failure fallback logs a captured,
  asserted message (not a silent swallow).

## 10. Risks / open questions

- **base_url env capture map.** Materialization needs a type→base-URL-env-var map
  (it exists piecemeal in `cmdutil.providerEnvConfig` / the adapters). Risk: missing
  a var means a silently-default base URL. Mitigation: drive the map off the same
  source the adapters use; the seed-fidelity test catches drift.
- **Two key homes during transition.** `credentials.toml` stays type-keyed on disk;
  the resolver reads name-first then type. A custom instance with the *same name* as
  a type could shadow — names are unique and lowercased (loader-enforced), so this is
  bounded; documented.
- **`NewFromEnv` retained.** Keeping it as the materialization input means the env
  factories remain the source of truth for *detection*; that's intentional (it is
  §4.11's valid core) — we are not re-implementing them, only reading their output.
