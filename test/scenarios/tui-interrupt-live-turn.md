# tui-interrupt-live-turn: serf-tui /interrupt fires against a real mid-turn session

**What this covers**: kata `9sck`. The web title-bar interrupt is
verified by `workspace-title-bar-actions.md` (kata `gx92` + the
`k7t8` cancelFunc fix landed in `20c8a33`). The Bubble Tea TUI's
session command palette exposes `/interrupt` (kata `57be`), but we
have never driven that entry against a live in-flight turn. This
scenario does — via `tmux send-keys` / `capture-pane` — and asserts
the post-interrupt behaviour after kata `0ax1`: the session
transitions back to `idle` (NOT `closed`), the partial tool output
is preserved on the transcript along with a system interrupt
marker, the user can immediately send a follow-up message, and the
TUI does not crash. The stale-capabilities bug (kata `4yvd`) that
used to require running `/status` before opening the palette was
fixed in `f305d8a`: the TUI now refreshes session detail on the
idle→processing transition, so `/interrupt` shows enabled in the
palette as soon as the turn starts.

## Pre-state

- `tmux` installed (tested on tmux 3.4).
- `serf-hub` reachable on `127.0.0.1:9180`. Token at
  `~/.serf/auth-token` (`./serf-hub` launches it; web-only auth).
- `./serf-tui` and `./serf-hub` built and present in the repo root
  (`go build -o serf-tui ./cmd/serf-tui && go build -o serf-hub
  ./cmd/serf-hub`).
- OpenAI OAuth signed in (`./serf openai status` shows
  `source=oauth`). The slow-turn prompt needs the model to actually
  call `exec_command`; `openai/gpt-5.4-mini` does so reliably.
- No leftover `tmux` session named `serf-interrupt-test` (the
  scenario kills it first).

## Steps

Shared setup:

```bash
TOKEN=$(cat ~/.serf/auth-token)
HUB=http://localhost:9180
tmpdir=$(mktemp -d -t serf-e2e-9sck-XXXXX)
tmux kill-session -t serf-interrupt-test 2>/dev/null
```

1. **Spawn a fresh session** via the REST API (cleanest path: gives
   you a known session_id to navigate to in the TUI):
   ```bash
   resp=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":\"reply with the word 'ready' and nothing else\",\"model\":\"openai/gpt-5.4-mini\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     $HUB/api/spawn)
   SID=$(echo "$resp" | python3 -c "import json,sys; print(json.load(sys.stdin)['session_id'])")
   # wait for idle
   for i in $(seq 1 30); do
     state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
              | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
     [ "$state" = "idle" ] && break
     sleep 1
   done
   echo "SID=$SID state=$state"
   ```

2. **Launch serf-tui in tmux**:
   ```bash
   tmux new-session -d -s serf-interrupt-test -x 200 -y 50 \
     "./serf-tui --hub-addr 127.0.0.1:9180 --debug"
   sleep 1
   tmux capture-pane -t serf-interrupt-test -p | head -5
   ```
   Header line should read `serf live ... http://127.0.0.1:9180 · N
   live` and the project for `$tmpdir` should be visible in the
   tree.

3. **Navigate to the session leaf**. The cursor starts on the first
   project header. Send `Down` until `>` is on the leaf row whose
   text contains your `$SID`. (Exact key count depends on dashboard
   contents.) Then `Enter`:
   ```bash
   tmux send-keys -t serf-interrupt-test Down Down Down
   sleep 0.3
   tmux send-keys -t serf-interrupt-test Enter
   sleep 0.5
   tmux capture-pane -t serf-interrupt-test -p | head -10
   ```
   The header should now read
   `serf / session / reply with the word 'ready' ...` and the status
   line shows `state: idle  model: gpt-5.4-mini`.

4. **Send a slow turn**. The prompt must force a real tool call,
   otherwise the model will fabricate the loop output and finish
   instantly (sharp edge from `workspace-title-bar-actions.md`):
   ```bash
   tmux send-keys -t serf-interrupt-test -l 'You MUST call the exec_command tool with command="bash -c '\''for i in $(seq 1 30); do echo step $i; sleep 2; done'\''". Do not fabricate output; actually run the tool. Wait for it to complete before composing your communicate response.'
   sleep 0.2
   tmux send-keys -t serf-interrupt-test Enter
   sleep 2
   ```

5. **Confirm the turn is in flight** (this is what the interrupt
   will cancel):
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   print('state=', d.get('state'))
   print('active_turn_id=', d.get('active_turn_id'))
   print('caps.interrupt=', d.get('capabilities', {}).get('interrupt'))
   "
   ```
   Expect: `state=active`, `active_turn_id=turn_N`,
   `caps.interrupt=True`. Also confirm via `ps`:
   ```bash
   ps -ef | grep -E 'bash -c.*seq 1 30' | grep -v grep
   ```
   should show the spawned loop process running.

6. **Open the session command palette**:
   ```bash
   tmux send-keys -t serf-interrupt-test C-p
   sleep 0.3
   tmux capture-pane -t serf-interrupt-test -p | head -25
   ```
   The palette overlay shows the command list. The crucial line:
   ```
   /interrupt  interrupt the active turn
   ```
   with NO `disabled: ...` suffix. If you see
   `disabled: source does not advertise interrupt`, kata `4yvd` has
   regressed — the TUI is no longer refreshing session detail on
   the idle→processing transition (see `f305d8a`), or the daemon
   stopped advertising `Interrupt: true` mid-turn (that would also
   break the REST path).

7. **Filter to interrupt and fire**:
   ```bash
   tmux send-keys -t serf-interrupt-test -l 'interrupt'
   sleep 0.2
   tmux send-keys -t serf-interrupt-test Enter
   sleep 1.0
   ```

8. **Verify the interrupt landed**:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
     | python3 -c "
   import json, sys
   d = json.load(sys.stdin)
   print('state=', d.get('state'))
   print('live=', d.get('live'))
   print('active_turn_id=', d.get('active_turn_id'))
   "
   tmux capture-pane -t serf-interrupt-test -p | head -25
   ```
   REST expects:
   ```
   state= idle
   live= True
   active_turn_id= None
   ```
   TUI pane status line should show `state: idle` and somewhere on
   the transcript view: `Serf error: context canceled`. The session
   stays alive (kata `0ax1`); only the active turn was cancelled.

9. **Verify the transcript preserved the mid-turn state plus the
   interrupt marker**:
   ```bash
   TFILE=$(find ~/.local/state/serf/projects -name "$SID.transcript.jsonl")
   tail -4 "$TFILE" | python3 -c "
   import json, sys
   for line in sys.stdin:
       j = json.loads(line)
       t = j.get('turn', {})
       msg = t.get('message', {})
       print('kind=', t.get('kind'))
       for c in msg.get('content', []):
           k = c.get('kind')
           if k == 'tool_call':
               print(' tool_call', c['tool_call'].get('name'))
           elif k == 'tool_result':
               print(' tool_result', str(c.get('tool_result', {}).get('content'))[:200])
           elif k == 'text':
               print(' text', str(c.get('text',''))[:200])
   "
   ```
   Expect the tail to include the `ASSISTANT` tool_call for
   `exec_command`, a `TOOL_RESULTS` whose content shows the partial
   output (`step 1\nstep 2\n...step 13` or similar, however many
   steps got through) terminated by `[ERROR: Command was canceled]`,
   and then a final `STEERING` turn whose text contains
   `The user interrupted the previous turn`. The model never got
   to issue its final `communicate` for this turn; that's the kata
   `0ax1` semantic. Verify the bash loop process is gone:
   ```bash
   ps -ef | grep -E 'bash -c.*seq 1 30' | grep -v grep || echo 'loop gone'
   ```

10. **Send a follow-up message** (the kata `0ax1` promise: the
    session is immediately usable after an interrupt). Type a quick
    prompt and confirm it runs to completion:
    ```bash
    tmux send-keys -t serf-interrupt-test -l 'reply with only the single word OK'
    sleep 0.2
    tmux send-keys -t serf-interrupt-test Enter
    for i in $(seq 1 30); do
      state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
               | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
      [ "$state" = "idle" ] && break
      sleep 1
    done
    tail -3 "$TFILE" | python3 -c "
    import json, sys
    for line in sys.stdin:
        j = json.loads(line)
        t = j.get('turn', {})
        for c in t.get('message', {}).get('content', []):
            if c.get('kind') == 'tool_call' and c.get('tool_call', {}).get('name') == 'communicate':
                print('REPLY:', c['tool_call']['arguments'].get('message'))
    "
    ```
    The follow-up reply should appear (typically `OK`), proving the
    session loop is alive after the interrupt.

11. **Confirm the TUI did not crash**:
    ```bash
    tmux ls | grep serf-interrupt-test
    ps -ef | grep -E 'serf-tui.*--hub-addr' | grep -v grep
    ```
    Both should still report the process alive.

12. **Interrupt-with-queue (kata `0bq1` natural composition).**
    The session is idle again; start a fresh slow turn, then
    while it's processing queue a follow-up via Enter and
    interrupt the turn via the palette. The queued line should
    survive the interrupt and run as the next user turn.
    ```bash
    tmux send-keys -t serf-interrupt-test -l 'You MUST call exec_command with command="bash -c '\''for i in $(seq 1 15); do echo loop $i; sleep 2; done'\''". Do not fabricate output.'
    tmux send-keys -t serf-interrupt-test Enter
    sleep 3
    tmux capture-pane -t serf-interrupt-test -p | head -25
    ```
    Composer label should now read `queue` with footer `enter:
    queue  ctrl+s: send as steer …`. Queue the follow-up:
    ```bash
    tmux send-keys -t serf-interrupt-test -l 'after that loop, reply with the single word DONE'
    tmux send-keys -t serf-interrupt-test Enter
    sleep 0.5
    tmux capture-pane -t serf-interrupt-test -p | head -30
    ```
    `queued (1)` block above the composer. Now interrupt:
    ```bash
    tmux send-keys -t serf-interrupt-test C-p
    sleep 0.3
    tmux send-keys -t serf-interrupt-test -l 'interrupt'
    sleep 0.2
    tmux send-keys -t serf-interrupt-test Enter
    sleep 1.5
    tmux capture-pane -t serf-interrupt-test -p | head -30
    ```
    After the interrupt, the session goes idle and the daemon's
    queue (already populated by step 12's Enter) immediately
    drains into a new user turn. The `queued (...)` preview row
    is gone (popped on `turn/completed` from the cancelled
    turn) and a fresh processing turn for the `DONE` follow-up
    starts. Wait for it to finish:
    ```bash
    for i in $(seq 1 30); do
      state=$(curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/sessions/local:$SID" \
               | python3 -c "import json,sys; print(json.load(sys.stdin).get('state'))")
      [ "$state" = "idle" ] && break
      sleep 1
    done
    tail -3 "$TFILE" | python3 -c "
    import json, sys
    for line in sys.stdin:
        j = json.loads(line)
        t = j.get('turn', {})
        for c in t.get('message', {}).get('content', []):
            if c.get('kind') == 'tool_call' and c.get('tool_call', {}).get('name') == 'communicate':
                print('REPLY:', c['tool_call']['arguments'].get('message'))
    "
    ```
    Reply text should contain `DONE`. Falsification: the queued
    follow-up is dropped (means the daemon's interrupt path
    cleared the queue — file a regression against
    `agent/session.go` ProcessInput abort handling) or the
    queue preview persists in the TUI past the interrupt (means
    `popSessionQueueHead` regressed on the cancelled completion).

13. **Exit cleanly**. From the session view, `Ctrl+O` to return to
    the dashboard, then `q`:
    ```bash
    tmux send-keys -t serf-interrupt-test C-o
    sleep 0.3
    tmux send-keys -t serf-interrupt-test q
    sleep 0.5
    tmux ls | grep serf-interrupt-test || echo 'tmux session ended'
    ```

## Expected

- **Step 1**: spawn returns `session_id`; session reaches `idle`
  within ~10 s.
- **Step 2**: TUI dashboard renders with the new session as a leaf
  under its `$tmpdir` project.
- **Step 4**: status line flips to `state: active  ...  busy:
  turn_N`. Transcript view shows the new user message and a
  `▸ shell  Run requested 30-step loop command and capture output
  ...` row.
- **Step 5**: REST reports `state=active`,
  `capabilities.interrupt=true`; bash loop process visible in `ps`.
- **Step 6**: palette shows `/interrupt` enabled within ~1 s of the
  turn starting, with no user intervention needed beyond opening the
  palette (the TUI auto-refreshed its capability snapshot on the
  idle→processing transition, fix `f305d8a`). **Falsification**: if
  `/interrupt` is `disabled: source does not advertise interrupt`,
  kata `4yvd` regressed — `applyHubNotification` is no longer
  fetching session detail on the into-processing branch, or the
  daemon stopped advertising `Interrupt: true` mid-turn (that would
  also break the REST path).
- **Step 7-8**: within ~1-2 s of Enter, session state flips from
  `active` back to `idle` (`live=true`, `active_turn_id`
  cleared). TUI status line shows `state: idle`. The session stays
  alive — the abort signal cancels the *turn*, not the *session*
  (kata `0ax1`). The active turn is reported `status=canceled` on
  the appwire `turn/completed` notification. **Falsification**:
  state stays `active` for the full ~60 s of the bash loop, OR
  state flips to `closed`/`ended` (the old pre-`0ax1` semantic —
  would mean ProcessInput's abort path is calling `s.Close()`
  again), OR REST returns 503 on the underlying interrupt POST
  (would mean `cancelFunc` never wired, i.e. k7t8 regressed).
- **Step 9**: transcript ends with the partial loop output plus
  `[ERROR: Command was canceled]` baked into the `TOOL_RESULTS`
  entry, followed by a `STEERING` turn carrying the
  `<SYSTEM-REMINDER>The user interrupted the previous turn ...`
  marker. No `communicate` tool_call from the assistant for the
  interrupted turn (model never got to compose one). Bash loop
  process is gone.
- **Step 10 (follow-up after interrupt)**: the new prompt sails
  through. State cycles `idle → active → idle`; the assistant
  emits a `communicate` reply (typically `OK`). This is the kata
  `0ax1` user-facing promise: an interrupt does not lock the user
  out of the session.
- **Step 11**: TUI process and tmux session both still alive after
  the interrupt — the abort signal closes neither the session nor
  the TUI.
- **Step 12 (interrupt-with-queue, kata `0bq1` composition)**:
  the queued follow-up runs as a new user turn immediately after
  the interrupt. The `queued (N)` preview row clears the moment
  the cancelled turn's `turn/completed` notification arrives
  (the TUI's `popSessionQueueHead` runs on non-failed
  completion). The agent's `communicate` reply for the new turn
  contains `DONE`. Falsification: queue persists past the
  interrupt, OR no new turn starts despite the queue having
  been populated (the daemon dropped the queue when handling
  the interrupt — file a regression against
  `agent/session.go`).
- **Step 13**: `q` from the dashboard ends the tmux session
  cleanly.

## Cleanup

```bash
tmux kill-session -t serf-interrupt-test 2>/dev/null
rm -rf "$tmpdir"
# meta + transcript files under ~/.local/state/serf/projects linger
# (harmless). Optional:
# find ~/.local/state/serf/projects -name "$SID*" -delete
```

## Sharp edges

- **TUI capability snapshot used to go stale mid-turn** (kata
  `4yvd`, fixed in `f305d8a`). At session-open the TUI fetched the
  detail while idle (so `Interrupt=false`). The original
  `applyHubNotification` only re-fetched when `ThreadStatusChanged`
  transitioned OUT of `processing`, so the cached idle snapshot
  stuck around for the entire turn and the palette rendered
  `/interrupt` as disabled even when the daemon and REST API both
  said it was available. The fix is to fetch on any status
  transition (in particular idle→processing), so the palette
  reflects the source's live capability set as soon as the turn
  starts. The pre-fix workaround — typing `/status` to force a
  refresh — is no longer needed.
- **Slow-turn prompts must force a real tool call.** The model
  loves to shortcut by fabricating the loop output and calling
  `communicate` immediately — turn finishes in <1 s and there is
  nothing to interrupt. The phrasing in step 4 (`You MUST call ...
  Do not fabricate output; actually run the tool. Wait for it to
  complete ...`) was needed to get `gpt-5.4-mini` to actually
  invoke `exec_command`. If your `ps` check in step 5 shows no
  `bash -c ... seq 1 30` process, the model shortcut — re-roll the
  prompt with stronger insistence.
- **Tmux capture timing.** Always `sleep 0.1-0.5` between
  `send-keys` and `capture-pane`. Bubble Tea coalesces redraws and
  tmux scrapes the screen on a tick. 200-300 ms is the comfortable
  margin; immediate captures show the previous frame.
- **`tmux send-keys -l <text>` sends text literally.** Use it for
  the prompt body. Use the un-flagged form for named keys
  (`Enter`, `Down`, `C-p`, `Escape`). The interrupt prompt mixes
  both: type the body with `-l`, send `Enter` separately.
- **Interrupt cancels the turn, not the session** (kata `0ax1`).
  After kata `k7t8` wired the per-turn cancel, kata `0ax1` changed
  ProcessInput's abort path in `agent/session.go` to flip state
  back to `SessionIdle` (and append a `STEERING` interrupt marker
  to the transcript) instead of calling `s.Close()`. Matches
  Claude Code / codex: the user clicks interrupt, the in-flight
  turn dies, the session stays ready for the next message.
  Subsequent `send`, `compact`, `clear`, `shutdown` all keep
  working on the same SID. If a regression makes state go to
  `closed`/`ended` after an interrupt, the abort handling in
  `ProcessInput` is calling `s.Close()` again.
- **`q` only exits from the dashboard.** From the session view,
  `q` is treated as composer input (gets typed into the prompt).
  Use `Ctrl+O` to return to the dashboard first, then `q`. Or send
  `C-c C-c` within 1 s (see `tui-workspace-navigation.md`).
- **TUI state name vs REST state name.** Only matters for the
  terminal `closed` state (e.g. after `shutdown`): REST
  `/api/sessions/...` reports `state=closed` while the TUI status
  line displays `state: ended`. The TUI normalises `closed` →
  `ended` for display in `hubDetailFromAppThread` /
  `normalizeState`. For `idle` and `processing` both views use the
  same string.
