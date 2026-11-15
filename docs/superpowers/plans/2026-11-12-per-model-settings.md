# Per-Model Settings and Compaction Thresholds Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-model `compaction_threshold` (in tokens) and full per-model capability and routing override management across `llm/registry`, `agent`, `appwire`, `cmd/evener-hub`, `cmd/evener`, and `InstanceSheet` in the web UI.

**Architecture:** `providers.toml` remains the single durable source of truth via `Caps.CompactionThreshold *int` (`>= 4096`), decoupled from physical `Caps.ContextWindow` / `Caps.MaxInputTokens` wire admission in `llm.ApplyTokenBudget`. `contextmgr.Manager` derives per-profile compaction fractions against `EffectiveWindow(prof)` while keeping `Session.ContextStatus()` invariants intact; the hub exposes fast `InstanceModels` summaries plus `evener/instance/modelGet` and `evener/instance/modelEdit`, consumed by an in-sheet `InstanceModelEditor` drill-in subview inside `InstanceSheet`.

**Tech Stack:** Go 1.26 (`llm/registry`, `llm`, `agent`, `appwire`, `cmd/evener-hub`, `cmd/evener`), TypeScript + React + Zustand + Vitest + Biome (`appwire-client/typescript`, `cmd/evener-hub/frontend`).

**Spec:** `docs/superpowers/specs/2026-11-12-per-model-settings-design.md`

## Global Constraints

- Spec authority: `docs/superpowers/specs/2026-11-12-per-model-settings-design.md` governs all behavior, types, and invariants.
- Follow strict TDD on every task: write failing behavior tests first, run them to observe the expected failure, write minimal production code to pass, and run the affected package suite before committing.
- Never assert on human-readable error copy or UI prose phrasing (`docs/developing-evener/testing.md`); assert structured error codes, wire fields, state transitions, roles, accessible states, and persisted TOML values.
- No test file or test function name may carry a task, sprint, round, pass, or coverage-percentage label (`AGENTS.md`).
- Constants and bounds: `registry.MinCompactionThreshold = 4096`, `registry.MaxTokenLimit = math.MaxInt32` (`2_147_483_647`), `contextmgr.DefaultCompactAt = 0.90`.
- Frontend widget & CSS rules (`docs/developing-evener/design-system.md`): import widgets exclusively from `src/widgets` barrel; write zero raw `<button>`, `<input>`, `<select>`, or `<textarea>` elements outside `src/widgets`; never reach into widget DOM via CSS; avoid all `ds-audit.mjs` `WIDGET_CONCEPTS` substrings (`chip`, `badge`, `pill`, `btn`, `button`, `card`, `empty`, `menu`, `tooltip`, `switch`, `toggle`, `skeleton`, `kbd`, `meter`) in CSS module class names; use `var(--token)` custom properties only; render `ConfirmDialog` as a sibling outside `<Sheet>`; build effort controls from `EFFORT_TIERS` and `effortLabel` in `@evener/appwire-client` (enforced by `make lint-repo-audit`).
- Never use `git add -A` without `git status` first; stage explicit file paths only.
- End every git commit message with this exact trailer line:
  `Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>`

## Review Focus

1. **Slashed and bracketed model IDs (`deepseek/deepseek-4.1-flash`, `claude-opus-4-5[1m]`):** Registry lookup, TOML table keys, `InstanceModels`, `GetModel`, `EditModel`, and client toggle keys must preserve slashes and brackets inside the model ID without splitting `model` on `/`. Pinned in Task 1 and Task 3.
2. **Lowering `ContextWindow` below an already-saved `CompactionThreshold`:** Editing `ContextWindow` down to `200_000` when `CompactionThreshold = 250_000` already sits on that row must trigger rollback/rejection with `appwire.InvalidParams` and leave `providers.toml` unchanged. Pinned in Task 3.
3. **Small valid `CompactionThreshold` on a large window (`4096` on `1,048,576`):** `thresholdsFor` must keep `0 < warn < checkpoint <= summarize < 1.0` so `warn` never collapses to `0` and nudges on an empty session. Pinned in Task 2.
4. **Decimal-suffix edge cases in `parseTokenCount` (`" 250k "`, `"1.13k"`, `"0.29m"`, `"1.2345k"`, `"0"`, `"-5k"`, `"3000m"`):** Base-10 digit shifting must parse `"1.13k"` -> `1130` and `"0.29m"` -> `290000` without IEEE-754 float drift while rejecting sub-token fractions, non-positive values, and `> 2_147_483_647`. Pinned in Task 4.
5. **Unsaved connection draft surviving a default-model protocol edit in `InstanceModelEditor`:** Editing `Protocol` on an instance's default model moves `endpointFingerprint` and `protocol` on the parent `InstanceEntry`; `InstanceSheet` must re-anchor its baseline so returning via `← <instance>` and saving an unsaved `apiKeyEnv` edit succeeds cleanly. Pinned in Task 5.

---

### Task 1: Registry `compaction_threshold`, `InputCeiling`, Provenance, Authored Accessors, and CLI

**Files:**
- Modify: `llm/registry/types.go`
- Modify: `llm/registry/schema.go`
- Modify: `llm/registry/load.go`
- Modify: `llm/registry/derive.go`
- Modify: `llm/registry/resolve.go`
- Modify: `llm/token_budget.go`
- Modify: `cmd/evener/models.go`
- Create: `llm/registry/compaction_threshold_test.go`
- Modify: `llm/client_token_budget_test.go`
- Modify: `cmd/evener/models_test.go`

**Interfaces:**
- Consumes: existing `registry.Caps`, `registry.Model`, `registry.Provider`, `registry.Layer`, `registry.Registry`.
- Produces:
  - `Caps.CompactionThreshold *int` (`toml:"compaction_threshold" json:"compaction_threshold,omitempty"`) in `llm/registry/types.go`
  - `func (c Caps) InputCeiling() int` in `llm/registry/types.go`
  - `const MinCompactionThreshold = 4096` and `const MaxTokenLimit = math.MaxInt32` in `llm/registry/schema.go`
  - `func ThinkingShapes() []string` in `llm/registry/schema.go`
  - `InstanceModel` extended in `llm/registry/types.go`:
    ```go
    type InstanceModel struct {
        ID                  string `json:"id"`
        Disabled            bool   `json:"disabled"`
        AuthoredOnly        bool   `json:"authored_only,omitempty"`
        Customized          bool   `json:"customized,omitempty"`
        ContextWindow       *int   `json:"context_window,omitempty"`
        CompactionThreshold *int   `json:"compaction_threshold,omitempty"`
    }
    ```
  - `func (r *Registry) AuthoredProvider(name string) (Provider, bool)` in `llm/registry/load.go`
  - `func (r *Registry) AuthoredModel(name, model string) (Model, bool)` in `llm/registry/load.go`
  - `func (r *Registry) IsAuthoredOnlyModel(instance, model string) bool` in `llm/registry/resolve.go`
  - `func (r *Registry) CheckAliasTarget(instance, model, aliasOf string) error` in `llm/registry/resolve.go`
  - `derive(c *Caps, m *Model, in deriveInput, prov map[string]string) []string` returning warnings in `llm/registry/derive.go`
  - `Resolved.Provenance` populated for `"Protocol"`, `"WireID"`, and `"AliasOf"` in `llm/registry/resolve.go`

- [ ] **Step 1: Write failing registry, token-budget, and CLI tests**

Create `llm/registry/compaction_threshold_test.go` and add cases in `llm/client_token_budget_test.go` and `cmd/evener/models_test.go` covering:
- `ParseConfig` / `MarshalConfig` / `WriteConfigFile` round-trip of `compaction_threshold = 250000` at provider, top-glob (`[models."*deepseek*"]`), provider-glob, and exact-row (`[providers.work.models."deepseek/deepseek-4.1-flash"]`) levels; nil `CompactionThreshold` omits the key.
- `ParseConfig` rejects `compaction_threshold = 0`, `-1`, `250`, `4095`, and `math.MaxInt32 + 1`, and rejects `context_window` / `max_input_tokens` / `max_output_tokens > math.MaxInt32`; accepts `4096` and `math.MaxInt32`.
- `(Caps).InputCeiling()` returns `0` when unset, `*ContextWindow` or `*MaxInputTokens` when one is set, and the smaller positive value when both are set.
- Layer precedence (`config/row` beats `live`, `config/glob`, `overlay`), `seedFromAlias` copying `CompactionThreshold` and retaining `target.Provenance["CompactionThreshold"]`, and `derive` clearing `CompactionThreshold` to `nil` (`prov["CompactionThreshold"] == "derived"`) with a warning in `Resolved.Warnings` when `*CompactionThreshold >= c.InputCeiling()` without failing `Load`.
- `Resolved.Provenance` recording `"Protocol"` (provider layer vs `"config/row"`), `"WireID"`, and `"AliasOf"`.
- `ThinkingShapes()` returning `[]string{"adaptive", "budget", "budget+effort", "level"}`.
- `AuthoredProvider`, `AuthoredModel` (returning deep clones so caller mutation cannot corrupt `r`), `CheckAliasTarget` (accepting valid same-instance and `<curated>/<id>` non-alias targets; rejecting self-alias, glob, unknown target, and chained two-hop alias), and `IsAuthoredOnlyModel` + `InstanceModels` summary (`Disabled`, `AuthoredOnly`, `Customized`, `ContextWindow`, `CompactionThreshold`) across implicit, explicit, live-overlaid (snapshot row hidden by live listing then re-added in config -> `AuthoredOnly == true`), and slashed/bracketed model IDs.
- `llm.ApplyTokenBudget`: with `ContextWindow = 1_000_000` and `CompactionThreshold = 250_000`, a `300_000`-token input request succeeds with `AdmittedOutput` unclamped by `250_000`.
- `cmd/evener`: `runModels` `list` includes `compact=250000` (`"; "`-joined before warnings) in `NOTES`, and `inspect` JSON carries `caps.compaction_threshold` and `provenance.CompactionThreshold`.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `go test ./llm/registry ./llm ./cmd/evener -run 'TestCompactionThreshold|TestInputCeiling|TestThinkingShapes|TestAuthored|TestCheckAliasTarget|TestInstanceModelsSummary|TestModels'`
Expected: FAIL (missing `CompactionThreshold`, `InputCeiling`, `ThinkingShapes`, `AuthoredProvider`, `AuthoredModel`, `CheckAliasTarget`, `IsAuthoredOnlyModel`).

- [ ] **Step 3: Implement registry, `llm`, and CLI changes**

- In `llm/registry/types.go`: add `CompactionThreshold *int` to `Caps`, `InputCeiling() int` on `Caps`, and summary fields on `InstanceModel`.
- In `llm/token_budget.go`: replace the inline smallest-limit calculation in `tokenSafetyReserve` with `caps.InputCeiling()`.
- In `llm/registry/schema.go`: add `MinCompactionThreshold = 4096`, `MaxTokenLimit = math.MaxInt32`, range checks in `validateCaps`, and `ThinkingShapes() []string`.
- In `llm/registry/load.go`: clone `CompactionThreshold` in `cloneCaps`; retain cloned user `*Layer` on `Registry` (`r.userLayer`) in `loadUserLayer`; implement `AuthoredProvider` and `AuthoredModel` returning cloned values (`cloneProvider` / `cloneModel`).
- In `llm/registry/derive.go`: update `derive` to return `warnings []string` (updating internal callers in `resolve.go` and `derive_test.go`), clamping `c.CompactionThreshold` to `nil` (`prov["CompactionThreshold"] = provDerived`) with a warning when `ceiling := c.InputCeiling(); ceiling > 0 && *c.CompactionThreshold >= ceiling`.
- In `llm/registry/resolve.go`:
  - Copy `CompactionThreshold` and preserve its provenance in `seedFromAlias`.
  - Record `prov["Protocol"]`, `prov["WireID"]`, and `prov["AliasOf"]` in `applyRowScalars` / `resolveLayersModeLive` (seeding `prov["Protocol"]` from the last provider layer on `rec` that set `Protocol`).
  - Implement `CheckAliasTarget(instance, model, aliasOf string) error` and `IsAuthoredOnlyModel(instance, model string) bool`.
  - Update `InstanceModels(instance string)` to snapshot live rows once under `r.liveMu.RLock()`, compute non-`LayerConfig` visible IDs once on `rec`, and populate `Disabled`, `AuthoredOnly`, `Customized`, `ContextWindow`, and clamped `CompactionThreshold` via a fast non-reflection scalar replay helper `modelSummary` modeled on `modelDisabled`.
- In `cmd/evener/models.go`: prepend `fmt.Sprintf("compact=%d", *res.Caps.CompactionThreshold)` to `notes` before `res.Warnings` and join with `"; "`.

- [ ] **Step 4: Run package suites to verify GREEN**

Run: `go test ./llm/registry ./llm ./cmd/evener`
Expected: PASS across all three packages with pristine output.

- [ ] **Step 5: Commit Task 1**

```bash
git status --short
git add llm/registry/ llm/token_budget.go llm/client_token_budget_test.go cmd/evener/models.go cmd/evener/models_test.go
git commit -m "$(cat <<'EOF'
feat(registry): add per-model compaction_threshold and fast model summaries

Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>
EOF
)"
```

---

### Task 2: Profile, `contextmgr` Compaction Math, `ContextMetrics`, and `SetModel` Event

**Files:**
- Modify: `agent/provider/profile.go`
- Modify: `agent/schema/context_metrics.go`
- Modify: `agent/events/payloads.go`
- Modify: `agent/internal/contextmgr/manager.go`
- Modify: `agent/internal/contextmgr/pressure.go`
- Modify: `agent/internal/contextmgr/status_profile.go`
- Modify: `agent/internal/contextmgr/compact.go`
- Modify: `agent/internal/contextmgr/strategy_session_log.go`
- Modify: `agent/context_metrics.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session_self_compact.go`
- Modify: `agent/session.go`
- Modify: `agent/provider/profile_test.go`
- Modify: `agent/internal/contextmgr/compact_test.go`
- Modify: `agent/session_compact_at_test.go`

**Interfaces:**
- Consumes (from Task 1): `Caps.CompactionThreshold *int`, `(Caps).InputCeiling() int`.
- Produces:
  - `func (p *Profile) CompactionThreshold() int` in `agent/provider/profile.go`
  - `ContextMetrics.CompactionThreshold int` in `agent/schema/context_metrics.go`
  - `func EffectiveWindow(prof *provider.Profile) (window int, assumed bool)` in `agent/internal/contextmgr/pressure.go`
  - `func (cm *Manager) SetExplicitCompactAt(fraction float64)` in `agent/internal/contextmgr/manager.go`
  - `func (cm *Manager) WarnThresholdFor(prof *provider.Profile) float64` and `func (cm *Manager) CheckpointThresholdFor(prof *provider.Profile) float64` in `agent/internal/contextmgr/pressure.go`
  - `events.ModelChangedData` extended in `agent/events/payloads.go`:
    ```go
    ContextUsed         int     `json:"context_used,omitempty"`
    ContextWindow       int     `json:"context_window,omitempty"`
    ContextPressure     float64 `json:"context_pressure,omitempty"`
    ContextAssumed      bool    `json:"context_assumed,omitempty"`
    CompactionThreshold int     `json:"compaction_threshold,omitempty"`
    ```

- [ ] **Step 1: Write failing `provider`, `contextmgr`, and `agent` tests**

Add tests in `agent/provider/profile_test.go`, `agent/internal/contextmgr/compact_test.go`, and `agent/session_compact_at_test.go` covering:
- `Profile.CompactionThreshold()`: returns `250_000` when valid; returns `0` when `p == nil`, `rowFactsInherited == true`, unset, or `>= p.res.Caps.InputCeiling()`.
- `thresholdsFor` / `MaybeCompact` / `SessionLogStrategy` / `OODAStrategy` on `ContextWindow = 1_000_000`, `CompactionThreshold = 250_000`: no fold at `240_000`, `WarnThresholdFor` fires at `200_000` (`0.20`), Layer 1 checkpoint + note elicitation fires at `250_000` (`0.25`), Layer 2 summarize fires at `263_889` (`0.25 * 0.95 / 0.90`).
- Small threshold edge case (`CompactionThreshold = 4096` on `ContextWindow = 1_048_576`): `0 < warn < checkpoint <= summarize < 1.0` (`warn` strictly positive, no nudge on zero tokens).
- Threshold above `DefaultCompactAt` (`CompactionThreshold = 920_000` on `1_000_000` with `CompactAtPercent == 0`): no checkpoint at `910_000`, checkpoint fires at `920_000`, `summarize` stays `0.95` (`950_000`); when `CompactAtPercent = 20` (`200_000`), tighter `200_000` wins and `ContextMetrics.CompactionThreshold` reports `200_000`.
- Unknown `ContextWindow` (`0`) with `CompactionThreshold = 250_000`: `EffectiveWindow` returns `(277778, true)`, `UnknownWindowInputTripwire` (`200_000`) does not fire at `210_000`, `WarningCodeContextWindowAssumed` is not emitted, checkpoint fires at `250_000`, and `Session.ContextStatus()`, `Session.Meta()`, `TurnModelSwitch`, `checkContextStatusInvariants`, and `TestContextStatusAllocationBudget` (`<= 2` allocs) all hold.
- `Session.SetModel`: switching between `1M` unthresholded and `1M` (`CompactionThreshold = 250_000`) at `260_000` used tokens emits `EventModelChanged` carrying the new `ContextUsed`, `ContextWindow`, `ContextPressure`, `ContextAssumed`, and `CompactionThreshold`, and records the threshold-aware switch-marker warning.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `go test ./agent/provider ./agent/internal/contextmgr ./agent -run 'TestProfile.*Compaction|TestMaybeCompact.*CompactionThreshold|TestCompactAt'`
Expected: FAIL (undefined methods/fields).

- [ ] **Step 3: Implement `Profile`, `contextmgr`, and `Session` changes**

- In `agent/provider/profile.go`: implement `CompactionThreshold() int`.
- In `agent/schema/context_metrics.go` & `agent/events/payloads.go`: add `CompactionThreshold` and `ModelChangedData` context fields.
- In `agent/internal/contextmgr/manager.go` & `pressure.go`:
  - Add `explicitCompactAt float64` and `thresholdScale float64` on `Manager`; add `SetExplicitCompactAt(fraction float64)` (calls `SetCompactAt(fraction)` and records `cm.explicitCompactAt = fraction`, called from `agent/session_init.go` when `CompactAtPercent > 0`); update `ApplyThresholdScale` to record `cm.thresholdScale = scale` in addition to scaling the three base fields.
  - Export `EffectiveWindow(prof *provider.Profile) (window int, assumed bool)` (`effectiveWindowOf` delegates to it).
  - Implement `thresholdsFor(prof *provider.Profile) layerThresholds`, `WarnThresholdFor(prof)`, and `CheckpointThresholdFor(prof)` per Spec §2 (`lead := min(nudgeLead, fraction*0.20)`, `warn = min(selfCompactNudgeAt, max(0, fraction-lead))`, `summarize = max(fraction, min(fraction*(summarizeBackstopAt/DefaultCompactAt), summarizeBackstopAt))`, scaled by `cm.thresholdScale` when `> 0 && != 1.0`).
  - Update `SetProfile` to reset `cm.assumedWindowDisclosed = false` when `(cm.profile.CompactionThreshold() > 0) != (profile.CompactionThreshold() > 0)`.
- In `agent/internal/contextmgr/status_profile.go`: populate `CompactionThreshold` on `schema.ContextMetrics` (`0` when `prof.CompactionThreshold() <= 0`, else `int(math.Round(th.checkpoint * float64(window)))`).
- In `agent/internal/contextmgr/compact.go` & `strategy_session_log.go`: update `assumptionStillGoverns` to include `&& current.CompactionThreshold() == 0` and route `unknownWindowTripwireFires` through `assumptionStillGoverns(captured, cm.profile)`; replace direct `cm.CheckpointThreshold` / `cm.SummarizeThreshold` reads in `MaybeCompact` and `SessionLogStrategy.ManageContext` with `th := cm.thresholdsFor(prof)`.
- In `agent/context_metrics.go`, `agent/session_self_compact.go`, and `agent/session.go`: update `budgetingWindow` to use `contextmgr.EffectiveWindow(profile)`; update `maybeElicitNoteBeforeCompaction` and `maybeNudgeSelfCompact` to use `CheckpointThresholdFor` / `WarnThresholdFor`; update `Session.SetModel` (`agent/session.go`) to check `CheckpointThresholdFor(nextProfile)`, format threshold-based switch warnings without synthetic window figures, and populate the five context fields on `events.ModelChangedData`.

- [ ] **Step 4: Run package suites to verify GREEN**

Run: `go test ./agent/provider ./agent/internal/contextmgr && go test ./agent -run 'TestCompactAt|TestContextStatus|TestModelSwitch|TestMaybeElicit|TestS2Cov_MaybeNudge'`
Expected: PASS (including serial `TestContextStatusAllocationBudget` and `TestContextStatusAllocationCeiling10000Turns`).

- [ ] **Step 5: Commit Task 2**

```bash
git status --short
git add agent/provider/profile.go agent/provider/profile_test.go agent/schema/context_metrics.go agent/events/payloads.go agent/internal/contextmgr/ agent/context_metrics.go agent/session_init.go agent/session_self_compact.go agent/session.go agent/session_compact_at_test.go
git commit -m "$(cat <<'EOF'
feat(agent): drive compaction layers and context status from per-model threshold

Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>
EOF
)"
```

---

### Task 3: AppWire Contracts, Hub `modelGet` / `modelEdit` RPCs, and TypeScript Codegen

**Files:**
- Modify: `appwire/types_protocol.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/types_auth_commands.go`
- Modify: `appwire/types_models_nav.go`
- Modify: `appwire/types_turn.go`
- Modify: `appwire/types_thread.go`
- Modify: `cmdutil/cmdutil.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Modify: `server/appwire_thread_projection.go`
- Modify: `cmd/evener-hub/app_instances.go`
- Create: `cmd/evener-hub/app_instances_models.go`
- Modify: `cmd/evener-hub/app_rpc.go`
- Modify: `appwire-client/typescript/types.gen.ts` (generated via `make generate`)
- Modify: `appwire/types_test.go`
- Create: `cmd/evener-hub/app_instances_models_test.go`
- Modify: `cmd/evener-hub/app_rpc_instance_broadcast_test.go`
- Modify: `cmd/evener-hub/app_rpc_test.go`

**Interfaces:**
- Consumes (from Tasks 1 & 2): `registry.InstanceModel` summary fields, `r.AuthoredProvider`, `r.AuthoredModel`, `r.IsAuthoredOnlyModel`, `r.CheckAliasTarget`, `registry.ThinkingFormats()`, `registry.ThinkingShapes()`, `ContextMetrics.CompactionThreshold`, `events.ModelChangedData` context fields.
- Produces:
  - `MethodEvenerInstanceModelGet = "evener/instance/modelGet"` and `MethodEvenerInstanceModelEdit = "evener/instance/modelEdit"` (`ScopeHub`, absent from `hostRequestMethods`)
  - `InstanceModelEntry` (`ID`, `Disabled`, `Customized`, `ContextWindow`, `CompactionThreshold`), `InstanceModelGetParams`, `InstanceModelEffective`, `InstanceModelAuthored`, `InstanceModelDetailResponse`, `InstanceModelEditParams`, `InstanceModelEditResponse` in `appwire/types_auth_commands.go`
  - `EvenerThread.CompactionThreshold int` (`appwire/types_thread.go`), `ModelDescriptor.CompactionThreshold *int` (`appwire/types_models_nav.go`), `ThreadModelChangedParams` context fields (`appwire/types_turn.go`)
  - `(*hubInstancesController).GetModel(params appwire.InstanceModelGetParams) (appwire.InstanceModelDetailResponse, error)` and `(*hubInstancesController).EditModel(params appwire.InstanceModelEditParams, out *appwire.InstanceModelEditResponse) error` in `cmd/evener-hub/app_instances_models.go`
  - Generated TypeScript types in `appwire-client/typescript/types.gen.ts`

- [ ] **Step 1: Write failing `appwire` and `cmd/evener-hub` tests**

Add wire round-trip tests in `appwire/types_test.go`, create `cmd/evener-hub/app_instances_models_test.go`, and update `app_rpc_instance_broadcast_test.go` / `app_rpc_test.go` covering:
- Wire JSON serialization of `InstanceModelEntry`, `InstanceModelDetailResponse`, `InstanceModelEditParams` (set/clear pairs), `EvenerThread.CompactionThreshold`, `ModelDescriptor.CompactionThreshold` (`cmdutil.ModelDescriptorFromResolved`), and `ThreadModelChangedParams`.
- `GetModel`: returns `Effective`, deterministic `Provenance` (`"catalog"`, `"overlay"`, `"live"`, `"provider"`, `"glob"`, `"config"`, `"alias"`, `"derived"`), `Authored`, `Disabled`, `DisabledLockstepTarget` on same-provider aliases, `AuthoredOnly`, `ThinkingFormats`, `ThinkingShapes`, and `Warnings`; remains readable when `WritesRefused` is true; refuses unknown instances, globs, and unknown unauthored models with `InvalidParams`.
- `EditModel` full lifecycle on both implicit (`anthropic`) and explicit (`work`) instances, including slashed/bracketed IDs (`deepseek/deepseek-4.1-flash`, `claude-opus-4-5[1m]`):
  - Setting `CompactionThreshold = 250_000`, `Protocol = "openai-chat"`, `InputModalities = ["text", "image"]`, `EffortValues = ["low", "high"]` persists exact `providers.toml` keys without touching base alias rows (`claude-opus-4-5`), preserves cached live sibling models across reload, and broadcasts `evener/auth/updated` with `OriginClientId`.
  - Hand-authored non-UI keys (`headers`, `cost`, `fields`, `chat_template_kwargs`) survive targeted `EditModel` and `ResetAll: true`.
  - Clearing all overrides removes `p.Models[model]` (`modelRowIsBare`) and, on a shadowed implicit instance with no other fields, removes `l.Providers[name]` (`providerIsBare`, `#1024`).
  - `Create: true` / `AuthoredOnly` / `RemoveCustom: true` symmetry (including when a live listing hides snapshot rows and on `oauth-openai-codex`); `RemoveCustom` refused on catalog/overlay/live rows.
  - Validation and rollback: `refuseModelEditSetClearPair` on all 14 pairs and mutually exclusive mode flags; empty slice refusal on `EffortValues` / `InputModalities`; `CheckAliasTarget` failure in memory before disk write; setting `CompactionThreshold >= ContextWindow` OR lowering `ContextWindow` below an existing `CompactionThreshold` rejected with `InvalidParams` and `providers.toml` left unchanged; `ExpectedEndpointFingerprint` verified when non-empty and allowed when empty on unkeyable hubs; `effectiveAccessChanged` calling `forgetCredentialRejection(name)` when the default model's protocol changes.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `go test ./appwire ./cmdutil ./internal/appprojector ./server ./cmd/evener-hub -run 'TestInstanceModel|TestInstances_Model'`
Expected: FAIL (missing wire types, controller methods, and RPC registrations).

- [ ] **Step 3: Implement wire types, projections, hub controller, RPCs, and codegen**

- Update `appwire/types_protocol.go`, `appwire/protocol.go`, `appwire/types_auth_commands.go`, `appwire/types_models_nav.go`, `appwire/types_turn.go`, `appwire/types_thread.go`.
- Update `cmdutil/cmdutil.go` (`ModelDescriptorFromResolved`), `internal/appprojector/appwire_projection.go` (`EventModelChanged` -> `ThreadModelChangedParams`), and `server/appwire_thread_projection.go` (`EvenerThread.CompactionThreshold`).
- In `cmd/evener-hub/app_instances.go`:
  - Update `listLocked` to use `r.AuthoredProvider(name)` (zero disk read) and reuse already-built `InstanceEntry` from `entries` when populating `AvailableProviders[i].Setup`.
  - Update `instanceModels` to copy `Customized`, `ContextWindow`, and `CompactionThreshold` directly from `r.InstanceModels(name)`.
  - Extend `writeAndReload` with an optional post-reload `validate func(*registry.Registry) error` callback sharing the single rollback path.
- Create `cmd/evener-hub/app_instances_models.go` implementing `GetModel`, `EditModel`, `refuseModelEditSetClearPair`, `normalizeModelProvenance`, `modelRowIsBare` (`reflect.DeepEqual(row, registry.Model{ID: row.ID})`), single-read layer cloning (`cloneLayer`), pre-write ceiling/alias checks, post-reload clamp validator, and `effectiveAccessChanged` -> `forgetCredentialRejection`.
- In `cmd/evener-hub/app_rpc.go`: extract generic `instanceWriteResult[T any](originClientID string, run func() (T, error)) (T, error)` (`instanceWrite` calls it) and register `MethodEvenerInstanceModelGet` and `MethodEvenerInstanceModelEdit`.
- Run `make generate` so `appwire-client/typescript/types.gen.ts` updates cleanly, and format it with Biome (`cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/types.gen.ts`) if needed.

- [ ] **Step 4: Run package suites and generated-freshness check to verify GREEN**

Run: `go test ./appwire ./cmdutil ./internal/appprojector ./server ./cmd/evener/... ./cmd/evener-hub && make lint-generated`
Expected: PASS across all packages and `lint-generated` clean.

- [ ] **Step 5: Commit Task 3**

```bash
git status --short
git add appwire/ cmdutil/ internal/appprojector/ server/ cmd/evener-hub/ appwire-client/typescript/types.gen.ts
git commit -m "$(cat <<'EOF'
feat(hub): add modelGet and modelEdit RPCs and compactionThreshold wire fields

Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>
EOF
)"
```

---

### Task 4: Shared `@evener/appwire-client` Store, `parseTokenCount`, and Session Chrome

**Files:**
- Modify: `appwire-client/typescript/displayFormat.ts`
- Modify: `appwire-client/typescript/reasoningEffort.ts`
- Modify: `appwire-client/typescript/index.ts`
- Modify: `appwire-client/typescript/model.ts`
- Modify: `appwire-client/typescript/transcriptItems.ts`
- Modify: `appwire-client/typescript/threadUsage.ts`
- Modify: `appwire-client/typescript/transcriptCache.ts`
- Modify: `appwire-client/typescript/reducer.ts`
- Modify: `appwire-client/typescript/state/mutation/testing.ts`
- Modify: `appwire-client/typescript/state/credentials/instances.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/statusFormat.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx`
- Modify: `appwire-client/typescript/displayFormat.test.ts`
- Modify: `appwire-client/typescript/threadModelFromCache.test.ts`
- Modify: `appwire-client/typescript/reducer.test.ts`
- Modify: `appwire-client/typescript/state/credentials/instances.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.test.tsx`

**Interfaces:**
- Consumes (from Task 3): generated wire types `InstanceModelEntry`, `InstanceModelGetParams`, `InstanceModelDetailResponse`, `InstanceModelEditParams`, `InstanceModelEditResponse`, `EvenerThread.compactionThreshold`, `ThreadModelChangedParams` context fields in `appwire-client/typescript/types.gen.ts`.
- Produces:
  - `export function parseTokenCount(raw: string): number | null` in `appwire-client/typescript/displayFormat.ts` (re-exported from `@evener/appwire-client`)
  - `EFFORT_TIERS` re-exported from `appwire-client/typescript/reasoningEffort.ts` and `appwire-client/typescript/index.ts`
  - `ThreadModel.compactionThreshold: number` (`0` when absent) in `appwire-client/typescript/model.ts`
  - `CredentialInstancesState.getModel(params: InstanceModelGetParams): Promise<InstanceModelDetailResponse>` and `CredentialInstancesState.editModel(params: Omit<InstanceModelEditParams, "originClientId">, options?: { onSuperseded?: (response: InstanceModelEditResponse) => void }): Promise<{ applied: boolean; detail: InstanceModelDetailResponse | null }>` in `appwire-client/typescript/state/credentials/instances.ts`
  - `export function effectiveContextStatus(model: Pick<ThreadModel, "contextUsed" | "contextWindow" | "contextPressure" | "compactionThreshold">)` in `cmd/evener-hub/frontend/src/panes/session/chrome/statusFormat.ts`

- [ ] **Step 1: Write failing client store, token parser, reducer, and session chrome tests**

Add unit and component tests in `displayFormat.test.ts`, `threadModelFromCache.test.ts`, `reducer.test.ts`, `state/credentials/instances.test.ts`, `StatusRow.test.tsx`, and `DetailsPanel.test.tsx` covering:
- `parseTokenCount`: `"250000"` -> `250000`, `" 250,000 "` -> `250000`, `"250k"` -> `250000`, `"1.13k"` -> `1130`, `"0.29M"` -> `290000`, `"1.5m"` -> `1500000`, `"2147483647"` -> `2147483647`; returns `null` for `""`, `"0"`, `"-5k"`, `"1.5"` (fractional without suffix), `"1.2345k"` (>3 decimals for K), `"2147483648"`, `"3000m"`, `"abc"`.
- `ThreadModel` hydration (`transcriptItems.ts`), `transcriptCache` round-trip (`0` default on cached sessions), and `reducer.ts` `thread/model/changed` updating `contextUsed`, `contextWindow`, `contextPressure`, `contextAssumed`, and `compactionThreshold` live in both directions (`0 -> 250000` and `250000 -> 0`).
- `CredentialInstancesStore`: `getModel` guards stale connections; `editModel` arms `noteLocalMutation(undefined)` so own `evener/auth/updated` echo is consumed without firing a foreign refetch; `keepRefreshedModels` preserves `customized`, `contextWindow`, `compactionThreshold`, newly added custom models, and `removeCustom` deletions when a `refreshModels` landed in flight.
- `StatusRow` and `DetailsPanel`: with `contextUsed = 200_000`, `contextWindow = 1_000_000`, `compactionThreshold = 250_000`, both surfaces compute `80%` and `attention` tone, `<VisuallyHidden><meter>` and `<Meter>` both have `value=200000` and `max=250000`, `DetailsPanel` shows the `(1M window)` parenthetical when `!contextAssumed` and `(window unknown)` with un-prefixed `remaining` (`50K`) when `contextAssumed` is true; when `compactionThreshold == 0`, output is unchanged.

- [ ] **Step 2: Run targeted tests to verify RED**

Run: `make test-api-package` and `cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/StatusRow.test.tsx src/panes/session/chrome/DetailsPanel.test.tsx`
Expected: FAIL (missing exports, store methods, and threshold gauge behavior).

- [ ] **Step 3: Implement `@evener/appwire-client` and session chrome changes**

- Implement base-10 string-shifting `parseTokenCount` in `appwire-client/typescript/displayFormat.ts` and export it from `appwire-client/typescript/index.ts`.
- Re-export `EFFORT_TIERS` from `appwire-client/typescript/reasoningEffort.ts` and `appwire-client/typescript/index.ts`.
- Thread `compactionThreshold: number` through `model.ts`, `transcriptItems.ts`, `threadUsage.ts`, `transcriptCache.ts`, `state/mutation/testing.ts`, and `reducer.ts` (`thread/model/changed`, updating reducer snapshots if needed via vitest).
- Implement `getModel` and `editModel` in `appwire-client/typescript/state/credentials/instances.ts` (generalizing `applyMutation` to carry the full response while passing `response.list` to `listState`, and updating `WrittenModel` / `keepRefreshedModels` to upsert full `InstanceModelEntry` or filter out removed IDs).
- Implement `effectiveContextStatus` in `statusFormat.ts` and wire it into `StatusRow.tsx` (both `<VisuallyHidden><meter>` and `<Meter>`) and `DetailsPanel.tsx`.
- Format touched TypeScript files with Biome: `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript src/panes/session/chrome`.

- [ ] **Step 4: Verify GREEN across package tests, vitest, and typecheck**

Run: `make test-api-package && (cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/StatusRow.test.tsx src/panes/session/chrome/DetailsPanel.test.tsx && npm run typecheck && npm run lint)`
Expected: PASS with zero typecheck or Biome errors.

- [ ] **Step 5: Commit Task 4**

```bash
git status --short
git add appwire-client/typescript/ cmd/evener-hub/frontend/src/panes/session/chrome/
git commit -m "$(cat <<'EOF'
feat(client): wire per-model store mutations, token parser, and compaction gauge

Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>
EOF
)"
```

---

### Task 5: `InstanceSheet` Model Rows, `InstanceModelEditor` Drill-In UI, and Evergreen Docs

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceModelEdit.ts`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceModelEditor.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceSheet.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceSheet.module.css`
- Modify: `docs/llm-providers.md`
- Modify: `docs/product/subsystems.md`
- Modify: `docs/design/context.md`
- Create: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/instanceModelEdit.test.ts`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/InstanceSheet.models.test.tsx`

**Interfaces:**
- Consumes (from Task 4): `parseTokenCount`, `formatTokenCount`, `EFFORT_TIERS`, `effortLabel`, `credentialsStore.getState().getModel`, `credentialsStore.getState().editModel`, `PROTOCOL_OPTIONS` and `SURFACE_OPTIONS` (`instanceEdit.ts`), barrelled widgets from `../../../../widgets` (`Sheet`, `Switch`, `Button`, `Chip`, `FormRow`, `Input`, `Select`, `SegmentedControl`, `Disclosure`, `InlineNotice`, `ConfirmDialog`, `Skeleton`, `useFocusRehome`, `useToasts`).
- Produces:
  - Pure draft/diff/validation helpers in `instanceModelEdit.ts`: `ModelDraft`, `seedModelDraft(detail)`, `hasAuthoredOverrides(authored)`, `validateModelDraft(draft, detail, existingModels, mode)`, `modelEditParams(name, initial, draft, mode, expectedEndpointFingerprint)`
  - `InstanceModelEditor` component in `InstanceModelEditor.tsx`
  - Updated `InstanceSheet` with enriched model rows, `+ Add custom model`, conditionally mounted `InstanceModelEditor`, `scrollTop` preservation, focus transitions, `Escape` back vs `[×]` close, connection-draft re-anchoring on model save, and sibling `ConfirmDialog` for `Remove custom model`
  - Updated evergreen documentation in `docs/llm-providers.md`, `docs/product/subsystems.md`, and `docs/design/context.md`

- [ ] **Step 1: Write failing pure model-form tests and `InstanceSheet` UI journey tests**

Create `instanceModelEdit.test.ts` and extend `InstanceSheet.models.test.tsx` covering:
- `instanceModelEdit.ts`: seeding inherited (`null` / `""`) vs authored fields; `hasAuthoredOverrides` detecting `false` booleans as overrides; generating minimal `InstanceModelEditParams` (only changed fields + `clear*` flags when reset); client validation (`compactionThreshold >= 4096` and `< hardCeiling` of drafted/effective `contextWindow` and `effective.maxInputTokens`; non-empty `effortValues` when non-null; `inputModalities` always retaining `"text"` + non-UI baseline modalities when toggled; `create` mode rejecting empty, `*`, or duplicate `Model ID`).
- `InstanceSheet.models.test.tsx`:
  - Overview `Models` rows render `<Switch label={row.id}>`, `<Chip kind="customized">` when `row.customized`, formatted summary (`1M ctx · compact 250K`), `Configure ›` (`aria-label="Configure <id>"`), and `+ Add custom model`.
  - Clicking `Configure ›` calls `getModel`, mounts `InstanceModelEditor` while hiding the overview wrapper, moves focus to `← <instance>`, displays inherited placeholders with provenance (`Default: 900K (90% of 1M window)`), and auto-opens `Routing & wire overrides` only when customized or in `create` mode.
  - Editing `Auto-compact threshold` to `"250k"`, toggling `EFFORT_TIERS` and `Image`/`PDF` modalities, and clicking `Save` sends `editModel` with `compactionThreshold: 250000`, updates the baseline, and enables per-field `Reset` and `Reset all overrides`.
  - Pressing `Escape` inside `InstanceModelEditor` returns to the overview (keeping `InstanceSheet` open, restoring focus to `Configure <id>` and restoring `OverlayPanel`'s `.body` `scrollTop`), whereas clicking `Sheet`'s `Close` button dismisses `InstanceSheet`.
  - Unsaved connection `draft` edit (`apiKeyEnv`) on `InstanceSheet` survives drilling into the default model, changing `Protocol` (`endpointFingerprint` moves), saving the model, returning via `← <instance>`, and clicking `Save` on the overview without triggering `instance-replaced`.
  - `+ Add custom model` creates a row (`create: true`), and `Remove custom model` opens the sibling `ConfirmDialog` outside `<Sheet>` and calls `editModel({ ..., removeCustom: true })`, returning to the overview.
  - Respects `writesRefused` (disabling `Save`, `Reset all overrides`, `+ Add custom model`, `Remove custom model` while keeping `getModel` readable) and `renameGap` (`refuseDuringRename`).

- [ ] **Step 2: Run targeted vitest files to verify RED**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/credentials/instanceModelEdit.test.ts src/panes/settings/sections/credentials/InstanceSheet.models.test.tsx`
Expected: FAIL (`instanceModelEdit` missing and new `InstanceSheet` assertions failing).

- [ ] **Step 3: Implement `instanceModelEdit.ts`, `InstanceModelEditor.tsx`, `InstanceSheet.tsx`, CSS, and evergreen docs**

- Implement pure form logic in `instanceModelEdit.ts` using `parseTokenCount`, `formatTokenCount`, `EFFORT_TIERS`, `effortLabel`, `PROTOCOL_OPTIONS`, and `SURFACE_OPTIONS`.
- Implement `InstanceModelEditor.tsx` and update `InstanceSheet.tsx` + `InstanceSheet.module.css`:
  - Compose strictly from `src/widgets` barrel; bare `Switch` for `Enabled`; CSS class names free of `WIDGET_CONCEPTS` substrings (`.modelRow`, `.modelSummary`, `.subviewRoot`, `.subviewNav`, `.fieldInline`, `.effortGroup`, `.modalityGroup`).
  - Keep overview wrapper mounted with `hidden={activeModelView !== null}`, conditionally mount `{activeModelView !== null && <InstanceModelEditor ... />}`, preserve `.body` `scrollTop` across transitions via `useLayoutEffect`, manage focus transitions + `useFocusRehome`, handle `Escape` (`event.preventDefault(); backToOverview()`), render `Remove custom model`'s `ConfirmDialog` as a sibling of `<Sheet>`, and re-anchor `InstanceSheet`'s `seededIdentity` / `seededFingerprint` / untouched `initial` & `draft` fields when its own `editModel` lands.
- Update evergreen documentation in `docs/llm-providers.md`, `docs/product/subsystems.md` (**S02**, **S15**, **S16**), and `docs/design/context.md` (zero emdashes, present tense).
- Run Biome autofix: `cd cmd/evener-hub/frontend && npx biome check --write src/panes/settings/sections/credentials`.

- [ ] **Step 4: Run vitest, typecheck, Biome, `ds-audit.mjs`, and `lint-repo-audit` to verify GREEN**

Run: `(cd cmd/evener-hub/frontend && npx vitest run src/panes/settings/sections/credentials/ && npm run typecheck && npm run lint && node scripts/ds-audit.mjs) && make lint-repo-audit`
Expected: PASS with zero bypasses, zero type errors, and zero lint errors.

- [ ] **Step 5: Commit Task 5**

```bash
git status --short
git add cmd/evener-hub/frontend/src/panes/settings/sections/credentials/ docs/llm-providers.md docs/product/subsystems.md docs/design/context.md
git commit -m "$(cat <<'EOF'
feat(web): add per-model settings editor in InstanceSheet and update docs

Co-authored-by: Evener ebbb6bb (vertex-arc/gemini-4-carbon) <034w1grp6Okm7Bf5Xh5zlw@magic-kingdom>
EOF
)"
```
