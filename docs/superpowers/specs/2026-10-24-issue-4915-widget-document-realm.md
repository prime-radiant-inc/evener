# Overlays and widgets resolve their own document and window

Issue: [#4915](https://github.com/prime-radiant-inc/evener/issues/4915). The
same class was fixed for `widgets/popover`, `widgets/focusscope`, the needs-you
list and the composer in PR #4854; this sweeps the widgets that still reach for
the opener.

## Delivery split

Jesse approved splitting the sweep. The hovercard's floating-label lifecycle
turned out to own several document-bound subsystems (portal, placement, pointer
tracking, resize observation, gesture cancellation, dismissal, focus reads) that
review kept finding one at a time, so it moves to its own change with one
systematic pass. Three changes:

1. **1a (this spec): the internal realm helper plus `menu`.** Menu is the small,
   stable instance of the class and lands first.
2. **1b: `hovercard`** (bubble, floating label, gestures), threaded through one
   realm input with a test per subsystem.
3. **Part 2: `sheet`, `directorypicker`, `textarea`** and the tooltip's own
   portal target.

## Problem

A conversation can live in a Dockview popout, which is a separate `Window` with
its own `Document` and its own constructors. A shared widget mounted there that
uses the module-scope `document`, `window`, or `instanceof Element` portals,
positions, listens or focus-manages against the opener instead of the window the
person is using. In a popout that means a menu rendered into the wrong document
(invisible or wrongly placed), dismissal listeners on the wrong window, and
`instanceof` checks that reject nodes created in the popout's realm.

## Scope (1a)

| Component | Reach-throughs |
| --- | --- |
| `widgets/internal/documentRealm` (new) | `ownerDocumentOf` / `ownerWindowOf`, realm checks by `nodeType`; a Document argument answers with itself |
| `widgets/internal/viewportWatch` (new) | the scroll-capture plus resize pair, on the node's own window |
| `widgets/internal/useOwnedDocument` (new) | the document a widget's root lives in, kept current across a Dockview move (including removal-then-insertion), with one shared observer per document |
| `widgets/menu` | `document.addEventListener("mousedown")`; `window` scroll/resize; `target instanceof Document`, `target === window`, `target instanceof Element`; portal to `document.body` |

## The helper

- `ownerDocumentOf` returns `node.ownerDocument` when it has one, itself for a
  Document, else the module's own `document`.
- `ownerWindowOf` returns that document's `defaultView`, else the module's own
  `window`.
- Realm checks do not ask for constructors at all: a node is an element when
  `nodeType === 1`, a document when `nodeType === 9`. That number is identical
  in every realm and survives a node being adopted into another document.

`useOwnedDocument` watches the resolved document for the root leaving it and
turns a move into a render, so the portac and listeners rebind. A detached node
is re-checked briefly because Dockview can remove and re-insert across a turn.
Observations are shared per document; an idle menu does not observe.

## Non-goals

- No new public widget and no new public widget prop. The widget APIs in
  `docs/web-ui/design-system.md` are unchanged.
- `hovercard`, `sheet`, `directorypicker`, `textarea`, the tooltip's portal and
  the tooltip consumer are out of scope (1b and part 2).
- No change to how a popout is opened, moved or closed, and no Dockview
  listener changes.

## Behavior

1. A menu mounted inside a popout portals into the popout's `body`.
2. Its dismissal (`mousedown`, scroll, resize) attaches to the popout's own
   document/window; the opener's events do not reach it.
3. Placement flips and clamps against the owning window's viewport.
4. A menu whose panel Dockview moves keeps up: the portal, listeners and
   placement follow the document it is in now.
5. Nodes adopted into another document keep their original prototype, so no
   `instanceof` decides realm.

## Preservation and recovery

The opener keeps working exactly as today. An unmounting menu removes every
listener from the window it attached to. No menu state or pending interaction is
dropped by a move.

## Test plan

A real second realm in jsdom is an **iframe's** `contentDocument`:
`document.implementation.createHTMLDocument` shares the opener's constructors
and cannot catch an `instanceof` defect. Cases render the widget through
`createPortal` into the iframe body.

1. **menu portal** — the panel is a child of the popout body, not the opener's.
2. **menu dismissal** — a `mousedown` in the popout closes it; a scroll on the
   popout window closes it; an unrelated popout container's scroll leaves it
   open.
3. **menu placement** — opener 1024x768 vs popout 600x150 gives 390px/22px for
   a trigger at (450,20) sized 140x125.
4. **menu adoption** — the ownership hook reports the destination document
   after a container moves, and after a remove-then-insert turn; a menu open
   across a move closes (focus leaves the popout document), which is why the
   menu-level journey is the hook's contract rather than an open menu surviving.

Every case must keep the existing opener-document tests passing unchanged.

## Documentation

One rule in `docs/developing-evener/design-system.md` § "Adding or extending a
widget": shared widgets resolve the document and window of the node they own,
never the module-scope globals, and realm checks use `nodeType`.
