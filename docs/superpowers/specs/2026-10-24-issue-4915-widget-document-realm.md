# Overlays and widgets resolve their own document and window

Issue: [#4915](https://github.com/prime-radiant-inc/evener/issues/4915). The
same class was fixed for `widgets/popover`, `widgets/focusscope`, the needs-you
list and the composer in PR #4854; this sweeps the widgets that still reach for
the opener.

Jesse approved delivering this sweep as **two PRs**. This spec is part 1: the
shared helper plus `menu` and `hovercard` (including its floating bubble and
floating label). Part 2 covers `sheet`, `directorypicker` and `textarea` and
starts from the helper this PR lands.

## Problem

A conversation can live in a Dockview popout, which is a separate `Window` with
its own `Document` and its own constructors. A shared widget mounted there that
uses the module-scope `document`, `window`, or `instanceof Element` portals,
positions, listens or focus-manages against the opener instead of the window the
person is using. In a popout that means a menu or bubble rendered into the wrong
document (invisible or wrongly placed), dismissal listeners on the wrong window,
and `instanceof` checks that reject nodes created in the popout's realm.

## Scope

One internal helper, applied to two widget areas:

| Widget | Reach-throughs |
| --- | --- |
| `widgets/menu` | `document.addEventListener("mousedown")`; `window` scroll/resize; `target instanceof Document`, `target === window`, `target instanceof Element`; portal to `document.body` |
| `widgets/hovercard` | `window` scroll/resize for press abort; `document.addEventListener("pointerdown", …, true)`; the floating label's own `window` scroll/resize dismissal pair and its `document.activeElement` read; portal to `document.body` and `window.innerWidth/innerHeight` in `floatingBubble.tsx` |

## The helper

`src/widgets/internal/documentRealm.ts` (internal, imported only from inside
`src/widgets/`; the barrel does not export it):

```ts
export function ownerDocumentOf(node: Node | null | undefined): Document;
export function ownerWindowOf(node: Node | null | undefined): Window;
```

- `ownerDocumentOf` returns `node.ownerDocument` when it has one, else the
  module's own `document` (the widget has not mounted yet).
- `ownerWindowOf` returns that document's `defaultView`, else the module's own
  `window`.
- Realm checks do not ask for constructors at all: a node is an element when
  `nodeType === 1`, a document when `nodeType === 9`. That number is identical
  in every realm and survives a node being adopted into another document, which
  is exactly the Dockview move.

Each widget resolves its document/window from the nodes it actually holds (its
root element, its trigger), not from a module-scope global captured at import.

`isHoverless()` deliberately keeps reading the module window: it asks whether
the *device* can hover, which is the same answer in every window of the same
browser. That is a capability read, not a document read.

## Non-goals

- No new widget and no new public widget prop. The widget APIs in
  `docs/web-ui/design-system.md` are unchanged.
- Part 2's widgets (`sheet`, `directorypicker`, `textarea`) and the tooltip's
  own portal target are out of scope here; the tooltip keeps its current
  behavior and its tests.
- No change to `widgets/popover`, `widgets/focusscope`, the needs-you list or
  the composer; they were fixed in #4854 and are the reference cases.
- No change to how a popout is opened, moved or closed, and no Dockview
  listener changes.

## Behavior

1. A menu or hovercard bubble mounted inside a popout portals into the popout's
   `body`, so it is visible and positioned in the window the person is using.
2. Dismissal, drag and resize listeners attach to the popout's `window`/
   `document`; a scroll, resize or outside press there closes or updates the
   widget. The opener's events do not.
3. Placement math uses the owning window's `innerWidth`/`innerHeight`.
4. The pointer-activity tracker keeps its shared document listeners; the
   hoverless capability read keeps using the module window.
5. Nodes adopted into another document (Dockview moving a mounted panel) are
   still recognized: element/document checks use `nodeType`.

## Preservation and recovery

The opener keeps working exactly as today. A widget whose trigger is unmounted
still removes every listener from the window it attached to. No overlay state,
focus target or pending gesture is dropped by the move: the press-abort and
dismissal behavior is unchanged within whichever window owns the widget.

## Test plan (whole journey per widget, in jsdom where a realm exists)

A real second realm in jsdom is an **iframe's** `contentDocument`:
`document.implementation.createHTMLDocument` shares the opener's constructors
and cannot catch an `instanceof` defect, as PR #4854 demonstrated. Each case
renders the widget through `createPortal` into the iframe body.

1. **menu** — the panel is a child of the popout body (and not the opener's); a
   `mousedown` in the popout closes it; a scroll on the popout window closes it;
   a scroll on an unrelated popout container leaves it open.
2. **hovercard** — the bubble is a child of the popout body; an outside
   `pointerdown` in the popout dismisses it; placement uses the popout
   viewport (600x150 gives left 352px and top 8px, versus the opener's math).
3. **floating label** — a label shown while its trigger sits in the popout
   dismisses on a scroll in the popout's own window.

Every case must also keep the existing opener-document tests passing unchanged.
Where jsdom cannot model something, the case asserts the resolved-realm seam
that can be observed instead, and this spec records the limit rather than
claiming browser behavior it did not prove.

Browser guard: not needed for the class. The cascade guard already exercises a
real popout's needs-you list; the remaining risk is geometry inside a popout
window, which jsdom cannot measure. That limit is recorded in the PR and, if
Jesse wants it proven, becomes a separate browser-guard leg rather than a claim
here.

## Documentation

One rule in `docs/developing-evener/design-system.md` § "Adding or extending a
widget": shared widgets resolve the document and window of the node they own,
never the module-scope globals, and realm checks use `nodeType`.
