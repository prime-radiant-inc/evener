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
this compares the `[data-testid="pending-message-stack"]` box against a
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
