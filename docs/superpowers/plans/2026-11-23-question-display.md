# Question Display Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restyle live and historical web questions with the approved neutral frame and clearer question/option hierarchy on desktop and mobile web.

**Architecture:** Keep the existing AskDock state, transcript parsing, and controlled answer components. Change only their presentational markup and CSS, then mirror the typography and option hierarchy in the read-only transcript renderer.

**Tech Stack:** React 19, TypeScript, CSS Modules, Vitest, Testing Library, Chrome layoutguard.

**Spec:** `docs/superpowers/specs/2026-11-23-question-display-design.md`

## Global Constraints

- Preserve answering, notes, free text, multi-select, tabs, automatic advancement, submission, recovery, keyboard behavior, and live announcements.
- Use existing widgets and design tokens; add no widget API, palette, font, or native-app change.
- Preserve semantic controls, accessible names, focus handling, wrapping, and the 44px coarse-pointer target floor.
- Update only visual assertions superseded by the approved design; do not add brittle tests for deleted CSS or copy.

## Review Focus

- Long or unbroken model-authored labels and details must wrap inside the neutral frame; the existing mobile layoutguard owns this.
- Options without details and `Something else…` must retain comfortable target sizes; the existing option-row CSS check owns this.
- Switching between an option and free text must preserve focus and mutual exclusion; existing AskQuestionCard behavior tests own this.
- Multi-question batches must retain numbered tabs even though single cards lose their numeric prefix; existing AskDock tab tests own this.
- Read-only history must remain noninteractive and retain its parsing fallbacks; existing askUser renderer tests own this. Live and history share `AskOptionCopy`, so their wrapping rule cannot drift apart.

---

### Task 1: Neutral-frame question hierarchy

**Files:**
- Create: `cmd/evener-hub/frontend/src/panes/session/AskOptionCopy.tsx`
- Create: `cmd/evener-hub/frontend/src/panes/session/askoptioncopy.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/AskDock.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/askdock.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/AskQuestionCard.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/askquestioncard.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/AskDock.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/composer/askDock/AskQuestionCard.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/askUser.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/askuser.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/tools/askUser.test.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/askdock-mobile-tall-crush/harness.html`
- Modify: `cmd/evener-hub/frontend/scripts/layoutguard/cases/askdock-mobile-tall-crush/case.json`
- Create: `cmd/evener-hub/frontend/scripts/layoutguard/cases/askuser-history-long-label/{case.json,harness.html,assert.mjs}`
- Modify: `docs/web-ui/design-system.md`

**Interfaces:**
- Consumes: existing `AskQuestionCardProps`, `AskResolution`, `Card`, and read-only `AskUserQuestion` data.
- Produces: no new production API. `AskQuestionCard` drops its now-unused `number` prop; all callers and test harnesses drop it too.

- [x] **Step 1: Confirm the focused baseline**

Run from `cmd/evener-hub/frontend`:

```bash
npm test -- src/panes/session/composer/askDock/AskQuestionCard.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx src/panes/session/transcript/tools/askUser.test.tsx
```

Expected: all listed tests and the package's script tests pass before edits.

- [x] **Step 2: Change the existing visual tests to the approved structure**

In `AskDock.test.tsx`, extend the pending-question render test to require the batch's `Card` to carry the neutral `outlined` class. In `AskQuestionCard.test.tsx`, replace the numeric-prefix and continuous-inline-prose assertions with checks that the header/question remain available and each option has a copy wrapper containing separate label-line and detail elements. Remove `number` from the controlled harness. Keep the existing wrap, recommendation, grouping, free-text, multi-select, focus, and touch-floor checks.

In `askUser.test.tsx`, extend the existing body test so each option's detail lives in the option copy stack after its label line. Do not add standalone copy-presence tests.

- [x] **Step 3: Prove the changed assertions fail before implementation**

Run the Step 1 command.

Expected: FAIL because the batch still uses `attention` instead of `outlined`, and options still lack the new copy/label-line structure. Confirm those scenarios are the failures before editing production code.

- [x] **Step 4: Implement the live neutral frame**

In `AskDock.tsx`, render each batch as `<Card variant="outlined">` without `tone="attention"`. Move the visual question cue inside that frame and render the approved small amber dot plus `Question` label; leave `AskDockAnnouncements` unchanged. In `askdock.module.css`, replace the old anchor styling with the compact cue/dot layout using tokens and retain all dock, tab, footer, and touch-target contracts.

- [x] **Step 5: Implement the live question and option hierarchy**

In `AskQuestionCard.tsx`, remove the per-card number and unused prop. Keep the header and prose question, then structure every option as the native input plus a copy stack: a label line containing the label and optional recommendation, followed by the detail. Use the same row geometry for `Something else…` and keep all event handlers unchanged.

In `askquestioncard.module.css`, remove the nested white question surface. Use larger semibold prose type with the shared prose leading for the question, a grid for input/copy alignment, a flex-wrapping label line, and a block detail with secondary ink. Preserve selected/hover washes, focus rings, wrapping, field behavior, and coarse-pointer sizing with existing tokens.

- [x] **Step 6: Run the live question tests**

Run:

```bash
npm test -- src/panes/session/composer/askDock/AskQuestionCard.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx
```

Expected: PASS, including all unchanged interaction, recovery, focus, and tab tests.

- [x] **Step 7: Align read-only history and its fixture**

In `askUser.tsx` and `askuser.module.css`, keep the read-only `Card` and controls-free rendering while applying the same strong prose question, label-line/detail stack, and quiet recommendation treatment. Share that stack through `AskOptionCopy` and `askoptioncopy.module.css` so live and historical labels cannot drift. Do not add live attention or answer controls.

Update `askdock-mobile-tall-crush/harness.html` to mirror the new neutral card, cue, and option markup. Keep its real-browser measurements for label/detail separation, unbroken-token wrapping, transcript scrolling, and unchanged pane allocation. Update `case.json`'s description and mutation note to describe the grid/copy-stack layout while retaining the existing `overflow-wrap` mutation oracle. Add `askuser-history-long-label` to prove the read-only flex label also wraps an unbroken token inside its card.

- [x] **Step 8: Update the evergreen design contract**

In `docs/web-ui/design-system.md`, describe AskDock as a neutral outlined frame with a small attention cue. Remove the obsolete statement that pending AskDock batches use the amber attention envelope; leave the sandbox-approval contract intact.

- [ ] **Step 9: Run focused and real-browser checks**

Run from `cmd/evener-hub/frontend`:

```bash
npm test -- src/panes/session/composer/askDock/AskQuestionCard.test.tsx src/panes/session/composer/askDock/AskDock.test.tsx src/panes/session/transcript/tools/askUser.test.tsx
npm run typecheck
npm run layoutguard -- askdock-mobile-tall-crush
npm run layoutguard -- askuser-history-long-label
```

Expected: every command exits zero; layoutguard reports no overlap, horizontal escape, internal dock scroll, or pane-allocation change.

Run Biome on every touched file under `src/`, then run `make test-web` and `make test-web-browser` from the repository root, one at a time. Leave the out-of-scope layoutguard HTML on its existing formatting. Expected: both gates exit zero.

- [x] **Step 10: Review actual rendering**

Use the real `/dev/surfaces` composer fixture or equivalent real component preview. Inspect neutral light and dark themes at desktop and phone widths with selected, long-content, free-text, and multi-select states. Confirm the result matches approved mockup B and no orange batch wrapper remains. Record any unverified state explicitly.

- [ ] **Step 11: Review and commit the complete change**

Inspect `git diff --check`, the full diff, and `git status`. Stage only the plan's files plus the approved design and this plan. Commit with a message naming the neutral question display, followed by the required Evener provenance trailer.
