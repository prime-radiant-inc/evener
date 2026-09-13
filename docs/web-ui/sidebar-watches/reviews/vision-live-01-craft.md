# Vision Live Review 01 — Visual Craft & Design-System Compliance

**VISION: working.** All three screenshots rendered with readable pixels. Proof string read
directly off `live-panel-2x.png`: "Fires every 1m · armed 34m18s ago · 34 deliveries · last at
00:11".

Reviewer lens: visual craft and design-system compliance, judging the **live app**, not the
mockup.

## What is actually visible

- **live-full.png (1440×940):** Three-column app. Left rail with `LIVE` and `PROJECTS`
  sections, both listing the session "Arm Watches For UI Review" with a `2 watches` count in
  the subtitle. Centre transcript with three "Timer fired · every 1m" turns. Right Activity
  panel with a "Watches / 2 armed" group containing both watches ("Migration prints DONE",
  "Hourly sweep" with the dot timeline) sitting above the running job rows and a collapsed
  "2 inactive · 1 failed" row.
- **live-panel-2x.png:** The Activity panel at 2x. Both watch rows expanded, each with a
  chevron + clock icon, bold title, right-aligned metadata ("on output · armed" / "every 1m ·
  34 fires"), and an indented drawer hanging off a thin left guide rule. The Hourly sweep
  drawer contains the 32-dot timeline with `23:40` … `now 00:11` anchors and the caption
  "Last 32 of 34 deliveries". Below the watches, the tick-loop job row shares the identical
  row grammar (chevron, icon, bold title, right metadata, drawer with monospace command).
- **live-rail-3x.png:** The rail at 3x. Session rows carry "2 watches" as the **first** token
  of the subtitle ("2 watches · demo · 1 job running · m…" in LIVE; "2 watches · 1 job
  running · main" under the demo project). `1m` timestamp right-aligned. No tree/branch line
  is drawn; hierarchy is indentation only.

## WHAT WORKS

1. **Row grammar is a real family.** Watch rows and job rows share chevron, icon, bold title,
   right-aligned metadata, and the guide-ruled drawer. The watches do not look bolted on;
   they read as siblings of the job rows below them. (live-panel-2x, whole panel.)
2. **Neutral ink in the watch block.** No red, no danger tint, no tinted band, no nested
   box anywhere in the watch group. Nesting is conveyed by indentation + a hairline rule
   only — exactly the restraint the design rules call for.
3. **The timeline reads as a timeline.** Evenly spaced dots, a terminal "now" tick at the
   right, `23:40` / `now 00:11` anchors on the same baseline, and the caption "Last 32 of 34
   deliveries" set as part of the composition directly beneath. It is the only graphic
   element, as specified. (live-panel-2x, Hourly sweep drawer.)
4. **Count placement obeys the rule.** In the rail, "2 watches" leads the subtitle, so when
   the LIVE row runs out of width it is the trailing branch token ("main" → "m…") that gets
   ellipsized, never the count. This is the specified behaviour working under real
   truncation. (live-rail-3x, LIVE row.)
5. **Honest empty state.** The output watch's "There is no schedule to draw here — this one
   fires when the job says the word, not when a clock says so." replaces the timeline with
   one prose line. No fake empty graphic, no dashed placeholder box. (live-panel-2x,
   Migration drawer.)
6. **No forbidden data.** No drop counts and no next-fire time or countdown anywhere in the
   watch UI. The facts lines carry only what the data can know.

## WHAT DOES NOT

1. **Duplicated watch title inside its own drawer — HIGH.**
   *live-panel-2x.png, both watch drawers.* Each expanded watch repeats its name as the
   first drawer line: the bold row title "Hourly sweep" is immediately followed by a second
   "Hourly sweep" one line below; same for "Migration prints DONE". It reads as a rendering
   bug (double title), burns vertical space, and breaks the drawer's information hierarchy —
   the eye hits the same string twice before reaching the facts.
   **Fix:** drop the repeated name line in the drawer; the row title is the title. Start the
   drawer on the facts line.

2. **"1 failed" in red inside the same panel — MEDIUM.**
   *live-full.png and live-panel-2x.png, collapsed jobs row at the bottom of the Activity
   panel.* "1 failed" is rendered in red — the only red ink in the UI. The design rule for
   this surface is neutral ink only. This row belongs to the jobs list rather than the watch
   block, so it may be pre-existing, but it sits directly beneath the watches in the same
   neutral panel and the tint pulls the eye to a collapsed, non-actionable summary row —
   the loudest ink on the least important element.
   **Fix:** render the failed count in the same low ink as "2 inactive" (weight or a neutral
   glyph can carry the emphasis); if red is intentional for jobs, scope it so it never
   shares a panel with the neutral watch block.

3. **Timeline dot gradient appears to fade toward "now" — MEDIUM.**
   *live-panel-2x.png, Hourly sweep timeline.* At 2x the leftmost (oldest) dots render
   slightly larger/darker and the dots grow smaller/lighter toward the right, so the faintest
   dots sit next to the "now" marker. That inverts the expected emphasis: recency should be
   the most legible end. (At 1x in live-full.png the gradient is nearly invisible, which
   itself suggests the contrast range is too subtle to carry meaning.)
   **Fix:** if a recency gradient is intended, reverse it so the newest dots are darkest, and
   widen the contrast enough to read at 1x; otherwise make all dots uniform and let the
   "now" tick alone mark the present.

4. **Stray "— · 2m" on the job row — LOW.**
   *live-panel-2x.png, tick-loop job row, right metadata.* A bare em-dash before the
   timestamp ("— · 2m") looks like an empty placeholder that failed to render a value.
   Outside the watch block but inside the same list family, so it degrades the shared row
   grammar.
   **Fix:** suppress the dash segment when its value is empty rather than rendering a lone
   separator.

5. **One-character ellipsis truncation in the rail — LOW.**
   *live-rail-3x.png, LIVE session row subtitle.* The subtitle truncates to "… · m…" — a
   single surviving character plus ellipsis. That is worse than dropping the token entirely:
   it reads as a glitch, not an abbreviation. The count itself is safe (see Works #4), so
   this is purely about truncation polish.
   **Fix:** truncate at token boundaries — if "main" cannot fit whole, omit it and end at
   "1 job running …" or just "1 job running".

6. **Low-ink gray on warm beige is borderline for contrast — LOW.**
   *live-rail-3x.png, section headers, subtitle fragments, `1m` timestamps; live-panel-2x,
   timeline anchors and caption.* The muted grays ("2 watches ·", "1m", "23:40",
   "Last 32 of 34 deliveries") sit at the edge of comfortable legibility against the cream
   background, and the panel captions are the smallest, lightest text in the composition.
   **Fix:** step the secondary ink one shade darker; verify the timeline anchors and caption
   against WCAG AA for small text.

7. **Long job ID dominates the output watch's facts line — LOW.**
   *live-panel-2x.png, Migration drawer.* "Waiting on job_034NnSnRLFjNWPnAO6mLLk_
   jHtbABK0DJdv, matching NEVERMATCHESXYZ …" lets a 34-character machine hash own the line
   and forces a mid-phrase wrap. The human-meaningful token (the match pattern) is pushed to
   line two.
   **Fix:** truncate the job id with a middle ellipsis (e.g. `job_034N…ABK0DJdv`) so the
   pattern and the armed/no-deliveries state stay on the first line.

8. **Duplicated Activity panel title (app chrome, noted for completeness) — LOW.**
   *live-full.png, top of right panel.* The tab bar reads "Activity · Arm Watches For UI
   Review ×" and the panel's own bold heading directly below repeats "Activity · Arm Watches
   For UI Review" verbatim. Same double-title smell as finding 1, one level up. Not part of
   the watch block, but it is the first thing the eye meets in the panel.
   **Fix:** keep the tab label, shorten the in-panel heading (e.g. just "Activity").

Also observed, outside this feature's scope but visible in the shots: the centre
transcript's first line is clipped beneath the sticky header (live-full.png), and the tick
job's `$` icon is green while the watch clock icons are neutral — a minor icon-ink mismatch
within the shared row family.

## VERDICT

**Not quite fit to ship — one blocking craft fix, then yes.** The structural design is
right: the watch rows genuinely read as family with the job rows, the timeline is a composed
graphic with its caption rather than a decoration, the empty state is honest prose, and
every stated data rule (no drop counts, no next-fire, count-before-branch, neutral watch
ink, no nested boxes) holds in the live render. But the **duplicated watch title inside each
drawer (finding 1)** makes the feature look unfinished — it is the kind of repetition a
user reads as a bug — and the **red "1 failed" (finding 2)** breaks the neutral-ink rule on
the very surface the rule is meant to protect. Fix those two, take finding 3 (gradient
direction) and finding 5 (truncation) as same-release polish, and this is a finished
product surface rather than a wireframe.
