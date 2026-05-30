# Phase 2 — Unified Provider/Instance CRUD UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** One instance-CRUD screen in both the web hub and `serf-tui`, replacing the duplicate Providers + Credentials screens.

**Architecture:** New instance-CRUD RPCs operate on the hub's `providers.toml` (`ProvidersConfigPath`); the existing credential RPCs are re-keyed by instance name; both UIs bind to a new instance-list shape; pickers display by instance name. Backend (tasks 1–5) lands first, then the two UIs (6–8), then picker display (9), then closeout (10).

**Tech stack:** Go, `internal/appwire` RPC types, BurntSushi/toml via `internal/providerconfig`, the hub's `assets/*.js` bridge + Go HTML templates, bubbletea/lipgloss tui.

Spec: [`../specs/2026-05-29-provider-instances-phase-2-ui.md`](../specs/2026-05-29-provider-instances-phase-2-ui.md). Parent (1c, merged): the all-config model.

---

## Key existing files (read before touching)

- `internal/appwire/types.go` — RPC param/response structs (e.g. `AuthListResponse`, `AuthStatusResponse`, `AuthApiKeySetParams`).
- `cmd/serf-hub/app_auth.go` — `hubAuthController` (`Status/List/ApiKeySet/Logout/LoginStart/LoginComplete/DeviceStart/DevicePoll`), `newHubAuthController*`.
- where RPC methods are dispatched to JS (grep `authList`/`ApiKeySet` registration; the JS side is `cmd/serf-hub/assets/launchconfig.js`).
- `cmd/serf-hub/spawn.go` — `HubSpawner.ProvidersConfigPath` (the providers.toml path) + `ProviderConfig`.
- `cmd/serf-hub/main.go` — controller construction + the materialized config (`loadedProviderConfig`, `providersConfigPath`).
- `internal/providerconfig/{providerconfig.go,load.go,materialize.go}` — `Config`/`InstanceConfig`, `Load`/`LoadFile`, `Seed`/`Marshal`.
- `internal/credentials/store.go` — `ResolveKey`, `Set`/`Clear`/`Get`, source layers.
- `cmd/serf-hub/templates/partials/credentials.html`, `settings/providers.html`; `cmd/serf-hub/assets/spawn.js` (`abbreviateModel`).
- `cmd/serf-tui/credentials_panel.go`, `model_display.go`.

---

### Task 1 — appwire instance types

**Files:** `internal/appwire/types.go` (+ existing test file if any).
Add: `InstanceEntry` (name, type, apiStyle, baseURL, isDefault, authModes, activeSource, hasStoredFile, hasStoredOAuth, envVar, storedEmail — mirror the credential fields of `AuthStatusResponse`); `InstanceListResponse{Instances []InstanceEntry}`; `InstanceCreateParams{Type,Name,APIStyle,BaseURL}`; `InstanceEditParams{Name,APIStyle,BaseURL}`; `InstanceRemoveParams{Name}`; `InstanceSetDefaultParams{Name}`. JSON tags matching the existing camelCase convention in the file.
- [ ] Write a struct/JSON round-trip test for the new types (match how existing appwire types are tested). Build green. Commit.

### Task 2 — `providerconfig` write + mutate helpers

**Files:** `internal/providerconfig/materialize.go` (or a new `mutate.go`) + test.
Add `func WriteFile(path string, cfg Config) error` — `Marshal` + atomic write (temp + rename, 0644); refactor `cmdutil.MaterializeProvidersConfig` to call it (DRY). Add pure mutators on `Config`: `Upsert(inst InstanceConfig) Config`, `RemoveInstance(name string) Config`, `WithDefault(name string) Config`, and a `ValidateInstanceName(name) error` (lowercase, non-empty, no `/`, unique vs existing) + `ValidateAPIStyle(typ, style) error` (style only for openai).
- [ ] TDD each: WriteFile round-trips through `LoadFile` (descriptors-only, no `api_key`); mutators are pure; validators reject bad input. Commit.

### Task 3 — instance controller (CRUD)

**Files:** `cmd/serf-hub/app_instances.go` (new) + test.
`hubInstancesController{ providersPath string; cfg *providerconfig.Config; creds *credentials.Store; auth *hubAuthController; mu sync.Mutex }`.
- `List() InstanceListResponse` — for each instance, join credential status (resolve file/oauth/env per instance **name**, type's env var; reuse the `auth` controller's per-name status logic — see Task 4) + `isDefault`.
- `Create/Edit/Remove/SetDefault` — `mu.Lock`; reload from disk (`providerconfig.LoadFile`); validate (Task 2); mutate (Task 2); `providerconfig.WriteFile`; update the in-memory `*cfg`. Remove also clears `creds.Clear(name)` + deletes `auth/<name>.json`; if it removed the default, WithDefault(first remaining).
- [ ] TDD: create→list; edit keeps type; remove drops + clears creds/oauth + reassigns default; setDefault persists; round-trips through Load; no secret in the file. Commit.

### Task 4 — re-key credential RPCs by instance name

**Files:** `cmd/serf-hub/app_auth.go` + test.
Change `ApiKeySet/Logout/DeviceStart/DevicePoll/LoginStart/LoginComplete/Status` to key on instance **name**, resolving the instance **type** from the loaded config to gate auth modes (OAuth only for openai-tag) and to target `credentials.toml[name]` / `auth/<name>.json`. Keep the device-code flow intact. Expose a per-instance status helper the instance controller (Task 3) reuses for `List`.
- [ ] TDD: `ApiKeySet("work",…)` → `credentials.toml[work]`; OAuth for an openai-type `work` → `auth/work.json`; OAuth rejected for non-openai; status reflects per-instance. Commit.

### Task 5 — register the new RPCs

**Files:** wherever the hub registers controller methods for the JS bridge (grep the `authList`/`List` registration) + `cmd/serf-hub/main.go` (construct `hubInstancesController` with `providersConfigPath`, `credsStore`, the auth controller).
- [ ] Wire `instanceList/instanceCreate/instanceEdit/instanceRemove/instanceSetDefault`. Build green; a smoke RPC test if the harness supports it. Commit.

### Task 6 — JS bridge

**Files:** `cmd/serf-hub/assets/launchconfig.js`.
Add `instanceList()/instanceCreate(...)/instanceEdit(...)/instanceRemove(name)/instanceSetDefault(name)` mirroring the existing `auth*` method style; ensure the re-keyed `authApiKeySet`/`authLogout`/`authDeviceStart`/etc. pass the instance name.
- [ ] Commit (covered by the web test in Task 7).

### Task 7 — web screen

**Files:** rewrite `cmd/serf-hub/templates/partials/credentials.html`; remove/repoint `settings/providers.html`; reuse `settings-collection`/`status-badge`/source-layer CSS.
Render instances **grouped by type**, each row per the approved mockup (name, ★default, apiStyle/base_url, source-layers, actions `Set/Replace key · Sign in/Refresh OAuth · Clear · Edit · Remove · make default`). Per-type `[+ add instance]` inline form (name, apiStyle for openai, base_url, credential later/key/oauth). Reuse the existing device-code OAuth editor.
- [ ] Test mirroring the existing credentials.html approach (JSDOM/RPC round-trip) if present; otherwise a focused render + create + remove test. Commit.

### Task 8 — tui panel

**Files:** rewrite `cmd/serf-tui/credentials_panel.go` (+ test).
Grouped instance list + keybindings (`↑↓`, `enter` set key, `o` oauth, `c` clear, `n` new, `e` edit, `x` remove, `*` default, `esc`); a create/edit sub-form (type pre-chosen for new; name/apiStyle/base_url). Reuse the existing set-key/oauth message flows, keyed by instance name.
- [ ] Model test: grouped render; each keybinding emits the right message. Commit.

### Task 9 — picker display by instance name

**Files:** `cmd/serf-hub/assets/spawn.js` (`abbreviateModel`), `cmd/serf-tui/model_display.go` (+ tests).
Display models by their instance name rather than stripping a hardcoded type-prefix allowlist (the model ref is already `instanceName/model` from 1b). Keep the date-suffix stripping.
- [ ] TDD: a custom-instance model (`work/gpt-5`) displays labeled by `work`, not mis-stripped. Commit.

### Task 10 — integration + closeout

- [ ] End-to-end: create an instance via the controller → it appears in `List`, in the materialized `providers.toml`, and (smoke) in `/api/models`. Remove → gone + creds cleared.
- [ ] `go build ./...`, `go test ./...` green; **no `~/.serf` pollution** (tests isolate `ProvidersConfigPath`/state dir).
- [ ] Final holistic review subagent over the cumulative diff; fix findings.
- [ ] superpowers:finishing-a-development-branch (merge to local main).

---

## Self-review

- **Spec coverage:** data (T1), config writes (T2), CRUD (T3), re-key (T4), wiring (T5), web (T6–7), tui (T8), pickers (T9), integration (T10). ✓
- **Type consistency:** all CRUD goes through `providerconfig.Config`/`InstanceConfig` + `WriteFile`; credential status reuses one per-name helper (T4) shared by `List` (T3). ✓
- **No secret to disk:** every write is `providerconfig.Marshal` (descriptors-only) — asserted in T2, T3. ✓
