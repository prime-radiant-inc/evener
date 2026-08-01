# status-vocabulary-roundtrip: attainable status words and icons agree across sidebar and TUI

**What this covers**: Track A §1 (unified status vocabulary & icons) and §2
(ask-tiering). The live steps compare the attainable your-move and
question-waiting states across the web rail, the TUI dashboard row, and the
TUI session header. A deterministic gate pins the complete `hubapi.StateWord`
vocabulary, including `errored`, without pretending this setup can manufacture
every owning runtime state.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. This card
used to query `[data-ref="local:<id>"]`, `.status-icon` and `data-ask`, all
of which died with the vanilla frontend (`660376f78`). The rail row is now
`[data-session-ref="local:<SID>"]` with the state word inside
`[data-testid="rail-row-activity"]` — and **lowercased** by `humanizeState`
(`shell/rail/RailRow.tsx:138-153`), deliberately diverging from
`hubapi.StateWord`'s sentence case. Compare words case-insensitively.

## Pre-state

- Hub running with a fresh, isolated `$HOME` (its own `~/.serf`, kernel-
  assigned port — see the Setup checklist in `docs/agentic-testing.md`), real
  credentials, no prior sessions.
- A TUI (`serf-tui`) pointed at the same hub, in a tmux session named from
  your own `$run` dir. Use a tall window (`-y 300`+) — the session header
  line scrolls out of a short pane's capture (see Sharp edges).
- `superpowers-chrome:browsing` (or equivalent CDP browser) available for the
  rail assertions, plus a real SPA bundle (`make build-web` — a checkout that
  has never run it serves a one-line `dist/PLACEHOLDER` and no app).

## Steps

1. **[browser-free]** Spawn a session and let it settle to a generic
   `awaiting` (your-move) state — a prompt with no `ask_user` call, e.g.
   "Say hello and stop." Wait for `state=="awaiting"` via
   `GET /api/sessions/local:<id>`.

2. **[browser-free] Every row for this session must agree — assert it on the
   wire.** A live session is listed **twice** in the rail: once in the
   auto-grouped Live tier and again under its own project (a standing
   decision, kata `b8m6`, restated at `shell/rail/Rail.tsx:563-573`). The
   failure mode worth catching is not the duplication but the two rows
   disagreeing, and both rows are built server-side, so the exact assertion
   belongs here rather than in the DOM:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/tree" \
     | jq --arg ref "local:$SID" '
         [ .. | objects | select(.ref? == $ref)
           | {row_id, ref, state, ask_pending: (.ask_pending // false)} ]'
   ```
   Fields are `hubapi.TreeNode` (`hubapi/types.go#TreeNode`): `row_id`, `ref`,
   `state`, `ask_pending`.

3. **[browser] The rail renders what the wire said.** Navigate to
   `/auth?token=$TOKEN&next=/s/local:$SID` and read every rendered copy:
   ```javascript
   (() => {
     const rows = [...document.querySelectorAll('[data-session-ref="local:<SID>"]')];
     return {
       port: location.port,                       // page-identity check, always
       count: rows.length,
       activity: rows.map((r) =>
         r.querySelector('[data-testid="rail-row-activity"]')?.textContent ?? null),
     };
   })()
   ```

4. **[TUI]** In the TUI dashboard, filter to the same session (`/` + a suffix
   of its ID + Enter) and press Enter again to open/attach it. Capture the
   pane and find the `SERF / SESSION` header line, which carries the state
   badge.

5. **Force a genuine `ask_user` question**, then repeat steps 2, 3 and 4, and
   additionally check the TUI dashboard's per-row list (filtered by project,
   *not* opened) for the ask marker.

6. **[browser-free] Deterministic vocabulary gate** for states this live
   setup cannot transition into on demand:
   ```bash
   go test ./hubapi -run '^TestStateWord$' -count=1
   ```

7. **[browser-free] Regression guard on the two-row agreement** — the
   property step 2 samples once, pinned for every tier builder:
   ```bash
   go test ./cmd/serf-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
   ```

## Expected

- **Step 2 (exact, browser-free)**: every object sharing the session's `ref`
  reports the **same** `state` and the same `ask_pending`. For the your-move
  state that is `state:"awaiting"`, `ask_pending:false` on all copies.
  Falsification: two rows for one session disagree on either field — that is
  the reader being unable to tell which listing is stale.
- **Step 3 (rail)**: `count` is the number of tiers the session appears in
  (2 for a live session in a project), and every entry in `activity`
  contains the lowercase word `your move`. The gloss line also carries the
  project and/or branch joined with ` · ` (`RailRow.tsx:231-251`), so match
  a substring, never the whole string. Falsification: the two rendered rows
  carry different state words, or a row's word contradicts step 2's wire
  value.
- **Step 4**: the `SERF / SESSION` header's badge line reads `● YOUR MOVE`.
- **Step 5 (ask-pending)**: step 2's wire rows all flip to
  `ask_pending:true`; every rail row's activity text contains
  `question waiting`; the TUI header badge reads `● QUESTION WAITING`; and
  the TUI dashboard row for this session carries the `◆` marker
  (`cmd/serf-tui/hub_dashboard_view.go:325-328`). Falsification: any surface
  still reads "your move" while another says "question waiting".
- **Step 6**: the deterministic mapping includes
  `StateWord("errored", false) == "Error"` (`hubapi/attention.go:58-79`).
  This pins vocabulary only; it does not claim this scenario produced a live
  `errored` state.
- **Step 7**: `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnState` and
  `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnAPendingAsk` pass
  (`cmd/serf-hub/internal/hubcore/tree_live_agreement_test.go:47,96`,
  registered at `scenarios_fuzz_test.go:32-33`).
- **Falsification (whole card)**: if a rail row's word and the TUI
  header-badge word ever disagree for the same underlying state (e.g. the
  rail says "working" while the TUI still says "AWAITING"), or if two rows
  for the *same* session ever disagree with each other, the shared
  `hubapi.StateWord` delegation is broken or one surface/row bypassed it.
  Case is **not** a disagreement — see Surface above.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` for every session spawned;
  kill the TUI tmux session by the name you derived from `$run`; kill the
  hub by the PID you captured; remove the isolated `$HOME`.

## Sharp edges

- **The AskPending propagation gap this card used to document is FIXED — step
  5 is now a regression guard, not a repro.** The card previously reported
  that only the NeedsYou copy of a rail row carried `ask_pending`, that the
  project-group copy read "Your move" while the session was genuinely
  ask-pending, and that the hub-proxied TUI dashboard dropped the bit
  entirely. Neither holds against current source. `buildTree` resolves the
  marker through one shared closure, `askPendingFor`
  (`cmd/serf-hub/internal/hubcore/tree.go:622-627`), whose own comment
  requires **every** TreeNode builder to use it; all three builders do
  (`:792`, `:996`, `:1094`). On the TUI side, `LocalDaemonEntry` now carries
  `PendingAsk` and `threadFromEntry` sets `SerfThread.AskPending` from it
  (`cmd/serf-hub/internal/appsource/local_daemon.go:38-47,799`), so the
  dashboard's per-row `◆` works through the hub, not just on a direct daemon
  attach. `SetPendingAskFunc` no longer exists at all; the live bit comes
  from a direct `sess.HasPendingAsk()` call (`cmd/serf/serve.go:967`). Step 7
  pins the agreement property so it cannot silently regress again.
- **There is no separate "needs you" section in the rail.** The auto-grouped
  NeedsYou tier was deliberately removed (`Rail.tsx:563-573`, kata `vbh8`
  §2.2) because it listed a session that was already under its own project.
  A row's own Cadence dot carries attention now. `tree.needs_you` is still on
  the wire and still feeds the rail-host badge — do not read its presence as
  evidence of a rendered section.
- **A quiet row has no activity line at all.** The gloss renders only for
  SIGNAL_STATES — `working` / `needs-you` / `failed`
  (`RailRow.tsx:166,487`) — plus depth-0 rows, which get a second line to
  name their project (`:498`). An `idle` session nested under a project row
  therefore has **no** `[data-testid="rail-row-activity"]` at all, and the
  state word moves to the row label's `title` tooltip
  (`rowTooltip`, `:447-463`). Step 3 works because `awaiting` maps to
  `needs-you`; do not reuse its query for an idle session.
- **The TUI session header lives in the scrollable transcript body, not the
  fixed chrome** (`hub_session_view.go`'s session-header lines, rendered
  inside the session main body) — bubbletea's altscreen means `tmux
  capture-pane` only ever sees the *current* frame, not scrollback, so on a
  short window (the 50-row size other `tui-*` cards use) a chatty first turn
  can push the header off the top with no way to scroll back into tmux
  history. Use a tall window (`-y 300`+).
- **The TUI's `StatusBadge` uppercases whatever word it's given**
  (`tuiprim.StatusBadge`), and the web rail lowercases
  (`humanizeState`). Both are deliberate surface-specific styling, not a
  vocabulary mismatch — the shared vocabulary is `hubapi.StateWord`, which
  the TUI reaches through `displayWord` (`hub_dashboard_view.go#displayWord`).
- **Historical result: forcing `errored` live could not be verified in the
  original pass.** A bad model name was rejected at spawn time before a
  session existed, and recoverable provider failures return the live session
  to `idle`. The obsolete procedure has therefore been removed from the
  runnable steps. Do not substitute transcript-tail or API-log heuristics:
  the owning runtime state is the source of truth. `hubapi.TestStateWord`
  pins the vocabulary mapping; a future live `errored` scenario needs a
  deterministic owning-state transition and should be a separate card.
