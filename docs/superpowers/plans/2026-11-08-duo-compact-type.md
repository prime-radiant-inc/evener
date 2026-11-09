# Global Compact Serif and Duo Density Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply smaller Source Serif prose globally and substantially compact the Duo's existing composer and activity footer without changing app behavior.

**Architecture:** Change the shared web type tokens. Add narrowly scoped Duo cover-density CSS to the existing session composer, SessionChrome, and StatusBar, preserving their DOM and activity actions. Keep the OS-owned side rail untouched.

**Tech Stack:** React, TypeScript, CSS Modules, Vitest, Playwright layout guards.

**Spec:** `docs/superpowers/specs/2026-11-08-duo-compact-type-design.md`

## Global Constraints

- Source Serif remains the prose face; default prose becomes 16px with a dedicated 1.45 prose-leading token on reading surfaces. General UI/editor `--line-height-body` stays unchanged.
- Phone editable text remains at least 16px and primary touch targets remain 44px.
- Existing drafts, attachments, Stop/Send/Steer, question/approval docks, counters, and Overview links retain their behavior.
- No OS side-rail bridge, new activity model, or liquid-glass overlay in this pass.
- Use existing widgets and tokens; no new widget API.

## Review Focus

- The global prose ramp affects every reading surface, not only the Duo: token tests and desktop/phone browser metrics pin the intentional change.
- The 466px cover viewport may coexist with desktop and phone layouts: Duo CSS must not alter the global desktop footer or ordinary phone behavior.
- Four activity counters must remain reachable at compact widths: existing StatusBar behavior tests plus a Duo browser geometry check pin the links and their layout.
- Empty/live, filled, and pending composer states have different controls: existing Composer tests and targeted browser checks ensure the compact dock does not remove or overlap them.
- Keyboard and bottom safe-area insets must still clear the compact dock: existing CSS contracts and browser geometry checks cover this.

---

### Task 1: Compact the global serif reading ramp

**Files:**
- Modify: `cmd/evener-hub/frontend/src/styles/tokens.css`
- Modify: `cmd/evener-hub/frontend/src/styles/token-contract.test.ts`
- Modify: `cmd/evener-hub/frontend/src/widgets/markdown/markdown.module.css`
- Modify: reading-surface CSS that shares the prose-leading token, including live/settled transcript styles
- Modify: `docs/web-ui/design-system.md`
- Modify: `docs/product/subsystems.md`

- [ ] Add a failing token test for the 16px prose token and 1.45 line-height while retaining Source Serif and existing scaling.
- [ ] Run the targeted token test and confirm it fails on the old 18px/1.6 values.
- [ ] Change the shared tokens, not individual transcript components or `--font-prose`.
- [ ] Run the token and type specimen tests, then verify desktop and phone computed metrics in the browser.
- [ ] Update the owning design-system and subsystem documentation to describe the smaller serif reading ramp.
- [ ] Commit the type-ramp change and tests separately from the Duo layout work.

### Task 2: Compact the Duo composer and status/footer presentation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/composer.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/statusrow.module.css`
- Modify: `cmd/evener-hub/frontend/src/shell/statusbar/statusbar.module.css`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/`
- Modify: `docs/web-ui/design-system.md`
- Modify: `docs/product/subsystems.md`

- [ ] Add a failing Duo 466 × 678 browser geometry check for the compact existing composer and activity footer, including all four counter links and their hit areas.
- [ ] Run the new guard against the baseline and confirm it demonstrates the excess footer height before CSS changes.
- [ ] Implement the smallest Duo-scoped spacing/layout changes, using the measured cover width and existing CSS ownership. Do not change component behavior or introduce a separate activity store.
- [ ] Preserve compact path/branch truncation, Agents/Jobs/Watches/Tasks counters, model/effort/context controls, Stop/Send/Steer, attachments, and question/approval docks.
- [ ] Run the new guard and existing composer/status tests; inspect the Duo viewport and ordinary desktop/phone widths.
- [ ] Update the design-system and subsystem docs with the actual compact layout and the OS-rail limitation.
- [ ] Commit the Duo layout, geometry coverage, and documentation.

### Task 3: Review and verify the whole change

- [ ] Run `make test-web` and the targeted `make test-web-browser` / layoutguard checks, one heavy gate at a time.
- [ ] Review the complete diff against the approved spec; have two independent reviewers assess behavior preservation and global type scope.
- [ ] Resolve legitimate findings with failing-then-passing tests.
- [ ] Run `simplify-code:simplify-code`, then rerun affected checks.
- [ ] Commit all intended changes and shepherd the PR through CI and review. Do not deploy without separate authorization.
