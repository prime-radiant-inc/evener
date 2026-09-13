# Vision review 02 — information triage (live running UI)

VISION: working — read off the pixels: "Fires every 1m · armed 34m18s ago · 34 deliveries · last at 00:11" under the "Hourly sweep" watch in live-panel-2x.png.

## What is visible

- **live-full.png** (1440x940): three-column hub. Left rail with "evener-hub", blue "+ New session", LIVE section ("Arm Watches For UI Review", subtitle "2 watches · demo · 1 job running · m…"), PROJECTS → demo (badge 1) → same session again. Center: session transcript with repeating "Timer fired · every 1m" folds and "No-action wakeup…" replies. Right: Activity panel with Watches (2 armed), one running job row, one collapsed "2 inactive · 1 failed" fold.
- **live-panel-2x.png**: the Activity panel alone. "Watches / 2 armed" header; watch row 1 "Migration prints DONE — on output · armed" with facts "Waiting on job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv, matching NEVERMATCHESXYZ · armed 2m42s ago · no deliveries yet" and the no-schedule sentence; watch row 2 "Hourly sweep — every 1m · 34 fires" with facts line, a 32-dot timeline labelled "23:40 … now 00:11", and caption "Last 32 of 34 deliveries"; then the "$ Tick loop…" running job row with its sh command and "running 2m · 7b · started 00:08"; then "› 2 inactive · 1 failed".
- **live-rail-3x.png**: the rail at 3x. Both the LIVE row and the PROJECTS→demo row show the same session with "2 watches · 1 job running" subtitles, green dots, right-aligned "1m". The LIVE row's subtitle is truncated to "m…".

## WHAT WORKS

1. **Watches block is structurally separate from jobs.** Severity: n/a (positive). live-panel-2x.png, whole panel. The "Watches / 2 armed" header sits above the watch rows; watches carry a clock glyph, jobs carry a green "$". No interleaving. A scanner can treat the block as one unit and skip it or dive in. Keep this.
2. **The timeline answers "is it alive?" in under a second.** Severity: n/a. live-panel-2x.png, Hourly sweep detail. 32 uniformly spaced dots ending in a slightly larger marker directly above "now 00:11" reads instantly as "fires regularly, fired just now". This is the single best triage element in the panel.
3. **The no-schedule sentence reads as explanation, not error.** Severity: n/a. live-panel-2x.png, Migration prints DONE detail. It is set in the same lighter-gray caption style as "Last 32 of 34 deliveries", no red, no warning icon, and the row header already says "armed" in green. I did not conclude the watch was broken.
4. **Color vocabulary is consistent.** Severity: n/a. All images. Green = live/healthy (dots, "armed", "1 job running", "running"), red = failed, amber = project badge. No color is used decoratively.
5. **"2 watches" in the rail subtitle survives to 3x.** Severity: n/a. live-rail-3x.png. The watch count is visible in both LIVE and PROJECTS rows without opening the session.

## WHAT DOES NOT

1. **The inert watch is listed first; the live proof is second.** Severity: medium. live-panel-2x.png, top of Watches block. "Migration prints DONE" (zero deliveries, by design never fires) occupies the first and tallest slot; "Hourly sweep" with its liveness evidence is below it. For the question "is that watch actually alive or stuck?", the first thing the eye gets is a watch with nothing to show, and a hasty scanner can walk away thinking "this session's watches are quiet" — or worse, "something is stuck" — one row before the disproof. Fix: sort watches by most recent delivery (or firing watches before never-fired ones); failing that, put zero-delivery output watches last.
2. **The 34-character job id is the heaviest token in the panel and carries zero triage value.** Severity: medium. live-panel-2x.png, Migration prints DONE facts line. "job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv" forces the facts line to wrap, pushing "armed 2m42s ago · no deliveries yet" — the actual triage content — onto the second line. The eye is pulled to the one string it cannot parse. Fix: truncate to something like "job_034N…DJdv" with the full id on hover/copy.
3. **"every 1m · 34 fires" (row header) vs "34 deliveries" (facts line) uses two words for the same number.** Severity: medium-low. live-panel-2x.png, Hourly sweep row. A careful reader must stop and decide whether "fires" and "deliveries" are different counters (fires attempted vs delivered?). They are the same 34. Fix: pick one term — "deliveries" is the one used elsewhere — and use it in both places.
4. **"Last 32 of 34 deliveries" explains the count but not the gap.** Severity: low-medium. live-panel-2x.png, below the dot timeline. The caption tells me 2 deliveries are not drawn but not why or where they went; combined with the "23:40 / now 00:11" axis the reader has three numbers to reconcile to confirm nothing was dropped. It informs, but only after arithmetic. Fix: "Showing the 32 most recent of 34 deliveries" (or draw all 34 when the count is this small).
5. **Each expanded watch repeats its own name as a bold heading.** Severity: low-medium. live-panel-2x.png, both watch details. "Migration prints DONE" and "Hourly sweep" appear twice, stacked within ~30px, identical wording. It costs a full line of vertical space per watch and adds nothing — the row header directly above already said it. Fix: cut the duplicate title inside the detail body.
6. **Mixed time bases force mental conversion.** Severity: low. live-panel-2x.png, Hourly sweep facts. "armed 34m18s ago" (relative) next to "last at 00:11" (absolute clock) next to "now 00:11" (axis label). To check "did it fire within the last minute?" I have to notice "now" and "last" are the same clock value. Fix: use relative for recency ("last 12s ago") or pair every absolute time with its offset.
7. **"— · 2m" on the job row leads with an em dash.** Severity: low. live-panel-2x.png, Tick loop row header. An em dash as the first metadata token looks like a missing value. Fix: drop the dash when there is no preceding stat.
8. **Rail subtitle truncation produces "m…".** Severity: low. live-rail-3x.png, LIVE row. "2 watches · demo · 1 job running · m…" — the branch name "main" is clipped to an ambiguous fragment while the PROJECTS copy of the same row fits "main" fully. Also note the LIVE row carries "demo" and the nested row doesn't, so the two copies of the same session read slightly differently. Fix: truncate with an ellipsis that doesn't leave a one-letter stub, or drop the branch first.

## Wrong-conclusion audit

- "Migration prints DONE" at the top with "no deliveries yet": mitigated by green "armed" and the no-schedule sentence, but its first position means the reader meets the ambiguity before the reassurance is fully absorbed (finding 1).
- "1 failed" in red at the bottom fold: correctly draws the eye for question 1; no change.
- The dot timeline cannot be misread as stale — the end marker plus "now 00:11" anchors it.
- No element suggested a dead watch was armed, or vice versa.

## Verdict

The panel answers the three questions, but not equally fast:

1. **Is anything waiting on me?** Yes/No is answerable in ~2s — nothing in Watches demands action, and the one thing that might ("1 failed") is red — but it is buried in a collapsed fold at the bottom while a never-firing watch gets top billing.
2. **What is this session doing?** Fast: the Tick loop job row with its command is self-explanatory.
3. **Is that watch alive or stuck?** Fast for Hourly sweep (dots + "now 00:11" end marker is excellent); slower than it should be for the output watch, only because it is listed first and its facts line is dominated by an unparseable job id.

Overall: good bones, correct instincts on color and the timeline; the triage cost is ordering (inert watch first), one oversized opaque token, and duplicated labels. Fix findings 1–3 and this panel scans in one pass.
