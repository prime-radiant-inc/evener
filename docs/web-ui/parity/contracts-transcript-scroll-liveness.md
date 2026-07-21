# Transcript / scroll / liveness / thinking / streaming — jstest behavior contracts

Mined from `cmd/serf-hub/jstest/` for a future Vitest rewrite. Each line is one observable
behavior a new suite must re-cover, tagged with the jstest file that currently pins it (not
line numbers — see the sibling `parity-m4-transcript.md` for source-level file:line citations
against `renderer.js`/`renderer-tools.js`/`renderer-format.js`; this document is derived purely
from the *test* suite's black-box assertions, not from reading the implementation).

**Scope.** All 49 `test-renderer*.js` files, in full. Plus eight "friends" whose subject is
squarely transcript/streaming/liveness content even though they aren't `test-renderer`-prefixed:
`test-transcript-windowing.js`, `test-tool-streaming-output.js`, `test-diff-line-kind.js`,
`test-mcp-error-marker.js`, `test-system-churn.js`, `test-realistic-flow.js`,
`test-turn-meta-badge.js`, `test-tool-meta-timing.js`. One pure CSS-token file in scope
(`test-transcript-typography.js`) is noted by name only, per instructions.

**Deliberately excluded** (belong to adjacent feature areas, presumably covered elsewhere):
`test-optimistic-rendering.js` (pending/steer-chip registry — `pending.js`/`appwire.js`, not
`renderer.js`); the whole `test-appwire-*.js` family (wire-protocol/transport mapping layer);
`test-ask-card.js` / `test-ask-compose.js` / `test-ask-submit.js` (the ask_user tool feature);
`test-panes*.js`, `test-doc-pane-open-beside.js`, `test-thread-document-bridge.js` (document
panes); `test-subagents.js` / `test-subagent-nav.js` (the subagent aggregation module — distinct
from the renderer-side glyph/open-beside contracts already covered here); `test-settings*.js`,
`test-composer-status-compact.js`, `test-panel-history-teardown.js`, `test-thread-state.js`,
`test-live-title-updates.js`, `test-panels.js`, `test-icons.js`, `test-model-display.js`.

---

## 1. Base event-replay contract

- A cold event replay renders exactly one user message with its text and any inline data-URL image thumbnail. (test-renderer.js)
- A user message carries a quiet "You" tag rendered outside the message pill, never mixed into the pill's own text. (test-renderer.js)
- SYSTEM-REMINDER current-task steering is fully suppressed — no steering divider renders for it. (test-renderer.js)
- A `task_list` update collapses to one task-update card naming the task by description (not `#id`); no separate system-line prose and no redundant "now on" line accompany it. (test-renderer.js)
- `task_list` tool calls never render as a generic tool-call card — only as the task-update card. (test-renderer.js)
- The `communicate` tool renders its message as exactly one assistant-message bubble. (test-renderer.js)
- A standard tool call (e.g. `exec_command`) renders a human-readable intent, a machine command verb/target, a human result summary, and a preformatted shell-output block together on one row. (test-renderer.js)
- An empty `/tasks` fetch leaves the tasks badge absent rather than throwing. (test-renderer.js)
- The script-tag order in the shipped app template must exactly match the renderer test harness's own module load order, so a module added to one list but not the other fails loudly instead of silently breaking the app or every renderer test. (test-renderer-file-mirror.js)

## 2. Cold start & session bootstrap

- An empty session renders a bare welcome pane with no tagline or example-prompt clutter. (test-renderer-cold-start.js)
- Sending from an empty session dissolves the welcome pane, optimistically echoes the user's message, and shows a skeleton placeholder in the reply gap. (test-renderer-cold-start.js)
- The send-to-first-frame gap shows a calm "starting…" liveness line, never a fabricated multi-stage "Connecting…→Thinking…" narration. (test-renderer-cold-start.js)
- `TURN_STARTED` alone keeps the skeleton gap state; only the first real frame (`ASSISTANT_TEXT_START`) removes the skeleton. (test-renderer-cold-start.js)
- Cold hydration pages backward through older turns until primary user/assistant dialogue is visible in the window, then stops and retains the remaining older-turns cursor. (test-renderer-primary-hydration.js)
- An initial window that already contains dialogue never fetches older pages (single-read fast path preserved). (test-renderer-primary-hydration.js)
- The bounded 40-turn read window is retained even while paging backward for primary dialogue. (test-renderer-primary-hydration.js)

## 3. Hydration replay (history load / reconnect)

- Hydration replay yields to the event loop between chunks instead of blocking synchronously, so a timer scheduled before it starts can fire mid-replay. (test-renderer-hydration-chunked.js)
- A live notification arriving mid-chunked-replay buffers and appears after all hydrated content, never interleaved into it. (test-renderer-hydration-chunked.js)
- Swapping sessions mid-chunk aborts the in-flight replay cleanly without throwing. (test-renderer-hydration-chunked.js)
- After a reconnect, buffered live events replay after re-hydration completes, each exactly once, in order. (test-renderer-hydration-chunked.js)
- A reconnect landing mid-chunk resets the half-rendered transcript before re-hydrating rather than appending a duplicate replay on top of it. (test-renderer-hydration-in-progress-guard.js)
- The settings status-visibility toggle firing mid-chunk defers to the in-progress hydration instead of resetting/re-rendering/declaring-complete underneath it. (test-renderer-hydration-in-progress-guard.js)
- A near-top scroll firing mid-chunk does not double-fetch the older-turns cursor that hydration itself is about to page in. (test-renderer-hydration-in-progress-guard.js)
- In every mid-chunk race (reconnect, settings toggle, near-top scroll), each hydrated transcript item ends up rendered exactly once, in hydration order. (test-renderer-hydration-in-progress-guard.js)
- Historical task descriptions render gated behind pending description lookups for both the snapshot and any buffered live transcript items. (test-renderer-hydration-order.js)
- A resolved task description replaces the numeric `#id` label on the historical plan card in place, without duplicating transcript items or the task card. (test-renderer-hydration-order.js)
- Out-of-order task-description responses never let an older in-flight request's data overwrite a newer request's cached metadata, label, or badge. (test-renderer-hydration-order.js)
- Task-description metadata is cached per session; a stale response for a since-abandoned session must not mutate a different session's cache. (test-renderer-hydration-order.js)
- When cached task metadata resolves before the transcript replay reaches that task, the later transcript render picks up the cached description/status/note instead of the raw transcript-derived placeholder. (test-renderer-hydration-order.js)
- A description that merely starts with another task's hash-style label (e.g. "#202") is never misattributed to that other task. (test-renderer-hydration-order.js)
- Re-initializing the renderer for a new session drops any guarded task-description state left over from the prior session. (test-renderer-hydration-order.js)
- With no reader scroll during hydration, the hydration-end settle parks the viewport at the bottom. (test-renderer-hydration-scroll-intent.js)
- A reader who scrolls up mid-replay is never yanked back to the bottom when hydration completes. (test-renderer-hydration-scroll-intent.js)
- Reader-scroll intent resets between hydrations — a later hydration with no scroll sticks to the bottom again. (test-renderer-hydration-scroll-intent.js)
- The asynchronous scrollTop-correction events fired by prepending older turns mid-hydration must never be misread as reader scroll intent (which would wrongly suppress the hydration-end settle). (test-renderer-hydration-scroll-intent.js)
- Per-event stick/scroll measurement and the new-content-pill counter are both suppressed during a hydration replay, and resume immediately once it ends. (test-renderer-hydration-settle.js)
- Hydration triggers exactly one scroll settle, after all replayed and buffered-live events have rendered — not one per event. (test-renderer-hydration-settle.js)
- A failed hydration still clears the scroll-suppression flag so the page can't end up permanently wedged. (test-renderer-hydration-settle.js)
- `resetTranscriptReplay` also clears the scroll-suppression flag. (test-renderer-hydration-settle.js)
- A superseded hydration that resolves after a newer hydration already completed returns without touching state or the DOM — it must not reset or replay into the newer transcript. (test-renderer-hydration-stale-overlap.js)
- A stale hydration's rejection (`.catch`) likewise leaves the newer stream's connection state and transcript untouched. (test-renderer-hydration-stale-overlap.js)
- When a stale, mid-replay hydration aborts after a newer hydration has taken over, only the newer hydration may clear the scroll-suppression flag — the older one's abort must not re-enable per-event settling underneath the newer replay. (test-renderer-hydration-stale-overlap.js)

## 4. Lazy paging & prepending older turns

- Older turns are fetched via `listTurns` only when an older-turns cursor exists, and prepended above the live content without disturbing in-progress state at the bottom. (test-renderer-lazy-turns.js)
- Overlapping near-top-scroll triggers while a page is already loading collapse to exactly one fetch. (test-renderer-lazy-turns.js)
- Once the cursor reaches the head of history, no further older-turns fetch is issued. (test-renderer-lazy-turns.js)
- Prepending older turns must not reset the live liveness clock, the live streaming message id, or the live last-user-text. (test-renderer-lazy-turns.js)
- A prepended historical `TURN_COMPLETED` must not finalize or otherwise corrupt the live, still-streaming turn. (test-renderer-lazy-turns.js)
- A historical `USER_INPUT` inside a prepended page must not settle or detach the anchor of a live pending `ask_user`. (test-renderer-lazy-turns.js)
- A historical reasoning stream inside a prepended page must not hijack the live reasoning element or buffer — live reasoning keeps streaming into its own block. (test-renderer-lazy-turns.js)
- A historical `ask_user` inside a prepended page must not switch the live composer into ask mode, hide the live input, build a response dock, steal focus, or light the needs-you dock; the historical question still renders into the prepended history itself. (test-renderer-lazy-turns.js)
- A completed delegate spawn inside a prepended page must not overwrite the live document-level subagent breadcrumb/rollup chip; the historical spawn still renders its own row in the prepended history. (test-renderer-lazy-turns.js)
- A historical `task_list` card inside a prepended page must not trigger a live `/tasks` re-fetch. (test-renderer-lazy-turns.js)
- A scroll-triggered prepend preserves the live new-content pill's count, painted count, jump target, and debounce timer; a zero-event prepend leaves a pending pill timer completely untouched. (test-renderer-lazy-turns.js)
- Every document-level side-effecting helper reachable from a replay event must no-op while a staging (prepend) replay is in progress. (test-renderer-lazy-turns.js)
- After a prepend's scroll-position drift correction, the programmatic-scroll hold releases exactly one animation frame later than the correction itself, not in the same frame and not on a later task. (test-renderer-prepend-settle-release.js)
- A scroll event dispatched between the drift correction and its release frame must not register as reader-scroll-intent; one dispatched after the release must. (test-renderer-prepend-settle-release.js)
- If the settle callback is cancelled, its held scroll-depth is still drained by the surviving release callback — no hold is ever stranded. (test-renderer-prepend-settle-release.js)
- If the release callback itself is cancelled (superseded by a second settle), the later surviving release still drains all accumulated holds. (test-renderer-prepend-settle-release.js)
- Re-initializing the renderer for a new session retires the prior session's pending settle/release frames so a stale callback can never fire into the new session and misread its scroll state. (test-renderer-prepend-settle-release.js)
- Prepending older turns schedules exactly one next-frame callback to run the settle/scroll correction, not one per prepended item. (test-transcript-windowing.js)
- *(test-transcript-windowing.js also pins a CSS-token contract — conversation children get `content-visibility: auto` with a remembered `contain-intrinsic-size` — style-only, not re-covered behaviorally.)*

## 5. Scroll behavior & new-content affordance

- Frames appended while the reader has scrolled up must not move the viewport. (test-renderer-scroll.js)
- Frames appended while the reader is already at the bottom stick the viewport to the new bottom. (test-renderer-scroll.js)
- Multiple scroll events firing within one tick coalesce to at most one scroll-affordance handler run per animation frame. (test-renderer-scroll-throttle.js)
- A same-tick `scheduleFrame` call on an unrelated key (e.g. prepend-settle) must not cancel the pending scroll-affordance frame. (test-renderer-scroll-throttle.js)
- The error-anchor query is cached between calls and only invalidated by a `TOOL_CALL_END` or a session swap. (test-renderer-scroll-throttle.js)
- Re-initializing on a fresh conversation element (session swap) invalidates the error-anchor cache and the dirty-assistant-message set, so a prior session's detached anchors never leak a phantom error pill into the new session. (test-renderer-scroll-throttle.js)
- New rendered content arriving while the reader is scrolled up shows a "↓ N new" pill counting only entries that rendered since the reader left the bottom; no pill appears while already at the bottom. (test-renderer-new-content-pill.js)
- Suppressed/no-op events (e.g. a dropped `communicate` call) never bump the pill's counter. (test-renderer-new-content-pill.js)
- Clicking the pill scrolls to the bottom and clears it; scrolling back to the bottom manually also clears it. (test-renderer-new-content-pill.js)
- New content arriving under an awaiting/attention state reads "↓ needs you" instead of a plain count, including when the awaiting flip arrives in a later frame than the content that produced it — the pill upgrades in place. (test-renderer-new-content-pill.js)
- An error anchor below the fold produces a red icon pill (never a literal "✕") reading "error", with the arrow pointing toward the anchor (↓ below, ↑ once scrolled past it); error outranks a simultaneous needs-you state. (test-renderer-new-content-pill.js)
- A finalized tool call is marked as an error anchor only when it actually failed. (test-renderer-new-content-pill.js)
- The plain count is debounced: it never repaints to the final total mid-burst, never renders "↓ 0 new" during the debounce window (first paint seeds from the live count), and settles to the accurate total once the burst quiets. (test-renderer-new-content-pill.js)
- On init, the workspace's `--workspace-visible-height` custom property is set synchronously from the current `visualViewport` height. (test-renderer-viewport-dock.js)
- A `visualViewport` resize/scroll event schedules one animation frame that applies the new visible height when it runs. (test-renderer-viewport-dock.js)
- Re-initializing against a replacement workspace/conversation element removes the old `visualViewport` listeners, binds fresh ones, and synchronously stamps the new workspace's visible height. (test-renderer-viewport-dock.js)
- A stale viewport-resize callback scheduled before a session swap must not mutate the replacement session's workspace once it fires. (test-renderer-viewport-dock.js)

## 6. Liveness / stall detection

- Any incoming frame (including reasoning) stamps a `lastFrameAt` clock used to detect staleness. (test-renderer-liveness.js)
- A working session with no frames for longer than the stall threshold shows an honest "no updates for Ns" notice, sets a stalled flag on the conversation, and stops the reassuring status-dot pulse. (test-renderer-liveness.js)
- A fresh frame clears the stall notice, clears the stalled flag, and resumes the dot pulse. (test-renderer-liveness.js)
- An idle (not working) session never shows the "no updates" stall notice, however long the gap. (test-renderer-liveness.js)
- A calm-quiet gap (below the stall threshold) shows a coarse, quantized time bucket ("quiet ~30s", rolling to "~1m") — never an exact rising per-second counter — and the breathing pulse continues. (test-renderer-liveness.js)
- Crossing the stall threshold escalates to an amber "may be stalled" concern band with a colorblind-safe glyph and drops the pulse; a fresh frame recovers from concern back to normal. (test-renderer-liveness.js)
- The liveness line is driven off the inline status-row `[data-liveness]` span; an htmx innerHTML-swap of that row's container detaches the old span, and the renderer must not write into the detached node before re-acquiring the fresh one on `htmx:afterSwap`. (test-renderer-liveness.js)
- An `htmx:afterSwap` on an unrelated target must not cause the renderer to re-acquire (and potentially mis-bind) the liveness handle. (test-renderer-liveness.js)
- Self-heal (re-subscribe/re-hydrate) is driven off the same inline status-row liveness span used for display, not a separate mechanism. (test-renderer-liveness-selfheal.js)
- The calm-quiet band never triggers a self-heal. (test-renderer-liveness-selfheal.js)
- Crossing into the concern band triggers exactly one self-heal; remaining in concern on subsequent ticks does not re-fire it. (test-renderer-liveness-selfheal.js)
- A new concern episode after a recovery self-heals again (the trigger re-arms after recovery). (test-renderer-liveness-selfheal.js)
- Without an active live stream (e.g. viewing a past session), silence never triggers a self-heal. (test-renderer-liveness-selfheal.js)

## 7. Streaming assistant text (deltas, tails, coalescing, finalize)

- Multiple assistant-text deltas landing before a settle coalesce to exactly one markdown parse per settle, not one parse per delta. (test-renderer-delta-coalescing.js)
- Below the 4KB threshold, streamed assistant text renders normally (parsed) via the settle path. (test-renderer-streaming-tail.js)
- At the 4KB crossing, the head renders exactly once as the first ≤4096 parsed characters and then freezes; the remainder (including the crossing delta's own overflow) streams as raw, un-parsed text into a `.streaming-tail`, and head+tail always reassemble to the exact buffer. (test-renderer-streaming-tail.js)
- Once in tail mode, further deltas append raw text with no re-parse, inserted ahead of a real (not pseudo-element) `aria-hidden` caret span that always stays last. (test-renderer-streaming-tail.js)
- Finalization replaces the frozen head + raw tail with one fully parsed render and removes the tail/caret nodes. (test-renderer-streaming-tail.js)
- A single flush containing both the last pre-crossing delta and the crossing delta together (batched live path) still renders the head exactly once before freezing it. (test-renderer-streaming-tail.js)
- A single first delta larger than 4KB still gets its first ~4KB parsed at the switch (not left fully raw until finalization), with the remainder streaming raw. (test-renderer-streaming-tail.js)
- A UTF-16 surrogate pair straddling the 4096-char boundary backs the boundary off by one code unit so neither the frozen head nor the tail ever renders a split pair (no U+FFFD). (test-renderer-streaming-tail.js)
- If the head is already settled at exactly the boundary length when the crossing delta arrives (no new head content), the crossing must not re-render/replace the head DOM node — preserving any active text selection. (test-renderer-streaming-tail.js)
- If `marked.parse` throws at finalization, the message falls back to plain text (`textContent`) instead of letting the exception escape. (test-renderer-streaming-tail.js)
- An 8000-char live/final tail slice (shell and read_file tool output, both streaming and at bodyEnd) never starts mid-surrogate-pair; the boundary backs off to the next whole code unit so no U+FFFD ever appears at the head of a folded tail. (test-renderer-surrogate-tail.js)
- A folded/elided tail is prefixed with an explicit "not retained" note rather than a bare "…" that could be mistaken for real output. (test-renderer-surrogate-tail.js)
- The shared `tailSlice` helper returns short text untouched, drops an orphaned low surrogate at a hard cut, and is exported for reuse by every tool renderer that needs a live tail. (test-renderer-surrogate-tail.js)
- A `communicate` tool call landing while its matching agentMessage is still streaming is deduped by comparing against the raw source buffer, not the rendered DOM text — so it still matches once raw markdown (e.g. `**bold**`) has crossed into the un-rendered `.streaming-tail` and no longer textually matches the parsed candidate. (test-renderer-communicate-dedup.js)
- `TURN_COMPLETED` with no prior `ASSISTANT_TEXT_END` finalizes the in-progress message from its accumulated text buffer (interrupt case) and appends the turn-meta badge. (test-renderer-idempotent-finalize.js)
- A late `ASSISTANT_TEXT_END` for an already-finalized message replaces that same block in place (same node, badge and turn id preserved) rather than appending a duplicate. (test-renderer-idempotent-finalize.js)
- A `TURN_COMPLETED` for a turn id that doesn't match the currently-streaming message must not finalize that message — later deltas for the real active turn keep landing in it, with no turn-meta badge attached from the mismatch. (test-renderer-idempotent-finalize.js)
- A late END arriving after tool-call rows, a `SESSION_END` banner, or a coalesced `.system-run` have already followed the finalized block must still replace that same block in place, not append a duplicate — the trailing banner/system-run remains last. (test-renderer-idempotent-finalize.js)
- A late END with no active message, where the previously-finalized block is no longer the transcript's tail element, must not clobber that older block — it lands as its own new block at the tail instead. (test-renderer-idempotent-finalize.js)
- Finalizing an assistant message that started but produced no text drops the "replace on late END" pointer entirely (no residue). (test-renderer-idempotent-finalize.js)
- An `item/agentMessage/reset` notification maps to exactly one `ASSISTANT_TEXT_RESET` event carrying the item id. (test-renderer-assistant-reset.js)
- `ASSISTANT_TEXT_RESET` discards the in-progress (partial) assistant message entirely, so a retried model call's output replaces rather than appends to it. (test-renderer-assistant-reset.js)
- After a reset and a fresh full retry stream, exactly one assistant message exists holding the retry's complete output, not the discarded partial. (test-renderer-assistant-reset.js)
- Live-delivered deltas queue in a frame queue and apply together with a single settle only once flushed — nothing renders before the flush. (test-renderer-frame-batching.js)
- A transcript reset invalidates any events still queued from before it — they never apply after the reset. (test-renderer-frame-batching.js)
- `scheduleFrame` falls back to a timer when `requestAnimationFrame` is unavailable (plain jsdom / exotic embedded webviews). (test-renderer-schedule-frame.js)
- `cancelFrame` drops a pending callback before it runs. (test-renderer-schedule-frame.js)
- Different keys hold independent pending slots — scheduling one key never cancels another key's pending callback. (test-renderer-schedule-frame.js)
- Re-scheduling the same key cancels only that key's earlier pending callback. (test-renderer-schedule-frame.js)
- Calling `cancelFrame()` with no key cancels every keyed pending slot. (test-renderer-schedule-frame.js)
- A `visibilitychange` firing before a scheduled frame flush still drains the pending event queue when the tab hides. (test-renderer-visibility-flush.js)
- While the tab stays hidden with no further `visibilitychange`, a queued delta is held until a hidden-path timer (250ms) drains it on its own, since rAF never fires while hidden. (test-renderer-visibility-flush.js)
- Returning to visible drains the queue immediately again, in either direction of the transition. (test-renderer-visibility-flush.js)
- Streaming shell tool output writes directly into one `<pre>` in place per delta — no fold-chrome rebuild while streaming — and the wrap carries `.streaming` for CSS clamping. (test-tool-streaming-output.js)
- Fold chrome (the "show more" affordance) is built exactly once, at `bodyEnd`, after `.streaming` is dropped; the head shown is the first 5 lines. (test-tool-streaming-output.js)
- While streaming past the 8000-char budget, the pane shows the live tail (last 8000 chars), not a frozen head, so a long-running command never looks stalled. (test-tool-streaming-output.js)
- At `bodyEnd`, an over-budget output folds to the tail with an explicit "earlier output not retained" prefix (never a bare "…"), keeping the same last-8000-chars tail the user was already watching, ending on the output's true final line. (test-tool-streaming-output.js)
- The `read_file` tool renderer shares the exact same live-tail streaming and bodyEnd-fold behavior as shell. (test-tool-streaming-output.js)
- Three shell deltas batched into one frame-flush write the `<pre>` exactly once, with the final accumulated text — both via the batched live path and via a synchronous `handleData` call. (test-tool-streaming-output.js)

## 8. Thinking / reasoning blocks

- `REASONING_START` opens one quiet `.think` block that streams fully open (not collapsed) while it is the current turn's thought. (test-renderer-thinking.js)
- Reasoning deltas append into the same open block's body — one block total, not one per delta. (test-renderer-thinking.js)
- Once the assistant starts answering, the thinking block collapses to a "Thought for Ns" summary carrying a noun-phrase gist of the thought and a duration-tier class. (test-renderer-thinking.js)
- An empty reasoning block (no deltas ever arrived) leaves no residue at turn-complete — it is removed rather than shown empty. (test-renderer-thinking.js)
- Resetting the transcript (e.g. before a reconnect replay) drops the stale reasoning element handle and clears the reasoning buffer, so a fresh reasoning stream after the reset renders into the live DOM instead of silently no-op'ing on a detached node. (test-renderer-thinking.js)
- Reasoning deltas append as new text nodes (no full-buffer rewrite) — one text node per delta. (test-renderer-reasoning-append.js)
- The collapsed/preview tail always shows the newest ~200 characters of the reasoning buffer, never a stale frozen prefix, even once the buffer exceeds the preview's slice window. (test-renderer-reasoning-append.js)

## 9. Turn lifecycle & per-turn metadata

- A provider-sourced turn failure renders as a red diagnostic end-cap inside the transcript (not chrome), with a human-readable summary as the primary line and no leading "[error]" prefix. (test-renderer-turn-failure.js)
- The raw `[error]` provider text (including any request id) is available in a mono block behind a collapsed-by-default expandable detail. (test-renderer-turn-failure.js)
- The end-cap always offers a Retry action when the user had a turn to replay. (test-renderer-turn-failure.js)
- The end-cap's taxonomy badge is derived from `cause.kind`, overriding a generic stored `source` (e.g. `source:"serf"` with a provider cause still badges "Provider …"). (test-renderer-turn-failure.js)
- A `turn/completed` notification maps to a `TURN_COMPLETED` event carrying both the turn id and the full turn object, not just the id. (test-turn-meta-badge.js)
- The assistant message that closes a turn is stamped with `data-turn-id`, and gains a `.turn-meta` badge (duration, token counts, cost) once `turn/completed` lands. (test-turn-meta-badge.js)
- The badge reuses the existing duration-format convention and renders the cost inside a nested `.cost` child span. (test-turn-meta-badge.js)
- With the Show-cost setting off, the badge's title/tooltip never leaks the cost figure, and no dangling separator is left where the cost span is hidden. (test-turn-meta-badge.js)
- A live steering notification carrying `source:"user"` maps through appwire to a `STEERING_INJECTED` event that preserves `source:"user"`; system-originated steering carries no source. (test-renderer-steering-source.js)
- User-originated steering (`source:"user"`) renders as a normal user message bubble (with the "You" tag and any images), never as the collapsible "steering injected" divider. (test-renderer-steering-source.js)
- System-originated steering (no source) keeps the collapsible divider treatment. (test-renderer-steering-source.js)
- Hydrated (historical) steering items carry `item.source` through the same mapping as live notifications, so a past user-sent steer replays as a user bubble and a past system nudge replays as a divider. (test-renderer-steering-source.js)
- The tool-row hover meta (timestamp · runtime) always reflects server-provided `startedAt`/`completedAt`/`durationMs`; it is never synthesized from the client's wall clock. (test-tool-meta-timing.js)
- When an event carries no server timing at all, the meta renders empty rather than inventing a time. (test-tool-meta-timing.js)
- A derived runtime is shown only when computed from real server stamps, and a same-second (0s) derived span is omitted as too coarse to state honestly. (test-tool-meta-timing.js)
- *(test-tool-meta-timing.js also pins a CSS-layout contract — the meta is positioned absolute/out-of-flow so revealing it on hover/focus/completion never reflows the row's text — style-only.)*

## 10. Tool-call rendering

- With no submitted turn payload recorded, no recovery action is offered even for a retry-eligible error source. (test-renderer-diagnostic-actions.js)
- A provider-source error offers a single "Retry turn" action that resubmits the original payload. (test-renderer-diagnostic-actions.js)
- A hub-source error offers a single "Reconnect & retry" action instead of "Retry turn". (test-renderer-diagnostic-actions.js)
- Other error sources (serf, ui) offer no action button at all, including when the local hub itself fails. (test-renderer-diagnostic-actions.js)
- A provider failure after some partial agent work in the turn offers "Continue" (from the existing transcript) instead of replaying the original prompt/attachments. (test-renderer-diagnostic-actions.js)
- A standalone background-job completion (e.g. a delegate/shell `JOB_FINISHED`) landing in a fresh, otherwise-empty turn must not count as "current-turn work" — the recovery for a subsequent provider failure is still "Retry turn" with the original payload, not a work-discarding "Continue". (test-renderer-diagnostic-actions.js)
- Submitting a new turn via the optimistic local-echo path resets the per-turn "has work" flag immediately, so a provider failure racing the server round-trip recovers the new turn, not the previous one. (test-renderer-diagnostic-actions.js)
- The submitted-turn payload is snapshotted before awaiting `startTurn`, so a provider failure arriving during that await still offers Retry with the freshly submitted payload, not a stale one. (test-renderer-diagnostic-actions.js)
- The recovery-action onclick handler calls `SerfAppwire.startTurn` directly (never falls back to a bare fetch) with the exact ref/text/images captured at submit time. (test-renderer-diagnostic-actions.js)
- Retry/reconnect failure banners are worded per source ("retry failed:" vs "reconnect failed:") and carry the matching diagnostic title. (test-renderer-diagnostic-actions.js)
- When `SerfAppwire` is unavailable at click time, the retry/reconnect path shows a diagnostic banner naming "appwire unavailable" instead of silently falling back to a legacy `/send` fetch. (test-renderer-diagnostic-actions.js)
- Multiple diff-output deltas for one tool call landing inside a single batched flush coalesce to exactly one diff re-render at settle (last output wins), not one render per delta. (test-renderer-diff-delta-coalescing.js)
- Coalescing is per-frame, not a one-time latch — the next frame's deltas render again. (test-renderer-diff-delta-coalescing.js)
- A still-pending coalesced delta must never clobber `TOOL_CALL_END`'s authoritative final render when both land in the same frame. (test-renderer-diff-delta-coalescing.js)
- Cheap single-textContent tool renderers (e.g. shell) coalesce the same way: multiple deltas in one frame produce one write with the accumulated output. (test-renderer-diff-delta-coalescing.js)
- A delta keyed by `call_id` and a later `TOOL_CALL_END` keyed by `item_id` alias to the same tool state — a stale deferred delta entry must not re-render over the finalized output even when the two events use different key fields. (test-renderer-diff-delta-coalescing.js)
- Tool-output image descriptors render as an inline thumbnail wrapper under the owning tool row. (test-renderer-output-images.js)
- An invalid/protocol-relative image descriptor in a mixed set is omitted rather than rendered broken; an external-only protocol-relative descriptor renders no image wrapper at all. (test-renderer-output-images.js)
- Clicking a tool-output image opens the shared lightbox. (test-renderer-output-images.js)
- An MCP-namespaced tool name (e.g. `server__tool`) with no dedicated renderer still falls through to the default renderer, and a server-reported error on that call still renders the error marker, not "ok". (test-mcp-error-marker.js)
- An MCP-namespaced tool call with no error still renders the ok marker via the same default-renderer fallback. (test-mcp-error-marker.js)
- `edit_file` diff output renders every diff line with a `data-line-kind` of `add`, `del`, or `ctx` as appropriate. (test-diff-line-kind.js)
- `write_file` output rendered through the diff renderer marks `+` lines as `add` and other lines as `ctx`. (test-diff-line-kind.js)
- `apply_patch` hunk header (`@@`) lines are marked `data-line-kind="hunk"`. (test-diff-line-kind.js)
- Diff header lines (`+++`/`---`) never get an `add`/`del` line-kind. (test-diff-line-kind.js)

## 11. Task / plan cards

- Appending N new tasks in one call renders one task-update card with one row per newly appended task. (test-renderer-advanced.js)
- A multi-task update in one call renders only the rows that actually changed on the card, with descriptions seeded from the most recent full-list steering snapshot. (test-renderer-advanced.js)
- A bare full-list steering (no `task_list` mutation) seeds the description cache silently — it renders neither a pointer nor a card. (test-renderer-advanced.js)
- Loop-detection steering still renders as a normal steering divider. (test-renderer-advanced.js)
- `action:"view"` `task_list` calls are fully suppressed — no card, no divider, no tool-call row. (test-renderer-advanced.js)
- Marking several tasks done in one call renders each as a flagged "touched-done" row, and the card's progress count reflects all of them (e.g. "3/3"). (test-renderer-advanced.js)
- A cancelled task renders as a flagged "touched-cancelled" row carrying its cancellation note. (test-renderer-advanced.js)
- Bracketed descriptions (e.g. ending in `[WIP]`) in a full-list steering are not clipped by the parser. (test-renderer-advanced.js)
- A reasoning-effort suffix (`[high]`, and the expanded `[minimal]`/`[max]` vocabulary) is stripped from the parsed full-list description. (test-renderer-advanced.js)
- `task-nudge` steering is suppressed like other bookkeeping steering. (test-renderer-advanced.js)
- When the daemon auto-advances the next task to in_progress inside the same `task_list` call, the card shows that task as current and the trailing "now on X" steering line is suppressed. (test-renderer-advanced.js)
- An update naming a task id with no seeded description and no State snapshot still renders, falling back to labeling the row "#N". (test-renderer-advanced.js)
- A `THREAD_STATUS_CHANGED` to active immediately clears any stale idle send/queue capability on the send button rather than carrying over capabilities from the prior idle `SESSION_START`. (test-renderer-advanced.js)
- Consecutive `SYSTEM_MESSAGE` prompt/prompt-loaded/round-timing blocks each render their own disclosure with the correct summary label and body. (test-renderer-advanced.js)
- A saved "show all hook exits" preference is read from local storage and changes which `SYSTEM_LINE` hook-exit lines render (normal-exit-only by default, all exits when enabled). (test-renderer-advanced.js)
- Appending tasks shows only the newly added tasks with their progress; a fresh append with no prior state shows every appended task. (test-renderer-plan.js)
- Empty/missing append fields render from the authoritative State snapshot rather than the call's raw arguments. (test-renderer-plan.js)
- Malformed nested task mutations still render without crashing and without refreshing a card for the malformed portion. (test-renderer-plan.js)
- Completing a task and auto-activating the next one in the same call shows both the completed and the newly activated task on the card. (test-renderer-plan.js)
- An explicit activation call shows only the activated task on the card. (test-renderer-plan.js)
- A non-status update (e.g. a note-only change) shows only the progress header, no per-row status change. (test-renderer-plan.js)
- Consecutive `task_list` mutations each produce their own card scoped to their own changes, not a merged card. (test-renderer-plan.js)
- A degraded replay (missing some historical context) renders only the explicit changes it can prove — it never invents an auto-activation it didn't observe. (test-renderer-plan.js)
- An empty `action:"view"` append and a failed mutation both render no card at all. (test-renderer-plan.js)
- *(test-renderer-plan.js also pins CSS-token contracts for `.task-card`/`.plan-item` styling — left-rail not boxed, no legacy aggregate/full-plan/done-pile styles, tabular-nums progress, current-glyph pulse — style-only.)*
- `planGlyphForStatus` returns unified SVG icon markup for `done`/`in_progress`/`cancelled`, and only the literal "○" fallback for `pending` (not a unified-icon state). (test-renderer-format-plan-glyphs.js)

## 12. System/lifecycle event churn & compaction

- A compaction item's structured `raw.compaction` payload (not just its prose text) is forwarded onto the `SYSTEM_MESSAGE` event by appwire; an ordinary system message never carries a fabricated `raw`. (test-renderer-compaction.js)
- Compaction renders as a dedicated, quiet, expandable `.context-compaction-line` — never a silent rug-pull. (test-renderer-compaction.js)
- Expanding the compaction line shows the real before/after numbers (turns, estimated tokens, compaction layer) sourced from the structured payload, not paraphrased prose. (test-renderer-compaction.js)
- A single lifecycle event renders as a quiet one-liner with no divider weight and no meaningless "N chars"/"N KB" payload count. (test-system-churn.js)
- A run of 3 or more adjacent lifecycle events coalesces into one collapsed toggle naming the count and the first event; expanding it reveals the individual blocks. (test-system-churn.js)
- Fewer than 3 adjacent lifecycle events do not coalesce — each renders as its own visible block. (test-system-churn.js)
- A non-lifecycle entry (e.g. prose) between lifecycle events breaks the run into two separate, sub-threshold (uncoalesced) runs. (test-system-churn.js)
- Consecutive plugin-loaded events group into their own plugin-disclosure run, with a collapsed toggle naming the loaded plugin(s) and expandable per-plugin detail rows, kept separate from surrounding generic system-event runs even when interleaved. (test-system-churn.js)
- A plugin-loaded event with no plugin name still gets its own plugin disclosure (not folded into a generic system-event count); the split from surrounding runs is driven by structured plugin metadata, not by whether the event has a non-empty display name. (test-system-churn.js)

## 13. Connection / transport chrome

- A transport (daemon/appwire) drop shows an amber "Reconnecting…" chrome banner with a working-icon glyph, never a transcript error row, and disables send while disconnected. (test-renderer-connection-banner.js)
- A successful reconnect clears the banner. (test-renderer-connection-banner.js)
- Reconnection attempts that keep failing escalate the banner to red "Connection lost", still without polluting the transcript. (test-renderer-connection-banner.js)
- An AppWire application-level failure (as opposed to a transport drop) shows a distinct "Transcript unavailable" banner carrying the exact server message, is not retried, and is never mislabeled as a connection loss. (test-renderer-connection-banner.js)

## 14. User message actions, fork & accessibility

- Every transcript message (user or assistant) carries a fork `<button type="button">` action. (test-renderer-fork-from-message.js)
- Forking a user message forks the session at that message's transcript entry index with `defer_input:true`, stages the server-returned original text as the child session's draft, and navigates to the child without ever calling `startTurn` (no auto-run). (test-renderer-fork-from-message.js)
- Forking an assistant message forks at the entry index of the user prompt that produced it (retry semantics), staging that prompt's text, again with `defer_input` and no auto-run. (test-renderer-fork-from-message.js)
- `SerfDrafts.writeFor` stores a draft for an arbitrary (not-yet-visited) session id and removes the entry when written with blank content. (test-renderer-fork-from-message.js)
- Opening the forked child session restores the staged draft into its composer, ready for editing, without auto-submitting it. (test-renderer-fork-from-message.js)
- An optimistic local echo's fork button reads its transcript entry index at click time — after the server echo corrects the optimistic index, a later click forks at the corrected index, never a stale inferred one. (test-renderer-fork-from-message.js)
- `showForkDialog` navigates using `json.ref` when present, even if `child_session_id`/`session_id` are also present (highest priority). (test-renderer-fork-ref.js)
- With no `ref`, `showForkDialog` falls back to `json.child_session_id`. (test-renderer-fork-ref.js)
- With neither `ref` nor `child_session_id`, `showForkDialog` falls back to `json.session_id`. (test-renderer-fork-ref.js)
- Each user message's copy and edit actions render as real, keyboard-focusable `<button type="button">` elements, never a clickable `<span onclick>`. (test-renderer-user-actions-a11y.js)
- A collapse/expand disclosure (thinking block, tool cluster, coalesced system run) exposes `aria-expanded`, starting `"true"` while streaming-open. (test-renderer-disclosure-aria.js)
- Clicking to collapse removes the `.open` class and flips `aria-expanded` to `"false"`; clicking again restores both. (test-renderer-disclosure-aria.js)

## 15. Images

- A user message with multiple images lays them out as one neutral contact-sheet card holding one grid cell per image, never the single-image per-card path. (test-renderer-multi-image.js)
- Each grid cell shows a thumbnail plus a per-cell filename caption. (test-renderer-multi-image.js)
- A single-image message still uses today's single-card path (no grid, no sheet). (test-renderer-multi-image.js)
- Opening any cell opens one shared lightbox instance positioned at that cell, exposing prev/next controls that step across the whole message's image set and wrap at the ends; Esc closes the single shared instance. (test-renderer-multi-image.js)

## 16. Subagent rows & affordances (renderer-side)

- A completed subagent row with a transcript ref gains an "open beside" button that calls `SerfPanes.open()` with the correct pane href (including source-qualified refs) without triggering the row's own hard-navigation click handler. (test-renderer-open-beside.js)
- The open-beside button is entirely absent from subagent rows when `window.SerfPanes` doesn't exist (e.g. an iframe guard). (test-renderer-open-beside.js)
- `subagentGlyph` returns unified SVG icon markup for `done`/`failed`/`running`, and only the literal "?" for `unknown` (not a unified-icon state). (test-renderer-subagent-glyphs.js)
- The scroll-nudge pill (needs-you and urgent-error variants), the off-screen dock, and the ask-header glyph all render unified SVG icons, never the literal "◆"/"✕" characters. (test-renderer-needsyou-affordances.js)
- The settled-ask summary line renders its icon and text as separate DOM nodes (not string-concatenated `innerHTML`), so raw/unescaped question-header and reply text can never be parsed as live HTML while still displaying literally (XSS-safe). (test-renderer-needsyou-affordances.js)

## 17. In-transcript job notifications

- A delegate-completion `<job-notification>` block parses into a structured summary. (test-renderer-notifications.js)
- An "exhausted" notification classifies as a terminal, non-success outcome. (test-renderer-notifications.js)
- The raw notification text always stays inspectable, even once rendered as a tidied card. (test-renderer-notifications.js)
- A completed job recedes visually: a neutral done glyph, no wall of bordered key/value chips, and demoted/secondary job-kind metadata (no boilerplate id echo). (test-renderer-notifications.js)
- A watch notification (with a non-JSON excerpt) parses and renders as a minimal warning-toned card. (test-renderer-notifications.js)
- A watch-send notification parses its concerns/warning tone and renders a minimal warning card; communicate concerns surface as a tidy facts list. (test-renderer-notifications.js)
- An observer-callback notification is coerced from a "success" tone to warning and renders accordingly. (test-renderer-notifications.js)
- A malformed excerpt stays raw/inspectable rather than being interpreted, and is never injected as live HTML. (test-renderer-notifications.js)
- A nonzero exit code gives the notification error tone and renders a minimal error card. (test-renderer-notifications.js)
- An outer job failure overrides a successful inner `communicate` status. (test-renderer-notifications.js)
- A shell failure notification shows its failure metadata (reason, exit code). (test-renderer-notifications.js)
- A job-less `event="watch"` renders as a generic "Watch triggered" card; a watch carrying a concrete job id renders "Job watch". (test-renderer-notifications.js)
- A watch-send notification renders its delivery metadata (delivery id, trigger). (test-renderer-notifications.js)
- An observer-callback notification renders within the same notification card family as job notifications. (test-renderer-notifications.js)
- A malformed notification (missing fields) falls back to a card that still shows the raw evidence. (test-renderer-notifications.js)
- Notification text is HTML-escaped when rendered. (test-renderer-notifications.js)
- A very long unstructured failure excerpt is collapsed rather than rendered in full. (test-renderer-notifications.js)
- A `communicate` message inside a notification renders through markdown. (test-renderer-notifications.js)
- A `communicate` message inside a notification truncates at 8k characters. (test-renderer-notifications.js)
- Several `<job-notification>` blocks joined by newlines in one turn each parse individually — a greedy match must not aggregate multiple blocks into one notification, and each card's raw disclosure shows only its own block's text (no bleed across block boundaries). (test-renderer-notifications.js)
- Interstitial prose between/around notification blocks in the same turn is preserved (rendered), not silently swallowed. (test-renderer-notifications.js)

## 18. Compact / embedded-pane rendering

- When the renderer detects it is running inside a framed context, it adds a `pane-compact` class to `document.body`; unframed, it does not. (test-renderer-pane-compact.js)
- In compact mode, newly created cheap-tool clusters carry `[data-compact]`; outside compact mode they don't. (test-renderer-pane-compact.js)
- *(test-renderer-pane-compact.js also pins a CSS-token contract — the `.pane-compact` stylesheet scope exists and trims the header/title row/hotkey labels/conversation spacing — style-only.)*

## 19. End-to-end realistic flow

- A realistic multi-turn run renders exactly one user message with the original prompt text in its pill. (test-realistic-flow.js)
- It renders exactly two assistant messages — one from `ASSISTANT_TEXT_END`, one from the `communicate` tool — each carrying its own distinct content. (test-realistic-flow.js)
- SYSTEM-REMINDER-wrapped steerings are suppressed while a bare loop-detection nudge (no SYSTEM-REMINDER wrapper) still renders as a divider. (test-realistic-flow.js)
- Adjacent cheap tool calls (e.g. read + grep) group into exactly one cluster, while edit/shell remain their own standalone tool-call rows. (test-realistic-flow.js)
- Each successful `task_list` update across the run appends its own per-call task card (task_list itself never renders as a generic tool-call row). (test-realistic-flow.js)
- The full run's rendered transcript has the exact expected number of top-level children — nothing extra, nothing missing. (test-realistic-flow.js)

## CSS-token-only tests (in scope by name, not detailed — no DOM behavior to port)

- test-transcript-typography.js — pins `.tool-call`/transcript font-family stylesheet rules against `style.css`.
