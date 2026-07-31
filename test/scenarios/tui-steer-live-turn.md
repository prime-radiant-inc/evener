# tui-steer-live-turn: force-steer drains the queue (or composer) as a STEERING entry

**What this covers**: kata `mn4z` (the original steer kata) folded
into kata `0bq1` (force-steer composer keybind). The old auto-
switch-to-steer behaviour is gone: Enter on a processing session
now enqueues via `turn/queue` (kata 111a) instead of steering. To
deliver a STEERING entry that the agent acts on *mid-turn* — the
behaviour this scenario originally covered — the user now presses
`Ctrl+S` (force-steer) which calls `turn/drainAsSteer`.

When the queue is empty and the composer has text, this is the
direct equivalent of the old "type a steer, press Enter" path —
just bound to a different key. The wiring lives in:
- `cmd/serf-tui/composer_panel.go:155-165` (`hubComposerModeQueue`) —
  sets the `queue` label and `CanSteer`. The footer a live session
  actually renders comes from `composerFooterHints`
  (`cmd/serf-tui/composer_render.go:348-360`) via `tuiprim.KbdHint`,
  which joins key and action with a space — `enter queue  ctrl+s steer
  …`, no colons. `composer_panel.go`'s colon'd `Keys` strings are only
  the fallback for a panel built without a `ChipContext`
  (`composer_panel.go:264-273`), i.e. unit fixtures, never a live pane.
- `cmd/serf-tui/hub_session_keys.go:491` (`handleSessionForceSteer`).
- `cmd/serf-tui/queue_send.go:80` (`sendHubDrainAsSteer`) — the composer
  text rides directly on ONE `turn/drainAsSteer` call. There is no
  `sendHubQueueThenDrain` two-RPC chain any more.

For the queue-only and queue+composer drain paths, see
`tui-queue-then-drain-as-steer.md`.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on an isolated `$HOME` and free port
  (never Jesse's port `9180` — see the Setup checklist in
  `docs/agentic-testing.md`). Token at
  `$HOME/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` (or `./serf`) built in repo root.
- Anthropic OAuth or API key configured so the default
  `anthropic/claude-haiku-4-5-20251001` model can be invoked.
- The tmux session name is derived from this run's own scratch dir
  (`TMUX_SESSION`, set beside the `mktemp` below), so a second agent
  running this card at the same time cannot drive or kill this one's
  pane. Nothing to clear out first — the name is new every run.

## Steps

1. **Prepare a hermetic workdir with a README to read**:
   ```
   WORKDIR=$(mktemp -d -t serf-steer-XXXX)
   TMUX_SESSION="serf-steer-$(basename "$WORKDIR")"
   cp README.md "$WORKDIR/README.md"
   ```

2. **Launch in tmux**:
   ```
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:$PORT --debug"
   sleep 1
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```

3. **Open the spawn form and retarget**:
   ```
   tmux send-keys -t "$TMUX_SESSION" "n"
   sleep 0.5
   tmux send-keys -t "$TMUX_SESSION" BTab
   tmux send-keys -t "$TMUX_SESSION" C-u
   tmux send-keys -t "$TMUX_SESSION" -l "$WORKDIR"
   tmux send-keys -t "$TMUX_SESSION" Tab
   ```

4. **Type a multi-round prompt and submit**:
   ```
   tmux send-keys -t "$TMUX_SESSION" -l "Read the README.md file in the current directory. Then write a 5-paragraph essay about its main themes. Use formal prose."
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 1.5
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   Confirm view shows `serf / session / <session-id>`, the status row
   reads `state: active  model: claude-haiku-4-5-…`, the
   second status row reads
   `status: hub connected  provider: anthropic  queue: ready
   busy: turn_1`, and the composer label is now `queue` (not
   `message`, not `steer`) with footer
   `enter queue  ctrl+s steer  esc browse  ⌘P palette  ⌘O dashboard`
   (no colons, ⌘-glyphs, and no `/help` in queue mode).

5. **Wait for the model to actually start producing tokens**
   so the steer is unambiguously mid-turn:
   ```
   sleep 3
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```

6. **Type the steer text — do NOT press Enter — then press
   Ctrl+S to force-steer**:
   ```
   tmux send-keys -t "$TMUX_SESSION" -l "Change of plans: write only a single haiku (3 lines, 5-7-5 syllables) instead of the essay. Disregard the essay instruction."
   tmux send-keys -t "$TMUX_SESSION" C-s
   sleep 1
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   A `Force-steer sent.` system line appears (per
   `cmd/serf-tui/hub_update.go:233` `hubDrainAsSteerMsg` handler,
   message text at `:272`). The
   composer clears. Because the queue was empty when Ctrl+S
   fired, `handleSessionForceSteer` took the "queue composer
   then drain" branch — but the drain pops a single line, so
   the agent sees one STEERING entry with the exact text typed.

7. **Wait for the turn to wrap and verify the model adjusted**:
   ```
   sleep 8
   tmux capture-pane -t "$TMUX_SESSION" -p
   ```
   The closing assistant output is a single haiku, not a fifth
   essay paragraph. The composer label flips back from `queue`
   to `message`, footer from `enter queue` to `enter send`.

8. **Cross-check the transcript on disk**:
   ```
   SID=$(tmux capture-pane -t "$TMUX_SESSION" -p | \
     grep -oE '01[0-9A-Z]{24}' | head -1)
   TS=$(find $HOME/.local/state/serf/projects -name "$SID.transcript.jsonl")
   grep -oE '"kind":"STEERING"' "$TS"
   ```
   At least one match. Inspect the full row:
   ```
   grep '"kind":"STEERING"' "$TS" | head -1
   ```
   The `text` payload contains the steer message sent in step
   6 verbatim. Message role is `user`. The agent's next
   `ASSISTANT` entry should issue a `communicate` tool call
   whose `arguments.message` is the haiku.

9. **Exit and clean up**:
   ```
   tmux send-keys -t "$TMUX_SESSION" "i"
   tmux send-keys -t "$TMUX_SESSION" C-c C-c
   tmux kill-session -t "$TMUX_SESSION" 2>/dev/null
   rm -rf "$WORKDIR"
   ```

## Expected

- Step 4: composer flips to `queue` mode while the turn is
  processing. The signature is two correlated changes — label
  `queue` above the prompt **and** footer `enter queue  ctrl+s steer
  …`. Falsification: footer still reads `enter steer` (means the old
  auto-switch path was restored by mistake), or `enter send` (means the
  source isn't advertising the `queue` capability — file a regression).
  Do not grep for the colon'd `enter: queue` form: it renders only in
  unit fixtures, so a literal match against a live pane fails on a
  healthy build.
- Step 6: `Force-steer sent.` system line appears within ~1 s.
  Falsification: nothing changes (means the Ctrl+S handler
  regressed); or the steer text shows up as a new `USER_INPUT`
  row in the transcript instead of `STEERING` (means
  `handleSessionForceSteer` routed through `sendHubQueue`
  *without* the follow-up drain — sequencing regression).
- Step 7: post-steer output is short and references the
  steer. Falsification: model finishes the essay anyway.
- Step 8: transcript has at least one `STEERING` entry whose
  `text` equals the steer string from step 6.

## Cleanup

- `tmux kill-session -t "$TMUX_SESSION" 2>/dev/null`.
- `rm -rf "$WORKDIR"`.

## Sharp edges

- **Enter no longer steers.** The old auto-switch-to-steer
  behaviour (`hubComposerModeSteer` deriving from a processing
  turn + Steer cap + non-empty ActiveTurnID) is gone. Enter
  during processing now calls `turn/queue` (kata 111a) and the
  message becomes the next user turn after the in-flight one
  finishes. If you want the agent to act on the message
  *during* the current turn, use Ctrl+S (force-steer) instead.
  This is a deliberate UX change from kata 111a/0bq1 — the
  default now matches the user's likely intent ("send my next
  message after this turn") and the disruptive force-steer
  needs an explicit keypress.
- **Ctrl+S keybind choice.** Bubble Tea cannot reliably detect
  Shift+Enter on most terminals (the Shift modifier is dropped
  by the terminal protocol), so the force-steer keybind is
  Ctrl+S. It is reachable one-handed while composing, only
  conflicts with the launch-overrides modal (mutually
  exclusive with the composer), and is unaffected by terminal
  XOFF flow control thanks to Bubble Tea's raw mode.
- **Ctrl+S with empty composer + empty queue posts a banner**
  rather than calling the hub. Same trim-empty guard as the
  Enter path.
- **No `/steer` slash command.** The composer-mode-during-
  processing keybind is the only way to steer from the TUI
  today. Web UI parity for force-steer is a separate kata.
- **Steer text is sent verbatim, not wrapped.** The transcript
  entry's content is exactly what you typed.
- **The 8-second post-steer wait is model-dependent.** Haiku
  is fast; slower models may need more.
- **Auth + provider tax.** This scenario *does* burn real API
  tokens.
