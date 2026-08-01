# tui-queue-then-completes: enqueue a user message mid-turn, see it run as the next turn

**What this covers**: kata `111a` (TUI surface). While a turn is in flight,
the hub TUI's composer uses `queue` mode
(`cmd/serf-tui/composer_panel.go#hubComposerModeQueue`). Pressing Enter follows
the queue branch in `(*hubModel).updateSessionKey`
(`cmd/serf-tui/hub_session_keys.go#updateSessionKey`), calls `turn/queue`, and
shows the daemon-owned queue above the composer. When the in-flight turn
finishes cleanly, `(*Session).ProcessInput`
(`agent/session_lifecycle.go#ProcessInput`) drains the queued input as a fresh
turn. This scenario exercises that round trip against a real model and then
starts a second TUI process to prove a cold `thread/read` snapshot reproduces
the same queue state.

## Pre-state

- `tmux`, `curl`, and `jq` installed (`tmux` 3.4 is known to work).
- `serf-hub` reachable on an isolated `$HOME` and free port (never Jesse's
  live hub port; see the Setup checklist in `docs/agentic-testing.md`). The
  isolated token is at `$HOME/.serf/auth-token`.
- `./serf-tui` and `./serf-hub` built in the repo root
  (`go build -o serf-tui ./cmd/serf-tui && go build -o serf-hub
  ./cmd/serf-hub`).
- Anthropic OAuth or an API key configured so
  `anthropic/claude-haiku-4-5-20251001` can be invoked.
- Run every code block below in the same Bash shell. The setup creates one
  unique run root and derives the tmux name from it; all request bodies, logs,
  and pane captures live under that root. The exit trap shuts down the spawned
  session, kills only this run's tmux session, and removes the root.

## Steps

Use `tmux send-keys -t "$TMUX_SESSION" ...` to drive input and
`tmux capture-pane -t "$TMUX_SESSION" -p` to read the visible pane.

1. **Create the run root and install cleanup**:

   ```bash
   set -euo pipefail

   RUN_ROOT=""
   TMUX_SESSION=""
   HUB=""
   TOKEN=""
   SID=""

   cleanup() {
     local exit_code="${1:-0}"
     local owned_root="${RUN_ROOT:-}"
     local owned_tmux="${TMUX_SESSION:-}"
     local owned_hub="${HUB:-}"
     local owned_token="${TOKEN:-}"
     local owned_sid="${SID:-}"

     if [ -n "$owned_tmux" ]; then
       tmux kill-session -t "$owned_tmux" 2>/dev/null || true
     fi
     if [ -n "$owned_sid" ] && [ -n "$owned_hub" ] && [ -n "$owned_token" ]; then
       curl -fsS -X POST \
         -H "Authorization: Bearer $owned_token" \
         -H "Content-Type: application/json" \
         -d '{}' "$owned_hub/api/sessions/local:$owned_sid/shutdown" >/dev/null || true
     fi
     if [ -n "$owned_root" ] && [ -d "$owned_root" ]; then
       rm -rf "$owned_root"
     fi
     return "$exit_code"
   }

   RUN_ROOT=$(mktemp -d -t serf-queue-XXXXXX)
   trap 'cleanup "$?"' EXIT

   WORKDIR="$RUN_ROOT/work"
   TMUX_SESSION="serf-queue-$(basename "$RUN_ROOT")"
   if [ -z "${PORT:-}" ]; then
     printf 'PORT must name the isolated test hub\n' >&2
     exit 1
   fi
   HUB_ADDR="127.0.0.1:$PORT"
   HUB="http://$HUB_ADDR"
   TOKEN=$(cat "$HOME/.serf/auth-token")

   mkdir "$WORKDIR"
   cp README.md "$WORKDIR/README.md"
   ```

   Cleanup-visible variables are initialized before any fallible command, the
   trap is the first command after `mktemp`, and nounset-safe snapshots keep it
   valid on every partial path. The trap returns the incoming exit status, so a
   setup failure is cleaned without being converted into a false success.

2. **Spawn the slow first turn through the structured REST API and capture its
   exact session ID before attaching the TUI**:

   ```bash
   FIRST_PROMPT='Read README.md and write a 4-paragraph essay about its main themes. Use formal prose.'
   QUEUE_TEXT='After the essay, also list the file sizes of every file in the working directory.'

   jq -n --arg prompt "$FIRST_PROMPT" --arg workdir "$WORKDIR" '{
     prompt: $prompt,
     harness: "serf",
     model: "anthropic/claude-haiku-4-5-20251001",
     working_dir: $workdir,
     branch: "",
     access_mode: "full",
     agent: "default",
     launch_overrides: {}
   }' > "$RUN_ROOT/spawn-request.json"

   curl -fsS -X POST \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     --data-binary @"$RUN_ROOT/spawn-request.json" \
     "$HUB/api/spawn" > "$RUN_ROOT/spawn-response.json"

   SID=$(jq -er '.session_id | select(type == "string" and length > 0)' \
     "$RUN_ROOT/spawn-response.json")
   REF=$(jq -er '.ref | select(type == "string" and length > 0)' \
     "$RUN_ROOT/spawn-response.json")
   test "$REF" = "local:$SID"
   ```

   Do not derive `SID` from pane text or assume an uppercase ULID alphabet.
   The `/api/spawn` response is the authority, and valid session IDs may be
   lowercase.

3. **Launch the first TUI process and open that exact session**. From the
   dashboard, `/` opens the command palette (`updateDashboardKey`,
   `cmd/serf-tui/hub_keys.go#updateDashboardKey`); its session item ID contains
   the full ref (`commandPaletteEntriesForRows`,
   `cmd/serf-tui/command_palette.go#commandPaletteEntriesForRows`), and the
   picker filters on item IDs as well as visible labels
   (`cmd/serf-tui/internal/tuipick/picker_panel.go#PickerPanel.filtered`):

   ```bash
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "./serf-tui --hub-addr \"$HUB_ADDR\" --auth-token \"$TOKEN\" --no-auto-start-hub --debug 2>\"$RUN_ROOT/tui-live.log\""
   sleep 1
   tmux send-keys -t "$TMUX_SESSION" "/"
   tmux send-keys -t "$TMUX_SESSION" -l "$SID"
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 0.5
   sleep 3

   tmux capture-pane -t "$TMUX_SESSION" -p | tee "$RUN_ROOT/prequeue-pane.txt"
   curl -fsS -H "Authorization: Bearer $TOKEN" \
     "$HUB/api/sessions/local:$SID" > "$RUN_ROOT/prequeue-session.json"
   jq -e '
     .state == "active"
     and (.active_turn_id | type == "string" and length > 0)
     and .capabilities.queue == true
   ' "$RUN_ROOT/prequeue-session.json" >/dev/null
   ```

   Confirm the pane says `state: active`, the second status row includes
   `queue: ready` and `busy: turn_1`, and the composer label is `queue` (not
   `message`) with footer `enter: queue  ctrl+s: send as steer ...`. An
   assistant message or tool-call row should already be visible. If the REST
   assertion reports that the first turn has finished, the run did not reach
   the mid-turn precondition and is invalid; do not treat the later steps as a
   pass.

4. **Queue the follow-up through the live TUI**:

   ```bash
   tmux send-keys -t "$TMUX_SESSION" -l "$QUEUE_TEXT"
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 0.5
   tmux capture-pane -t "$TMUX_SESSION" -p | tee "$RUN_ROOT/queue-live-pane.txt"

   grep -F 'queued (1)' "$RUN_ROOT/queue-live-pane.txt"
   grep -F "  1. $QUEUE_TEXT" "$RUN_ROOT/queue-live-pane.txt"
   ```

   The composer clears and the queue block contains the complete line:

   ```text
     1. After the essay, also list the file sizes of every file in the working directory.
   ```

   There is no ellipsis at this width. The daemon collapses an entry to its
   first line without imposing a length bound (`agent/session_queue.go#firstQueueLine`),
   and `renderQueuePreview` truncates only beyond `width - 6`
   (`cmd/serf-tui/composer_panel.go#renderQueuePreview`). This message is 81
   runes, well below the 194-rune limit at width 200.

5. **Cold-reattach and prove snapshot authority**. Kill the first TUI process,
   start a new one under the same per-run tmux name, and open the exact SID
   again:

   ```bash
   tmux kill-session -t "$TMUX_SESSION"
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "./serf-tui --hub-addr \"$HUB_ADDR\" --auth-token \"$TOKEN\" --no-auto-start-hub --debug 2>\"$RUN_ROOT/tui-cold.log\""
   sleep 1
   tmux send-keys -t "$TMUX_SESSION" "/"
   tmux send-keys -t "$TMUX_SESSION" -l "$SID"
   tmux send-keys -t "$TMUX_SESSION" Enter
   sleep 0.5
   tmux capture-pane -t "$TMUX_SESSION" -p | tee "$RUN_ROOT/queue-cold-pane.txt"

   grep -F 'queued (1)' "$RUN_ROOT/queue-cold-pane.txt"
   grep -F "  1. $QUEUE_TEXT" "$RUN_ROOT/queue-cold-pane.txt"
   ```

   The new process has no local queue history and received none of the first
   process's notifications. Reproducing the count and full preview therefore
   exercises the `thread/read` snapshot path: session entry clears local queue
   state and applies `detail.Queue` via `applyQueueState`
   (`cmd/serf-tui/hub_notifications.go#applyQueueState`). Falsification: the
   live pane in step 4 has the queue but this cold pane does not, or the count
   or text differs.

6. **Wait for the original turn to finish and the queued message to start
   automatically**:

   ```bash
   sleep 12

   prev=$(tmux capture-pane -t "$TMUX_SESSION" -p)
   sleep 0.02
   cur=$(tmux capture-pane -t "$TMUX_SESSION" -p)
   while [ "$prev" != "$cur" ]; do
     prev="$cur"
     sleep 0.02
     cur=$(tmux capture-pane -t "$TMUX_SESSION" -p)
   done
   printf '%s\n' "$cur" | tee "$RUN_ROOT/after-drain-pane.txt"
   ! grep -F 'queued (' "$RUN_ROOT/after-drain-pane.txt"

   TS=$(find "$HOME/.local/state/serf/projects" \
     -name "$SID.transcript.jsonl" -print -quit)
   test -n "$TS"

   jq -e -s --arg queued "$QUEUE_TEXT" '
     [
       .[]
       | select(.kind == "entry" and .turn.kind == "USER_INPUT")
       | [.turn.message.content[]? | select(.kind == "text") | .text]
       | join("")
     ] as $inputs
     | (($inputs | length) >= 2 and $inputs[-1] == $queued)
   ' "$TS" >/dev/null
   ```

   The two 20 ms-spaced, byte-identical captures follow the direct-driving
   stable-capture rule in `docs/testing.md` (the Go harness equivalent is
   `CaptureStable`, exercised by `TestTUITmuxE2E_CaptureStableDuringStream` in
   `cmd/serf-tui/tmux_e2e_test.go#TestTUITmuxE2E_CaptureStableDuringStream`).
   Only that stable frame is used for the negative queue-row assertion. The
   structured transcript assertion positively proves that a second
   `USER_INPUT` turn started without another keypress and contains exactly the
   follow-up queued in step 4. Eventually the file listing appears in the
   model's `communicate` payload.

7. **Clean up explicitly** (the exit trap performs the same cleanup after an
   earlier failure):

   ```bash
   trap - EXIT
   cleanup
   ```

## Expected

- Step 3: the first turn is genuinely active, queue capability is advertised,
  and the composer says `queue` / `enter: queue`. A `message`, `steer`, or
  read-only composer falsifies the precondition or the queue-mode behavior.
- Step 4: Enter calls `turn/queue`, clears the draft, and renders `queued (1)`
  plus the exact 81-rune line with no ellipsis. Immediate appearance as a new
  transcript turn means Enter routed through the wrong method; absence of the
  queue block means the authoritative `thread/queueChanged` state did not
  reach the TUI.
- Step 5: a brand-new TUI process renders the same count and text immediately
  after opening the session. A missing or different cold preview means the
  `thread/read` snapshot was not applied authoritatively.
- Step 6: after `turn_1` completes, two consecutive stable captures show the
  queue block absent, and the transcript positively contains the queued text
  as the second user-input turn. The daemon contract is covered
  deterministically by `TestSession_Enqueue_DrainsAfterTurnCompletes`
  (`agent/session_lifecycle_test.go#TestSession_Enqueue_DrainsAfterTurnCompletes`).

## Cleanup

The step-1 exit trap owns cleanup for every path: it kills only
`$TMUX_SESSION`, posts shutdown for `local:$SID` when spawn succeeded, and
removes `$RUN_ROOT`. The explicit step 7 disarms the trap before invoking the
same function once.

## Sharp edges

- **Use the structured ID.** The ordinary TUI session pane does not display a
  dependable machine-readable SID. Use `.session_id` from the spawn response;
  do not grep the pane or constrain the ID to an uppercase alphabet.
- **Dashboard and session palette keys differ.** `/` opens the palette on the
  dashboard; Ctrl+P is the session-mode binding. Both attaches in this card
  begin on the dashboard and therefore send `/`.
- **Do not auto-start a fallback hub.** Both TUI invocations pass
  `--no-auto-start-hub`; an unreachable test hub must fail this run rather than
  make the TUI create a second hub outside this run's ownership boundary
  (`cmd/serf-tui/internal/hubstart/hub_start.go#ParseTUIStartupOptions`).
- **The queue preview is authoritative.** `appwire.QueueState` carries queue
  depth and first-line preview text on both `thread/read` and
  `thread/queueChanged`. `applyQueueState` replaces the TUI's snapshot rather
  than appending locally, so another client or a cold TUI converges on the
  daemon.
- **Pop-on-completion assumes clean completion.** `ProcessInput` drains queued
  user input only after the active turn reaches the corresponding lifecycle
  boundary. If the live provider fails the first turn, this scenario has not
  exercised the success path; use the deterministic test cited above to
  distinguish provider failure from a queue regression.
- **Empty-trim still applies.** Enter on a blank composer is a no-op in
  `updateSessionKey`, so accidental blank submissions cannot add queue rows.
- **No `/queue` slash command.** The composer-mode-during-processing path is
  the TUI queue surface today.
- **Auth and provider tax.** This scenario uses a live provider for two turns.
  It is intentionally not part of default deterministic tests.
