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

- Complete the full Setup checklist in `docs/agentic-testing.md` in the same
  Bash shell through the `TOKEN=...` step. Its single `run=$(mktemp -d ...)`
  owns fresh `$run/serf-hub`, `$run/serf`, and `$run/serf-tui` binaries, the
  isolated `$HOME=$run/home` and session state, the kernel-assigned hub port,
  and `$run/hub.log` / `$run/hub.pid`. Do not create or reuse another run root.
- `tmux`, `curl`, and `jq` installed (`tmux` 3.4 is known to work).
- Anthropic OAuth or an API key configured so
  `anthropic/claude-haiku-4-5-20251001` can be invoked by the isolated hub.
- `SERF_LIVE_TESTS=1` exported explicitly for this provider-backed scenario;
  the card refuses to start without that opt-in.
- Run every code block below in the same Bash shell. The setup creates one
  workdir and derives a tmux name from the driving shell's PID; all request
  bodies, logs, and pane captures remain under the existing `$run`. The exit
  trap shuts down the spawned session, kills the exact tmux session and
  recorded hub PID, then removes that one run root.

## Steps

Use `tmux send-keys -t "$TMUX_SESSION" ...` to drive input and
`tmux capture-pane -t "$TMUX_SESSION" -p` to read the visible pane.

1. **Bind to the Setup-checklist run and install cleanup**:

   ```bash
   set -euo pipefail

   if [ "${SERF_LIVE_TESTS:-}" != "1" ]; then
     printf 'set SERF_LIVE_TESTS=1 to opt into the live provider scenario\n' >&2
     exit 1
   fi

   RUN_ROOT="${run:-}"
   RUN_OWNED=0
   TMUX_SESSION=""
   HUB="${HUB:-}"
   TOKEN="${TOKEN:-}"
   SETUP_HUBPID="${HUBPID:-}"
   OWNED_HUBPID=""
   SID=""

   cleanup() {
     local exit_code="${1:-0}"
     local run_owned="${RUN_OWNED:-0}"
     local owned_root="${RUN_ROOT:-}"
     local owned_tmux="${TMUX_SESSION:-}"
     local owned_hub="${HUB:-}"
     local owned_token="${TOKEN:-}"
     local owned_hub_pid="${OWNED_HUBPID:-}"
     local owned_sid="${SID:-}"

     if [ "$run_owned" -eq 1 ] && [ -n "$owned_sid" ] && \
       [ -n "$owned_hub" ] && [ -n "$owned_token" ]; then
       curl -fsS -X POST \
         -H "Authorization: Bearer $owned_token" \
         -H "Content-Type: application/json" \
         -d '{}' "$owned_hub/api/sessions/local:$owned_sid/shutdown" >/dev/null || true
     fi
     if [ "$run_owned" -eq 1 ] && [ -n "$owned_tmux" ]; then
       tmux kill-session -t "$owned_tmux" 2>/dev/null || true
     fi
     if [ "$run_owned" -eq 1 ] && [[ "$owned_hub_pid" =~ ^[1-9][0-9]*$ ]]; then
       kill "$owned_hub_pid" 2>/dev/null || true
       wait "$owned_hub_pid" 2>/dev/null || true
     fi
     if [ "$run_owned" -eq 1 ] && [ -n "$owned_root" ] && [ -d "$owned_root" ]; then
       rm -rf "$owned_root"
     fi
     return "$exit_code"
   }

   trap 'cleanup "$?"' EXIT

   if [ -z "$RUN_ROOT" ] || [ ! -d "$RUN_ROOT" ]; then
     printf 'run must be the Setup checklist directory\n' >&2
     exit 1
   fi
   case "$(basename "$RUN_ROOT")" in
     serf-e2e-*) ;;
     *) printf 'run is not a Setup checklist directory: %s\n' "$RUN_ROOT" >&2; exit 1 ;;
   esac
   if [ "${HOME:-}" != "$RUN_ROOT/home" ]; then
     printf 'HOME must be the Setup checklist home under run\n' >&2
     exit 1
   fi

   RECORDED_HUBPID=$(cat "$RUN_ROOT/hub.pid")
   if ! [[ "$RECORDED_HUBPID" =~ ^[1-9][0-9]*$ ]]; then
     printf '%s\n' \
       'hub.pid is not a canonical positive decimal PID; Setup owner retains run' >&2
     exit 1
   fi
   if [ -n "$SETUP_HUBPID" ] && [ "$SETUP_HUBPID" != "$RECORDED_HUBPID" ]; then
     printf '%s\n' \
       'HUBPID does not match run/hub.pid; Setup owner retains run and both PIDs' >&2
     exit 1
   fi
   if ! kill -0 "$RECORDED_HUBPID"; then
     printf 'recorded hub PID is not running; Setup owner retains run\n' >&2
     exit 1
   fi

   OWNED_HUBPID="$RECORDED_HUBPID"
   RUN_OWNED=1

   test -x "$RUN_ROOT/serf-hub"
   test -x "$RUN_ROOT/serf"
   test -x "$RUN_ROOT/serf-tui"
   test -f "$RUN_ROOT/hub.log"

   WORKDIR="$RUN_ROOT/work"
   TMUX_SESSION="serf-queue-$$"
   if [ -z "${PORT:-}" ]; then
     printf 'PORT must name the Setup checklist hub\n' >&2
     exit 1
   fi
   HUB_ADDR="127.0.0.1:$PORT"
   test "$HUB" = "http://$HUB_ADDR"
   RECORDED_TOKEN=$(cat "$HOME/.serf/auth-token")
   test "$TOKEN" = "$RECORDED_TOKEN"

   mkdir "$WORKDIR"
   cp README.md "$WORKDIR/README.md"

   cat > "$WORKDIR/hold-first-turn.sh" <<'EOF'
   #!/usr/bin/env bash
   set -euo pipefail

   touch .first-turn-started
   while [ ! -e .first-turn-release ]; do
     sleep 0.1
   done
   printf '%s\n' 'first turn gate released'
   EOF
   chmod +x "$WORKDIR/hold-first-turn.sh"

   cat > "$WORKDIR/list-file-sizes.sh" <<'EOF'
   #!/usr/bin/env bash
   set -euo pipefail

   for file in *; do
     [ -f "$file" ] || continue
     bytes=$(wc -c < "$file" | tr -d '[:space:]')
     printf '%s %s\n' "$file" "$bytes"
   done
   EOF
   chmod +x "$WORKDIR/list-file-sizes.sh"
   README_BYTES=$(wc -c < "$WORKDIR/README.md" | tr -d '[:space:]')
   ```

   There is no second `mktemp`: `$RUN_ROOT` is exactly the Setup checklist's
   `$run`. Path, basename, and HOME checks alone do not authorize cleanup.
   `OWNED_HUBPID` stays empty and `RUN_OWNED` stays zero until the exact
   `$run/hub.pid` value is a canonical strictly positive decimal matching
   `[1-9][0-9]*`, matches the Setup shell's ambient `HUBPID` when one exists,
   and names a live process. The cleanup function repeats the same positive-PID
   check before `kill` and `wait`; `0`, `00`, and other noncanonical values can
   never acquire process-group semantics there. A missing, invalid, dead, or
   mismatched PID therefore exits nonzero without killing either candidate PID
   or removing the run root; the Setup-checklist owner retains the intact run
   and responsibility for resolving it. Only after all PID checks succeed does
   this card atomically claim cleanup. From then on, the trap returns the
   incoming status and, in order, shuts down the structured spawn SID, kills
   the derived tmux name, kills the trusted PID, and removes only `$run`.

   The positive-pane helper below polls for observable readiness. Its ten-second
   bound only fails a stuck transition; elapsed time is never a success
   condition:

   ```bash
   capture_until() {
     local output="$1"
     shift
     local pane=""
     local needle
     local ready

     for _ in $(seq 1 100); do
       pane=$(tmux capture-pane -t "$TMUX_SESSION" -p)
       ready=1
       for needle in "$@"; do
         if ! grep -F "$needle" <<<"$pane" >/dev/null; then
           ready=0
           break
         fi
       done
       if [ "$ready" -eq 1 ]; then
         printf '%s\n' "$pane" | tee "$output"
         return 0
       fi
       sleep 0.1
     done

     printf '%s\n' "$pane" > "$output"
     return 1
   }
   ```

2. **Spawn the slow first turn through the structured REST API and capture its
   exact session ID before attaching the TUI**:

   ```bash
   FIRST_PROMPT='Run ./hold-first-turn.sh and wait for it to finish before doing anything else. Then read README.md and write a 4-paragraph essay about its main themes. Use formal prose.'
   QUEUE_TEXT='After the first turn is released, run ./list-file-sizes.sh and include its exact README.md line in your final communicate response.'

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

   first_turn_started=0
   for _ in $(seq 1 300); do
     if [ -f "$WORKDIR/.first-turn-started" ]; then
       first_turn_started=1
       break
     fi
     sleep 0.1
   done
   test "$first_turn_started" -eq 1
   ```

   Do not derive `SID` from pane text or assume an uppercase ULID alphabet.
   The `/api/spawn` response is the authority, and valid session IDs may be
   lowercase. The `.first-turn-started` marker is the first-turn control
   authority: the model has actually entered the deliberate blocking operation,
   not merely received a prompt that might take a while.

3. **Launch the first TUI process and open that exact session**. From the
   dashboard, `/` opens the command palette (`updateDashboardKey`,
   `cmd/serf-tui/hub_keys.go#updateDashboardKey`); its session item ID contains
   the full ref (`commandPaletteEntriesForRows`,
   `cmd/serf-tui/command_palette.go#commandPaletteEntriesForRows`), and the
   picker filters on item IDs as well as visible labels
   (`cmd/serf-tui/internal/tuipick/picker_panel.go#PickerPanel.filtered`):

   ```bash
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "\"$RUN_ROOT/serf-tui\" --hub-addr \"$HUB_ADDR\" --auth-token \"$TOKEN\" --no-auto-start-hub --debug 2>\"$RUN_ROOT/tui-live.log\""
   capture_until "$RUN_ROOT/dashboard-live-pane.txt" 'SERF LIVE'
   tmux send-keys -t "$TMUX_SESSION" "/"
   capture_until "$RUN_ROOT/palette-live-pane.txt" 'Command palette'
   tmux send-keys -t "$TMUX_SESSION" -l "$SID"
   tmux send-keys -t "$TMUX_SESSION" Enter
   capture_until "$RUN_ROOT/prequeue-pane.txt" 'enter queue'
   curl -fsS -H "Authorization: Bearer $TOKEN" \
     "$HUB/api/sessions/local:$SID" > "$RUN_ROOT/prequeue-session.json"
   jq -e '
     .state == "active"
     and (.active_turn_id | type == "string" and length > 0)
     and .capabilities.queue == true
   ' "$RUN_ROOT/prequeue-session.json" >/dev/null
   ```

   Confirm the pane shows the visible `queue` composer (not `message`) with
   footer `enter queue  ctrl+s steer ...`. The dashboard, palette,
   and queue composer are visible readiness conditions; the REST response is
   authoritative for active state, the active turn, and queue capability. No
   fixed delay or provider-dependent assistant row is used as a proxy. If the
   REST assertion reports that the first turn has finished, the run did not
   reach the mid-turn precondition and is invalid; do not treat the later steps
   as a pass.

4. **Queue the follow-up through the live TUI**:

   ```bash
   tmux send-keys -t "$TMUX_SESSION" -l "$QUEUE_TEXT"
   tmux send-keys -t "$TMUX_SESSION" Enter
   capture_until "$RUN_ROOT/queue-live-pane.txt" \
     'queued (1)' "  1. $QUEUE_TEXT"

   grep -F 'queued (1)' "$RUN_ROOT/queue-live-pane.txt"
   grep -F "  1. $QUEUE_TEXT" "$RUN_ROOT/queue-live-pane.txt"
   ```

   The composer clears and the queue block contains the complete line:

   ```text
     1. After the first turn is released, run ./list-file-sizes.sh and include its exact README.md line in your final communicate response.
   ```

   There is no ellipsis at this width. The daemon collapses an entry to its
   first line without imposing a length bound (`agent/session_queue.go#firstQueueLine`),
   and `renderQueuePreview` truncates only beyond `width - 6`
   (`cmd/serf-tui/composer_panel.go#renderQueuePreview`). This message is well
   below the 194-rune limit at width 200.

5. **Cold-reattach and prove snapshot authority**. Kill the first TUI process,
   start a new one under the same per-run tmux name, and open the exact SID
   again:

   ```bash
   tmux kill-session -t "$TMUX_SESSION"
   tmux new-session -d -s "$TMUX_SESSION" -x 200 -y 50 \
     "\"$RUN_ROOT/serf-tui\" --hub-addr \"$HUB_ADDR\" --auth-token \"$TOKEN\" --no-auto-start-hub --debug 2>\"$RUN_ROOT/tui-cold.log\""
   capture_until "$RUN_ROOT/dashboard-cold-pane.txt" 'SERF LIVE'
   tmux send-keys -t "$TMUX_SESSION" "/"
   capture_until "$RUN_ROOT/palette-cold-pane.txt" 'Command palette'
   tmux send-keys -t "$TMUX_SESSION" -l "$SID"
   tmux send-keys -t "$TMUX_SESSION" Enter
   capture_until "$RUN_ROOT/queue-cold-pane.txt" \
     'queued (1)' "  1. $QUEUE_TEXT"

   grep -F 'queued (1)' "$RUN_ROOT/queue-cold-pane.txt"
   grep -F "  1. $QUEUE_TEXT" "$RUN_ROOT/queue-cold-pane.txt"

   curl -fsS -H "Authorization: Bearer $TOKEN" \
     "$HUB/api/sessions/local:$SID" > "$RUN_ROOT/cold-session.json"
   jq -e '
     .state == "active"
     and (.active_turn_id | type == "string" and length > 0)
     and .capabilities.queue == true
   ' "$RUN_ROOT/cold-session.json" >/dev/null
   test ! -e "$WORKDIR/.first-turn-release"

   touch "$WORKDIR/.first-turn-release"
   ```

   The new process has no local queue history and received none of the first
   process's notifications. Reproducing the count and full preview therefore
   exercises the `thread/read` snapshot path: session entry clears local queue
   state and applies `detail.Queue` via `applyQueueState`
   (`cmd/serf-tui/hub_notifications.go#applyQueueState`). Falsification: the
   live pane in step 4 has the queue but this cold pane does not, or the count
   or text differs. The REST assertion is deliberately repeated after the cold
   attach: a queue preview alone does not prove the first turn is still active.
   Only after both observations pass does the step release the controlled gate.

6. **Wait for the queued turn to complete successfully and then verify the
   queue has drained**:

   ```bash
   TS=$(find "$HOME/.local/state/serf/projects" \
     -name "$SID.transcript.jsonl" -print -quit)
   test -n "$TS"

   queued_turn_succeeded() {
     jq -e -s --arg queued "$QUEUE_TEXT" --arg listing "README.md $README_BYTES" '
       [
         .[]
         | select(.kind == "entry" and .turn.kind == "USER_INPUT")
         | [.turn.message.content[]? | select(.kind == "text") | .text]
         | join("")
       ] as $inputs
       | (($inputs | length) >= 2 and $inputs[1] == $queued)
       and any(
         .[];
         .kind == "entry"
         and .turn.kind == "TOOL_RESULTS"
         and any(
           (.turn.message.content // [])[];
           .kind == "tool_result"
           and .tool_result.is_error == false
           and ((.tool_result.content | tostring) | contains($listing))
         )
       )
       and any(
         .[];
         .kind == "entry"
         and .turn.kind == "ASSISTANT"
         and any(
           (.turn.message.content // [])[];
           .kind == "tool_call"
           and .tool_call.name == "communicate"
           and ((.tool_call.arguments.message // "") | contains($listing))
         )
       )
     ' "$TS" >/dev/null
   }

   queued_turn_succeeded_flag=0
   for _ in $(seq 1 600); do
     if queued_turn_succeeded; then
       queued_turn_succeeded_flag=1
       break
     fi
     sleep 0.1
   done
   test "$queued_turn_succeeded_flag" -eq 1

   session_idle=0
   for _ in $(seq 1 300); do
     curl -fsS -H "Authorization: Bearer $TOKEN" \
       "$HUB/api/sessions/local:$SID" > "$RUN_ROOT/after-queued-session.json"
     if jq -e '
       .state == "idle"
       and (.active_turn_id == null or .active_turn_id == "")
     ' "$RUN_ROOT/after-queued-session.json" >/dev/null; then
       session_idle=1
       break
     fi
     sleep 0.1
   done
   test "$session_idle" -eq 1

   stable_queue_absent=0
   cur=""
   for _ in $(seq 1 500); do
     prev=$(tmux capture-pane -t "$TMUX_SESSION" -p)
     sleep 0.02
     cur=$(tmux capture-pane -t "$TMUX_SESSION" -p)
     if [ "$prev" = "$cur" ] && ! grep -F 'queued (' <<<"$cur" >/dev/null; then
       stable_queue_absent=1
       break
     fi
   done
   printf '%s\n' "$cur" | tee "$RUN_ROOT/after-drain-pane.txt"
   test "$stable_queue_absent" -eq 1
   ! grep -F 'queued (' "$RUN_ROOT/after-drain-pane.txt"
   ```

   The first bounded poll succeeds only when the transcript's exact second
   `USER_INPUT` equals the queued text, the shell tool returned the expected
   `README.md <bytes>` line without an error, and the queued turn issued a
   `communicate` call carrying that line. Its 60-second ceiling is a failure
   bound, never a success condition. The idle-state poll is a second positive
   contract, not an elapsed-time assumption. The pane loop evaluates absence
   only after two 20 ms-spaced, byte-identical captures, following the
   direct-driving stable-capture rule in `docs/testing.md` (the Go harness
   equivalent is `CaptureStable`, exercised by
   `TestTUITmuxE2E_CaptureStableDuringStream` in
   `cmd/serf-tui/tmux_e2e_test.go#TestTUITmuxE2E_CaptureStableDuringStream`).
   Only that stable frame is persisted and used for the final negative
   queue-row assertion. The transcript checks above prove the queued turn's
   successful file-listing result and final response durably, rather than
   inferring success from the queue row disappearing.

7. **Clean up explicitly** (the exit trap performs the same cleanup after an
   earlier failure):

   ```bash
   trap - EXIT
   cleanup
   ```

## Expected

- Step 3: the first turn is genuinely active, queue capability is advertised,
  and the composer says `queue` / `enter queue`. A `message`, `steer`, or
  read-only composer falsifies the precondition or the queue-mode behavior.
- Step 4: Enter calls `turn/queue`, clears the draft, and renders `queued (1)`
  plus the exact queue line with no ellipsis. Immediate appearance as a new
  transcript turn means Enter routed through the wrong method; absence of the
  queue block means the authoritative `thread/queueChanged` state did not
  reach the TUI.
- Step 5: a brand-new TUI process renders the same count and text immediately
  after opening the session. A missing or different cold preview means the
  `thread/read` snapshot was not applied authoritatively.
- Step 6: after the gate releases, the transcript positively contains the
  queued text as the second user-input turn, a successful file-listing tool
  result containing `README.md <bytes>`, and a queued-turn `communicate` call
  carrying that same line. The REST state is idle, and only then do two
  consecutive stable captures prove the queue block is absent. The daemon
  contract is covered deterministically by
  `TestSession_Enqueue_DrainsAfterTurnCompletes`
  (`agent/session_lifecycle_test.go#TestSession_Enqueue_DrainsAfterTurnCompletes`).

## Cleanup

The step-1 exit trap owns cleanup for every path: it posts shutdown for
`local:$SID` when spawn succeeded, kills only `$TMUX_SESSION`, kills and waits
for the exact PID read from `$run/hub.pid`, and removes only `$RUN_ROOT`, in
that order. The explicit step 7 disarms the trap before invoking the same
function once.

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
