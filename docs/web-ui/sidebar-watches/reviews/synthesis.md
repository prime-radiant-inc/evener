# Review synthesis (round 1 → round 2)

Four independent reviewers each read all three mockups plus the real rail/activity
source. Vision was unavailable to every reviewer, so all four reviewed the
HTML/CSS and code, not the PNGs. That is a real limitation: nothing below is an
eyeball judgment about rendered polish.

## Rankings

| Lens | 1st | 2nd | 3rd |
| --- | --- | --- | --- |
| Triage / information design | **B** | A | C |
| Interaction / a11y / edge cases | **B** | A | C |
| Visual craft / design-system | **A** | B | C |
| Feasibility / data model | **A** | B | C |

Every lens rejects **C**. A and B split — A wins on craft and cost, B on
hierarchy and honesty. That split is what round 2 resolves.

## Findings the reviewers agreed on, and I verified

1. **No watch data reaches the frontend today.** Verified: the only
   watch-adjacent wire fields are `fromWatch` (jobs) and `parentWatchGranted`
   (delegates). Everything else is plumbing.
2. **The facts the mockups lean on are not exposed.** Verified in
   `agent/internal/jobstore/record.go` and `agent/doctor/watches.go`:
   - `WatchRecord` (the folded registry) carries no `Note` and no cadence. Both
     live in `WatchConfigSnapshot` (`OutputMatch`, `ProgressIntervalMS`,
     `Events`, `Every`, `AfterSeconds`, `RepeatSeconds`, `Note`), which rides the
     registration event's `Config` and is *not* retained by `FoldWatches`.
   - `WatchView` exposes no `Note` and no cadence either.
   - No next-fire or previous-fire instant exists anywhere (`rg` for
     `next_fire|fire_at|nextfire` finds only a function name, not a field).
   - `DeliveryView` carries no timestamp, so the mockups' `19:10:02` delivery
     times are unbacked.
3. **The summary-line coverage hole.** `RailRow.tsx` renders the gloss only when
   `showsActivity || showsProject`, and `showsActivity` is
   `showsGloss || hasWorkingDescendants || hasRunningJobs`. A session whose only
   pending work is an armed watch would show nothing. Every mockup assumed this
   away. This must be decided explicitly.
4. **Truncation order.** `activityGloss` puts the branch last so it is the
   ellipsis sacrifice. A watch segment appended after the job count is safe only
   if it is inserted *before* the branch; the mockups did not show a branch.
5. **Watches spent a hue they must not.** In A and B the watch segment sits
   inside `.rail-gloss--alive` and inherits `--alive`; in C the selected row has
   a static `--accent` outline. Both violate the four-hue one-meaning rule
   (`docs/web-ui/design-system.md:446`, `tokens.css`).
6. **C violates stated design-system rules.** Verified:
   `design-system.md:155` bans "speculative progress bars" (the `.track`);
   `:142` bans "nested boxes and tinted bands" (the Armed band + cards); the
   ticking `next in 4m` countdown conflicts with the "no idle motion" law
   (`:527`, `:541`), and only the activity panel has a tick provider — the rail
   row is deliberately clock-free.
7. **The watch glyph is not in the app's fonts.** Both `@font-face` blocks in
   `styles/global.css` declare a `unicode-range` that stops at U+2215, so `◷`
   (U+25F7) and `↻` (U+21BB) fall back to a system font, and the rail's
   accessible name is name-from-content, so a screen reader would announce the
   glyph character, not the word "watch". Existing precedent is an SVG glyph
   (Chevron) or `role="img" aria-label`.
8. **Hand-rolled rows break reachability.** C's cards are plain divs with no
   role, tabindex, or handler; its cadence ticks and progress track are
   `title`-only on non-focusable spans. Watch rows must be real `treeitem`s
   (panel) / rail rows (rail), inheriting the existing key handling.
9. **"Ended watches (N)" would be a third fold** on a session and needs a new
   `RailNode` kind; only B proposed it, and only B gave ended watches any
   surface at all.
10. **Feasibility: C's headline visual is unbuildable.** Verified: no next-fire
    instant is tracked (the runtime holds a `time.NewTicker` per watch, not a
    schedule), no previous-fire instant is kept live, and there is no TTL or
    duration field at all — so the countdown, the cadence ticks, and the
    `↻30m ×6h` claim cannot be made honest. B's soonest-first ordering depends on
    the same missing next-fire key; `evener/jobs/list` has no watch kind, so
    interleaving watches with jobs is a two-source merge with no shared sort.
11. **The one live watch projection already exists.** `jobManager.liveWatchSummaries()`
    returns `{id, source, condition, deliveries, createdAt}` per visible watch,
    and `recentWatchEntry` carries `{end_reason, ended_at}` for ended ones;
    cadence and note are already inside `watchConditionSummary`'s prose. Nothing
    on the hub wire uses them yet, so the work is projection, not invention.

## Round-2 decision

Build on A's rail and B's honesty, on top of the existing live projection:
**watch rows** (see `round-2-primary.html`) with **watches first** as the
alternative (`round-2-variant.html`). Both drop C's countdown, ticks, progress
track, card band, and summary-line regression.

## What that means for round 2

Keep from A: the one-line rail row, the neutral count, no new grammar, watches
grouped distinctly in the panel.
Keep from B: honest `armed` for conditions, no fabricated countdown, an
ended-watch surface, and the insight that runaway drops is the one watch state
that belongs on a triage surface.
Drop from C: the card band, the progress track, the ticking countdown, and the
summary-line regression.

Also required regardless of direction (the reviewers' shared round-2 list):

- Decide the coverage hole: watches must earn a second line on an otherwise-quiet
  row, or the count silently fails on exactly the sessions where a watch is the
  only thing happening.
- Neutral ink for every watch element; danger only for runaway drops.
- Insert the watch segment before `branch` in `activityGloss`, and unit-test the
  join order.
- SVG watch glyph (or `role="img"` + `aria-label`), never a bare text glyph.
- Watch rows as first-class `ActivityRow` / rail children, not ad-hoc spans.
- Copy vocabulary with nouns: `N watches`, `N armed`, `N needs a look`; the
  panel header must stop meaning "running only".
- Zero and many states: hide at 0, cap and fold at many.
- Only claim what the wire can carry, or add the projection.
