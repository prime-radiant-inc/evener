# status-vocabulary-roundtrip: attainable status words and icons agree across sidebar and TUI

**What this covers**: Track A §1 (unified status vocabulary & icons). The live steps compare
the attainable your-move and question-waiting states across the web sidebar and TUI session
header. A deterministic gate pins the complete `hubapi.StateWord` vocabulary, including
`errored`, without pretending this setup can manufacture every owning runtime state.

## Pre-state

- Hub running with a fresh `$HOME` (isolated `~/.serf`), real credentials, no prior sessions.
- A TUI (`serf-tui`) pointed at the same hub, in a tmux session for scriptable interaction.
  Use a tall window (`-y 300`+) — the session header line scrolls out of a short pane's
  capture (see Sharp edges).
- `superpowers-chrome:browsing` (or equivalent CDP browser) available for the web assertions.

## Steps

1. Spawn a session and let it settle to a generic `awaiting` (your-move) state — a prompt with
   no `ask_user` call, e.g. "Say hello and stop." Wait for `state=="awaiting"` via
   `GET /api/sessions/local:<id>`.
2. In the browser, open the sidebar and inspect **every** row for this session (a live session
   in the needs-you band is rendered twice — once under the NeedsYou cluster, once under its
   project group — so a query that stops at the first match can hide a disagreement between
   the two copies):
   ```javascript
   (() => {
     const rows = [...document.querySelectorAll('[data-ref="local:<id>"]')];
     return rows.map(r => ({
       rowId: r.getAttribute('data-row-id'),
       title: r.querySelector('.status-icon').getAttribute('title'),
       hasSvg: !!r.querySelector('.status-icon svg'),
       dataAsk: r.getAttribute('data-ask'),
     }));
   })()
   ```
3. In the TUI dashboard, filter to the same session (`/` + a suffix of its ID + Enter) and
   press Enter again to open/attach it. Capture the pane and find the `SERF / SESSION` header
   line, which carries the state badge.
4. Force a genuine `ask_user` question, re-check both surfaces plus the sidebar's `data-ask`
   attribute on every row copy, and check whether the TUI dashboard's per-row list (filtered by
   project, *not* opened) shows any ask-specific marker on this session's row.
5. Run the deterministic vocabulary gate for states that this live setup cannot transition into
   on demand:
   ```bash
   go test ./hubapi -run '^TestStateWord$' -count=1
   ```

## Expected

- Step 2: every row sharing the session's `data-ref` reports **the same** `title`/`dataAsk` —
  for the your-move state, `title === "Your move"` and `dataAsk` is absent (`null`) on all
  copies; `hasSvg === true` on all copies.
- Step 3: the `SERF / SESSION` header's badge line reads `● YOUR MOVE`.
- Step 4 (ask-pending): every sidebar row copy reads tooltip `"Question waiting"` and
  `data-ask="true"`; TUI header badge reads `● QUESTION WAITING`.
- Step 5: the deterministic mapping includes `StateWord("errored", false) == "Error"`. This
  pins vocabulary only; it does not claim that this scenario produced a live `errored` state.
- Falsification: if the sidebar tooltip word and the TUI header-badge word ever disagree for
  the same underlying state (e.g. sidebar says "Working" while the TUI still says "ACTIVE"),
  or if two DOM rows for the *same* session ever disagree with each other, the shared
  `hubapi.StateWord` delegation is broken or one surface/row bypassed it.

## Cleanup

- Shut down the spawned session(s); kill the TUI tmux session; remove the isolated `$HOME`.

## Sharp edges

- **The TUI session header lives in the scrollable transcript body, not the fixed chrome**
  (`hub_session_view.go`'s `sessionHeaderLines()`, rendered inside `renderSessionMainBody`) —
  bubbletea's altscreen means `tmux capture-pane` only ever sees the *current* frame, not
  scrollback, so on a short window (e.g. the 50-row size used by other `tui-*` cards) a
  chatty first turn (a dozen-plus "Prompt loaded" notice lines) can push the header off the
  top of the visible frame with no way to scroll back into tmux history. Use a tall window
  (`-y 300`+) so the header is guaranteed to still be on-screen, rather than trying to scroll
  the TUI's own viewport.
- **The TUI's `StatusBadge` uppercases whatever word it's given** (`tuiprim.StatusBadge`); the
  web never uppercases — this is expected surface-specific styling, not a vocabulary mismatch.
- **The TUI dashboard's per-row list does NOT currently show a distinguishable ask-pending
  marker, and its per-row word is the raw wire state, not `hubapi.StateWord`.** The TUI
  dashboard never consumes `/api/tree` at all — it builds its tree from the appwire JSON-RPC
  `thread.list` call (`fetchHubTree` in `cmd/serf-tui/hub_commands.go:149` calls
  `client.ThreadList`, then `hubTreeFromThreads(resp.Data)` at line 153), and
  `hubNodeFromThread` (`cmd/serf-tui/hub_types.go:181`) populates `hubTreeNode.AskPending`
  straight from `thread.Serf.AskPending` — not from any `/api/tree` node. The direct-daemon
  appwire path for a single-thread read is fully wired: `cmd/serf/serve.go:357` registers
  `srv.SetPendingAskFunc(...)`, and `appThread()` (`server/appwire_runtime.go:440`) overlays
  that live bit into `SerfThread.AskPending` — which is exactly why the TUI's session-header
  badge (step 3) correctly shows "Question waiting"/"Your move". But this card's actual setup
  attaches the TUI *through the hub*, not directly to a daemon, and the hub's
  `LocalDaemonSource.ListThreads` (`cmd/serf-hub/internal/appsource/local_daemon.go:57`) does
  not proxy the daemon's `appThread()` output for the dashboard-list call — it reconstructs
  each thread from its roster entry via `threadFromEntry` (same file, line 531), which
  assembles `Serf: appwire.SerfThread{Ref, Capabilities}` (lines 549-550) with no `AskPending`
  set at all. `LocalDaemonEntry` (line 26) doesn't even carry a pending-ask bit yet, so a real
  fix requires plumbing a pending-ask signal from the roster through `LocalDaemonEntry` and
  `threadFromEntry` into `SerfThread.AskPending`, not editing `/api/tree` or
  `hubTreeResponse`. Confirmed live: a session with a real pending `ask_user` question showed
  `awaiting` with no `◆` on its dashboard row, while its project-rollup line correctly read
  `1 live · Your move`/`Question waiting` via `displayWord`. This is a real, pre-existing gap
  (Task 29's per-row marker is reachable only via the sidebar's/tree's needs-you copies, never
  via the TUI dashboard's hub-proxied live/project listing) — filed as a finding, not fixed by
  this card. The TUI's *session header badge* (step 3) is unaffected and correct even through
  the hub, because the single-thread read path differs from the list path: the hub's
  `LocalDaemonSource.ReadThread` (same file, line 68) proxies straight through to the daemon's
  own `client.ThreadRead` call instead of reconstructing the thread locally, so it still returns
  the daemon's fully-wired `appThread()` output with `AskPending` correctly set — only
  `ListThreads` takes the local-reconstruction shortcut that drops it.
- **The web sidebar renders a live needs-you-band session twice** — once under the NeedsYou
  cluster (`row_id` prefixed `needsyou:`), once under its project group (`row_id` prefixed
  `project:...`) — and only the NeedsYou copy carries `ask_pending`/the correct tooltip; the
  project-group copy inherits the same gap described above (its `TreeNode` never gets
  `AskPending` set) and renders `"Your move"` even while the same session is genuinely
  ask-pending. `document.querySelector` (first-match) reliably hits the NeedsYou copy because
  `sidebar.js`'s `flatten()` emits `needs_you` before `projects`, so a single-row assertion
  will not *notice* this — step 2's all-rows query is written specifically to catch it. Found
  live in this run; same root cause as the TUI gap above (`AskPending` only set on the
  `needs_you` `TreeNode`s in `cmd/serf-hub/internal/hubcore/tree.go`).
- **Historical result: forcing `errored` live could not be verified in the original pass.** A
  bad model name was rejected at spawn time before a session existed, and recoverable provider
  failures return the live session to `idle`. The obsolete procedure has therefore been removed
  from the runnable steps. Do not substitute transcript-tail or API-log heuristics: the owning
  runtime state is the source of truth. `hubapi.TestStateWord` pins the vocabulary mapping; a
  future live `errored` scenario needs a deterministic owning-state transition and should be a
  separate card.
