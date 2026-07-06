# ended-session-metrics-tui-and-web: an ended session's work time/tokens/cost surface via both the TUI details drawer and the web details panel

**What this covers**: the WS2 gap Track C fills — an ended (non-live) session's
metrics were previously only inferable from raw transcript files. This
exercises both surfaces added/extended to close that gap: the TUI
`/details` palette command (`cmd/serf-tui/details_drawer.go`,
`hub_command_registry.go`'s `fetchCurrentHubSession`) and the web details
panel's ended-session branch (`cmd/serf-hub/web_workspace.go`
`renderDetailsPanel` / `tokensAndCostRows`, commit `35d8b031`).

## Pre-state

- An isolated `serf-hub` (fake `$HOME`, non-9180 port) with `-serf` pointed at
  a freshly built `serf` binary.
- A session spawned via `POST /api/spawn`, sent one prompt to completion, then
  ended via `POST /s/<id>/shutdown`.

## Steps

1. Spawn a session against a live model, send a short prompt, poll
   `/api/sessions/local:<id>` until `state` is no longer `active`
   (`idle` in the current state model for the common no-ask case — see
   Sharp edges).
2. `POST /s/<id>/shutdown`; confirm the daemon process exits and
   `/api/sessions/local:<id>` subsequently reports `state:"ended"`,
   `live:false`.
3. **Web**: `GET /_partials/s/<id>/details` (with `HX-Request: true` and a
   valid bearer/cookie) and inspect the returned `<dl class="details-list">`.
4. **TUI**: launch `serf-tui --hub-addr <host:port> --no-auto-start-hub`,
   navigate Dashboard → project → the ended session's row → Enter to open
   it, open the command palette (`Ctrl+P`... "⌘P" in the footer hint), select
   `/details`, Enter.

## Expected

- Step 3 (web): the details `<dl>` contains `work time` and `tokens` rows
  with real (non-zero, matching the actual turn's usage) values, and (this
  session's model has pricing data) a `cost` row
  (`data-row="cost"`/`dt`+`dd`) with a `~$` estimate. Falsification: the rows
  are absent, or show placeholder/zero values while the live-session variant
  of the same endpoint showed real ones.
- Step 4 (TUI): the details drawer shows `Work:` and `Tokens:` lines with the
  same real values as the web panel (cost is TUI-scope-out per the WS2 plan —
  the TUI gap-fill was tokens/work time, not cost). Falsification: the drawer
  shows blank/zero `Work:`/`Tokens:` for an ended session that has real
  recorded usage.

## Cleanup

- None beyond step 2's shutdown (already ends the session). Delete the
  fake-`$HOME` tmpdir when done with the whole Y2 run.

## Sharp edges

- **State vocabulary**: the current normalized-state model
  (`hubapi/attention.go`) still defines both `idle` (display word "Idle")
  and `awaiting` (display word "Your move") as distinct terminal states for
  a still-live session. `idle` is the common case: per
  `agent/session_tool_round.go`'s `deliverIfCommunicated`, a completed turn
  with no question/ask pending lands on `SessionIdle`, and
  `cmd/serf-hub/internal/hubcore/tree.go`'s `NormalizeState` maps that
  straight through to the `"idle"` string in `/api/sessions/*`. `awaiting`
  is reserved for the less common case where a question/ask is actually
  pending on the session (`boundaryState = SessionAwaiting` only when
  `askedThisRound`). Poll for anything that isn't `active`/`processing`, or
  specifically `idle` for this card's no-ask scenario.
- **This run's actual coverage — fully live, no fallback needed for either
  surface**:
  - Spawned `01KWVAVAB2AZ4T1ETP40ARTA3V` against `openai/gpt-5.5` (real
    OAuth-backed OpenAI Responses API call) with prompt "Reply with exactly
    the words: hello from serf y2 test." The turn completed for real:
    `usage: {input_tokens:12528, output_tokens:41, cache_read_tokens:2304,
    total_tokens:14873}`, `work_millis:3569`.
  - `POST /s/<id>/shutdown` ended it; the daemon PID (85116) exited within
    3s; `/api/sessions/local:<id>` subsequently showed `"state":"ended"`,
    `"live":false`.
  - **Web** `GET /_partials/s/<id>/details` (real HTTP call against the
    isolated hub, `HX-Request: true` + bearer token) returned:
    ```
    <dt>work time</dt><dd>3s</dd>
    <dt>tokens</dt><dd>↑13k ↓41 · cache-read 2k · total 15k</dd>
    <dt data-row="cost">cost</dt><dd data-row="cost">~$0.07</dd>
    ```
    — the ended-session branch of `renderDetailsPanel` is confirmed working
    with real usage data end to end.
  - **TUI**: launched `/tmp/serf-tui --hub-addr 127.0.0.1:9197
    --hub-bin /tmp/serf-hub --no-auto-start-hub --auth-token <token>` in a
    detached tmux session, drove it via `tmux send-keys`/`capture-pane`:
    Dashboard → expanded the project → opened the ended session → `Ctrl+P` →
    `/details` → Enter. The rendered drawer showed:
    ```
    DETAILS  ● NOTLOADED
    Session:  01KWVAVAB2AZ4T1ETP40ARTA3V
    Turns:    3
    Work:     3s
    Tokens:   ↑12k ↓41 · cache-read 2.3k · total 14k
    ```
    confirming the TUI gap-fill with real, non-zero values matching the web
    panel (small formatting differences — `2k` vs `2.3k`, `13k` vs `12k` —
    are expected rounding/precision differences between the two renderers,
    not a discrepancy in the underlying usage data).
  - Observed but out of scope for this card: the TUI drawer's status badge
    read `NOTLOADED` rather than `ENDED` for this session, and "Turns: 3"
    (transcript-line count including the STEERING-style internal entries)
    differs from the web panel's `turns: 1` (user-turn count). Both look
    like pre-existing TUI/past-index label and counting quirks unrelated to
    Track C's cost/settings work; noted here for visibility, not fixed as
    part of this task (out of scope for Phase Y's gates).
