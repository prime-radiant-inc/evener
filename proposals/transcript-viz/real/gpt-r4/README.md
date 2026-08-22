# Token Seismograph

**Comprehension question:** *Where did this session spend its tokens, how quickly did cumulative burn rise, and which individual turns or tool results caused the cliffs?*

Open [`idea-1.html`](./idea-1.html) directly with `file://`. It is self-contained and embeds the complete contents of all three files in `../../realdata/` inline.

## Compact footprint

This is a **156 px-wide vertical rail × 100% of the transcript stage** (`#resourceRail`), within the 160 px vertical-rail budget. It has no internal scrolling. At the verified 1440 × 900 viewport, the rail's measured CSS rectangle was **156 × 818 px** (`x=1284`, `y=45`). The replay transport is the separate 45 px toolbar above it and is not part of the component footprint.

## Encoding

Real elapsed time runs top-to-bottom. This is intentionally not turn-index spacing: long wall-clock gaps remain empty.

- **Cyan horizontal strokes:** per-assistant-turn input tokens, log-scaled against that session's largest input turn.
- **Gold horizontal strokes:** per-assistant-turn output tokens, independently log-scaled so output remains legible beside much larger input values.
- **Red ticks:** `TOOL_RESULTS.res_bytes`, log-scaled; the header accumulates result bytes.
- **White step line (`Σ BURN`):** cumulative `in + out`. It jumps horizontally only at a real assistant timestamp and stays vertical through idle time.
- **Red numbered diamonds:** the session's five largest `in + out` assistant turns, placed at their real timestamp and cumulative position.
- **Cyan dashed head:** the current interpolated real wall-clock position.
- **Blue / gold chevrons:** permanent prompt anchors. Blue is `USER_INPUT`; gold is only `STEERING` where `steer_src === "user"`.

The source exposes only per-turn `in`, `out`, and their derived total. It has **no cache-read/fresh-input split**, so the rail does not pretend input is fresh. This absence is stated in both the rail footer and page legend.

## Update and in-flight model

Replay starts from each record's real `started` timestamp and advances toward its real `ended` timestamp. Play/pause, three speed choices, restart, and the seek slider sit outside the rail. Seeking reconstructs state from source timestamps rather than from a synthetic event list:

1. locate the latest stream record at or before the requested time;
2. sum assistant `in + out` and result bytes at or before that time;
3. reconstruct active jobs from each job's real `started` / `ended` interval;
4. show the open interval as `#current KIND → #next` and draw the replay head at the exact interpolated time.

This makes seek deterministic and preserves multi-hour idle gaps. Session switching rebuilds the transcript, cost scales, prompt anchors, and replay state from that session only.

## Prompt jump mechanism

Every qualifying prompt exists as its own HTML `<button>` over the rail for the entire replay, including future prompts. Closely timed prompts get collision-resolved vertical positions; a leader connects each displaced button to its exact timestamp. A click calls `scrollIntoView({block: "center"})` on the real transcript row `#row-<stream.i>`, records the target in `data-last-jump`, and selects the matching marker. It does **not** merge or summarize prompts.

Marker counts from the records are:

- Session A: 8 (`USER_INPUT` only)
- Session B: 4 (3 `USER_INPUT` + user-sourced steering #372)
- Session C: 11 (`USER_INPUT` only)

Other `STEERING` records are daemon/system notifications and are intentionally not represented as user prompt anchors.

## Real resource totals shown by the component

All session metrics below are computed from the embedded records; no cost events or values are generated.

| Session | ID | Model | Input | Output | **Total shown** | Result bytes |
|---|---|---|---:|---:|---:|---:|
| A | `034B9Kg7tBDQYMBLr6nsyA` | k3 | 674,032 | 139,733 | **813,765** | 116,034 |
| B | `034B9Q0chIC4ng0EoxGOpQ` | k3 | 784,898 | 220,644 | **1,005,542** | 66,889 |
| C | `034B3mp76Fsod8yMVDlj2k` | gpt-5.6-luna | 9,451,628 | 101,536 | **9,553,164** | 1,210,292 |

## What real data changed from the synthetic direction

- The resource distribution is much less tidy. Session C is overwhelmingly input-heavy and ends with several 480k–543k-token turns, creating genuine cumulative cliffs.
- Real result sizes are separate `TOOL_RESULTS` records rather than invented tool durations.
- Real timestamps create large blank bands; the design preserves them instead of evenly distributing turns.
- **No compaction events exist**, so there is no fold encoding.
- **No watches fired**, so there is no watch lane.
- Session B is a real leaf with **0 jobs and 0 children**. The footer states `jobs 0 active / 0 · children 0`; the rail remains a valid cost view rather than showing an error or empty-state warning.
- Cache attribution is absent, so only honest input/output texture is rendered.

## Long-session scaling

The width remains 156 px regardless of record count and the component never scrolls. Multiple events mapping to one pixel intentionally overdraw into denser texture; log-width encoding preserves small and large turns together, while the cumulative step line retains exact aggregate shape. The largest included session has 932 stream records, 379 costed assistant turns, 379 result records, 238 jobs, and 11 permanent prompt buttons. Prompt clustering is handled separately from cost overdraw so every prompt in all three supplied sessions remains individually clickable.

## Chrome verification

One Chrome verification pass was run from `file://` at **1440 × 900, 100% zoom** (`devicePixelRatio=1`, `visualViewport.scale=1`).

- Tight rail crops captured a live state at **1,955,923 / 9,553,164 tokens** and the completed state at **9,553,164 / 9,553,164**, visibly advancing the cumulative line and result texture.
- Clicking Session C's prompt marker **#444** scrolled its matching `USER_INPUT` row to the exact transcript center: row center **471 px**, transcript center **471 px**, `data-last-jump="444"`, marker selected. The row text was “As each PR finishes, I want you to do an aggressive simplification pass.”
- All switcher totals and record/marker counts were exercised. Session B rendered 562 rows, 4 prompt markers, total **1,005,542**, and the neutral `jobs 0 active / 0 · children 0` state.
- The corrected artifact produced no captured console messages.
