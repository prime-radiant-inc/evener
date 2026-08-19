# Inline Composer Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the model, reasoning-effort, context-meter, and session-actions controls from the session footer into the composer row beside the paperclip without changing their behavior.

**Architecture:** Refactor `SessionChrome` so its existing controls can render in an embedded composer placement while retaining its panel handles and action callbacks. `Composer` will render the embedded chrome in `PromptCard`'s leading slot after the attachment button, and `Session.tsx` will stop mounting the standalone footer chrome. Existing `StatusRow` and session-menu behavior remains shared rather than duplicated.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest, Testing Library, Vite, Biome, layoutguard scripts, Make frontend gates.

## Global Constraints

- Keep the change deterministic; do not add provider credentials, network calls, or live-model dependencies.
- Run `npx biome check --write` on touched frontend files under `cmd/evener-hub/frontend/src/` before the frontend gate.
- Use `make test-web` as the canonical frontend unit, typecheck, and Biome gate.
- Run `make test-web-browser` when Chrome is available.
- Preserve native model/effort controls, accessible names, menu actions, panel mounting, attachment behavior, and Send/Stop/Steer behavior.
- Do not use CSS-only visual reordering or fixed-position layout hacks.

---

## File map

- Modify `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`: add the embedded presentation boundary while keeping session-menu panels and callbacks in one owner.
- Modify `cmd/evener-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`: define the embedded chrome/controls layout and focus/overflow rules.
- Modify `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx` (create if absent): cover the embedded control contract and existing menu/panel wiring.
- Modify `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`: render embedded chrome beside the attachment control in `PromptCard.leading`.
- Modify `cmd/evener-hub/frontend/src/panes/session/composer/composer.module.css`: add only composer-specific leading-cluster sizing needed for the embedded chrome.
- Modify `cmd/evener-hub/frontend/src/panes/session/Session.tsx`: remove the standalone footer `SessionChrome` mount after its controls move into the composer.
- Modify `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx` and `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`: assert one control instance in the composer and no duplicate footer row.
- Modify `cmd/evener-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx` only if its old parent/layout assumptions need updated; preserve its control behavior tests.
- Modify `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html` and its `assert.mjs` only if the harness represents the production footer placement; update expected geometry/row selectors, not unrelated harness markup.

---

### Task 1: Add an embedded SessionChrome presentation

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/chrome/sessionchrome.module.css`
- Test: `cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx`

**Interfaces:**
- Consumes: existing `SessionChromeProps { ref: string }`, `StatusRow`, `GoalControl`, `SessionMenu`, and the existing Details/Tasks/Activity panel handles.
- Produces: an explicit embedded/inline presentation prop or component that renders the model, effort, context meter, and session-actions trigger as one leading-row cluster while preserving the existing standalone presentation for callers until Task 2.

- [ ] **Step 1: Inspect existing SessionChrome test setup and write the failing embedded contract test**

Add a test fixture using the repository's existing store/provider setup. Render the embedded variant for a hydrated thread and assert the four stable hooks are inside the embedded cluster in order:

```tsx
const cluster = screen.getByTestId("session-chrome-inline");
expect(within(cluster).getByTestId("status-row-identity")).toBeInTheDocument();
expect(within(cluster).getByTestId("status-row-context")).toBeInTheDocument();
expect(within(cluster).getByRole("button", { name: "Session actions" })).toBeInTheDocument();
expect(cluster.compareDocumentPosition(screen.getByTestId("status-row-context")));
```

Also assert the standalone mode still renders `data-testid="session-chrome"` and the embedded mode does not create a second `status-row` or second session-menu trigger.

- [ ] **Step 2: Run the focused test and verify it fails**

Run from the repository root:

```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Expected: the new embedded test fails because the embedded presentation does not yet exist.

- [ ] **Step 3: Implement the embedded presentation with one control owner**

Refactor `SessionChrome` minimally so its existing store reads, panel refs, `openDetails`/`openTasks`/`openActivity` callbacks, and `SessionMenu` action adapters remain shared. Add an explicit presentation mode such as:

```tsx
type SessionChromePlacement = "footer" | "composer";
interface SessionChromeProps {
  ref: string;
  placement?: SessionChromePlacement;
}
```

In `composer` placement, render a single `session-chrome-inline` cluster containing `StatusRow` and the session menu's trigger, while keeping the panels mounted. Do not render cadence or goal in that cluster. Keep `footer` placement behavior intact until Task 2. Ensure the menu trigger remains labeled `Session actions` and all existing callbacks are used unchanged.

Add CSS for the embedded cluster as a nowrap, min-width-safe flex group with the existing focus-visible treatment. Make `StatusRow` flex-shrink-compatible in the embedded parent without changing its meter/percent container-query thresholds.

- [ ] **Step 4: Run the focused test and verify it passes**

```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx
```

Expected: PASS, including the pre-existing standalone tests.

- [ ] **Step 5: Format and commit the self-contained refactor**

```bash
cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/chrome/SessionChrome.tsx src/panes/session/chrome/sessionchrome.module.css src/panes/session/chrome/SessionChrome.test.tsx
cd ../../..
git add cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.tsx cmd/evener-hub/frontend/src/panes/session/chrome/sessionchrome.module.css cmd/evener-hub/frontend/src/panes/session/chrome/SessionChrome.test.tsx
git commit -m "refactor: embed session chrome controls"
```

---

### Task 2: Move the control cluster into the composer

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/composer.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/Session.test.tsx`

**Interfaces:**
- Consumes: Task 1's embedded `SessionChrome` placement and existing `PromptCard.leading` slot.
- Produces: the production DOM order `paperclip → model → effort → meter → session actions` in the composer, with no standalone duplicate status/control row.

- [ ] **Step 1: Write the failing integration assertions**

Extend the existing composer test that verifies the shared `PromptCard` and attachment button. Assert that the inline cluster is a descendant of the prompt card controls and follows the attachment button in document order:

```tsx
const card = screen.getByTestId("composer-input-card");
const attach = within(card).getByTestId("composer-attach");
const inline = within(card).getByTestId("session-chrome-inline");
expect(attach.compareDocumentPosition(inline) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
expect(within(card).getByTestId("status-row-context")).toBeInTheDocument();
expect(screen.queryAllByTestId("status-row")).toHaveLength(1);
```

Add a Session-level assertion that the footer no longer contains `session-chrome` while the composer contains `session-chrome-inline`. Keep existing tests for model changes, effort changes, menu actions, attachment selection, and submit routing unchanged.

- [ ] **Step 2: Run the focused tests and verify they fail**

```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx
```

Expected: the new placement assertions fail because `Composer` still has only the attachment leading control and `Session.tsx` still mounts the footer chrome.

- [ ] **Step 3: Wire the embedded chrome into Composer and remove the duplicate footer mount**

In `Composer.tsx`, import `SessionChrome` and make `PromptCard.leading` render a flex group with the paperclip first and `<SessionChrome ref={ref} placement="composer" />` second. Preserve the existing ended-session gating: if the composer controls are hidden for an unengaged ended session, do not mount the inline cluster either.

In `Session.tsx`, remove the separate `<SessionChrome ref={ref} />` footer child. Keep the footer wrapper and measured composer flow intact so transcript/footer sizing and pending chips remain unchanged.

Add only the CSS needed to let the leading group consume available space, shrink safely, and keep the trailing Send/Stop/Steer action group aligned. Do not duplicate `StatusRow` or menu styles in `composer.module.css`.

- [ ] **Step 4: Run the focused tests and verify they pass**

```bash
cd cmd/evener-hub/frontend && npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx src/panes/session/chrome/SessionChrome.test.tsx
```

Expected: PASS with exactly one status/control cluster and unchanged behavior tests.

- [ ] **Step 5: Format and commit the integration**

```bash
cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/composer/Composer.tsx src/panes/session/composer/composer.module.css src/panes/session/Session.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx
cd ../../..
git add cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx cmd/evener-hub/frontend/src/panes/session/composer/composer.module.css cmd/evener-hub/frontend/src/panes/session/Session.tsx cmd/evener-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/evener-hub/frontend/src/panes/session/Session.test.tsx
git commit -m "feat: move session controls into composer"
```

---

### Task 3: Update layout guards and run frontend gates

**Files:**
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html` only if it still encodes the old production placement.
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs` only for assertions that intentionally target the old footer row.
- Modify: relevant tests discovered by the focused runs only when a failure identifies an outdated placement contract.

**Interfaces:**
- Consumes: the integrated composer control row from Task 2.
- Produces: deterministic layout assertions for wide and compact composer widths plus passing frontend gates.

- [ ] **Step 1: Run the relevant layout guard before changing expectations**

```bash
cd cmd/evener-hub/frontend && node scripts/layoutguard/run.mjs --case compact-session-footer
```

Record any failures and inspect the case's `harness.html` and `assert.mjs`. Do not modify the harness merely to silence unrelated accessibility or geometry failures.

- [ ] **Step 2: Update only stale placement assertions**

Change selectors and expected row ownership so the guard verifies the attachment, model, effort, meter, and session-actions controls share the composer control row. Retain checks for no horizontal overflow, usable focus-visible outlines, compact meter behavior, and the Send control's trailing edge.

- [ ] **Step 3: Run focused tests, formatting, and the canonical frontend gate**

```bash
cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/composer/Composer.tsx src/panes/session/composer/composer.module.css src/panes/session/Session.tsx src/panes/session/composer/Composer.test.tsx src/panes/session/Session.test.tsx src/panes/session/chrome/SessionChrome.tsx src/panes/session/chrome/sessionchrome.module.css src/panes/session/chrome/SessionChrome.test.tsx
cd ../../..
make test-web
```

Expected: the command exits 0 with frontend unit tests, typecheck, and Biome passing.

- [ ] **Step 4: Run browser geometry guards when Chrome is available**

```bash
make test-web-browser
```

Expected: the browser guard exits 0. If Chrome is unavailable, report that limitation and retain the deterministic `make test-web` evidence.

- [ ] **Step 5: Review final diff and commit guard changes**

```bash
git diff --check
git status --short
git diff --stat
git add cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/harness.html cmd/evener-hub/frontend/scripts/layoutguard/cases/compact-session-footer/assert.mjs
git commit -m "test: guard inline composer controls"
```

Stage only files actually changed by this task; if neither guard file changed, do not create an empty commit.
