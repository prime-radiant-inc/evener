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
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in repo root
  (`go build -o serf-tui ./cmd/serf-tui && go build -o serf-hub
  ./cmd/serf-hub`).
- Anthropic OAuth or API key configured so the default
  `anthropic/claude-haiku-4-5-20251001` model can be invoked.
- No leftover tmux session named `serf-queue-test`
  (`tmux kill-session -t serf-queue-test 2>/dev/null`).

## Steps

Use `tmux send-keys -t serf-queue-test KEY ...` to drive input
and `tmux capture-pane -t serf-queue-test -p` to read the
visible pane.

1. **Prepare a hermetic workdir**:
   ```
   WORKDIR=$(mktemp -d -t serf-queue-XXXX)
   cp README.md "$WORKDIR/README.md"
   ```

2. **Launch in tmux**:
   ```
   tmux new-session -d -s serf-queue-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   tmux capture-pane -t serf-queue-test -p
   ```

3. **Open the spawn form and retarget the workdir**:
   ```
   tmux send-keys -t serf-queue-test "n"
   sleep 0.5
   tmux send-keys -t serf-queue-test BTab
   tmux send-keys -t serf-queue-test C-u
   tmux send-keys -t serf-queue-test -l "$WORKDIR"
   tmux send-keys -t serf-queue-test Tab
   ```

4. **Send a slow first prompt**:
   ```
   tmux send-keys -t serf-queue-test -l "Read README.md and write a 4-paragraph essay about its main themes. Use formal prose."
   tmux send-keys -t serf-queue-test Enter
   sleep 2
   tmux capture-pane -t serf-queue-test -p
   ```
   Confirm `state: active`, the second status row reads
   `queue: ready  busy: turn_1`, and the composer label is now
   `queue` (not `message`) with footer
   `enter: queue  ctrl+s: send as steer  esc: browse  ctrl+p:
   palette  ctrl+o: dashboard  /help`.

5. **Wait for the model to actually start producing tokens**:
   ```
   sleep 3
   tmux capture-pane -t serf-queue-test -p
   ```
   Confirm an assistant message or tool-call row has appeared.

6. **Type a follow-up and press Enter to queue it**:
   ```
   tmux send-keys -t serf-queue-test -l "After the essay, also list the file sizes of every file in the working directory."
   tmux send-keys -t serf-queue-test Enter
   sleep 0.5
   tmux capture-pane -t serf-queue-test -p
   ```
   The composer clears. Above the composer, a `queued (1)`
   section appears with the first line of the queued message
   truncated and prefixed `1. After the essay, also list the…`.
   This is the local preview tracked in
   `cmd/serf-tui/hub_model.go:hubModel.sessionQueue` plus the
   `renderQueuePreview` rendering. The turn underway is
   unaffected; the daemon stashed the message via `turn/queue`.

7. **Confirm the daemon's queue is non-empty via REST**:
   ```
   SID=$(tmux capture-pane -t serf-queue-test -p | \
     grep -oE '01[0-9A-Z]{24}' | head -1)
   curl -s -H "Authorization: Bearer $(cat ~/.serf/auth-token)" \
     "http://localhost:9180/api/sessions/local:$SID" | \
     python3 -c "import json,sys; d=json.load(sys.stdin); print('state=',d.get('state'))"
   ```
   `state= active`. (Queue depth is not yet surfaced by the
   appwire layer — see sharp edges; the TUI preview is the
   user-visible truth.)

8. **Wait for the original turn to wrap and the queued message
   to run automatically**:
   ```
   sleep 12
   tmux capture-pane -t serf-queue-test -p
   ```
   The queue preview disappears (`queued (...)` row is gone)
   the moment the original turn completes — the TUI pops the
   head on `turn/completed` in
   `cmd/serf-tui/hub_model.go:applyHubNotification` so the
   preview matches the daemon's pop. A new processing turn
   `turn_2` immediately starts for the queued line and the
   composer flips back to `queue` while the second turn runs.
   Eventually the file listing appears as a `communicate`
   payload from the model.

9. **Cross-check the transcript on disk**:
   ```
   TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
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
    tmux send-keys -t serf-queue-test C-o
    tmux send-keys -t serf-queue-test "q"
    tmux kill-session -t serf-queue-test 2>/dev/null
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
  through `turn/start` or `turn/steer`); the local queue
  preview never appears (means `appendSessionQueue` regressed or
  the appwire call failed silently).
- Step 8: when `turn_1` completes the queue preview is dropped
  and `turn_2` starts automatically with the queued message as
  its USER_INPUT. Falsification: the queue preview persists
  after `turn_1` ends but no new turn begins (means the daemon's
  pop-on-completion loop in `agent/session.go:ProcessInput`
  regressed). Or: the preview disappears but the new turn never
  starts (means appwire's `MethodTurnQueue` accepted the message
  but the daemon's inputQueue never delivered it to ProcessInput
  — file a regression with the test from
  `agent/session_test.go`).
- Step 9: transcript has two `USER_INPUT` rows in order. The
  queued line's text matches exactly what was typed in step 6.

## Cleanup

- `tmux kill-session -t serf-queue-test 2>/dev/null`.
- `rm -rf "$WORKDIR"`.
- The spawned session ULID is left on the hub. Optional cleanup
  via `~/go/bin/serf` or manual delete.

## Sharp edges

- **The queue preview is TUI-local, not authoritative.** The
  appwire layer (`internal/appwire/types.go`) does not yet
  surface queue depth or preview text — Phase 2a only landed the
  `Capabilities.Queue` bit. The TUI mirrors what *it* enqueued
  in the current process via `hubModel.sessionQueue` and
  reconciles by popping the head on each `turn/completed`
  notification. The mirror is correct for a single TUI session;
  if a second client enqueues to the same session, or if the TUI
  is restarted mid-turn, the preview won't reflect those entries
  until the appwire layer carries depth+preview. That's a
  follow-on kata (see "Deferred work").
- **Pop-on-completed assumes clean completion.** The TUI drops
  the head only when `params.Turn.Status` is *not* `failed`. A
  failed turn doesn't drain the daemon's queue either (see
  `agent/session.go` ProcessInput error path); the queue
  preview correctly stays put so the user can decide whether to
  retry or `/clear`.
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
