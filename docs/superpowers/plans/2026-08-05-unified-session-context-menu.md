# Unified Session Context Menu Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the two divergent session "⋯" menus (session pane and sidebar rail) with one shared `SessionMenu` component whose items are Details/Tasks/Activity — Rename/Pin/Archive — Shut down/Delete.

**Architecture:** One app-level component `src/shell/sessionMenu/SessionMenu.tsx` owns the menu item list, grouping/separators, and all dialogs (Rename, Shut down, Delete, PinSectionPicker). Each render site (SessionChrome, RailRow) maps its own data source (`ThreadModel` / `ApiTreeNode`) into a normalized prop bag plus a `SessionMenuActions` callback object; adapters own mutation side effects and failure toasts. Spec: `docs/superpowers/specs/2026-08-05-unified-session-context-menu-design.md` (commit ea57d5967).

**Tech Stack:** React 19, TypeScript, zustand stores, vitest + @testing-library/react, CSS modules, Biome.

## Global Constraints

- All work happens in the git worktree created for this feature (branch `unified-session-menu`), never in the main checkout.
- Frontend gates before finishing: `npx biome check --write` on every touched file, then `make test-web`; on this Chrome-capable host also `make test-web-browser`. All from the repo root of the worktree.
- Frontend code lives under `cmd/serf-hub/frontend/src/`. All paths below are relative to that directory unless absolute.
- Follow existing conventions verbatim: `requireClass` for CSS-module classes, `useToasts()` + `sessionActionError` for failure feedback, `Dialog`/`Button`/`Input`/`Menu` widgets from `src/widgets`, doc comments explaining *why* on every non-obvious decision (this codebase's house style).
- Avoid Biome `noNonNullAssertion` and array-index-key violations (CI checks).
- Menu keyboard contract (widgets/menu): roving tabindex, arrow/Home/End navigation must skip separators exactly as they skip disabled items.
- One owner per dialog: `SessionMenu` owns every session dialog; Rail deletes its own session rename/pin/delete dialog state and markup.
- Failure convention: adapters toast on mutation failure and rethrow; `SessionMenu` keeps the dialog open on rejection (confirm button re-enabled) and closes only on success.
- Do not change: project-row menus, the rail `+` new-session button, slash-command behavior, or any server code.

---

### Task 1: Menu widget separators

**Files:**
- Modify: `widgets/menu/index.tsx`
- Modify: `widgets/menu/menu.module.css`
- Modify: `widgets/menu/menu.test.tsx`
- Modify: `widgets/index.ts:47`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `MenuSeparator` (`{ kind: "separator"; id: string }`), `MenuEntry` (`MenuItem | MenuSeparator`), `isSeparator(entry: MenuEntry): entry is MenuSeparator`. `MenuProps.items` widens to `MenuEntry[]`. Later tasks pass separators inside the items array.

- [ ] **Step 1: Write the failing tests**

Add to `widgets/menu/menu.test.tsx` (mirror the file's existing userEvent/open-menu patterns):

```tsx
test("renders a separator between groups and skips it in arrow navigation", async () => {
  const user = userEvent.setup();
  const onA = vi.fn();
  const onB = vi.fn();
  render(
    <Menu
      trigger={<span>open</span>}
      items={[
        { id: "a", label: "A", onSelect: onA },
        { kind: "separator", id: "sep-1" },
        { id: "b", label: "B", onSelect: onB },
      ]}
    />,
  );
  await user.click(screen.getByRole("button", { name: "open" }));
  expect(screen.getByRole("separator")).toBeTruthy();
  // First enabled item is A; one ArrowDown must land on B, never the separator.
  await user.keyboard("{ArrowDown}");
  await user.keyboard("{Enter}");
  expect(onB).toHaveBeenCalledTimes(1);
  expect(onA).not.toHaveBeenCalled();
});

test("End/Home skip separators at the edges", async () => {
  const user = userEvent.setup();
  render(
    <Menu
      trigger={<span>open</span>}
      items={[
        { kind: "separator", id: "sep-top" },
        { id: "a", label: "A", onSelect: () => {} },
        { kind: "separator", id: "sep-bottom" },
      ]}
    />,
  );
  await user.click(screen.getByRole("button", { name: "open" }));
  // A is the ONLY actionable item: Home, End, and ArrowDown all land on it.
  await user.keyboard("{End}");
  expect(screen.getByRole("menuitem", { name: "A" }).tabIndex).toBe(0);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/widgets/menu/menu.test.tsx`
Expected: FAIL — `MenuSeparator` is not assignable to `MenuItem[]` (type error) / no `role="separator"` rendered.

- [ ] **Step 3: Implement separator support**

In `widgets/menu/index.tsx`, after the `MenuItem` interface:

```tsx
export interface MenuSeparator {
  kind: "separator";
  id: string;
}

export type MenuEntry = MenuItem | MenuSeparator;

export function isSeparator(entry: MenuEntry): entry is MenuSeparator {
  return "kind" in entry && entry.kind === "separator";
}

// Actionable = keyboard-reachable: separators and disabled items are both
// skipped by roving navigation, so every index helper below uses this one
// predicate instead of reading .disabled directly.
function isActionable(entry: MenuEntry): boolean {
  return !isSeparator(entry) && !entry.disabled;
}
```

Then:
- `MenuProps.items: MenuEntry[]` (update the interface).
- `firstEnabledIndex`/`lastEnabledIndex`/`stepEnabledIndex`: change `!itemAt(items, i).disabled` / `!item.disabled` to `isActionable(...)`; their `items` params become `MenuEntry[]`; `itemAt` returns `MenuEntry`.
- `selectItem(item: MenuEntry)`: early-return when `isSeparator(item)` (unreachable via keyboard, but `onClick` is only attached to actionable rows anyway — keep the guard honest).
- In the `useState(() => firstEnabledIndex(items))` and `openMenu`, no change (they call the updated helpers).
- In the popup's `items.map`, branch:

```tsx
{items.map((item, index) =>
  isSeparator(item) ? (
    <li key={item.id} role="separator" className={CLASS.separator} />
  ) : (
    // ...existing actionable <li> unchanged...
  ),
)}
```

Add `separator: requireClass(styles.separator, "menu.module.css", "separator")` to `CLASS`.

In `widgets/menu/menu.module.css` (after `.itemDisabled`):

```css
/* Group rule between item clusters (SessionMenu's panes/organize/
 * destructive groups). --edge matches .popup's own border so the rule
 * reads as the popup's own hairline, not a new color. */
.separator {
  margin: var(--space-1) 0;
  border-top: 1px solid var(--edge);
  list-style: none;
}
```

In `widgets/index.ts:47`: `export type { MenuEntry, MenuItem, MenuProps, MenuSeparator } from "./menu";` and add `export { isSeparator, Menu } from "./menu";` (replacing the existing `export { Menu }` line).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/widgets/menu/menu.test.tsx`
Expected: PASS (all pre-existing tests plus the two new ones — `MenuItem[]` is assignable to `MenuEntry[]`, so no existing consumer breaks).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/widgets/menu cmd/serf-hub/frontend/src/widgets/index.ts
git commit -m "feat(web): separator items for the Menu widget"
```

---

### Task 2: Shared foundations (pure moves + one helper)

**Files:**
- Create: `shell/rail/sessionKind.ts`
- Modify: `shell/rail/RailRow.tsx` (remove `NESTED_KINDS`/`isTopLevelSession`, import instead; keep a re-export)
- Create: `shell/deletedSessionPanes.ts`
- Modify: `shell/rail/Rail.tsx` (delete `closePanesForDeletedSessions` + `leaveDeadRoute`, import from the new module)
- Modify: `panes/sessionPanels/index.ts` (add `sessionPanelPaneType`)
- Modify: `stores/tree.ts` (add `findSessionNode`)
- Test: `stores/tree.test.ts` (add `findSessionNode` tests)

**Interfaces:**
- Produces:
  - `isTopLevelSession(session: ApiTreeNode): boolean` from `shell/rail/sessionKind.ts` (RailRow re-exports it, so existing imports keep working).
  - `closePanesForDeletedSessions(deletedIDs: string[]): void` from `shell/deletedSessionPanes.ts`.
  - `sessionPanelPaneType(kind: SessionPanelKind): "sessionTasks" | "sessionActivity" | "sessionDetails"` from `panes/sessionPanels/index.ts`.
  - `findSessionNode(tree: TreeResponse, ref: string): TreeNode | undefined` from `stores/tree.ts`.

- [ ] **Step 1: Write the failing tests**

Add to `stores/tree.test.ts` (use the file's existing node/project builders if present, else construct literals matching `TreeNode`):

```ts
test("findSessionNode finds a session nested in a project's children", () => {
  const child = { ...baseNode, ref: "local:child", children: [] };
  const parent = { ...baseNode, ref: "local:parent", children: [child] };
  const tree = { ...baseTree, projects: [{ ...baseProject, sessions: [parent] }] };
  expect(findSessionNode(tree, "local:child")?.ref).toBe("local:child");
});

test("findSessionNode searches live, pin sections, archived projects, and test runs", () => {
  // one assertion per collection: a node ONLY in live, only in
  // pin_sections[0].sessions, only in archived_projects[0].sessions,
  // only in test_runs[0].sessions is found
});

test("findSessionNode returns undefined for an unknown ref", () => {
  expect(findSessionNode(baseTree, "local:nope")).toBeUndefined();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/stores/tree.test.ts`
Expected: FAIL — `findSessionNode is not exported`.

- [ ] **Step 3: Implement**

`shell/rail/sessionKind.ts` (moved verbatim from RailRow.tsx, including its doc comment):

```ts
import type { TreeNode as ApiTreeNode } from "../../stores/tree";

// (move the NESTED_KINDS doc comment + constant + isTopLevelSession here
//  byte-for-byte from RailRow.tsx)
const NESTED_KINDS: ReadonlySet<string> = new Set(["subagent", "fork", "cluster"]);

export function isTopLevelSession(session: ApiTreeNode): boolean {
  return !NESTED_KINDS.has(session.kind);
}
```

In `shell/rail/RailRow.tsx`: delete `NESTED_KINDS` and the `isTopLevelSession` definition; add `import { isTopLevelSession } from "./sessionKind";` and, directly beneath the imports, `export { isTopLevelSession } from "./sessionKind";` so `Rail.tsx` and tests importing it from RailRow are untouched.

`shell/deletedSessionPanes.ts`: move `closePanesForDeletedSessions` and `leaveDeadRoute` (Rail.tsx lines 108-154, with their doc comments) here verbatim; `export` only `closePanesForDeletedSessions`. Imports needed at the top: `workspaceStore` from `./workspace`, `navigate`, `paneToURL`, `urlToPane` from `./routing` (copy Rail.tsx's exact import specifiers for these). In `shell/rail/Rail.tsx`: delete both functions and add `import { closePanesForDeletedSessions } from "../deletedSessionPanes";`; remove now-unused imports (`urlToPane`, `paneToURL`) from Rail.tsx — but keep `navigate` (still used by `spawnInProject`/`Settings`).

In `panes/sessionPanels/index.ts`, after `sessionPanelTitle`:

```ts
/** The workspace pane type each panel kind opens (SessionMenu, rail rows). */
export function sessionPanelPaneType(kind: SessionPanelKind): "sessionTasks" | "sessionActivity" | "sessionDetails" {
  return kind === "tasks" ? "sessionTasks" : kind === "activity" ? "sessionActivity" : "sessionDetails";
}
```

In `stores/tree.ts`, at the end:

```ts
// findSessionNode is the by-ref lookup over every collection a session can
// appear in (live, needs_you, pin sections, projects, archived projects,
// test runs) recursing through subagent children. First match wins; refs
// are unique across the response, so order only costs, never lies.
export function findSessionNode(tree: TreeResponse, ref: string): TreeNode | undefined {
  const visit = (nodes: TreeNode[]): TreeNode | undefined => {
    for (const node of nodes) {
      if (node.ref === ref) return node;
      const found = visit(node.children);
      if (found) return found;
    }
    return undefined;
  };
  const projectLists = [...tree.projects, ...tree.archived_projects, ...tree.test_runs].map((p) => p.sessions);
  const lists = [tree.live, tree.needs_you, ...tree.pin_sections.map((s) => s.sessions), ...projectLists];
  for (const list of lists) {
    const found = visit(list);
    if (found) return found;
  }
  return undefined;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/stores/tree.test.ts src/shell/rail`
Expected: PASS — new tests green, and every existing rail test still passes (pure moves, no behavior change).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src
git commit -m "refactor(web): extract sessionKind, deletedSessionPanes, sessionPanelPaneType, findSessionNode"
```

---

### Task 3: SessionMenu core (panes group + Rename + Shut down)

**Files:**
- Create: `shell/sessionMenu/SessionMenu.tsx`
- Create: `shell/sessionMenu/sessionmenu.module.css`
- Test: `shell/sessionMenu/SessionMenu.test.tsx`

**Interfaces:**
- Consumes: `MenuEntry`/`isSeparator` (Task 1), `SessionPanelKind` from `panes/sessionPanels/index.ts`.
- Produces (Task 4-6 rely on these exact names):

```ts
export type PinTarget = { section_id: string } | { section_name: string };

export interface SessionMenuActions {
  onOpenPane(pane: SessionPanelKind): void;
  onRename(name: string): Promise<void>;
  onShutdown(): Promise<void>;
  onPin(target: PinTarget, section?: PinSectionSummary): Promise<void>;
  onUnpin(): Promise<void>;
  onToggleArchive(): Promise<void>;
  onDelete(): Promise<void>;
}

export interface SessionMenuProps {
  sessionRef: string;
  title: string;
  triggerLabel: string;        // sr-only trigger name: "Session actions" / `Actions for ${title}`
  canRename: boolean;
  canShutdown: boolean;
  treeNode?: ApiTreeNode;      // Task 4: presence enables Pin/Archive/Delete per rules
  panesOpen: { details: boolean; tasks: boolean; activity: boolean };
  taskLabel?: string;          // e.g. "Tasks 3/7"; defaults to "Tasks"
  activityLabel?: string;      // e.g. "Activity · 2"; defaults to "Activity"
  actions: SessionMenuActions;
  triggerTabIndex?: number;    // -1 inside rail rows (roving tabindex contract)
}
```

- [ ] **Step 1: Write the failing tests**

Create `shell/sessionMenu/SessionMenu.test.tsx`. Setup mirrors `panes/session/chrome/SessionActionsMenu.test.tsx` (`resetToastStoreForTests`, `cleanup`, `userEvent`). Helper:

```tsx
function renderMenu(overrides: Partial<SessionMenuProps> = {}) {
  const actions: SessionMenuActions = {
    onOpenPane: vi.fn(),
    onRename: vi.fn().mockResolvedValue(undefined),
    onShutdown: vi.fn().mockResolvedValue(undefined),
    onPin: vi.fn().mockResolvedValue(undefined),
    onUnpin: vi.fn().mockResolvedValue(undefined),
    onToggleArchive: vi.fn().mockResolvedValue(undefined),
    onDelete: vi.fn().mockResolvedValue(undefined),
  };
  render(
    <SessionMenu
      sessionRef="ref_a"
      title="My session"
      triggerLabel="Session actions"
      canRename
      canShutdown
      panesOpen={{ details: false, tasks: true, activity: false }}
      actions={actions}
      {...overrides}
    />,
  );
  return actions;
}

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /session actions/i }));
}
```

Tests:

```tsx
test("panes group leads with open-state checkmarks and dispatches onOpenPane", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Details" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Tasks ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Activity" })).toBeTruthy();
  await user.click(screen.getByRole("menuitem", { name: "Tasks ✓" }));
  expect(actions.onOpenPane).toHaveBeenCalledWith("tasks");
});

test("live labels replace the plain pane names", async () => {
  const user = userEvent.setup();
  renderMenu({ taskLabel: "Tasks 3/7", activityLabel: "Activity · 2" });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Tasks 3/7 ✓" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: "Activity · 2" })).toBeTruthy();
});

test("Rename opens its dialog; saving calls onRename and closes", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  const dialog = screen.getByRole("dialog", { name: "Rename session" });
  const input = within(dialog).getByLabelText("Name");
  expect((input as HTMLInputElement).value).toBe("My session");
  await user.clear(input);
  await user.type(input, "New name");
  await user.click(within(dialog).getByRole("button", { name: "Rename" }));
  await waitFor(() => expect(actions.onRename).toHaveBeenCalledWith("New name"));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("a rejected onRename keeps the dialog open (adapter toasted)", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  actions.onRename = vi.fn().mockRejectedValue(new Error("boom"));
  // rerender with the rejecting action
  cleanup();
  renderMenu({ actions });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Rename" }));
  await user.click(screen.getByRole("button", { name: "Rename" }));
  await waitFor(() => expect(actions.onRename).toHaveBeenCalled());
  expect(screen.getByRole("dialog", { name: "Rename session" })).toBeTruthy();
});

test("Rename is disabled when canRename is false", async () => {
  const user = userEvent.setup();
  renderMenu({ canRename: false });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Rename" }).getAttribute("aria-disabled")).toBe("true");
});

test("Shut down confirms before calling onShutdown", async () => {
  const user = userEvent.setup();
  const actions = renderMenu();
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Shut down" }));
  const dialog = screen.getByRole("dialog", { name: "Shut down this session?" });
  await user.click(within(dialog).getByRole("button", { name: "Shut down" }));
  await waitFor(() => expect(actions.onShutdown).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("no organization or delete items without a treeNode", async () => {
  const user = userEvent.setup();
  renderMenu();
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/sessionMenu/SessionMenu.test.tsx`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement SessionMenu**

`shell/sessionMenu/sessionmenu.module.css` — copy `panes/session/chrome/sessionactionsmenu.module.css` verbatim (srOnly/field/label/footer/body classes).

`shell/sessionMenu/SessionMenu.tsx`:

```tsx
// SessionMenu: THE session "⋯" menu, shared by the session pane's chrome
// and the sidebar rail row (2026-08-05-unified-session-context-menu-design).
// One component owns the item list, the grouping separators, and every
// dialog (Rename / Shut down / Delete / PinSectionPicker - Task 4 adds the
// last two); each render site maps its own data source into the normalized
// props and owns its mutation side effects behind SessionMenuActions.
//
// Failure convention: the ADAPTER toasts (Rail's runAction, the chrome's
// own try/catch) and the rejected promise propagates back here, so a failed
// confirm leaves the dialog open with the confirm button re-enabled; only
// success closes it. Slash-command actions (goal/aside/compact/clear) are
// deliberately NOT here - the command palette owns those.
import { type ChangeEvent, useState } from "react";
import type { SessionPanelKind } from "../../panes/sessionPanels";
import type { PinSectionSummary, TreeNode as ApiTreeNode } from "../../stores/tree";
import { Button, Dialog, Input, Menu, type MenuEntry, useToasts } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import styles from "./sessionmenu.module.css";

export type PinTarget = { section_id: string } | { section_name: string };

export interface SessionMenuActions { /* exactly as in the Interfaces block above */ }
export interface SessionMenuProps { /* exactly as in the Interfaces block above */ }

const CLASS = {
  srOnly: requireClass(styles.srOnly, "sessionmenu.module.css", "srOnly"),
  field: requireClass(styles.field, "sessionmenu.module.css", "field"),
  label: requireClass(styles.label, "sessionmenu.module.css", "label"),
  footer: requireClass(styles.footer, "sessionmenu.module.css", "footer"),
  body: requireClass(styles.body, "sessionmenu.module.css", "body"),
};

const checked = (label: string, open: boolean) => (open ? `${label} ✓` : label);

export function SessionMenu({
  sessionRef,
  title,
  triggerLabel,
  canRename,
  canShutdown,
  panesOpen,
  taskLabel,
  activityLabel,
  actions,
  triggerTabIndex,
}: SessionMenuProps) {
  const [busy, setBusy] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);
  const [renameValue, setRenameValue] = useState("");
  const [shutdownOpen, setShutdownOpen] = useState(false);

  // confirm runs a dialog-confirmed action: busy-lock the confirm button
  // against double-submit, close ONLY on success (a rejection was already
  // toasted by the adapter - see the header comment).
  async function confirm(action: () => Promise<void>, close: () => void) {
    setBusy(true);
    try {
      await action();
      close();
    } catch {
      // adapter toasted; leave the dialog open
    } finally {
      setBusy(false);
    }
  }

  const items: MenuEntry[] = [
    { id: "details", label: checked("Details", panesOpen.details), onSelect: () => actions.onOpenPane("details") },
    { id: "tasks", label: checked(taskLabel ?? "Tasks", panesOpen.tasks), onSelect: () => actions.onOpenPane("tasks") },
    {
      id: "activity",
      label: checked(activityLabel ?? "Activity", panesOpen.activity),
      onSelect: () => actions.onOpenPane("activity"),
    },
    { kind: "separator", id: "sep-organize" },
    {
      id: "rename",
      label: "Rename",
      disabled: !canRename,
      onSelect: () => {
        setRenameValue(title);
        setRenameOpen(true);
      },
    },
    { kind: "separator", id: "sep-destructive" },
    {
      id: "shutdown",
      label: "Shut down",
      disabled: !canShutdown,
      onSelect: () => setShutdownOpen(true),
    },
  ];

  return (
    <>
      <Menu
        variant="quiet"
        triggerTabIndex={triggerTabIndex}
        trigger={
          <>
            <span aria-hidden="true">⋯</span>
            <span className={CLASS.srOnly}>{triggerLabel}</span>
          </>
        }
        items={items}
      />

      <Dialog
        open={renameOpen}
        onClose={() => setRenameOpen(false)}
        title="Rename session"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setRenameOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={() => void confirm(() => actions.onRename(renameValue.trim()), () => setRenameOpen(false))}
              disabled={busy || !renameValue.trim()}
            >
              Rename
            </Button>
          </div>
        }
      >
        <div className={CLASS.field}>
          <label className={CLASS.label} htmlFor={`session-menu-rename-${sessionRef}`}>
            Name
          </label>
          <Input
            id={`session-menu-rename-${sessionRef}`}
            value={renameValue}
            onChange={(e: ChangeEvent<HTMLInputElement>) => setRenameValue(e.target.value)}
          />
        </div>
      </Dialog>

      <Dialog
        open={shutdownOpen}
        onClose={() => setShutdownOpen(false)}
        title="Shut down this session?"
        footer={
          <div className={CLASS.footer}>
            <Button variant="quiet" onClick={() => setShutdownOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              onClick={() => void confirm(actions.onShutdown, () => setShutdownOpen(false))}
              disabled={busy}
            >
              Shut down
            </Button>
          </div>
        }
      >
        <p className={CLASS.body}>
          The agent process for this session will stop. You can still read the transcript afterward.
        </p>
      </Dialog>
    </>
  );
}
```

(`useToasts` is imported for Task 4's picker error path; if unused after Task 3, drop it from the import until then so Biome stays clean.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/sessionMenu/SessionMenu.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/shell/sessionMenu
git commit -m "feat(web): SessionMenu core - panes group, rename, shutdown"
```

---

### Task 4: SessionMenu organization group + Delete

**Files:**
- Modify: `shell/sessionMenu/SessionMenu.tsx`
- Test: `shell/sessionMenu/SessionMenu.test.tsx`

**Interfaces:**
- Consumes: `isTopLevelSession` from `shell/rail/sessionKind.ts` (Task 2), `PinSectionPicker` from `shell/rail/PinSectionPicker.tsx` (existing; props `{ session: TreeNode; onAssign: (target: PinTarget, section?: PinSectionSummary) => Promise<void>; onClose: () => void }`), `SessionMenuProps.treeNode` + `actions.onPin/onUnpin/onToggleArchive/onDelete` (Task 3).
- Produces: the complete menu contents both adapters render (identical item set).

Menu contents after this task (group order is a hard requirement):

```
Details / Tasks / Activity            (✓ suffix when open)
── separator ──
Rename                                (disabled: !canRename)
Pin this session… | Unpin             (treeNode && isTopLevelSession; Unpin when pin_section_id set)
Archive | Unarchive                   (treeNode && isTopLevelSession; Unarchive when tier === "archived")
── separator ──
Shut down                             (disabled: !canShutdown)
Delete…                               (treeNode && isTopLevelSession && host_id === "local")
```

Separator rule: a separator renders only between two non-empty groups (the organize separator always renders since Rename is always present; the Pin/Archive items join the organize group when eligible).

- [ ] **Step 1: Write the failing tests**

Add to `SessionMenu.test.tsx`. Tree-node builder:

```tsx
function treeNode(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "row_1",
    ref: "ref_a",
    host_id: "local",
    session_id: "sess_a",
    title: "My session",
    project: "proj",
    state: "idle",
    kind: "session",
    live: false,
    children: [],
    ...overrides,
  };
}
```

Tests:

```tsx
test("full menu: organize group between separators, delete last", async () => {
  const user = userEvent.setup();
  renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
  expect(items).toEqual(["Details", "Tasks ✓", "Activity", "Rename", "Pin this session…", "Archive", "Shut down", "Delete…"]);
  expect(screen.getAllByRole("separator")).toHaveLength(2);
});

test("nested kinds and remote hosts lose organization/delete items", async () => {
  const user = userEvent.setup();
  renderMenu({ treeNode: treeNode({ kind: "subagent" }) });
  await openMenu(user);
  expect(screen.queryByRole("menuitem", { name: /pin/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /archive/i })).toBeNull();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
  cleanup();
  renderMenu({ treeNode: treeNode({ host_id: "remote" }) });
  await openMenu(user);
  expect(screen.getByRole("menuitem", { name: "Pin this session…" })).toBeTruthy();
  expect(screen.queryByRole("menuitem", { name: /delete/i })).toBeNull();
});

test("pinned session offers Unpin; archived offers Unarchive", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode({ pin_section_id: "sec_1", tier: "archived" }) });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unpin" }));
  expect(actions.onUnpin).toHaveBeenCalledTimes(1);
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Unarchive" }));
  expect(actions.onToggleArchive).toHaveBeenCalledTimes(1);
});

test("Pin this session… opens the PinSectionPicker; assigning calls onPin and closes", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode() });
  // PinSectionPicker fetches sections on mount - stub the REST helper the
  // same way shell/rail/PinSectionPicker.test.tsx does (vi.mock ../rail/actions
  // or an MSW/fetch stub matching that test file's exact technique - READ it
  // first and copy its approach).
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Pin this session…" }));
  // pick the first section the picker lists, then:
  await waitFor(() => expect(actions.onPin).toHaveBeenCalledWith({ section_id: expect.any(String) }, expect.anything()));
});

test("Delete… confirms before calling onDelete", async () => {
  const user = userEvent.setup();
  const actions = renderMenu({ treeNode: treeNode() });
  await openMenu(user);
  await user.click(screen.getByRole("menuitem", { name: "Delete…" }));
  const dialog = screen.getByRole("dialog", { name: "Delete session?" });
  expect(within(dialog).getByText(/Permanently delete "My session"\?/)).toBeTruthy();
  await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(actions.onDelete).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/sessionMenu/SessionMenu.test.tsx`
Expected: FAIL — no Pin/Archive/Delete items rendered.

- [ ] **Step 3: Implement**

In `SessionMenu.tsx`:

- Destructure `treeNode` from props. Add imports: `isTopLevelSession` from `../rail/sessionKind`, `PinSectionPicker` from `../rail/PinSectionPicker`.
- Add state: `const [pickerOpen, setPickerOpen] = useState(false);` and `const [deleteOpen, setDeleteOpen] = useState(false);`
- Compute:

```tsx
const organizationEligible = treeNode !== undefined && isTopLevelSession(treeNode);
const deleteEligible = organizationEligible && treeNode.host_id === "local";
```

- Build the items array as groups and join (replace the flat array from Task 3):

```tsx
const paneItems: MenuEntry[] = [ /* Details/Tasks/Activity exactly as Task 3 */ ];
const organizeItems: MenuEntry[] = [
  { id: "rename", label: "Rename", disabled: !canRename, onSelect: () => { setRenameValue(title); setRenameOpen(true); } },
];
if (organizationEligible) {
  organizeItems.push(
    treeNode.pin_section_id
      ? { id: "unpin", label: "Unpin", onSelect: () => void confirm(actions.onUnpin, () => undefined) }
      : { id: "pin", label: "Pin this session…", onSelect: () => setPickerOpen(true) },
    {
      id: "archive",
      label: treeNode.tier === "archived" ? "Unarchive" : "Archive",
      onSelect: () => void confirm(actions.onToggleArchive, () => undefined),
    },
  );
}
const destructiveItems: MenuEntry[] = [
  { id: "shutdown", label: "Shut down", disabled: !canShutdown, onSelect: () => setShutdownOpen(true) },
];
if (deleteEligible) {
  destructiveItems.push({ id: "delete", label: "Delete…", onSelect: () => setDeleteOpen(true) });
}
const items: MenuEntry[] = [
  ...paneItems,
  { kind: "separator", id: "sep-organize" },
  ...organizeItems,
  { kind: "separator", id: "sep-destructive" },
  ...destructiveItems,
];
```

- After the Shut down dialog, add the Delete dialog and the picker:

```tsx
<Dialog
  open={deleteOpen}
  onClose={() => setDeleteOpen(false)}
  title="Delete session?"
  footer={
    <div className={CLASS.footer}>
      <Button variant="quiet" onClick={() => setDeleteOpen(false)}>
        Cancel
      </Button>
      <Button variant="danger" onClick={() => void confirm(actions.onDelete, () => setDeleteOpen(false))} disabled={busy}>
        Delete
      </Button>
    </div>
  }
>
  <p className={CLASS.body}>Permanently delete "{title}"? This removes its transcript and cannot be undone.</p>
</Dialog>

{pickerOpen && treeNode && (
  <PinSectionPicker
    session={treeNode}
    onAssign={async (target, section) => {
      await actions.onPin(target, section);
      setPickerOpen(false);
    }}
    onClose={() => setPickerOpen(false)}
  />
)}
```

(The picker's own onAssign contract already toasts/captures errors internally per PinSectionPicker.tsx — verify against that file and match its existing Rail behavior: Rail's `assignPickerTarget` closes the picker only on success via the token guard. SessionMenu's simpler version: close on success, stay open on rejection, exactly like `confirm`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/sessionMenu`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/shell/sessionMenu
git commit -m "feat(web): SessionMenu organization group and delete"
```

---

### Task 5: SessionChrome integration (+ GoalControl dialog removal)

**Files:**
- Modify: `panes/session/chrome/SessionChrome.tsx`
- Modify: `panes/session/chrome/GoalControl.tsx` (remove the set-goal Dialog and its props/state)
- Delete: `panes/session/chrome/SessionActionsMenu.tsx`
- Delete: `panes/session/chrome/sessionactionsmenu.module.css`
- Delete: `panes/session/chrome/SessionActionsMenu.test.tsx`
- Test: `panes/session/chrome/SessionChrome.test.tsx` (update), `panes/session/chrome/GoalControl.test.tsx` (remove dialog tests)

**Interfaces:**
- Consumes: `SessionMenu` (Tasks 3-4), `findSessionNode` + `useTreeStore` from `stores/tree.ts`, `closePanesForDeletedSessions` from `shell/deletedSessionPanes.ts`, rail REST helpers `assignSessionPin`/`unpinSession`/`setArchived`/`deleteSession` from `shell/rail/actions.ts`, `isPaneOpen`/`workspaceStore`/`useWorkspaceStore` from `shell/workspace.ts` (all Task 2 or existing).
- Produces: the session pane renders the shared menu; no inline Details/Tasks/Activity buttons anywhere.

- [ ] **Step 1: Write/adjust the failing tests**

In `SessionChrome.test.tsx` (keep the file's existing render setup with FakeClient; register the three session panel pane types the way the test file already registers "session" if it does):

```tsx
test("status row has no inline Details/Tasks/Activity buttons; they live in the menu", async () => {
  const user = userEvent.setup();
  renderChrome(); // the file's existing helper
  expect(screen.queryByRole("button", { name: "Details" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Tasks/ })).toBeNull();
  expect(screen.queryByRole("button", { name: /Activity/ })).toBeNull();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  expect(screen.getByRole("menuitem", { name: "Details" })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Tasks/ })).toBeTruthy();
  expect(screen.getByRole("menuitem", { name: /Activity/ })).toBeTruthy();
});

test("menu Tasks item toggles the sessionTasks workspace pane on desktop", async () => {
  const user = userEvent.setup();
  renderChrome();
  await user.click(screen.getByRole("button", { name: /session actions/i }));
  await user.click(screen.getByRole("menuitem", { name: /Tasks/ }));
  expect(isPaneOpen(workspaceStore.getState(), "sessionTasks", { ref: "ref_a" })).toBe(true);
});

test("menu offers Pin/Archive/Delete when the session is in the tree; omits them otherwise", async () => {
  // Seed the tree store with a top-level local node for ref_a (treeStore.setState
  // with a minimal TreeResponse - see stores/tree.ts normalizeTree for shape),
  // open the menu, expect Pin this session…/Archive/Delete…;
  // then reset the tree to null and expect them gone.
});

test("menu Shut down is gated on capabilities.shutdown", async () => {
  // render with capabilities.shutdown false; item has aria-disabled="true"
});
```

Update every existing SessionChrome test that clicks the inline `Details`/`Tasks`/`Activity` buttons to go through the menu instead. Remove tests covering the 640px narrow-collapse (`useNarrowerThan`, `extraItems`) — that machinery is deleted.

In `GoalControl.test.tsx`: delete every test that opens or submits the set-goal Dialog (`dialogOpen`, "Set goal" title, objective Textarea, Save). Keep the chip + clear-popover tests unchanged.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/chrome/SessionChrome.test.tsx src/panes/session/chrome/GoalControl.test.tsx`
Expected: FAIL — inline buttons still exist / `SessionActionsMenu` still rendered.

- [ ] **Step 3: Implement**

`GoalControl.tsx`: remove the `dialogOpen`/`onDialogOpenChange` props, the `objective`/`busy` state, `handleSave`, the `useEffect` resetting the objective, and the entire `<Dialog title="Set goal">` block. Remove now-unused imports (`Dialog`, `Textarea`, `ChangeEvent`, `useEffect` if unused). Update the header comment: the chip + clear popover remain; setting a goal happens through the command palette's `/goal` builtin. Keep `sessionRef`/`model` props and everything chip/popover-related (`handleClear`, override cache) exactly as-is.

`SessionChrome.tsx`:
- Delete: `useNarrowerThan` (whole hook), `NARROW_CHROME_WIDTH_PX`, `chromeRef`/`narrow`/`collapsed`, `overflowItems`, the `!isMobile && !collapsed && (...)` inline-buttons block, `goalDialogOpen`/`setGoalDialogOpen` state, the `SessionActionsMenu` import, and the `Button`/`MenuItem` imports if now unused. Remove `dialogOpen`/`onDialogOpenChange` from the `GoalControl` element.
- `openDetails`/`openTasks`/`openActivity` stay byte-identical (mobile Sheet via refs, desktop `togglePane`).
- Panels stay mounted triggerless: `<DetailsPanel ref={detailsRef} model={model} now={now} hideTrigger />`, `<TasksPanel ... hideTrigger />`, `<ActivityPanel ... hideTrigger refreshWhenHidden />`. (`refreshWhenHidden` is now unconditional: the menu's `Activity · N` label reads the same summary the hidden trigger used to refresh.)
- Add the tree lookup and adapters:

```tsx
const tree = useTreeStore((s) => s.tree);
const treeNode = tree ? findSessionNode(tree, sessionRef) : undefined;
```

- Replace `<SessionActionsMenu ... />` with:

```tsx
<SessionMenu
  sessionRef={sessionRef}
  title={model.name}
  triggerLabel="Session actions"
  canRename={model.capabilities.rename}
  canShutdown={model.capabilities.shutdown}
  treeNode={treeNode}
  panesOpen={{ details: detailsOpen, tasks: tasksOpen, activity: activityOpen }}
  taskLabel={model.tasks ? `Tasks ${model.tasks.done}/${model.tasks.total}` : undefined}
  activityLabel={activityLabel}
  actions={{
    onOpenPane: (pane) => {
      if (pane === "details") openDetails();
      else if (pane === "tasks") openTasks();
      else openActivity();
    },
    onRename: async (name) => {
      try {
        await threadsStore.getState().rename(sessionRef, name);
      } catch (err) {
        toasts.push("error", sessionActionError("Couldn't rename session", err));
        throw err;
      }
    },
    onShutdown: async () => {
      try {
        await threadsStore.getState().shutdown(sessionRef);
      } catch (err) {
        toasts.push("error", sessionActionError("Couldn't shut down session", err));
        throw err;
      }
    },
    onPin: async (target) => {
      try {
        await assignSessionPin(sessionRef, target);
        await treeStore.getState().refresh();
      } catch (err) {
        toasts.push("error", sessionActionError("Couldn't assign pinned session", err));
        throw err;
      }
    },
    onUnpin: async () => {
      try {
        await unpinSession(sessionRef);
        await treeStore.getState().refresh();
      } catch (err) {
        toasts.push("error", sessionActionError("Couldn't unpin session", err));
        throw err;
      }
    },
    onToggleArchive: async () => {
      if (!treeNode) return;
      try {
        await setArchived("session", treeNode.session_id, treeNode.tier !== "archived");
        await treeStore.getState().refresh();
      } catch (err) {
        toasts.push("error", sessionActionError("Couldn't update archive state", err));
        throw err;
      }
    },
    onDelete: async () => {
      try {
        const result = await deleteSession(sessionRef);
        await treeStore.getState().refresh();
        closePanesForDeletedSessions(result.deleted);
        if (result.skipped.length > 0) {
          const reason = result.skipped[0]?.reason ?? "still in use";
          toasts.push("warning", `Couldn't delete "${model.name}": ${reason}`);
        }
      } catch (err) {
        toasts.push("error", sessionActionError(`Couldn't delete "${model.name}"`, err));
        throw err;
      }
    },
  }}
/>
```

- The component needs `const toasts = useToasts();` and imports: `SessionMenu` from `../../../shell/sessionMenu/SessionMenu`, `findSessionNode` + `treeStore` + `useTreeStore` from `../../../stores/tree`, `threadsStore` from `../../../stores/threads`, `sessionActionError` from `../../../protocol/errors`, `assignSessionPin`/`unpinSession`/`setArchived`/`deleteSession` from `../../../shell/rail/actions`, `closePanesForDeletedSessions` from `../../../shell/deletedSessionPanes`. Verify `useTreeStore` is exported from `stores/tree.ts` (Rail.tsx imports it — copy its exact import).
- Delete the three `SessionActionsMenu` files listed above.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session`
Expected: PASS — updated chrome/goal tests, all panel tests untouched.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session
git commit -m "feat(web): session chrome renders the shared SessionMenu; drop inline pane buttons and goal dialog"
```

---

### Task 6: Rail integration

**Files:**
- Modify: `shell/rail/RailRow.tsx` (SessionRow renders `SessionMenu`; delete `sessionMenuItems`; extend `RailRowActions`)
- Modify: `shell/rail/Rail.tsx` (delete session rename/pin/delete dialog state + markup + handlers; implement the new action callbacks)
- Test: `shell/rail/RailRow.test.tsx`, `shell/rail/Rail.test.tsx` (update)

**Interfaces:**
- Consumes: `SessionMenu` (Tasks 3-4), `isTopLevelSession` from `./sessionKind` (Task 2), `sessionPanelPaneType` from `panes/sessionPanels/index.ts` (Task 2), `closePanesForDeletedSessions` from `../deletedSessionPanes` (Task 2), `isPaneOpen`/`useWorkspaceStore`/`workspaceStore` from `../workspace`.
- Produces: the new `RailRowActions` session surface (replaces `onPinSectionRequest`/`onRenameRequest`/`onDeleteSessionRequest`):

```ts
export interface RailRowActions {
  onOpenSessionPane(session: ApiTreeNode, pane: SessionPanelKind): void;
  onRenameSession(session: ApiTreeNode, name: string): Promise<void>;
  onShutdownSession(session: ApiTreeNode): Promise<void>;
  onPinSession(session: ApiTreeNode, target: PinTarget, section?: PinSectionSummary): Promise<void>;
  onUnpinRequest(session: ApiTreeNode): void;          // unchanged
  onToggleArchiveSession(session: ApiTreeNode): void;  // unchanged
  onDeleteSession(session: ApiTreeNode): Promise<void>;
  onToggleFavoriteProject(project: ApiTreeProject): void;   // unchanged
  onToggleArchiveProject(project: ApiTreeProject): void;    // unchanged
  onDeleteProjectRequest(project: ApiTreeProject): void;    // unchanged
}
```

- [ ] **Step 1: Write/adjust the failing tests**

`RailRow.test.tsx`: replace the old menu tests with equivalents against the unified menu. The `acts` mock object gains the new async callbacks (`vi.fn().mockResolvedValue(undefined)`). Key tests:

```tsx
test("session row menu is the unified menu: panes group first, shut down present", async () => {
  const user = userEvent.setup();
  renderRow(); // the file's existing helper for a top-level session row
  await user.click(screen.getByRole("button", { name: /actions for/i }));
  const items = screen.getAllByRole("menuitem").map((el) => el.textContent);
  expect(items).toEqual(["Details", "Tasks", "Activity", "Rename", "Pin this session…", "Archive", "Shut down", "Delete…"]);
});

test("Details opens the session pane, then the sessionDetails pane", async () => {
  const user = userEvent.setup();
  renderRow();
  await user.click(screen.getByRole("button", { name: /actions for/i }));
  await user.click(screen.getByRole("menuitem", { name: "Details" }));
  const panes = workspaceStore.getState().panes.map((p) => p.type);
  expect(panes).toContain("session");
  expect(panes).toContain("sessionDetails");
});

test("rename saves through onRenameSession; shut down confirms through onShutdownSession", async () => {
  // open menu → Rename → dialog → save → expect onRenameSession(session, "new name")
  // open menu → Shut down → confirm → expect onShutdownSession(session)
});
```

(Register a test "session" pane + the three sessionPanel types via `registerPane` at the top of the file, mirroring `SessionActionsMenu.test.tsx`'s existing test-only registration; reset the workspace store in `beforeEach` with `resetWorkspaceStoreForTests`.)

`Rail.test.tsx`: the pin-picker flow tests (`Pin this session…` → pick section) keep working unchanged *if* the picker's accessible names are unchanged — they are, the same `PinSectionPicker` renders, now from inside `SessionMenu`. Update only tests that referenced the removed Rail-level handlers/state by name. The delete-session flow test clicks `Delete…` then the confirm button labeled `Delete` in dialog `Delete session?` — same names, should pass unchanged. Run the file and fix any mock-shape fallout (the `actions` prop object shape changed).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/rail`
Expected: FAIL — `RailRowActions` type mismatch, old items missing.

- [ ] **Step 3: Implement**

`RailRow.tsx`:
- Delete `sessionMenuItems` and its doc comments (Pin/Rename/Archive/Delete logic now lives in `SessionMenu`).
- Update `RailRowActions` to the interface above. Imports add: `type SessionPanelKind` from `../../panes/sessionPanels`, `type PinTarget` + `SessionMenu` from `../sessionMenu/SessionMenu`, `isPaneOpen` + `useWorkspaceStore` from `../workspace`, `type PinSectionSummary` from `../../stores/tree`.
- In `SessionRow`, replace `<ActionsMenu label={session.title} items={sessionMenuItems(session, actions)} />` with:

```tsx
<SessionMenuRow session={session} actions={actions} />
```

and add beside `SessionRow`:

```tsx
// The rail-row use of the shared session menu: same component the session
// pane's chrome renders, fed from the ApiTreeNode instead of a ThreadModel.
// panesOpen drives the ✓ markers via the workspace store; triggerTabIndex
// -1 keeps the Tree widget's single-roving-Tab-stop contract (see the old
// ActionsMenu's comment, which this replaces).
function SessionMenuRow({ session, actions }: { session: ApiTreeNode; actions: RailRowActions }) {
  const ref = session.ref;
  const panesOpen = useWorkspaceStore((s) => ({
    details: isPaneOpen(s, "sessionDetails", { ref }),
    tasks: isPaneOpen(s, "sessionTasks", { ref }),
    activity: isPaneOpen(s, "sessionActivity", { ref }),
  }));
  return (
    <SessionMenu
      sessionRef={ref}
      title={session.title}
      triggerLabel={`Actions for ${session.title}`}
      canRename={session.rename === true}
      canShutdown={session.live}
      treeNode={session}
      panesOpen={panesOpen}
      actions={{
        onOpenPane: (pane) => actions.onOpenSessionPane(session, pane),
        onRename: (name) => actions.onRenameSession(session, name),
        onShutdown: () => actions.onShutdownSession(session),
        onPin: (target, section) => actions.onPinSession(session, target, section),
        onUnpin: () => actions.onUnpinRequest(session),
        onToggleArchive: () => actions.onToggleArchiveSession(session),
        onDelete: () => actions.onDeleteSession(session),
      }}
      triggerTabIndex={-1}
    />
  );
}
```

Note: `useWorkspaceStore` with an object literal selector re-renders on every store change unless the store supports an equality fn — check how `SessionChrome` selects (it uses three separate boolean selectors). If `useWorkspaceStore` has no shallow option, use three separate selectors exactly like SessionChrome does. `onUnpinRequest`/`onToggleArchiveSession` return `void` in the current interface — SessionMenu's `confirm` awaits them; change their `RailRowActions` types to return `Promise<void>` and have Rail return the `runAction(...)` promise (runAction already returns `Promise<void>`), so a failed unpin/archive toasts AND rejects per the failure convention.

`Rail.tsx`:
- Delete state: `renameTarget`, `renameValue`, `pickerTarget`, `pickerToken` (only if unused elsewhere — the pin-SECTION rename uses `sectionRenameToken`, keep that), `deleteSessionTarget`.
- Delete handlers: `closeRenameDialog`, `confirmRename`, `assignPickerTarget`, `closeDeleteSessionDialog`, `confirmDeleteSession`. Delete the JSX blocks for: the session-rename `Dialog` (`renameTarget && ...`), the `PinSectionPicker` (`pickerTarget && ...`), and the session-delete `Dialog` (`deleteSessionTarget && ...`). Keep section rename/delete and project delete dialogs untouched. Also remove `pickerToken.current += 1` from the unmount cleanup effect if `pickerToken` is gone.
- Replace the three old `rowActions` entries and add the new ones:

```ts
onOpenSessionPane: (session, pane) => {
  const workspace = workspaceStore.getState();
  workspace.openPane("session", { ref: session.ref });
  workspace.openPane(sessionPanelPaneType(pane), { ref: session.ref });
},
onRenameSession: async (session, name) => {
  await runAction(() => renameSession(session.ref, name), "Couldn't rename session", { kind: "sessionTitle", ref: session.ref, title: name }, true);
},
onShutdownSession: async (session) => {
  await runAction(() => threadsStore.getState().shutdown(session.ref), "Couldn't shut down session", undefined, true);
},
onPinSession: async (session, target, section) => {
  const optimistic = section
    ? ({ kind: "sessionPin", ref: session.ref, source: session, section } as const)
    : (result: Awaited<ReturnType<typeof assignSessionPin>>): PendingOp => ({
        kind: "sessionPin",
        ref: session.ref,
        source: session,
        section: result.assignment.section,
      });
  await runAction(() => assignSessionPin(session.ref, target), "Couldn't assign pinned session", optimistic, true);
},
onUnpinRequest: (session) =>
  runAction(() => unpinSession(session.ref), "Couldn't unpin session", { kind: "sessionUnpin", ref: session.ref }, true),
onToggleArchiveSession: (session) => {
  const archiving = session.tier !== "archived";
  return runAction(
    () => setArchived("session", session.session_id, archiving),
    "Couldn't update archive state",
    archiving ? { kind: "hideSession", ref: session.ref } : undefined,
    true,
  );
},
onDeleteSession: async (session) => {
  const optimistic: PendingOp = { kind: "hideSession", ref: session.ref };
  setPending((ops) => [...ops, optimistic]);
  try {
    const result = await deleteSession(session.ref);
    await treeStore.getState().refresh();
    closePanesForDeletedSessions(result.deleted);
    if (result.skipped.length > 0) {
      const reason = result.skipped[0]?.reason ?? "still in use";
      toasts.push("warning", `Couldn't delete "${session.title}": ${reason}`);
    }
  } catch (err) {
    toasts.push("error", `Couldn't delete "${session.title}": ${errorText(err)}`);
    throw err;
  } finally {
    setPending((ops) => ops.filter((op) => op !== optimistic));
  }
},
```

- Imports to add: `threadsStore` from `../../stores/threads`, `sessionPanelPaneType` + the side-effect registration `../../panes/sessionPanels` (importing the helper from that module runs `registerPane` for the three panel types — required, since the rail can now be the FIRST place a sessionDetails pane is opened; add a comment saying so), `workspaceStore` from `../workspace` (if not already imported). Imports to remove if now unused: `PinSectionPicker`.
- Rail keeps `closePanesForDeletedSessions` usage for project delete (imported from `../deletedSessionPanes` since Task 2).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/shell/rail`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/shell
git commit -m "feat(web): rail session rows render the shared SessionMenu"
```

---

### Task 7: Gates and final verification

**Files:** any file Biome rewrites.

- [ ] **Step 1: Biome**

Run: `cd cmd/serf-hub/frontend && npx biome check --write src/widgets/menu src/shell/sessionMenu src/shell/rail src/shell/deletedSessionPanes.ts src/panes/session src/stores/tree.ts src/widgets/index.ts`
Expected: no errors; stage any formatting fixes.

- [ ] **Step 2: Full frontend gate**

Run: `make test-web` (from the worktree repo root)
Expected: PASS (unit + typecheck + Biome).

- [ ] **Step 3: Browser gate**

Run: `make test-web-browser`
Expected: PASS. Pay attention to any geometry/popup tests covering the menu — separators add two `<li>`s to every session menu.

- [ ] **Step 4: Manual smoke checklist** (report results, do not skip)

In a running hub (`make build-hub` + run, or the dev server the repo documents):
1. Session pane ⋯ menu shows Details/Tasks/Activity — Rename/Pin/Archive — Shut down/Delete with separators; no Goal/Aside/Compact/Clear.
2. Sidebar row ⋯ menu shows the identical list.
3. Sidebar → Tasks opens the session pane and the Tasks panel.
4. Narrow the session pane below 640px: the footer stays one line, menu still works (no collapse machinery remains).

- [ ] **Step 5: Final commit**

```bash
git add -A cmd/serf-hub/frontend/src
git commit -m "chore(web): biome formatting for unified session menu" --allow-empty
```
