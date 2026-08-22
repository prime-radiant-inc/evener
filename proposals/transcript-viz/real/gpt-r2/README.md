# Strata Halo — real-data corner badge

**One-liner:** A 316 × 232 px ambient session instrument that turns real record progress, token pressure, tool flight, anomalies, idle gaps, and every human prompt into a glanceable corner halo.

Open `index.html` directly with `file://`; it has no network or build dependency. All three source JSON documents are embedded in full as inert `application/json` script blocks.

## Comprehension question

> **How far through this real session are we, what is happening right now, is token pressure unusual, and where can I jump back to any human prompt?**

The center answers “now,” the two rings answer “how far / how much,” the notches answer “what deserves attention,” and the permanent outer prompt constellation answers “where did the human intervene?”

## Encoding

- **Blue outer ring:** completed real records divided by the session's real record count.
- **Cyan inner ring:** cumulative observed input + output tokens divided by that session's observed final token total. This is deliberately not presented as a fictional context budget.
- **Center core:** the last real record kind and index. Between an `ASSISTANT` record with calls and its immediately following `TOOL_RESULTS`, it changes to the real call name and elapsed/total flight time.
- **Blue outer ticks/dots:** every `USER_INPUT`, always visible and clickable.
- **Amber outer ticks/dots:** every `STEERING` whose `steer_src === "user"`, always visible and clickable.
- **Hollow gray ticks:** real inter-record gaps of at least 15 minutes.
- **Red notches:** real errored calls (`e === true`), failed jobs, or a job still running at extraction. **Amber anomaly notches** are cancelled jobs.
- **Orange “runout p95” notches:** assistant turns at or above that session's real 95th-percentile input + output token count. This is an honest runout-pressure proxy, not an invented exhaustion event or budget.
- **Token-burn comb:** 30 equal bins of real wall time. Blue is input, cyan is output, and the white needle is the replay head.
- **Right metrics:** record count, real elapsed time, cumulative/observed tokens, and jobs running now/total jobs. Child totals remain in the transcript header because the dataset has no child start/end timing.

The footer is the in-component legend/caption. The dimmed transcript behind the badge contains every real stream record; absent text is represented only with real schema facts such as kind, call name, result bytes, tokens, and timestamp.

## Realtime update model

The transport's seek position is linear in the exact interval from `started` to `ended`. While playing, each animation frame advances the replay clock by browser elapsed time multiplied by the selected real-time speed (`1×`, `60×`, `600×`, or `3600×`). A binary upper bound over parsed record timestamps selects the current record. Consequently, multi-hour idle intervals are not compressed: the wall clock and burn needle move while the turn-progress ring stands still.

In-flight state is reconstructed only for a real adjacent pair:

1. an `ASSISTANT` record containing one or more `calls` starts the interval;
2. its immediately following `TOOL_RESULTS` timestamp ends it;
3. within that interval the core displays the real call name(s), elapsed time, and total interval duration.

`LIVE` means the replay clock is advancing, `PAUSE` means transport is stopped, and `END` means the real `ended` timestamp was reached. Seeking pauses so an exact real interval can be inspected.

## Session switcher and honest zero states

- **A:** `034B9Kg7tBDQYMBLr6nsyA` — 353 records, 18 jobs, 8 children, 8 prompt anchors.
- **B:** `034B9Q0chIC4ng0EoxGOpQ` — 562 records, **0 jobs, 0 children**, 4 prompt anchors (3 user inputs + 1 user-sourced steering). It renders a calm `0 / 0 jobs`, not a broken or empty visualization.
- **C:** `034B3mp76Fsod8yMVDlj2k` — 932 records, 238 jobs, 39 children, 11 prompt anchors.

Switching sessions resets that session to its real start and rebuilds the mock transcript, rings, real-time histogram, p95 threshold, gap ticks, anomaly list, and prompt anchors from the selected embedded document.

## Prompt-jump mechanism

Prompt anchors are derived exactly as:

```js
kind === "USER_INPUT" || (kind === "STEERING" && steer_src === "user")
```

Every matching record gets its own permanent SVG hit target with `data-prompt="<i>"`, keyboard focus, and an accessible label. Click or Enter/Space calls `document.getElementById("step-" + i).scrollIntoView({ block: "center" })`, flashes that exact transcript row, and leaves a visible `jumped #<i>` receipt in the badge. Anchors are not hidden by replay position.

## Scaling

The badge remains **316 × 232 px** regardless of 353, 562, or 932 records; it has `overflow: hidden` and no internal scrolling. The transport is outside this footprint. The rings are ratio-based and the burn comb remains 30 real-time bins, so neither grows with transcript length.

Prompt markers remain one DOM target per human prompt to preserve the one-click guarantee. The present real maximum is 11. In a production session with far denser prompts, targets should be packed into additional shallow radial lanes—not aggregated—while the backing transcript can be virtualized as long as `step-<i>` navigation remains addressable.

## What real data changed versus the synthetic rounds

- There are **no compaction/checkpoint events**, so there is no fold arc, fold count, or invented context reset.
- There are **no watch fires**, so there is no runaway-watch alarm.
- Session B has no jobs or delegates; the instrument explicitly treats this as a valid leaf state.
- Real timestamp gaps remain gaps rather than being normalized into uniformly spaced activity.
- Token totals and p95 pressure come from actual `in`/`out` fields; no synthetic token budget is claimed.
- Tool flight exists only where real `ASSISTANT → TOOL_RESULTS` timestamps support it. Many real intervals are very short; the seek control makes them inspectable without lengthening them.
- Error, cancellation, failure, and still-running notches come only from real call/job fields.

## Chrome verification (single pass, 100% scale)

Verified at a 1280 × 720 CSS-pixel viewport with `visualViewport.scale === 1` and `devicePixelRatio === 1`:

- Measured badge border box: **316 × 232 px**, inset 18 px right/bottom, `overflow: hidden`.
- `before.png` and `after.png` are tight badge crops from the running 600× replay; Session A advanced from turn **238/353** to **297/353**, with ring, tokens, wall time, jobs, burn needle, and LIVE state updating.
- `prompt-jump.png` records a click on prompt marker **#233**. The badge receipt says `jumped #233`, its tooltip shows the real prompt, and exact row `step-233` (`USER_INPUT`: “it needs to let me get to any user prompt step, at least”) is in the viewport.
- Seeking halfway between real Session A records #44 and #45 reconstructed `shell` as `CURRENT TOOL · REAL INTERVAL`, showing **running 01:00 / 02:00**.
- The switcher rendered B with 562 rows, 4 prompt targets, `0 / 0 jobs`, and zero children; C rendered 932 rows, 11 prompt targets, and 238 jobs.
