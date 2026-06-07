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

- Fresh binaries + a test hub on a free port (see
  `web-goal-set-and-complete.md` Pre-state / `docs/agentic-testing.md`):
  ```bash
  go build -o /tmp/serf-hub-test ./cmd/serf-hub
  go build -o /tmp/serf-test     ./cmd/serf
  go build -o /tmp/serf-tui-test ./cmd/serf-tui
  /tmp/serf-hub-test -addr 127.0.0.1:9185 -serf /tmp/serf-test &
  sleep 2
  ```
- OpenAI usable; `openai/gpt-5.4-mini` is enough for goal turns.

## Steps

1. **Start the TUI in tmux** at a fixed size with `--debug` (plain-text
   capture, stderr to a log):
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-goaltui-XXXXX)
   tmux kill-session -t serf-goal 2>/dev/null
   tmux new-session -d -s serf-goal -x 200 -y 50 \
     "/tmp/serf-tui-test --hub-addr 127.0.0.1:9185 --debug 2>/tmp/goaltui-stderr.log"
   sleep 2
   ```

2. **Spawn a session** through the new-session form (see the tmux
   form-fill recipe in `docs/agentic-testing.md`). Set `working_dir` to
   `$tmpdir`, model `openai/gpt-5.4-mini`, and a trivial first prompt
   (`Say hello and stop.`). Wait for the spawn turn to reach idle:
   ```bash
   for i in $(seq 1 40); do
     tmux capture-pane -t serf-goal -p | grep -qiE "state[: ]+idle|idle" && break; sleep 1
   done
   ```

3. **Set a two-step goal** that forces at least one continuation. In the
   composer, type the command literally (`-l`) and submit:
   ```bash
   tmux send-keys -t serf-goal -l "/goal Create a file seed.txt containing the number 7, then create double.txt containing that number doubled (14). Verify both files, then mark the goal complete."
   sleep 0.5
   tmux send-keys -t serf-goal Enter
   ```
   **Expected:** the composer accepts the command; the agent starts a
   turn. Within a few seconds the header shows a `goal` chip. Falsify: if
   the TUI prints `goal not available` or a usage hint instead of starting
   work, the command did not reach `client.GoalSet` — check
   `runHubGoal`/the command registry.

4. **Confirm the goal state via `/goal status`** (the always-visible
   assertion — see the chip caveat in step 5):
   ```bash
   tmux send-keys -t serf-goal -l "/goal status"; sleep 0.3; tmux send-keys -t serf-goal Enter
   sleep 1
   tmux capture-pane -t serf-goal -p | grep -iE "Goal: (active|complete|blocked)"
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
   tmux send-keys -t serf-goal PageUp; tmux send-keys -t serf-goal PageUp; sleep 0.5
   tmux capture-pane -t serf-goal -p | grep -nE "src .*serf .*goal (active|complete|blocked) [0-9]"
   ```
   **Expected:** a line like
   `src serf · model … · dir … · ctx … · goal complete 0`. Falsify: if
   `/goal status` reports a goal (step 4) but this strip never shows a
   `goal` part after scrolling to the top, the chip wiring
   (`m.detail.Goal` → `addPart`) has regressed.

6. **Wait for completion** and verify the result on disk + the chip:
   ```bash
   for i in $(seq 1 120); do
     tmux capture-pane -t serf-goal -p | grep -qiE "goal +complete" && { echo "complete i=$i"; break; }
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
tmux kill-session -t serf-goal 2>/dev/null
rm -rf /tmp/serf-e2e-goaltui-*
pkill -f serf-hub-test   # only if you started the test hub
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
