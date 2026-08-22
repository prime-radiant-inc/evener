# Real-data Micro Strata Strip

## Comprehension question

**Where did this session spend real wall-clock time, what is active at the replay head, and which user prompts anchor the work?**

`idea-1.html` is the horizontal, bottom-docked counterpart of the selected Micro Strata Rail. It is a self-contained `file://` prototype with all three supplied real-session JSON objects embedded inline and a three-way session switcher.

## Encoding

The strip is a wall-clock ribbon, not a turn-index histogram. Horizontal position is the event's real timestamp; therefore broad empty regions are genuine idle gaps.

- **TURN** — one vertical microbar per real stream record. Cyan is assistant activity, dark teal is tool results, blue is `USER_INPUT`, muted ochre is daemon/system steering, pink is attention resolution, and slate covers environment/system records.
- **CALLS** — one microtick per recorded tool call at its assistant-turn timestamp. An outlined moving segment means the calls are in flight between that real `ASSISTANT` timestamp and the next real `TOOL_RESULTS` timestamp.
- **ERROR** — red diamonds are calls whose source record has `e: true`; red crosses are failed jobs at their real end timestamps.
- **JOBS** — spans use the exported `started` and `ended` timestamps. Green completed, amber cancelled, red failed, and bright outlined green still running at the replay head.
- **DELG** — violet spans begin at each successful real `delegate` tool invocation. Successful invocations map one-to-one to `children` in creation order; a child closes at its first real delegate-notification timestamp, matched by the shared 10-character time-derived prefix of child session ID and delegate ID. All 47 spans in these files close this way.
- **White needle + LIVE NOW cluster** — the replay head, current stream step, exact recorded time, idle-gap duration or in-flight tool names, and live counts for calls/jobs/delegates.
- **Prompt diamonds** — blue for every `USER_INPUT`, amber for every `STEERING` record whose `steer_src === "user"`. They are individual elements and are never binned or aggregated.

There is deliberately no compaction or watch encoding: these three real files contain neither compaction events nor watch fires.

## Update model

The replay head advances through actual elapsed milliseconds. Playback controls sit outside the 96px component and provide play/pause, restart, 60× / 600× / 3600× speed, and continuous seek. Seeking rebuilds state solely from source timestamps.

A tool call is considered in flight only during the observable interval from its `ASSISTANT` record to the next `TOOL_RESULTS` record. Job and delegate activity is recomputed by interval containment at the replay head. If no stream event or active interval occurs, the now cluster explicitly reports an `IDLE GAP` rather than smoothing or skipping it. No random or synthetic event generator is present.

## Scaling

The footprint remains fixed while the time domain expands. Event marks become a barcode texture at long durations; jobs and delegate spans cycle through four subtracks to preserve overlap cues. Prompt markers are the exception to compression: every qualifying prompt remains a separate 14×18px SVG hit target, even when visual diamonds overlap. The component has no internal scrolling.

For much longer sessions, the same encoding can retain wall-clock fidelity while adding semantic zoom on hover or a selected time window; prompt hit targets must still remain discrete and addressable.

## Exact footprint

**96px high × the full available transcript width.** The timeline occupies the flexible width and the live-now cluster occupies 244px inside that same 96px dock. The transport bar and 29px legend/caption are explicitly outside the component budget.

## Prompt jump mechanism

Prompt markers appear when their real timestamp enters session-so-far and remain thereafter (unless the operator intentionally seeks back before that timestamp). Click or keyboard-activate any diamond to call `jumpTo(step)`: it locates `#step-<session-id>-<exact-i>`, scrolls the dimmed mock transcript row to center, adds a selection/flash state, shows `JUMPED TO EXACT STEP #…`, and records the step in `document.body.dataset.lastJump` for testability.

Across the three datasets this yields **23 permanent anchors**: 8, 4, and 11 respectively. The leaf session's fourth anchor is the only user-sourced steering record in the supplied data.

## What real data changed versus the synthetic rounds

- Real elapsed time is highly uneven, including multi-hour blank gaps; the strip's texture is consequently sparse in places instead of uniformly busy.
- There are **no compaction events and no watch fires**, so neither is implied or backfilled.
- The leaf worker has **zero jobs and zero delegates**. Those lanes display an explicit neutral “leaf session · no … in source” label rather than an error state.
- Steering is frequent, but nearly all steering records are daemon notifications with no `steer_src: "user"`; only the one explicitly user-sourced record becomes an amber prompt anchor.
- Density varies sharply: the three sessions contain 0 / 18 / 238 jobs and 0 / 8 / 39 delegate children. The large tree therefore reads as a dense woven band while the leaf remains intentionally quiet.
- Error marks use only exported call error flags and failed job statuses. No illustrative failure, fold, watch, or activity was added.
