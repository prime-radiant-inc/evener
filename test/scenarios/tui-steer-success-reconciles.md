# tui-steer-success-reconciles: optimistic-rendering happy path (TUI)

TUI counterpart of `web-steer-success-reconciles.md`. Drives a
Ctrl+S force-steer against a processing turn and verifies the
two-frame transition: first capture-pane shows the faint `⠋ `
spinner prefix on the optimistic placeholder; second capture-pane
(after the daemon's `serf/steering/injected` notification arrives)
shows the prefix gone and the authoritative steering chip in its
place.

The end-to-end steer is already covered by `tui-steer-live-turn.md`
(kata `mn4z`); this scenario adds the optimistic-rendering visual
check on the TUI side.

The wiring lives in:
- `cmd/serf-tui/pending.go:pendingCoordinator.TryReconcile` —
  matches an in-flight pending entry by method + normalized text.
- `cmd/serf-tui/hub_model.go:applyHubNotification` — invokes
  `TryReconcile` before the authoritative reducer append.
- `cmd/serf-tui/message.go` — Pending sets `⠋ ` prefix; the
  reconcile drops `msg.Pending` so the prefix disappears and the
  row collapses into the authoritative steering chip.

Driver: tmux send-keys / capture-pane.

## Pre-state

- `tmux` installed.
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token`.
- `./serf-tui` built fresh from this branch.
- Anthropic OAuth or API key configured for
  `anthropic/claude-haiku-4-5-20251001`.
- No leftover tmux session named `serf-steer-ok-test`.

## Steps

1. **Prepare a workdir with a pacing AGENTS.md** so the first
   turn stays in `processing` long enough to send Ctrl+S:
   ```
   WORKDIR=$(mktemp -d -t serf-steer-ok-XXXX)
   cat > "$WORKDIR/AGENTS.md" <<'EOF'
   # Working agreement

   Insert exec_command bash -c "sleep 5" calls between every
   paragraph and every tool call. There must be at least 4 sleep
   calls per turn.
   EOF
   ```

2. **Launch the TUI in tmux**:
   ```
   tmux new-session -d -s serf-steer-ok-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   ```

3. **Spawn a slow turn** that will stay in `processing` for ~60 s:
   ```
   tmux send-keys -t serf-steer-ok-test "n"
   sleep 0.5
   tmux send-keys -t serf-steer-ok-test BTab
   tmux send-keys -t serf-steer-ok-test C-u
   tmux send-keys -t serf-steer-ok-test -l "$WORKDIR"
   tmux send-keys -t serf-steer-ok-test Tab
   tmux send-keys -t serf-steer-ok-test -l "Read AGENTS.md in your cwd. Then write a long 5-paragraph essay about software engineering. Follow the pacing rules in AGENTS.md exactly."
   tmux send-keys -t serf-steer-ok-test Enter
   # Wait until composer label flips to `queue` (= state=active).
   sleep 5
   tmux capture-pane -t serf-steer-ok-test -p | grep -q "state: active"
   ```

4. **Type a steer body and press Ctrl+S**:
   ```
   tmux send-keys -t serf-steer-ok-test -l "Change of plans: write only a single haiku instead of the essay."
   tmux send-keys -t serf-steer-ok-test C-s
   # Capture immediately — should see the ⠋ pending prefix.
   tmux capture-pane -t serf-steer-ok-test -p > /tmp/pane-pending.txt
   ```

5. **Wait for reconcile** — the daemon ack + appwire round-trip
   for `serf/steering/injected` typically lands within 1-2 s:
   ```
   sleep 3
   tmux capture-pane -t serf-steer-ok-test -p > /tmp/pane-reconciled.txt
   ```

## Expected

- **/tmp/pane-pending.txt** contains a line with the faint `⠋ `
  prefix (UTF-8 `e2 a0 8b`) followed by `↻ Change of plans: …`.
  No `✗` glyph anywhere in the pane.
  ```
  grep -q '⠋' /tmp/pane-pending.txt
  ! grep -q '✗' /tmp/pane-pending.txt
  ```
- **/tmp/pane-reconciled.txt** no longer contains the `⠋ ` prefix
  on the steer text. A `↻ Change of plans: …` line (the
  authoritative steering chip, rendered by the same `↻ ` prefix
  in `message.go:msgSteering`) is present.
  ```
  ! grep -q '⠋' /tmp/pane-reconciled.txt
  grep -q '↻ Change of plans' /tmp/pane-reconciled.txt
  ```
- Transcript on disk records exactly one new `kind=STEERING` entry
  whose text matches the typed steer:
  ```
  SID=$(tmux capture-pane -t serf-steer-ok-test -p | \
    grep -oE '01[0-9A-Z]{24}' | head -1)
  TS=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
  grep -c '"kind":"STEERING"' "$TS"  # 1
  ```
- Session settles to `idle` with the model honoring the steer
  (closing output references a haiku, not the 5-paragraph essay).

Falsification:

- `⠋ ` still present in `pane-reconciled.txt` after 3 s — reconcile
  failed to match. Most likely cause: `TryReconcile` normalization
  mismatch between the typed text and the authoritative
  `serf/steering/injected` payload.
- Two `↻ Change of plans` lines in `pane-reconciled.txt` — the
  reducer appended the authoritative chip without removing the
  pending placeholder (reconcile dropped the entry but left the
  rendered row).
- `✗ ` appears in `pane-reconciled.txt` — daemon rejected the
  steer despite `processing` state.

## Cleanup

```
tmux send-keys -t serf-steer-ok-test "i"
tmux send-keys -t serf-steer-ok-test C-c C-c
tmux kill-session -t serf-steer-ok-test 2>/dev/null
rm -rf "$WORKDIR"
rm -f /tmp/pane-pending.txt /tmp/pane-reconciled.txt
```

## Sharp edges

- **Capture timing**. The window between `Register` and
  `TryReconcile` is small (200-800 ms with Haiku). If your
  terminal lags or the capture-pane fires after the reconcile
  notification arrives, `pane-pending.txt` will not contain the
  `⠋ ` glyph. Insert a `sleep 0.2` immediately before the first
  capture if needed; the pending entry is still visible because
  the optimistic timeout is 10 s (see `pendingTimeout` in
  `pending.go:12`).
- **Same `↻ ` glyph for pending and authoritative**. Both rows
  use the steering `↻ ` prefix. The pending differentiator is the
  preceding `⠋ ` glyph and the faint lipgloss style; the
  authoritative chip has neither.
