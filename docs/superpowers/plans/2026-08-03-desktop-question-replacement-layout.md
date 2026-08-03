# Desktop question replacement layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the session composer region fill available space so the open question surface replaces the composer and remains bottom anchored on desktop without regressing mobile layout.

**Architecture:** Keep the existing Composer/AskDock state switch and pane-local layout. Move the full-height flex responsibility to the shared composer parent, and use the same replacement slot for the composer and AskDock. Preserve AskDock's internal overflow behavior and existing mobile viewport containment rules.

**Tech Stack:** React, TypeScript, CSS Modules, Vitest, Testing Library, Vite.

## Global Constraints

- Do not use viewport-fixed positioning that escapes the active pane.
- Pending questions replace the composer; they must not render an additional composer.
- Preserve existing mobile viewport-height and overscroll containment behavior.
- Keep question controls, state, submission behavior, and unrelated pane layout unchanged.
- Read `docs/testing.md` before changing tests.

---

### Task 1: Establish the replacement-slot layout contract

**Files:**
- Read: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
- Read: `cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css`
- Read: `cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx`

**Interfaces:**
- Consumes: existing `Composer` conditional rendering and `AskDock` component.
- Produces: a shared composer-region CSS contract in which the parent fills available height and the active child is bottom aligned.

- [ ] **Step 1: Read testing guidance and inspect existing layout tests**

Run:
```bash
sed -n '1,180p' docs/testing.md
grep -n -C 5 "AskDock\|askPending\|composer" cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx
```

Record the existing test harness and any layout-contract conventions before editing.

- [ ] **Step 2: Add a failing structural contract test**

Extend the existing focused test file with assertions against the rendered structure, using the project’s existing render helpers. The test must verify that the pending-question state renders the AskDock replacement surface and does not render the normal composer textarea. Use accessible queries already established in the file, for example:

```tsx
expect(screen.getByText("Answer the agent’s questions.")).toBeTruthy();
expect(screen.queryByRole("textbox", { name: /message/i })).toBeNull();
```

Add a CSS-source contract assertion only if the repository’s existing layout tests use that pattern; otherwise keep the test at the DOM/state boundary and verify CSS through the web layout/preflight checks.

- [ ] **Step 3: Run the focused test and confirm the contract fails or exposes the missing layout behavior**

Run:
```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx
```

Expected: the test suite either fails on the new replacement assertion or demonstrates the current layout contract is not covered. Do not weaken the assertion to accommodate the current incorrect layout.

- [ ] **Step 4: Implement the minimal parent/child flex change**

Update the existing composer-region CSS so the parent consumes available height (`flex: 1 1 auto; min-height: 0`) and the active replacement surface is bottom aligned. Keep the existing `Composer`/`AskDock` conditional rendering unchanged unless inspection proves both are currently mounted. Do not add `position: fixed`; do not alter question state or controls.

The change must preserve the current AskDock scroll boundary (`overflow-y: auto`) and avoid introducing horizontal overflow.

- [ ] **Step 5: Run focused tests and inspect the diff**

Run:
```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/composer/Composer.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx

git diff --check
git diff -- cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx
```

Expected: focused tests pass and the diff contains only the replacement-slot layout/test changes.

- [ ] **Step 6: Commit the focused implementation**

```bash
git add cmd/serf-hub/frontend/src/panes/session/composer/composer.module.css cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx cmd/serf-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx
git commit -m "fix(webui): bottom-anchor desktop question replacement"
```

---

### Task 2: Verify responsive layout and web gates

**Files:**
- Read: `cmd/serf-hub/frontend/src/styles/global.css`
- Read: `cmd/serf-hub/frontend/src/panes/session/composer/askDock/askdock.module.css`
- Modify: only if Task 1 reveals a focused mobile regression
- Test: existing focused and responsive layout tests

**Interfaces:**
- Consumes: Task 1’s replacement-slot CSS contract.
- Produces: verified desktop and mobile behavior with no unrelated changes.

- [ ] **Step 1: Run the full relevant frontend test stream**

Run:
```bash
cd cmd/serf-hub/frontend
npm test -- --run
```

If the package script does not accept these arguments, run the repository’s documented equivalent from `package.json`; report the exact command used.

- [ ] **Step 2: Run repository web preflight and build checks**

From the repository root, run:
```bash
make web-preflight
make build-web
```

Expected: both commands exit successfully. Read and address any warnings or errors rather than suppressing them.

- [ ] **Step 3: Confirm the final diff and working tree**

Run:
```bash
git diff --check
git status --short
git log -2 --oneline
```

Confirm the implementation preserves mobile rules, uses no viewport-fixed positioning, keeps the question surface in the active pane, and changes only the scoped files.

- [ ] **Step 4: Commit only any verified follow-up correction**

If verification identifies a real regression, make the smallest correction, rerun the failing command, and commit it separately:

```bash
git add <only-verified-files>
git commit -m "fix(webui): preserve responsive question layout"
```

If no correction is required, do not create an empty commit.
