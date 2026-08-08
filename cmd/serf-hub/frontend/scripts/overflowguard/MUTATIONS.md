# overflowguard mutation evidence

kata eevs (see `kata show eevs`): every browser guard's assertions need
proof they can actually fail — "a guard that has never failed is a
decoration" (docs/testing.md). layoutguard records this per-case in each
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

## bsq9 concern — in scope for this audit, not fixed here

`overflowharness-entry.tsx`'s `visible()` helper (used for
`footer.effortVisible`/`contextVisible`/`queueVisible`) is:

```ts
const visible = (el: HTMLElement | null) => el !== null && getComputedStyle(el).display !== "none";
```

This checks only the element's OWN computed `display`, not any ancestor's.
An ancestor with `display: none` would make the element itself report a
non-`"none"` `display` (its own declared value, uncomputed-through-ancestor)
while it is not actually visible — the exact false-green bsq9 describes.
Confirmed this helper is the one and only visibility check `footer.*Visible`
depends on; per the kata brief, bsq9 itself is out of scope and NOT fixed by
this commit.

No other assertion in this guard depends on `visible()` — the scroller scan
uses `clientWidth`/`scrollWidth` (which already read as 0/0 under an
ancestor `display:none`, so the scan itself doesn't have this defect), and
the disclosure contract reads `getComputedStyle` values directly off the
elements it already knows are attached and open.
