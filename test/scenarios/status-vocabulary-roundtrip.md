# status-vocabulary-roundtrip: attainable status states map across the rail and its hovercard

**What this covers**: Track A §1 (status vocabulary & icons) and §2
(ask-tiering). The live steps verify that attainable needs-you and
question-waiting states map to the web rail's `Needs you` category while the
title HoverCard preserves the detailed state. A deterministic gate pins the complete
`hubapi.StateWord` vocabulary, including `errored`, without pretending this
setup can manufacture every owning runtime state.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. This card
used to query `[data-ref="local:<id>"]`, `.status-icon` and `data-ask`, all
of which died with the vanilla frontend (`660376f78`). The rail row is now
`[data-session-ref="local:<SID>"]`; its compact signal exposes the category
through `rail-row-signal` and an accessible label,
while hovering `[data-testid="rail-row-title"]` exposes the detailed status
in the context card.

## Pre-state

- Hub running with a fresh, isolated `$HOME` (its own `~/.evener`, kernel-
  assigned port — see the Setup checklist in `docs/developing-evener/agentic-testing.md`), real
  credentials, no prior sessions.
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
         const signal = r.querySelector('[data-testid="rail-row-signal"] [role="img"]');
         return signal?.getAttribute("aria-label") ?? null;
       }),
     };
   })()
   ```

4. **Force a genuine `ask_user` question**, then repeat steps 2 and 3.
   Hover `[data-session-ref="local:<SID>"] [data-testid="rail-row-title"]`, read that
   title's `aria-describedby`, then read the element with that ID; its header must say
   `Question waiting`.

5. **[browser-free] Deterministic vocabulary gate** for states this live
   setup cannot transition into on demand:
   ```bash
   go test ./hubapi -run '^TestStateWord$' -count=1
   ```

6. **[browser-free] Regression guard on the two-row agreement** — the
   property step 2 samples once, pinned for every tier builder:
   ```bash
   go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1
   ```

## Expected

- **Step 2 (exact, browser-free)**: every object sharing the session's `ref`
  reports the **same** `state` and the same `ask_pending`. For the needs-you
  state that is `state:"awaiting"`, `ask_pending:false` on all copies.
  Falsification: two rows for one session disagree on either field — that is
  the reader being unable to tell which listing is stale.
- **Step 3 (rail)**: `count` is the number of tiers the session appears in
  (2 for a live session in a project), and every entry in `indicators` is
  `Needs you`. Falsification: the two rendered rows carry different status
  categories, or a row's category contradicts step 2's wire value.
- **Step 4 (ask-pending)**: step 2's wire rows all flip to
  `ask_pending:true`; every rail row retains the `Needs you` indicator, the
  hovered context card reads `Question waiting`. Falsification: a surface
  still reads "needs you" while another says "question waiting".
- **Step 5**: the deterministic mapping includes
  `StateWord("errored", false) == "Error"` (`hubapi/attention.go:58-79`).
  This pins vocabulary only; it does not claim this scenario produced a live
  `errored` state.
- **Step 6**: `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnState` and
  `fuzzScenarioBuildTree_LiveAndProjectRowsAgreeOnAPendingAsk` pass
  (`cmd/evener-hub/internal/hubcore/tree_live_agreement_test.go:47,96`,
  registered at `scenarios_fuzz_test.go:32-33`).
- **Falsification (whole card)**: if a rail row's category contradicts the
  wire's detailed state (for example, the rail says `Working` while the
  HoverCard says `AWAITING`), if the HoverCard loses the
  ask-specific detail, or if two rows for the *same* session disagree with
  each other, one surface or row bypassed the shared state.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` for every session spawned;
  kill the hub by the PID you captured; remove the isolated `$HOME`.

## Sharp edges

- **The AskPending propagation gap this card used to document is FIXED — step
  6 is now a regression guard, not a repro.** The card previously reported
  that only the NeedsYou copy of a rail row carried `ask_pending`, that the
  project-group copy read "Your move" while the session was genuinely
  ask-pending. Neither holds against current source. `buildTree` resolves the
  marker through one shared closure, `askPendingFor`
  (`cmd/evener-hub/internal/hubcore/tree.go:622-627`), whose own comment
  requires **every** TreeNode builder to use it; all three builders do
  (`:792`, `:996`, `:1094`). `LocalDaemonEntry` carries
  `PendingAsk` and `threadFromEntry` sets `EvenerThread.AskPending` from it
  (`cmd/evener-hub/internal/appsource/local_daemon.go:38-47,799`).
  `SetPendingAskFunc` no longer exists at all; the live bit comes
  from a direct `sess.HasPendingAsk()` call (`cmd/evener/serve.go:967`). Step 6
  pins the agreement property so it cannot silently regress again.
- **There is no separate "needs you" section in the rail.** The auto-grouped
  NeedsYou tier was deliberately removed (`Rail.tsx:563-573`, kata `vbh8`
  §2.2) because it listed a session that was already under its own project.
  A row's own Cadence dot carries attention now. `tree.needs_you` is still on
  the wire and still feeds the rail-host badge — do not read its presence as
  evidence of a rendered section.
- **A quiet row has no indicator.** `rail-row-signal`
  renders only for `working`, `needs-you`, and `failed`; an idle session has
  none. Hover `rail-row-title` and read its context card when a scenario
  needs the quiet row's detailed status.
- **The rail and its HoverCard have different display depths.** The compact
  web rail maps `hubapi.StateWord` to `Working`, `Needs you`, or `Failed`; its HoverCard restores
  details such as `Question waiting` and `Restart required`.
- **Historical result: forcing `errored` live could not be verified in the
  original pass.** A bad model name was rejected at spawn time before a
  session existed, and recoverable provider failures return the live session
  to `idle`. The obsolete procedure has therefore been removed from the
  runnable steps. Do not substitute transcript-tail or API-log heuristics:
  the owning runtime state is the source of truth. `hubapi.TestStateWord`
  pins the vocabulary mapping; a future live `errored` scenario needs a
  deterministic owning-state transition and should be a separate card.
