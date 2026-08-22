# REAL-4 · k3-r3 — Multi-Session Micro Strata Rail

**File:** `idea-1.html` (444 KB, self-contained, `file://`-safe, inline CSS/JS, no network).
**Data:** all three real sessions from `proposals/transcript-viz/realdata/*.json` embedded **verbatim** as
JS literals (`034B9Kg7…` 353t orchestrator · `034B9Q0c…` 562t leaf worker · `034B3mp7…` 932t big-tree
orchestrator). No synthetic events anywhere; every mark on screen derives from a real `stream` row,
real `jobs[]` timestamps, or the real `children[]` count.

## Comprehension question

> *"I ran several agent sessions yesterday — how did their shapes differ: who worked when, who idled,
> who fanned out into delegates/jobs, and where did the human step in?"*

One glance compares the three real sessions as **mini-multiple rails on one shared replay clock**:
the orchestrator's 11.5 h with 8 delegate spawns and a 4.2 h idle hole, the leaf worker's dense
5.5 h burst with zero jobs/delegates, the big-tree's 15 h sprawl with 238 jobs and 91 steering
ticks. Difference in shape *is* the content.

## Encoding (per rail, 136 px lanes)

| Lane | x | Meaning |
|---|---|---|
| kind strip | 0–6 | one 2 px tick per real turn, colored by `kind` (USER blue, STEER amber, ASSISTANT slate, TOOL_RESULTS dim, ATTN violet, ENV teal, HOOK gray) |
| tool heat | 8–46 | assistant turns: teal alpha ∝ `calls.length`; TOOL_RESULTS: gray sliver ∝ `res_bytes` |
| delegates | 50–68 | violet chevron per real `delegate` call, dot per `delegate_send` (children have no timestamps in the data — spawns are recovered from the stream) |
| jobs | 70–86 | bar from real `started`→`ended`; dashed+animated while in flight, green `completed`, red `failed` (+✕), gray `cancelled` |
| events | 90–116 | minor steering (non-user `steer_src`) amber tick, ATTENTION_RESOLUTION violet, ENV/HOOK ticks, red diamond per errored tool call |
| **prompt anchors** | left edge | **▶ chevron per USER_INPUT (blue) and per STEERING with `steer_src==="user"` (amber) — permanent, never collapse, min 10 px separation with leader lines** |
| gaps | full width | hatched band + "idle N.Nh" label for real gaps > 15 min; white now-line + `T+h:mm` chip |
| end cap | — | `END h:mm` line + hatch below each session's real `ended` |

Y axis = **real elapsed seconds ÷ 15.15 h (the longest session)** — identical in all three rails, so
the white now-line is horizontally aligned across rails and pace/duration differences are literal:
rail B's content stops at 36 % height (END 5:26), rail A at 76 % (END 11:27), rail C fills it.

## Update model (realtime feel)

- Single shared clock `simT` 0→DMAX, advanced on real timestamps (100 ms tick, 150×/600×/2400×
  transport + seek slider + restart, all outside the component budget in the page chrome).
- Per-rail append-only reveal: turns/jobs/gaps/anchors appear exactly when their real elapsed time
  is crossed; seeking backward resets and re-reveals. DOM churn per tick is O(new events).
- In-flight reconstruction: the head turn carries an animated dashed stratum until its successor's
  timestamp arrives; running job bars grow with the now-line and freeze at their real end; an
  `idle…` tag appears when the head turn is older than the gap threshold.
- LIVE indicator per rail (pulsing dot + `LIVE` chip) flips to `ENDED` as the now-line crosses each
  session's real `ended`; the group chip reads `ALL ENDED` at DMAX.
- The dimmed mock transcript streams the focused rail's real rows (real text, real tool chips,
  real `T+` times) and auto-follows the head; clicking a rail header re-focuses it.

## Scaling

- Tested at 932 turns + 238 jobs (session C): append-only reveal keeps the frame loop flat;
  full re-reveal on seek is ~1.3 k SVG nodes per rail, one-shot.
- Honest limits: ~2–3 k turns/rail stays smooth; beyond that the kind strip saturates into a solid
  bar (acceptable: density *is* the signal at that point) and job bars begin to overplot — a real
  deployment would bin jobs per pixel-row. Turn labels/tooltips use binary search over elapsed
  time, so hover stays O(log n).

## Exact footprint

**466 px wide × full viewport height** (3 rails × 146 px + 2 × 8 px gaps + 12 px padding), inside the
≤480 px budget; sits in situ at the right edge beside the dimmed transcript. Transport, clock, seek
and legend live in the page chrome/caption bars — outside the component. No scrolling inside any
rail; the only scroller is the host transcript pane.

## Prompt-jump mechanism

`prompts = stream.filter(kind==='USER_INPUT' || (kind==='STEERING' && steer_src==='user'))` per
session → A: 8, B: 4, C: 11 anchors. Each is a 13×12 px click target (`rect[data-p=turnIndex]`)
drawn last, on top, once its real timestamp is reached; stacked anchors get 10 px minimum
separation plus a leader line to their true y, so every anchor stays individually clickable.
Click → `jumpTo(session, turnIndex)`: switches transcript focus to that session, rebuilds its rows
to the current head, `scrollIntoView({block:'center'})` the exact `#tr-{s}-{i}` row, flashes it
1.2 s, and holds auto-follow for 7 s. Verified in Chrome: clicking rail B's anchor for turn #193
switched the transcript to `034B9Q0c…` and scrolled #193 ("REDO with a changed goal…") into view.

## What real data changed vs synthetic

1. **steer_src is almost always absent** (1 of 160 steering turns). The spec rule
   `steer_src==='user'` yields only ONE steering anchor (B #372); the other 159 steering turns
   demote to minor amber ticks (still clickable) instead of fabricating anchor status. The caption
   says this plainly.
2. **No CHECKPOINT/SUMMARY turns exist** — the anchor design's fold bars and context micro-bar were
   removed, not faked. Token readout is cumulative real `out` tokens only.
3. **No watches in any session** — the watch lane is gone; its space became the real events lane
   (ATTENTION_RESOLUTION turns out to be frequent: 64 in C — an event kind no synthetic round used).
4. **Children carry no timestamps** — delegate brackets (spawn→report) cannot be drawn honestly;
   spawns are recovered from real `delegate` tool calls and rendered as point chevrons, with the
   real child count in the header. Session B is verifiably A's child (`parent` field) — noted, not drawn.
5. **Real timestamps are bursty with multi-hour holes** — the y axis is time-linear (not
   turn-index), so the 252 min / 378 min owner-away gaps render as literal hatched voids, and the
   now-line visibly stalls through them. USER_INPUTs land right after the big gaps — the human
   returning — which the shared clock makes visible across all three rails at once.
6. **Jobs run on their own timestamps with `-07:00` offsets** (stream is UTC) — parsing normalizes
   both; one C job was still `running` at extraction and is closed at the session's real end.
7. **Real durations differ 3×** (5.45 h / 11.45 h / 15.15 h) — rails end at different heights with
   END caps instead of normalized full-height rails; the difference in span is the story.

## Verification evidence (Chrome, file://, 1280×800 @100%)

- Console self-check logged per session: turns/jobs/children/anchors/gaps/duration — all match
  `realdata/README.md` (353/18/8/8, 562/0/0/4, 932/238/39/11).
- Animation proof: screenshot at T+3:46 (partial reveal, LIVE) vs T+15:08 (full reveal,
  `ALL ENDED`, footers `#353/353 · out 139.7k`, `#562/562 · 220.6k`, `#932/932 · 101.5k` — matching
  the real token sums 139,733 / 220,644 / 101,536).
- Click test: `#rm1 rect[data-p="193"]` clicked → transcript header switched to `034B9Q0c…`,
  `tr-1-193` scrolled into view (scrollTop 4333) with flash highlight in the capture.
- Final smoke: clock advancing, railgroup measured exactly 466 px.
