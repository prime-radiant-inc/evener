# Vision review 03 — adversarial lens, live hub

**VISION: working.** Read off the pixels: the string "There is no schedule to
draw here — this one fires when the job says the word, not when a clock says
so." is rendered verbatim in the Migration prints DONE block
(live-panel-2x.png), matching `WATCH_NO_SCHEDULE_LINE` in
ActivityRowDetail.tsx:49-50.

Panelist 3 of 4. Lens: adversarial — what breaks, misleads, or embarrasses
this surface in real use. Screenshots are of the real hub running this branch,
not mockups.

## What is visible per image

### live-full.png (whole app, 1440x940)

Three-pane app. Left rail: `LIVE` section with session row "Arm Watches For UI
Review ›" (timestamp "1m"), subline "2 watches · demo · 1 job running · m…"
(visibly truncated); `PROJECTS` section with "demo ▾" (badge "1") and the same
session with subline "2 watches · 1 job running · main" (fully visible).
Center: transcript for the session. Right: Activity panel with group header
"Watches … 2 armed", two expanded watch blocks, then a job row ("Tick loop…",
`running 2m · 7b · started 00:08`), then a collapsed fold "2 inactive ·
1 failed" (the only red ink anywhere in the shot).

Watch block 1: "▾ ⏱ Migration prints DONE … on output · armed"; facts "Waiting
on job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv, matching NEVERMATCHESXYZ · armed
2m42s ago · no deliveries yet"; then the no-schedule sentence.

Watch block 2: "▾ ⏱ Hourly sweep … every 1m · 34 fires"; facts "Fires every
1m · armed 34m18s ago · 34 deliveries · last at 00:11"; a timeline of ~32 dots
with left label "23:40" and right label "now 00:11"; caption "Last 32 of 34
deliveries".

### live-panel-2x.png (Activity panel at 2x)

Same panel, legible. Notable at this zoom: the long job id wraps cleanly to a
second line (no clipping, no ellipsis); the facts lines, note, and captions
share one left indent; the dot row is inset slightly from the facts text and
its right end does not line up exactly with the right-aligned "now 00:11"
label; the rightmost timeline mark is a solid vertical "now" bar that touches
or overlaps the final dot; the faintest text is the job meta "running 2m · 7b
· started 00:08".

### live-rail-3x.png (rail column at 3x)

The LIVE row's subline truncates the branch to "m…" while the identical
session under PROJECTS shows "main" in full. The LIVE row is squeezed by the
"1m" timestamp. The orange "1" badge on the "demo" project row is a session
count chip, not a watch count.

## WHAT WORKS

### W1. Long content does not break the 532px panel — explicitly fine

The worst realistic input — a 42-character job id plus a 15-character output
pattern in one facts sentence — wraps mid-token to a second line with no
clipping and no ellipsis (live-panel-2x.png, Migration prints DONE facts).
Source corroboration: `.watchFacts` and `.watchNote` both carry
`overflow-wrap: anywhere` (activitypanel.module.css:283-293). The same
property is on the no-schedule line and the caption, so a long note or a long
pattern degrades to taller text, never to a lie. I tried to break this and
could not.

### W2. The honesty rules hold on screen

No drop counts, no next-fire instant, no countdown anywhere on the surface.
The no-schedule sentence is verbatim the source string
(ActivityRowDetail.tsx:49-50). Ink discipline: the only red in all three shots
is "1 failed" in the jobs fold — a different surface with its own semantics.
The watch surface is neutral: the glyph, dots, and the now bar are
`--ink-mid`/`--ink-low` (activitypanel.module.css:319-337). No nested boxes or
tinted bands: the detail strip is a hairline left rule, not a card.

### W3. The faintest text still clears WCAG AA — verified numerically

The lowest-ink strings on the surface (timeline labels, "Last 32 of 34
deliveries", the no-schedule sentence) are `--ink-low` #6D6D64
(tokens.css:244). Computed contrast against the light theme's surfaces:
4.70:1 on #F4F3EF, 4.96:1 on #FAF9F5 — above the 4.5:1 AA floor for normal
text. The `--ink-mid` facts line is 5.80:1 or better. The caption tier looked
alarmingly faint in the screenshot; the numbers say it passes.

### W4. The accessible story is coherent

The watch row's accessible name is "Watch: \<note\>" via `aria-label`
(ActivityTree.tsx:534,547), the chevron is a real button with its own
"Show/Hide details for Watch: …" label and `aria-expanded`
(ActivityTree.tsx:556-566), and the timeline is `role="img"` with an
`aria-label` that restates the caption and the window ("Last 32 of 34
deliveries, 23:40 to now 00:11", ActivityRowDetail.tsx:79-84) while the dots
and bar are `aria-hidden`. A non-sighted user loses nothing the dots carry.
The toggle is discoverable: the whole row clicks, the chevron is visible at
rest in all three shots.

### W5. The two armed counts cannot drift

The panel header's "2 armed" and the rail's "2 watches" are the same predicate
run twice, not two implementations: `activeWatchCount` delegates to
`armedWatchCount` (railNodes.ts:339-348), and ActivityTree.tsx:789 uses
`armedWatchCount` directly. Both shots show 2 and 2.

### W6. The capped caption tells the truth about the cap

"Last 32 of 34 deliveries" is rendered exactly when the retained ring is
smaller than the delivery count (ActivityRowDetail.tsx:73-76), and only
retained instants are drawn. The graphic does not pretend to 34.

## WHAT DOES NOT

### F1. The "now" bar and the last delivery dot merge into one mark — medium

**Evidence:** live-panel-2x.png — the rightmost timeline mark is a solid
vertical bar that touches/overlaps the final dot; at 1x (live-full.png) they
are one blob. **Source:** the now bar is pinned at `left: 100%`
(ActivityRowDetail.tsx:95-100) and the last dot's position is
`(millis − earliest) / (now − earliest)` (ActivityRowDetail.tsx:65-70), so any
delivery within the last ~1% of the span lands on top of the bar. In this
snapshot the 00:11 delivery and "now 00:11" coincide, so the collision is
honest here — but a viewer cannot distinguish "a delivery just fired" from
"that is just the now marker", and the bar is taller than the dots, so the
merged mark reads as "the bar", erasing the most recent delivery — the one
fact a person checks this graphic for. The general case is worse: dots have no
collision handling at all, so any two deliveries closer than ~1.5% of the span
(a burst, or a sub-minute cadence) smear into each other while the caption
still claims the full count.

**Fix:** clamp dot positions to a maximum of ~97-98% so the now bar always
stands alone in reserved space (the rail already has a 6px inline margin to
absorb overhang, activitypanel.module.css:307-310), and document that dense
deliveries may overlap — or round positions apart by a minimum pixel spacing
when they collide.

### F2. An armed output watch that will never fire is indistinguishable from a healthy one — medium

**Evidence:** live-panel-2x.png — "Waiting on
job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv, matching NEVERMATCHESXYZ · armed
2m42s ago · no deliveries yet". The block looks exactly the same whether the
target job is running (it is, luckily — the tick-loop row sits below it by
coincidence of panel ordering, not by design) or died an hour ago, or the
pattern can never match. "armed" only asserts the watch object is active; it
says nothing about the target's liveness. `watchFacts`
(activityRows.ts:177-179) interpolates the target id but never its state. This
is the one place the surface can mislead by omission in real use: the watch
that has silently gone orphaned is precisely the watch a user opens this panel
to check on.

**Fix:** when the target is a job in the same session's activity data (it is
right there in the panel), append its liveness to the facts line — "waiting on
job_034… (running)" vs "(exited)" — so a dead target is visible without
cross-referencing rows by eye. If the wire doesn't carry target state, this is
a projection gap worth closing rather than a copy fix.

### F3. One number, two nouns; one count, two phrasings — low

**Evidence:** live-panel-2x.png — the Hourly sweep row meta says "every 1m ·
34 fires" while its own facts line, four centimetres away, says "34
deliveries", and the caption says "deliveries" again. Meanwhile the group
header says "2 armed" and the rail row for the same session says "2 watches"
(live-rail-3x.png). **Source:** `watchMeta` uses "fire/fires"
(activityRows.ts:168); `watchFacts` and the caption use "delivery/deliveries"
(activityRows.ts:193, ActivityRowDetail.tsx:76); the rail formats "N watches"
(RailRow.tsx:634). Same predicate, same counts — but "34 fires" next to "34
deliveries" invites "are these different counters?", and "2 armed" vs
"2 watches" invites "is one of these a subset?".

**Fix:** pick one noun for the delivery count ("deliveries" — it already
dominates) and use it in `watchMeta`; consider "2 armed watches" in the group
header so the header and the rail obviously count the same thing.

### F4. The expanded detail repeats the row name verbatim — low

**Evidence:** live-panel-2x.png — row "Hourly sweep", then the detail's first
line is "Hourly sweep" again; row "Migration prints DONE", detail first line
"Migration prints DONE". **Source:** the row name is `watchName(watch)` =
trimmed note (activityRows.ts:96-98), and the detail prints the same note
again as its first element (ActivityRowDetail.tsx:323-327). Whenever the note
is the name — the common case — the detail opens with a redundant line, and
the one identifier the row never shows (the watch id) is shown nowhere.

**Fix:** when the note equals the row name, skip the repeated note line or
replace it with the watch id, so expansion always adds information.

### F5. The timeline's left endpoint invites a "this is the whole history" reading — low

**Evidence:** live-panel-2x.png — "23:40" under the left end, "now 00:11"
under the right. But the watch was armed 34m18s ago (~23:37) and 2 deliveries
precede the drawn window; "23:40" is the earliest *retained* instant, not the
start. The caption "Last 32 of 34 deliveries" mitigates this, but the two
endpoint labels frame the graphic as a complete span from start to now, which
is the one thing it is not. **Source:** the start label is
`clockFromMillis(earliest)` over the retained instants
(ActivityRowDetail.tsx:71,103).

**Fix:** fold the window into the caption ("Last 32 of 34 deliveries,
23:40–00:11") and drop the left endpoint label, or keep the label and accept
the ambiguity — but the current pairing (bare clock + capped caption) is the
most misleading arrangement of the three.

### F6. Rail branch truncates to a one-character fragment "m…" — nit, adjacent surface

**Evidence:** live-rail-3x.png — LIVE row subline "2 watches · demo · 1 job
running · m…" while the same session under PROJECTS shows "main" in full.
**Source:** the branch is the documented "deliberate ellipsis sacrifice"
(RailRow.tsx:626-627), which is a defensible priority order — but a one
character plus ellipsis fragment carries zero information and reads as a
rendering bug next to the untruncated twin row below it.

**Fix:** when a trailing token fits fewer than ~4 characters, drop the whole
token rather than printing a fragment. Out of the watch surface proper;
noted because it sits in the same screenshot and the same second-line grammar
that carries the watch count.

## Notes on the axes I could not break

- **Wrapping/clipping at 532px and below:** facts, note, no-schedule line, and
  caption all wrap via `overflow-wrap: anywhere`; the timeline is
  percentage-positioned and fluid, so 1024px and the mobile sheet narrow it
  without clipping. The only width-dependent risk is dot overlap under bursty
  deliveries (F1), which is density-driven, not width-driven.
- **Red/danger tint:** none on the watch surface, verified in all three shots.
- **Contrast:** verified numerically (W3); what looks faint at a glance still
  passes AA.
- **Snapshot honesty:** "now 00:11" is recomputed from the ticking `now`
  (ActivityTree.tsx:538-539, TreeTickProvider), not a frozen string; the
  armed-age figures differing from the brief's (34m18s vs 34m7s) are just
  screenshot timing, not a defect.
- **Accessibility:** row name, toggle, and timeline aria-label are all
  sensible (W4); the caption does carry the dots' meaning.

## Verdict

**Ship it after fixing F1 and F2; F3-F6 are polish, none blocking.** The
surface is honest on every axis it claims — no invented counts, no fabricated
instants, neutral ink, AA contrast — and long content wraps instead of lying.
The two findings worth real effort are both about the graphic and the facts
line *erasing* the most decision-relevant fact: the latest delivery visually
merges with the now marker (F1), and an output watch whose target has died is
pixel-identical to one that is patiently waiting (F2). Everything else I tried
to break — wrapping, contrast, counts drifting between rail and panel,
accessibility naming — held.
