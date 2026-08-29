# Mobile Welcome + Sidebar Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify the mobile welcome screen and the bottom-panel sidebar into a single expandable surface that slides up to full screen, move key bindings to the panel's bottom, and remove example task prompts on both mobile and desktop.

**Architecture:** Add an opt-in `expandable` capability to the shared `Sheet` widget (drag handle + peek/full geometry). Extract welcome content into a reusable `WelcomeContent` component. Build a `MobilePanel` that composes the Sheet with the forwarded rail + welcome content. `StackHost` owns the panel-open state and wires both entry points.

**Tech Stack:** React 19, Zustand, CSS Modules, Vitest + Testing Library, Biome.

**Spec:** `docs/superpowers/specs/2026-08-29-mobile-welcome-panel-design.md`

## Global Constraints

- Panes never ask "am I mobile?" — the mobile panel is a shell-level composition in `src/shell/mobile/`, not a behavior switch inside `Welcome.tsx`.
- The mobile stream (`src/shell/mobile/**`) must NOT import from `src/shell/rail/**` — they meet only through props wired by the integrator (StackHost forwards `railSlot`).
- The `Sheet` widget's `expandable` prop defaults to `undefined`; every existing `<Sheet>` and `<Dialog>` consumer stays byte-for-byte identical.
- `OverlayPanel`'s new `handle`, `bodyClassName`, and `headerClassName` props all default to `undefined`.
- Frontend gate: `npx biome check --write src/` on touched files, then `make test-web` (and `make test-web-browser` on Chrome-capable hosts).
- No `noNonNullAssertion` or array-index-key Biome violations.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/widgets/dialog/OverlayPanel.tsx` | Add `handle`, `bodyClassName`, `headerClassName` optional props (all default undefined). |
| `src/widgets/sheet/index.tsx` | Add `expandable` prop; manage geometry state; pass handle + body/header classes through OverlayPanel. |
| `src/widgets/sheet/sheet.module.css` | Add `.handle`, `.expandableBottom`, `.expandableHeader`, `.expandableBody` rules. |
| `src/widgets/sheet/sheet.test.tsx` | Add expandable tests. |
| `src/panes/welcome/WelcomeContent.tsx` | **New:** presentational welcome content (`showNewSession`, `showHints` props). |
| `src/panes/welcome/Welcome.tsx` | Thin wrapper over `WelcomeContent`; remove example prompts. |
| `src/panes/welcome/welcome.module.css` | Remove `.examples*` rules. |
| `src/panes/welcome/Welcome.test.tsx` | Remove example-prompt test; keep rest. |
| `src/shell/mobile/MobilePanel.tsx` | **New:** unified panel (Sheet + forwarded `rail` prop + WelcomeContent). Owns auto-close effect. |
| `src/shell/mobile/MobilePanel.module.css` | **New:** panel layout styles. |
| `src/shell/mobile/MobilePanel.test.tsx` | **New:** panel state/content tests. |
| `src/shell/mobile/TreeDrawer.tsx` | Trigger only; receives `onOpen` prop. |
| `src/shell/mobile/TreeDrawer.test.tsx` | Update: no owned Sheet; assert trigger calls onOpen. |
| `src/shell/mobile/StackHost.tsx` | Own shared panel-open `useState`; open panel when `nothingFocused` (guarded by `routeDeferred`); forward `railSlot` to MobilePanel; pass `onOpen` to TreeDrawer. |
| `src/shell/mobile/StackHost.test.tsx` | Update welcome-landing assertions; add routeDeferred flash test. |

---

### Task 1: Add `handle`, `bodyClassName`, `headerClassName` props to OverlayPanel

**Files:**
- Modify: `src/widgets/dialog/OverlayPanel.tsx`
- Test: `src/widgets/dialog/OverlayPanel.tsx` (no separate test file — the existing `sheet.test.tsx` and `dialog.test.tsx` exercise OverlayPanel; this task adds no new behavior, only optional passthrough props)

**Interfaces:**
- Produces: `OverlayPanelProps` gains `handle?: ReactNode`, `bodyClassName?: string`, `headerClassName?: string`. The handle renders before `<header>`. `bodyClassName` and `headerClassName` are appended to the existing `.body` and `.header` classes respectively.

- [ ] **Step 1: Add the props to OverlayPanelProps**

In `src/widgets/dialog/OverlayPanel.tsx`, add three optional props to the interface:

```ts
export interface OverlayPanelProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  panelClassName: string;
  handle?: ReactNode;
  bodyClassName?: string;
  headerClassName?: string;
}
```

- [ ] **Step 2: Render the handle before the header and apply body/header classes**

Update the `OverlayPanel` function signature to accept the new props and render them:

```tsx
export function OverlayPanel({ open, onClose, title, children, footer, panelClassName, handle, bodyClassName, headerClassName }: OverlayPanelProps) {
```

In the JSX, render `{handle}` before `<header>`, and append the optional classes:

```tsx
        <div
          role="dialog"
          aria-modal="true"
          aria-labelledby={titleId}
          className={panelClassName}
          onKeyDown={handleKeyDown}
        >
          {handle}
          <header className={headerClassName ? `${CLASS.header} ${headerClassName}` : CLASS.header}>
            <h2 id={titleId} className={CLASS.title}>
              {title}
            </h2>
          </header>
          <div className={bodyClassName ? `${CLASS.body} ${bodyClassName}` : CLASS.body}>{children}</div>
          {footer !== undefined && <div className={CLASS.footer}>{footer}</div>}
          <button type="button" className={CLASS.closeButton} onClick={onClose} aria-label="Close">
            <CloseIcon />
          </button>
        </div>
```

- [ ] **Step 3: Verify existing tests still pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/sheet/sheet.test.tsx src/widgets/dialog/ --maxWorkers=4`
Expected: PASS (all existing tests — the new props default to undefined, so no existing consumer changes).

- [ ] **Step 4: Commit**

```bash
git add src/widgets/dialog/OverlayPanel.tsx
git commit -m "feat: add handle/bodyClassName/headerClassName slots to OverlayPanel"
```

---

### Task 2: Add `expandable` capability to Sheet

**Files:**
- Modify: `src/widgets/sheet/index.tsx`
- Modify: `src/widgets/sheet/sheet.module.css`
- Test: `src/widgets/sheet/sheet.test.tsx`

**Interfaces:**
- Consumes: `OverlayPanel`'s `handle`, `bodyClassName`, `headerClassName` props (Task 1).
- Produces: `SheetProps` gains `expandable?: { peekHeight: number; fullScreenFirst?: boolean }`. When set, the Sheet renders a drag handle, manages a `"peek" | "full"` geometry state, resets to full on every open transition, and passes scoped body/header classes to OverlayPanel.

- [ ] **Step 1: Write failing tests for expandable mode**

Add these tests to `src/widgets/sheet/sheet.test.tsx`:

```tsx
test("expandable renders a drag handle", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200 }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.getByRole("dialog").querySelector("[data-testid='sheet-handle']")).toBeTruthy();
});

test("non-expandable renders no drag handle", () => {
  render(
    <Sheet open side="bottom" onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.queryByTestId("sheet-handle")).toBeNull();
});

test("fullScreenFirst starts at full on mount", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  expect(panel.getAttribute("data-geometry")).toBe("full");
});

test("fullScreenFirst resets to full on reopen", async () => {
  const { rerender } = render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  // Simulate user dragging to peek
  fireEvent.pointerDown(screen.getByTestId("sheet-handle"), { clientY: 500 });
  fireEvent.pointerMove(window, { clientY: 800 });
  fireEvent.pointerUp(window, { clientY: 800 });
  expect(panel.getAttribute("data-geometry")).toBe("peek");
  // Close and reopen
  rerender(
    <Sheet open={false} side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  rerender(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: true }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  expect(screen.getByRole("dialog").getAttribute("data-geometry")).toBe("full");
});

test("tap on handle toggles peek to full", () => {
  render(
    <Sheet open side="bottom" expandable={{ peekHeight: 200, fullScreenFirst: false }} onClose={vi.fn()} title="t">
      Body
    </Sheet>,
  );
  const panel = screen.getByRole("dialog");
  // Starts at peek (fullScreenFirst is false)
  expect(panel.getAttribute("data-geometry")).toBe("peek");
  // Tap toggles to full
  fireEvent.pointerDown(screen.getByTestId("sheet-handle"), { clientY: 100 });
  fireEvent.pointerUp(window, { clientY: 100 });
  expect(panel.getAttribute("data-geometry")).toBe("full");
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/sheet/sheet.test.tsx --maxWorkers=4`
Expected: FAIL — `expandable` prop and handle not yet implemented.

- [ ] **Step 3: Add CSS rules for handle and expandable classes**

In `src/widgets/sheet/sheet.module.css`, add:

```css
.handle {
  display: flex;
  justify-content: center;
  padding: var(--space-2) 0 var(--space-1);
  cursor: grab;
  touch-action: none;
}

.handleBar {
  width: 36px;
  height: 4px;
  border-radius: 2px;
  background: var(--ink-low);
}

.expandableBottom {
  height: 100vh;
  transition: height var(--motion-duration-overlay) var(--motion-easing-standard);
}

.expandableHeader {
  position: sticky;
  top: 0;
  z-index: 1;
  background: var(--surface-inset);
}

.expandableBody {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

@media (prefers-reduced-motion: reduce) {
  .expandableBottom {
    transition: none;
  }
}
```

- [ ] **Step 4: Implement the expandable capability in Sheet**

Rewrite `src/widgets/sheet/index.tsx`:

```tsx
import { type ReactNode, useEffect, useRef, useState } from "react";
import dialogStyles from "../dialog/dialog.module.css";
import { OverlayPanel } from "../dialog/OverlayPanel";
import { requireClass } from "../internal/requireClass";
import styles from "./sheet.module.css";

export type SheetSide = "right" | "bottom" | "left";
export type SheetSize = "standard" | "wide";

export interface ExpandableConfig {
  peekHeight: number;
  fullScreenFirst?: boolean;
}

export interface SheetProps {
  side?: SheetSide;
  size?: SheetSize;
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  expandable?: ExpandableConfig;
}

const BASE_PANEL_CLASS = requireClass(dialogStyles.panel, "dialog.module.css", "panel");

const SIDE_CLASS: Record<SheetSide, string> = {
  right: `${BASE_PANEL_CLASS} ${requireClass(styles.right, "sheet.module.css", "right")}`,
  bottom: `${BASE_PANEL_CLASS} ${requireClass(styles.bottom, "sheet.module.css", "bottom")}`,
  left: `${BASE_PANEL_CLASS} ${requireClass(styles.left, "sheet.module.css", "left")}`,
};

const SIZE_CLASS: Record<SheetSize, string> = {
  standard: requireClass(styles.standard, "sheet.module.css", "standard"),
  wide: requireClass(styles.wide, "sheet.module.css", "wide"),
};

const HANDLE_CLASS = requireClass(styles.handle, "sheet.module.css", "handle");
const HANDLE_BAR_CLASS = requireClass(styles.handleBar, "sheet.module.css", "handleBar");
const EXPANDABLE_BOTTOM_CLASS = requireClass(styles.expandableBottom, "sheet.module.css", "expandableBottom");
const EXPANDABLE_HEADER_CLASS = requireClass(styles.expandableHeader, "sheet.module.css", "expandableHeader");
const EXPANDABLE_BODY_CLASS = requireClass(styles.expandableBody, "sheet.module.css", "expandableBody");

function DragHandle({ onPointerDown }: { onPointerDown: (e: React.PointerEvent) => void }) {
  return (
    <div className={HANDLE_CLASS} data-testid="sheet-handle" onPointerDown={onPointerDown}>
      <div className={HANDLE_BAR_CLASS} />
    </div>
  );
}

export function Sheet({ side = "right", size = "standard", open, onClose, title, children, footer, expandable }: SheetProps) {
  const [geometry, setGeometry] = useState<"peek" | "full">(
    expandable?.fullScreenFirst ? "full" : "peek",
  );
  const dragStartYRef = useRef<number | null>(null);
  const dragStartGeometryRef = useRef<"peek" | "full">("peek");

  // Reset geometry to full on every open transition (false→true).
  useEffect(() => {
    if (open && expandable?.fullScreenFirst) setGeometry("full");
  }, [open, expandable?.fullScreenFirst]);

  function handlePointerDown(e: React.PointerEvent) {
    dragStartYRef.current = e.clientY;
    dragStartGeometryRef.current = geometry;
  }

  function handlePointerMove(e: PointerEvent) {
    if (dragStartYRef.current === null) return;
    const delta = e.clientY - dragStartYRef.current;
    // Dragging down from full toward peek, or up from peek toward full
    if (dragStartGeometryRef.current === "full" && delta > 50) setGeometry("peek");
    else if (dragStartGeometryRef.current === "peek" && delta < -50) setGeometry("full");
  }

  function handlePointerUp(e: PointerEvent) {
    if (dragStartYRef.current === null) return;
    const delta = e.clientY - dragStartYRef.current;
    const wasDrag = Math.abs(delta) > 10;
    dragStartYRef.current = null;
    if (!wasDrag) {
      // Tap toggles
      setGeometry((g) => (g === "peek" ? "full" : "peek"));
    }
    window.removeEventListener("pointermove", handlePointerMove);
    window.removeEventListener("pointerup", handlePointerUp);
  }

  function startDrag(e: React.PointerEvent) {
    handlePointerDown(e);
    window.addEventListener("pointermove", handlePointerMove);
    window.addEventListener("pointerup", handlePointerUp);
  }

  if (!expandable) {
    return (
      <OverlayPanel
        open={open}
        onClose={onClose}
        title={title}
        footer={footer}
        panelClassName={`${SIDE_CLASS[side]} ${SIZE_CLASS[size]}`}
      >
        {children}
      </OverlayPanel>
    );
  }

  const heightStyle: React.CSSProperties = geometry === "full" ? { height: "100vh" } : { height: `${expandable.peekHeight}px` };

  return (
    <OverlayPanel
      open={open}
      onClose={onClose}
      title={title}
      footer={footer}
      handle={<DragHandle onPointerDown={startDrag} />}
      headerClassName={EXPANDABLE_HEADER_CLASS}
      bodyClassName={EXPANDABLE_BODY_CLASS}
      panelClassName={`${SIDE_CLASS[side]} ${EXPANDABLE_BOTTOM_CLASS}`}
    >
      <div style={heightStyle} data-geometry={geometry}>
        {children}
      </div>
    </OverlayPanel>
  );
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/widgets/sheet/sheet.test.tsx --maxWorkers=4`
Expected: PASS

- [ ] **Step 6: Run biome on touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/widgets/sheet/index.tsx src/widgets/sheet/sheet.module.css src/widgets/sheet/sheet.test.tsx`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add src/widgets/sheet/index.tsx src/widgets/sheet/sheet.module.css src/widgets/sheet/sheet.test.tsx
git commit -m "feat: add expandable capability to Sheet widget"
```

---

### Task 3: Extract WelcomeContent and remove example prompts

**Files:**
- Create: `src/panes/welcome/WelcomeContent.tsx`
- Modify: `src/panes/welcome/Welcome.tsx`
- Modify: `src/panes/welcome/welcome.module.css`
- Test: `src/panes/welcome/Welcome.test.tsx`

**Interfaces:**
- Produces: `WelcomeContent` component with props `{ note?: string; showNewSession?: boolean; showHints?: boolean }`. Renders "Jump back in" (resume candidate), "New session" (when `showNewSession`), orientation text, chord hints (when `showHints`). No example prompts.

- [ ] **Step 1: Write failing test for WelcomeContent absence of example prompts**

Add to `src/panes/welcome/Welcome.test.tsx` (or create `WelcomeContent.test.tsx`):

```tsx
import { WelcomeContent } from "./WelcomeContent";

test("WelcomeContent does not render example prompts", () => {
  render(<WelcomeContent />);
  expect(screen.queryByRole("button", { name: /Find and fix the root cause/i })).toBeNull();
  expect(screen.queryByText("Try a task to get started")).toBeNull();
});

test("WelcomeContent renders New session only when showNewSession is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryByRole("button", { name: "New session" })).toBeNull();
  rerender(<WelcomeContent showNewSession />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("WelcomeContent renders chord hints only when showHints is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryByText("command palette")).toBeNull();
  rerender(<WelcomeContent showHints />);
  expect(screen.getByText("command palette")).toBeTruthy();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/welcome/ --maxWorkers=4`
Expected: FAIL — `WelcomeContent` not yet created.

- [ ] **Step 3: Create WelcomeContent.tsx**

Create `src/panes/welcome/WelcomeContent.tsx`:

```tsx
import { navigate, paneToURL } from "../../shell/routing";
import { selectLiveRows, selectNeedsYouRows } from "../../stores/navigation/selectors";
import { type NavigationStoreState, useNavigationStore } from "../../stores/navigation/store";
import { Button, KeyHint } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./welcome.module.css";

const CLASS = {
  actions: requireClass(styles.actions, "welcome.module.css", "actions"),
  hints: requireClass(styles.hints, "welcome.module.css", "hints"),
  hintRow: requireClass(styles.hintRow, "welcome.module.css", "hintRow"),
  hintFooter: requireClass(styles.hintFooter, "welcome.module.css", "hintFooter"),
};

const CHORD_HINTS: { keys: string[]; desc: string }[] = [
  { keys: ["Mod", "K"], desc: "command palette" },
  { keys: ["Mod", "I"], desc: "focus the composer" },
  { keys: ["Mod", "J"], desc: "next session needing you" },
];

export interface WelcomeContentProps {
  note?: string;
  showNewSession?: boolean;
  showHints?: boolean;
}

function goToNewSession(): void {
  const url = paneToURL("spawn", {});
  if (url) navigate(url);
}

function goToSession(ref: string): void {
  const url = paneToURL("session", { ref });
  if (url) navigate(url);
}

function resumeCandidate(navigation: NavigationStoreState) {
  return selectNeedsYouRows(navigation)[0] ?? selectLiveRows(navigation)[0];
}

export function WelcomeContent({ note, showNewSession, showHints }: WelcomeContentProps) {
  const navigation = useNavigationStore();
  const candidate = resumeCandidate(navigation);

  return (
    <div className={CLASS.actions}>
      {note && <p>{note}</p>}
      {candidate !== undefined && (
        <Button variant="primary" onClick={() => goToSession(candidate.ref)}>
          Jump back in: {candidate.title}
        </Button>
      )}
      {showNewSession && (
        <Button variant="secondary" onClick={goToNewSession}>
          New session
        </Button>
      )}
      <p className={styles.orientation}>
        A session can read and edit the repository, run commands, and delegate work to helpers.
      </p>
      {showHints && (
        <div className={CLASS.hints}>
          {CHORD_HINTS.map((hint) => (
            <div className={CLASS.hintRow} key={hint.desc}>
              <KeyHint keys={hint.keys} />
              <span>{hint.desc}</span>
            </div>
          ))}
          <p className={CLASS.hintFooter}>
            <KeyHint keys={["?"]} /> inside the command palette shows all shortcuts.
          </p>
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Simplify Welcome.tsx to a thin wrapper**

Rewrite `src/panes/welcome/Welcome.tsx`:

```tsx
import type { PaneProps } from "../../shell/paneRegistry";
import { EmptyState, PaneScaffold } from "../../widgets";
import { WelcomeContent } from "./WelcomeContent";

export interface WelcomePaneParams {
  note?: string;
}

export default function Welcome({ params }: PaneProps<WelcomePaneParams>) {
  return (
    <PaneScaffold title="Welcome">
      <EmptyState
        title="No session open"
        hint={params.note}
        action={<WelcomeContent note={params.note} showNewSession showHints />}
      />
    </PaneScaffold>
  );
}
```

- [ ] **Step 5: Remove .examples CSS rules from welcome.module.css**

In `src/panes/welcome/welcome.module.css`, delete the `.examples`, `.examplesLabel`, and `.examples button` rules (and their `@media` block if any). Keep `.actions`, `.orientation`, `.hints`, `.hintRow`, `.hintFooter`.

- [ ] **Step 6: Remove the example-prompt test and goToExample test from Welcome.test.tsx**

Delete the test `"offers concrete example tasks that activate the real Spawn prefill"`. Keep all other tests (chord hints, orientation, Jump back in, New session, note hint).

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/panes/welcome/ --maxWorkers=4`
Expected: PASS

- [ ] **Step 8: Run biome on touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/panes/welcome/WelcomeContent.tsx src/panes/welcome/Welcome.tsx src/panes/welcome/welcome.module.css src/panes/welcome/Welcome.test.tsx`
Expected: no errors

- [ ] **Step 9: Commit**

```bash
git add src/panes/welcome/WelcomeContent.tsx src/panes/welcome/Welcome.tsx src/panes/welcome/welcome.module.css src/panes/welcome/Welcome.test.tsx
git commit -m "feat: extract WelcomeContent, remove example prompts"
```

---

### Task 4: Build MobilePanel

**Files:**
- Create: `src/shell/mobile/MobilePanel.tsx`
- Create: `src/shell/mobile/MobilePanel.module.css`
- Test: `src/shell/mobile/MobilePanel.test.tsx`

**Interfaces:**
- Consumes: `Sheet` with `expandable` (Task 2), `WelcomeContent` (Task 3), `useWorkspaceStore`, `useNavigationStore`.
- Produces: `MobilePanel` component with props `{ rail: ReactNode; open: boolean; onClose: () => void }`. Renders the Sheet with rail content + welcome content (when `nothingFocused`). Owns the auto-close effect (calls `onClose` when `focusedPaneId` changes while open).

- [ ] **Step 1: Write failing tests for MobilePanel**

Create `src/shell/mobile/MobilePanel.test.tsx`:

```tsx
import { type ReactNode, cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { MobilePanel } from "./MobilePanel";

afterEach(cleanup);

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

function RailFixture() {
  return <div data-testid="rail-fixture">Rail</div>;
}

test("renders rail content when open", () => {
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.getByTestId("rail-fixture")).toBeTruthy();
});

test("renders welcome content when nothing is focused", () => {
  // Nothing focused → backstop opens welcome
  workspaceStore.getState().openPane("welcome");
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.getByText(/read and edit the repository/i)).toBeTruthy();
});

test("hides welcome content when a session is focused", () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  render(<MobilePanel rail={<RailFixture />} open onClose={vi.fn()} />);
  expect(screen.queryByText(/read and edit the repository/i)).toBeNull();
  expect(screen.getByTestId("rail-fixture")).toBeTruthy();
});

test("calls onClose when focusedPaneId changes while open", () => {
  workspaceStore.getState().openPane("doc", { ref: "ref_a" });
  const onClose = vi.fn();
  render(<MobilePanel rail={<RailFixture />} open onClose={onClose} />);
  expect(onClose).not.toHaveBeenCalled();
  workspaceStore.getState().openPane("doc", { ref: "ref_b" });
  expect(onClose).toHaveBeenCalled();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/MobilePanel.test.tsx --maxWorkers=4`
Expected: FAIL — `MobilePanel` not yet created.

- [ ] **Step 3: Create MobilePanel.tsx**

Create `src/shell/mobile/MobilePanel.tsx`:

```tsx
import { type ReactNode, useEffect, useRef } from "react";
import { WelcomeContent } from "../../panes/welcome/WelcomeContent";
import { Sheet } from "../../widgets";
import { useWorkspaceStore } from "../workspace";

const PEEK_HEIGHT = 360;

export interface MobilePanelProps {
  rail: ReactNode;
  open: boolean;
  onClose: () => void;
}

export function MobilePanel({ rail, open, onClose }: MobilePanelProps) {
  const focusedPaneId = useWorkspaceStore((s) => s.focusedPaneId);
  const panes = useWorkspaceStore((s) => s.panes);
  const focusedPane = panes.find((p) => p.id === focusedPaneId) ?? null;
  const nothingFocused = focusedPaneId === null || focusedPane?.type === "welcome";

  const prevFocusedIdRef = useRef(focusedPaneId);

  // Auto-close on navigation: when focusedPaneId changes while the panel
  // is open, close it (same pattern as TreeDrawer's old effect).
  useEffect(() => {
    if (open && prevFocusedIdRef.current !== focusedPaneId) onClose();
    prevFocusedIdRef.current = focusedPaneId;
  }, [focusedPaneId, open, onClose]);

  return (
    <Sheet
      side="bottom"
      open={open}
      onClose={onClose}
      title="Sessions"
      expandable={{ peekHeight: PEEK_HEIGHT, fullScreenFirst: true }}
    >
      {rail}
      {nothingFocused && <WelcomeContent showHints />}
    </Sheet>
  );
}
```

- [ ] **Step 4: Create MobilePanel.module.css**

Create `src/shell/mobile/MobilePanel.module.css`:

```css
.panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/MobilePanel.test.tsx --maxWorkers=4`
Expected: PASS

- [ ] **Step 6: Run biome on touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/shell/mobile/MobilePanel.tsx src/shell/mobile/MobilePanel.module.css src/shell/mobile/MobilePanel.test.tsx`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add src/shell/mobile/MobilePanel.tsx src/shell/mobile/MobilePanel.module.css src/shell/mobile/MobilePanel.test.tsx
git commit -m "feat: add MobilePanel unified mobile panel"
```

---

### Task 5: Convert TreeDrawer to trigger-only

**Files:**
- Modify: `src/shell/mobile/TreeDrawer.tsx`
- Test: `src/shell/mobile/TreeDrawer.test.tsx`

**Interfaces:**
- Consumes: `onOpen` callback prop (from StackHost).
- Produces: `TreeDrawer` renders only the trigger button + badge; calls `onOpen` on click. No longer owns a `Sheet`.

- [ ] **Step 1: Write failing test for trigger-only TreeDrawer**

Add to `src/shell/mobile/TreeDrawer.test.tsx`:

```tsx
test("trigger calls onOpen instead of opening a Sheet", async () => {
  const onOpen = vi.fn();
  const user = userEvent.setup();
  render(<TreeDrawer onOpen={onOpen} />);
  await user.click(screen.getByRole("button", { name: "Sessions" }));
  expect(onOpen).toHaveBeenCalled();
  // No dialog (Sheet) should be rendered by TreeDrawer anymore
  expect(screen.queryByRole("dialog")).toBeNull();
});
```

- [ ] **Step 2: Run tests to verify it fails**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/TreeDrawer.test.tsx --maxWorkers=4`
Expected: FAIL — TreeDrawer still owns a Sheet.

- [ ] **Step 3: Rewrite TreeDrawer as trigger-only**

Rewrite `src/shell/mobile/TreeDrawer.tsx`:

```tsx
import { useMemo } from "react";
import { selectNeedsYouCount } from "../../stores/navigation/selectors";
import { useNavigationStore } from "../../stores/navigation/store";
import { Badge, IconButton } from "../../widgets";
import styles from "./treedrawer.module.css";

function SessionsIcon() {
  return (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M2 4 H14 M2 8 H14 M2 12 H14" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  );
}

export interface TreeDrawerProps {
  onOpen: () => void;
}

export function TreeDrawer({ onOpen }: TreeDrawerProps) {
  const navigation = useNavigationStore();
  const needsYou = useMemo(() => selectNeedsYouCount(navigation), [navigation]);

  return (
    <span className={styles.triggerWrap}>
      <IconButton label="Sessions" icon={<SessionsIcon />} variant="quiet" onClick={onOpen} />
      {needsYou > 0 && (
        <span className={styles.badgeOverlay}>
          <Badge count={needsYou} tone="attention" />
        </span>
      )}
    </span>
  );
}
```

- [ ] **Step 4: Update existing TreeDrawer tests**

In `src/shell/mobile/TreeDrawer.test.tsx`, update tests that render `<TreeDrawer>` to pass `onOpen={vi.fn()}`. Remove tests that assert Sheet behavior (open/close, auto-close, placeholder). Keep the badge tests.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/TreeDrawer.test.tsx --maxWorkers=4`
Expected: PASS

- [ ] **Step 6: Run biome on touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/shell/mobile/TreeDrawer.tsx src/shell/mobile/TreeDrawer.test.tsx`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add src/shell/mobile/TreeDrawer.tsx src/shell/mobile/TreeDrawer.test.tsx
git commit -m "refactor: convert TreeDrawer to trigger-only"
```

---

### Task 6: Wire StackHost to own panel-open state and render MobilePanel

**Files:**
- Modify: `src/shell/mobile/StackHost.tsx`
- Test: `src/shell/mobile/StackHost.test.tsx`

**Interfaces:**
- Consumes: `MobilePanel` (Task 4), `TreeDrawer` with `onOpen` (Task 5).
- Produces: StackHost owns `useState<boolean>` for panel-open; opens panel when `nothingFocused` (guarded by `routeDeferred`); passes `railSlot` to MobilePanel as `rail`; passes `onOpen` to TreeDrawer; passes `open`/`onClose` to MobilePanel.

- [ ] **Step 1: Write failing test for panel opening on nothingFocused**

Add to `src/shell/mobile/StackHost.test.tsx`:

```tsx
test("opens the mobile panel when nothing is focused", async () => {
  render(<StackHost />);
  // Welcome pane mounts behind the panel; the panel opens over it
  await screen.findByText("No session open");
  // The panel's Sheet title "Sessions" should be visible
  expect(screen.getByRole("dialog", { name: "Sessions" })).toBeTruthy();
});

test("does not flash the panel during routeDeferred", async () => {
  render(<StackHost routeDeferred />);
  // Wait for welcome to appear
  await screen.findByText("No session open");
  // Panel should NOT be open while routeDeferred
  expect(screen.queryByRole("dialog", { name: "Sessions" })).toBeNull();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/StackHost.test.tsx --maxWorkers=4`
Expected: FAIL — StackHost doesn't render MobilePanel yet.

- [ ] **Step 3: Wire StackHost to own panel-open state and render MobilePanel**

In `src/shell/mobile/StackHost.tsx`:

1. Add `useState` import (already imported from React).
2. Add import: `import { MobilePanel } from "./MobilePanel";`
3. In the `StackHost` function, add panel-open state:
   ```tsx
   const [panelOpen, setPanelOpen] = useState(false);
   ```
4. Compute `nothingFocused`:
   ```tsx
   const nothingFocused = focusedPaneId === null || focusedPane?.type === "welcome";
   ```
5. Add the nothingFocused-open effect (after the existing backstop effect):
   ```tsx
   useEffect(() => {
     if (routeDeferred) return;
     if (nothingFocused) setPanelOpen(true);
   }, [nothingFocused, routeDeferred]);
   ```
6. Replace `<TreeDrawer>{railSlot}</TreeDrawer>` in the topBar with:
   ```tsx
   <TreeDrawer onOpen={() => setPanelOpen(true)} />
   ```
7. After the `<div className={styles.body}>...</div>` block (but inside the host div), add:
   ```tsx
   <MobilePanel rail={railSlot} open={panelOpen} onClose={() => setPanelOpen(false)} />
   ```

- [ ] **Step 4: Update existing StackHost tests**

In `src/shell/mobile/StackHost.test.tsx`, update tests that assert the welcome pane is the visible surface — now the panel opens over it. The "falls back to opening welcome" test should still find "No session open" (the welcome pane renders behind the panel). Tests that interact with TreeDrawer's Sheet need updating to use the panel instead.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/StackHost.test.tsx --maxWorkers=4`
Expected: PASS

- [ ] **Step 6: Run biome on touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/shell/mobile/StackHost.tsx src/shell/mobile/StackHost.test.tsx`
Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add src/shell/mobile/StackHost.tsx src/shell/mobile/StackHost.test.tsx
git commit -m "feat: wire StackHost to own panel-open state and render MobilePanel"
```

---

### Task 7: Run full frontend gate and fix any regressions

**Files:**
- All touched files from Tasks 1-6.

- [ ] **Step 1: Run biome across all touched files**

Run: `cd cmd/evener-hub/frontend && npx biome check --write src/widgets/sheet/ src/widgets/dialog/OverlayPanel.tsx src/panes/welcome/ src/shell/mobile/`
Expected: no errors

- [ ] **Step 2: Run typecheck**

Run: `cd cmd/evener-hub/frontend && npx tsc --noEmit --incremental false`
Expected: no errors

- [ ] **Step 3: Run full test suite**

Run: `cd cmd/evener-hub/frontend && make test-web`
Expected: PASS

- [ ] **Step 4: Run layoutguard**

Run: `cd cmd/evener-hub/frontend && npm run layoutguard`
Expected: PASS — the expandable panel at peek and full must not overflow the viewport.

- [ ] **Step 5: Run overflowguard**

Run: `cd cmd/evener-hub/frontend && npm run overflowguard`
Expected: PASS — welcome content in the panel must not overflow at 390px.

- [ ] **Step 6: Fix any failures**

Address any test failures, type errors, or guard violations. Root-cause each failure; do not weaken assertions.

- [ ] **Step 7: Commit any fixes**

```bash
git add -A
git commit -m "fix: resolve frontend gate regressions from mobile panel unification"
```

- [ ] **Step 8: Run the full gate once more**

Run: `cd cmd/evener-hub/frontend && make test-web`
Expected: PASS

---

### Task 8: Update existing tests that depend on TreeDrawer's old Sheet behavior

**Files:**
- Test: `src/shell/mobile/StackHost.test.tsx`
- Test: `src/shell/mobile/TreeDrawer.test.tsx`
- Test: `src/shell/App.test.tsx` (if it references TreeDrawer's Sheet)

- [ ] **Step 1: Search for tests that reference TreeDrawer's Sheet**

Run: `cd cmd/evener-hub/frontend && rg -n "TreeDrawer|tree.drawer|tree-drawer" src/ -g '*.test.*'`
Review each hit: any test that asserts TreeDrawer opens a Sheet, auto-closes, or renders a placeholder must be updated to test MobilePanel instead.

- [ ] **Step 2: Fix each test**

Update each test to reflect the new architecture: TreeDrawer is a trigger, MobilePanel owns the Sheet. Tests that asserted auto-close-on-navigation should assert MobilePanel's `onClose` behavior instead.

- [ ] **Step 3: Run all mobile tests**

Run: `cd cmd/evener-hub/frontend && npx vitest run src/shell/mobile/ --maxWorkers=4`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add src/shell/mobile/StackHost.test.tsx src/shell/mobile/TreeDrawer.test.tsx
git commit -m "test: update tests for TreeDrawer trigger-only and MobilePanel"
```
