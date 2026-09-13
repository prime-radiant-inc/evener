# Vision review 01 — visual craft & design-system compliance

**VISION: working.** All six images rendered and were legible. Proof-of-sight detail: in
`r3-slice1.png` the expanded "Poll the queue depth" block ends with the caption "Red marks
are the 8 the depth fuse dropped · deepest self-influence depth 4" beneath a timeline whose
right end carries a blue caret and the label "now 19:14" — only visible by looking.

Reviewer lens: hierarchy, typographic rhythm, spacing/alignment, density, colour coherence,
timeline legibility, family resemblance across row kinds, and rendering correctness.

## What each image actually shows

- **r3-slice1.png** — Top band of the round-3-fluent page: kicker "SIDEBAR WATCHES · ROUND 3,
  BUILT ON DIRECTION A", H1 "Fluent activity", intro prose, theme toggle, then the rail card
  (EVENER / LIVE, "Fix flaky sidebar tests" expanded with fold-out children) beside the
  Activity panel. The panel opens with `WATCHES 3 ARMED · 1 NEEDS A LOOK` and three watch
  rows, all with expanded bodies: note prose, a facts sentence, and (for the two timer
  watches) a dot-on-axis delivery timeline with clock-time end labels and a blue "now" caret.
  The condition watch shows the honest "no schedule to draw" line instead of a timeline.
- **r3-slice2.png** — Middle band: the panel's RUNNING group (expanded `go test` job with
  terminal output, "Audit row memo boundary" delegate row, "Ended watches (4)" collapsed
  row), then the opt-in "WATCHES WITH A COUNTDOWN" card ("in 6m" / "—") beside the
  dashed-border "WHAT THIS COSTS, HONESTLY" prose block, and the start of the rationale
  section.
- **r3-slice3.png** — Bottom band: prose only — "WHY THE EXPANDED BLOCK LOOKS LIKE THIS" and
  "DATA EACH ELEMENT NEEDS" bullet lists on empty background. No UI surfaces.
- **r3-panel-3x.png** — A 3x crop of the Activity panel column, but it frames the RUNNING
  group and the cost/honesty block, not the watch block; the Watches group is above the
  crop. Shows the terminal-output clipping and the delegate-row glyph clearly.
- **r3-rail-3x.png** — The rail column at 3x: header prose (right-edge clipped by the crop),
  the LIVE session row with summary line "2 subagents working · 1 job running · 3 watches",
  and the fold-out: job row (green dot, "running 12m"), one icon-less row ("Prune stale
  fixtures"), three watch rows with drawn clock icons and cadence/state sub-lines, and two
  collapsed disclosure rows.
- **r2-primary.png** — The rejected round-2 layout for comparison: running work first,
  watches as a group beneath, monospace `key: value` detail boxes and a per-delivery list
  instead of prose + axis timeline. Confirms round 3 is the stronger direction.

## WHAT WORKS

1. **Hierarchy lands in the right order.** In `r3-slice1.png` the eye goes first to the red
   "every 10s · 8 dropped" and the bright note prose of the expanded watch, then to the
   group header and other rows. The one watch that needs attention is unmissable without
   anything shouting.
2. **The facts sentence carries real rhythm.** "Fires every 10 seconds · armed 2h 12m ago ·
   214 deliveries, 8 dropped" with the values brightened and the connective tissue dimmed
   scans faster than round 2's `key: value` grid (compare `r2-primary.png`'s detail boxes)
   and reads as one surface with the job/delegate meta lines, which use the same
   brightened-value / mid-dot grammar. Family resemblance across row kinds is genuine:
   caret + leading icon + title + right-aligned state, note prose, facts line — jobs,
   delegates, and watches are one compositional family.
3. **The timeline is an honest, legible graphic.** Axis with real clock-time end labels
   ("17:02" / "now 19:14"), delivered/dropped encoded as neutral vs red dots, and a
   "now" caret. It draws only history — no countdown, no next-fire tick, nothing the
   runtime doesn't know. The condition watch's "There is no schedule to draw here…"
   prose is the right call and is set at the same size/ink as the facts, so the absence
   of a graphic reads as information, not as a missing element.
4. **Colour discipline is tight.** Green = running/OK, red = drops/needs-a-look, everything
   else neutral warm-gray ink. Red is never spent on decoration; the "NEEDS A LOOK" phrase
   in the Watches header and the one red watch row agree. The tinted ink (tan/gold
   durations, dimmed connective text) is used consistently in both rail and panel.
5. **Row grammar survives in the rail.** In `r3-rail-3x.png` the fold-out watch rows reuse
   the same clock icon, title + cadence/state sub-line pattern, with red reserved for the
   one degraded watch ("every 10s · 8 dropped"). The rail and panel tell the same story at
   two densities — this is what "one product surface" looks like.
6. **Terminal output stays preformatted and neutral** (per the stated rule), including the
   FAIL line — resisting the urge to splash red inside verbatim output is correct.

## WHAT DOES NOT

1. **[high] Caret state contradicts rendered state — `r3-slice1.png` (and the original
   `round-3-fluent-dark.png`), Activity panel, Watches group, rows 2 and 3.** "Deploy
   rollback check" and "Migration prints DONE" render fully expanded bodies (note, facts,
   timeline/prose) while showing **right-pointing ▸ carets**; only row 1 shows ▾. Confirmed
   on the unmodified original, so this is in the mock, not the crop. A reader cannot tell
   whether those rows are open or closed, and it breaks the caret grammar the rest of the
   UI (rail disclosures, "Ended watches (4)") follows. **Fix:** render ▾ on every row whose
   body is shown; if the intent was "all expanded for the mock," all three carets must be ▾.
2. **[high] Terminal output clips at the card's right edge — `r3-panel-3x.png` /
   `r3-slice2.png`, RUNNING group, expanded `go test` row.** The durations on the two
   `ok primeradiant.com/...` lines ("8.4…", "1.20…") are cut by the card border with no
   fade, ellipsis, or scroll affordance. Also confirmed on the original. This is the kind
   of clipping that makes a surface read as unfinished. **Fix:** give the pre block
   horizontal scroll or right padding, or truncate the package path (middle-ellipsis) so
   the duration column always fits.
3. **[medium] The delegate-row leading glyph reads as a missing-glyph box —
   `r3-panel-3x.png`, "Audit row memo boundary" row.** The small green glyph before the
   title renders as a cramped asterisk/sparkle that at 1x is ambiguous with tofu. The
   design system already solved this for watches ("a drawn clock, not a typed glyph", per
   the round-2 notes in `r2-primary.png`). **Fix:** use a drawn icon for delegates at the
   same optical size and stroke as the clock icons, or drop the glyph and let the green `$`
   and clock icons own the leading slot.
4. **[medium] Timeline dot counts don't reconcile with the caption — `r3-slice1.png`,
   "Poll the queue depth" expanded block.** The caption says "Red marks are the 8 the depth
   fuse dropped", but only **2 red marks** are drawn, among ~8 dots total, while the facts
   line claims "214 deliveries". If the axis is a bounded ring of recent deliveries, the
   caption currently implies all 8 drops are shown and the dot count implies ~8 deliveries
   total — both contradict the numbers two lines up. This quietly violates the design's own
   "every mark is an event that actually happened" honesty rule. **Fix:** reword to
   "red marks are drops — 8 dropped overall" (and/or caption the window, e.g. "last 8
   deliveries"), so marks and prose never disagree.
5. **[medium] "Prune stale fixtures" rail row breaks the leading-slot grammar —
   `r3-rail-3x.png`, fold-out.** Every sibling row has a leading marker (green dot, clock
   icon, ▸); this row has none, so its title floats one icon-width right of the others and
   its status is unencoded — yet it still carries a right-aligned "13m". **Fix:** give it
   the marker its state deserves (dim dot for idle/done), keeping the leading column
   occupied on every row.
6. **[low] The "now" caret's blue is a new, singleton hue — `r3-slice1.png`, both
   timelines.** The palette is green/red/neutral; the thin blue caret is the only blue
   element in either surface and reads slightly like a text cursor. **Fix:** render "now"
   in the primary ink (or the tan timestamp tint) as a slightly heavier tick, keeping hue
   count at two accents.
7. **[low] The opt-in countdown card adds a third accent — `r3-slice2.png`, "WATCHES WITH
   A COUNTDOWN" card.** "in 6m" is set in a warm olive that appears nowhere else; the em
   dash for the condition watch is correct and honest. **Fix:** if this shape ships, put
   "in 6m" in the existing tan duration tint.
8. **[low] Watches group header does double duty — `r3-slice1.png`, "WATCHES 3 ARMED · 1
   NEEDS A LOOK".** Cramming a status sentence into an all-caps eyebrow label makes the
   longest header on the page and mixes two type roles; round 2's separate red "NEEDS A
   LOOK" group header was clearer. **Fix:** keep the header to "WATCHES 3" and let the one
   red row carry the attention signal (it already does), or split the abnormal watch into
   its own group as round 2 did.
9. **[low] All three watch rows expanded at once** inflates the panel so RUNNING starts far
   below the fold (`r3-slice1.png` bottom). Presumably a mock convenience, but if the
   shipped default expands more than one row, the panel's top is consumed by history rather
   than live work. **Fix:** default to one expanded row (the needs-a-look one).
10. **[low] Panel crop artifact:** `r3-panel-3x.png` is labeled as containing the watch
    block but actually frames the RUNNING group and cost block; reviewers should rely on
    `r3-slice1.png` for the Watches group at 2x. Not a design defect — noted so the panel
    doesn't grade the wrong crop.

## Verdict

**Fit to ship as the fluent activity panel, with two blocking fixes.** The design language is
the strongest of the rounds: the prose-plus-axis expanded block is a real improvement over
round 2's definition lists, the colour discipline and cross-surface row grammar are coherent,
and the surface never implies data the runtime lacks — no countdown, no next-fire, no drop
counts invented. It looks like a finished product surface in composition, but two rendering
defects read as wireframe sloppiness and must be fixed before this is the shipped mock: the
**caret/state contradiction on watch rows 2–3 (finding 1)** and the **right-edge clipping of
terminal output (finding 2)**. Finding 4 (timeline marks vs. caption arithmetic) should be
fixed in the same pass because it undercuts the honesty the design is built on. Everything
else is polish.
