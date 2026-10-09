# status-vocabulary-roundtrip: attainable status states map across sidebar and TUI

**What this covers**: Track A §1 (status vocabulary & icons) and §2
(ask-tiering). The live steps verify that attainable your-move and
question-waiting states map to the web rail's `Needs you` category while the
title HoverCard preserves the detailed state and the TUI keeps its dashboard
and session-header wording. A deterministic gate pins the complete
`hubapi.StateWord` vocabulary, including `errored`, without pretending this
setup can manufacture every owning runtime state.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. This card
used to query `[data-ref="local:<id>"]`, `.status-icon` and `data-ask`, all
of which died with the vanilla frontend (`660376f78`). The rail row is now
`[data-session-ref="local:<SID>"]`; its compact signal exposes the category
through `rail-status-spinner` / `rail-status-dot` and an accessible label,
while hovering `[data-testid="rail-row-title"]` exposes the detailed status
in the context card.

## Pre-state

- Hub running with a fresh, isolated `$HOME` (its own `~/.evener`, kernel-
  assigned port — see the Setup checklist in `docs/developing-evener/agentic-testing.md`), real
  credentials, no prior sessions.
- A TUI (`evener tui`) pointed at the same hub, in a tmux session named from
  your own `$run` dir. Use a tall window (`-y 300`+) — the session header
  line scrolls out of a short pane's capture (see Sharp edges).
- `superpowers-chrome:browsing` (or equivalent CDP browser) available for the
  rail assertions, plus a real SPA bundle (`make build-web` — a checkout that
  has never run it serves a one-line `dist/PLACEHOLDER` and no app).

## Steps

1. **[browser-free]** Spawn a session and let it settle to a generic
   `awaiting` state with no pending question — a prompt with no `ask_user`
   call that ends on `needs_response`, e.g. "Ask me in one sentence which
   color I prefer, then end your turn with end_reason needs_response. Do not
   use ask_user." (A plain reply such as "Say hello and stop." rests `idle`.)
   Wait for `state=="awaiting"` via `GET /api/sessions/local:<id>`.

2. **[browser-free] Every row for this session must agree — assert it on the
   wire.** A live session is listed **twice** in the rail: once in the
   auto-grouped Live tier and again under its own project (a standing
   decision, kata `b8m6`, restated at `shell/rail/Rail.tsx:563-573`). The
   failure mode worth catching is not the duplication but the two rows
   disagreeing, and both rows are built server-side, so the exact assertion
   belongs here rather than in the DOM:
   On the authenticated `/rpc` AppWire connection, read the `live` and
   `needs_you` sections with `evener/navigation/read`, read `pin_catalog` and
   each nonempty `pin_section`, and read the target's `location` resource to
   obtain its `project_key`. Then read
   `{"resource":"project","projectKey":"<key>"}` and inspect its `current`,
   `recent`, and `archived` session arrays. While any tier reports
   `remaining > 0`, page that tier with `project_page` until it reaches zero.
   Collect every returned copy of the target ref as the wire assertion.
   Fields are `NavigationSessionSummary`
   (`hubapi/navigation.go#NavigationSessionSummary`): `ref`, `state`,
   `ask_pending`, and `project`.

3. **[browser] The rail renders what the wire said.** Navigate to
   `/auth/$TOKEN?next=/s/local:$SID` and read every rendered copy:
   ```javascript
   (() => {
     const rows = [...document.querySelectorAll('[data-session-ref="local:<SID>"]')];
     return {
       port: location.port,                       // page-identity check, always
       count: rows.length,
       indicators: rows.map((r) => {
         const signal = r.querySelector('[data-testid="rail-status-spinner"], [data-testid="rail-status-dot"]');
         return signal?.getAttribute("aria-label") ?? null;
       }),
     };
   })()
   ```

4. **[TUI]** In the TUI dashboard, filter to the same session (`/` + a suffix
   of its ID + Enter) and press Enter again to open/attach it. Capture the
   pane and find the `EVENER / SESSION` header line, which carries the state
   badge.

5. **Force a genuine `ask_user` question**, then repeat steps 2, 3 and 4.
   Hover `[data-session-ref="local:<SID>"] [data-testid="rail-row-title"]`, read that
   title's `aria-describedby`, then read the element with that ID; its header must say
   `Question waiting`. Additionally
   check the TUI dashboard's per-row list (filtered by project, *not* opened)
   for the ask marker.

6. **[browser-free] Deterministic vocabulary gate** for states this live
   setup cannot transition into on demand:
   ```bash
   go test ./hubapi -run '^TestStateWord$' -count=1
   ```

7. **[browser-free] Regression guard on the two-row agreement** — the
   property step 2 samples once, pinned for every tier builder:
   ```bash
   go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
   ```

## Expected

- **Step 2 (exact, browser-free)**: every object sharing the session's `ref`
  reports the **same** `state` and the same `ask_pending`. For the your-move
  state that is `state:"awaiting"`, `ask_pending:false` on all copies.
  Falsification: two rows for one session disagree on either field — that is
  the reader being unable to tell which listing is stale.
- **Step 3 (rail)**: `count` is the number of tiers the session appears in
  (2 for a live session in a project), and every entry in `indicators` is
  `Needs you`. Falsification: the two rendered rows carry different status
  categories, or a row's category contradicts step 2's wire value.
- **Step 4**: the `EVENER / SESSION` header's badge line reads `● YOUR MOVE`.
- **Step 5 (ask-pending)**: step 2's wire rows all flip to
  `ask_pending:true`; every rail row retains the `Needs you` indicator, the
  hovered context card reads `Question waiting`, the TUI header badge reads
  `● QUESTION WAITING`, and the TUI dashboard row for this session carries the `◆` marker
  (`cmd/evener-tui/hub_dashboard_view.go:325-328`). Falsification: any surface
  still reads "your move" while another says "question waiting".
- **Step 6**: the deterministic mapping includes
  `StateWord("errored", false) == "Error"` (`hubapi/attention.go:58-79`).
  This pins vocabulary only; it does not claim this scenario produced a live
  `errored` state.
- **Step 7**: `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnState` and
  `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnAPendingAsk` pass
  (`cmd/evener-hub/internal/hubcore/tree_live_agreement_test.go:47,96`,
  registered at `scenarios_fuzz_test.go:32-33`).
- **Falsification (whole card)**: if a rail row's category contradicts the
  TUI's detailed word for the same underlying state (for example, the rail
  says `Running` while the TUI says `AWAITING`), if the HoverCard loses the
  ask-specific detail, or if two rows for the *same* session disagree with
  each other, one surface or row bypassed the shared state.

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
  (`cmd/evener-hub/internal/hubcore/tree.go:622-627`), whose own comment
  requires **every** TreeNode builder to use it; all three builders do
  (`:792`, `:996`, `:1094`). On the TUI side, `LocalDaemonEntry` now carries
  `PendingAsk` and `threadFromEntry` sets `EvenerThread.AskPending` from it
  (`cmd/evener-hub/internal/appsource/local_daemon.go:38-47,799`), so the
  dashboard's per-row `◆` works through the hub, not just on a direct daemon
  attach. `SetPendingAskFunc` no longer exists at all; the live bit comes
  from a direct `sess.HasPendingAsk()` call (`cmd/evener/serve.go:967`). Step 7
  pins the agreement property so it cannot silently regress again.
- **There is no separate "needs you" section in the rail.** The auto-grouped
  NeedsYou tier was deliberately removed (`Rail.tsx:563-573`, kata `vbh8`
  §2.2) because it listed a session that was already under its own project.
  A row's own Cadence dot carries attention now. `tree.needs_you` is still on
  the wire and still feeds the rail-host badge — do not read its presence as
  evidence of a rendered section.
- **A quiet row has no indicator.** `rail-status-spinner` / `rail-status-dot`
  renders only for `working`, `needs-you`, and `failed`; an idle session has
  neither. Hover `rail-row-title` and read its context card when a scenario
  needs the quiet row's detailed status.
- **The TUI session header lives in the scrollable transcript body, not the
  fixed chrome** (`hub_session_view.go`'s session-header lines, rendered
  inside the session main body) — bubbletea's altscreen means `tmux
  capture-pane` only ever sees the *current* frame, not scrollback, so on a
  short window (the 50-row size other `tui-*` cards use) a chatty first turn
  can push the header off the top with no way to scroll back into tmux
  history. Use a tall window (`-y 300`+).
- **The TUI and rail have different display depths.** The TUI's `StatusBadge`
  uppercases the detailed `hubapi.StateWord` value. The compact web rail maps
  that state to `Running`, `Needs you`, or `Broken`; its HoverCard restores
  details such as `Question waiting` and `Restart required`.
- **Historical result: forcing `errored` live could not be verified in the
  original pass.** A bad model name was rejected at spawn time before a
  session existed, and recoverable provider failures return the live session
  to `idle`. The obsolete procedure has therefore been removed from the
  runnable steps. Do not substitute transcript-tail or API-log heuristics:
  the owning runtime state is the source of truth. `hubapi.TestStateWord`
  pins the vocabulary mapping; a future live `errored` scenario needs a
  deterministic owning-state transition and should be a separate card.
