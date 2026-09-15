# Vision live review 05 — confirming review of the three watch-block fixes

Reviewer: confirming reviewer (vision panel, pass 5)
Date: 2026-09-13
Subject: running evener hub UI, post-fix captures. Pre-fix captures not provided; verdicts are based only on the three new shots.

Evidence examined:

- `/tmp/evener-watch-review/shots/fixed-panel.png` — Activity panel, both watch rows expanded (883×1075)
- `/tmp/evener-watch-review/shots/fixed-full.png` — whole app at 1440×940
- `/tmp/evener-watch-review/shots/fixed-rail.png` — rail column

## VISION status

VISION: working. All three reads returned pixels I could read. Sample string read off the pixels of fixed-panel.png: `Fires every 1m · armed 56m ago · 55 deliveries · last at 00:32`. (The brief expected `armed 55m47s ago`; the live render shows the same fact rounded/ticked forward to `56m ago` and `1m32s ago` vs `1m19s` — consistent with a live ticking clock, not a discrepancy.)

## Per-fix verdicts

### Fix 1 — note no longer printed twice: FIXED

The "Hourly sweep" row title reads `Hourly sweep` and its expanded block leads with the facts line `Fires every 1m · armed 56m ago · 55 deliveries · last at 00:32` — the title text appears exactly once. The "Migration prints DONE" row title reads `Migration prints DONE`; its expanded block leads with `Waiting on job_034NnSnRLFjNWPnAO6mLLk_n0LipoLu5YCj, matching NEVERMATCHESXYZ · armed 1m32s ago · no deliveries yet` followed by `There is no schedule to draw here — this one fires when the job says the word, not when a clock says so.` Neither note is repeated as the first line of its own block. Both notes are well under the 48-character budget (`Hourly sweep` = 12 chars, `Migration prints DONE` = 21 chars), so per the fix they render once, as the row title. The over-budget path (lead paragraph rendered) cannot be exercised by these two watches, so it is unverified here — noted, not held against the fix.

### Fix 2 — newest delivery dot no longer swallowed by the "now" marker: FIXED

Verified two ways. Visually, the "Hourly sweep" timeline shows a full row of dots with the rightmost dot clearly drawn and separated from the marker; the `now 00:33` label sits below the rail at the right and does not overlap any dot. Computationally, I thresholded the dot row band (y≈415–425) of fixed-panel.png and found 33 marks: 32 dots, each 8–9 px wide on an even ≈22.6 px pitch (centers x≈100 to x≈800), plus one final 2-px-wide vertical tick at x=821–822 — the "now" marker. The last dot ends at x=803, leaving a full dot-pitch column (≈18 px clear gap) between the newest dot and the marker. The marker keeps its own column; no dot is hidden behind it. The caption `Last 32 of 55 deliveries` matches the 32 dots counted.

### Fix 3 — count called "deliveries" everywhere: FIXED

The count noun is `deliveries` in every position: row header `every 1m · 55 deliveries`, facts line `… · 55 deliveries · last at 00:32`, caption `Last 32 of 55 deliveries`, and the zero-delivery watch's `no deliveries yet`. The word `fires` survives only as a verb describing the schedule — `Fires every 1m` and `…this one fires when the job says the word…` — never as the count noun. The old header/facts mismatch ("fires" vs "deliveries") is gone.

## Supporting claims from the brief, also confirmed

- Group header: `Watches` … `2 armed` (fixed-panel.png, fixed-full.png).
- Rail row: both the LIVE row and the PROJECTS row show `2 watches` for the session (fixed-rail.png).
- Zero-delivery watch renders the no-schedule line instead of a timeline, as specified.

## Remaining defects

None inside the watch block. No clipped or overlapping text, the timeline is readable (32 evenly spaced dots plus a distinct thin marker tick that cannot be mistaken for a 33rd dot — it is 2 px wide vs the 8–9 px dots), and no heading repeats.

Two observations outside the watch block, reported for completeness (not blocking for this change):

1. **Low — rail row ellipsized.** In fixed-full.png and fixed-rail.png the LIVE row sub-line reads `2 watches · demo · 1 job running · m…` — the tail (presumably the branch `main`) is truncated with an ellipsis, while the PROJECTS row shows the same facts fully (`2 watches · 1 job running · main`). The watch count itself is intact. Specific fix: widen the rail sub-line budget or drop a lower-priority segment (e.g. the project name, which is redundant under LIVE) before the branch name.
2. **Low — "28 new" pill overlaps thread text.** In fixed-full.png the jump-to-latest pill renders on top of the last session message, splitting it as `it stays armed until c[˅ 28 new]ng else done.`. This is in the session transcript column, unrelated to the watch block. Specific fix: anchor the pill to the composer/thread boundary padding rather than over the final message line.

## Honesty rules in the render

Hold:

- **No drop counts**: the window is stated honestly as `Last 32 of 55 deliveries` — 23 dropped deliveries are accounted for in the caption, and the counted dots (32) match.
- **No next-fire or countdown**: the schedule is stated as a period (`Fires every 1m`); no "next in …" or ticking countdown appears anywhere in the watch block.
- **No fabricated instant**: the only instants shown are `last at 00:32`, `now 00:33`, and `armed … ago` relative times, all consistent with the live session.
- **Neutral ink in the watch block**: all watch-block text and dots render in the same neutral gray ink. The only colored text nearby is `1 failed` in red inside the collapsed jobs group (`3 inactive · 1 failed`), which is outside the watch block.

## Final verdict

**SHIP.** All three reported defects are verifiably fixed in the live render: no duplicated note, newest delivery dot fully visible with the marker in its own column (32 dots + 1 marker tick, confirmed by pixel measurement), and "deliveries" used as the count noun everywhere. The honesty rules hold. The two remaining observations are outside the watch block and low severity; they do not gate this change.
