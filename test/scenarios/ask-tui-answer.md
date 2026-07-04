# ask-tui-answer: TUI attaches to an awaiting session — chip, keypress-only overlay, prose also answers

**What this covers**: spec §8 row `ask-tui-answer.md` — the TUI's "lens rule" (§6.2): every
overlay in `cmd/serf-tui/` opens from a keypress and never auto-traps focus, `Esc` defers
without discarding answers, and — because the composer is never put into a special
answer-vs-steer mode — typing an ordinary reply resolves the question exactly like
submitting through the overlay does.

## Pre-state

- Build fresh side-by-side binaries (adds `serf-tui` to `ask-web-answer.md`'s recipe):
  ```bash
  go build -o /tmp/serf-ask     ./cmd/serf
  go build -o /tmp/serf-hub-ask ./cmd/serf-hub
  go build -o /tmp/serf-tui-ask ./cmd/serf-tui
  ```
- Credentials + hub, same as `ask-web-answer.md` (reuse that hub if it's still running on
  `127.0.0.1:9280`; otherwise start fresh — **never pass `--state-dir`/`SERF_STATE_DIR`**):
  ```bash
  set -a; . /Users/jesse/prime-radiant/toil-suite/serf/.env; set +a
  /tmp/serf-hub-ask -addr 127.0.0.1:9280 -serf /tmp/serf-ask &
  sleep 2
  TOKEN=$(cat ~/.serf/auth-token)
  HUB=http://127.0.0.1:9280
  ```
- `tmux` available.

## Steps

1. Spawn a session that asks a question, and drive it to `awaiting` **before** the TUI ever
   attaches — this is a cold-attach test (spec §6: "cold attach and live attach use the same
   rule"), so there is no timing race to catch the transition live:
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-ask-tui-XXXXX)
   body=$(jq -n --arg wd "$tmpdir" '{
     prompt: "Before doing any other work, call the ask_user tool once. Ask exactly one question: header \"Naming\", question \"What should we call the new service?\", with exactly two options: concise (detail \"short internal codename\") and descriptive (detail \"longer, self-explanatory name\"). Do not mark either as recommended. Do not do anything else first.",
     model: "openai/gpt-5.5",
     working_dir: $wd,
     harness: "serf",
     branch: "",
     access_mode: "full",
     agent: "default",
     launch_overrides: {}
   }')
   resp=$(curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d "$body" "$HUB/api/spawn")
   SID=$(echo "$resp" | jq -r '.session_id')
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" = "awaiting" ] && break
     sleep 1
   done
   echo "SID=$SID state=$state"
   ```
2. Launch the TUI in tmux and open the awaiting session by filtering the dashboard palette
   on a distinctive suffix of the session ID (the `/` palette footer reads
   `type filter  up/down navigate  enter select  esc close` — `enter` opens the filtered
   row, per `tui-workspace-navigation.md`):
   ```bash
   tmux kill-session -t serf-ask-tui 2>/dev/null
   tmux new-session -d -s serf-ask-tui -x 200 -y 50 \
     "/tmp/serf-tui-ask --hub-addr 127.0.0.1:9280 --debug 2>/tmp/ask-tui-stderr.log"
   sleep 2
   suffix=${SID: -8}
   tmux send-keys -t serf-ask-tui "/"
   sleep 0.3
   tmux send-keys -t serf-ask-tui -l "$suffix"
   sleep 0.3
   tmux send-keys -t serf-ask-tui Enter
   sleep 1
   ```
3. **Auto-open check (cold attach).** Capture the pane immediately:
   ```bash
   tmux capture-pane -t serf-ask-tui -p | grep -E "question waiting|ctrl\+q to answer|question 1/1"
   ```
4. **Open only by keypress.** Press `ctrl+q` and capture:
   ```bash
   tmux send-keys -t serf-ask-tui C-q
   sleep 0.5
   tmux capture-pane -t serf-ask-tui -p | grep -E "\[Naming\] question 1/1|choose|answer|note|next question|defer"
   ```
5. **Esc defers, does not discard.** Press `Esc`, capture:
   ```bash
   tmux send-keys -t serf-ask-tui Escape
   sleep 0.5
   tmux capture-pane -t serf-ask-tui -p | grep -E "question waiting|ctrl\+q to answer"
   ```
6. **Navigate away and back without a fresh `ctrl+q`.** Return to the dashboard, then
   re-open the same session via the same filter:
   ```bash
   tmux send-keys -t serf-ask-tui C-o
   sleep 0.5
   tmux send-keys -t serf-ask-tui "/"
   sleep 0.3
   tmux send-keys -t serf-ask-tui -l "$suffix"
   sleep 0.3
   tmux send-keys -t serf-ask-tui Enter
   sleep 1
   tmux capture-pane -t serf-ask-tui -p | grep -E "question waiting|question 1/1"
   ```
7. **`ctrl+q` resumes the same (deferred) overlay, not a fresh one:**
   ```bash
   tmux send-keys -t serf-ask-tui C-q
   sleep 0.5
   tmux capture-pane -t serf-ask-tui -p | grep -E "\[Naming\] question 1/1"
   tmux send-keys -t serf-ask-tui Escape
   sleep 0.3
   ```
8. **Typing prose in the composer also answers** — never go through the overlay's
   review/submit; type a plain reply directly and send it:
   ```bash
   tmux send-keys -t serf-ask-tui -l "let's go with descriptive — clearer for new hires"
   sleep 0.3
   tmux send-keys -t serf-ask-tui Enter
   ```
9. Confirm resolution, both live and on disk:
   ```bash
   for i in $(seq 1 60); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" | jq -r '.state // ""')
     [ "$state" != "awaiting" ] && break
     sleep 1
   done
   echo "state=$state"
   tmux capture-pane -t serf-ask-tui -p | grep -E "question waiting" && echo "STILL WAITING (unexpected)" || echo "chip cleared"
   go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:4
   ```

## Expected

- Step 3: the chip `◆ question waiting — ctrl+q to answer` is visible; **no**
  `question 1/1` overlay title and none of the overlay's footer hints appear. This is the
  cold-attach form of "does not auto-open."
- Step 4: the overlay opens with title `[Naming] question 1/1` and footer hints containing
  `choose`, `answer`, `note`, `next question`, `defer`.
- Step 5: the overlay is gone; the `◆ question waiting` chip is visible again (deferred, not
  resolved — the question is still pending).
- Step 6: after leaving the session and re-entering it **without** pressing `ctrl+q`, only
  the chip shows — the overlay does not reappear on its own.
- Step 7: `ctrl+q` reopens the identical `[Naming] question 1/1` overlay (the same pending
  set resumed, not rebuilt).
- Step 9: `state` leaves `awaiting`; the chip is gone; the outline's `USER_INPUT` turn text
  is the raw typed prose (`let's go with descriptive — clearer for new hires`), **not** the
  structured `[answers]` form — proving the free-typed path is a first-class way to answer,
  not a fallback.
- Falsification: if the overlay auto-opens, traps `esc`, or the composer cannot submit prose
  as the reply, the lens rule is broken.

## Cleanup

```bash
tmux kill-session -t serf-ask-tui 2>/dev/null
curl -s -X POST -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}' "$HUB/s/$SID/shutdown" >/dev/null
pkill -f serf-hub-ask
rm -rf "$tmpdir" /tmp/serf-ask /tmp/serf-hub-ask /tmp/serf-tui-ask
```

## Sharp edges

- **`--debug` is required** for deterministic `capture-pane` — without it, bubbletea's
  AltScreen returns screen-buffer escapes instead of plain text (same caveat as every other
  `tui-*` card).
- **Capture after a settle sleep, not immediately** — `capture-pane` right after
  `send-keys` can return the previous frame; `sleep 0.3–0.5` between keys and captures.
- The overlay is a pure widget over an immutable snapshot of the pending questions
  (`newQuestionOverlay` copies them); deferring and resuming never re-fetches or re-derives
  the question set from the transcript, so step 7's "same overlay" check is really checking
  that `toggleAskOverlay` un-defers in place rather than discarding progress.
- If step 8's typed reply had instead been sent from *inside* the still-open overlay's text
  editors, it would compose into a structured resolution, not raw prose — the point of this
  card is specifically that the ordinary **composer**, with no overlay open at all, is an
  equally valid way to answer. Confirm the overlay was deferred (step 7's `Escape`) before
  step 8.
- The chip is driven by `composerPanel.AwaitingQuestion`, independent of whether the overlay
  is open, deferred, or was never opened — it tracks "is this session awaiting with a
  pending question," not overlay visibility.
