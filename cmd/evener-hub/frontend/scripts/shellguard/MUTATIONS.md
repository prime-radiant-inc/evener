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

## The phone tap floor reaches the rendered Settings controls (`checkSettingsGeometry()`'s filter and RadioGroup heights) - W1-F03/W1-F10

The Settings geometry driver measures the nav filter field, then navigates to
Appearance (`/settings/theme`) and measures every `role="radio"` option
there, in both the 1400px and 390px viewports:

- at 390px the filter field's and every option's height must be at least
  44px;
- at 1400px the filter field's height must stay 32px and every option's 28px.

The phone floor arrives through `composes:` - `Input` composes its own
`tapFloor` in `widgets/input/input.module.css`, and the radio option composes
`styles/tapfloor.module.css` from `widgets/radiogroup/radiogroup.module.css`.
The 390px desktop-height sibling would not distinguish a broken tap floor
from a working one, hence the explicit desktop pins.

**Mutation performed:** in `cmd/evener-hub/frontend/src/widgets/radiogroup/radiogroup.module.css`
deleted the `.option` rule's `composes: tapFloor from "../../styles/tapfloor.module.css";`,
then ran `node scripts/shellguard/settingsGeometry.mjs`.

**Result before restore:** FAIL (the guard process exited 1), one new line
per theme:

```
appearance 390px XL light: phone System radio option retains tap floor (28px)
appearance 390px XL dark: phone System radio option retains tap floor (28px)
```

The same run's measurement lines show all eleven Appearance options at 28px
in both themes, while the filter field stayed at 44px - it composes its own
tap floor - so the failure is attributable to the removed RadioGroup
composition alone, not to a lost media block or a collapsed pane. The
assertion names the first offending option; every option had fallen to the
28px desktop row.

**Result after restore:** PASS, both viewports and themes, options 44px at
390px and 28px at 1400px.

**Verified:** 2026-11-24. **Expect:** fail.

## Phone CommandPalette paints above the Sessions drawer (`measurePhoneSearchPalette()` / `assertPhoneSearchPalette()`) - #7763

At `400x844` with mobile/touch emulation, `MobilePanel` ("Sessions") opens on
the welcome route, Search (`[data-testid="rail-search"]`) is clicked to open
`CommandPalette`, a toast is pushed to `ToastRegion`, and `Escape` dismisses the
palette:

- `document.elementFromPoint(200, 422)` and the palette combobox center must hit
  `CommandPalette` (not `Sessions`);
- an active error toast must still hit-test above `CommandPalette`;
- pressing `Escape` must keep `Sessions` open and restore focus/hit-testing to
  Search.

**Mutation performed:** before moving `<CommandPalette />` after
`<CheatsheetOverlay />` and `.content` in `cmd/evener-hub/frontend/src/shell/AppShell.tsx`
(when `<CommandPalette />` rendered before `<StackHost />` at the same
`--z-dialog` level), ran `node scripts/shellguard/run.mjs`.

**Result before fix:** FAIL (exited 1) with both hit-tests hitting the
`Sessions` drawer instead of `CommandPalette`:

```
shellguard FAIL: phone Command palette center hit-test was occluded: {"tag":"SPAN","ariaLabel":null,"labelledTitle":null,"inPalette":false,"inSessions":true,"isInput":false,"inToast":false}
shellguard FAIL: phone Command palette input is not hit-testable/focused: {"tag":"SPAN","ariaLabel":null,"labelledTitle":null,"inPalette":false,"inSessions":true,"isInput":false,"inToast":false,"focused":true}
```

**Result after fix:** PASS, exit 0 across `npm run shellguard`.

**Verified:** 2026-11-25. **Expect:** fail.
