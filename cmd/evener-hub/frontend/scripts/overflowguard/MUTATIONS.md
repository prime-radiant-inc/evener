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
