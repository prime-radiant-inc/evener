# tui-goal-set-and-complete: set a /goal from the TUI and watch it drive to completion

**What this covers**: the `/goal` objective engine on the serf-tui surface
(branch `goal-objective-engine`; `cmd/serf-tui/hub_commands.go` `runHubGoal`,
command registered in `hub_command_registry.go` as `/goal`, header chip in
`hub_session_view.go:60`). The TUI counterpart to
`web-goal-set-and-complete.md`. Verifies:

- **`/goal <objective>`** sets the session goal via `client.GoalSet`.
- **header chip** — while a goal is set, the session header renders a
  `goal <status> <iterations>` part (`addPart("goal", …)`).
- **`/goal status`** prints the cached snapshot
  (`hubGoalStatusText` → `Goal: <status> <iterations>`).
- **autonomous continuation + terminal report** — the agent works across
  continuation turns and stops with a reported status; the chip flips to
  `complete`.

The engine itself is unit- and live-tested at the agent layer
(`agent/session_goal_*_test.go`, `TestGoalLive_*`); this proves the TUI
command and the header chip are wired to it.

## Pre-state

- Fresh binaries and a hub on a kernel-assigned port, both under one
  `mktemp` run directory — never a fixed `/tmp/serf-hub-test` a second
  concurrent build would overwrite mid-run (kata `k2rx`), never a port a
  human picked (kata `68fm`). Same recipe as `docs/agentic-testing.md`'s
  Setup checklist:
  ```bash
  run=$(mktemp -d -t serf-e2e-goaltui-XXXXXX)
  go build -o "$run/serf-hub" ./cmd/serf-hub
  go build -o "$run/serf"     ./cmd/serf
  go build -o "$run/serf-tui" ./cmd/serf-tui
  "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
  HUBPID=$!
  for i in $(seq 1 50); do
    PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
    [ -n "$PORT" ] && break
    kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
    sleep 0.1
  done
  [ -n "$PORT" ] || { echo "hub never logged a listening port" >&2; exit 1; }
  ```
- Credentials: this card inherits `web-goal-set-and-complete.md`'s
  arrangement, including its documented OAuth-footgun exception and the
  flock pre-check that goes with it — read that card's Pre-state before
  starting, since a hub sharing Jesse's `$HOME` cannot start at all while
  his real one holds the lock, and the read-back loop above will report
  that as "hub exited before listening".
- OpenAI usable; `openai/gpt-5.4-mini` is enough for goal turns.

## Steps

1. **Start the TUI in tmux** at a fixed size with `--debug` (plain-text
   capture, stderr to a log):
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-goaltui-work-XXXXX)
   TMUX_SESSION="serf-goal-$(basename "$tmpdir")"
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "$run/serf-tui --hub-addr 127.0.0.1:$PORT --debug 2>$run/goaltui-stderr.log"
   sleep 2
   ```

2. **Spawn a session** through the new-session form (see the tmux
   form-fill recipe in `docs/agentic-testing.md`). Set `working_dir` to
   `$tmpdir`, model `openai/gpt-5.4-mini`, and a trivial first prompt
   (`Say hello and stop.`). Wait for the spawn turn to reach idle:
   ```bash
   for i in $(seq 1 40); do
     tmux capture-pane -t "$TMUX_SESSION" -p | grep -qiE "state[: ]+idle|idle" && break; sleep 1
   done
   ```

3. **Set a two-step goal** that forces at least one continuation. In the
   composer, type the command literally (`-l`) and submit:
   ```bash
   tmux send-keys -t "$TMUX_SESSION" -l "/goal Create a file seed.txt containing the number 7, then create double.txt containing that number doubled (14). Verify both files, then mark the goal complete."
   sleep 0.5
   tmux send-keys -t "$TMUX_SESSION" Enter
   ```
   **Expected:** the composer accepts the command; the agent starts a
   turn. Within a few seconds the header shows a `goal` chip. Falsify: if
   the TUI prints `goal not available` or a usage hint instead of starting
   work, the command did not reach `client.GoalSet` — check
   `runHubGoal`/the command registry.

4. **Confirm the goal state via `/goal status`** (the always-visible
   assertion — see the chip caveat in step 5):
   ```bash
   tmux send-keys -t "$TMUX_SESSION" -l "/goal status"; sleep 0.3; tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 1
   tmux capture-pane -t "$TMUX_SESSION" -p | grep -iE "Goal: (active|complete|blocked)"
   ```
   **Expected:** a transcript line `Goal: <status> <iterations>` (e.g.
   `Goal: active 1` mid-run, `Goal: complete 0` after). This reads the same
   cached `m.detail.Goal` the header chip uses, so it is the reliable proxy.

5. **Verify the header chip** — the `goal` part of the session meta strip
   (`hub_session_view.go` `addPart("goal", …)`). The meta strip sits at the
   **top of the scrollable session body**, which auto-scrolls to the
   transcript tail, so a plain `capture-pane` usually shows the composer
   footer (`harness … model …`, a *different* strip with no goal) — not the
   chip. Scroll the body to the top first:
   ```bash
   tmux send-keys -t "$TMUX_SESSION" PageUp; tmux send-keys -t "$TMUX_SESSION" PageUp; sleep 0.5
   tmux capture-pane -t "$TMUX_SESSION" -p | grep -nE "src .*serf .*goal (active|complete|blocked) [0-9]"
   ```
   **Expected:** a line like
   `src serf · model … · dir … · ctx … · goal complete 0`. Falsify: if
   `/goal status` reports a goal (step 4) but this strip never shows a
   `goal` part after scrolling to the top, the chip wiring
   (`m.detail.Goal` → `addPart`) has regressed.

6. **Wait for completion** and verify the result on disk + the chip:
   ```bash
   for i in $(seq 1 120); do
     tmux capture-pane -t "$TMUX_SESSION" -p | grep -qiE "goal +complete" && { echo "complete i=$i"; break; }
     sleep 2
   done
   cat "$tmpdir/seed.txt" "$tmpdir/double.txt" 2>/dev/null
   ```
   **Expected:** the chip reads `goal complete`; `seed.txt`=`7`,
   `double.txt`=`14`; a terminal goal line appears in the transcript.
   Falsify: chip stuck on `active` past ~4 min ⇒ the gate stopped issuing
   continuations; `blocked` with correct files ⇒ no-progress breaker
   mis-fired.

## Cleanup

```bash
tmux kill-session -t "$TMUX_SESSION" 2>/dev/null
kill "$HUBPID" 2>/dev/null   # by pid: `pkill -f serf-hub` would take out
                             # another agent's hub too (docs/agentic-testing.md)
rm -rf "$run" "$tmpdir"
```

## Sharp edges

- **Capture the glyph, not the color.** `capture-pane -p` strips ANSI; the
  chip text (`goal active 2`) is plain — grep the words, add `-e` only if
  you need styling.
- **Model nondeterminism.** Single-turn completion shows `N == 0` and no
  continuation chip transition; the two-step objective biases toward
  `N >= 1`. Assert `active`→`complete` and `N >= 1`, never an exact count.
- **Header layout drift.** The chip is `addPart("goal", "<status> <iter>")`;
  a header reshuffle may move it but the `goal <status> <num>` text is the
  stable assertion.
- **`--debug` is required** for deterministic `capture-pane` — without it
  bubbletea's AltScreen returns screen-buffer escapes, not plain text.
