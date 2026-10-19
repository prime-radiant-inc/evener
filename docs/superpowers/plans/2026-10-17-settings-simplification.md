# Settings Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make project configuration contextual and ordinary settings concise without losing controls or changing persistence.

**Architecture:** Reuse the existing section components, host-scoped launch store, transcript display store, and widget library. Move repository review into Project settings; use controlled disclosures for optional detail and keep editable state above conditional content.

**Tech Stack:** React, TypeScript, Zustand, Vitest, Testing Library, existing AppWire transport fixtures.

**Spec:** `docs/superpowers/specs/2026-10-17-settings-simplification-design.md`

## Global Constraints

- No backend changes, parent-directory discovery, new persistence, new widget APIs, or launch-flow redesign.
- No deployment/restart of the running hub.
- Trust remains an explicit action against the reviewed hash.
- Copy is not an assertion oracle.
- Preserve all persistence authorities and recovery behavior.
- Execute inline, then simplify, converge local RoboRev branch review, and shepherd the PR.

## Review Focus

- Host or directory changes during repository inspection/trust must not expose or authorize the previous scope (Task 1).
- Reconnect and trust refresh must preserve an unsaved local project draft (Task 1).
- Switching display layouts during a pending save must preserve independent drafts and feedback (Task 2).
- Failed notification permission and channel toggles must preserve the stored loudness scope (Task 3).
- Closing/filtering the shortcut editor must preserve edits and leave accessibility/recovery controls reachable (Task 3).

## Files and verification conventions

Unless fully qualified, section paths below are under `cmd/evener-hub/frontend/src/panes/settings/sections/`. Keep component-specific styles beside their components. Existing tests use real components and stores with transport-boundary fixtures. Extend those fixtures rather than mocking child components. Queries may use accessible names to operate controls, but assert state, wire calls, visibility, focus, or structure, never prose equality.

From `cmd/evener-hub/frontend`, run focused tests with `./node_modules/.bin/vitest run <listed paths> --maxWorkers=2`. Before implementation read `docs/developing-evener/testing.md`, `docs/developing-evener/design-system.md`, the product guide, and Impeccable's craft floor. Baseline: notifications, inputShortcuts, transcript and project test files passed, 44 tests.

### Task 1: Contextual repository review

**Files:** Modify `inrepo.tsx`, `project.tsx`, their corresponding test files, `inrepoHostScope.test.tsx`, `projectHostScope.test.tsx`, `../Settings.tsx`, `../sections.ts`, and existing settings/routing tests that cover valid and retired routes. Update `docs/web-ui/README.md` and the applicable product ownership references.

**Interfaces:** Extract `RepositoryConfigSection({host, cwd}: {host: string; cwd: string})` from the current repository review implementation. It uses `launchConfigStoreForHost(host)` and the existing `resolve(cwd)` / `trustRepo(cwd, hash)` methods. ProjectSection supplies its route directory and selected host. Remove the global wrapper and directory picker once no longer referenced. Keep repo load/error state independent of the local form.

- [ ] Add regression tests to project and host-scope suites. Verify repository preview and local override form coexist; inspection failure leaves local saving available; no automatic trust call occurs; the explicit action sends the selected directory and reviewed hash to the selected host. Assert outgoing method/params with the existing FakeClient request capture.
- [ ] Add deferred-response cases for switching host/directory while loading and trusting. Resolve the old response last and assert the current scope remains unchanged. Edit the local form, deliver reconnect/trust-refresh events, and assert the draft survives and saving uses the current owner.
- [ ] Run these tests and record the expected failures before implementing the extraction and composition.
- [ ] Implement the component and scoped lifetimes; remove inrepo navigation/dispatch. Retired inrepo URLs use the existing fallback, including mobile Back/Forward and selected-host preservation. Update route regressions without weakening existing assertions.
- [ ] Run project, repository, host-scope, settings and route tests plus `npm run typecheck`. Update docs and commit only the changed files.

### Task 2: One conversation layout and usable local reset

**Files:** Modify `transcript.tsx`, `TranscriptDisplayCard.tsx`, their test files and existing module CSS as needed. Reuse `../../../stores/transcriptDisplay.ts` without changing its persistence contract.

**Interfaces:** Keep TranscriptSection's existing draft/save ownership. Add a layout selector using an existing widget, initializing from the application's existing viewport classification. Add `onClearLocal(): void` to the section-owned TranscriptDisplayCard props, implemented by `transcriptDisplayStore.getState().clearLocal(layout)`. This is a component prop, not a widget API change. Use controlled `Disclosure` for Technical details.

- [ ] Add tests that initially find one layout editor, can select the other, and preserve independent unsaved drafts/save outcomes while a request is pending. Cover both starting viewport classes.
- [ ] Seed both local overrides and shared defaults. Clear the selected local override and assert `local[selected]` is cleared, the other layout is unchanged, no shared-default patch is sent, and the effective selected config now follows the shared default. Retain existing blocked-storage recovery coverage.
- [ ] Assert the preview stays available while technical inventories/revision content are folded and become available when expanded. Run targeted tests and observe failures.
- [ ] Implement conditional rendering with state retained in TranscriptSection; add the reset action and disclosure. Run transcript/card/store regressions and typecheck, update the web guide, and commit touched files.

### Task 3: Short notification and shortcut settings

**Files:** Modify `notifications.tsx`, `keybindings.tsx`, their test files, `inputShortcuts.test.tsx`, and existing module CSS if needed. Keep `inputShortcuts.tsx` composition and `display.tsx` Enter-to-send ownership.

**Interfaces:** Keep existing notification preference setters and permission flow. Show the scope group iff `notifications.os || notifications.sound`. Keep character-key and error/retry controls outside a controlled Disclosure. Keep search/edit state in KeybindingsSection; filter ACTION_DISPLAY_ROWS by normalized action ID and displayed label through an existing text-input widget.

- [ ] Test all four sound/desktop channel combinations; assert the scope control's availability and unchanged stored scope when hidden. Cover rejected permission and re-enabling a channel. Rename accessible queries only as needed to operate the renamed switches.
- [ ] Test collapsed shortcut editor, immediately reachable basic switches/retry, opening and keyboard operation, case-insensitive label and action-ID searches, zero results, clearing search, and closing/reopening after editing. Assert binding state and row availability rather than copy.
- [ ] Run notification/keybindings/inputShortcuts tests and confirm failures before implementation.
- [ ] Implement conditional scope, Desktop notifications and Browser tab indicator labels, shortcut disclosure/search, and preserved editing state. Rerun the targeted tests and typecheck. Update docs and commit touched files.

### Task 4: Diagnostics and naming

**Files:** Modify `hub.tsx`, `storage.tsx`, corresponding tests, `launchServer.tsx`, `../sections.ts`, and related settings tests. Update `docs/web-ui/README.md` and affected product documentation.

**Interfaces:** Use existing controlled `Disclosure` with `open: boolean` and `onOpenChange(open: boolean): void`. No new widget props. Existing diagnostic RPCs and update/cache/resident actions remain unchanged.

- [ ] Test folded runtime/path information becoming available on expansion; assert update/resident/cache controls and error recovery remain reachable while folded. Verify the run-directory value is present only in expanded Storage. Keep current source-of-truth/path-value assertions.
- [ ] Run Hub and Storage regressions and observe failures. Implement Technical details disclosures and remove only the duplicated Hub run-directory row.
- [ ] Rename launch metadata/heading/help to Session defaults while retaining launch-evener routing. Ensure settings search still matches launch-related terms. Test search/navigation behavior, not exact label wording.
- [ ] Run affected tests, format touched files with the frontend's pinned Biome, run `npm run typecheck`, update docs, and commit exact changed paths.

### Task 5: Finish, simplify, review, and merge

**Interfaces:** All changes above are independent of backend discovery/trust rules. The whole branch is the review unit.

- [ ] Self-review against every spec paragraph, including mobile reachability and error paths. Run `make test-web` from repository root; run `make test-web-browser` separately. Inspect gate scripts before reporting their claims. Browser acceptance must use the new build, not the unchanged live hub; report any unrun live-host scenario.
- [ ] Use `simplify-code:simplify-code` on the branch changes. Apply justified simplifications and rerun the affected checks. Commit only reviewed changes.
- [ ] Run `roborev review --branch --wait`; read every finding. Reproduce genuine findings red-first, fix, rerun tests and repeat whole-branch review until clean. Inspect `roborev list --open` for this branch's commits without closing other work's findings.
- [ ] Use `shepherd-pr`: push the branch, create a PR with scope and actual verification, inspect CI and review output, fix complete rounds before pushing again. Merge main before final approval when needed, with clean affected paths and unchanged refs verified.
- [ ] When final-head CI and review satisfy repository merge policy, merge with an exact-head guard. Report the PR, merge result, tests and limits. Preserve the existing running hub and unrelated critique file.

## Self-review

Each approved simplification and label maps to a task. Five cross-state risks have explicit tests. Existing persistence and trust interfaces are reused; no backend or widget API addition is required. The plan is ready for Jesse's confirmation, preserving inline execution.
