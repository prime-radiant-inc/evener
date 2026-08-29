# Mobile welcome + sidebar unification

**Status:** design
**Date:** 2026-08-29
**Owner:** evener

## Problem

On mobile, two separate surfaces cover the "nothing is open" and "browse my
sessions" needs: a full-screen `Welcome` pane, and the `TreeDrawer` bottom
`Sheet` that hosts the `<Rail/>` session tree. They share content (the "New
session" button, the resume link, the session list) but live in different
components reached through different entry points. The welcome pane also shows
example task prompts ("Try a task to get started") that should be removed on both
mobile and desktop.

## Goals

1. **Unify** the mobile welcome screen and the bottom-panel sidebar into a
   single expandable surface.
2. The unified panel can **slide up to full screen** and collapse back down,
   via a drag handle plus tap.
3. The panel opens **full screen first** when no session is focused.
4. The key bindings (Mod+K / Mod+I / Mod+J, and "?" inside the palette) move to
   the **bottom of the unified panel** on mobile.
5. Remove the example task prompts from the welcome screen on **both** mobile
   and desktop.
6. Desktop keeps the chord hints on the `Welcome` pane (no bottom panel there).

## Non-goals

- Changing desktop's persistent left rail or dockview layout.
- Changing the ~8 other `Sheet` consumers (TasksPanel, DetailsPanel,
  PinSectionPicker, MobileSettingRows, ActivityPanel,
  TranscriptDetailControl, the dev gallery).
- Persisting the panel's peek/full state across reloads (it is transient, like
  the drawer's open/closed state today).
- A drag gesture on desktop (no bottom panel on desktop).

## Architecture

### The unification model

On mobile, the "welcome screen" and the "sidebar" become **one expandable
bottom panel**. The panel is the existing `Sheet` widget (`side="bottom"`)
extended with an opt-in **expandable** capability. The panel's content has
three regions, top to bottom:

1. **Session tree** — the existing `<Rail/>` content (session list, "New
   session" button, pin sections, archived). Always present.
2. **Welcome actions** — "Jump back in" (resume candidate) + orientation text.
   Present **only when no session is focused** (the panel is then the landing
   screen). Hidden when a session is live. Note: the `<Rail/>` already renders
   its own "+ New session" button (Rail.tsx ~line 1086), so the panel does NOT
   duplicate it here — the welcome actions render "Jump back in" and the
   orientation text only; "New session" lives in the Rail above.
3. **Key bindings** — the chord hints. Present only when no session is focused,
   at the panel's bottom.

The panel's **content** depends on focus state; the panel's **geometry**
(peek vs. full screen) is orthogonal and is a reusable `Sheet` capability. So
"welcome" and "sidebar" are the same React surface on mobile, distinguished
only by what is focused — not by which component is mounted.

### Platform split

| State | Mobile | Desktop |
|---|---|---|
| No session focused | Unified panel, full-screen-first. Shows tree + welcome actions + key bindings. | `Welcome` pane (example prompts removed, chord hints kept). Rail docked. |
| Session focused | Session pane full screen. Top-bar Sessions button reopens the panel (full). Tree only. | Session pane + rail. |

### Global constraint honored

Panes never ask "am I mobile?" The `Welcome` component stays host-agnostic. The
new mobile panel is a **shell-level** composition in `src/shell/mobile/`, not a
behavior switch inside `Welcome.tsx`. The welcome *content* (actions, hints) is
extracted into a small reusable module both the desktop `Welcome` pane and the
mobile panel render.

## Components

### 1. `Sheet` expandable capability (`src/widgets/sheet/`)

Add an opt-in expandable mode to the shared `Sheet` widget. New props:

```ts
interface SheetProps {
  // ...existing props...
  /** When set, the sheet renders a drag handle and can transition between
   *  `peekHeight` and full screen. Default: undefined (no handle, fixed
   *  geometry — the current behavior every other consumer relies on). */
  expandable?: {
    /** Peek height in px. The collapsed resting height. */
    peekHeight: number;
    /** Start at full height when the sheet opens. Unlike a mount-only prop,
     *  this re-applies on every open transition (false→true) via an effect
     *  inside the Sheet, because the Sheet's host (e.g. MobilePanel) stays
     *  mounted across open/close cycles — `OverlayPanel` returns null when
     *  closed but the geometry state in the wrapper persists, so the geometry
     *  must be explicitly reset on each open, not only on first mount. */
    fullScreenFirst?: boolean;
  };
}
```

**Behavior:**
- A drag handle renders at the top of the panel, **above the title**. This
  requires a new optional `handle` slot on `OverlayPanel` (rendered before the
  `<header>`); `Sheet` passes the handle element through it when `expandable`
  is set. `OverlayPanel`'s `handle` prop defaults to `undefined` — every
  existing Dialog and Sheet consumer is unchanged. (The handle cannot live in
  `children` because `OverlayPanel` renders `<header>` before the body
  `<div>{children}</div>`, so a child would appear below the title in DOM
  order.)
- **Geometry reset on open:** the Sheet owns a `geometry` state
  (`"peek" | "full"`) and an effect: `useEffect(() => { if (open &&
  expandable?.fullScreenFirst) setGeometry("full"); }, [open]);`. This fires on
  every `open` false→true transition, resetting to full even when the host
  (MobilePanel) stays mounted and the geometry state persisted from a prior
  peek. Without this, reopening after collapsing to peek would show peek,
  violating "full screen first."
- **Drag:** pointer/touch drag on the handle transitions the panel height
  continuously between `peekHeight` and `100vh`. Release snaps to the nearer
  bound (with a small hysteresis band; >50% of the way to the other bound
  commits). Respects `prefers-reduced-motion` (snap, no smooth transition).
- **Tap:** a tap on the handle toggles between peek and full.
- The scrim and Escape-to-close behavior are unchanged (Escape still closes the
  sheet entirely; scrim click still closes).
- The panel's `max-height` is no longer the fixed `min(480px, 100vh)` when
  expandable; it animates between the two bounds. The existing `.bottom` rule
  stays the default for non-expandable sheets.

**Why additive:** `expandable` defaults to `undefined` and `OverlayPanel`'s
`handle` defaults to `undefined`; every existing `<Sheet>` and `<Dialog>` call
site is byte-for-byte identical in behavior and geometry. No existing test or
consumer changes. The geometry-reset effect only runs when `expandable` is set.

**Scroll and sticky — scoped CSS mechanism (does NOT touch shared
`dialog.module.css`):** the panel body and header live in `dialog.module.css`
(shared by all Dialogs and Sheets). To make the handle and title sticky while
the body scrolls — *only* in the expandable variant — `OverlayPanel` gains two
new optional props: `bodyClassName` and `headerClassName`. When `expandable` is
set, `Sheet` passes:
- `headerClassName`: an `expandableHeader` class from `sheet.module.css`
  (`position: sticky; top: 0; z-index: 1; background: var(--surface-1);`).
- `bodyClassName`: an `expandableBody` class from `sheet.module.css`
  (`flex: 1 1 auto; min-height: 0; overflow-y: auto;`).

`OverlayPanel` applies these as *additional* classes alongside its own
`.header`/`.body` — the shared classes stay the default, so non-expandable
Sheets and all Dialogs are unaffected. The Rail's own `height: 100%` then
resolves against the body's bounded height, enabling its internal scroll. This
keeps the "byte-for-byte identical" promise for non-expandable consumers while
giving the expandable variant the sticky-header + scroll-body layout it needs.

### 2. Welcome content module (`src/panes/welcome/WelcomeContent.tsx`)

Extract the focus-dependent welcome content (the `actions` block) into a
presentational component so both the desktop `Welcome` pane and the mobile
panel render the same markup:

```tsx
interface WelcomeContentProps {
  note?: string;
  /** Render the "New session" button. Desktop's standalone Welcome pane
   *  shows it (no rail beside it on a cold load). The mobile panel hides it
   *  — the Rail inside the same panel already renders its own "+ New
   *  session" button, so a second one would stack two primaries. */
  showNewSession?: boolean;
  /** Render the chord-hint block. Desktop keeps it; the mobile panel shows
   *  it only when nothing is focused (it controls visibility itself). */
  showHints?: boolean;
}
```

Renders: optional note hint, "Jump back in" (resume candidate, when one
exists), "New session" button (only when `showNewSession`), orientation text.
**No example prompts** in either consumer. The chord hints render only when
`showHints` is true.

`Welcome.tsx` (desktop) becomes a thin wrapper: `PaneScaffold` + `EmptyState` +
`<WelcomeContent showNewSession showHints />`. The mobile panel renders
`<WelcomeContent showHints />` (no `showNewSession` — the Rail owns that).

### 3. Unified mobile panel (`src/shell/mobile/MobilePanel.tsx`)

New component, replacing the `TreeDrawer`-as-welcome role on mobile. It
composes the `Sheet` with the rail content (forwarded as a prop, not imported
directly — see Stream boundary below) and the welcome content:

```tsx
interface MobilePanelProps {
  /** The rail content, forwarded from StackHost's railSlot — NOT imported
   *  from src/shell/rail/ (stream boundary, see below). */
  rail: ReactNode;
  open: boolean;
  onClose: () => void;
}

// Inside MobilePanel:
<Sheet side="bottom" expandable={{ peekHeight, fullScreenFirst: true }}
       open={open} onClose={onClose} title="Sessions" ...>
  {rail}                                     // session tree (always)
  {nothingFocused && <WelcomeContent showHints />}  // welcome + key bindings
</Sheet>
```

**Stream boundary:** `MobilePanel` lives in `src/shell/mobile/` and must NOT
import `Rail` from `src/shell/rail/` — the mobile and rail streams meet only
through props wired by the integrator (the documented boundary in
`TreeDrawer.tsx`'s header comment). `StackHost` forwards its existing
`railSlot` prop to `MobilePanel` as `rail`, exactly as it today forwards
`railSlot` to `TreeDrawer`'s `children`. No new cross-stream import is created.

**State it reads:**
- `useWorkspaceStore.focusedPaneId` + the focused pane's type — to decide
  `nothingFocused` (`focusedPaneId === null || focusedPane?.type === "welcome"`).
  The welcome pane stays registered and is still opened as the backstop on
  mobile (StackHost line 282, AppShell line 244), but on mobile it renders
  *behind* the panel — the panel covers it. `nothingFocused` is true when the
  backstop has landed on "welcome" (no real session is focused), which is
  exactly when the panel shows welcome content.
- `useNavigationStore` — for the resume candidate (inside `WelcomeContent`).

**Entry points:**
- `StackHost` opens `MobilePanel` (full-screen-first) when `nothingFocused`
  becomes true — instead of showing the standalone `Welcome` pane as the
  visible surface. The welcome pane still mounts behind the panel (the
  backstop effect that calls `openPane("welcome")` stays; it is what makes
  `nothingFocused` true). The panel's `open` state is driven by the shared
  `useState` in `StackHost` (see Decision). **This effect must check
  `routeDeferred`** (mirroring StackHost's URL-sync effect, line 267): a
  deep-linked session route defers placement while its location resource
  resolves, and the backstop opens "welcome" in the gap — without the guard,
  the panel would flash open then closed when the session lands. The effect:
  `useEffect(() => { if (routeDeferred) return; if (nothingFocused)
  setPanelOpen(true); }, [nothingFocused, routeDeferred]);`
- The top-bar Sessions button (today's `TreeDrawer` trigger) opens
  `MobilePanel` when a session is focused (sets the shared `open` state true).

**Auto-close on navigation — effect home:** the auto-close effect (today in
`TreeDrawer.tsx` line 63-66, keyed on `focusedPaneId` change) moves into
`MobilePanel`, which calls `onClose` (the `setPanelOpen(false)` in StackHost)
when `focusedPaneId` changes while open. This is the same pattern as today,
relocated because TreeDrawer no longer owns the `open` state. The effect:
`useEffect(() => { if (open && prevFocusedIdRef.current !== focusedPaneId)
  onClose(); prevFocusedIdRef.current = focusedPaneId; }, [focusedPaneId, open]);`
Selecting a session closes the panel when focus **changes**. (Tapping the
already-focused session row does not change `focusedPaneId` — the panel stays
open; this matches today's `TreeDrawer` behavior and is not a regression.)

### 4. `TreeDrawer` becomes the trigger only (`src/shell/mobile/TreeDrawer.tsx`)

`TreeDrawer` keeps its top-bar `IconButton` + needs-you `Badge` trigger but
no longer owns a `Sheet`. The bottom `Sheet` and its `EmptyState` placeholder
are removed; `MobilePanel` is the single panel (see Decision: Sheet ownership).

### Decision: Sheet ownership and shared open state

`StackHost` owns the panel-open state (`useState<boolean>`) and threads it to
both children:
- `MobilePanel` receives `open` + `onClose` (it owns the `Sheet` and the
  `expandable` geometry state internally).
- `TreeDrawer`'s trigger receives `onOpen` (sets the state true). `TreeDrawer`
  keeps only the trigger button + badge; it no longer owns a `Sheet`.

This is the mechanism: one `useState` in `StackHost` is the single source of
truth for "is the panel open," shared by both entry points. `StackHost` mounts
`MobilePanel` once (always mounted; the `Sheet`'s `open` prop gates
visibility). `MobilePanel` decides `fullScreenFirst` based on focus state, which
the trigger alone cannot know. This keeps the one-sheet invariant (never two
panels).

## State machine

`MobilePanel` has two orthogonal axes:

### Geometry (peek ↔ full)
```
peek  --[drag up >50% / tap]-->  full
full  --[drag down >50% / tap]-->  peek
```
Initial geometry on open: **full** when `nothingFocused` (full-screen-first),
**full** when opened via the Sessions trigger while a session is focused (per
the user's "full screen first" answer — opening it returns to full).

### Visibility (open/closed)
```
closed --[Sessions button / no-session landing]--> open
open   --[Escape / scrim / session selected (focus changes)]-->   closed
```
Selecting a session from the tree: `openPane("session", {ref})` fires,
`focusedPaneId` changes, `MobilePanel`'s auto-close effect closes the panel.
Tapping the already-focused session row does not change `focusedPaneId`, so
the panel stays open (same as today's `TreeDrawer` — not a regression).
Collapsing to peek does **not** close the panel; it stays open at peek height.
Closing the panel (Escape/scrim) leaves it closed until the trigger or a
return-to-welcome reopens it.

### Focus transitions
- `nothingFocused` → session focused (user tapped a row): panel closes.
- session focused → `nothingFocused` (user went back, or closed the last
  session): `StackHost`'s backstop effect (line 282) fires
  `openPane("welcome")` as today, which makes `nothingFocused` true, and the
  panel reopens full-screen-first off the `nothingFocused`-keyed effect (which
  checks `routeDeferred` — see Components §3).

### Effect ordering (session→welcome while panel is open)
When the panel is open over a session and focus transitions to welcome, two
effects fire on the same `focusedPaneId` change: MobilePanel's auto-close
(`onClose` → `setPanelOpen(false)`) and StackHost's nothingFocused-open
(`setPanelOpen(true)`). React runs child effects before parent effects, and
both updates batch in the same commit, so the last write wins
(`setPanelOpen(true)` from the parent). The correct result (panel open) holds
as long as the auto-close effect lives in MobilePanel (child) and the
nothingFocused-open effect lives in StackHost (parent). **If a refactor ever
moves the auto-close into StackHost, the auto-close effect must be declared
before the nothingFocused-open effect** so last-write-wins still produces
`open=true`.

### `handleBack` backstop (line 291)
`handleBack` today calls `openPane("welcome")` when the back-stack is
exhausted. This stays **unchanged** — it still opens the welcome pane (which
makes `nothingFocused` true), and the panel opens off the same
`nothingFocused` effect above. The back-stack logic itself
(`backStackRef`, `wentBackRef`, `popValidBackTarget`) is untouched. The panel
is a shell overlay, not a pane, so it does not participate in the pane
back-stack; Back walks to the previous pane or lands on welcome (panel opens),
exactly as today. The only change to `StackHost` is: (a) it owns the shared
panel-open `useState`, (b) it opens the panel when `nothingFocused` becomes
true (guarded by `routeDeferred`), (c) it passes `onOpen` to `TreeDrawer`'s
trigger, and `open`/`onClose`/`rail` to `MobilePanel`.

### Closed panel on welcome (mobile)
When the user closes the panel (Escape/scrim) while `nothingFocused`, the
panel stays closed (the `nothingFocused` effect doesn't re-fire — its
dependency was already true). The user sees the `Welcome` pane behind the
panel. On mobile, this pane renders with `showNewSession` (it doesn't know
it's behind a panel), so its "New session" button is visible — a minor
redundancy with the Rail's own button that's only reachable when the panel is
closed. This is an accepted trade-off: the welcome pane is the desktop
surface and the mobile fallback; suppressing its content on mobile would
require the pane to ask "am I mobile?" (violating the global constraint).
The user can reopen the panel via the Sessions button, which is the intended
mobile welcome surface. If this proves confusing, a follow-up could render
the welcome pane's body empty on mobile via a host-supplied prop — out of
scope for this spec.

## Interaction detail

### Drag handle
- A 36px-wide, 4px-tall rounded bar centered at the panel's top, above the
  title row. `cursor: grab` (desktop); no cursor change on touch.
- Pointer events (`pointerdown` on handle → `pointermove` on window →
  `pointerup`), so one code path covers mouse and touch. No separate touch
  handler.
- During drag, the panel height follows the pointer (`100vh - pointerY`),
  clamped to `[peekHeight, 100vh]`. No smooth transition during the drag; the
  snap on release uses a short transition (`--motion-duration-overlay`,
  `--motion-easing-standard`), disabled under `prefers-reduced-motion`.
- A drag that starts on the handle but the user releases without crossing
  the hysteresis midpoint counts as a **tap** (toggles peek/full).

### Tap
- Tap on the handle toggles peek ↔ full.
- Tapping the scrim still closes (unchanged `OverlayPanel` behavior).

### Keyboard
- Escape closes the panel (unchanged). No dedicated "expand/collapse" key —
  the handle is pointer/touch-first; the panel is mobile-only.

## Styling

- New `sheet.module.css` rules for `.handle` and `.expandable` (the
  expandable bottom variant's height behavior). The existing `.bottom` rule
  (`max-height: min(480px, 100vh)`) remains the default for non-expandable
  sheets.
- The expandable bottom sheet uses `height` (not `max-height`) set
  dynamically, with `transition: height var(--motion-duration-overlay)`; the
  `sheetInBottom` keyframe runs on open as today.
- Peek height: **`min(40vh, 360px)`** (chosen so the session tree's first
  ~6 rows are visible without consuming the whole screen; tunable via a CSS
  variable `--sheet-peek-height` so it can be adjusted without code changes).
- **Scrolling:** the panel body (`OverlayPanel`'s `.body`) scrolls
  vertically when content exceeds the panel height — `overflow-y: auto` at
  peek, same at full (content rarely exceeds full screen, but the tree can).
  The drag handle and title are sticky (do not scroll). At peek height, the
  session tree is visible first; the welcome actions and key bindings are
  below it and reached by scrolling down. At full screen, all three regions
  are visible (tree at top, welcome actions, key bindings at bottom); the
  tree itself scrolls internally if it overflows.
- Drag handle and welcome content reuse existing design tokens (`--space-*`,
  `--ink-*`, `--surface-*`, `--edge`).

## Desktop changes

`Welcome.tsx` / `WelcomeContent.tsx`:
- Remove `EXAMPLE_PROMPTS` and the `.examples` / `.examplesLabel` markup and
  CSS.
- Keep `CHORD_HINTS` and the `.hints` block (rendered when `showHints`).
- Remove the `goToExample` helper and its test.
- The orientation text ("A session can read and edit the repository…") stays.

`welcome.module.css`:
- Remove `.examples`, `.examplesLabel`, and the `.examples button` rule.
- Keep `.actions`, `.orientation`, `.hints`, `.hintRow`, `.hintFooter`.

No change to desktop rail, dockview, or any non-mobile host.

## Content model (what the panel shows)

| Region | No session focused | Session focused (panel reopened) |
|---|---|---|
| Session tree (`<Rail/>`) | Yes | Yes |
| "Jump back in" / orientation | Yes | No |
| Chord hints | Yes (at bottom) | No |

When a session is focused and the user opens the panel via the Sessions
button, they get the tree only — the welcome content is not the landing
anymore, so it is hidden to avoid redundancy with the live session behind it.

## Testing

### Unit tests (vitest, jsdom)

**`Sheet` expandable:**
- Renders a handle iff `expandable` is set.
- Default (no `expandable`): no handle, geometry unchanged — assert existing
  `.bottom` class still applied, no `.expandable` class.
- Tap on handle toggles peek ↔ full (assert height class/data attribute).
- `fullScreenFirst` starts at full on mount.
- `prefers-reduced-motion` disables the snap transition (assert no transition
  class / inline style).
- Escape and scrim-click still close when expandable.

**`WelcomeContent`:**
- Renders "Jump back in" when a resume candidate exists; omits it otherwise.
- Renders chord hints iff `showHints`.
- Does **not** render any example prompt button (assert absence).
- "New session" navigates to `/new`.

**`Welcome.tsx` (desktop wrapper):**
- Existing welcome tests updated: the example-prompt test removed; the
  chord-hint tests kept; the "orientation" test kept.

**`MobilePanel`:**
- `nothingFocused` → panel open, full-screen-first, shows tree + welcome
  content + hints.
- Session focused → panel reopened shows tree only.
- Selecting a session row closes the panel (focus change effect).
- `StackHost` renders `MobilePanel` instead of the `Welcome` pane when
  `nothingFocused`.

**`TreeDrawer`:**
- Trigger button + badge unchanged; no longer owns a `Sheet`.

### Guard tests
- `layoutguard`: the expandable panel at peek and full must not overflow the
  viewport; the drag handle must not cause horizontal overflow.
- `overflowguard`: welcome content in the panel must not overflow at 390px
  (the existing `#197` cap behavior is preserved by reusing `WelcomeContent`).
- `shellguard` / `spawnguard`: unaffected (no shell-command or spawn changes).

### Test boundaries
- Pane/host seam: `MobilePanel` is tested through the existing
  `StackHost`/`TreeDrawer` test harnesses with scripted navigation stores, not
  a live provider.
- The `Sheet` expandable tests use the existing `sheet.test.tsx` harness.

## Risks & mitigations

1. **Shared `Sheet` regressions.** The `expandable` prop defaults off; add a
   regression test asserting a default `<Sheet side="bottom">` renders
   identically before and after. Run the full `make test-web` suite.
2. **Drag gesture on real touch devices.** jsdom does not implement real
   pointer capture or touch events; the drag is unit-tested with manually
   dispatched `PointerEvent` constructors (`new PointerEvent("pointerdown",
   {...})` + `dispatchEvent`), which jsdom does support for event dispatch even
   without real capture. Note the gap: jsdom's lack of pointer capture means
   `setPointerCapture` is a no-op, so the test attaches `pointermove`/`pointerup`
   listeners to `window` directly (the same approach the implementation takes).
   The existing `OverlayPanel` scrim tests dispatch synthetic `MouseEvent`s,
   not `PointerEvent`s — this is a new test pattern, not a reuse. A
   real-device smoke is noted as a follow-up, not a gate.
3. **Two-panel race.** Only one `MobilePanel` is ever mounted (StackHost owns
   it); the `Sheet`'s `open` prop is the single visibility gate. No second
   `Sheet` is instantiated.
4. **Back-stack interaction.** `StackHost`'s `handleBack` backstop and the
   null-focus backstop effect (line 282) both still call
   `openPane("welcome")` as today — this is what makes `nothingFocused` true,
   which opens the panel. The back-stack logic (`backStackRef`,
   `wentBackRef`, `popValidBackTarget`) is untouched — the panel is a shell
   overlay, not a pane, so it does not participate in the pane back-stack.
5. **Focus trap.** `OverlayPanel`'s `FocusScope` traps focus inside the panel.
   The session tree and welcome content are focusable; the drag handle is not
   (pointer-only), so tab order stays within content.

## File impact

| File | Change |
|---|---|
| `src/widgets/dialog/OverlayPanel.tsx` | Add optional `handle` slot (rendered before `<header>`); add optional `bodyClassName`/`headerClassName` props. All default to undefined. |
| `src/widgets/sheet/index.tsx` | Add `expandable` prop; pass handle through `handle` slot; pass `expandableHeader`/`expandableBody` classes through `headerClassName`/`bodyClassName` when expandable. |
| `src/widgets/sheet/sheet.module.css` | Add `.handle`, `.expandable`, `.expandableHeader`, `.expandableBody` rules; keep `.bottom` default. |
| `src/widgets/sheet/sheet.test.tsx` | Add expandable tests; keep existing. |
| `src/panes/welcome/WelcomeContent.tsx` | **New:** extracted focus-dependent content (`showNewSession`, `showHints`). |
| `src/panes/welcome/Welcome.tsx` | Thin wrapper over `WelcomeContent`; remove example prompts. |
| `src/panes/welcome/welcome.module.css` | Remove `.examples*`; keep hints/orientation. |
| `src/panes/welcome/Welcome.test.tsx` | Remove example-prompt test; keep rest. |
| `src/shell/mobile/MobilePanel.tsx` | **New:** unified panel (Sheet + forwarded `rail` prop + WelcomeContent). Owns auto-close effect. |
| `src/shell/mobile/MobilePanel.module.css` | **New:** panel layout styles. |
| `src/shell/mobile/MobilePanel.test.tsx` | **New:** panel state/content tests; auto-close on focus change; geometry reset on open. |
| `src/shell/mobile/TreeDrawer.tsx` | Trigger only; receives `onOpen` prop instead of owning a Sheet. |
| `src/shell/mobile/TreeDrawer.test.tsx` | Update: no owned Sheet; assert trigger calls onOpen. |
| `src/shell/mobile/StackHost.tsx` | Own shared panel-open `useState`; open panel when `nothingFocused` (guarded by `routeDeferred`); forward `railSlot` to MobilePanel as `rail`; pass `onOpen` to TreeDrawer, `open`/`onClose`/`rail` to MobilePanel. `handleBack` and backstop effect unchanged (still call `openPane("welcome")`). |
| `src/shell/mobile/StackHost.test.tsx` | Update welcome-landing assertions (panel opens, not standalone pane); add routeDeferred flash test. |
| `src/shell/mobile/StackHost.module.css` | (Likely none — panel is an overlay.) |

## Out of scope / follow-ups

- Persisting peek vs. full preference across reloads.
- A keyboard shortcut to expand/collapse the panel.
- Animating the welcome content's height when it appears/disappears (the panel
  height is driven by geometry, not content; content visibility is an
  immediate toggle).
- Real-device drag calibration (a smoke-test follow-up).
