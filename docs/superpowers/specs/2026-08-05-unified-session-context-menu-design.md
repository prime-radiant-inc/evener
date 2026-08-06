# Unified session context menu — design

Date: 2026-08-05
Status: approved design, pre-plan

## Problem

A session has two different "⋯" menus today:

- **Session pane** (`panes/session/chrome/SessionActionsMenu.tsx`): Set goal…,
  Aside, Compact, Clear, Shut down, Rename — plus Details/Tasks/Activity
  prepended only when the pane is narrower than 640px.
- **Sidebar rail** (`shell/rail/RailRow.tsx` `sessionMenuItems`): Pin/Unpin,
  Rename, Archive/Unarchive, Delete…

They should be the same menu. Set goal…, Aside, Compact, and Clear are slash
commands and do not belong in the menu. Details, Tasks, and Activity belong in
the menu permanently, not only as a narrow-pane overflow.

## Decisions (from the user)

1. The menu is **literally identical** in both contexts. The session pane gains
   Pin/Archive/Delete; the sidebar gains Shut down.
2. The inline Details/Tasks/Activity buttons **leave the session status row**
   entirely; those three always live in the menu.
3. Order: **panes → organize → destructive**, with separators (the Menu widget
   gains separator support).
4. Architecture: **one shared `SessionMenu` component** with normalized props;
   each render site maps its own data source into them (adapter pattern).
5. The set-goal `Dialog` in `GoalControl` becomes unreachable once "Set goal…"
   leaves the menu, so the dead dialog wiring is **removed**. Setting a goal
   remains available through the command palette's `/goal` builtin
   (`shell/palette/commands.ts` "Set session goal" → `threadsStore.setGoal` →
   `goal/set` RPC). Typing `/goal foo` as a plain composer message sends
   literal text to the model — true today, unchanged by this work (the daemon
   expands only plugin slash commands; `agent/session_slash_command.go`).

## Menu contents

Identical in both contexts, three groups separated by rules:

| Item | Label notes | Gating |
| --- | --- | --- |
| Details | ` ✓` suffix when its pane is open | always |
| Tasks | ` ✓` when open; session context keeps live count label (`Tasks 3/7`) | always |
| Activity | ` ✓` when open; session context keeps badge label (`Activity · 2`) | always |
| — separator — | | |
| Rename | | session ctx: `model.capabilities.rename`; sidebar: `session.rename` wire flag |
| Pin this session… / Unpin | Unpin when `pin_section_id` set | top-level sessions only (`isTopLevelSession`) |
| Archive / Unarchive | Unarchive when `tier === "archived"` | top-level sessions only |
| — separator — | | |
| Shut down | confirm dialog | session ctx: `model.capabilities.shutdown`; sidebar: `session.live` |
| Delete… | confirm dialog | top-level **and** `host_id === "local"` |

Removed: Set goal…, Aside, Compact, Clear.

## Architecture

### New shared component: `src/shell/sessionMenu/SessionMenu.tsx`

An app-level component (not a widget). It owns:

- the item list and grouping,
- the Rename dialog,
- the Shut down confirm dialog,
- the Delete confirm dialog,
- the PinSectionPicker dialog (reused from `shell/rail/PinSectionPicker.tsx`).

Props are a normalized view of one session plus a callback object:

```ts
interface SessionMenuProps {
  sessionRef: string;
  title: string;
  triggerLabel: string; // sr-only trigger name per context
  // Capability/flags, mapped by the caller from ThreadModel or ApiTreeNode.
  canRename: boolean;
  canShutdown: boolean;
  // The session's tree node. Present ⟺ Pin/Archive/Delete are offered
  // (eligibility still checks isTopLevelSession / host_id inside the menu);
  // absent (session not in the tree) omits them. Also feeds PinSectionPicker.
  treeNode?: ApiTreeNode;
  panesOpen: { details: boolean; tasks: boolean; activity: boolean };
  taskLabel?: string;     // e.g. "Tasks 3/7", session ctx
  activityLabel?: string; // e.g. "Activity · 2", session ctx
  actions: SessionMenuActions;
  triggerTabIndex?: number; // -1 in the rail row (roving tabindex contract)
}

interface SessionMenuActions {
  onOpenPane(pane: SessionPanelKind): void;
  onRename(name: string): Promise<void>;
  onShutdown(): Promise<void>;
  onPin(target: PinTarget, section?: PinSectionSummary): Promise<void>;
  onUnpin(): Promise<void>;
  onToggleArchive(): Promise<void>;
  onDelete(): Promise<void>;
}
```

Mutations are performed by the **adapters** behind these callbacks, not by
the component: the session-pane adapter calls `threadsStore.rename` /
`threadsStore.shutdown` and the rail REST helpers (`assignSessionPin`,
`unpinSession`, `setArchived`, `deleteSession`) with a `treeStore.refresh()`;
the rail adapter routes the same calls through its `runAction` so the
optimistic-pending overlay keeps working. Failure convention: the adapter
toasts (`sessionActionError` / Rail's messages) and rethrows; SessionMenu
keeps the dialog open on rejection and closes only on success. This keeps
one owner per dialog — the shared component — with no duplicated dialog
stacks, and Rail keeps its pending-overlay behavior without knowing anything
about the dialogs.

### Session-pane adapter (`SessionChrome.tsx`)

- Maps `ThreadModel.capabilities` → `canRename`/`canShutdown`.
- Looks the session up in the tree store by ref (`findSessionNode`) and
  passes it as `treeNode` (top-level/local/tier/pin state are read off it).
  If the session is absent from the tree, `treeNode` is undefined and
  Pin/Archive/Delete are omitted.
- `onOpenPane` preserves today's behavior exactly: desktop toggles the
  workspace pane (`togglePane("sessionDetails" | "sessionTasks" |
  "sessionActivity", { ref })`); mobile opens the Sheet via the existing
  panel refs.
- Delete success additionally closes the session pane.
- Labels pass the live `Tasks n/m` and `Activity · N` strings.

### Sidebar adapter (`RailRow.tsx` / `Rail.tsx`)

- Maps `ApiTreeNode`: `rename` flag → `canRename`, `live` → `canShutdown`,
  `isTopLevelSession`/`host_id`/`tier`/`pin_section_id` → `organization`.
- `onOpenPane` **opens** (never toggles) the session pane and then the
  sub-pane: `openPane("session", { ref })` then
  `openPane("sessionDetails" | "sessionTasks" | "sessionActivity", { ref })`.
  This path is the same on mobile and desktop — the workspace owns pane
  presentation; the rail cannot reach the mobile Sheet refs inside
  SessionChrome.
- Pin/Archive/Delete/Rename keep their exact current rail *effects*
  (optimistic pending overlay, awaited refresh, skipped/live reconciliation)
  by passing Rail's `runAction` as `runMutation` (see above). The dialogs
  themselves move into the shared component; Rail deletes its own rename /
  delete-session / pin-picker dialog state and markup.

### Menu widget: separators

`widgets/menu` gains a separator item kind (e.g. `MenuItem` becomes a union:
action item | `{ kind: "separator", id }`). Separators render as a rule, are
skipped by keyboard navigation, and never receive focus. Additive change; all
existing consumers are unaffected.

### SessionChrome simplification

Removed:

- the inline Details/Tasks/Activity `Button`s,
- `useNarrowerThan` / `NARROW_CHROME_WIDTH_PX` and the whole narrow-collapse
  path,
- `SessionActionsMenu.extraItems` (and `SessionActionsMenu` itself, replaced
  by the shared `SessionMenu`),
- the set-goal dialog wiring: `SessionChrome`'s `goalDialogOpen` state,
  `GoalControl`'s `dialogOpen`/`onDialogOpenChange` props and its Dialog. The
  goal chip and its clear popover stay.

Kept: `DetailsPanel`/`TasksPanel`/`ActivityPanel` mounted triggerless
(`hideTrigger`) for the mobile Sheet path; the `✓` open-state markers come
from the existing `isPaneOpen` selectors.

## Data flow

No new server endpoints. All actions use existing RPCs/REST:

- `thread/rename`, `thread/shutdown` (appwire, via threadsStore)
- pin/unpin/archive/delete (`shell/rail/actions.ts` REST helpers)
- pane opening (workspaceStore, local)

## Error handling

- Every mutation toast-on-failure with `sessionActionError("Couldn't …", err)`.
- Confirm dialogs disable their confirm button while a request is in flight
  (existing `runGuarded` pattern).
- Sidebar Shut down is gated on `live`; a race (session ends after the menu
  renders) is refused server-side and surfaces as a toast, same as the rail's
  existing delete race handling.

## Testing

- `SessionMenu.test.tsx`: item set/order/separators; gating matrix
  (rename/shutdown/pin/archive/delete on/off); labels with counts and `✓`
  markers; rename/shutdown/delete dialog flows (confirm, cancel, failure
  toast); `onOpenPane` dispatch.
- Adapter tests: `SessionChrome.test.tsx` (menu replaces buttons; narrow
  collapse gone; mobile Sheet path still opens from menu items) and
  `RailRow.test.tsx`/`Rail.test.tsx` (menu contents match the old rail menu +
  Shut down; open-pane-from-rail navigates and opens the sub-pane;
  pin/archive/delete still drive Rail's pending overlay).
- `widgets/menu` separator tests (render, keyboard skip).
- GoalControl/SessionChrome tests updated for the removed dialog.
- Gates: `npx biome check --write` on touched files, `make test-web`, and
  `make test-web-browser` (Chrome-capable host).

## Out of scope

- Changing what the slash commands do, or the composer's handling of typed
  `/goal` text.
- Project-row menus, the `+` new-session button, and non-session rail rows.
- Any visual redesign of the menu beyond separators.

## Worktree

Implementation happens in a new git worktree branched from `main`.
