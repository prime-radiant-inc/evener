# tui-queue-then-completes: enqueue a user message mid-turn, see it run as the next turn

**What this covers**: kata `111a` (TUI surface). The hub TUI's
composer now defaults to `queue` mode while a turn is in flight
(`cmd/serf-tui/composer_panel.go:hubComposerModeQueue`,
`cmd/serf-tui/hub_model.go` Enter handler around 1418-1450). The
old auto-switch-to-steer behaviour is gone: pressing Enter during
processing calls `turn/queue` on the daemon and the queued line
shows up as `queued (N)` above the composer. When the in-flight
turn finishes cleanly, the daemon's `Session.ProcessInput` outer
loop pops the next queued message and starts a fresh user turn
without any further user action. This scenario exercises the
round trip end-to-end against a real model.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on an isolated `$HOME` and free port
  (never Jesse's port `9180` — see the Setup checklist in
  `docs/agentic-testing.md`). Token at
  `$HOME/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in repo root
  (`go build -o serf-tui ./cmd/serf-tui && go build -o serf-hub
  ./cmd/serf-hub`).
- Anthropic OAuth or API key configured so the default
  `anthropic/claude-haiku-4-5-20251001` model can be invoked.
- The tmux session name is derived from this run's own scratch dir
  (`TMUX_SESSION`, set beside the `mktemp` below), so a second agent
  running this card at the same time cannot drive or kill this one's
  pane. Nothing to clear out first — the name is new every run.

## Steps

Use `tmux send-keys -t "$TMUX_SESSION" KEY ...` to drive input
and `tmux capture-pane -t "$TMUX_SESSION" -p` to read the
visible pane.

1. **Prepare a hermetic workdir**:
   ```
   WORKDIR=$(mktemp -d -t serf-queue-XXXX)
   TMUX_SESSION="serf-queue-$(basename "$WORKDIR")"
   cp README.md "$WORKDIR/README.md"
   ```

2. **Launch in tmux**:
   ```
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:$PORT --debug"
   sleep 1
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```

3. **Open the spawn form and retarget the workdir**:
   ```
   tmux send-keys -t "$TMUX_SESSION" "n"
   sleep 0.5
   tmux send-keys -t "$TMUX_SESSION" BTab
   tmux send-keys -t "$TMUX_SESSION" C-u
   tmux send-keys -t "$TMUX_SESSION" -l "$WORKDIR"
   tmux send-keys -t "$TMUX_SESSION" Tab
   ```

4. **Send a slow first prompt**:
   ```
   tmux send-keys -t "$TMUX_SESSION" -l "Read README.md and write a 4-paragraph essay about its main themes. Use formal prose."
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 2
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   Confirm `state: active`, the second status row reads
   `queue: ready  busy: turn_1`, and the composer label is now
   `queue` (not `message`) with footer
   `enter: queue  ctrl+s: send as steer  esc: browse  ctrl+p:
   palette  ctrl+o: dashboard  /help`.

5. **Wait for the model to actually start producing tokens**:
   ```
   sleep 3
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   Confirm an assistant message or tool-call row has appeared.

6. **Type a follow-up and press Enter to queue it**:
   ```
   tmux send-keys -t "$TMUX_SESSION" -l "After the essay, also list the file sizes of every file in the working directory."
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 0.5
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   The composer clears. Above the composer, a `queued (1)`
   section appears with the first line of the queued message
   truncated and prefixed `1. After the essay, also list the…`.
   This is the authoritative preview from `thread/queueChanged`, stored in
   `hubModel.sessionQueue` and rendered by `renderQueuePreview`. The turn
   underway is unaffected; the daemon stashed the message via `turn/queue`.

7. **Compare the preview to the authoritative appwire snapshot**:
   ```
   SID=$(tmux capture-pane -t "$TMUX_SESSION" -p | \
     grep -oE '01[0-9A-Z]{24}' | head -1)
   ```
   Dial `ws://127.0.0.1:$PORT/rpc` with
   `Authorization: Bearer $(cat "$HOME/.serf/auth-token")`, initialize, then
   call `thread/read` for `local:$SID` and inspect `thread.serf.queue`. Its
   `depth` is `1` and its first `preview` entry is the same
   first-line-truncated text displayed above the TUI composer. The legacy
   REST session endpoint may still report `state=active`, but it is not the
   queue authority for this card.

8. **Wait for the original turn to wrap and the queued message
   to run automatically**:
   ```
   sleep 12
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   The queue preview disappears (`queued (...)` row is gone) when
   `thread/queueChanged` carries the daemon's post-pop queue state. A new processing turn
   `turn_2` immediately starts for the queued line and the
   composer flips back to `queue` while the second turn runs.
   Eventually the file listing appears as a `communicate`
   payload from the model.

9. **Cross-check the transcript on disk**:
   ```
   TS=$(find $HOME/.local/state/serf/projects -name "$SID.transcript.jsonl")
   grep -oE '"kind":"USER_INPUT"' "$TS" | wc -l
   ```
   At least two `USER_INPUT` rows — one for the essay prompt and
   one for the queued follow-up. Inspect the second one:
   ```
   grep '"kind":"USER_INPUT"' "$TS" | tail -1
   ```
   Confirm the `text` field contains the file-listing follow-up
   from step 6. The agent's next `ASSISTANT` entries should
   address it.

10. **Exit and clean up**:
    ```
    tmux send-keys -t "$TMUX_SESSION" C-o
    tmux send-keys -t "$TMUX_SESSION" "q"
    tmux kill-session -t "$TMUX_SESSION" 2>/dev/null
    rm -rf "$WORKDIR"
    ```

## Expected

- Step 4: composer label flips to `queue` (not `steer`, not
  `message`) and the footer reads `enter: queue  ctrl+s: send
  as steer …` — the new keybind row. Falsification: footer still
  reads `enter: steer` (means the old auto-switch path regressed)
  or `enter: send` (means the source isn't advertising the
  `queue` capability — file a regression against the daemon's
  `turn/queue` capability gating).
- Step 6: Enter while processing routes through `turn/queue`,
  the composer clears, and the `queued (1)` preview block appears
  above the composer. Falsification: the line shows up in the
  transcript as a new turn immediately (means Enter still routed
  through `turn/start` or `turn/steer`); the queue
  preview never appears (means the authoritative
  `thread/queueChanged` update did not reach or apply in the TUI).
- Step 7: the TUI's `queued (1)` text equals
  `thread.serf.queue.preview[0]` and its displayed count equals
  `thread.serf.queue.depth`. Falsification: the snapshot and TUI differ
  (means the TUI is mirroring stale local state or failed to apply the
  authoritative queue update).
- Step 8: when `turn_1` completes the queue preview is dropped
  and `turn_2` starts automatically with the queued message as
  its USER_INPUT. Falsification: the queue preview persists
  after `turn_1` ends but no new turn begins (means the daemon's
  pop-on-completion loop in `agent/session.go:ProcessInput`
  regressed, or its `thread/queueChanged` update did not arrive). Or: the
  preview disappears but the new turn never starts (means appwire's `MethodTurnQueue` accepted the message
  but the daemon's inputQueue never delivered it to ProcessInput
  — file a regression with the test from
  `agent/session_test.go`).
- Step 9: transcript has two `USER_INPUT` rows in order. The
  queued line's text matches exactly what was typed in step 6.

## Cleanup

- `tmux kill-session -t "$TMUX_SESSION" 2>/dev/null`.
- `rm -rf "$WORKDIR"`.
- The spawned session ID is left on the hub. Optional cleanup
  via `~/go/bin/serf` or manual delete.

## Sharp edges

- **The queue preview is authoritative.** `appwire.QueueState` carries
  queue depth and first-line-truncated preview text on both the
  `thread/read` snapshot and `thread/queueChanged`. The TUI replaces its
  preview from those values rather than appending local enqueue actions, so
  another client, reconnect, or TUI restart converges on the daemon's queue.
- **Pop-on-completed assumes clean completion.** The daemon drains
  the head only when it emits the corresponding
  `thread/queueChanged`. A failed turn doesn't drain the daemon's queue
  either (see `agent/session.go` ProcessInput error path); the authoritative
  preview correctly stays put so the user can decide whether to retry or
  `/clear`.
- **Empty-trim still applies.** Enter on a blank composer
  remains a no-op (`strings.TrimSpace(text) == ""` guard in the
  Enter handler), so the queue preview can't get spammed with
  empty entries by accidental Enter presses.
- **No `/queue` slash command.** The composer-mode-during-
  processing path is the only way to queue from the TUI today.
  Web UI parity is handled by a separate kata.
- **Auth + provider tax.** This scenario burns real tokens for
  two turns of haiku-4-5 — the essay plus the file listing
  follow-up. Cheap, but not free.
