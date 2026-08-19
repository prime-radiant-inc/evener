// ONE definition of "visible" for the browser guards, because there used to be
// two and they disagreed.
//
// overflowguard asks whether a required footer fact (effort, context, queue) is
// on the screen; spawnguard asks the same of the Spawn pane's breakpoint-gated
// blocks. Both wrote their own predicate, and both got it wrong in a different
// way (kata bsq9): overflowguard read the element's OWN computed display, which
// an ancestor's `display: none` never changes, and spawnguard's guard script
// re-derived the verdict from a reported display/visibility pair and dropped
// the geometry clauses the harness had. Two copies of one word is how that
// happens, so there is now one.
//
// WHAT THIS ASKS, clause by clause, each measured in Chrome against the fixture
// overflowharness-entry.tsx mounts on every run:
//
//   getClientRects().length > 0
//     Box generation, which unlike the `display` property is inherited down the
//     tree: an element inside a `display: none` subtree generates no box at any
//     depth. This is the ancestor-awareness bsq9 is about, and it subsumes the
//     element's own `display: none` - measured 0 rects for both - so there is no
//     separate display clause.
//
//   width > 0 && height > 0
//     A box can be generated and still enclose nothing: `transform: scale(0)`,
//     an explicit 0x0, and an empty inline element all report one client rect
//     with zero area (measured). This clause costs the visually-hidden recipe
//     NOTHING - statusrow.module.css's `.srOnly` is a 1x1 box, measured exactly
//     1x1, which is present for the reader it is written for and stays visible
//     here on purpose.
//
//   visibility !== "hidden"
//     Inherited, so it is ancestor-aware for free: a descendant of a
//     `visibility: hidden` subtree computes `hidden` itself (measured). Such an
//     element keeps its full layout geometry, so no geometric clause can see it.
//
// DELIBERATELY NOT opacity. `opacity` is not inherited: an ancestor at opacity 0
// hides its descendants while each descendant still computes 1, so an
// element-local opacity check would be ancestor-BLIND - bsq9's exact mistake
// wearing a different property. It is also a legitimate resting state in this
// product rather than a synonym for absent (kata hk8v: the attachment remove
// button rests at opacity 0 and is revealed on hover and focus).
//
// KNOWN LIMITS, measured rather than assumed, so the next reader does not have
// to re-derive them:
//   - `content-visibility: hidden`, and the closed `<details>` that Chrome
//     implements with it, keep full layout geometry (measured 38.75x17 for a
//     text span inside both). No predicate built on geometry can see them.
//   - `display: contents` generates no box of its own while its children render
//     normally, so it reads as NOT visible here. That is a false RED, which is
//     the safe direction - a guard crying wolf gets investigated, a guard
//     staying quiet does not - and nothing in src/ uses `display: contents`
//     today. If something does, measure its children, do not loosen this.
export function isElementVisible(element: Element | null): boolean {
  if (element === null) return false;
  if (element.getClientRects().length === 0) return false;
  const box = element.getBoundingClientRect();
  if (box.width <= 0 || box.height <= 0) return false;
  return getComputedStyle(element).visibility !== "hidden";
}
