# tui-queue-then-drain-as-steer: force-steer drains the queue into a single STEERING message

**What this covers**: kata `0bq1` (TUI surface). When the
composer is in `queue` mode (mid-turn), the new force-steer
keybind `Ctrl+S` calls `turn/drainAsSteer` on the daemon to pop
every queued message — plus any unsent composer text — into a
single STEERING entry for the in-flight turn. The agent sees the
combined payload as a steer that nudges the current turn instead
of waiting for the next one. This scenario exercises the queue
+ drain round trip against a real model.

The wiring lives in:
- `cmd/serf-tui/composer_panel.go:hubComposerModeQueue` —
  footer hint advertises `ctrl+s: send as steer` whenever the
  source also has the `Steer` capability.
- `cmd/serf-tui/hub_model.go:handleSessionForceSteer` — picks
  one of three branches: (a) drain only, (b) drain + composer
  via `sendHubQueueThenDrain`, or (c) no-op banner when both
  queue and composer are empty.
- `cmd/serf-tui/queue_send.go:sendHubQueueThenDrain` — chains
  `client.TurnQueue` then `client.TurnDrainAsSteer` inside one
  Bubble Tea command so the daemon sees them strictly in order.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in repo root.
- Anthropic OAuth or API key configured so
  `anthropic/claude-haiku-4-5-20251001` can be invoked.
- No leftover tmux session named `serf-drain-test`.

## Steps

1. **Hermetic workdir + tmux + spawn**:
   ```
   WORKDIR=$(mktemp -d -t serf-drain-XXXX)
   cp /home/jesse/git/prime-radiant/serf/README.md "$WORKDIR/README.md"
   tmux new-session -d -s serf-drain-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   tmux send-keys -t serf-drain-test "n"
   sleep 0.5
   tmux send-keys -t serf-drain-test BTab
   tmux send-keys -t serf-drain-test C-u
   tmux send-keys -t serf-drain-test -l "$WORKDIR"
   tmux send-keys -t serf-drain-test Tab
   ```

2. **Send a slow first prompt**:
   ```
   tmux send-keys -t serf-drain-test -l "Read README.md and write a 5-paragraph essay about its main themes. Use formal prose. Take your time."
   tmux send-keys -t serf-drain-test Enter
   sleep 3
   tmux capture-pane -t serf-drain-test -p
   ```
   Confirm `state: processing`, composer label `queue`, footer
   `enter: queue  ctrl+s: send as steer …`.

3. **Type two follow-up messages and queue both with Enter**:
   ```
   tmux send-keys -t serf-drain-test -l "Also list the file sizes."
   tmux send-keys -t serf-drain-test Enter
   sleep 0.3
   tmux send-keys -t serf-drain-test -l "And summarise each section in one sentence."
   tmux send-keys -t serf-drain-test Enter
   sleep 0.5
   tmux capture-pane -t serf-drain-test -p
   ```
   The composer should be empty. Above it, `queued (2)` followed
   by two lines:
   ```
     1. Also list the file sizes.
     2. And summarise each section in one sentence.
   ```

4. **Type a third line — but DO NOT press Enter — then press
   Ctrl+S to force-steer**:
   ```
   tmux send-keys -t serf-drain-test -l "Change of plans: write a haiku instead. Forget everything else."
   sleep 0.2
   tmux send-keys -t serf-drain-test C-s
   sleep 1
   tmux capture-pane -t serf-drain-test -p
   ```
   The `queued (...)` preview block disappears, the composer
   clears, and a `Force-steer sent.` system line appears in the
   transcript. The composer-text-in-flight ("Change of plans…")
   was queued first, then `turn/drainAsSteer` popped all three
   queued entries (the two from step 3 plus the in-flight
   composer text) into one STEERING message.

5. **Wait for the running turn to honour the steer**:
   ```
   sleep 10
   tmux capture-pane -t serf-drain-test -p
   ```
   The composer flips back to `message` (`enter: send`). The
   assistant's final output is short — a haiku, not a five-
   paragraph essay or a long file listing.

6. **Cross-check the transcript on disk**:
   ```
   SID=$(tmux capture-pane -t serf-drain-test -p | \
     grep -oE '01[0-9A-Z]{24}' | head -1)
   TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   grep -c '"kind":"STEERING"' "$TS"
   ```
   At least one. Inspect the STEERING entry:
   ```
   grep '"kind":"STEERING"' "$TS" | tail -1 | python3 -c "
   import json, sys
   for line in sys.stdin:
       j = json.loads(line)
       for c in j.get('turn',{}).get('message',{}).get('content',[]):
           if c.get('kind') == 'text':
               print('STEERING:', c.get('text',''))
   "
   ```
   The combined steer text should contain all three lines —
   the two queued in step 3 and the composer text from step 4 —
   joined by blank lines (per
   `agent/session.go:DrainAsSteer`).

7. **Exit and clean up**:
   ```
   tmux send-keys -t serf-drain-test C-o
   tmux send-keys -t serf-drain-test "q"
   tmux kill-session -t serf-drain-test 2>/dev/null
   rm -rf "$WORKDIR"
   ```

## Expected

- Step 3: two `queued (2)` preview rows appear above the
  composer. The composer remains empty between them, ready for
  more typing. Falsification: only one row shows (means
  `appendSessionQueue` is overwriting instead of appending), or
  no rows show (means the appwire call is failing — check the
  notice panel for "Queue failed").
- Step 4: composer text was non-empty when Ctrl+S fired.
  `handleSessionForceSteer` should call
  `sendHubQueueThenDrain` (single Bubble Tea command, both
  appwire calls serialised), drop the local queue preview, and
  post `Force-steer sent.`. Falsification: the queue preview
  persists after Ctrl+S (means the success handler for
  `hubDrainAsSteerMsg` didn't run, or `clearSessionQueue`
  regressed). Or: only the composer text is steered and the
  two queued messages are dropped (means the queue helper
  didn't run before the drain — sequencing regression in
  `sendHubQueueThenDrain`).
- Step 5: the model abandons the essay and emits a haiku.
  Falsification: model finishes the essay anyway (would
  indicate the combined STEERING entry was persisted but not
  surfaced to the LLM's next round — file a regression).
- Step 6: STEERING entry contains all three lines. The
  daemon's `DrainAsSteer` joins queued messages with blank
  lines; falsification is if only one of the three lines shows
  up.

## Cleanup

- `tmux kill-session -t serf-drain-test 2>/dev/null`.
- `rm -rf "$WORKDIR"`.

## Sharp edges

- **Ctrl+S with empty queue AND empty composer is a no-op
  banner.** `handleSessionForceSteer` returns early with a
  `"Nothing to steer: the queue is empty."` system line rather
  than calling the hub. This is to keep accidental Ctrl+S
  presses from spamming empty STEERING entries.
- **Ctrl+S without steer capability is silently demoted.**
  Some sources may advertise `queue` but not `steer` (rare).
  When that happens the footer hint omits `ctrl+s: send as
  steer` and the keybind posts a banner explaining the source
  does not advertise steer instead of calling the hub.
- **The drain is atomic on the daemon side.** Once the
  daemon's `Session.DrainAsSteer` has the queue lock, no new
  enqueue can interleave; the combined STEERING message
  reflects exactly the queue snapshot at the call instant
  (plus the composer text we enqueued right before). The TUI
  doesn't try to mirror that lock — the local queue preview
  is cleared optimistically on the success path.
- **Ctrl+S was chosen over Shift+Enter** because Bubble Tea
  cannot reliably distinguish Shift+Enter from a bare Enter
  through standard terminal input — most terminals don't
  forward the Shift modifier on Return. Ctrl+S is one-handed,
  ergonomically reachable while composing, and only conflicted
  with the launch-overrides modal (which is mutually exclusive
  with the composer). The terminal flow-control XOFF
  interpretation of Ctrl+S is disabled in Bubble Tea's raw
  mode, so it reaches our key handler.
- **The combined STEERING entry is what the agent sees, not
  the individual queue lines.** If you `/clear` and inspect
  the transcript, you'll find one STEERING row with
  newline-joined text — not three rows. This matters for
  later token-counting and replays.
