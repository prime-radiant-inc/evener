# ended-session-metrics: an ended session's work time/tokens/cost surface via the web details panel

**What this covers**: the WS2 gap Track C fills — an ended (non-live) session's
metrics were previously only inferable from raw transcript files. This
exercises the web Session-details panel's ended-session rows
(`cmd/evener-hub/frontend/src/panes/session/chrome/DetailsPanel.tsx:183-196`).

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Session operations"
and "Driving the web UI" — the selector map there is the single
place these hooks are maintained. The web half used to `GET
/_partials/s/<id>/details` and read a `<dl class="details-list">`; the whole
`/_partials` URL space and `renderDetailsPanel` / `tokensAndCostRows` went
with the vanilla frontend (`660376f78`) — `handleSession` in `web_workspace.go`
serves only the SPA shell, and neither function exists anywhere now. What
replaces them is one AppWire thread object and one React panel — see steps 3 and 4.

## Pre-state

- An isolated `evener hub` (fake `$HOME`, non-`9180` port — never Jesse's
  real hub; see the Setup checklist in `docs/developing-evener/agentic-testing.md`) with
  `-evener` pointed at a freshly built `evener` binary.
- A session spawned via `POST /api/spawn`, sent one prompt to completion, then
  ended via `POST /api/sessions/local:<id>/shutdown`.
- For step 4 only: a real SPA bundle. A checkout that has never run
  `make build-web` ships a one-line `frontend/dist/PLACEHOLDER` and serves no
  app at all (rebuild matrix item 3 in the runbook).

## Steps

1. **[browser-free]** Spawn a session against a live model, send a short
   prompt, poll `/api/sessions/local:<id>` until `state` is no longer
   `active` (`idle` in the current state model for the common no-ask case —
   see Sharp edges).
2. **[browser-free]** `POST /api/sessions/local:<id>/shutdown`; confirm the
   daemon process exits and `/api/sessions/local:<id>` subsequently reports
   `state:"ended"`, `live:false`.

3. **[browser-free] The exact numbers, from the wire.** The same AppWire thread
   object carries every figure the web panel renders, so the arithmetic assertion
   belongs here rather than in a DOM read:
   ```json
   {"id":3,"method":"thread/read",
    "params":{"ref":"local:<SID>","includeTurns":false}}
   ```
   Read `result.thread.status.type` and the fields on `result.thread.evener`.
   Their wire names come from `appwire.EvenerThread`
   (`appwire/types_thread.go#EvenerThread`): `workMillis`,
   `usage.{inputTokens,outputTokens,cacheReadTokens,totalTokens}`, and the
   server-formatted `cost` string. The web displays that cost verbatim (no
   client-side formatter; the pricing table is Go-side).

4. **[browser] Web.** Navigate to `/auth/$TOKEN?next=/s/local:$SID`,
   open **Session details**, and read the three rows:
   ```javascript
   ({
     port: location.port,                        // page-identity check, always
     workTime: document.querySelector('[data-testid="session-details-work-time"]')?.textContent,
     tokens: document.querySelector('[data-testid="session-details-tokens"]')?.textContent,
     cost: document.querySelector('[data-testid="session-details-cost"]')?.textContent,
     strip: document.querySelector('[data-testid="status-row"]')?.textContent,
   })
   ```
   The ended session's own status strip is a second, independent rendering of
   two of the same figures — `[data-testid="status-row-work-time"]` and
   `[data-testid="status-row-cost"]` inside `EndedSummary`
   (`StatusRow.tsx:250-279`) — so `strip` cross-checks the panel for free.

## Expected

- **Step 3 (exact, browser-free)**: `status.type` is `ended`,
  `evener.workMillis` is a real non-zero measurement matching the turn that ran,
  and `usage` carries non-zero input/output totals. Falsification: `usage` is
  absent or zeroed on a session that demonstrably ran a turn, or
  `workMillis` is 0 — that is the WS2 gap reopening.
- **Step 4 (web)**: all three rows are present with values consistent with
  step 3's wire figures — `session-details-work-time`, `session-details-tokens`
  (rendered `↑<in> ↓<out>`, `DetailsPanel.tsx:188-191`), and, when the model
  has pricing data, `session-details-cost` carrying a `~$` estimate. `strip`
  contains the same work-time and cost text. Falsification: a row is absent
  while step 3 shows a real figure for it, or the panel and the strip
  disagree.
## Cleanup

- None beyond step 2's shutdown (already ends the session). Kill the hub by
  the PID you captured; delete the isolated `$HOME` and the `$run` scratch
  dir when done with the whole Y2 run.

## Sharp edges

- **A row that renders nothing is an honest answer, not a missing row.** The
  panel omits a figure the wire never measured rather than printing a
  fabricated one: `DetailsPanel` gates each row on the value's presence, and
  `formatWorkDuration` clamps a real sub-second duration up to `1s` — which
  is correct for a measurement and a lie for the absence of one
  (`StatusRow.tsx:236-249`). A falsy cost (the daemon's honest "unknown")
  renders nothing rather than `~$0.00`. Check step 3's REST object before
  calling an absent row a regression.
- **The panel's formatter rounds.** The web panel presents the wire's
  counts through a display formatter, so assert against step 3's raw
  integers when a displayed figure looks rounded rather than treating the
  rounding as a data discrepancy.
- **State vocabulary**: the normalized-state model (`hubapi/attention.go`)
  defines both `idle` (display word "Idle") and `awaiting` (display word
  "Needs you") as distinct terminal states for a still-live session. `idle`
  is the common case: per `agent/session_tool_round.go`'s
  `deliverIfCommunicated`, a completed turn with no question/ask pending
  lands on `SessionIdle`, and `cmd/evener-hub/internal/hubcore/tree.go`'s
  `NormalizeState` maps that straight through to the `"idle"` string in
  `/api/sessions/*`. `awaiting` is reserved for a pending question or a turn
  that ended on `needs_response`. Poll for anything that isn't `active`,
  or specifically `idle` for this card's no-ask scenario.
- **The ended session's model switcher is still live.** `EndedSummary`
  deliberately keeps a working `ModelSwitch` (`StatusRow.tsx:235-259`) —
  the hub advertises Send and ChangeModel for a cold exited thread and
  resumes it behind either call. A clickable model chip on an ended session
  is the design, not a stale control.
