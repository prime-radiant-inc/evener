# Issue #5000: cascadeguard reveal ignores a closed inactive disclosure beside current delegates

Status: frozen for independent PAR review. Lane `followup-5000-cascadeguard-reveal`, based on `69c67e8580` plus the origin/main merge at `5971ad00af`.

## Missing invariant

`reveal(edge, container)` in `cmd/evener-hub/frontend/scripts/cascadeguard/run.mjs` (lines 1551-1579 at this base) must reach a delegate row that the real Agents tab hides behind its inactive disclosure. It chooses the control to press from the total count of visible activity anchors:

```js
const visible = await read(`document.querySelectorAll(${q(`${container} [data-activity-anchor]`)}).length`);
const b = buttons.find(n => !n.matches(REFUSED_BUTTON_SELECTOR) &&
  (visible === 0
    ? n.textContent.trim().startsWith('Inactive subagents (')
    : n.textContent.trim().startsWith('Show ') || n.textContent.trim() === 'Load more subagents'));
```

`AgentsTab.tsx` renders `current` rows before the inactive disclosure and `inactive` rows after it. A valid mixed collection therefore has at least one visible current anchor and a closed `Inactive subagents (N)` fold. The old condition takes the `visible !== 0` branch, excludes the fold, and needs a `Show ` or `Load more subagents` control. When the collection is complete there is neither, so `reveal` waits until its deadline and throws `real disclosure or direct page boundary` without ever pressing the only useful control.

## Evidence

CI run `40168560141` failed `TestAgentCascadeBrowser` at the `fixture.branch` reveal (`run.mjs:1965` then, `run.mjs:1950` after this base's merge). Retained artifact `12589936779` (`browser-guards-artifacts-40168560141-1`, downloaded 2026-10-25) contains:

- `driver.log`: `Error: timed out after 15000ms ... waiting for real disclosure or direct page boundary`, stack `reveal` -> `run.mjs:1965`.
- `failure.html`: exactly one `[data-activity-anchor]` (`cascade-1`, running), one closed `Inactive subagents (51)` button, zero `Load more subagents` buttons, zero `Show ` buttons inside the sidebar.
- `reveal-events.json`: `[]`.

Real headless Chrome (`Chrome/155.0.8059.84`) on that captured `failure.html`, evaluated at `file://`:

| Condition | Visible anchors | Selected control |
| --- | --- | --- |
| Old anchor-count condition | 1 | `null` |
| DOM-order candidate | 1 | `Inactive subagents (51)` |

The one anchor precedes the fold in document order and none follows it, which is the state a closed inactive fold beside current rows always has.

## Chosen change (driver only)

An inline version of the same DOM-order predicate was already reviewed at
`25e5114d14` and ships in the notification repair stack (`d8cdf797` / #4983 and
`d8a1d96` / #5005). It is not on main. This lane re-lands the same predicate on
current main and adds the missing driver/test seam; the extraction supersedes
the inline shape because `run.mjs` executes on import and cannot expose its
helpers to a test.

Move the control choice into a page-evaluated helper, `cmd/evener-hub/frontend/scripts/cascadeguard/revealControl.mjs`:

```js
export function revealControl(container, refusedSelector) {
  if (!container) return null;
  const buttons = [...container.querySelectorAll("button")];
  const usable = (button) => !button.matches(refusedSelector);
  const fold = buttons.find((button) => usable(button) && button.textContent.trim().startsWith("Inactive subagents ("));
  // AgentsTab renders current rows before the fold and inactive rows after it.
  const DOCUMENT_POSITION_FOLLOWING = 4;
  const inactiveVisible = fold !== undefined && [...container.querySelectorAll("[data-activity-anchor]")]
    .some((node) => !!(fold.compareDocumentPosition(node) & DOCUMENT_POSITION_FOLLOWING));
  if (fold && !inactiveVisible) return fold;
  return buttons.find((button) => usable(button)
    && (button.textContent.trim().startsWith("Show ") || button.textContent.trim() === "Load more subagents")) ?? null;
}
```

The constant avoids `document.defaultView`, which is null for documents made
with `createHTMLDocument` or `DOMParser`, and avoids a bare `Node` global so the
serialized function is self-contained.

`reveal` keeps its existing wait and injects that exact function source into the page:

```js
const step = await wait(revealControlStep(selector, container, REFUSED_BUTTON_SELECTOR), "real disclosure or direct page boundary");
```

`revealControlStep(selector, container, refusedSelector)` returns the one-page-turn expression `reveal` evaluates. It is exported from the same module and contains the unchanged target check, `scrollIntoView`, topmost-element hit test, `REFUSED_BUTTON_SELECTOR` filter, `textContent` snapshot and `click`. `run.mjs` keeps the ten-boundary loop, the progress assertion (`textContent !== before`) and the existing `Driver.waitPage` deadline (15s floor, widened only by its load reaction). `Show ` and `Load more subagents` still handle an open fold and remote paging. The step builder is what makes the seam cover the driver's expression rather than only the predicate.

Ordering trade-off: while the fold is open, the helper presses every `Show N
more` before it presses `Load more subagents`. A terminal target may be hidden
among the already-loaded inactive rows, so pressing the remote boundary first
would never reveal it. The reverse case — a current target on a later remote
page — costs one fold plus one `Show` per 20 already-loaded inactive rows
(`AgentsTab.tsx` `PAGE`) before the boundary. The current fixture needs four
boundaries; a collection with more than 160 loaded inactive rows and a target
past the next remote page would reach the ten-boundary tripwire. There is no
DOM state that distinguishes
"hidden in the loaded inactive list" from "not loaded yet", so no fixed order
serves both cases; the reviewed predicate keeps the order that fixes this issue.

## Regression at the driver/test seam

New `cmd/evener-hub/frontend/scripts/cascadeguard/revealControl.test.js`, run by the frontend's Vitest/jsdom suite (the same seam as `scripts/browserGuardButtonState.test.js`). It imports the exported function and exercises:

1. Mixed current row + closed fold + no page boundary -> the fold. This is the issue's red case; the old anchor-count condition returns `null`.
2. The same mixed state plus a rendered `Load more subagents` boundary -> the fold, not the boundary. The old condition picks the boundary here.
3. Closed fold with no current rows -> the fold (existing behavior).
4. Open fold (an anchor follows it) -> `Show N more`.
5. No fold, remote page boundary -> `Load more subagents`.
6. Open fold with every loaded inactive row visible and a remote page boundary -> `Load more subagents` (the fixture takes this path once all 49 page-one inactive rows render).
7. Open fold with both `Show N more` and `Load more subagents` -> `Show N more`, pinning the documented order.
8. A detached document made with `createHTMLDocument` (no `defaultView`) -> the same fold choice, proving the helper has no browsing-context dependency.
9. Refused (`aria-disabled="true"`/`:disabled`) fold -> skipped, the page boundary is chosen; refused fold alone -> `null`. This is defensive: `AgentsTab` does not render the fold disabled today, but the driver must keep rejecting a disabled control.
10. The serialized helper evaluated with `Node` and `document` shadowed as `undefined` still chooses the fold, proving it needs neither global.
11. The exact `revealControlStep` expression, evaluated as the driver evaluates it, presses the fold beside a current row, returns `{ revealed: true }` without pressing when the target is visible, and returns `null` without pressing when the control is not topmost.

Red evidence is the test run against the old condition placed in the helper (test 1 fails, returning `null`); the green run is the DOM-order condition above. The full browser guard then proves the injected path end to end.

## Non-goals

- No production UI, `AgentsTab.tsx`, store, or rendering change.
- No timeout or boundary-cap change.
- No change to target identity, hit testing, disabled rejection, paging, or progress assertions.
- The issue says this is driver-only; no product behavior is expected to change.
- The initial gate at `run.mjs:1553` still accepts only the target row or the
  fold, not a `Load more subagents` boundary. That is a pre-existing limitation
  for a first page with no terminal rows, not the defect in #5000; it is filed
  as #5018 and not changed here.
- The fold control has no `aria-expanded`, which is why the driver infers its
  state from document order. Adding it is a product accessibility change,
  outside this driver-only lane.
- Unrelated findings become separate issues.

## Verification

1. Red/green: `cd cmd/evener-hub/frontend && npx vitest run scripts/cascadeguard/revealControl.test.js` fails on the mixed-row case with the old condition and passes after the DOM-order change (15 cases in the final file).
2. Full guard: `make build-web` then `go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v` (the documented route; `make test-web-browser` runs the same guard plus the other browser guards). A pre-fix run on this base did not fail at the branch reveal: the fold was already open there, so the old frozen anchor count reached the target through `Show N more`. It failed later in the attention journey, which does not touch `reveal`. The full guard is therefore the integration proof, not the deterministic #5000 red; the captured DOM and the seam test supply the red.
3. Captured-DOM reproduction: the real headless Chrome run against the retained `failure.html` is the deterministic mixed current/inactive case (old condition null, DOM-order fold).
4. `make build` for the Go embed of the rebuilt frontend.
5. Record exact commands, outputs, artifact paths, and any unverified limits in the PR and final report.
