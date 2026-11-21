# shellguard mutation evidence

kata eevs (see `kata show eevs`): every browser guard's assertions need
proof they can actually fail - "a guard that has never failed is a
decoration" (docs/developing-evener/testing.md). shellguard is not
case-structured the way layoutguard is (one `run.mjs` over the
`src/dev/shellguard-entry.tsx` harness), so this file records one mechanism
per assertion group, picked to fit the group's own shape.

Update this file (declaration mutated, date, expected result) whenever an
assertion group in `run.mjs` or `shellguard-entry.tsx` changes in a way that
could affect what it actually catches. There is no automated check that this
file stays current; like overflowguard's copy, it is a manual discipline.

## Focus-triggered scroll on a layout dismissal (`measureFocusScroll()` /
`assertFocusScroll()`, the `window.focusScrollFixture` harness) - #6838

The rail is scrolled to its maximum, the last session row's `...` menu is
opened, and a fixture-owned block taller than the rail body is prepended
above that row. The insertion pushes the trigger out of the rail body's
client area and the menu's own layout observer dismisses it. After the
dismissal settles the guard reads `document.activeElement` and the rail
body's `scrollTop`:

- focus must be back on the row's menu trigger;
- the rail body's `scrollTop` must be the value recorded just before the
  dismissal (the insertion itself is separately asserted not to have moved
  it, and the fixture disables scroll anchoring so the only remaining mover
  is the dismissal's focus).

The fix under test is `widgets/menu/index.tsx:933`'s
`triggerRef.current?.focus({ preventScroll: true })`, on the menu's
layout-dismissal path (`closeMenuRestoringFocusWithoutScroll`).

**Mutation performed:** in `cmd/evener-hub/frontend/src/widgets/menu/index.tsx`
changed the dismissal's restore from
`triggerRef.current?.focus({ preventScroll: true })` to
`triggerRef.current?.focus()`, then ran `node scripts/shellguard/run.mjs`.

**Result before restore:** FAIL, one new line:

```
shellguard FAIL: focus scroll: the rail body's scrollTop moved from 3471 to 4319 (+848.0px, the fixture inserted 848px above the trigger) when the dismissal returned focus to the trigger
```

The +848px the dismissal added is exactly the block the fixture prepended
above the trigger, which the failure message names. The check separately
asserts that the insertion itself did not move the offset and that the
trigger left the rail body's client area, so the movement is attributable to
the dismissal's focus alone. The guard process exited 1. Focus itself still
landed on the trigger (the fixture holds the row's actions reveal, see
below), so the scrollTop assertion is what catches this mutation. The run's
own summary line reports the rail body as 4279px in 808px.

**Result after restore:** PASS:

```
shellguard focus scroll: PASS - dismissed to rail row local:p11-s9's menu trigger with the row's actions reveal held by the fixture and the rail body's scrollTop unchanged at 3471
```

**Verified:** 2026-11-20. **Expect:** fail.

### The row's reveal is a fixture control, and the guard measures it honestly

A rail row's `.actions` wrapper (`src/shell/rail/RailRow.module.css:103-120`)
is `visibility: hidden` unless the row is hovered, its treeitem is focused,
or its own menu is open. A layout dismissal ends all three at once: the
trigger stays in the document, the 120ms visibility delay raises it to
`visibility: hidden`, and Chrome blurs a focused element it can no longer
show. Measured on this fixture without holding the reveal, the dismissal DID
land focus on the trigger - the trigger was the active element when
`aria-expanded` flipped to `false`, still connected and `visibility: visible`
- and a `focusout` to `body` followed once the reveal collapsed.

So `revealFocusScrollActions()` in `shellguard-entry.tsx` pins that one
row's `.actions` wrapper visible for the measurement (inline
`visibility: visible; opacity: 1`), and `scrollRailToEnd()` reports
`actionsRevealed` so the runner fails loudly if the wrapper or its class
name ever moves. This is fixture state, not an assertion weakening: the
`activeElement` and `scrollTop` assertions are unconditional, and the
mutation above still fails the `scrollTop` one. What the group does NOT
measure is the
real rail's hover-quiet fade-out after a dismissal - with no pointer on the
row and no treeitem focus, that styling returns focus to `body` on its own,
independently of `preventScroll`.
