# Kata UI Cleanups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair the five assigned frontend/design-system issues with the smallest maintainable changes and leave controller-owned issue state untouched.

**Architecture:** Keep each fix at its existing boundary: SteeringItem owns wire-kind presentation, the historical docs point only at current design-system guidance, ThemeFlip scopes both panes explicitly, the public steering parser remains the tested behavior boundary, and StreamingText exposes one inherited ink hook for ThinkBlock to override. No shell rail or controller files are in scope.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest, Vite, Biome, kata CLI, Go repository build tooling.

## Global Constraints

- Default tests remain deterministic and must not gain provider, network, quota, model, or ambient-machine dependencies.
- Do not touch `src/shell/rail/**`.
- Do not merge controller changes, close kata issues, or change kata ownership/status.
- Use focused red/green tests for behavioral fixes; the docs and export cleanup rely on existing consumer-boundary coverage where a removal assertion would be a brittle change detector.
- Run the relevant frontend tests, typecheck, lint, production build, browser guards where available, root `make build-runtime`, `git diff --check`, and a full diff self-review.

---

### Task 1: Preserve the steering kind on notification leftovers (ghv3)

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/SteeringItem.test.tsx`

- [ ] **Step 1: Write the failing test**

Add a mixed steer fixture containing a valid `<job-notification>` block followed by prose, render it with `steeringKind: "tasks-done"`, and assert that the notification card, the leftover divider, and `System steered: Tasks done` all appear.

- [ ] **Step 2: Run the focused test and verify the expected failure**

Run `npm test -- --run src/panes/session/transcript/messages/SteeringItem.test.tsx -t "mixed notification"` from `cmd/serf-hub/frontend`. It must fail because the notification branch currently hardcodes `System steered`.

- [ ] **Step 3: Implement the minimal fix**

Compute the known-kind label once before the notification branch and use the same `label ? \`${STEERED}: ${label}\` : STEERED` expression for a non-empty leftover divider and the ordinary divider.

- [ ] **Step 4: Run the focused test and the neighboring parser tests**

Run the SteeringItem test file and `npm test -- --run src/panes/session/transcript/messages/steeringClassify.test.ts`.

- [ ] **Step 5: Commit**

Commit the implementation and regression test with a detailed message naming ghv3 and the mixed notification-plus-prose contract.

### Task 2: Correct superseded web-UI document references (arx3)

**Files:**
- Modify: `docs/web-ui/history/mockup-plan.md`
- Modify: `docs/web-ui/history/ux-and-implementation-plan.md`

- [ ] **Step 1: Update the historical references**

Remove the obsolete “design-system.md §7 — still needs rules” claim. Identify the files as historical planning material and point readers, using the correct `../design-system.md` relative path, to the current design system’s §8 known-gaps section where a current reference is useful.

- [ ] **Step 2: Check the rendered links and stale wording**

Run `rg -n "design-system\.md §7|what-still-needs-rules|still needs rules" docs/web-ui` and inspect the two edited paragraphs. No reference may land on current §7, and the current document must remain identified as current.

- [ ] **Step 3: Commit**

Commit the two-document cleanup with a detailed message naming arx3 and the superseded-history decision.

### Task 3: Scope both ThemeFlip panes to their intended themes (zscn)

**Files:**
- Modify: `cmd/serf-hub/frontend/src/dev/ThemeFlip.tsx`
- Test: `cmd/serf-hub/frontend/src/dev/ThemeFlip.test.tsx`

- [ ] **Step 1: Write the failing behavioral test**

Set the jsdom document root to `data-theme="light"`, render ThemeFlip, and assert that the Dark pane explicitly carries `data-theme="dark"` while the Light pane carries `data-theme="light"`.

- [ ] **Step 2: Run the focused test and verify the expected failure**

Run `npm test -- --run src/dev/ThemeFlip.test.tsx -t "explicitly scopes"`. It must fail because the current first pane has no theme attribute and inherits the light root.

- [ ] **Step 3: Implement the minimal fix**

Set `data-theme="dark"` on the first pane. Keep the existing light-pane attribute and surface/border token declarations unchanged.

- [ ] **Step 4: Run the focused test and attempt the browser visual check**

Run the ThemeFlip test file. Start the Vite app if needed and use the available Chrome/CDP guard path to inspect `/dev/widgets`; if no Chrome/Chromium is installed, record that browser verification is unavailable rather than changing the test suite to depend on it.

- [ ] **Step 5: Commit**

Commit the component and behavioral contract with a detailed message naming zscn and the light-root inheritance failure.

### Task 4: Remove the export-only parser surface (5ev7)

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/steeringClassify.ts`

- [ ] **Step 1: Verify the consumer boundary**

Run `rg -n "stripSystemReminder" cmd/serf-hub/frontend/src` and confirm the declaration plus its in-module call are the only occurrences. Keep parser tests exercising `parseSteeringNotifications`, not the removed helper.

- [ ] **Step 2: Remove only the export modifier**

Make `stripSystemReminder` module-private without changing its behavior or parser call site.

- [ ] **Step 3: Run the public-parser tests and lint reachability check**

Run `npm test -- --run src/panes/session/transcript/messages/steeringClassify.test.ts` and the focused Biome/lint check so the parser coverage remains real and no stale import survives.

- [ ] **Step 4: Commit**

Commit the export cleanup with a detailed message naming 5ev7 and the public parser coverage boundary.

### Task 5: Give StreamingText an overridable quiet-ink hook (gfaj)

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/streamingtext.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/thinkblock.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/messages/ThinkBlock.test.tsx`

- [ ] **Step 1: Write the failing cross-file contract**

Read the two CSS modules after stripping comments. Assert that StreamingText’s root declares `color: var(--prose-ink, var(--ink-hi));` and ThinkBlock’s live paragraph sets `--prose-ink: var(--ink-mid);`.

- [ ] **Step 2: Run the focused test and verify the expected failure**

Run `npm test -- --run src/panes/session/transcript/messages/ThinkBlock.test.tsx -t "shared prose ink"`. It must fail against the current direct `--ink-hi` and direct paragraph color declarations.

- [ ] **Step 3: Implement the minimal CSS hook**

Replace StreamingText’s direct color with the shared fallback hook and replace ThinkBlock’s live paragraph color declaration with the custom-property override. Do not alter other StreamingText callers or the settled Markdown hook.

- [ ] **Step 4: Run the focused tests and browser check if available**

Run ThinkBlock, StreamingText, and AgentMessageItem tests. Use a real browser to inspect the widget/transcript colors when available; otherwise record the environment limitation.

- [ ] **Step 5: Commit**

Commit the CSS and cross-file contract with a detailed message naming gfaj and the live-thinking contrast regression.

### Task 6: Final verification and kata handoff

- [ ] Run the complete relevant frontend suite, typecheck, lint, production build, `layoutguard`, `overflowguard`, root `make build-runtime`, and `git diff --check`.
- [ ] Review the complete diff for scope, tests, comments, and accidental changes under `src/shell/rail/**` or controller-owned files.
- [ ] Add substantive kata comments to ghv3, arx3, zscn, 5ev7, and gfaj naming the ready commits and test evidence. Do not close or merge any issue.
- [ ] Search kata before creating any issue for unrelated defects; if one is genuinely separate, create it with a stable idempotency key, relate it with `kata edit --related`, and report its id. Do not silently broaden this batch.
