# overflowguard mutation evidence

kata eevs (see `kata show eevs`): every browser guard's assertions need
proof they can actually fail — "a guard that has never failed is a
decoration" (docs/developing-evener/testing.md). layoutguard records this per-case in each
`case.json`'s `mutation` field. overflowguard has no per-case directory —
it's one script asserting several properties against the real, live-rendered
Session pane (`src/dev/overflowharness-entry.tsx`) — so this file is that
same evidence for the whole guard, one mechanism per guard, picked to fit
each guard's own shape.

Update this file (declaration mutated, date, expected result) whenever an
assertion group in `run.mjs` or `overflowharness-entry.tsx` changes in a way
that could affect what it actually catches. There's no automated check that
this file stays current (overflowguard is not case-structured the way
layoutguard is, so there's no natural per-entry field to warn on) — this is
a manual discipline, flagged for Jesse rather than built here.

## Core horizontal-scroll scan (`measure()`'s scroller loop)

The guard's central mechanism: every element under `#oh-pane` is scanned for
`scrollWidth > clientWidth` on a container whose computed `overflow-x` is
`auto`/`scroll` (not `hidden`/`clip`, which are the intentional fix pattern,
and not visually-hidden 1px clip boxes).

**Mutation performed:** reverted `widgets/panescaffold/panescaffold.module.css`'s
`.body { overflow-x: clip }` to `overflow-x: auto`, and appended a
`.body::after { content: ""; display: block; width: 3000px; height: 1px }`
rule to force real overflow content (nothing in the current fixture actually
overflows, so mutating the containment property alone wouldn't move the
needle — the injected pseudo-element reproduces the "some content is wider
than its scroll container" precondition the original njy9/chevron bug hit).

**Result before restore:** FAIL at all four widths, e.g. `390px ... FAIL - 1
horizontal scroll container(s): div._body_11kw9_62  content 3032px in a
357px box (+2675px)`; the `panel collapse` check also went red
(`horizontalOverflowCount: 2`).

**Result after restore:** PASS at all four widths, `panel collapse ... PASS`.

**Verified:** 2026-08-07. **Expect:** fail.

## Disclosure affordance/layout contract (`disclosureContract()`, the
`summaryDisplay`/`markerDisplay`/`fullWidth`/`stacked`/`aligned` checks)

**Mutation performed:** `panes/session/transcript/messages/notificationcard.module.css`'s
`.summary { display: list-item }` reverted to `display: block` (the native
`<details>`/`<summary>` marker affordance the guard's `summaryDisplay`/
`markerDisplay` checks exist to protect).

**Result before restore:** FAIL at all four widths, e.g. `390px ... FAIL -
raw-notification disclosure affordance/layout: summary=block, marker=inline,
...`.

**Result after restore:** PASS at all four widths.

**Verified:** 2026-08-07. **Expect:** fail.

## Panel-collapse / session-menu split (`verifyPanelCollapse()`)

Not independently mutation-tested against a specific CSS declaration this
pass — it's driven by dockview's own runtime split logic plus SessionMenu
wiring (JS behavior, not a CSS breakpoint token), so there's no single-line
CSS revert that isolates it the way the other two groups have. It DID go red
incidentally during the core-scan mutation above
(`horizontalOverflowCount: 2`), which at least proves it's exercised by a
real DOM it can see failing. Left as a follow-up if Jesse wants a dedicated
mutation for the dock-split threshold itself.

## Top-level intent group column (`verifyIntentColumn()` /
`inspectIntentColumn()`, the `?intenttail=1` fixture)

Asserts the reload-shaped regression: a settled transcript whose final turn
ENDS in intent-bearing tool calls projects that trailing run as its own
virtual-list row (TranscriptBody's `transcriptRowsForProjection`), rendered
outside TurnBlock's `.turn` column. The horizontal-overflow scan cannot see
the misalignment (nothing escapes a scroller), so this compares the
top-level `details[data-testid="intent-group"]`'s box against a
`[data-testid="turn-block"]` box directly at 1024px under the chat preset.

**Mutation performed:** removed `max-width: var(--session-measure)` and
`margin-inline: auto` from `.intentGroup` in
`panes/session/session.module.css` (i.e. the pre-fix state — this guard was
written red-first against exactly that CSS).

**Result before restore:** FAIL -
`intent group column ... FAIL - {"groupFound":true,"groupLeft":25,"groupRight":999,"turnLeft":160,"turnRight":864}`
(the group spanned the full 974px pane content box; the turn column held its
704px measure at 160-864). Every other assertion group stayed PASS, so the
fixture mode disturbs nothing else.

**Result after restore:** PASS -
`intent group column ... PASS - top-level intent group shares the turn content column`.

**Verified:** 2026-09-08. **Expect:** fail.

## Phone Overview About tab (`verifyAboutOverview()` / `runAboutOverviewGuard()`, the `?panels=1&activity=about` fixture)

Asserts the phone's replacement session-details surface: the local-session
Overview opened on About resolves its branch row through the scoped
`evener/git/head` answer, and the row, the sidebar and the page all stay inside
a 390px viewport with no horizontal scroll container. The harness applies
`StackHost.module.css`'s real `.activityOverlay` class, so the overlay rule
itself is measured. `node scripts/overflowguard/run.mjs --about-only` runs just
this page; the full sweep runs it too.

**Mutation performed:** `src/shell/mobile/StackHost.module.css`'s
`.activityOverlay { inset: 0 }` changed to `inset: 0 -100px 0 0`, so the phone
overlay is 490px wide on the 390px viewport.

**Result before restore:** FAIL —
`390px About Overview ... FAIL - sidebar=490px wide, expected the 390px overlay; sidebar spans 0-490, outside the 390px viewport; page scrolls horizontally: document=390, body=490; {"viewportWidth":390,"sidebarLeft":0,"sidebarRight":490,"sidebarWidth":490,...,"branchContained":true,"horizontalOverflowCount":0,"documentScrollWidth":390,"bodyScrollWidth":490}`.
The branch row itself stayed contained, so the width, viewport-edge and page
scroll assertions are what catch this regression.

**Result after restore:** PASS —
`390px About Overview ... PASS - feature/overflow-about-branch-that-must-not-widen-the-panel resolved and contained in the 390px overlay`.

**Verified:** 2026-11-11. **Expect:** fail.

## Trailing held-steer stack column (`verifyHeldSteerColumn()` /
`inspectHeldSteerColumn()`, the `?heldtail=1` fixture)

Asserts the pending-stack column regression: the pending stack renders every
  held steer/drain/promote entry as the transcript's trailing virtual-list row
(Session's `trailingRow`), outside TurnBlock's `.turn` column, so without a
measure rule of its own the ghosts start at the pane's raw left edge while
every delivered message above sits clamped and centered - the same
trailing-row rule `.intentGroup` and AskDock's `.dock` already carry. The
horizontal-overflow scan cannot see this (nothing escapes a scroller), so
this compares the `[data-testid="pending-row-stack"]` box against a
`[data-testid="turn-block"]` box directly at 1024px. The fixture seeds its
one ghost through the real durable outbox path (`enqueueIntent` + the shared
projection refresh), never a direct store write.

**Mutation performed:** removed `max-width: var(--session-measure)` and
`margin-inline: auto` from `.stack` in
`panes/session/transcript/messages/heldsteerstack.module.css` (i.e. the
pre-fix state - this guard was written red-first against exactly that CSS).

**Result before restore:** FAIL -
`held steer column ... FAIL - {"stackFound":true,"stackLeft":24,"stackRight":985,"turnLeft":152.5,"turnRight":856.5}`
(the stack spanned the full 961px pane content box; the turn column held its
704px measure at 152.5-856.5). Every other assertion group stayed PASS, so
the fixture mode disturbs nothing else.

**Result after restore:** PASS -
`held steer column ... PASS - trailing held-steer stack shares the turn content column`.

**Verified:** 2026-09-27. **Expect:** fail.

## First-reply skeleton column (`inspectColdStartSkeleton()`, the
`?coldstart=1` fixture)

Asserts that `ColdStartSkeleton` fills the shared reading column at the Wide
transcript measure: its longest `skeleton-line` bar must fill the column's
content box after `.coldStart`'s own padding, and the column must fill its
parent's content box. The horizontal-overflow scan cannot see a narrow
skeleton - nothing escapes a scroller, the bars simply stop short of the
column. The fixture is an active thread with no turn and one pending send
seeded through the real durable outbox path (`enqueueIntent` for `turn/start`
plus the shared projection refresh).

**Mutation performed:** appended `max-width: 42rem` to `.skeleton` in
`widgets/skeleton/skeleton.module.css` - the inner cap the deleted
`.coldStartMeasure` wrapper used to apply.

**Result before restore:** FAIL -
`cold start skeleton ... FAIL - {"found":true,"measure":"wide","skeletonWidth":976,"contentWidth":944,"barWidth":672,"contentLeft":40,"contentRight":984,"parentContentWidth":976,"padding":["32px","16px","32px","16px"]}`
(the bar held 672px inside a 944px column; the column itself stayed 976px).

**Result after restore:** PASS -
`cold start skeleton ... PASS - first-reply skeleton fills the wide reading column after its padding`
with `measured bar 944.0px in column content 944.0px, column 976.0px of 976.0px at wide`.

A cap on `.coldStart` itself is not a visible regression here: the composed
`.measureColumn` rule is emitted after `session.module.css`, so its
`max-width` wins the cascade (observed 2026-11-24 while mutation-testing this
case).

**Verified:** 2026-11-24. **Expect:** fail.

## Footer visibility predicate (`isElementVisible` in `src/dev/guardVisibility.ts`) — kata bsq9

`footer.effortVisible` / `contextVisible` / `queueVisible` all rest on one
predicate, which used to be:

```ts
const visible = (el: HTMLElement | null) => el !== null && getComputedStyle(el).display !== "none";
```

That asks the element about ITSELF. An ancestor's `display: none` never
changes a descendant's own computed display, so a fact inside a collapsed
subtree still computed `flex`/`inline` and reported visible. It is now
rendered geometry plus inherited visibility, and it lives in
`src/dev/guardVisibility.ts` because spawnguard was carrying a second,
weaker copy of the same word.

### The mutation, with its placement — the placement is part of the recipe

**Mutation performed:** from `src/dev/overflowharness-entry.tsx`, at module
scope, append a `<style>` element carrying
`[data-testid="status-row"] { display: none }`, so the REAL effort / context /
queue elements sit under a `display: none` ancestor. Then run the sweep
against both predicates.

```ts
const mutation = document.createElement("style");
mutation.textContent = '[data-testid="status-row"] { display: none }';
document.head.appendChild(mutation);
```

**Do NOT put that rule in `overflowharness.html`'s static `<head>` instead.**
It silently does nothing and the whole sweep reports
`390px ... PASS - disclosures stay native/stacked and nothing scrolls
horizontally` (verified). Vite injects the CSS-module `<style>` tags at
runtime, *after* the parsed head, so `.row { display: flex }` wins on document
order at equal specificity. Appending from the entry module works because the
CSS imports at the top of that file are hoisted and evaluated first, putting
the mutation last. A mutation recipe whose wrong variant passes quietly is
worth more warning than the recipe itself.

**Result with the OLD predicate:** the line that never appeared is
`pressured footer facts missing` — `effort`/`context`/`queue` all read visible
with the row collapsed. That is the false green bsq9 describes, reproduced.

Two other lines DO appear in that run, and neither is the footer-visibility
check doing its job: `pressured footer model has zero visible width`, from a
separate check that reads `clientWidth` directly, and the
`shared visible() predicate ... is broken` line from the probe below, which is
the fixture catching the predicate rather than the predicate catching the DOM.
Read for the absence of `pressured footer facts missing`, not for a green run.

**Result with the NEW predicate**, both lines, at 390, 700, 1024 and 1400:

```
390px ... FAIL - pressured footer facts missing: effort=false, context=false, queue=false
390px ... FAIL - pressured footer model has zero visible width
```

**Verified:** 2026-08-17. **Expect:** fail.

### The live fixture

The predicate also carries a fixture, `visibilityProbe()`, asserted at every
width by `run.mjs` — so unlike the groups above it does not depend on this
file staying current, and it is the only fixture spawnguard has for the same
function. One probe per clause:

| probe | expected | pins |
| --- | --- | --- |
| `rendered` | visible | the positive control: without it, a predicate answering "not visible" to everything would satisfy every other row |
| `ancestorHidden` | not visible | the bsq9 regression — own display `flex`, inside a `display: none` div |
| `visuallyHidden` | visible | `statusrow.module.css`'s `.srOnly` recipe, measured exactly 1x1, so the area clause costs it nothing |
| `visibilityHiddenAncestor` | not visible | inherited `visibility` — the span keeps its full box, so geometry alone cannot see it |
| `zeroArea` | not visible | `transform: scale(0)` — one client rect enclosing nothing |

Red-before-green for each: with the original `display`-only predicate the
probe reported `ancestorHidden=true` at all four widths; with the
rects-only predicate that replaced it, `visibilityHiddenAncestor=true` and
`zeroArea=true` at all four widths.

**Measured limits**, so nobody has to re-derive them: `content-visibility:
hidden` and the closed `<details>` Chrome implements with it both keep full
layout geometry (38.75x17 for a text span inside either), so no geometric
predicate can see them; `display: contents` reads as not visible, a false RED
that is the safe direction and that nothing in `src/` triggers today. Opacity
is deliberately excluded — it is not inherited, so an element-local check
would be ancestor-blind in exactly the way bsq9 is about, and opacity 0 is a
legitimate resting state here (kata hk8v).

## Outside press holds the composer until its click lands (`verifyComposerOutsidePressClick()`)

The composer card is in flow. Its outside-press listener used to clear
`data-composer-editing` at `pointerdown`, which shrank the card from its
expanded height to one row before mouseup. A transcript button or disclosure
under the pointer then moved away and the browser delivered the click to an
ancestor: the user clicked the control and nothing happened. The guard drives
trusted `Input.dispatchMouseEvent` at a real disclosure below the card, holds
the press long enough for a fold to land, and asserts editing survived the
hold, the disclosure toggled on release, and the card folded only after the
click. A second case drags inside transcript text and asserts the card did not
fold mid-gesture.

**Mutation performed:** reverted the deferral in
`src/panes/session/composer/Composer.tsx` — the `pointerdown` listener cleared
`data-composer-editing` immediately again, and the click/pointerup handling
that defers it was removed. A stronger mutation also has to restore the two
other height drivers the same hold covers: `handleFollowUpBlur`'s
`setFollowUpFocused(false)` and the resting-row reconcile. The browser case
catches the first because its harness session is live; the ended-session
driver has unit coverage instead
(`Composer.recovery.test.tsx`, "an ended session's field keeps its writing
height until the outside click lands"), which fails while that one is ungated.

**Result before restore:** FAIL at both widths, `composer outside-press click
390px ... FAIL - editing must survive the outside press until its click lands`
and the same at 1400px.

**Result after restore:** PASS at both widths,
`{"disclosure":"Context summary · 104 chars","heldEditing":true,"toggled":true,"foldedAfterClick":"true","dragHeldCard":true}`.

**Verified:** 2026-11-16. **Expect:** fail.

### The pressed disclosure's own state is the toggle oracle

The toggle assertion first read "some disclosure differs from the pressed
one's initial state". The harness holds five turn-block disclosures in mixed
states (before the press: `[false,false,false,true,false]`), so a sibling
already in the other state satisfied that predicate even when the pressed
disclosure never changed. The guard now records the pressed disclosure's index
in the `details` list and asserts that node's own `open` flipped, plus that the
siblings' states are unchanged. The result line reports
`pressedOpenBefore`/`pressedOpenAfter`/`siblingOpensBefore` so a passing run
shows the flip was real.

**Mutation performed:** `disabled` on the scaffold `Disclosure` in
`src/panes/session/transcript/messages/SystemNoticeItem.tsx`, so the pressed
summary's `requestToggle` never runs and its own `open` never flips.

**Result with the old predicate:** PASS at both widths while the result line
read `"pressedOpenAfter":false` — the vacuous pass this finding named.

**Result with the pressed-identity predicate:** FAIL at both widths,
`composer outside-press click 390px ... FAIL - page eval threw: composer
condition timed out: document.querySelectorAll('[data-testid="turn-block"]
details')[1].open !== false`, and the same at 1400px.

**Result after restore:** PASS at both widths, with `"pressedOpenAfter":true`
and `"toggled":true`.

**Verified:** 2026-11-16. **Expect:** fail.

## Landscape phone folded row and single safe-area owner

The real-composer fixture renders a 572px session pane inside a 951×669pt
coarse-pointer landscape viewport with a 34px bottom safe area. That keeps the
card at 524px, below PromptCard's 620px desktop folded threshold, while the
shell uses its desktop edge footer. The assertions require compact folded
controls, a shared field/control row, and only the ordinary 12px footer gap.
Both resting and editor-focus states are measured: focus must restore Attach,
the model picker, and the stacked two-line editor. The edge footer must reserve
the 34px band, leaving the status chips 34px above the viewport bottom.

**Mutation performed:** used the pre-fix conditions:
`controlsCollapsed = isMobile && resting` in `Composer.tsx`, and the
`.footerBeforeEdge { padding-bottom: var(--space-3) }` exemption only inside the
`max-width: 899px` media block in `panescaffold.module.css`.

**Result before restore:** FAIL:
`951x669 Duo landscape composer safe-area=34 ... desktop bottom padding/gap=46/46px, expected unchanged 12px; the resting landscape phone did not compact its controls; folded field and controls remain stacked`.
The card measured 125px tall, and the field/control centres were
504.203125/547. This is the reported shape, not a fixture-only approximation.

**Result after restore:** PASS:
`951x669 Duo landscape composer safe-area=34 ... one folded row, footer gap=12px, card width=524px`.
The existing phone safe-area/keyboard matrix and desktop 900/1400px footer
cases remained green in both runs.

**Command:** `node scripts/overflowguard/run.mjs --footer-only 951`.

**Verified:** 2026-11-20. **Expect:** fail when either pre-fix condition is restored.

The independent review then pointed out that the edge footer did not actually
own the band and that the first version measured only the resting state. With
the new assertions red, Chrome reported `edge footer safe-area padding=0px,
expected 34px`, `status chips leave 0px below themselves, expected 34px`, and
the focused state was exercised. Adding `.edgeFooter { padding-bottom:
var(--dock-clearance) }` and PromptCard's existing `controlsTestId` hook turned
the full matrix green: resting keeps one row and focus restores full controls,
while both states keep the 12px composer gap and 34px status-chip clearance.

The paired fine-pointer case uses the same 951×669 viewport and 572px pane with
touch disabled. It requires the stacked controls and visible model picker. A
mutation removing `(pointer: coarse) and (hover: none)` from
`LANDSCAPE_PHONE_QUERY` failed with `a fine-pointer landscape desktop
incorrectly compacted its controls`, `the fine-pointer resting row lost its
model picker`, and `the fine-pointer folded field and controls did not stay
stacked`. Restoring those conditions passed: `951x669 fine-pointer landscape
composer safe-area=34 ... PASS - stacked controls and visible model, footer
gap=12px, status clearance=34px`. The safe-area assertion reads the status bar's
bottom edge and chip containment because smaller fine-pointer chips are centred
in their 30px bar rather than filling its height.

## Composer action tap floor on a coarse landscape phone (`assertDuoLandscapeComposer`)

The 951x669 coarse Duo landscape fixture puts a 572px pane in a viewport past
the 899px phone boundary. Its composer content box is below 559px, so the
container query hides the Send/Stop words and only the icon remains. The guard
measures whichever of Send or Stop fills the primary slot and requires the 44px
tap floor in both axes.

**Mutation performed:** reverted the Send/Stop width floor in
`panes/session/composer/composer.module.css` to its pre-fix prelude
(`@media (max-width: 899px)`, dropping `, (any-pointer: coarse)`).

**Result before restore:** FAIL:
`951x669 Duo landscape composer safe-area=34 focus=none ... FAIL - the icon-only landscape Send/Stop is 31.0x44.0px, below the 44px tap floor`,
and the same measurement failed for `focus=editor`. Every other footer case
stayed PASS in that run.

**Result after restore:** PASS:
`951x669 Duo landscape composer safe-area=34 focus=none ... PASS - one folded row, Send/Stop=44.0x44.0px, footer gap=12px, status clearance=34px`,
and the editor-focus state likewise.

**Command:** `node scripts/overflowguard/run.mjs --footer-only`.

**Verified:** 2026-11-22. **Expect:** fail when the coarse-pointer arm is removed.
