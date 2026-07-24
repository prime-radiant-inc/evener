# Web UI — UX Round 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the twelve round-2 UX defects Jesse reported clicking around the built SPA (c8gt, 3w2p, vbh8, 4y12, yhmh, 9ct0, qb8e, yd16, tv5k, 677w, yt2q, zrzr) and add a deep foldable subagent tree to the sidebar.

**Architecture:** Pure frontend work in `cmd/serf-hub/frontend` — the §4.3 reversal (spec, 2026-07-23) sources the subagent summary and live status from the child-transcript watch the card already opens, so there is **no Go/wire/codegen change and no generated-types chokepoint**. Work is partitioned into six file-disjoint streams that run in parallel, plus two small shared-infra prerequisites (a store-backed `Disclosure` widget, and a shared floating-popover primitive extracted from the existing Menu) that two streams depend on.

**Tech Stack:** React 19, TypeScript 6 (strict, `noUncheckedIndexedAccess`), Vite, vanilla-zustand stores, dockview, vitest + jsdom, biome. Design tokens in `src/styles/tokens.css` enforced by `src/styles/token-contract.test.ts`.

## Global Constraints

- **Color:** every color resolves to `var(--…)` from `src/styles/tokens.css`; never a hex/rgb/hsl/oklch/oklab/lab/lch literal, **including in comments** (`src/styles/token-contract.test.ts` greps the raw file text). `color-mix(...)` over token vars is allowed.
- **One meaning per hue:** `--alive` green = agent working/streaming/running; `--attention` amber = **a human is needed, nothing else**; `--danger` red = failure/destructive; `--accent` steel-blue = focus ring/selection/links (ungated, allowed anywhere); `--ink-low` grey = idle/ended/timestamps/placeholders.
- **Attention-hue allowlist:** only widgets in `token-contract.test.ts`'s `SEMANTIC_USE_ALLOWLIST` (cadence, button, chip, badge, statusdot, meter, diffblock, toast, dialog, formrow, collectioneditor) may use `var(--attention|--alive|--danger)` (and their `-bg` companions). Components under `src/shell/**` and `src/panes/**/chrome/**` may use only `--accent`/`--surface-*`/`--ink-*`/`--edge`. A **new** widget that needs a status hue must (a) live at `src/widgets/<name>/<name>.module.css` (directory basename === file basename, per `WIDGET_STYLESHEET_RE`) **and** (b) be added to `SEMANTIC_USE_ALLOWLIST`. Prefer composing the existing allowlisted `Cadence`/`StatusDot`/`Badge`/`Chip` widgets so no allowlist edit is needed.
- **New color token:** if ever added, it MUST appear in **both** the dark and light `:root` blocks of `tokens.css` (the contract test fails on dark/light drift). This plan adds none.
- **Motion budget:** only `--motion-duration-attention` (200ms attention-onset color transition) and `--motion-duration-overlay` (120ms overlay fade-scale). No idle pulses, shimmer, or faked liveness. A "running" indicator shows honest state.
- **Status widgets:** reuse `StatusDot` (`widgets/statusdot`, 8px dot, states `working|needs-you|failed|idle|ended`) and `Cadence` (`widgets/cadence`, props `{state, frameTimes: number[], now: number}` all required, pure render, 64×10 SVG) — never reinvent them.
- **Test harness:** `npm run typecheck` (`tsc --noEmit`), `npm test` (`vitest run`), `npm run lint` (`biome ci src`). Run these from `cmd/serf-hub/frontend`. Tests wrap components manually (no shared render helper) and call `cleanup()` per file; use `requireClass` for CSS-module class access.
- **Testing posture (Jesse's steer this phase):** UI/vitest test failures are **not** merge blockers while the UX is being made right (baseline tracked in kata 4wgg). Compile gates — `tsc`, `biome`, `build` — **do** hold. New behavior still gets tests per TDD; the token-contract test in particular MUST stay green.
- **No Go change:** this plan touches only `cmd/serf-hub/frontend/**`. If a task finds itself editing a `.go` file or `types.gen.ts`, stop — that means the §4.3 reversal was misread.

## Stream Map (parallelization)

Streams are file-disjoint and can run concurrently, with three ordering edges: **S0** (shared widgets) must land before **S4**/**S5** (they import `Disclosure`/`Popover`); **S1** (reducer) produces `ItemModel.description` that **S4** consumes; and **S2 → S3** are *not* disjoint — both edit `Rail.tsx` and `Rail.module.css`, so S3 runs after S2 (S2 lays the header/footer scaffold + its styles; S3 then edits only the body's section rendering + adds `.activity`/`.time`/guide-rail rules). Everything else is exclusive and parallel. Within a stream, tasks are sequential (S4 especially: Task 9 plumbing → Task 10 card → Task 11 migration).

| Stream | Tasks | Owns (files) | Depends on |
|---|---|---|---|
| **S0 shared-infra** | 1, 2 | `widgets/disclosure/**`, `widgets/popover/**` | — |
| **S1 reducer** | 3, 4 | `protocol/reducer.ts`, `protocol/model.ts` (+ their tests) | — |
| **S2 shell frame** | 5, 6 | `shell/rail/useSidebarMode.ts`, `shell/rail/RailHost.tsx`, `shell/rail/Rail.tsx` + `Rail.module.css`, `panes/settings/sections/theme.tsx` (copy only, line 101), `stores/prefs.ts` (comment only) | — |
| **S3 sidebar tree** | 7, 8 | `shell/rail/RailRow.tsx`, `shell/rail/railNodes.ts`, `shell/rail/Rail.module.css`, `shell/rail/Rail.tsx` sections | S2 (same `Rail.tsx`/`Rail.module.css`) — sequence S3 after S2 |
| **S4 subagent card** | 9, 10, 11 | `panes/session/transcript/tools/subagentModule.tsx`, `watchedChild.tsx`, `stores/threads.ts` (watch params), `subagentModuleStore.ts` | S0 (Disclosure), S1 (`description`) |
| **S5 spawn form** | 12, 13, 14 | `widgets/modelCatalog/**`, `panes/spawn/**`, `panes/settings/sections/launchShared/fields.tsx` | S0 (Popover) |
| **S6 composer** | 15 | `panes/session/composer/Composer.tsx` + `composer/attachments/**` | — |

`Rail.tsx` is shared by S2 (§1.1 header/footer) and S3 (§2 tree sections). Assign `Rail.tsx` to **one** implementer (S2 first: it adds the header/footer scaffold; S3 then edits only the body's section rendering). Everything else is exclusive.

---

## S0 — Shared infrastructure

### Task 1: Store-backed `Disclosure` widget (yt2q core)

Disclosure open/closed state currently lives in component-local `useState` / uncontrolled `<details>` and dies when `VirtualList` unmounts an off-window row or dockview remounts the pane tree. This task builds a shared widget whose open/closed state lives in a module store keyed by a stable id, so it survives remount. It also carries the rotating-chevron affordance §4.1 needs.

**Files:**
- Create: `cmd/serf-hub/frontend/src/widgets/disclosure/disclosureStore.ts`
- Create: `cmd/serf-hub/frontend/src/widgets/disclosure/index.tsx`
- Create: `cmd/serf-hub/frontend/src/widgets/disclosure/disclosure.module.css`
- Test: `cmd/serf-hub/frontend/src/widgets/disclosure/disclosureStore.test.ts`
- Test: `cmd/serf-hub/frontend/src/widgets/disclosure/disclosure.test.tsx`

**Interfaces:**
- Produces:
  - `disclosureStore.ts`: `isDisclosureOpen(id: string, fallback: boolean): boolean` (reactive hook — calls `useStore`), `setDisclosureOpen(id: string, open: boolean): void`, `toggleDisclosure(id: string, fallback: boolean): void`, `resetDisclosureStoreForTests(): void`.
  - `index.tsx`: `Disclosure(props: DisclosureProps)` where
    ```ts
    export interface DisclosureProps {
      id: string;                 // stable key; state survives remount
      summary: React.ReactNode;   // the always-visible summary row content
      children: React.ReactNode;  // the collapsible body
      defaultOpen?: boolean;      // fallback used only when the store has no entry (default false)
      "data-testid"?: string;
    }
    ```
- Consumed by: Task 9 (subagent card), Task 11 (native `<details>` migration). The `disclosureStore` pattern mirrors `panes/session/transcript/tools/subagentModuleStore.ts` (vanilla `createStore` + `useStore`, singleton, `resetSubagentModuleStoreForTests`).

- [ ] **Step 1: Write the failing store test**

Create `src/widgets/disclosure/disclosureStore.test.ts`:
```ts
import { afterEach, expect, test } from "vitest";
import { isDisclosureOpen, resetDisclosureStoreForTests, setDisclosureOpen, toggleDisclosure } from "./disclosureStore";

afterEach(() => resetDisclosureStoreForTests());

test("unset id reports the fallback", () => {
  expect(isDisclosureOpen("a", false)).toBe(false);
  expect(isDisclosureOpen("a", true)).toBe(true);
});

test("setDisclosureOpen overrides the fallback and persists", () => {
  setDisclosureOpen("a", true);
  expect(isDisclosureOpen("a", false)).toBe(true);
  setDisclosureOpen("a", false);
  expect(isDisclosureOpen("a", true)).toBe(false);
});

test("toggle flips from the fallback then from stored state", () => {
  toggleDisclosure("a", false); // fallback false -> true
  expect(isDisclosureOpen("a", false)).toBe(true);
  toggleDisclosure("a", false); // stored true -> false
  expect(isDisclosureOpen("a", false)).toBe(false);
});
```
Note: `isDisclosureOpen` is a reactive hook (uses `useStore`), but its return is a plain boolean and these assertions read it outside React — that works because vanilla-zustand `useStore(store, selector)` called outside a component still returns the current snapshot. If the harness rejects a hook call outside render, wrap reads in a tiny `renderHook`-free probe component; keep the store's non-hook `getState()` accessor available for that. Prefer the direct form first.

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/widgets/disclosure/disclosureStore.test.ts`
Expected: FAIL — module `./disclosureStore` does not exist.

- [ ] **Step 3: Implement the store**

Create `src/widgets/disclosure/disclosureStore.ts`, mirroring `subagentModuleStore.ts`'s idiom (note the split import: `useStore` from `"zustand"`, `createStore` from `"zustand/vanilla"` — this is the exact pattern in `subagentModuleStore.ts:19-20`):
```ts
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";

interface DisclosureState {
  open: Map<string, boolean>;
}

const store = createStore<DisclosureState>(() => ({ open: new Map() }));

/** Reactive: re-renders the caller when this id's open state changes. */
export function isDisclosureOpen(id: string, fallback: boolean): boolean {
  return useStore(store, (s) => s.open.get(id) ?? fallback);
}

export function setDisclosureOpen(id: string, open: boolean): void {
  store.setState((s) => {
    const next = new Map(s.open);
    next.set(id, open);
    return { open: next };
  });
}

export function toggleDisclosure(id: string, fallback: boolean): void {
  const current = store.getState().open.get(id) ?? fallback;
  setDisclosureOpen(id, !current);
}

export function resetDisclosureStoreForTests(): void {
  store.setState({ open: new Map() });
}
```
Confirm zustand's `createStore`/`useStore` import path matches `subagentModuleStore.ts` exactly (read it first — it is the authoritative local example).

- [ ] **Step 4: Run the store test, verify it passes**

Run: `npx vitest run src/widgets/disclosure/disclosureStore.test.ts`
Expected: PASS (3 tests).

- [ ] **Step 5: Write the failing widget test**

Create `src/widgets/disclosure/disclosure.test.tsx`:
```tsx
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Disclosure } from "./index";
import { resetDisclosureStoreForTests } from "./disclosureStore";

afterEach(() => { cleanup(); resetDisclosureStoreForTests(); });

test("starts collapsed by default; clicking the summary expands", () => {
  render(<Disclosure id="d1" summary="Head" data-testid="d">Body</Disclosure>);
  expect(screen.queryByText("Body")).toBeNull();
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
});

test("open state survives remount because it lives in the store", () => {
  const { unmount } = render(<Disclosure id="keep" summary="Head">Body</Disclosure>);
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
  unmount();
  render(<Disclosure id="keep" summary="Head">Body</Disclosure>);
  expect(screen.getByText("Body")).toBeTruthy(); // still open after remount
});

test("defaultOpen renders open when the store has no entry", () => {
  render(<Disclosure id="d2" summary="Head" defaultOpen>Body</Disclosure>);
  expect(screen.getByText("Body")).toBeTruthy();
});
```

- [ ] **Step 6: Run it, verify it fails**

Run: `npx vitest run src/widgets/disclosure/disclosure.test.tsx`
Expected: FAIL — `./index` has no `Disclosure` export.

- [ ] **Step 7: Implement the widget + stylesheet**

Create `src/widgets/disclosure/index.tsx`. Model the controlled-summary behavior on `ToolCallItem.tsx:101-137` (preventDefault on the native summary, drive `open` from state) but source state from the store:
```tsx
import type { DisclosureProps } from "./types-inline"; // or inline the interface here
import { isDisclosureOpen, toggleDisclosure } from "./disclosureStore";
import { requireClass } from "../internal/requireClass";
import styles from "./disclosure.module.css";

const CLASS = {
  details: requireClass(styles.details, "disclosure.module.css", "details"),
  summary: requireClass(styles.summary, "disclosure.module.css", "summary"),
  chevron: requireClass(styles.chevron, "disclosure.module.css", "chevron"),
  body: requireClass(styles.body, "disclosure.module.css", "body"),
};

export interface DisclosureProps {
  id: string;
  summary: React.ReactNode;
  children: React.ReactNode;
  defaultOpen?: boolean;
  "data-testid"?: string;
}

export function Disclosure({ id, summary, children, defaultOpen = false, ...rest }: DisclosureProps) {
  const open = isDisclosureOpen(id, defaultOpen);
  return (
    <details className={CLASS.details} open={open} data-testid={rest["data-testid"]}>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; see ToolCallItem.tsx */}
      <summary
        className={CLASS.summary}
        onClick={(e) => {
          e.preventDefault();
          toggleDisclosure(id, defaultOpen);
        }}
      >
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"}>▸</span>
        {summary}
      </summary>
      {open && <div className={CLASS.body}>{children}</div>}
    </details>
  );
}
```
Create `src/widgets/disclosure/disclosure.module.css` — chevron rotates via a `--motion-duration-overlay` transform, uses only `--ink-*` (no status hue, so no allowlist entry needed):
```css
.details { display: block; }
.summary {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
  list-style: none;
  color: var(--ink-mid);
}
.summary::-webkit-details-marker { display: none; }
.chevron {
  display: inline-flex;
  color: var(--ink-low);
  font-size: 10px;
  transition: transform var(--motion-duration-overlay) ease;
}
.chevron[data-open="true"] { transform: rotate(90deg); }
.body { margin-top: var(--space-1); }
```
Confirm `--space-1`/`--space-2`/`--motion-duration-overlay` exist in `tokens.css` before use (read it; do not invent token names).

- [ ] **Step 8: Run the widget test, verify it passes**

Run: `npx vitest run src/widgets/disclosure/disclosure.test.tsx`
Expected: PASS (3 tests).

- [ ] **Step 9: Guard the token contract + compile**

Run: `npx vitest run src/styles/token-contract.test.ts && npm run typecheck`
Expected: PASS. (The new widget uses no color literal and no gated hue, so the contract stays green with no allowlist edit.)

- [ ] **Step 10: Commit**

```bash
git add src/widgets/disclosure
git commit -m "feat(disclosure): store-backed Disclosure widget surviving remount (yt2q core)"
```

### Task 2: Shared floating `Popover` primitive (§3.4)

§3.4 makes every combobox/picker an out-of-flow overlay that never reflows page content. The flip-then-clamp placement + portal already exist but are private to `widgets/menu/index.tsx` (`EDGE_MARGIN`, `TRIGGER_GAP`, `resolveAxis`, `computeMenuPosition`, `createPortal` to `document.body`). Extract the placement math into a shared module so the model combobox (Task 12) and the directory popover (Task 13) reuse the exact same behavior.

**Files:**
- Create: `cmd/serf-hub/frontend/src/widgets/popover/computePosition.ts`
- Create: `cmd/serf-hub/frontend/src/widgets/popover/index.tsx`
- Create: `cmd/serf-hub/frontend/src/widgets/popover/popover.module.css`
- Test: `cmd/serf-hub/frontend/src/widgets/popover/computePosition.test.ts`
- Modify: `cmd/serf-hub/frontend/src/widgets/menu/index.tsx` (re-import the extracted math instead of the local copies — behavior-preserving)

**Interfaces:**
- Produces:
  - `computePosition.ts`: `EDGE_MARGIN = 8`, `TRIGGER_GAP = 4`, `resolveAxis(primary, flipped, size, viewportSize): number`, `computePopoverPosition(triggerRect: DOMRect, popupSize: { width: number; height: number }): { left: number; top: number }`.
  - `index.tsx`: `Popover(props)`:
    ```ts
    export interface PopoverProps {
      open: boolean;
      onClose: () => void;               // fired on outside click, Escape, scroll, or resize
      trigger: React.ReactElement;       // rendered in-flow; its rect anchors the panel
      children: React.ReactNode;         // the floating panel content
      "data-testid"?: string;
    }
    ```
    Renders `trigger` inline; when `open`, portals `children` to `document.body` at the computed fixed position, re-measuring on open and closing on scroll/resize (mirror Menu's documented behavior at `widgets/menu/index.tsx:142-166`).
- Consumed by: Task 12 (model combobox), Task 13 (dir picker).

- [ ] **Step 1: Write the failing placement test**

Create `src/widgets/popover/computePosition.test.ts`. These pin the flip-then-clamp semantics documented at `menu/index.tsx:111-140`:
```ts
import { expect, test } from "vitest";
import { computePopoverPosition, resolveAxis, EDGE_MARGIN, TRIGGER_GAP } from "./computePosition";

test("resolveAxis returns primary when it fits", () => {
  expect(resolveAxis(10, 500, 100, 1000)).toBe(10);
});
test("resolveAxis flips when primary overflows and flipped fits", () => {
  // primary 950 + size 100 = 1050 > 1000 - 8; flipped 40 >= 8 -> flip
  expect(resolveAxis(950, 40, 100, 1000)).toBe(40);
});
test("resolveAxis clamps when neither side fits", () => {
  // primary 950 overflows; flipped -5 < 8 -> clamp into [8, 1000-100-8=892]
  expect(resolveAxis(950, -5, 100, 1000)).toBe(892);
});
test("computePopoverPosition opens below the trigger by TRIGGER_GAP", () => {
  const rect = { left: 20, right: 120, top: 30, bottom: 50 } as DOMRect;
  const pos = computePopoverPosition(rect, { width: 80, height: 40 });
  expect(pos.top).toBe(50 + TRIGGER_GAP);
  expect(pos.left).toBe(20);
  expect(EDGE_MARGIN).toBe(8);
});
```

- [ ] **Step 2: Run it, verify it fails**

Run: `npx vitest run src/widgets/popover/computePosition.test.ts`
Expected: FAIL — module missing.

- [ ] **Step 3: Extract the placement math**

Create `src/widgets/popover/computePosition.ts` by copying the constants + `resolveAxis` + renaming `computeMenuPosition` → `computePopoverPosition` verbatim from `widgets/menu/index.tsx:108-140`. Do not change the math — this is a pure move so both Menu and Popover share one implementation. `window.innerWidth/innerHeight` reads stay inside `computePopoverPosition` exactly as in the original.

- [ ] **Step 4: Run the placement test, verify it passes**

Run: `npx vitest run src/widgets/popover/computePosition.test.ts`
Expected: PASS (4 tests).

- [ ] **Step 5: Implement the Popover component + point Menu at the shared math**

Create `src/widgets/popover/index.tsx` — a general trigger+portal wrapper using `computePopoverPosition`, the measure-after-mount `useLayoutEffect` pattern, and the scroll/resize-closes-and-outside-click-closes handlers from Menu. Reuse `FocusScope` (already barrel-exported/shared) for the panel. Create `popover.module.css` with the panel surface (`background: var(--surface-1)`, `border: 1px solid var(--edge)`, `border-radius`, shadow token if one exists — read `tokens.css`) and the `--motion-duration-overlay` fade-scale entry.

Then edit `widgets/menu/index.tsx` to import `resolveAxis`/`computeMenuPosition`(now `computePopoverPosition`)/`EDGE_MARGIN`/`TRIGGER_GAP` from `../popover/computePosition` and delete the local copies. Keep every other line of Menu identical.

- [ ] **Step 6: Verify Menu still passes + compile**

Run: `npx vitest run src/widgets/menu && npm run typecheck`
Expected: PASS — Menu's existing tests are unchanged (behavior-preserving extraction). If any Menu test imported the private helpers directly, repoint its import.

- [ ] **Step 7: Write a Popover reflow test**

Add to a new `src/widgets/popover/popover.test.tsx`: render a `Popover` with a trigger and assert (a) the panel is **not** in the document when `open=false`, (b) when `open=true` the panel is portaled (query by its testid, assert its `parentElement` chain reaches `document.body`, not the trigger's wrapper). This is the §3.4 "floats, never reflows" guarantee.

- [ ] **Step 8: Run it + token contract**

Run: `npx vitest run src/widgets/popover && npx vitest run src/styles/token-contract.test.ts`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add src/widgets/popover src/widgets/menu/index.tsx
git commit -m "feat(popover): shared floating-popover primitive extracted from Menu (§3.4)"
```

---

## S1 — Reducer

### Task 3: Carry the tool-call `description` (purpose) into `ItemModel` (Activity-feed prerequisite)

§4.2's Activity feed renders each child tool-call's **purpose**. The purpose already crosses the wire as `ThreadItem.description` (`types.gen.ts:841`, set server-side) but `wireItemToModel` drops it. Add it to `ItemModel` and copy it across. This is a one-field addition that Task 9 consumes.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/protocol/model.ts:15-62` (add `description?`)
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts:108-131` (`wireItemToModel`)
- Test: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts`

**Interfaces:**
- Produces: `ItemModel.description?: string` — the tool-call purpose, populated on every hydrate and live path (both fold through `wireItemToModel`).
- Consumed by: Task 9 (Activity feed).

- [ ] **Step 1: Write the failing test**

Add to `src/protocol/reducer.test.ts` (near the other `wireItemToModel`/hydrate tests):
```ts
test("wireItemToModel carries the wire description (tool-call purpose) onto the item", () => {
  const model = hydrateThread(
    testThread({
      turns: [
        {
          id: "turn_1",
          items: [
            { id: "item_tool_1_0", type: "commandExecution", toolName: "delegate", callId: "c1", description: "audit the reducer" },
          ],
        },
      ],
    }),
    "ref",
    0,
  );
  const firstTurn = model.turns[0];
  if (!firstTurn) throw new Error("expected a turn");
  const item = firstTurn.items[0];
  if (!item) throw new Error("expected an item");
  expect(item.description).toBe("audit the reducer");
});
```
Match the existing `testThread(...)` helper's exact shape in this file — read it before writing, and pass whatever required `ThreadItem` fields it needs (`text: ""` etc.) so the wire object typechecks.

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/protocol/reducer.test.ts -t "carries the wire description"`
Expected: FAIL — `item.description` is `undefined`.

- [ ] **Step 3: Add the field to `ItemModel`**

In `src/protocol/model.ts`, inside the `ItemModel` interface, alongside the `toolName/callId/argumentsJSON/output/error` cluster (lines 21-25), add:
```ts
  /** Tool-call purpose — the wire ThreadItem.description, surfaced for the
   *  subagent Activity feed. Dropped historically by wireItemToModel; now carried. */
  description?: string;
```

- [ ] **Step 4: Copy it across in `wireItemToModel`**

In `src/protocol/reducer.ts:108-131`, add to the `model` literal (after `argumentsJSON: item.argumentsJson,`):
```ts
    description: item.description,
```

- [ ] **Step 5: Run the test, verify it passes**

Run: `npx vitest run src/protocol/reducer.test.ts -t "carries the wire description"`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/protocol/model.ts src/protocol/reducer.ts src/protocol/reducer.test.ts
git commit -m "feat(reducer): carry tool-call description onto ItemModel (Activity-feed prereq)"
```

### Task 4: Merge call↔result by CallID on reload (zrzr)

On reload, `TurnsFromFile` mints one wire turn per transcript entry (`apptranscript.go:548`), so the tool CALL (from the assistant entry) and its RESULT (from the tool-results entry) arrive as **two items with the same `callId`, different ids, in separate turns** — rendered as two cards and two `TurnSeparator`s. The Go contract comment says "the client merges the two by call id"; the SPA never did. Fix it as a **cross-turn post-pass** over the assembled `TurnModel[]` in both hydrate paths — the merge cannot live in `wireToTurnModel`, which only ever sees one turn.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts` (new `mergeToolCallsByCallId` helper; call it in `hydrateThread:253` and `prependOlderTurns:277`)
- Test: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts`

**Interfaces:**
- Consumes: `TurnModel`/`ItemModel` from `model.ts`; `wireToTurnModel` output.
- Produces: `mergeToolCallsByCallId(turns: TurnModel[]): TurnModel[]` — collapses a CALL item (`id` starts `item_tool_`, has `argumentsJSON`/`startedAt`) and its matching RESULT item (`id` starts `item_tool_result_`, same `callId`, has `output`/`error`/`exitCode`/`completedAt`) into one `ItemModel`: keep the CALL's `id` + `argumentsJSON` + `startedAt`; take `output`/`error`/`exitCode`/`completedAt`/settled `status` from the RESULT (mirrors the live `EventToolCallEnd` merged item). A turn left with zero items after the merge is dropped so its `TurnSeparator` disappears.

- [ ] **Step 1: Write the failing test**

Add to `src/protocol/reducer.test.ts`:
```ts
test("reload merges a tool CALL and its RESULT (separate turns, same callId) into one item", () => {
  const model = hydrateThread(
    testThread({
      turns: [
        {
          id: "turn_1",
          items: [
            {
              id: "item_tool_1_0",
              type: "commandExecution",
              toolName: "shell",
              callId: "call_A",
              argumentsJson: JSON.stringify({ command: "make test" }),
              startedAt: 1,
              status: "inProgress",
            },
          ],
        },
        {
          id: "turn_2",
          items: [
            {
              id: "item_tool_result_2_0",
              type: "commandExecution",
              toolName: "shell",
              callId: "call_A",
              output: "ok",
              exitCode: 0,
              completedAt: 2,
              status: "completed",
            },
          ],
        },
      ],
    }),
    "ref",
    0,
  );

  // Exactly one tool item survives, carrying both halves.
  const items = model.turns.flatMap((t) => t.items).filter((i) => i.callId === "call_A");
  expect(items).toHaveLength(1);
  const merged = items[0];
  if (!merged) throw new Error("expected merged item");
  expect(merged.id).toBe("item_tool_1_0");        // keeps the CALL id
  expect(merged.argumentsJSON).toBe(JSON.stringify({ command: "make test" })); // from the CALL
  expect(merged.output).toBe("ok");                // from the RESULT
  expect(merged.exitCode).toBe(0);                 // from the RESULT
  expect(merged.status).toBe("completed");         // settled from the RESULT
  expect(merged.startedAt).toBeTruthy();           // carried from the CALL half
  expect(merged.completedAt).toBeTruthy();         // carried from the RESULT half

  // The now-empty result turn is gone, so only one turn (and one separator) remains.
  expect(model.turns).toHaveLength(1);
});
```
`startedAt`/`completedAt` are asserted present, not to an exact ISO value: the wire stamps epoch **seconds** (`apptranscript.go:354` `turn.Timestamp.Unix()`) and `wireItemToModel`'s conversion is what it is — the test locks that both halves' timestamps survive the merge, not the formatter's output. Confirm the `testThread` wire-item fields (`argumentsJson`, `startedAt`, `completedAt` as numbers) match the `ThreadItem` type in `types.gen.ts` before running.

- [ ] **Step 2: Run it, verify it fails**

Run: `npx vitest run src/protocol/reducer.test.ts -t "merges a tool CALL and its RESULT"`
Expected: FAIL — two `call_A` items, two turns.

- [ ] **Step 3: Implement the merge post-pass**

In `src/protocol/reducer.ts`, add above `hydrateThread`:
```ts
const isToolResultId = (id: string) => id.startsWith("item_tool_result_");
const isToolCallId = (id: string) => id.startsWith("item_tool_") && !isToolResultId(id);

// Reload projects a tool CALL and its RESULT as two items sharing a callId, in
// separate wire turns (apptranscript.TurnsFromFile mints one turn per transcript
// entry). Collapse them the way the live path already produces a single item:
// the call supplies id + argumentsJSON + startedAt, the result supplies output +
// error + exitCode + completedAt + settled status. A turn emptied by the merge is
// dropped so its TurnSeparator does not survive. (zrzr)
function mergeToolCallsByCallId(turns: TurnModel[]): TurnModel[] {
  const resultByCallId = new Map<string, ItemModel>();
  for (const turn of turns) {
    for (const item of turn.items) {
      if (item.callId && isToolResultId(item.id)) resultByCallId.set(item.callId, item);
    }
  }
  if (resultByCallId.size === 0) return turns;

  const merged: TurnModel[] = [];
  for (const turn of turns) {
    const items: ItemModel[] = [];
    for (const item of turn.items) {
      if (item.callId && isToolResultId(item.id)) continue; // folded into its call
      if (item.callId && isToolCallId(item.id)) {
        const result = resultByCallId.get(item.callId);
        if (result) {
          items.push({
            ...item,
            output: result.output,
            error: result.error,
            exitCode: result.exitCode,
            completedAt: result.completedAt,
            status: result.status,
            outputImages: result.outputImages ?? item.outputImages,
          });
          continue;
        }
      }
      items.push(item);
    }
    if (items.length > 0) merged.push({ ...turn, items });
  }
  return merged;
}
```
Then wrap both hydrate paths:
- `hydrateThread` line 253: `turns: mergeToolCallsByCallId((thread.turns ?? []).map(wireToTurnModel)),`
- `prependOlderTurns` line 277: `const older = mergeToolCallsByCallId((resp.data ?? []).map(wireToTurnModel));`

Import `ItemModel` in `reducer.ts` if not already imported (it imports from `./model`).

- [ ] **Step 4: Run the test, verify it passes**

Run: `npx vitest run src/protocol/reducer.test.ts -t "merges a tool CALL and its RESULT"`
Expected: PASS.

- [ ] **Step 5: Guard against over-merging live/single-turn cases**

Run the full reducer suite to confirm the existing snapshot (`reducer.test.ts:1194-1220`) and live-path tests still pass — a CALL+RESULT already in the **same** turn (live) must still collapse correctly, and a lone CALL (still in progress, no result yet) must pass through untouched:
Run: `npx vitest run src/protocol/reducer.test.ts`
Expected: PASS (all). If a prior snapshot encoded the doubled-item bug as expected output, update that snapshot to the corrected single-item shape and note it in the commit.

- [ ] **Step 6: Commit**

```bash
git add src/protocol/reducer.ts src/protocol/reducer.test.ts
git commit -m "fix(reducer): merge tool call+result by callId on reload (zrzr)"
```

---

## S2 — Shell frame

### Task 5: Dock the sidebar at ≥900px (3w2p)

Drop the 1200px auto-collapse threshold; `auto` mode docks across the whole desktop range (≥900px, the first non-mobile pixel per `useIsMobile.ts`), matching an app.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/useSidebarMode.ts` (`WIDE_QUERY` at line 17 + doc comments at 8/10/30)
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailHost.test.tsx:54` (asserts `"min-width: 1200px"`; also test names/comments at 31/122/130)
- Modify: `cmd/serf-hub/frontend/src/panes/settings/sections/theme.tsx:101` (user-facing help copy: "Auto collapses below 1200px and expands above it.")
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailHost.tsx:5,128` and `cmd/serf-hub/frontend/src/stores/prefs.ts:49` (doc comments citing 1200px)
- Test: `cmd/serf-hub/frontend/src/shell/rail/useSidebarMode.test.ts`

**Interfaces:**
- Consumes/Produces: unchanged `ResolvedSidebar { mode, collapsed }` shape; only the breakpoint value changes.

- [ ] **Step 1: Update the failing test first**

In `src/shell/rail/useSidebarMode.test.ts`, change the media-query the `FakeMediaQueryList`/`installMatchMediaStub` matches against from `min-width: 1200px` to `min-width: 900px`, and update any test names/comments citing 1200. Add/adjust a case asserting that at 1000px wide, `auto` resolves to **not collapsed** (the new dock behavior — previously collapsed).

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/useSidebarMode.test.ts`
Expected: FAIL — code still queries 1200px, so the 1000px case still collapses.

- [ ] **Step 3: Change the breakpoint**

In `src/shell/rail/useSidebarMode.ts:17`: `const WIDE_QUERY = "(min-width: 900px)";`. Update the doc comment (lines 5-13) and the `ResolvedSidebar.collapsed` JSDoc (lines 29-31) to say 900px, noting `auto` now docks across the whole desktop range and only `rail` collapses. Keep everything else (the SSR/jsdom default-wide fallback, the effect wiring) identical.

- [ ] **Step 4: Run it, verify it passes**

Run: `npx vitest run src/shell/rail/useSidebarMode.test.ts`
Expected: PASS.

- [ ] **Step 5: Fix the RailHost + settings-copy references**

In `src/shell/rail/RailHost.test.tsx:54`, change the `.includes("min-width: 1200px")` assertion to `900px` (and update the 1200 in the stub comment at :31 and the test names at :122/:130). In `src/panes/settings/sections/theme.tsx:101`, update the user-facing help copy "Auto collapses below 1200px and expands above it." to 900px / "docks on any desktop width (≥900px)". Also update the doc comments citing 1200px in `src/shell/rail/RailHost.tsx:5,128` and `src/stores/prefs.ts:49` so no stale breakpoint remains. (Note: there is no `src/shell/theme.tsx`; the only user-facing sidebar-mode copy lives in the Settings theme section.)

- [ ] **Step 6: Run both suites + compile**

Run: `npx vitest run src/shell/rail/RailHost.test.tsx src/shell/rail/useSidebarMode.test.ts && npm run typecheck`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/shell/rail/useSidebarMode.ts src/shell/rail/useSidebarMode.test.ts src/shell/rail/RailHost.tsx src/shell/rail/RailHost.test.tsx src/panes/settings/sections/theme.tsx src/stores/prefs.ts
git commit -m "fix(shell): dock sidebar at >=900px, drop 1200px threshold (3w2p)"
```

### Task 6: Sidebar owns the chrome — search / new / footer (c8gt)

The "missing header" is chrome that belongs **inside the sidebar**, not a full-width top bar. Add a header zone (brand + home, a full-width search field showing `⌘K` that opens the palette, a full-width "+ New session" primary button) and a pinned footer zone (identity + settings gear). No full-width row eats vertical space; content runs floor-to-ceiling.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx:303-384` (header/footer scaffold around the existing body)
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.module.css:3-49` (header/footer layout; `.body` already `flex:1 1 auto`)
- Test: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`

**Interfaces:**
- Consumes: `openPalette` from `shell/palette/paletteController` (existing); the `[data-search-trigger]` global handler in `AppShell.tsx:164-189` already calls `openPalette()` on click of any element carrying that attribute — the new search field just needs `data-search-trigger`.
- Produces: the header/footer DOM that Task 7's row work renders between. `hostedInSheet` must suppress the header (the sheet supplies its own chrome) exactly as the existing `hostedInSheet` header suppression does.

- [ ] **Step 1: Write the failing test**

Add to `src/shell/rail/Rail.test.tsx`:
```tsx
test("renders in-sidebar chrome: search opens palette, + New session, settings", () => {
  // render <Rail /> in its inline (non-hostedInSheet) form via the file's existing harness
  // ...
  const search = screen.getByTestId("rail-search");
  expect(search.getAttribute("data-search-trigger")).not.toBeNull(); // wired to the palette handler
  expect(screen.getByText("⌘K")).toBeTruthy();
  expect(screen.getByRole("button", { name: /new session/i })).toBeTruthy();
  expect(screen.getByTestId("rail-settings")).toBeTruthy();
});

test("hostedInSheet suppresses the sidebar header chrome", () => {
  // render <Rail hostedInSheet /> and assert the search field is absent
  expect(screen.queryByTestId("rail-search")).toBeNull();
});
```
Use the file's existing render helper/`ClientProvider` wrapper (read the top of `Rail.test.tsx`); mirror how current tests mount `<Rail>`.

- [ ] **Step 2: Run it, verify it fails**

Run: `npx vitest run src/shell/rail/Rail.test.tsx -t "in-sidebar chrome"`
Expected: FAIL — no search field / New button / settings exist yet.

- [ ] **Step 3: Add the header + footer scaffold**

In `src/shell/rail/Rail.tsx`, wrap the existing header (`<h2>Sessions</h2>` region ~303-334) and body (~335-382) so that when NOT `hostedInSheet` the component renders, top to bottom: a header zone (brand `serf` + home `IconButton`; a `data-search-trigger` search field `data-testid="rail-search"` showing the `⌘K` chord; a full-width `<Button variant="primary">+ New session</Button>` wired to the existing new-session action the rail already knows — reuse whatever `ProjectRow`'s "New session" `IconButton` calls), then the existing scrolling body, then a footer zone (`data-testid="rail-settings"` gear + identity). Keep the existing `hostedInSheet` branch header-less. Reuse `Button`/`IconButton` widgets (both allowlisted; primary button uses `--accent`, allowed everywhere).

- [ ] **Step 4: Style header/footer**

In `src/shell/rail/Rail.module.css`: add `.header` (column, `gap: var(--space-2)`, `padding`, `border-bottom: 1px solid var(--edge)`), `.search` (full-width, `background: var(--surface-2)`, `border: 1px solid var(--edge)`, `--ink-low` placeholder text, `⌘K` right-aligned in `--font-mono`), `.footer` (`margin-top: auto` OR rely on `.body { flex:1 1 auto }` to push it down; `border-top: 1px solid var(--edge)`). Colors: only `--surface-*`/`--ink-*`/`--edge`/`--accent` (this file is under `src/shell/`, so no status hues). No color literals.

- [ ] **Step 5: Run the test, verify it passes**

Run: `npx vitest run src/shell/rail/Rail.test.tsx && npx vitest run src/styles/token-contract.test.ts`
Expected: PASS.

- [ ] **Step 6: Compile + commit**

```bash
npm run typecheck
git add src/shell/rail/Rail.tsx src/shell/rail/Rail.module.css src/shell/rail/Rail.test.tsx
git commit -m "feat(shell): sidebar owns search / new / settings chrome (c8gt)"
```

---

## S3 — Sidebar tree

### Task 7: Remove the auto-attention grouping; attention inline (vbh8, §2.2)

The auto-grouping `Needs you` `RailSection` lists a session that ALSO appears under its project — the duplication §2.2 calls out. Remove that one auto-group (the `Live` group is **retained** — see the decision note). Attention then surfaces inline instead: the session's own dot is **already** amber when it needs you (`cadenceStateFor(session.state)` maps `"awaiting"`/`"warning"` → `"needs-you"` at `RailRow.tsx:224` — no code needed), a **derived** amber count badge of needs-you descendants rides the session row (the project row already bubbles via `rollup_attn`), and needs-you sessions sort to the top within their project.

**Retained per spec §2.1:** the user-explicit `Pinned` (favorites) section, the `Archived` section, and `Test runs` stay — favorites are a deliberate user pin, not an automatic duplicate. The store tiers themselves stay untouched (RailHost's ☰ chip badge reads `attentionSummary.needsYou`).

> **Decision (Jesse, 2026-07-23):** drop **only** the `Needs you` auto-group; **keep** `Live`, `Pinned`, `Projects`, `Archived`, `Test runs`. Spec §2.2 named only "Needs you" as the duplication defect, so this removes exactly that. A *live* (`active`) session therefore still legitimately appears in both `Live` and its project — that residual duplication is accepted; only the needs-you double-listing is removed.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.tsx:337-343` (delete only the `Needs you` `RailSection` block; keep `Live`/`Pinned`/`Projects`/`Archived`/`Test runs`)
- Modify: `cmd/serf-hub/frontend/src/shell/rail/railNodes.ts` (add a `needsYouDescendantCount` pure helper; needs-you-first sort within a project's sessions)
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx:216-248` (`SessionRow`: derived amber count badge)
- Test: `cmd/serf-hub/frontend/src/shell/rail/Rail.test.tsx`, `RailRow.test.tsx`, `railNodes.test.ts`

**Interfaces:**
- Consumes: `TreeProject.sessions[]`, `TreeNode.state`, `TreeNode.children[]` (`stores/tree.ts:28-50` — there is **no** per-session attn count field; only `TreeProject.rollup_attn`). `cadenceStateFor` (`RailRow.tsx:60`, already maps needs-you). `Badge`/`Cadence` (allowlisted).
- Produces: `needsYouDescendantCount(node: ApiTreeNode): number` in `railNodes.ts` — count of nodes in `node.children` (recursively) whose `state` maps to needs-you. Consumed by `SessionRow`'s badge and reusable by the Task 8 tree.

- [ ] **Step 1: Write the failing tests**

Add to `railNodes.test.ts`:
```ts
test("needsYouDescendantCount counts needs-you nodes in the subtree, not the node itself", () => {
  const node = {
    row_id: "s1", ref: "r1", state: "active", children: [
      { row_id: "s1a", ref: "r1a", state: "awaiting", children: [] },
      { row_id: "s1b", ref: "r1b", state: "warning", children: [
        { row_id: "s1b1", ref: "r1b1", state: "active", children: [] },
      ] },
    ],
  } as unknown as ApiTreeNode;
  expect(needsYouDescendantCount(node)).toBe(2); // s1a + s1b, not the active root or grandchild
});
```
Add to `Rail.test.tsx`: seed a tree where the same session appears in both `needs_you` and a project; render `<Rail>`; assert the row appears **once** (`screen.getAllByText(title)` length 1) and no "Needs you" section header renders (but a "Pinned" header still does when `favorites` is non-empty). Match the file's existing tree-seeding helper.

- [ ] **Step 2: Run them, verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/railNodes.test.ts src/shell/rail/Rail.test.tsx`
Expected: FAIL — `needsYouDescendantCount` undefined; the session renders twice.

- [ ] **Step 3: Delete only the Needs-you auto-group**

In `Rail.tsx`, remove ONLY the `<RailSection title="Needs you" nodes={sessionNodes(tree.needs_you, …)} />` block (337-343). Leave `Live` (344-350), `Pinned` (351-357), `Projects` (358-364), the `Archived` block (365-374), and `Test runs` (375-381) exactly as they are (Jesse's decision: keep Live). Do not touch `stores/tree.ts` or RailHost's badge.

- [ ] **Step 4: Add the derived count helper + sort**

In `railNodes.ts`, add (reusing `cadenceStateFor` from RailRow, or inline the same `awaiting|warning` check to avoid a RailRow→railNodes import cycle — prefer inlining a small `stateNeedsYou(state: string)` local):
```ts
function stateNeedsYou(state: string): boolean {
  return state === "awaiting" || state === "warning";
}
export function needsYouDescendantCount(node: ApiTreeNode): number {
  return node.children.reduce(
    (sum, c) => sum + (stateNeedsYou(c.state) ? 1 : 0) + needsYouDescendantCount(c),
    0,
  );
}
```
And in `projectNodes` (`railNodes.ts:100`) sort each project's session children needs-you-first with a stable partition (a session needs you when `stateNeedsYou(n.state) || needsYouDescendantCount(n) > 0`):
```ts
children: [...p.sessions]
  .sort((a, b) => Number(sessionWantsYou(b)) - Number(sessionWantsYou(a)))
  .map((n) => toSessionNode(n, isExpanded)),
```
where `sessionWantsYou(n) = stateNeedsYou(n.state) || needsYouDescendantCount(n) > 0`. `Array.prototype.sort` is stable in the target engines, so non-attention order is preserved.

- [ ] **Step 5: Add the session-row badge**

In `RailRow.tsx` `SessionRow` (216-248), after the label/star (mirroring `ProjectRow:268`):
```tsx
{needsYouDescendantCount(session) > 0 && (
  <Badge count={needsYouDescendantCount(session)} tone="attention" />
)}
```
Import `needsYouDescendantCount` from `./railNodes`. (The session's own needs-you already shows via the amber `Cadence` dot at line 224 — the badge is specifically the count of things *under* it, so a leaf needs-you session shows just its dot, no redundant "1".)

- [ ] **Step 6: Run the tests, verify they pass**

Run: `npx vitest run src/shell/rail/railNodes.test.ts src/shell/rail/Rail.test.tsx src/shell/rail/RailRow.test.tsx && npx vitest run src/styles/token-contract.test.ts`
Expected: PASS.

- [ ] **Step 7: Compile + commit**

```bash
npm run typecheck
git add src/shell/rail/Rail.tsx src/shell/rail/RailRow.tsx src/shell/rail/railNodes.ts src/shell/rail/*.test.ts src/shell/rail/*.test.tsx
git commit -m "fix(rail): inline attention, drop duplicate needs-you grouping (vbh8)"
```

### Task 8: Subagent-tree row polish — activity line + timestamp + guide rail (vbh8 new capability, §2.3)

**The recursion already exists:** `toSessionNode` (`railNodes.ts:52-60`) already maps `n.children` into `SessionRailNode.children`, and `SessionRow` already renders a `Chevron` when `info.hasChildren`, so a session's subagent subtree is **already** a deep, per-level-foldable tree today. This task adds the §2.3 row polish that is missing: a short **second activity line** (humanized status), a **right-aligned relative timestamp**, and an `--edge` **hairline guide rail** per nested level. Leaves already show no twisty (empty `children` → `hasChildren` false).

**Files:**
- Modify: `cmd/serf-hub/frontend/src/shell/rail/RailRow.tsx` (`SessionRow`: second activity line + relative timestamp; state dot is already honest via `cadenceStateFor`)
- Modify: `cmd/serf-hub/frontend/src/shell/rail/Rail.module.css` (the rail's single stylesheet — **there is no `tree.module.css`**; add `.activity`, `.time`, and a nested-level guide-rail rule)
- Test: `cmd/serf-hub/frontend/src/shell/rail/RailRow.test.tsx`

**Interfaces:**
- Consumes: `TreeNode.state` (real; humanized for the activity line), `TreeNode.age?: string` and `TreeNode.updated_at?: string` (real, `tree.ts:44-45` — carried but unused by the row today), `TreeNode.model?: string` (real, `tree.ts:46`). **There is no `agent_type` field** — do not reference one; the second line is built from `state` (and optionally `model`). `cadenceStateFor` (already maps needs-you). `needsYouDescendantCount` from Task 7 (to decide badge-vs-timestamp in the right slot).
- Produces: nested foldable rows with the §2.3 anatomy. **Honest state only** — `Cadence` keeps `frameTimes={NO_FRAME_TIMES}`/`now={INERT_NOW}` as sibling rows already do; the sidebar has no per-node live frame stream this round (spec §2.3's live traces are OUT — see the coverage note).

- [ ] **Step 1: Write the failing test**

Add to `RailRow.test.tsx` (mirror the file's existing `SessionRow` render harness):
```tsx
test("SessionRow shows a humanized activity line and a relative timestamp", () => {
  // node: state "active", age "2m", no needs-you descendants
  // render SessionRow, then:
  expect(screen.getByTestId("rail-row-activity").textContent).toMatch(/working/i);
  expect(screen.getByTestId("rail-row-time").textContent).toBe("2m");
});

test("a needs-you count takes the right slot instead of the timestamp", () => {
  // node: state "active" with one "awaiting" child
  expect(screen.queryByTestId("rail-row-time")).toBeNull();
  expect(screen.getByText("1")).toBeTruthy(); // the Badge from Task 7
});
```
Use the real field names (`state`, `age`, `children`). Confirm the file's harness for mounting a single `SessionRow` (it may render through `<Rail>`/`RailSection` — follow the existing pattern).

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/rail/RailRow.test.tsx -t "activity line"`
Expected: FAIL — no activity line / timestamp rendered yet.

- [ ] **Step 3: Render the second line + timestamp**

In `RailRow.tsx` `SessionRow`, below the label add a second line `<span data-testid="rail-row-activity" className={CLASS.activity}>{humanizeState(session.state)}</span>` (a small local map: `active`→"working", `awaiting`/`warning`→"waiting on you", `errored`→"failed", `ended`→"ended", else "idle"; optionally ` · ${session.model}` when present). In the row's right slot, render EITHER the Task-7 `Badge` (when `needsYouDescendantCount(session) > 0`) OR `<span data-testid="rail-row-time" className={CLASS.time}>{session.age ?? ""}</span>` (when `age` is present and no badge). Recursion/folding is already handled by `toSessionNode` + `Tree` — do not add new recursion.

- [ ] **Step 4: Style depth + layout**

In `Rail.module.css`, add `.activity` (`color: var(--ink-low)`, smaller `font-size`, ellipsis) and `.time` (`color: var(--ink-low)`, `font-family: var(--font-mono)`, `margin-left:auto`), and a nested-level guide rail — a `border-left: 1px solid var(--edge)` on the Tree's nested-children container (inspect how `widgets/tree` marks nested levels; if it exposes a depth attribute/class, hang the border off that; otherwise add a left border + padding on the row group). Only `--ink-*`/`--edge`/`--surface-*`/`--accent`; no color literals, no status hues in this file.

- [ ] **Step 5: Run the tests + token contract**

Run: `npx vitest run src/shell/rail/RailRow.test.tsx src/shell/rail/Rail.test.tsx && npx vitest run src/styles/token-contract.test.ts`
Expected: PASS.

- [ ] **Step 6: Compile + commit**

```bash
npm run typecheck
git add src/shell/rail/RailRow.tsx src/shell/rail/Rail.module.css src/shell/rail/*.test.tsx
git commit -m "feat(rail): deep foldable subagent tree + activity line + timestamp (vbh8)"
```

---

## S4 — Subagent card

### Task 9: Live status write-back + `includeTurns` watch (yd16, §4.2 plumbing)

**This plumbing must land before the card body (Task 10) — Task 10's test reads the turns and live status this task produces.** Two data-layer fixes, both testable now against the `WatchedChildIndicator` that already renders on every running subagent row — no card redesign required:

1. **Live status write-back (the whole of yd16).** The status pill froze at the settled tool-output value (`rowFromDelegateItem` sets `kind: classifyJobStatus(status)` from the *frozen* delegate output). The live child status is already on the watched thread (`watchedChild.tsx:34` reads `model.status.type`) but is never written back to the row. Write it back as a **separate `liveKind` overlay field** the pill prefers over the frozen `kind`.
2. **Turns for the Activity feed (§4.2 prerequisite).** `watchReadParams` hardcodes `includeTurns:false`. The expanded card (Task 10) needs the watched read to carry turns. Make `includeTurns` a per-call option.

**Three ground-truthed hazards this task must handle (the naive one-liner does not):**

- **`rowKindFromChildStatus` mapper.** `model.status.type` is the WIRE thread-status vocabulary (`active`/`closed`/`systemError`/`awaiting`/`warning`/`idle`), NOT the job-status words `classifyJobStatus` reads (`completed`/`failed`/…). Feeding thread-status to `classifyJobStatus` misclassifies (`"closed"`→`"running"`, `"systemError"`→`"running"`). The canonical wire-status interpreter is `cadenceStateForStatus` (`panes/session/liveness.ts:30` — note: NOT `transcript/liveness.ts`). Reuse it, then adapt its `CadenceState` to `SubagentRowKind`.
- **Upsert clobber.** `DelegateBody`'s `useLayoutEffect` (`subagentModule.tsx:249-252`) has **no dep array**, so it re-`upsertSubagentRow`s on every render from the frozen output. `upsertSubagentRow` (`subagentModuleStore.ts:87`) replaces the whole row (keeps only `spawnIndex`), so any watch-written field is wiped on the next incidental render. Fix by making `upsertSubagentRow` **preserve the watch-owned `liveKind`** across upserts — `liveKind` is watch-owned, the delegate upsert must never touch it.
- **Write-back must be effect-guarded, never during render.** `updateSubagentRowIfExists` builds fresh Maps + a fresh sorted array every call, so `useSubagentRows` returns a new reference each write → an unguarded write during render is an infinite re-render loop. The write-back goes in a `useEffect` keyed on the derived `liveKind` so it fires only on an actual status change.

> **DECISION FOR SIGN-OFF (the one real design fork in this plan — resolve before S4/wave-2 launches):** how the expanded card's turns-carrying watch coexists with the row dot's lean `includeTurns:false` watch on the *same ref*. `watchThread` short-circuits on `watchedThreads.has(ref)` (`threads.ts:598`), so if the lean watch hydrates first, the card's later `{includeTurns:true}` call returns without ever loading turns.
> - **Option A — always-rich (simplest, ~2 lines):** drop the lean/rich distinction; `watchReadParams` always sets `includeTurns:true`. Every running row fetches its child's turn history (turnLimit 40) even when never expanded. Contradicts §4.2's explicit lean-watch resource choice (`watchReadParams` comment: "wasted fetch+storage for a subagent row most sessions never expand").
> - **Option B — monotonic upgrade-read (~30 lines in `threads.ts`, honors the spec):** track a per-ref `watchIncludeTurns` flag; on a `{includeTurns:true}` call for a ref currently tracked lean, issue a fresh rich `thread/read` (bypassing the `.has`/inflight-dedup shortcuts, which are for concurrent first-mounts) and replace the model. Monotonic: once any watcher wants turns, keep them until refcount hits zero.
> The steps below are written for **Option B** (honors §4.2). If Jesse picks A, Step 3's `threads.ts` change collapses to flipping `watchReadParams` to `includeTurns:true` and deleting the upgrade logic. This touches the invariant-documented watch machinery, so it is a controller/Jesse call, not an implementer's.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts` (`watchReadParams(ref, includeTurns)`; `watchThread(ref, opts?)` + per-ref `watchIncludeTurns` upgrade-read; interface sig at :109; teardown clears the map)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts` (add `liveKind?: SubagentRowKind` to `SubagentRow`; preserve it across `upsertSubagentRow`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx` (`rowKindFromChildStatus`; `SubagentModule` passes `turnId` to `SubagentRowView`; `SubagentRowView` passes `turnId`+`row.rowKey` to `WatchedChildIndicator`; pill `Chip` uses `row.liveKind ?? row.kind`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/watchedChild.tsx` (props gain `turnId`+`rowKey`; effect-guarded status write-back)
- Test: `cmd/serf-hub/frontend/src/stores/threads.test.ts` (includeTurns upgrade), `cmd/serf-hub/frontend/src/panes/session/transcript/tools/watchedChild.test.tsx` (write-back)

**Interfaces:**
- Consumes: `updateSubagentRowIfExists(turnId, rowKey, patch: Partial<SubagentRowInput>)` (`subagentModuleStore.ts:106`); `cadenceStateForStatus(type): CadenceState` (`panes/session/liveness.ts:30`); the child `model.status.type` + `model.turns`.
- Produces:
  - `watchThread(ref: string, opts?: { includeTurns?: boolean }): Promise<void>` — default lean; the expanded card (Task 10) passes `{ includeTurns: true }`.
  - `SubagentRow.liveKind?: SubagentRowKind` in `subagentModuleStore.ts` — the live-child-status overlay; the pill renders `row.liveKind ?? row.kind`.
  - `rowKindFromChildStatus(type: string): SubagentRowKind` exported from `subagentModule.tsx` — `cadenceStateForStatus(type)` then `failed`→`"failed"`, `ended`→`"done"`, else `"running"`.
  - `WatchedChildIndicatorProps { ref: string; turnId: string; rowKey: string }`.
  - **Task 10 consumes** the `{ includeTurns: true }` watch, the populated `model.turns`, and the `liveKind` overlay.

- [ ] **Step 1: Write the failing tests**

In `watchedChild.test.tsx` (mirror its existing FakeClient watch harness): render `<WatchedChildIndicator ref turnId rowKey />` after seeding a subagent row (`upsertSubagentRow(turnId, { rowKey, kind: "running", task: "t", resultPreview: "" })`); drive the watched child's `status.type` to `"systemError"`; assert the row's `liveKind` becomes `"failed"` via the store (`useSubagentRows`/`getState`). Add a case: child `status.type` `"closed"` → `liveKind` `"done"`. In `threads.test.ts`: assert `watchThread(ref, { includeTurns: true })` yields a `watchedThreads.get(ref)` whose `turns` are populated, and that a prior lean `watchThread(ref)` followed by a `{ includeTurns: true }` call **upgrades** (turns become populated — the `.has(ref)` short-circuit does not starve it). [Option A: instead assert the default `watchThread(ref)` already carries turns.]

- [ ] **Step 2: Run them, verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/watchedChild.test.tsx src/stores/threads.test.ts`
Expected: FAIL — no `liveKind` write-back; the upgrade read is starved by the `.has(ref)` short-circuit.

- [ ] **Step 3: Parameterize the watch read (Option B upgrade) + the store overlay field**

In `subagentModuleStore.ts`: add `liveKind?: SubagentRowKind;` to `SubagentRow` (it flows into `SubagentRowInput` automatically). In `upsertSubagentRow`, preserve the watch-owned overlay — change the row write to keep an existing `liveKind` when the delegate input omits it:
```ts
    const existingRow = rows.get(row.rowKey);
    const spawnIndex = existingRow?.spawnIndex ?? nextIndexBefore;
    rows.set(row.rowKey, { liveKind: existingRow?.liveKind, ...row, spawnIndex });
```
In `threads.ts`, add a per-ref bookkeeping map beside `watchRefCounts`:
```ts
const watchIncludeTurns = new Map<string, boolean>();
```
Give `watchReadParams(ref, includeTurns = false)` the flag (drop the hardcoded `includeTurns:false`); change the interface sig at line 109 to `watchThread(ref: string, opts?: { includeTurns?: boolean }): Promise<void>`; and rewrite `watchThread` so a lean-then-rich sequence re-reads (bypassing the `.has`/inflight shortcuts, which exist only for concurrent first-mounts):
```ts
  async watchThread(ref, opts) {
    const client = requireClient();
    const wantTurns = opts?.includeTurns ?? false;
    watchRefCounts.set(ref, (watchRefCounts.get(ref) ?? 0) + 1);
    const hadTurns = watchIncludeTurns.get(ref) ?? false;
    const needTurns = hadTurns || wantTurns; // monotonic: never downgrade mid-life
    watchIncludeTurns.set(ref, needTurns);
    const tracked = threadsStore.getState().watchedThreads.has(ref);
    const upgrading = tracked && wantTurns && !hadTurns;
    if (tracked && !upgrading) return; // already hydrated at the level we need
    // Upgrade re-read bypasses the inflight-dedup (that shares a possibly-lean
    // read); a fresh mount uses it, same as before.
    const model = upgrading
      ? await hydrateAndSubscribeWatch(client, ref, Date.now(), needTurns)
      : await (() => { /* existing inflight-dedup block, now passing needTurns */ })();
    if ((watchRefCounts.get(ref) ?? 0) <= 0) return;
    threadsStore.setState((s) => {
      const next = new Map(s.watchedThreads);
      next.set(ref, model);
      return { watchedThreads: next };
    });
  },
```
Give `hydrateAndSubscribeWatch(client, ref, now, includeTurns = false)` the flag and pass it to `watchReadParams`. In `releaseWatchedThread`, when the count hits zero and the ref is dropped, also `watchIncludeTurns.delete(ref)`. In `resetForTests`/teardown (`threads.ts:921` area, beside `inflightWatchHydrates.clear()`), add `watchIncludeTurns.clear()`.
(Keep the exact inflight-dedup body from the current `watchThread` for the non-upgrade branch — only thread `needTurns` through it. Read `threads.ts:595-627` and preserve every refcount/catch/`<=0`-guard line.)

- [ ] **Step 4: Add the mapper + write-back + thread `turnId` through**

In `subagentModule.tsx`, add beside `classifyJobStatus`:
```ts
import { cadenceStateForStatus } from "../../liveness";
// The child's LIVE thread status (wire vocabulary) mapped to a row kind for
// the status pill (yd16). Reuses cadenceStateForStatus (the one wire-status
// interpreter) rather than classifyJobStatus, which reads job-status words.
export function rowKindFromChildStatus(type: string): SubagentRowKind {
  const state = cadenceStateForStatus(type);
  if (state === "failed") return "failed";
  if (state === "ended") return "done";
  return "running"; // working / needs-you / idle → still running from the parent's view
}
```
Have `SubagentModule` pass `turnId` to each row (`<SubagentRowView key={row.rowKey} row={row} turnId={turnId} />`), widen `SubagentRowView({ row, turnId }: { row: SubagentRow; turnId: string })`, render the pill from the overlay (`<Chip tone={KIND_TONE[row.liveKind ?? row.kind]}>{KIND_LABEL[row.liveKind ?? row.kind]}</Chip>`), and pass ids down: `<WatchedChildIndicator ref={transcriptRef} turnId={turnId} rowKey={row.rowKey} />`. In `watchedChild.tsx`, widen the props to `{ ref, turnId, rowKey }` and add the effect-guarded write-back (only when `model` exists and `liveKind` changes):
```tsx
  const liveKind = model ? rowKindFromChildStatus(model.status.type) : undefined;
  useEffect(() => {
    if (liveKind) updateSubagentRowIfExists(turnId, rowKey, { liveKind });
  }, [turnId, rowKey, liveKind]);
```
Import `updateSubagentRowIfExists` from `./subagentModuleStore` and `rowKindFromChildStatus` from `./subagentModule`.

- [ ] **Step 5: Run the tests, verify they pass**

Run: `npx vitest run src/panes/session/transcript/tools/watchedChild.test.tsx src/stores/threads.test.ts`
Expected: PASS.

- [ ] **Step 6: Full subagent suite + compile + commit**

```bash
npx vitest run src/panes/session/transcript/tools src/stores/threads.test.ts && npm run typecheck
git add src/stores/threads.ts src/panes/session/transcript/tools/subagentModuleStore.ts src/panes/session/transcript/tools/subagentModule.tsx src/panes/session/transcript/tools/watchedChild.tsx src/panes/session/transcript/tools/*.test.tsx src/stores/threads.test.ts
git commit -m "fix(subagent-card): live status overlay + includeTurns upgrade watch (yd16)"
```

### Task 10: Card disclosure + Mandate → live Activity → Summary (qb8e, tv5k, §4.1/§4.2)

Give the subagent card a real disclosure (rotating chevron via the `Disclosure` widget) and, when expanded, a three-layer body: **Mandate** (the delegation `purpose`/task), **Activity** (a live feed of the child's tool-call `description`/purpose fields from the child transcript, latest highlighted while running), **Summary** (the child's final report — its last `agentMessage`). Collapsed stays a one-liner: title + status pill + step count. Depends on Task 9's `includeTurns` watch + status write-back.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx` (`SubagentRowView` → use `Disclosure`; expanded body; pass `{ includeTurns: true }` to the watch)
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`

**Interfaces:**
- Consumes: `Disclosure` (Task 1); `ItemModel.description` (Task 3); `watchThread(ref, { includeTurns: true })`, the `SubagentRow.liveKind` overlay, and `rowKindFromChildStatus` (all Task 9); the watched-thread `model.turns[].items[]` (`threadsStore.watchedThreads.get(ref)`); `SubagentRow` fields from `subagentModuleStore.ts`. **`SubagentRowView` already receives `turnId` as of Task 9** — reuse it, don't re-thread.
- Produces: the expanded card. The Mandate is the delegate item's `task` (already parsed in `rowFromDelegateItem`); the Activity feed maps the watched child's tool-call items to their `description`; the Summary is the watched child's last `agentMessage` item text. The collapsed pill renders `row.liveKind ?? row.kind` (the Task 9 overlay).

- [ ] **Step 1: Write the failing test**

Add to `subagentModule.test.tsx` (mirror the existing FakeClient `thread/read` wiring at lines 248-279): render an expanded delegate card whose watched child thread returns turns containing two tool-call items with `description` "step one"/"step two" and a final `agentMessage` "all done". Assert the card renders the Mandate (the task), an Activity feed showing "step one" and "step two", and the Summary "all done". Add a second test: a running child's status pill reads the **live** watched status written back by Task 9 (not the frozen tool-output value).

- [ ] **Step 2: Run it, verify it fails**

Run: `npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx -t "Mandate"`
Expected: FAIL — the card has no Disclosure/Mandate/Activity/Summary yet.

- [ ] **Step 3: Swap the row into a `Disclosure` and build the expanded body**

In `subagentModule.tsx`, wrap the row in `<Disclosure id={row.rowKey} summary={<...title + status pill + step count...>}>`. When expanded, call the watch with `{ includeTurns: true }` (Task 9's option) and, when `transcriptRef` is present, render three sections: Mandate (`row.task`), Activity (map the watched child's `model.turns.flatMap(t => t.items)` tool-call items to their `description`, latest highlighted while the child status is running), Summary (the last `agentMessage`-type item's text). Reuse `WatchedChildIndicator`'s watch subscription (`threadsStore.watchThread(transcriptRef, { includeTurns: true })` / `releaseWatchedThread`) but read `watchedThreads.get(ref)?.turns` for content. The status pill is a `Chip` (allowlisted) toned by the live child status.

- [ ] **Step 4: Run the test, verify it passes**

Run: `npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx -t "Mandate"`
Expected: PASS.

- [ ] **Step 5: Preserve existing fold/watch tests**

Run: `npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx`
Expected: PASS (all) — the existing "+N more"/Collapse fold test (line 337) and the WatchedChildIndicator Cadence test (248-279) must still pass. The `Disclosure` migration replaces the local `useState` fold with the store; keep the "+N more" cap behavior.

- [ ] **Step 6: Token contract + compile + commit**

```bash
npx vitest run src/styles/token-contract.test.ts && npm run typecheck
git add src/panes/session/transcript/tools/subagentModule.tsx src/panes/session/transcript/tools/subagentModule.test.tsx
git commit -m "feat(subagent-card): disclosure + Mandate/Activity/Summary body (qb8e, tv5k)"
```

### Task 11: "Open full transcript" while running + migrate native `<details>` to the store (§4.4, yt2q completion)

The child-transcript link works only when done; make it available while the subagent is still running (the opened pane watches the live child thread). And migrate the remaining component-local disclosures — the tool-call rows, the "+N more" fold, and the native `<details>` in `ThinkBlock`/`SteeringItem`/`SystemNotice` — onto the `Disclosure` store so open/closed state survives VirtualList re-windowing and dockview remounts.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx` (`SubagentRowView`: show "Open transcript" while `kind === "running"` too)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx:69-137` (drive `open` from `disclosureStore` keyed by `item.id` instead of local `useState`)
- Modify: `messages/ThinkBlock.tsx:85`, `messages/SteeringItem.tsx:50`, `messages/SystemNoticeItem.tsx:90,120` (replace raw `<details>` with `Disclosure`, keyed by `item.id` / first-item id for grouped)
- Test: `ToolCallItem.test.tsx`, the three message tests

**Interfaces:**
- Consumes: `Disclosure` + `disclosureStore` (Task 1); `openTranscript(ref)` (already used at `subagentModule.tsx:165`).
- Produces: remount-surviving disclosures across the transcript; live-transcript link.

- [ ] **Step 1: Write the failing tests**

In `subagentModule.test.tsx`: a **running** row (`kind: "running"`) with a `transcriptRef` renders the "Open transcript" button (today it renders for any `transcriptRef`, but assert it specifically appears while running). In `ToolCallItem.test.tsx`: expand a tool call, unmount, remount with the same `item.id`, assert it stays expanded (store-backed) — extend the existing disclosure tests at lines 94/102/137/259. In each message test: same remount-survives assertion.

- [ ] **Step 2: Run them, verify they fail**

Run: `npx vitest run src/panes/session/transcript/ToolCallItem.test.tsx -t "remount"`
Expected: FAIL — local `useState` resets on remount.

- [ ] **Step 3: Migrate ToolCallItem + messages to `Disclosure`**

Replace `ToolCallItem.tsx`'s local `expanded`/`userToggledRef` (69-70) and the controlled `<details>` (101-137) with `<Disclosure id={item.id} summary={...} defaultOpen={autoExpandCondition}>`. Preserve the failed-row `data-attention="error"` attribute and the autoExpand-once behavior (map it to `defaultOpen` computed from the live→settled edge; the store then holds subsequent user toggles). Replace the raw `<details>` in `ThinkBlock`/`SteeringItem`/`SystemNoticeItem` with `Disclosure` keyed by `item.id` (SystemGroup: the run's first item id, `firstItem.id`).

- [ ] **Step 4: Show the transcript link while running**

In `subagentModule.tsx` `SubagentRowView`, keep the "Open transcript" `Button` rendered when `transcriptRef` is present regardless of `kind` (it already is), and confirm `openTranscript` opens a pane that watches the live child thread — the §4.4 requirement is that it's not gated on done. If any code path hides it until terminal, remove that gate.

- [ ] **Step 5: Run the tests, verify they pass**

Run: `npx vitest run src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/messages && npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx`
Expected: PASS.

- [ ] **Step 6: Token contract + compile + commit**

```bash
npx vitest run src/styles/token-contract.test.ts && npm run typecheck
git add src/panes/session/transcript
git commit -m "feat(transcript): store-backed disclosures + live transcript link (yt2q, §4.4)"
```

---

## S5 — Spawn form

### Task 12: Model picker → click-the-chip combobox (4y12)

Replace the read-only chip + separate "Change model" button with a single chip-as-button trigger that opens a floating combobox (never reflows). Fix the **shared closed-state** in `widgets/modelCatalog` so both consumers (spawn and Settings) inherit it. Mirror the in-session `ModelSwitch` trigger over the already-shared `ModelCatalogPanel`, floated via the Task 2 `Popover`.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/widgets/modelCatalog/index.tsx:220-239` (closed-state → chip-as-button trigger + `Popover`)
- Test: `cmd/serf-hub/frontend/src/widgets/modelCatalog/*.test.tsx`

**Interfaces:**
- Consumes: `Popover` (Task 2); `ModelCatalogPanel` (already extracted, props `{loading, error, catalog, onPick, onCancel}`); the `ModelSwitch.tsx:118-144` trigger shape (button → `Chip` + chevron) to mirror.
- Produces: a floating model combobox both `panes/spawn` and `panes/settings/.../launchShared/fields.tsx:137` inherit unchanged (they render `<ModelCatalog>`; the fix is inside it).

- [ ] **Step 1: Write the failing test**

In the modelCatalog test: render the closed state; assert clicking the chip trigger opens the panel **as a portaled overlay** (not an inline sibling that reflows) — query the panel and assert its DOM parent is `document.body` (the `Popover` portal), and assert there is no separate "Change model" button. Assert picking an entry calls `onChange`/`onPick` and closes.

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/widgets/modelCatalog`
Expected: FAIL — closed state still renders the read-only Chip + "Change model" Button inline.

- [ ] **Step 3: Rebuild the closed state as a floating combobox**

In `modelCatalog/index.tsx:220-228`, replace the `<div className={CLASS.row}><Chip>…</Chip><Button>Change model</Button></div>` with a `Popover` whose `trigger` is a chip-as-button (mirror `ModelSwitch.tsx:118-131`: a `<button>` wrapping `<Chip>{value===""?"(default)":value}</Chip>` + a chevron + sr-only "change model") and whose panel is the existing `ModelCatalogPanel`. Remove the separate open-state early return (231-239) in favor of the `Popover`'s open/close. Both consumers inherit this automatically.

- [ ] **Step 4: Run the test, verify it passes**

Run: `npx vitest run src/widgets/modelCatalog`
Expected: PASS.

- [ ] **Step 5: Verify both consumers compile + Settings still works**

Run: `npx vitest run src/panes/settings src/panes/spawn && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/widgets/modelCatalog
git commit -m "feat(model-picker): click-the-chip floating combobox, shared by spawn + settings (4y12)"
```

### Task 13: Directory picker → one field, floating browse (yhmh)

Collapse the two directory inputs to a single working-directory field with a floating browse popover showing recents / `../` / subfolders. Remove the duplicate second path input and the redundant "Use this directory" button (Enter commits).

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/spawn/DirField.tsx:118-192` (single input; browse popover via `Popover`; drop the second input + button)
- Modify: `cmd/serf-hub/frontend/src/panes/spawn/dirField.module.css` (popover now via `Popover`; drop the `position:absolute .popup` local float)
- Test: `cmd/serf-hub/frontend/src/panes/spawn/DirField.test.tsx`

**Interfaces:**
- Consumes: `Popover` (Task 2); the existing `handleType` (118-129) and commit-on-Enter behavior.
- Produces: one dir field; browse floats (no reflow).

- [ ] **Step 1: Write the failing test**

In `DirField.test.tsx`: assert there is exactly one text input (`screen.getAllByRole("textbox")` length 1 within the field), no "Use this directory" button, and that opening browse portals the list (parent chain to `document.body`), and Enter in the field commits the typed path (existing behavior preserved).

- [ ] **Step 2: Run it, verify it fails**

Run: `npx vitest run src/panes/spawn/DirField.test.tsx`
Expected: FAIL — two inputs + "Use this directory" button present.

- [ ] **Step 3: Collapse to one field + floating browse**

In `DirField.tsx`: keep the outer `Input` (170) as the single field; move the recents/`../`/subfolders list into a `Popover` panel anchored to it; delete the popup's second `Input` (185) and the "Use this directory" `Button` (186-192). Route the browse list's selection back through `handleType`/commit. In `dirField.module.css`, remove the `.popup { position:absolute … }` float (23-38) — the `Popover` owns positioning now.

- [ ] **Step 4: Run the test, verify it passes**

Run: `npx vitest run src/panes/spawn/DirField.test.tsx`
Expected: PASS.

- [ ] **Step 5: Compile + token contract + commit**

```bash
npm run typecheck && npx vitest run src/styles/token-contract.test.ts
git add src/panes/spawn/DirField.tsx src/panes/spawn/dirField.module.css src/panes/spawn/DirField.test.tsx
git commit -m "feat(spawn): single directory field with floating browse (yhmh)"
```

### Task 14: One scroll surface; Advanced below the prompt (9ct0, §3.3)

§3.3: the form scrolls as **one** surface, the Advanced-options disclosure sits below the prompt (it already does — Spawn.tsx renders it at :452, after the prompt card at :417), and **Access mode lives inside Advanced** (today it is a top-level `FormRow` at `Spawn.tsx:393-400`, above the prompt — the spec's field order lists no top-level Access mode). Two concrete code facts drive this:
- `AdvancedOptions` takes `options: LaunchOption[]` and maps them to `Control`s — it has **no children slot** today, so hosting Access mode needs a small `AdvancedOptions.tsx` change.
- `AdvancedOptions` renders only behind `{schemaOptions.length > 0 && …}` (`Spawn.tsx:451-458`). If Access mode moves inside without dropping that gate, it disappears whenever the harness exposes no schema options. So `AdvancedOptions` must always render once it hosts Access mode.
- The only inner `max-height:280px; overflow:auto` is on `.resolved` (`advancedOptions.module.css:41-51`) — the "Show resolved config" `<pre>` JSON dump, not a wrapper of the form. Removing it there is what "remove the inner 280px scroller" means; the pane's outer scroll is `panescaffold.module.css .body` (`overflow-y:auto`).

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/spawn/AdvancedOptions.tsx:25-33,80-97` (add a `children?: React.ReactNode` prop to `AdvancedOptionsProps`; render it at the top of `.panel`, before the schema `Control`s)
- Modify: `cmd/serf-hub/frontend/src/panes/spawn/Spawn.tsx:393-400,451-458` (move the Access-mode `FormRow` out of the top-level field grid and into `<AdvancedOptions>{…}</AdvancedOptions>` as children; render `AdvancedOptions` unconditionally instead of gating on `schemaOptions.length > 0`, passing `options={schemaOptions}` — empty is fine)
- Modify: `cmd/serf-hub/frontend/src/panes/spawn/advancedOptions.module.css:41-51` (delete `max-height: 280px;` and `overflow: auto;` from `.resolved`)
- Test: `cmd/serf-hub/frontend/src/panes/spawn/Spawn.test.tsx`, `cmd/serf-hub/frontend/src/panes/spawn/AdvancedOptions.test.tsx`

**Interfaces:**
- Consumes: existing `AdvancedOptions` (`{ options, onOverridesChange, validatePath, resolveConfig }`) + `ACCESS_MODE_OPTIONS` (`panes/spawn/accessMode.ts`) + the `Select`/`FormRow` widgets.
- Produces: `AdvancedOptionsProps` gains `children?: React.ReactNode`, rendered inside `.panel` above the schema controls. Access mode is no longer a top-level field; it is the first control inside Advanced. `AdvancedOptions` always renders.

- [ ] **Step 1: Write/adjust the failing tests**

In `AdvancedOptions.test.tsx`: render `<AdvancedOptions options={[]} …>{<div data-testid="child-slot">hi</div>}</AdvancedOptions>`, expand it, and assert the child slot renders inside the expanded panel (before any schema control). In `Spawn.test.tsx`: assert the Access-mode `Select` (`#spawn-access`) is **not** in the DOM until "Advanced options" is expanded, then, after expanding Advanced, it **is** present (i.e. Access mode moved from top-level into Advanced). Keep assertions resilient to CSS-module hashing (query by role/testid/`#spawn-access`, not raw class names).

- [ ] **Step 2: Run them, verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/spawn/AdvancedOptions.test.tsx src/panes/spawn/Spawn.test.tsx -t "Advanced"`
Expected: FAIL — `AdvancedOptions` ignores children; Access mode still renders at top level.

- [ ] **Step 3: Add the children slot to `AdvancedOptions`**

In `AdvancedOptions.tsx`, add `children?: React.ReactNode;` to `AdvancedOptionsProps` (and destructure it), then render it first inside the panel:
```tsx
      {open && (
        <div id={panelId} className={CLASS.panel}>
          {children}
          {options.map((opt) => (
```
(Add the `ReactNode` type import if not present.)

- [ ] **Step 4: Move Access mode into Advanced + always-render + drop the inner scroller**

In `Spawn.tsx`, delete the top-level Access-mode `FormRow` (393-400) from the field grid and render `AdvancedOptions` unconditionally with Access mode as its children:
```tsx
        <AdvancedOptions
          options={schemaOptions}
          onOverridesChange={setAdvancedOverrides}
          validatePath={validatePath}
          resolveConfig={resolveConfig}
        >
          <FormRow label="Access mode" htmlFor="spawn-access">
            <Select
              id="spawn-access"
              value={accessMode}
              onChange={(e) => setAccessMode(e.target.value)}
              options={[{ value: "", label: "(default)" }, ...ACCESS_MODE_OPTIONS]}
            />
          </FormRow>
        </AdvancedOptions>
```
(Replace the `{schemaOptions.length > 0 && <AdvancedOptions … />}` block at 451-458.) In `advancedOptions.module.css`, delete `max-height: 280px;` (42) and `overflow: auto;` (45) from `.resolved` so the resolved-config dump grows in the one outer scroll rather than nesting a second.

- [ ] **Step 5: Run the tests, verify they pass**

Run: `npx vitest run src/panes/spawn/AdvancedOptions.test.tsx src/panes/spawn/Spawn.test.tsx`
Expected: PASS.

- [ ] **Step 6: Compile + token contract + commit**

```bash
npm run typecheck && npx vitest run src/styles/token-contract.test.ts
git add src/panes/spawn/AdvancedOptions.tsx src/panes/spawn/Spawn.tsx src/panes/spawn/advancedOptions.module.css src/panes/spawn/AdvancedOptions.test.tsx src/panes/spawn/Spawn.test.tsx
git commit -m "fix(spawn): one scroll surface, Access mode inside Advanced (9ct0)"
```

---

## S6 — Composer

### Task 15: Pasted-image thumbnail tile + lightbox (677w)

A pasted image is fully staged (base64 + decoded W×H) but renders as a text-only chip. Render each image attachment as a thumbnail tile — the actual image, dimensions overlaid, ✕ to remove — and clicking it opens the existing lightbox. Non-image attachments keep a labeled file tile.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx:551-564` (attachment preview → thumbnail tile)
- Possibly Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/flow/ImageGallery.tsx` (only if the lightbox needs a dimensions overlay / ✕; otherwise reuse as-is via its `Dialog`)
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`

**Interfaces:**
- Consumes: `PendingAttachment { marker, name, mediaType, width?, height?, data?, pending }` from `attachments/useAttachments.ts` — build the `src` as `data:${mediaType};base64,${data}` (the stored `data` has no `data:` prefix); `removeItem(marker)` to drop. The existing lightbox is `flow/ImageGallery` (click-to-expand, prev/next, Esc/backdrop close via shared `Dialog`; props `{images: string[]}`; testids `image-gallery-thumb`, `image-gallery-lightbox-img`).
- Produces: the thumbnail tile UI (§5 only builds the tile; the lightbox already exists).

- [ ] **Step 1: Write the failing test**

In `Composer.test.tsx` (use the existing `pastePngInto` + `installCanvasStubs` helpers, FakeImage 4×4): paste an image, assert an `<img>` thumbnail renders (not just a text chip) with a `src` beginning `data:image/png;base64,`, that the dimensions (`4×4`) are shown, and that clicking the ✕ calls `removeItem`/drops the attachment. Assert clicking the thumbnail opens the lightbox (`image-gallery-lightbox-img` appears).

- [ ] **Step 2: Run it, verify it fails**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session -t "thumbnail"`
Expected: FAIL — current preview is a text-only `<Chip>`.

- [ ] **Step 3: Render the thumbnail tile**

In `Composer.tsx:551-564`, replace the text `<Chip>` preview for image attachments with a tile: an `<img src={`data:${a.mediaType};base64,${a.data}`}>`, a dimensions overlay (`{a.width}×{a.height}`), and an ✕ `IconButton` calling `removeItem(a.marker)`. Wire click-to-open through `ImageGallery` (pass the single image's data-URL as its `images` array) or open the shared `Dialog` lightbox directly. Keep non-image attachments on the labeled file tile fallback.

- [ ] **Step 4: Run the test, verify it passes**

Run: `npx vitest run src/panes/session -t "thumbnail"`
Expected: PASS.

- [ ] **Step 5: Token contract + compile + commit**

```bash
npx vitest run src/styles/token-contract.test.ts && npm run typecheck
git add src/panes/session
git commit -m "feat(composer): pasted-image thumbnail tile + lightbox (677w)"
```

---

## Final integration gate (controller-owned, after all streams merge)

- [ ] **Step 1: Merge every stream branch into the integration branch** (controller resolves the one expected `Rail.tsx` overlap between S2 and S3, and the `reducer.ts`/`model.ts` shared edits between Tasks 3 and 4 if they landed on separate branches).
- [ ] **Step 2: Full typecheck** — `cd cmd/serf-hub/frontend && npm run typecheck`. Expected: clean.
- [ ] **Step 3: Full lint** — `npm run lint`. Expected: clean (biome).
- [ ] **Step 4: Full build** — `npm run build`. Expected: succeeds.
- [ ] **Step 5: Full test run** — `npm test`. Record pass/fail; UI failures are not merge-blockers per the phase steer, but the **token-contract test MUST pass** and any NEW test written in this plan MUST pass. Triage new-vs-baseline failures against kata 4wgg.
- [ ] **Step 6: No Go touched** — `git diff --name-only <base>..HEAD | grep -E '\.go$|types\.gen\.ts$'` returns nothing. Expected: empty (the §4.3 reversal means zero Go/codegen change).
- [ ] **Step 7: Close the twelve kata** with evidence (per-defect commit shas + the passing new tests): c8gt, 3w2p, vbh8, 4y12, yhmh, 9ct0, qb8e, yd16, tv5k, 677w, yt2q, zrzr.

---

## Self-review notes (coverage map)

- **c8gt** → Task 6 (sidebar chrome). **3w2p** → Task 5 (dock ≥900). **vbh8** → Tasks 7 + 8 (inline attention + deep tree). **4y12** → Task 12 (chip combobox). **yhmh** → Task 13 (one dir field). **9ct0** → Task 14 (one scroll + Advanced below prompt). **yd16** → Task 9 (live status write-back — the plumbing). **qb8e** → Task 10 (real disclosure). **tv5k** → Task 10 (Mandate/Activity/Summary body). **677w** → Task 15 (thumbnail + lightbox). **yt2q** → Tasks 1 + 11 (store-backed disclosure + migrations). **zrzr** → Task 4 (merge-by-CallID).
- **§3.4 float-everywhere** → Task 2 primitive, applied in Tasks 12 + 13. **§4.3 reversal** → Task 9 (plumbing) + Task 10 (card) source summary + status from the live child watch; **no wire field, no Go, no codegen** (integration gate Step 6 enforces).
- **§2.3 live per-node Cadence traces** are explicitly OUT (Task 8 keeps honest empty frames) — the sidebar has no per-node live frame stream this round; the spec's live-trace aspiration is corrected here to match the shipped `frameTimes` producers.
- **Token contract** guarded after every UI task; the two new widgets (Disclosure, Popover) use no gated hue, so `SEMANTIC_USE_ALLOWLIST` needs no edit.
