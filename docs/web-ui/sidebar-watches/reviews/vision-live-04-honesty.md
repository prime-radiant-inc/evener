# Vision review 04 — honesty & fidelity (live running hub)

Panelist 4 of 4. Lens: honesty and fidelity — does the surface claim anything the
runtime does not actually know?

## VISION status

VISION: working. All three screenshots were read as pixels. Specific string read
off the pixels as proof: the no-schedule sentence "There is no schedule to draw
here — this one fires when the job says the word, not when a clock says so."
(including the em dash) in `live-panel-2x.png`, and "Last 32 of 34 deliveries"
beneath the "Hourly sweep" timeline.

Caveat: the vision pipeline is model-generated transcription, not byte-exact OCR.
Where a character-level detail matters (em dash, separators "·"), I cross-checked
against the composing source and say so below. Dot counts in the timeline are
approximate — I could not count 32 individual dots reliably at 2x and say so
rather than guess.

## 1. Verbatim transcription of the watch block (live-panel-2x.png, cross-checked against live-full.png)

Group header:

```
Watches                                                              2 armed
```

Watch row 1 (expanded):

```
∨ 🕐 Migration prints DONE                              on output · armed

    Migration prints DONE
    Waiting on job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv, matching
    NEVERMATCHESXYZ · armed 2m42s ago · no deliveries yet
    There is no schedule to draw here — this one fires when the job says the
    word, not when a clock says so.
```

Watch row 2 (expanded):

```
∨ 🕐 Hourly sweep                                        every 1m · 34 fires

    Hourly sweep
    Fires every 1m · armed 34m18s ago · 34 deliveries · last at 00:11
    ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ● ▎
    23:40                                                   now 00:11
    Last 32 of 34 deliveries
```

(Timeline: approximately 32 marks — a row of grey dots plus a slightly darker
"now" end-cap marker at the right. I cannot verify the exact dot count from the
pixels; the visible density is consistent with "32 instants + 1 now marker".)

Adjacent non-watch rows, for context (not part of the watch block):

```
∨ $ Tick loop: one tick every 30 seconds, 40 ticks (~20 minutes)  ⧉   — · 2m
    sh -c 'i=0; while [ "$i" -lt 40 ]; do i=$((i+1)); echo tick $i; sleep 30; done'
    running 2m · 7b · started 00:08

> 2 inactive · 1 failed        ("1 failed" in red; "2 inactive" grey)
```

Rail (live-rail-3x.png), both the LIVE row and the nested project row for the
session "Arm Watches For UI Review":

```
2 watches · demo · 1 job running · m…      (LIVE row, truncated)
2 watches · 1 job running · main           (project row)
```

"2 watches" in neutral grey; "1 job running"/"main" green; no watch item carries
any red or amber tint.

## 2. Code-to-screen comparison

All code references are in this worktree (branch `sidebar-watches`).

| On screen | Composed by | Match? |
|---|---|---|
| `Watches` / `2 armed` | `ActivityTree.tsx:582-589` `WatchGroupHeader`, `` `${armed} armed` ``, count from `armedWatchCount` (`railNodes.ts:346-348`: `watches.filter(w => w.active).length`) | Exact. Both watches are active, so 2 armed. |
| `Migration prints DONE` (row name and detail note) | `activityRows.ts:96-98` `watchName` = `watch.note?.trim() \|\| watch.id`; note repeated verbatim in `ActivityRowDetail.tsx:323-327` | Exact. |
| `on output · armed` | `activityRows.ts:162-170` `watchMeta`, output branch: `` `on output · ${armedState(watch)}` `` (`armedState`, lines 128-130) | Exact. |
| `Waiting on job_034NnSnRLFjNWPnAO6mLLk_jHtbABK0DJdv, matching NEVERMATCHESXYZ · armed 2m42s ago · no deliveries yet` | `activityRows.ts:174-202` `watchFacts`: output branch (lines 177-179), armed-age segment (lines 186-191, via `armedAgeLabel` lines 135-140 and `watchDurationLabel`), zero-delivery branch (line 199) | Exact, including the "·" separators and "no deliveries yet". |
| `There is no schedule to draw here — this one fires when the job says the word, not when a clock says so.` | `ActivityRowDetail.tsx:49-50` `WATCH_NO_SCHEDULE_LINE`, rendered for any non-scheduled watch at lines 331-337 | Verbatim, em dash included. |
| `every 1m · 34 fires` | `watchMeta` scheduled branch (`activityRows.ts:166-169`): cadence label + `` `${watch.deliveries} fire${s}` `` since `deliveries (34) > 0` | Exact. |
| `Fires every 1m · armed 34m18s ago · 34 deliveries · last at 00:11` | `watchFacts` scheduled branch: `Fires ${cadence}` (lines 183-184), armed age, `` `${deliveries} deliveries` `` (line 193), `last at ${lastDeliveryClock(...)}` gated to scheduled kind (lines 194-197, `lastDeliveryClock` lines 150-157) | Exact. |
| Timeline labels `23:40` / `now 00:11` | `ActivityRowDetail.tsx:71-72, 102-105`: `startLabel` = clock of earliest retained instant; end label is the literal string `now ${endLabel}` where `endLabel` is the clock of the render tick | Exact. The right label is explicitly "now …", so the tick instant is never passed off as a delivery. |
| `Last 32 of 34 deliveries` | `ActivityRowDetail.tsx:73-76`: when `watch.deliveries (34) > instants.length (32)` → `` `Last ${instants.length} of ${watch.deliveries} deliveries` `` | Exact. (The other branch, "Delivered to this session", is not exercised here.) |
| Timeline dots | `ActivityRowDetail.tsx:86-100`: one `timelineDot` per retained instant positioned by `(millis - earliest)/(now - earliest)`, plus one `timelineNow` cap pinned at `left: 100%` | Consistent. The code would draw 32 dots + 1 now-cap; pixels show ~32 marks, exact count not verifiable by eye. No evidence of a fabricated instant. |
| Rail `2 watches` | `RailRow.tsx:629-635`: `` `${watchCount} watch${es}` `` with `watchCount = activeWatchCount(session)` → the same `armedWatchCount` predicate the panel header uses | Exact, and deliberately the same predicate (railNodes.ts:343-345 comment: "both numbers are one predicate, not two copies of one"). Neutral ink by construction (RailRow.tsx:621-627 comment). |

No divergence found between what is on screen and what the code composes. Every
string in the watch block is accounted for by a specific code path, and no string
the code can produce is missing or altered on screen.

## 3. Claim-by-claim honesty audit

1. **`34 deliveries` (facts) / `every 1m · 34 fires` (row meta) / `Last 32 of 34 deliveries` (caption) / ~32 drawn dots.**
   All four read off the same two wire fields (`deliveries` and the bounded
   `delivery_times` ring). The caption explicitly discloses that only 32 of 34
   are drawn. A reasonable user concludes exactly the truth: 34 real deliveries,
   the newest 32 plotted. **No overstatement.** The dots themselves are the one
   place a user could over-read — the oldest 2 deliveries are silently absent
   from the axis — but the caption immediately below the axis says precisely
   that. Honest.

2. **`last at 00:11`.** Composed by `lastDeliveryClock` from the newest retained
   instant. Because the ring keeps the *newest* 32, the newest retained instant
   is the true last delivery instant; the 32-cap cannot corrupt this claim.
   Gated to scheduled watches only (activityRows.ts:194), so it never appears
   where it would be meaningless. **Honest.** Minor note: it is HH:MM with no
   date, so a watch idle across midnight would show an ambiguous clock — not in
   evidence here, and the timeline's `23:40 → now 00:11` axis actually
   demonstrates the midnight crossing being handled sanely.

3. **`now 00:11` (timeline end label).** This is the render tick, not a
   delivery. It is labeled "now", positioned at 100% by a distinct element
   (`timelineNow`), and the rail's aria-label (`ActivityRowDetail.tsx:83`) reads
   "…, 23:40 to now 00:11". A user cannot mistake it for a 33rd delivery dot.
   **Honest.**

4. **`There is no schedule to draw here — this one fires when the job says the word, not when a clock says so.`**
   Shown for the output watch in place of a timeline (`ActivityRowDetail.tsx:331-337`).
   This is the strongest honesty feature on the surface: instead of drawing an
   empty or implied axis for a watch with no period, it says plainly why there
   is nothing to draw. The claim "fires when the job says the word" is exactly
   the output-match semantics ("matching NEVERMATCHESXYZ" on the line above).
   **Honest, and actively anti-overstatement.**

5. **`Watches 2 armed` (group header).** Counts only `active` watches via
   `armedWatchCount`. Both listed watches are armed, so 2 is correct. Edge worth
   naming: if a disarmed watch were in the list it would render as a row
   ("not armed", via `armedState`) while the header counts only armed ones — the
   header could then read lower than the row count. That is the disclosed,
   correct semantic (the header claims *armed*, not *total*), and it does not
   arise in this render. **Honest as rendered.**

6. **Rail `2 watches`.** Same predicate as the header (one shared function), so
   the two surfaces cannot drift. Wording differs ("2 watches" vs "2 armed") but
   the number counts armed watches in both places; a user reading "2 watches"
   beside a live session is not misled when both are armed. **Honest as
   rendered**; the wording asymmetry is cosmetic, not factual.

7. **Name vs. cadence tension: a watch named "Hourly sweep" that "Fires every 1m".**
   The name is the user-authored note, rendered verbatim by design
   (`watchName`, activityRows.ts:94-98, "the note a person wrote down"). The UI
   itself never asserts the watch is hourly — the runtime-sourced cadence
   "every 1m" sits in the same row and the facts line. A reasonable user reads
   "Hourly sweep" as a label, not a schedule claim, because the actual schedule
   is printed twice next to it. **Not an overstatement by the surface** — but
   worth one line in this review because it is the only place where
   user-supplied text and runtime fact visibly disagree. No change required;
   the design's answer (show the real cadence beside the note) is working as
   intended.

8. **`armed 2m42s ago` / `armed 34m18s ago`.** Derived from `created_at`
   (`armedAgeLabel`, activityRows.ts:135-140), i.e., age since creation. For a
   watch armed once at creation this equals "armed … ago" exactly. If the
   runtime ever allows disarm-then-re-arm, `created_at` would overstate the
   armed span — not in evidence in this render (both watches were armed at
   creation). **Honest as rendered.**

9. **Absences that carry claims by omission — checked all three:**
   no drop count anywhere (the feature tracks none, so none may be shown);
   no "next in …" / countdown / next-fire instant anywhere (the runtime keeps
   no such instant; `watchMeta`'s own comment at activityRows.ts:160-161 says
   "Never a next-fire or countdown"); no timeline drawn for the output watch.
   **All three hold in the pixels.**

## 4. Honesty-rules verdict for the render

- **Neutral ink, no red/danger tint in the watch block: CONFIRMED.** Every
  watch string — names, metas, facts, no-schedule line, timeline, caption,
  "2 armed", rail "2 watches" — renders in near-black or muted grey. The only
  red in the panel is "1 failed" in the collapsed fold row
  (`2 inactive · 1 failed`), which is outside the watch block and reports a
  real terminal-failure count from the activity tree, not watch data. The rail's
  watch count is neutral by explicit construction (RailRow.tsx:621-627: the
  count is its own element "so it keeps neutral ink … a watch … is not one of
  the four attention hues").
- **No drop counts: CONFIRMED.** None on screen; the header comment at
  ActivityTree.tsx:579-581 states the reason ("the projection tracks no drops,
  so there is no abnormal count to report") and the mockup's "8 dropped"
  elements are gone.
- **No next-fire time or countdown: CONFIRMED.** The only temporal strings are
  elapsed facts ("armed … ago", "last at 00:11") and the labeled "now" tick.
- **No fabricated instant: CONFIRMED as far as pixels allow.** The timeline
  draws one dot per wire-carried instant, positions them by interpolation
  between the earliest instant and now, caps the count disclosure in the
  caption, and labels the synthetic end marker "now". Exact dot count is not
  eyeball-verifiable; nothing in the render contradicts the 32-dot claim.

## 5. Deviations from the approved mockup (round-3-fluent.html)

The mockup showed three watches ("3 armed · 1 needs a look"); the live session
has two. Comparing grammar, not counts:

1. **Group header:** mockup `3 armed · 1 needs a look` → live `2 armed`. The
   "needs a look" abnormal count is dropped. **Deliberate and disclosed** —
   drops are untracked, so there is no abnormal state to count
   (ActivityTree.tsx:579-581).
2. **Abnormal-watch danger styling:** mockup's `wsvg--danger` icon, danger
   gloss `every 10s · 8 dropped`, danger meta → live has no danger path in the
   watch block at all. **Deliberate** — same reason.
3. **Facts line drops:** mockup `214 deliveries, 8 dropped` → live
   `34 deliveries · last at 00:11`. Drop count omitted. **Deliberate** — the
   runtime does not track per-delivery drops. Live adds "last at HH:MM", which
   the mockup did not show; this is an addition grounded in a real wire field
   (`lastDeliveryClock`), so it strengthens rather than weakens fidelity.
4. **Timeline dropped-dots and self-influence caption:** mockup drew red
   `timeline-dot--dropped` marks with caption "Red marks are the 8 the depth
   fuse dropped · deepest self-influence depth 4" → live draws only delivered
   instants in one neutral style with caption "Last 32 of 34 deliveries". Both
   omissions (drop marks, self-influence depth) are the deliberate, disclosed
   decisions — the runtime cannot know either number.
5. **Caption grammar:** mockup implied one caption; code has two branches
   ("Delivered to this session" when nothing was capped away,
   "Last N of M deliveries" otherwise, ActivityRowDetail.tsx:73-76). Live
   exercises the second. **Deviation that improves honesty.**
6. **Facts typography:** mockup bolded numerals inside the facts sentence
   (`<b>2h 12m</b> ago`, `<b>214</b> deliveries`); live facts are one plain
   composed string (`watchFacts` returns a flat string). **Cosmetic deviation,
   not a fidelity issue.**
7. **Timeline "now" marker position:** mockup at `left: 98%`, live pinned at
   `left: 100%` (ActivityRowDetail.tsx:98). Cosmetic; the live position matches
   the "now 00:11" label more exactly.

No live deviation introduces a claim the mockup did not make; every substantive
deviation is a removal of something the runtime cannot know.

## 6. Final verdict

**Honest enough to ship: YES.**

Every string in the watch block traces to a specific composing function and a
real wire field; I found zero divergences between pixels and code, and zero
claims a reasonable user could over-read into something the runtime does not
know. The three known limits are not merely respected, they are surfaced as
design: the 32-cap is disclosed in the caption, the schedule-less watch gets an
explicit sentence instead of an empty axis, and the absence of next-fire,
countdown, and drop counts is total — no stray instance anywhere on screen.
The two observations worth recording (none blocking): (a) user-authored notes
can contradict runtime facts in prose ("Hourly sweep" firing every 1m) — the
design's mitigation, printing the real cadence beside the note, works; (b)
"last at HH:MM" carries no date, which would be ambiguous for a watch idle
across days — not exercised here. Neither is an overstatement by the surface
itself.
