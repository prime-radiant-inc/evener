# Provider Instances (Phase 1b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make provider *instances* real and config-driven: a machine-global `~/.serf/providers.toml` defines named instances (custom base URLs, multiple instances of one type, `openai` `apiStyle`, per-instance OAuth), loaded by `llm.NewFromProviders` and an instance-aware resolver, reaching the daemon + the hub + the `launch-check` subprocess. When the file is absent, the existing `NewFromEnv` path is unchanged (zero-config dev still works).

**Architecture:** Builds on Phase 1a (instance NAME vs behavior TAG; session-injected `ResolveProfile`; central identity stamping; `internal/providerconfig` leaf). 1b adds: a `providers.toml` loader; per-instance adapter constructors (the env factories are opaque, so each adapter gains an instance constructor); `llm.NewFromProviders(cfg)`; a config-aware resolver; the `openai` apiStyle recipe + custom `anthropic` base-URL instances; per-instance OAuth (`auth/<instance>.json`); spawn `SERF_PROVIDERS_CONFIG`; and re-keying the deferred picker/launch behavior filters via `NameToTag` (now testable with custom instances).

**Tech Stack:** Go. Design record: `docs/superpowers/specs/2026-05-29-provider-type-instance-model-design.md` (v7, §4.7–4.13). As-built 1a architecture: `docs/llm-providers.md`.

**No migration (flag day):** zero deployed instances. Old `credentials.toml`/env/`openai.json` are NOT converted; `providers.toml` is created by the (future Phase 2) UI / a CLI / hand-editing. When `providers.toml` is absent, serf uses the existing `NewFromEnv` path with identity `NameToTag` (§4.10).

**Completeness method:** like 1a — the **integration backstop (Task 11)** + a grep sweep (Task 12) are the real guard; the per-task lists are starting points. Every task: `go build ./... && go test ./...` green before commit.

---

## Sub-phase boundaries (natural checkpoints)
- **1b-core (Tasks 1–6):** loader + per-instance adapters + `NewFromProviders` + config-aware resolver + the load-or-env helper + apiStyle/anthropic recipes. **Landing:** a hand-written `providers.toml` with multiple/custom instances routes end-to-end through `serf run`/`serve`.
- **1b-auth+spawn (Tasks 7–8):** per-instance OAuth + `SERF_PROVIDERS_CONFIG` spawn plumbing (hub-spawned daemons + launch-check use the file).
- **1b-rekey+verify (Tasks 9–12):** re-key the deferred behavior filters; integration backstop; sweep + full suite + smoke.

---

## Task 1: `providers.toml` schema + loader (`internal/providerconfig`)

**Files:** Modify `internal/providerconfig/providerconfig.go`; create `internal/providerconfig/load.go` + `load_test.go`.

Current `InstanceConfig{Name,Type,APIStyle,BaseURL,APIKey}` and `Config{Default,Instances}` exist. Add what the loader needs: a `Quirks string` field on `InstanceConfig` (for openai-compatible presets; §4.4). Add `StateHome` only if Task 7 needs it (defer — OAuth uses the global state home + instance filename).

- [ ] **Step 1: failing test** — `Load([]byte)` parses a `providers.toml` fixture (schema=1, default="work", `[instances.work]` type="openai" api_style="responses" api_key="sk-…", `[instances.kimi-corp]` type="kimi" base_url="…" quirks="kimi-k2.5") into a `Config`; round-trips; rejects (a) duplicate names, (b) a name containing `/`, (c) a non-lowercase name, (d) an unknown `type`, (e) a `default` naming no instance.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `Load(data []byte) (Config, error)` (and a `LoadFile(path)` that returns `(Config, bool, error)` — bool=exists) using the repo's TOML lib (match what `cmd/serf-hub/config.go`/`credentials` use). Validate: unique lowercased names, no `/`, known `Type`, `apiStyle` only for `openai`, `default` resolves (else first by sorted name). Add `Quirks` to `InstanceConfig`.
- [ ] **Step 4:** run → pass. `go build ./...`.
- [ ] **Step 5:** commit `feat(providerconfig): providers.toml schema + loader (PRI-1880)`.

## Task 2: per-instance adapter constructors — native adapters

The env factories read env directly (opaque). Give each adapter a constructor that takes resolved config so `NewFromProviders` can build per instance. Start with the native trio.

**Files:** `llm/providers/openai/adapter.go`, `llm/providers/anthropic/adapter.go`, `llm/providers/google/adapter.go` (+ tests).

- [ ] **Step 1: failing tests** — `openai.NewForInstance(openai.InstanceParams{Name, BaseURL, APIKey, OrgID, ProjectID, ChatGPTBaseURL, StateHome})` builds an adapter whose `Name()==Name`; `anthropic.NewForInstance({Name,BaseURL,APIKey})` and `google.NewForInstance({Name,BaseURL,APIKey})` likewise. (OAuth state-dir threading for openai is Task 7 — for now accept `StateHome` and pass it through to the existing OAuth resolution.)
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement: extract the construction bodies currently inside each `NewFromEnv`/factory into a `NewForInstance(params)` that the env factory then calls (env factory = read env → params → `NewForInstance`). Keep `Name()` returning the instance name (params.Name). Don't change wire behavior.
- [ ] **Step 4:** run → pass; existing provider tests stay green.
- [ ] **Step 5:** commit.

## Task 3: per-instance adapter constructors — openaicompat family + ollama

**Files:** `llm/providers/openaicompat/adapter.go` (+ `kimi`, `glm`, `openrouter`, `minimax`, `openrouter_anthropic`, `ollama`).

The thin wrappers (kimi/glm/openrouter→openaicompat; minimax/openrouter-anthropic→anthropic) currently hardcode base URL + `QuirksPreset(type)`. Give them instance constructors that take `{Name, BaseURL, APIKey, Quirks}` (quirks resolved from the type's preset by default, overridable by `InstanceConfig.Quirks`).

- [ ] **Step 1: failing tests** — `openaicompat.NewForInstance({Name:"work",BaseURL,APIKey,Quirks:QuirksPreset("kimi")})` → adapter `Name()=="work"` with kimi quirks; the wrapper helpers (`kimi.NewForInstance`, etc.) set the right base URL + preset by type; `ollama.NewForInstance({Name,BaseURL})`.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement (extract bodies into `NewForInstance`; the env factories call them). `minimax`/`openrouter_anthropic` wrap the anthropic adapter with their base URL.
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 4: `llm.NewFromProviders(cfg)` + `Client.SetNameToTag`

**Files:** `llm/env_registry.go` (or a new `llm/providers_config.go`), `llm/client.go` (+ tests).

- [ ] **Step 1: failing test** — `NewFromProviders(providerconfig.Config{...})` with two `openai` instances (`work`, `work2`) + one `kimi` instance registers three adapters routable by name; sets the configured default; `client.nameToTag` is populated (`work→openai`, `kimi-corp→kimi`) so `behaviorTagFor` returns the tag. A `chat-completions` openai instance registers the openaicompat adapter under its name (tag `openai-compatible`).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `NewFromProviders(cfg, opts...)`: for each instance, map `(Type, APIStyle)` → the adapter `NewForInstance` (Tasks 2/3); register under `instance.Name`; set default = `cfg.Default`; call `SetNameToTag(providerconfig.NameToTag(cfg))`. (The `openai`+`chat-completions` case builds the openaicompat adapter — the apiStyle fold-in, §4.7.)
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 5: config-aware profile resolver

**Files:** `agent/resolve.go` (new) or extend the closure; `cmdutil/cmdutil.go`; `cmd/serf/serve.go`, `cmd/serf/run.go` (+ tests).

The 1a resolver closure calls `cmdutil.SelectProfile` (env path). Make it config-aware: given the loaded `Config`, resolve an instance name → build the type's profile via the constructor with `id=instanceName` (via `WithProviderID`), the instance's `apiStyle`/`baseURL`/context.

- [ ] **Step 1: failing test** — `agent.ResolveProfileFromConfig(cfg, "work/gpt-5.2")` → profile `ID()=="work"`, `BehaviorTag()=="openai"`; `"kimi-corp/kimi-k2"` → `ID()=="kimi-corp"`, tag `kimi`; unknown instance → clear error listing instances. A `chat-completions` instance → tag `openai-compatible`.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `agent.ResolveProfileFromConfig(cfg, ref)` (uses the agent constructors + `WithProviderID` + apiStyle→constructor mapping; context window from catalog/config, not env). `cmdutil.SelectProfile` stays for the env path. In `serve.go`/`run.go`, the injected `ResolveProfile` becomes: if a `Config` is loaded → `ResolveProfileFromConfig`; else the existing `SelectProfile` closure.
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 6: the load-or-env helper; wire all client consumers

**Files:** a shared helper (e.g. `cmdutil` or a small `internal/providerload`); `cmd/serf/serve.go`, `run.go`, `cmd/serf-hub/web.go`, `cmd/serf/launch_check.go`, `llm/generate.go` `DefaultClient` (+ tests).

- [ ] **Step 1: failing test** — the helper returns `(*llm.Client, providerconfig.Config, hasConfig bool)`: when `$hubStateRoot/providers.toml` exists → `NewFromProviders` + the config + true; else → `NewFromEnv` + identity `NameToTag` + false. The path comes from `providerconfig.DefaultStateRoot()`/`SERF_PROVIDERS_CONFIG`.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement the helper; convert `serve.go:39`, `run.go:127`, `web.go:2024`, `launch_check.go:94` **and** `:159`, `DefaultClient` to use it; pass the `Config` into `SessionConfig`/`WebConfig` so the resolver (Task 5) and filters (Task 9) see it. **Landing: a hand-written `~/.serf/providers.toml` with 2 instances routes end-to-end via `serf run`.**
- [ ] **Step 4:** run → pass; `go build ./... && go test ./...`.
- [ ] **Step 5:** commit. *(End of 1b-core.)*

## Task 7: per-instance OAuth

**Files:** `internal/auth/openai/storage.go`, the openai adapter OAuth resolution, `cmd/serf-hub/app_auth.go`, `cmd/serf/openai_login.go`, `cmd/serf-hub/spawn.go validateProviderCredentials` (+ tests).

- [ ] **Step 1: failing tests** — `AuthFilePath(stateHome, instanceName)` → `$stateHome/serf/auth/<instanceName>.json`; default `openai` instance keeps `auth/openai.json`; `LoadAuth/SaveAuth/DeleteAuth` round-trip per instance; `AuthRecord.Validate` accepts a non-`openai`-named openai-tag instance (drop the hardcoded `Provider=="openai"` check); a custom openai instance `work` resolves its OAuth from `auth/work.json`.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement: thread `instanceName` through `AuthFilePath`/`LoadAuth`/`SaveAuth`/`DeleteAuth` + the openai adapter's OAuth resolution (it already has `StateHome` from Task 2) + the hub auth controller's `openai` branches (keyed by instance whose tag advertises oauth) + `openai_login` (`resolveOpenAIStateDir` gains an instance param) + `validateProviderCredentials`. Drop `AuthRecord.Validate`'s `Provider=="openai"` check.
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 8: spawn `SERF_PROVIDERS_CONFIG`

**Files:** `internal/launchconfig/env.go`, `cmd/serf-hub/spawn.go`, the daemon load helper (Task 6) (+ tests).

- [ ] **Step 1: failing tests** — `launchconfig.ToEnv` sets `SERF_PROVIDERS_CONFIG=<path>` when the hub has a config; a spawned `serf serve` and the `serf launch-check` subprocess both load it (the load helper consults `SERF_PROVIDERS_CONFIG` first, Task 6); `validateProviderCredentials` resolves the instance set (not the fixed maps) when a config exists.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement: thread `SERF_PROVIDERS_CONFIG` into `req.Env` via `ToEnv`; both spawn paths get it; the load helper already consults it. Remove the single-provider env injection when a config is present (keep env fallback when absent).
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 9: re-key the deferred behavior filters via `NameToTag`

**Files:** `cmd/serf/launch_check.go` (the `openrouter-anthropic` skip + `openrouter` tools filter), `cmd/serf-hub/web.go` (picker filters), `cmd/serf-hub/app_rpc.go launchProviderAllowsUnreportedModels` (+ tests). May add a `Client.BehaviorTagOf(name)` accessor (identity fallback).

- [ ] **Step 1: failing tests** — with a config where instance `or-work` has type `openrouter`, the launch-check/picker filters that today match `provider=="openrouter"` fire for `or-work` (via `NameToTag`/`BehaviorTagOf`); a renamed `openrouter-anthropic` instance is still skipped in model enumeration by tag.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement: these sites take the config's `NameToTag` (or a `Client.BehaviorTagOf`) and compare the tag, not the literal name. Env path: identity (unchanged behavior).
- [ ] **Step 4:** run → pass.
- [ ] **Step 5:** commit.

## Task 10: `cmdutil.queryModelContextWindow` instance-awareness (small)

**Files:** `cmdutil/cmdutil.go` (+ test). The env-keyed context-window query (`KIMI_*` etc.) is orphaned for custom instances. Make the config path supply context window from the catalog/config; keep env behavior when no config.

- [ ] Steps: TDD that a custom kimi instance gets its context window from the catalog (by tag), not env; implement; commit.

## Task 11: integration backstop (custom instances end-to-end)

**Files:** a new integration test (agent + a hub-level test if needed).

- [ ] Write a test that loads a `providers.toml` with: two `openai` instances (responses) named `work`/`work2`; an `openai` instance with `apiStyle=chat-completions` + custom base URL; a custom-base-URL `anthropic` instance; a `kimi` instance. Assert: each routes through `NewFromProviders` by name; behavior keys on tag (the chat-completions one gets no `openai`-tag behavior); a per-instance OAuth file round-trips; resume of a custom-instance session reconstructs the ref; `SetModel` between two instances preserves overrides. Must PASS.
- [ ] Commit.

## Task 12: completeness sweep + full suite + smoke

- [ ] Grep sweep for residual provider-name literals that should now be tag/config-keyed (the 1a-deferred set should be gone; flag any new (d) site). Fix real ones (TDD).
- [ ] `go build ./... && go test ./...` green.
- [ ] Smoke: build serf; with a temp `providers.toml` (two openai instances + a custom base URL), `serf launch-check --model work2/gpt-5.2 --json` resolves the custom instance (credential-free).
- [ ] Final commit.

---

## Self-Review (run before execution)
- **Spec coverage:** §4.7 apiStyle → Tasks 2/3/4/5; §4.8 anthropic base-URL → Tasks 2/5; §4.9 OAuth → Task 7; §4.10 loader/flag-day/env-fallback → Tasks 1/6; §4.11 NewFromProviders + consumers → Tasks 4/6; §4.12 spawn → Task 8; §4.13/§4.2 deferred filters → Task 9.
- **Opaque-factory risk:** Tasks 2/3 extract `NewForInstance` constructors so `NewFromProviders` doesn't reimplement env logic; the env factories become thin wrappers over them.
- **Behavior-preserving env path:** when no `providers.toml`, everything uses `NewFromEnv` + identity `NameToTag` — unchanged.
- **No placeholders:** each task has a concrete failing test + the exact files/signatures (from the code map).

## Execution Handoff
Subagent-driven (recommended), with the 1b-core (Tasks 1–6) as the first independently-testable landing.
