# Issue #4412: activity-tree intent geometry guard

## Status and authority

Frozen for PAR before implementation. The requested task supplies approval for
this browser-coverage-only change. No product, stylesheet, widget, navigation,
state, or recovery behavior changes.

Base: `7f8fb3ba706c875861658f75c2d0bdb909eab8ce`.
Lane: `issue-4412-activity-geometry` in the isolated delegated worktree.

Read: `AGENTS.md`, the product guide and S02 browser-workspace row,
`docs/developing-evener/testing.md` (including The Browser Guards), both design
system guides, `ActivityTree.tsx`, `activitypanel.module.css`, OpenButton and
OpenTranscriptButton, the layoutguard runner and responsive/Open hit-target
fixtures, and the frontend changes merged in #4374.

Issue #160 remains open as an umbrella. Its widthMatrix support already landed,
so this case uses the existing runner without waiting for that issue to close.

## Fixture and scope

Add one case, `activity-tree-intent-geometry`, under
`cmd/evener-hub/frontend/scripts/layoutguard/cases/`, containing `case.json`,
`harness.html`, and `assert.mjs`. Keep all existing cases and assertions intact.
No ActivityTree unit fixture change is needed: this is a static CSS geometry
fixture, not a second renderer or a test of activity transport.

Load the real global/tokens, Dialog/Sheet, activity-panel, Button, IconButton,
OpenButton, and SubagentDot styles. Import StatusDot as a real CSS Module in
each iframe, preserving its class namespace rather than flattening its
`.danger` together with Button's into the runner's concatenated stylesheet.
Require the live dot's visible 8px box so a failed module load cannot pass.
Reproduce the current production hierarchy:
the Sheet panel and padded body, then `activityPanel > panelColumn > tree`,
a live delegate row, then each child row in a nested
`role=group` / `.indentGuide`. Each dense row has the disclosure control,
`.denseKind` containing the fresh SubagentDot, `.denseName.denseNameLive`, the
OpenButton shell and icon button, then `.denseMeta` with its segment spans.
The Open control stays before the meta and beside the name, as in DenseRowView.

Use the short delegate name `inspect` and a deterministic 200-ASCII-character
intent. This is the near-limit scenario called out in #4374's source pin, not
a copy assertion. Include token and quiet-age segments after the intent, as
production does. Every measured row carries the same pressure. Use distinct
machine IDs to identify row activation; do not use human-readable copy as the
oracle. Disable transitions and animations in the fixture, and wait for the
runner's real bundled-font readiness before measurement.

## Phone-width and depth matrix

Use case.json's existing widthMatrix to create these fixture panes:

| Phone viewport, CSS px | Tree content width | Deepest nested groups | Rows measured |
| --- | --- | --- | --- |
| 360 | 328 | 0 | root |
| 390 | 358 | 2 | root through depth 2 |
| 430 | 398 | 4 | root through depth 4 |

The outer document has a 1200px-wide desktop viewport to fit all fixture panes;
each same-origin iframe has the table's actual phone viewport width, enabling
the real phone-width media queries and 44px Open target. The panes have no
invented inline row sizing, padding reductions, flex overrides, or name minima.
The real Sheet body contributes its 16px inset on each side. Assert the table's
tree content widths so omitting that inset cannot produce a false pass.
Use one markup builder across widths and depths. Measure each iframe's realized
width, each row's aria level and actual `.indentGuide` ancestor count so a lost
width or lost nesting cannot silently turn the hard scenario into an easy one.

This is deliberately not a Cartesian sweep of every depth at every width. An
exploratory real-Chrome measurement of unchanged CSS found name starvation
outside this envelope. PAR then exposed the Sheet body's additional 32px inset:
at an actual 320px phone viewport even the root name has only 5.65625px, and
at 390px/depth 3 only 8.8125px. That separate product limitation is filed as
[#4881](https://github.com/prime-radiant-inc/evener/issues/4881). This coverage-only
change must not fix it, accept it as readable, or weaken assertions to pass it.
The smallest guarded phone viewport is therefore 360px, and deep nesting is
guarded through four groups at 430px. Actual 320px Sheet readability and deeper
combinations remain explicitly unverified and blocked on that product issue.

## Geometry assertions

Use `getBoundingClientRect`, real text metrics, scroll/client widths, computed
visibility, and point hit-testing. Allow at most 0.5 CSS px for containment and
rounding, never for a zero-width result.

For all 9 rows:

1. The name has width at least **16 CSS px** and at least the independently
   measured advance of its first character plus an ellipsis when truncated.
   Measure these text advances using the name's actual font. This proves the
   box can paint a real identifying glyph beside its ellipsis, not merely a
   non-zero sliver or an ellipsis alone. A fully fitting name may use its natural
   text width instead. The 16px floor is independent of the protected 60% rule.
2. Name, Open button, and meta have non-zero height and are contained within
   the row and pane, with no horizontal overlap. The name and icon/control are
   visible, not display:none, visibility:hidden, or opacity:0.
3. The Open button retains its **44px phone hit width**, within 0.5px, and its
   own row's vertical band. Its box must fit inside the iframe viewport.
4. The intent is genuinely constrained: meta.scrollWidth exceeds
   meta.clientWidth, the meta's box is at most **60% of the row content width**
   plus 0.5px, and overflow is clipped with ellipsis rather than spilling over
   the control. Require single-line meta geometry and the existing ellipsis
   treatment; do not classify deliberate clipping as horizontal scrolling.
5. At 430px the root name fits its full natural text width, within 0.5px. This
   complements the pressure cases with a stronger ordinary-width assertion.

Failures name the width, depth, element and measured/required values. A passing
summary includes the rows covered and minimum measured name/control width.

## Clickability check

For every row, scroll the fixture row into view if necessary. Probe the Open
button center plus inset left/right/top/bottom points with the iframe's
`elementFromPoint`. Every point must resolve to that row's own button or a
contained icon node, never a neighbor, the disclosure, or clipped content.
Require the button to be enabled and visibly painted.

After verifying that the center hit's closest button is the expected row's own
button, activate that HTMLButtonElement with its DOM click method. The center
can hit an SVG path, which has no click method of its own. Record a row-specific
Open callback once, with no disclosure callback. This deliberately
checks static-fixture hit reachability and enabled DOM activation, not trusted
pointer synthesis, production pane navigation, a hub, or an actual transcript
load. Those are not the CSS contract under test.

## Red-first mutation and preservation

After adding the case, remove only `.denseMeta`'s `max-width: 60%` declaration
locally. Run this single case and require a non-zero exit naming name starvation
or excessive meta share. Restore the stylesheet exactly, verify it has no diff,
then rerun successfully. The mutation must never be staged or committed. Record
the observed failure in case.json's mutation evidence.

Do not weaken existing source pins or browser assertions. Do not add a widget,
new label, Go harness, or new product surface. The real stylesheets and their
owners remain unchanged, so evergreen product documentation needs no amendment.
The fixture makes no persistence changes and requires no new recovery owner.

## Verification

From `cmd/evener-hub/frontend`, with a task-private cache path beneath
`$EVENER_SCRATCH_DIR`:

```sh
BROWSER_GUARD_VITE_CACHE_DIR="$EVENER_SCRATCH_DIR/vite-cache" \
  npm run layoutguard -- activity-tree-intent-geometry
```

Run the restored case twice. Run `npm run typecheck` from the same directory.
Because no Go harness code changes, `make build` is not required. Do not run
`make test-web-browser` or the full suite locally; existing guards and the full
required surface remain CI's source of truth. Never run npm ci through the
lane's symlinked node_modules; its shared install was checked against an
identical package-lock.json before linking.

Freeze and PAR this specification with two independent competing reviewers.
After implementation, PAR the immutable base/head diff including new files,
run simplify-code, commit and run branch RoboRev review, then push, open the
PR against main and shepherd it. Merge a moved origin/main before the final
review/approval round. Admin squash-merge only when current-head required
checks are green and every RoboRev finding is low or conclusively refuted.
Report the exact commands, mutation failure, restored runs, thresholds,
fixture matrix, PR URL, merge SHA, review dispositions and unverified scope.
