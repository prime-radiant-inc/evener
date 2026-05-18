# tui-steer-in-idle-fails-fast: optimistic-rendering reject path (TUI)

TUI counterpart of `web-steer-in-idle-fails-fast.md`. Drives the
`serf-tui` Ctrl+S force-steer keybind against an IDLE session and
verifies the reducer renders the failed prefix (red `✗`) with the
daemon's Unavailable reason inline in the conversation pane —
without ever promoting the placeholder to an authoritative
STEERING entry.

The wiring lives in:
- `cmd/serf-tui/pending.go:pendingCoordinator` — owns the
  optimistic in-flight registry and the 10 s timeout.
- `cmd/serf-tui/hub_model.go:handleSessionForceSteer` — calls
  `Register("turn/drainAsSteer", text)` and `Fail(reason)` on
  rejection.
- `cmd/serf-tui/message.go` — renders the `⠋ ` (faint) prefix
  while `msg.Pending`, switches to red `✗ ` + `(failed: reason)`
  suffix while `msg.Failed`.

Closes kata `wymv` on the TUI side.

Driver: tmux send-keys / capture-pane.

## Pre-state

- `tmux` installed.
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` built fresh from this branch.
- Anthropic OAuth or API key configured.
- No leftover tmux session named `serf-steer-idle-test`.

## Steps

1. **Prepare a hermetic workdir**:
   ```
   WORKDIR=$(mktemp -d -t serf-steer-idle-XXXX)
   ```

2. **Launch the TUI in tmux**:
   ```
   tmux new-session -d -s serf-steer-idle-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   ```

3. **Spawn a tiny session** and let it run to IDLE:
   ```
   tmux send-keys -t serf-steer-idle-test "n"
   sleep 0.5
   tmux send-keys -t serf-steer-idle-test BTab
   tmux send-keys -t serf-steer-idle-test C-u
   tmux send-keys -t serf-steer-idle-test -l "$WORKDIR"
   tmux send-keys -t serf-steer-idle-test Tab
   tmux send-keys -t serf-steer-idle-test -l "please run \"echo hello\" via exec_command then stop"
   tmux send-keys -t serf-steer-idle-test Enter
   # Wait for state=idle (footer flips from `enter: queue` back to
   # `enter: send`, composer label flips from `queue` back to
   # `message`).
   for i in $(seq 1 60); do
     pane=$(tmux capture-pane -t serf-steer-idle-test -p)
     echo "$pane" | grep -q "state: idle" && break
     sleep 1
   done
   ```

4. **Type a steer body and press Ctrl+S in IDLE**. The composer is
   in `message` mode (not `queue`) so Ctrl+S should be the only
   gesture that even attempts a steer. The TUI optimistically
   registers a pending entry, then the daemon rejects with
   `Unavailable`:
   ```
   tmux send-keys -t serf-steer-idle-test -l "this steer should fail visibly"
   tmux send-keys -t serf-steer-idle-test C-s
   sleep 1
   tmux capture-pane -t serf-steer-idle-test -p
   ```

## Expected

- Within ~1 s of the Ctrl+S, the conversation pane contains a line
  with the red `✗ ` prefix (rendered by `message.go` for
  `msg.Failed = true`) followed by the user's typed text and a
  ` (failed: …)` suffix whose text contains `"not available"`
  (substring of the daemon's `"steer is not available for this
  session"` reply).
- The composer clears (the force-steer handler clears it on
  dispatch, then the failure flips the placeholder; the composer
  stays empty).
- No authoritative `STEERING` entry is appended to the transcript
  on disk:
  ```
  SID=$(tmux capture-pane -t serf-steer-idle-test -p | \
    grep -oE '01[0-9A-Z]{24}' | head -1)
  TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
  ! grep -q '"kind":"STEERING"' "$TS"
  ```

Falsification:

- The placeholder stays in `⠋ ` (pending) state for ~10 s then
  flips to `✗ ` with reason `"server did not confirm"` — means the
  daemon swallowed the request rather than rejecting it.
- A row with the steer text and NO `✗` prefix appears as a
  regular `STEERING` chip — means the daemon accepted the steer
  in IDLE (capability gating regression).
- The TUI shows a system-line banner `Force-steer sent.` instead
  of (or in addition to) the failed placeholder — means
  `handleSessionForceSteer` regressed and isn't routing through
  `pendingCoordinator`.

## Cleanup

```
tmux send-keys -t serf-steer-idle-test "i"
tmux send-keys -t serf-steer-idle-test C-c C-c
tmux kill-session -t serf-steer-idle-test 2>/dev/null
rm -rf "$WORKDIR"
```

## Sharp edges

- **`✗` vs `⠋` distinction in capture-pane**. The pending prefix
  `⠋` is rendered faint via lipgloss; the failed prefix `✗` is
  bright red. tmux `capture-pane -p` preserves the glyphs but
  drops the ANSI styling unless you pass `-e`. Grep on the glyph
  itself (UTF-8 `e2 9c 97` for `✗`), not on the color.
- **Why Ctrl+S in IDLE is the right repro**. In IDLE the composer
  is in `message` mode and Enter sends a normal turn; Ctrl+S is
  the only key that explicitly tries to steer. Production may
  guard against Ctrl+S in IDLE by ignoring the keypress; this
  scenario verifies the fail-visibly behaviour when the guard
  doesn't catch it.
