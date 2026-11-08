# Memory Disable Option Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose the existing per-session memory opt-out as a `memory_enabled` launch option settable from the web UI and the native app (global/project defaults plus per-launch override), with the pre-session skill catalog hiding gardening-memory when memory is off.

**Architecture:** One new boolean launch option flows through the existing schema-driven launch-config system: `launchconfig.Layer` → merge → wire (`appwire.LaunchConfigLayer`, TS types generated) → `LaunchOptionSchema` row (both clients render it generically) → `ToArgs` argv → `evener serve --disable-memory` (flag already implemented; engine untouched). The hub's fresh-spawn skill catalog derives `MemorySaveAvailable` from the resolved launch. The native new-session sheet adds one curated Memory control, gated on the hub's schema offering the field; the web needs no code.

**Tech Stack:** Go (hub, launchconfig, appwire), generated TypeScript types (internal/appwirets, `make generate`), React Native (mobile-native), React (web frontend, unchanged).

**Spec:** docs/superpowers/specs/2026-11-08-memory-disable-option-design.md — approved by Jesse 2026-11-08. The plan argues from the spec.

## Global Constraints

- Naming (Jesse's pick): TOML key `memory_enabled`, wire/TS field `memoryEnabled`, UI label "Memory", default On. The daemon flag `--disable-memory`, `SessionConfig.DisableMemory` and saved meta `disable_memory` keep their spelling; `launchconfig.ToArgs` is the single negation.
- `--disable-memory` is a stdlib Go Bool flag: the only argv spellings that parse are bare `--disable-memory` (=true) and the equals form `--disable-memory=false|true`. Never emit it as a two-element flag/value pair (Go would parse the bare flag then stop at the stray value).
- ToArgs emits in both directions when the field is set (mirroring `api_log`'s comment): effective `memoryEnabled` false → `--disable-memory=true`, true → `--disable-memory=false`, unset → nothing. This also keeps `TestEveryLayerFieldReachesArgs` green with no exclusion.
- Schema row exact values: Field `memory_enabled`, WireField `memoryEnabled`, Label "Memory", Kind boolean, Group `LaunchGroupAgent`, DefaultableLayers `[global, project]`, PerLaunch true, DriverSupport evener-only, BuiltinDefaultBool `&builtinBoolTrue`. Description (user-visible in both UIs): "Evener's cross-session memory. Off starts the session with no memory tools, no memory index or reminders in its context, and no gardening-memory skill. Stored memory files are kept; a resumed session keeps the state it was created with."
- No engine/agent changes, no new RPC methods, no live toggle for running sessions, resume behavior unchanged (launch layers carry only `api_log` on resume; stickiness comes from session meta).
- Tests assert behavior, never human-readable phrasing. No sprint/round/coverage labels in test names. Comments state invariants, not history.
- TypeScript types change only via `make generate` (types.gen.ts is generated; never hand-edit).
- Biome runs from `cmd/evener-hub/frontend` (web) or `mobile-native` (native), never from the repo root. Heavy local gates (`make vet`, `make lint`, `make test-native`) run one at a time.
- Work happens in an isolated worktree (repo rule). A Linear ticket is expected by the ops skill but Linear tooling is unavailable in evener sessions (checked 2026-11-08); proceed and note the missing ticket in the PR description.

## Review Focus

The spec implies these failure modes; each line's test lives in the named task.

1. A per-launch "On" must beat a disabling global default (and emit `--disable-memory=false`, not nothing) — Task 1 resolver test, Task 2 hub spawn test.
2. Wrong argv spelling breaks flag parsing at spawn (Bool flags need the equals form) — Task 1 ToArgs test asserts the exact single token.
3. A newer phone against an older hub shows a Memory toggle that silently does nothing — Task 3 gates the row on the hub schema and tests both sides.
4. The pre-session composer still offers gardening-memory for a memory-off launch (both per-launch and via a global default) — Task 2 catalog tests.
5. A remembered start or saved draft silently drops the memory choice — Task 3 owned-overrides round-trip tests.

---

### Task 0: Worktree and plan bookkeeping

**Files:**
- Create: worktree branch `memory-disable-option` (via `manage_worktree` create, default base HEAD)
- Commit on the branch: this plan and the design doc from the main checkout (they are currently uncommitted there; move, don't copy, so the main checkout keeps no stray files)

**Interfaces:**
- Produces: a clean worktree at `../evener-memory-disable-option` (or the managed worktree path) containing the plan and spec.

- [ ] **Step 1: Create the worktree**

`manage_worktree` create name `memory-disable-option` (default base is current HEAD). Check `ls docs/superpowers/plans/` in the fresh tree: if committed plans from past features are absent (they are deleted in completing PRs per the product README), commit this plan now and delete it in the final PR-prep commit (Task 5), matching that convention.

- [ ] **Step 2: Move plan and spec into the worktree and commit**

`git add docs/superpowers/specs/2026-11-08-memory-disable-option-design.md docs/superpowers/plans/2026-11-08-memory-disable-option.md` (copy them from the main checkout first if the worktree was created before they existed) and commit: `git commit -m "docs(specs): memory disable launch option design and plan"`.

### Task 1: launchconfig field, schema row, argv, wire, generated TS

**Files:**
- Modify: `cmd/evener-hub/internal/launchconfig/types.go` (Layer struct: `MemoryEnabled *bool` with `toml:"memory_enabled,omitempty"`, placed after `NonInteractive`)
- Modify: `cmd/evener-hub/internal/launchconfig/merge.go` (one `mergePtr` line after the NonInteractive merge)
- Modify: `cmd/evener-hub/internal/launchconfig/wire.go` (one `ClonePointer` line each in FromWire and ToWire)
- Modify: `cmd/evener-hub/internal/launchconfig/runtime_defaults.go` (`boolFields` gains `"memoryEnabled": func(l *Layer) **bool { return &l.MemoryEnabled }`)
- Modify: `cmd/evener-hub/internal/launchconfig/schema.go` (row after `agent`, values in Global Constraints)
- Modify: `cmd/evener-hub/internal/launchconfig/args.go` (emission near the other bools)
- Modify: `appwire/types_auth_commands.go` (`LaunchConfigLayer.MemoryEnabled *bool` with `json:"memoryEnabled,omitempty"`)
- Regenerate: `appwire-client/typescript/types.gen.ts` via `make generate`
- Test: `cmd/evener-hub/internal/launchconfig/memory_option_test.go` (new)
- Test: `cmd/evener-hub/internal/launchconfig/schema_test.go` (want list)

**Interfaces:**
- Consumes: nothing new (the reflective suites in `layer_fields_test.go` and `schema_test.go` enforce the rest automatically once the field exists).
- Produces: `Layer.MemoryEnabled *bool` (TOML `memory_enabled`); schema row `memory_enabled`/`memoryEnabled`; `appwire.LaunchConfigLayer.MemoryEnabled *bool` (JSON `memoryEnabled`); TS `memoryEnabled?: boolean`; ToArgs renders `--disable-memory=true` / `--disable-memory=false` as one argv token when set, nothing when nil.

- [ ] **Step 1: Write the failing tests**

New file `memory_option_test.go`, two tests (follow `api_log_test.go`'s style):

1. `checkToArgs_MemoryEnabled` (tri-state table): Effective `Layer{Model: "openai/gpt-5"}` + `MemoryEnabled` nil → args exactly `[--model openai/gpt-5 --daemon-idle-timeout 0s]` (no memory token); `MemoryEnabled = &true` → gains the single token `--disable-memory=false`; `MemoryEnabled = &false` → gains `--disable-memory=true`. Assert with `slices.Contains(args, "--disable-memory=false")` style checks plus a negative `!slices.ContainsFunc` for any arg containing "memory" when nil — this pins the equals-form single token (Review Focus 2).
2. `TestResolveMemoryEnabledLayerPrecedence`: in a temp launch root, write a global `launch.toml` containing literally `memory_enabled = false` (writing the bytes by hand pins the user-facing key spelling), plus a global model so resolve is valid. `Resolve(root, cwd, Layer{})` → `Effective.MemoryEnabled` non-nil false. Then `Resolve(root, cwd, Layer{MemoryEnabled: &true})` → non-nil true, and `Provenance["memory_enabled"] == LayerLaunch` (Review Focus 1).

In `schema_test.go`, add `"memory_enabled"` to the `want` slice in `checkLaunchOptionSchema_FieldCoverage` (the bidirectional check fails without it).

- [ ] **Step 2: Run to verify red**

Run: `go test ./cmd/evener-hub/internal/launchconfig/`
Expected: compile failure (`MemoryEnabled undefined`) — Go's red. The reflective suites would also fail once the field exists but the seams don't.

- [ ] **Step 3: Implement**

All values are pinned in Global Constraints: the Layer field, the `mergePtr` line, the two `ClonePointer` wire lines, the `boolFields` entry, the schema row, and in `args.go`:

```go
// Emitted in both directions so the spawned argv names the resolved choice
// and a launch layer can re-enable over a disabling default; an unset value
// emits nothing (evener's own default, which is enabled). The flag spells the
// negation, and as a Bool flag it takes only the single equals-form token.
if e.MemoryEnabled != nil {
    out = append(out, "--disable-memory="+strconv.FormatBool(!*e.MemoryEnabled))
}
```

Then add `MemoryEnabled *bool` (json `memoryEnabled,omitempty`) to `appwire.LaunchConfigLayer` (place after `NonInteractive`), and run `make generate`.

- [ ] **Step 4: Run to verify green**

Run: `go test ./cmd/evener-hub/internal/launchconfig/`
Expected: PASS, including `TestEveryLayerFieldHasASchemaRow`, `TestEveryLayerFieldSurvivesMerge`, `TestEveryLayerFieldRoundTripsTheWire`, `TestEveryLayerFieldReachesArgs` for the new field (no exclusions needed), and the schema coverage check.

Run: `go build ./...` — Expected: clean.
Run: `git diff --stat` after `make generate` — Expected: `appwire-client/typescript/types.gen.ts` gained `memoryEnabled?: boolean` on `LaunchConfigLayer`.

- [ ] **Step 5: Commit**

`git add -A cmd/evener-hub/internal/launchconfig appwire/types_auth_commands.go appwire-client/typescript/types.gen.ts` then
`git commit -m "feat(launchconfig): memory_enabled launch option through layers, wire and argv"`

### Task 2: hub — spawn seam, slash-catalog derivation, resume pin

**Files:**
- Modify: `cmd/evener-hub/app_spawn_slashcatalog.go` (`freshSpawnSkillAvailability` signature)
- Modify: `cmd/evener-hub/app_spawn_slashcatalog_availability_test.go` (guard rewrite + catalog cases)
- Test: `cmd/evener-hub/app_threadlifecycle_memory_test.go` (new)

**Interfaces:**
- Consumes: Task 1's `Layer.MemoryEnabled` and `ToArgs` token.
- Produces: `freshSpawnSkillAvailability(eff launchconfig.Layer) skill.SessionAvailability` with `MemorySaveAvailable: eff.MemoryEnabled == nil || *eff.MemoryEnabled`; call site passes `preview.resolved.Effective`.

- [ ] **Step 1: Write the failing tests**

In `app_threadlifecycle_memory_test.go` (follow `TestThreadStart_LaunchOverridesApplied` for the fake-spawner capture and `TestHubRPCThreadStartUsesGlobalLaunchDefaultModel` for the saved-global-layer setup):

1. `TestThreadStart_MemoryOverrideReachesSpawn`: `ThreadStart` with `LaunchOverrides{MemoryEnabled: new(false)}` → captured `hubcore.SpawnRequest.Resolved.Effective.MemoryEnabled` non-nil false, and `launchconfig.ToArgs(req.Resolved)` contains `--disable-memory=true`.
2. `TestThreadStart_MemoryEnabledOverGlobalDefault`: global layer `memory_enabled = false` saved via `newHubLaunchController(launchRoot, false).SetLayer`, then `ThreadStart` with `LaunchOverrides{MemoryEnabled: new(true)}` → effective true; `ToArgs` contains `--disable-memory=false` and not `--disable-memory=true` (Review Focus 1).
3. `TestResumeRequestCarriesNoMemoryLaunchLayer`: build a `hubcore.WebConfig` with a `Past` index holding one entry (follow existing `resumeRequestForConfig` callers' fixtures), call `resumeRequestForConfig(cfg, id)` → `req.Resolved.Effective.MemoryEnabled == nil` (pin: resume leaves memory to session meta).

In `app_spawn_slashcatalog_availability_test.go`:

4. Rewrite `TestFreshSpawnSkillAvailabilityHasNoLaunchMemoryControl` per its own comment's promise: the reflection loop and argv/env scans now assert the opposite invariant — `freshSpawnSkillAvailability(Layer{MemoryEnabled: &false})` reports `MemorySaveAvailable` false, `Layer{}` and `&true` report true; `ToArgs` of a disabled effective layer contains the `--disable-memory=true` token.
5. Add catalog behavior cases to the `TestHubSpawnSlashCatalogOffersGardeningMemoryToFreshRoot` file (same `writeSlashCatalogSkill` fixture): params with `LaunchOverrides{MemoryEnabled: new(false)}` → no offered gardening-memory entry (Review Focus 4); and a saved global `memory_enabled = false` layer → same. Keep the fresh-root case green.

- [ ] **Step 2: Run to verify red**

Run: `go test ./cmd/evener-hub/ -run 'MemoryOverride|MemoryEnabled|ResumeRequest|SlashCatalog'`
Expected: compile failure on the new `freshSpawnSkillAvailability` signature; after that, the availability assertions fail.

- [ ] **Step 3: Implement**

Change `freshSpawnSkillAvailability()` to take the effective layer and derive (Interfaces block); update the one call site. No other hub code changes: `prepareLocalSpawn` already merges `LaunchOverrides` (Task 1's wire field flows through `launchconfig.FromWire`), and `spawn.go` already renders argv via `ToArgs`.

- [ ] **Step 4: Run to verify green**

Run: `go test ./cmd/evener-hub/ -run 'MemoryOverride|MemoryEnabled|ResumeRequest|SlashCatalog'`
Expected: PASS. Also run `go test ./cmd/evener-hub/ -run 'ThreadStart_LaunchOverrides|ThreadStartUsesGlobal'` to confirm neighbors.

- [ ] **Step 5: Commit**

`git add cmd/evener-hub/app_spawn_slashcatalog.go cmd/evener-hub/app_spawn_slashcatalog_availability_test.go cmd/evener-hub/app_threadlifecycle_memory_test.go` then
`git commit -m "feat(hub): spawn and slash catalog honor the memory launch option"`

### Task 3: native — new-session Memory control, schema gate, remembered starts

**Files:**
- Modify: `mobile-native/src/conversation/services/newSession.ts` (service gains `launchSchema`)
- Modify: `mobile-native/src/newSession/useLaunchDefaults.ts` (`LaunchDefaults` pick + `FIELDS` gain `memoryEnabled`)
- Create: `mobile-native/src/newSession/useLaunchSchema.ts`
- Modify: `mobile-native/src/newSession/newSessionContext.tsx` (context carries `launchSchema`)
- Modify: `mobile-native/src/newSession/NewSessionSheet.tsx` (read + provide it beside `useSheetLaunchDefaults`)
- Modify: `mobile-native/src/newSession/MoreOptions.tsx` (Memory row, gated)
- Modify: `mobile-native/src/newSession/launchSetup.ts` (`ownedOverrides` keeps `memoryEnabled`)
- Modify: `mobile-native/src/newSession/launchMemory.ts` (`toSetup` type-check pair list)
- Test: `mobile-native/src/newSession/MoreOptions.test.tsx`, `launchSetup.test.ts`, `launchMemory.test.ts`, `sheetLaunchDefaults.test.tsx`

**Interfaces:**
- Consumes: Task 1's TS `memoryEnabled?: boolean` and the schema row (`wireField: "memoryEnabled"` arrives via `evener/launch/schema`).
- Produces: `NewSessionService.launchSchema(host): Promise<LaunchOptionSchemaResponse>` (via `hostRequest(client, host, "evener/launch/schema", {})`, routing like `resolveLaunch`); context field `launchSchema: LaunchOptionSchemaResponse | null`; MoreOptions renders the Memory segmented control only when `launchSchema?.options` contains `wireField === "memoryEnabled"`.

- [ ] **Step 1: Write the failing tests**

1. `MoreOptions.test.tsx` (follow the AccessPicker mount pattern with `store.setState({ launchOverrides })`): with a context schema that offers `memoryEnabled`, the Memory segmented control sets `launchOverrides.memoryEnabled` to `false`/`true` and back to unset via Default; the footer names the hub default from `launchDefaults.memoryEnabled` ("on"/"off" words are existing footer behavior — assert the store value, not the sentence). With a schema lacking the field, no Memory control renders and `launchOverrides` is untouched (Review Focus 3).
2. `launchSetup.test.ts` / `launchMemory.test.ts`: `ownedOverrides` keeps `memoryEnabled: false` through the remembered-start round trip (`toSetup` accepts and returns it; a non-boolean value is dropped) (Review Focus 5).
3. `sheetLaunchDefaults.test.tsx`: the defaults read carries `memoryEnabled` from the resolve response's effective layer.

- [ ] **Step 2: Run to verify red**

Run: `cd mobile-native && sh ../scripts/native/run-tests.sh src/newSession`
Expected: FAIL — no Memory control, `launchSchema` not in context, `ownedOverrides` drops the field.

- [ ] **Step 3: Implement**

All placements follow sibling patterns: `launchSchema` service method routes like `resolveLaunch`; `useLaunchSchema.ts` mirrors `useLaunchDefaults.ts` (`useHostRead` keyed on the same `evener/launch/updated` revision); `NewSessionSheet` provides it through the same context that carries `launchDefaults`; `MoreOptions` extends `Field` with `"memoryEnabled"`, widens `Setting.values` to `(string | number | boolean)[]`, adds `{ field: "memoryEnabled", label: "Memory", values: [true, false] }` to `SETTINGS`, maps booleans in `spoken()` to on/off words, and renders the row only when the schema offers it; `launchSetup.ownedOverrides` and the `toSetup` kind-check list gain `memoryEnabled` (boolean). The store's `start` path needs nothing: it already forwards `launchOverrides` wholesale.

- [ ] **Step 4: Run to verify green**

Run: `cd mobile-native && sh ../scripts/native/run-tests.sh src/newSession` → PASS
Run: `cd mobile-native && npm run check` → clean (tsc covers the TS types including generated `memoryEnabled`).
Run: `cd mobile-native && npx biome check --write src/newSession src/conversation/services/newSession.ts` → no remaining violations.

- [ ] **Step 5: Commit**

`git add mobile-native/src` then
`git commit -m "feat(native): Memory launch option in new session More options"`

### Task 4: evergreen docs

**Files:**
- Modify: `docs/product/memory.md` ("Per-session opt-out" section)
- Modify: `docs/product/subsystems.md` (S24 row)
- Modify: `mobile-native/DESIGN.md` (More options rows around lines 459 and 469)

**Interfaces:**
- Consumes: the shipped behavior from Tasks 1-3.
- Produces: docs describing current behavior, no dated narratives.

- [ ] **Step 1: Update the three docs**

`memory.md` "Per-session opt-out": add that the opt-out is settable from the web Launch settings / spawn Advanced options and the native Launch defaults / new-session More options as the `memory_enabled` launch option (global and project defaults plus per-launch), naming the two spellings and that `ToArgs` is the negation seam. "There is no live toggle" stays true. Keep present tense, no PR history.

`subsystems.md` S24: add the launch option and both client surfaces to the row's affected-surfaces text.

`mobile-native/DESIGN.md`: add Memory to the More options field lists (both the spec list and the behavior paragraph), matching how sandbox/Access and the existing three settings are described.

- [ ] **Step 2: Verify**

Run: `make docs-check` → PASS.

- [ ] **Step 3: Commit**

`git add docs/product/memory.md docs/product/subsystems.md mobile-native/DESIGN.md` then
`git commit -m "docs(product): memory launch option across hub and clients"`

### Task 5: gates, simplify, PR, shepherd

**Files:**
- Delete: `docs/superpowers/plans/2026-11-08-memory-disable-option.md` (repo rule: the completing PR deletes the implementation plan)
- Keep: the design doc (specs persist)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Targeted gates (serially, one at a time)**

Run each and expect PASS/clean: `go test ./cmd/evener-hub/internal/launchconfig/`; `go test ./cmd/evener-hub/ -run 'Memory|SlashCatalog|ThreadStart|Resume'`; `make test-api-package` (generated TS types + shared package); `cd mobile-native && npm run check && npm run lint`; `make docs-check`; `make vet`; `make lint` (includes generated-output freshness — catches a missed `make generate`).

- [ ] **Step 2: simplify-code pass**

Run the /simplify-code skill over the branch diff; apply what it finds; re-run the touched areas' tests.

- [ ] **Step 3: PR prep and push**

Delete the plan file, commit (`chore: remove completed implementation plan`), push the branch, open the PR against prime-radiant-inc/evener. PR description: links the design doc, states the missing Linear ticket (tooling unavailable), names the compat story (older hubs drop the unknown override; native gates on schema; no compat code to remove).

- [ ] **Step 4: Shepherd per repo rules**

One push, one CI run, one wait with the shepherd-pr settle detector. Read the roborev combined comment plus `roborev list --open` each round; fix findings red-test-first with one evidence comment per round covering every finding; refute wrong findings with the code's own construction. Once green and approved, enqueue: `scripts/ops/merge-enqueue.sh --pr <N> --wait 570`, repeated while it prints `MERGEQ timeout`. Handle low findings in a fast-follow PR.
