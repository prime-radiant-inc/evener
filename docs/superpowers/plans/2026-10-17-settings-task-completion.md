# Settings Task Completion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make settings discoverable, correctly scoped and resilient without replacing its design.

**Architecture:** Keep existing section components and host-scoped stores. Add a narrow dirty-editor registration boundary for intentional settings navigation, reuse existing dialogs/widgets, and preserve transport authority separately from visible drafts.

**Tech Stack:** TypeScript, React, Zustand, Vitest, existing widget library and browser guards.

**Spec:** `docs/superpowers/specs/2026-10-17-settings-task-completion-design.md`

## Global Constraints

- No new widget or widget prop, dependency, persistence authority, automatic install, automatic enablement, or automatic uncertain-write retry.
- Preserve host-registration identity, selected-cwd discovery, reviewed-hash trust, explicit-empty launch semantics, browser overrides and independent Desktop/Mobile defaults.
- Ask Stay/Discard before intentionally leaving an edited form; temporary errors retain drafts. Never transfer drafts across replaced host registrations.
- Use TDD and behavior assertions under `docs/developing-evener/testing.md`; read design-system guidance and craft floor before UI edits.
- Leave live hub and unrelated files untouched. Run heavy gates serially. Stage exact task paths, read staged diff, commit with hooks.

## Review Focus

- A stale async response after navigation cannot retarget a pending discard or mutation (Task 4).
- A source refresh must not convert an untouched stale form into an overwrite (Task 2).
- Permission denied/unavailable differs from preference persistence (Task 1).
- A shortcut write may have applied despite transport failure; reload before another explicit write (Task 3).
- An unset local override inherits repository values ahead of global defaults (Task 5).

### Task 1: Discoverable controls and honest feedback

**Files:** `cmd/evener-hub/frontend/src/panes/settings/sections.ts`, `sections.test.ts`, `SettingsNav.test.tsx`, `sections/notifications.tsx`, `sections/notifications.test.tsx`, `cmd/evener-hub/frontend/src/styles/tokens.css`, existing button contrast/visual tests under `src/widgets/button` or browser guard (find the owning test before adding).

**Interfaces:** Keep `matchesSettingsSection(section: SettingsSection, query: string): boolean` and existing metadata shape; add terms, not a search subsystem. Keep `NotificationsSection()` and prefs store contract. Existing button tokens only, no prop additions.

- [ ] Read source and reproduce queries `Desktop notifications`, `Phone density`, `mobile layout`, `desktop layout`, `quiet` returning incorrect/no destination. Add destination-ID assertions and preserve Devices pairing matches.
- [ ] Add permission behavior cases for denied, unavailable, newly denied/granted and rejected request. Assert no repeated permission request, visible status/alert semantics, retained intended preference or existing rejection rollback as appropriate, and no false working-state implication.
- [ ] Run targeted tests and record expected failures; implement metadata and inline permission-state/recovery feedback. No repeated browser prompts.
- [ ] Pin normal primary-button contrast >=4.5:1 with independently computed WCAG luminance in relevant tests; keep accent identity, use an existing theme-appropriate token/value rather than new widget API. Verify light/dark and hover/disabled meanings; do not assume disabled states require active contrast.
- [ ] Run targeted Vitest, `npm run typecheck` and explicit-path Biome from frontend. Update affected evergreen guide paragraphs; commit only task files.

### Task 2: Complete the first-plugin and recovery paths

**Files:** `cmd/evener-hub/frontend/src/panes/settings/sections/marketplacesPlugins/` (implementation and existing tests), associated plugin settings store only if source-reconciliation requires it, `docs/web-ui/README.md`.

**Interfaces:** Preserve `MarketplacesPluginsHostScope` and existing transport APIs. Add callbacks between current workspace and Installed/Browse/Sources children for direct navigation, not route aliases. Existing source sheet owns baseline/draft/conflict locally.

- [ ] Add failing component journeys: no installed plugins -> Browse; no sources -> Sources/add; successful source addition -> explicit browse action. Assert selected segment/data, no implicit install/enable/add calls.
- [ ] Add failures for plugin-only and marketplace-only read errors, keeping unaffected segment usable; targeted retry calls only failed read. Retain source draft across temporary refresh failure/recovery.
- [ ] Add concurrency cases: untouched open source follows refreshed canonical entry without becoming dirty; edited source retains draft and requires explicit choice before replacing externally changed values. Never silently overwrite or reseed edited values.
- [ ] Run RED cases, implement minimal callbacks/error isolation/baseline conflict UI with existing widgets and existing applied-removal safeguards. Explain catalog format using an existing documentation link verified in tree.
- [ ] Run plugin/settings targeted tests, typecheck and Biome, update guide, commit exact task files.

### Task 3: Retain shortcut proposals through failed saves

**Files:** `cmd/evener-hub/frontend/src/panes/settings/sections/keybindings.tsx`, `keybindings.test.tsx`, relevant existing keybindings store/tests if needed.

**Interfaces:** Preserve `KeybindingsSection()`, current chord semantics, existing request serialization and generation fences. Separate capture proposal lifetime from writable capability, without carrying drafts across hub registration changes.

- [ ] Add failing regression: capture chord, fail save, retain proposal through read recovery; explicit subsequent save uses current canonical revision. Assert no automatic second mutation.
- [ ] Cover successful-but-response-lost reconciliation, same-hub reconnect, unsupported hub, changed registration, folded/filtered proposal, Escape cancellation and focus. Tab remains bindable; do not invent keyboard behavior beyond explicit existing cancellation.
- [ ] Run RED cases, implement retained proposal/error state with current store authority, then run shortcut/input tests and typecheck/Biome.
- [ ] Update guide if recovery contract changes; commit exact task paths.

### Task 4: Confirm navigation away from unsaved settings

**Files:** `cmd/evener-hub/frontend/src/panes/settings/Settings.tsx`, `Settings.test.tsx`, `HostPicker.tsx`, settings host/project routing tests; create `settingsDrafts.tsx` and `settingsDrafts.test.tsx` if no existing equivalent; explicit editor consumers under `sections/`; `src/shell/routing.ts` and its tests only for necessary navigation interception.

**Interfaces:** Reuse existing navigation/dialog mechanisms if found. Otherwise create `useSettingsDirty(isDirty: boolean): void` for editor registration under a Settings-owned provider. Registration is component-lifetime scoped, not a persistent cache. Route confirmation must finish before destination context/host is adopted. Keep existing `navigate` consumers working without altered behavior when no dirty settings editor exists.

- [ ] Inventory explicit draft editors and their canonical dirty signals; record inventory in task report. Include launch config, source add/edit, directories/MCP, credentials/instructions where drafts exist, and shortcut proposals. Autosave preferences are excluded.
- [ ] Add failing integration tests: dirty host/project/section switch and Settings close -> confirmation; Stay keeps original context/route/input; Discard navigates once to requested destination. Clean forms navigate immediately. No stale handler after unmount.
- [ ] Cover mobile Back and browser history navigation, original pending destination stability, save clearing dirty state, cancelled save not clearing it, remote registration replacement without draft retargeting. Browser refresh/close uses native beforeunload only while dirty.
- [ ] Implement minimal guard and existing confirmation UI; no new widget API. Keep transient error recovery distinct from intentional navigation. If context replacement is automatic, discard no user's work by pretending it belongs to new registration; visibly invalidate write authority.
- [ ] Run guarded editor/routing/settings tests, typecheck/Biome, update evergreen guide, commit exact task paths.

### Task 5: Clarify scope and accurate inheritance

**Files:** `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx`, owning `TranscriptDisplayCard.tsx` (locate), project/launchShared implementation and tests, `appwire-client/typescript/launchSchema.ts` only when shared schema wording is responsible, relevant docs.

**Interfaces:** Keep current store persistence and launch merge order. Existing card composition may reorder scope status and action ahead of controls. Add form context prop only on non-widget LaunchConfigForm if needed for save labels. Inherited display data must come from resolver layers excluding local override, not the effective local result.

- [ ] Add structural/behavior test that active browser override remains when shared setting changes and explicit clear applies only selected layout. Verify scope/action precedes editor in DOM without asserting prose.
- [ ] Add project inheritance cases with distinct global, trusted repository and local values. Clearing local field shows repository/global fallback accurately before save; explicit-empty fields retain their existing semantics. Confirm no trust or precedence changes.
- [ ] Implement clear scope placement, accurate hints and context-specific action labels. Shorten explanatory copy only where guarantees remain explicit.
- [ ] Run transcript/project/launch tests and affected shared package tests, typecheck/Biome, update docs and commit exact paths.

### Task 6: Whole-branch verification and finish

**Files:** Only fixes discovered within the scoped review; docs stay evergreen.

- [ ] Run simplify-code review and one independent whole-branch correctness review after per-task reviews. Resolve material findings with failing-before/passing-after tests.
- [ ] Run `make test-web`, then `make test-web-browser` serially. Inspect actual gate commands before reporting results. Perform one desktop/mobile visual pass on isolated current-build fixtures, fix one batch, then at most one confirmation pass.
- [ ] Check all spec outcomes, especially guard/editor integration, partial outage, cross-host boundaries and token effects. Do not claim live permission/remote trust coverage from fixtures.
- [ ] Finish with a committed clean task branch, accurate review/test report, and preserve the live hub. If publishing a PR under standing workflow, require final-head CI and review evidence before merge.

## Self-review

The scope maps to Tasks1–5; Task6 validates combined behavior. Task4 depends on Task2/3 editors, so execute serially. Task5 reuses Task4's form guard and must preserve dirty-state semantics. Shared docs are edited serially. No task adds a widget API, aliases, persistence layer or trust change. Reproduction may refine mechanisms; record any consequential ruling in the plan ledger.
