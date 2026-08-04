# Web UI View Modes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Everything, Conversation, and Intent session views with stable mode-switch scrolling and end-position session opening.

**Architecture:** Keep transcript data unchanged and derive a mode-specific view model from the existing session turns and message/tool registries. Add the selector and view transformation at the session transcript boundary, while extending the existing `useTranscriptScroll` seam for anchor preservation and initial end scrolling. Keep Everything on the current `TurnBlock` path.

**Tech Stack:** React, TypeScript, Vite, Vitest, Testing Library, existing `VirtualList` and session stores.

## Global Constraints

- Everything must preserve the current UI, including tool calls and results.
- Conversation renders user/agent messages and centered italic tool-count dividers only.
- Intent renders user/agent messages and associated rationales, but no raw tool calls, arguments, or results.
- Switching modes preserves the same content position at the top of the viewport when possible.
- Opening a session always scrolls to the end after initial content renders, never to an interstitial point.
- Missing metadata must not prevent user/agent messages from rendering; missing rationales produce no Intent entry.
- Default tests must remain deterministic and must not require provider credentials or network access.

---

## File map

- Modify `cmd/serf-hub/frontend/src/panes/session/Session.tsx`: own the selected mode, render the selector, and choose Everything versus the derived focused transcript.
- Create `cmd/serf-hub/frontend/src/panes/session/viewModes.ts`: define mode types and pure transformation/grouping helpers.
- Create `cmd/serf-hub/frontend/src/panes/session/viewModes.test.ts`: test labels, grouping, filtering, ordering, and missing metadata.
- Modify `cmd/serf-hub/frontend/src/panes/session/session.module.css`: style the compact selector and focused-mode entries without changing Everything styles.
- Modify `cmd/serf-hub/frontend/src/panes/session/Session.test.tsx`: test selector behavior, mode rendering, accessibility, and initial selection.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts`: expose stable anchor capture/restoration and enforce initial scroll-to-end.
- Modify `cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts`: test anchor restoration and opening at the end.
- Modify `cmd/serf-hub/frontend/src/panes/session/Session.tsx` or add `cmd/serf-hub/frontend/src/panes/session/viewModeScroll.ts` only if the scroll hook needs a small dedicated anchor utility; keep anchor identity based on stable turn/event IDs.

## Interfaces

Use these interfaces unless existing local types require an equivalent adapter:

```ts
export type SessionViewMode = "everything" | "conversation" | "intent";

export const SESSION_VIEW_MODES: readonly {
  value: SessionViewMode;
  label: "Everything" | "Conversation" | "Intent";
}[];

export type FocusedEntry =
  | { kind: "message"; id: string; turnId: string; role: "user" | "agent"; message: unknown }
  | { kind: "tool-count"; id: string; turnId: string; count: number }
  | { kind: "intent"; id: string; turnId: string; rationale: string };

export function focusedEntries(turns: readonly unknown[], mode: "conversation" | "intent"): FocusedEntry[];
```

Use the repository’s concrete transcript/message types in place of `unknown` when implementing; the signatures above define the boundary, not a request to weaken types.

---

### Task 1: Define pure view-mode transformations

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/viewModes.ts`
- Create: `cmd/serf-hub/frontend/src/panes/session/viewModes.test.ts`

**Interfaces:**
- Consumes: the existing turn/message/tool data types imported from the session transcript modules.
- Produces: `SessionViewMode`, `SESSION_VIEW_MODES`, and `focusedEntries()` for the session renderer.

- [ ] **Step 1: Write failing transformation tests**

Create fixture turns containing user messages, agent messages, two contiguous tool calls, a message between tool calls, and rationale-bearing tool calls. Assert:

```ts
expect(SESSION_VIEW_MODES.map((mode) => mode.label)).toEqual([
  "Everything",
  "Conversation",
  "Intent",
]);
expect(focusedEntries(fixtures, "conversation")).toEqual([
  expect.objectContaining({ kind: "message", role: "user" }),
  expect.objectContaining({ kind: "tool-count", count: 2 }),
  expect.objectContaining({ kind: "message", role: "agent" }),
]);
expect(focusedEntries(fixtures, "intent").map((entry) => entry.kind)).toEqual([
  "message",
  "intent",
  "intent",
  "message",
]);
```
Also assert `1 tool call` and `3 tool calls` formatting data, no raw tool fields in focused entries, missing rationale omission, missing tool metadata tolerance, and stable IDs.

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/viewModes.test.ts`
Expected: FAIL because the transformation module does not exist.

- [ ] **Step 3: Implement the minimal pure transformation**

Inspect the concrete `TurnBlock`/message/tool registry types. Walk the source sequence once, emit user/agent messages, collapse contiguous hidden tools into one count entry for Conversation, and emit only non-empty rationale entries for Intent. Generate deterministic entry IDs from the underlying event/turn IDs; do not mutate source turns.

- [ ] **Step 4: Run focused tests**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/viewModes.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/viewModes.ts cmd/serf-hub/frontend/src/panes/session/viewModes.test.ts
git commit -m "feat(web): derive focused session view entries"
```

### Task 2: Add the three-mode selector and focused rendering

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/Session.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/session.module.css`
- Modify: `cmd/serf-hub/frontend/src/panes/session/Session.test.tsx`

**Interfaces:**
- Consumes: `SESSION_VIEW_MODES`, `SessionViewMode`, and `focusedEntries()` from Task 1.
- Produces: an accessible selector with exactly `Everything`, `Conversation`, `Intent`; Everything uses the existing `TurnBlock`/`VirtualList` path.

- [ ] **Step 1: Add failing component tests**

Render a hydrated fixture session and assert the header exposes a radio group with exactly the three labels, `Everything` is selected initially, and the existing tool content is visible. Click `Conversation`; assert user/agent text remains, raw tool text disappears, and `3 tool calls` appears. Click `Intent`; assert rationale text appears and raw tool text/results do not. Use keyboard interaction to change the selected radio and assert `aria-checked`/native checked state.

- [ ] **Step 2: Run the component tests and verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/Session.test.tsx`
Expected: FAIL because the selector and focused renderer are not implemented.

- [ ] **Step 3: Implement selector and render branches**

Add local `useState<SessionViewMode>("everything")`. Render the selector in the existing session header/title area using the repository’s established radio pattern. Keep Everything’s current `VirtualList` and `TurnBlock` untouched. For focused modes, render transformed entries with stable keys and existing message presentation where possible; render a centered italic count divider for `tool-count` and subdued rationale text for `intent`.

Do not add persistence or network writes. Unknown mode values must normalize to Everything.

- [ ] **Step 4: Add focused-mode CSS**

Use existing CSS variables and theme conventions. Keep selector compact, style the active state, make count text centered and italic, and make rationale subordinate to messages. Add responsive wrapping at the existing mobile breakpoint without changing Everything layout.

- [ ] **Step 5: Run component tests**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/Session.test.tsx`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/Session.tsx cmd/serf-hub/frontend/src/panes/session/session.module.css cmd/serf-hub/frontend/src/panes/session/Session.test.tsx
git commit -m "feat(web): add session view mode selector"
```

### Task 3: Preserve scroll anchor across mode changes and open at end

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/Session.tsx`
- Create only if needed: `cmd/serf-hub/frontend/src/panes/session/viewModeScroll.ts`

**Interfaces:**
- Consumes: stable focused-entry IDs and the existing `VirtualListHandle`/scroll state.
- Produces: a mode-change scroll API that captures the top visible stable entry and restores its offset after the new mode renders; an initial-open path that lands at the final transcript item.

- [ ] **Step 1: Write failing scroll tests**

Using the existing scroll-hook test seams, assert:

```ts
const anchor = captureTopAnchor({ id: "turn-4", offset: 18 });
expect(restoreTopAnchor(anchor, [{ id: "turn-4", offset: 18 }])).toEqual({ id: "turn-4", offset: 18 });
```
Add a mode-switch test where hidden tool entries change list height but the nearest surviving user/agent entry stays at the same viewport offset. Add a fallback test for a hidden anchor, and an initial-open test asserting `scrollToIndex(turnCount - 1)` after initial content is available, even when an interstitial marker exists.

- [ ] **Step 2: Run focused scroll tests and verify failure**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/flow/useTranscriptScroll.test.ts`
Expected: FAIL because the new anchor/open behavior is absent.

- [ ] **Step 3: Implement anchor capture/restoration**

Extend the hook with a mode-change signal or explicit `preserveAnchor(mode)` callback. Before changing mode, identify the first visible stable entry and record its ID plus pixel offset from the scroll container top. After the focused list commits, locate the same ID; if hidden, locate the nearest surrounding user/agent entry; otherwise restore normalized scroll proportion. Schedule restoration after virtual-list measurement, using the existing hook scheduling pattern rather than arbitrary polling.

- [ ] **Step 4: Enforce initial end position**

On first hydrated session render, scroll to the last transcript item after the list has content and measurements. Ensure this runs after any persisted/interstitial position is applied, so opening always ends at the newest content. Do not run this end jump on ordinary mode switches or subsequent live updates when the reader is intentionally away from the end.

- [ ] **Step 5: Run scroll tests**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/flow/useTranscriptScroll.test.ts`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.ts cmd/serf-hub/frontend/src/panes/session/transcript/flow/useTranscriptScroll.test.ts cmd/serf-hub/frontend/src/panes/session/Session.tsx cmd/serf-hub/frontend/src/panes/session/viewModeScroll.ts
git commit -m "feat(web): stabilize session scroll across views"
```

### Task 4: Run frontend gates and review regressions

**Files:**
- Modify only files required by formatter or test fixes from Tasks 1–3.

- [ ] **Step 1: Format touched frontend files**

Run: `cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/viewModes.ts src/panes/session/viewModes.test.ts src/panes/session/Session.tsx src/panes/session/Session.test.tsx src/panes/session/session.module.css src/panes/session/transcript/flow/useTranscriptScroll.ts src/panes/session/transcript/flow/useTranscriptScroll.test.ts`
Expected: Biome completes without errors.

- [ ] **Step 2: Run canonical frontend gate**

Run: `make test-web`
Expected: PASS for frontend unit tests, typecheck, and Biome checks.

- [ ] **Step 3: Run browser gate when available**

Run: `make test-web-browser`
Expected: PASS, including real geometry and scroll behavior guards. If Chrome is unavailable, report that limitation rather than skipping silently.

- [ ] **Step 4: Inspect the final diff**

Run: `git diff HEAD~3 --check` and `git status --short`.
Expected: no whitespace errors; only intended implementation files are changed, and pre-existing unrelated changes remain untouched.

- [ ] **Step 5: Commit gate fixes if any**

```bash
git add <only-the-gate-fix-files>
git commit -m "test(web): verify session view modes"
```
