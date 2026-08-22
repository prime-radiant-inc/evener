# True-Time Rail

**One-liner:** A 156px session minimap where vertical distance is elapsed wall time, so bursts, waits, and multi-hour absences remain literal rather than being normalized away.

## Comprehension question

> When did this session actually work, wait, and stall—and what was in flight at any chosen wall-clock instant?

The rail is designed to make the answer visible before the user reads individual transcript records. The alternate **TURN INDEX** view is available for structural comparison, but **TRUE TIME** is the default and the point of the variation.

## Real data

`idea-1.html` embeds the complete contents of all three supplied JSON files inline. It does not fetch data and does not generate events.

| Session | Shape | Records | Span | Prompts rendered | Jobs | Children |
|---|---:|---:|---:|---:|---:|---:|
| `034B9Kg7tBDQYMBLr6nsyA` | orchestrator | 353 | 11h 27m | 8 | 18 | 8 |
| `034B9Q0chIC4ng0EoxGOpQ` | leaf worker | 562 | 5h 27m | 4 | 0 | 0 |
| `034B3mp76Fsod8yMVDlj2k` | large orchestrator | 932 | 15h 09m | 11 | 238 | 39 |

“Prompts rendered” means every `USER_INPUT` plus every `STEERING` record whose `steer_src` is exactly `"user"`. The leaf session contributes the dataset's one user-sourced steering record, step 372.

## Encoding

From left to right inside the rail:

- UTC labels and a vertical axis establish literal elapsed time.
- Blue diamonds are `USER_INPUT`; amber diamonds are user-sourced `STEERING`.
- The gray event spine marks every real stream record at its timestamp.
- Teal strata encode real assistant tool-call counts; width increases with the number of calls on that record.
- The narrow cyan lane spans an assistant call record to its following real `TOOL_RESULTS` timestamp. A bright segment is currently in flight.
- Green or red job bands use each real job's `started`, `ended`, completion status, and exit code.
- Violet dots mark actual `delegate` calls in the stream. The dataset gives child metadata but no child lifetime timestamps, so the design does **not** invent delegate lifelines.
- Red crosses mark real calls with `e: true`.
- Dotted bands and labels mark adjacent-record gaps of at least ten minutes. In true-time mode the actual 4h 12m and 6h 18m absences occupy their actual share of the rail.
- The bright horizontal hairline and right-edge wedge are the replay's current wall-clock timestamp. They move continuously through idle space rather than jumping from event to event.

Future activity remains as a faint session skeleton, completed activity gains color, and current intervals brighten. Subpixel records receive a one-pixel visibility floor; their y-position still comes from the real timestamp.

## Update and replay model

Replay state is a timestamp, not a turn counter. `requestAnimationFrame` advances it by `frame_delta × selected_speed`; the controls provide 1×, 60×, 600×, and 3600× wall-time speeds. Play/pause, restart, seek, and session switching all rebuild the visible state from the same embedded records.

At any seek position the component reconstructs:

1. the last stream record whose timestamp is not later than the playhead;
2. assistant tool intervals whose real start/end timestamps contain the playhead; and
3. jobs whose real `started`/`ended` interval contains the playhead.

No artificial replay events are inserted. The LIVE hairline therefore advances at real-time scale, including across blank multi-hour gaps.

## Scaling

The true-time mapping is linear over each session's complete `started` → `ended` span. Long sessions do not scroll inside the component. Dense bursts become compact strata; long idle periods remain blank. Jobs use interval-partitioned micro-lanes, allowing the 238-job session to fit without changing the rail width. The **TURN INDEX** toggle removes wall-clock gaps when the user needs to compare record structure instead of elapsed time.

The dimmed transcript beside the rail may scroll; the rail itself never does.

## Exact footprint

- **Component:** `156px × 100%` of the available stage height.
- **Measured in Chrome at 1440×900, 100% zoom:** `156px × 815px`.
- **Internal scrolling:** none.
- **Outside the budget:** the top replay transport and bottom caption/legend.

## Prompt jump mechanism

Prompt markers are real DOM `<button>` elements, not merged canvas pixels. A greedy collision pass assigns nearby timestamps to adjacent horizontal fan lanes while preserving each marker's exact vertical timestamp anchor. This is visible around default-session steps 130/139 and 228/233.

Clicking a marker:

1. resolves its exact source record index;
2. calls `scrollIntoView({block: "center"})` on transcript row `#step-<i>`;
3. flashes that row;
4. disables automatic transcript following so the replay cannot immediately pull the view away; and
5. records `JUMPED #<i> · <timestamp> UTC` in the rail footer.

All markers remain visible and independently clickable before, during, and after replay.

## What real data changed from the synthetic anchor

The synthetic Micro Strata Rail was turn-normalized and populated with designed-for-demo compactions, watch fires, delegate lifetimes, and a runaway episode. The real sessions forced different decisions:

- **Time, not turn index, became the primary shape.** Real work arrives in short bursts separated by 12–29 minute pauses and multi-hour absences. Normalizing by turn would erase the most informative feature.
- **No folds are drawn.** These sessions contain no `CHECKPOINT` or `SUMMARY` compaction events.
- **No watch lane is drawn.** All three `watches` arrays are empty.
- **Delegate calls replace invented lifelines.** Child metadata has no honest start/end timestamps.
- **The leaf session is explicit, not empty-looking.** Its footer says `LEAF · 0 JOBS · 0 DELEGATES`.
- **Prompt density is real.** The design fans near-simultaneous prompts instead of allowing true-time overlap to collapse them.
- **Job density must scale honestly.** One session has 238 real jobs while another has none.
- **Failure and budget-exhaustion texture comes only from the supplied records.** Nothing is added to make the rail more dramatic.

## Verification

Chrome was run at 1440×900 with zoom reset to 100% (`devicePixelRatio === 1`). Verification covered:

- a tight rail crop before replay and after the real-time head advanced;
- exact rail measurement (`156 × 815`);
- all three sessions and their 8 / 4 / 11 prompt-button counts;
- the leaf session's zero-job/zero-child state;
- the 932-record session's 238 jobs and 39 children;
- the TRUE TIME / TURN INDEX toggle; and
- a click on prompt step 233, which centered the exact `USER_INPUT` row with a measured 0px center delta and set `JUMPED #233 · 23:45:10 UTC`.

The browser provider's saved console-log file reports that backend console logging is not implemented. As an independent runtime check, session switches, both axes, and seeks were exercised under `error`, `unhandledrejection`, and `console.error` capture; all three captured arrays remained empty.
