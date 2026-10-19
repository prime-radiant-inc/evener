# Native Apple Design Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. The session task list owns progress.

**Goal:** Give iPhone Evener a coherent Apple-native hierarchy, surface system and control grammar without changing its capabilities.

**Architecture:** Improve existing tokens and reusable components, then apply them to primary lists and legacy administration screens. Keep all state, recovery and navigation owners intact. Use the current native app, not browser stand-ins, for visual review.

**Tech Stack:** Expo SDK 57, React Native 0.86.3, TypeScript, React Navigation native stacks, SF Symbols, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-19-native-apple-design.md`

## Global constraints

- iPhone-first; iPad and Android remain deferred.
- Preserve drafts, uncertainty, approvals, navigation actions and read-position behavior.
- No new dependencies, framework changes, backend changes or invented copy claims.
- Read `docs/developing-evener/testing.md`; test behavior, never prose equality.
- Existing component APIs stay stable; avoid unrelated refactors.

## Review focus

- Accessibility text sizes: choices and row metadata reflow without clipping actions or content.
- Light/dark: 4.5:1 normal text contrast and distinct page/group/input layers.
- Disabled and selected controls: remain truthful, legible, and correctly inert.
- Long names and paths: wrap without pushing out trailing actions or the back gesture.
- Layout changes: do not remount focused inputs or change read-position/recovery logic.

## Task 1: Shared native foundation

**Files:** `mobile-native/src/design/tokens.ts`, `mobile-native/src/ui.tsx`, `mobile-native/src/sheet/Grouped.tsx`, `mobile-native/src/sheet/SearchField.tsx`, their existing tests.
**Interfaces:** Preserve `Palette`, `uiType`, `space`, `Row`, `Button`, `Segmented` and other exported APIs. Add only private layout helpers if needed.

- Add tests that render real components at default and large font scales, prove selections remain actionable, and prove controls retain 44-point targets. Extend palette contrast tests to grouped surfaces.
- Run `cd mobile-native && npm test -- src/design src/sheet src/ui.test.tsx`; observe the new failures.
- Apply crisp neutral surfaces, coherent type/spacing, restrained selection, and adaptive choices. Preserve semantic hue meanings and reading preferences.
- Repeat tests, run `npm run check`, format touched files with this tree's Biome, and commit exact changed files.

## Task 2: Primary work and conversation surfaces

**Files:** `mobile-native/src/board/BoardRow.tsx`, `mobile-native/src/board/BoardScreen.tsx`, related Board helpers, `mobile-native/src/session/Composer.tsx`, `mobile-native/src/session/SessionHeader.tsx`, `mobile-native/src/session/SessionTitle.tsx` and their tests as required by inspected evidence.
**Interfaces:** Preserve all navigation callbacks, row identity and state props, composer props, and scroll owners.

- Add red tests for large-text Board title/metadata reflow, a readable nonzero session-title allocation, a 44-point resting composer field, and Expand when the viewport height cap makes the field scroll.
- Run focused tests for changed components.
- Establish clear sentence-case section hierarchy and subordinate shortcuts. Align composer/context controls with the shared grammar without changing input lifecycle.
- Verify focused tests and existing Board/conversation behavior suites. Commit exact changed files.

## Task 3: Consistent administration layouts

**Files:** `mobile-native/src/HubSettingsScreen.tsx`, `mobile-native/src/LaunchSettingsScreen.tsx`, `mobile-native/src/KeybindingPreferencesScreen.tsx`, their tests and immediate editor layout files only when needed.
**Interfaces:** Use the existing `GroupedPage`, `Group`, `Row`, `GroupFooter`, `Button` APIs. Do not change shared primitive implementation in this task.

- Preserve existing model, draft, save and reconnect paths; add red tests for any new disclosures or reorganized action ownership.
- Convert ad hoc section frames and detail presentation to consistent native grouped sections. Keep all facts and actions reachable.
- Run focused screen tests and native typecheck. Commit only owned paths.

## Task 4: Native evidence and finish

**Files:** `mobile-native/DESIGN.md`, `mobile-native/.impeccable/design.json`, `docs/web-ui/design-system.md`, and a concise checked-results document under `docs/design/mobile/`.

- Run `make test-native` from repository root after inspecting its scripts. Do not run heavy gates concurrently.
- Build/install current-source iOS app. Inspect Board, conversation, New session, Hub, and changed administration screens in a bounded batched pass, including dark mode and large text.
- Fix material findings in one batch and confirm once.
- Run independent code and Impeccable finish reviews. Address findings without broadening scope.
- Derive the evergreen native design record from shipped code. Clarify native/web identity in the web guide, with no unnecessary web implementation changes.
- Simplify touched code, verify again, inspect diff, commit and report deliverables plus unverified limits. Do not publish, merge or deploy unless separately requested.
