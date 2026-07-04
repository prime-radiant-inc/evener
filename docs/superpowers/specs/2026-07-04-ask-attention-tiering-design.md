# Ask-aware attention tiering and loud-channel scoping — design

Status: approved direction, implementation deferred (see §0). All code anchors verified at main `c6ba3080a`, 2026-07-04.

## 0. Decisions (Jesse, 2026-07-04)

1. **Timing: everything after the WS3 sidebar rebuild lands.** This spec is written now; the tiering feature implements against the rebuilt sidebar. Exception: §2 is a standalone bug fix in shipped code and does not wait.
2. **Loud scope is a user setting**, `"questions & errors"` (default) vs `"everything needing me"` — not a hard rule. Preserves the attention model's every-settle-notifies behavior as an opt-in.
3. Out of scope, standing decisions honored: no read/seen tracking (punted at the attention-model spec gate); composer action-state at rest belongs to WS2 (`sessionTurnActionState`, cmd/serf-tui/composer_panel.go:99 — noted in the WS2 workstream memory); no new notification transports.

## 1. Problem

The attention-status model made every settled, output-producing session rest `awaiting` and land in NeedsYou. That is the intended inbox foundation — but it is coarse. Two different situations render identically amber:

- **Blocked on you**: the agent called `ask_user`; nothing can proceed until you answer. The agent explicitly requested your attention.
- **Your move, whenever**: a turn completed with output; the ball is in your court but nothing is stuck.

The daemon knows the difference (`askPending`, agent-side) but the signal never reaches the wire: `/status` serializes no ask field (server/server.go:80-93 `StatusInfo`), and the hub prober decodes only `session_id` + `state` (cmd/serf-hub/internal/hubcore/prober.go:19-22). Consequences:

- NeedsYou orders errored-first, then oldest-updated (tree.go:659-665). A session waiting on your answer sits interleaved with routine settles.
- OS/sound channels, when enabled, fire on *any* transition into needs_you or error (notifications.js:238-254). Every settle can go loud, so users disable the channels — and then miss actual questions. The original "I missed the notification" complaint reincarnates one layer up.

## 2. Prerequisite bug fix: restore must rebuild the pending-ask set (implement now)

**The bug (main `c6ba3080a`).** `s.askPending` is written only in the live tool `Exec` path (agent/session_tools_ask.go:128) and cleared at turn entry (session_lifecycle.go:783). The restore path derives the *state* — `session_init.go:513-516` calls `deriveRestoredState` and rests the session `awaiting` — but never rebuilds the *set*. A restored session with an unanswered question therefore has `askPendingCount()==0` and `HasPendingAsk()==false`, and every hold keyed on the pending set is inert after a daemon restart:

- Entry gate (session_lifecycle.go:453): autonomous wakes — job notifications, watch deliveries, continuations — are **accepted** and drive turns straight past the unanswered question.
- Goal holds (session_goal.go:61, :246): `/goal` **kicks** instead of arming.
- Compact guard (session_compaction.go:30): `Compact` **proceeds** and can summarize away the transcript tail the pending question lives in.
- Serve shadow hold (cmd/serf/serve.go:426): releases, so a refused-wake `/status` flicker guard no longer applies (moot once the entry gate accepts, but it decays together).

This violates the ask_user contract (spec 2026-07-03, §5.3 holds and compact guard), which states the holds apply "while a question is pending" with no restart carve-out. It also matters here: §3's wire bit must be truthful across restarts or the sidebar drops the ask marker on every daemon restart while the question is still unanswered.

**Fix shape.** In the restore path, when `deriveRestoredState` returns `awaiting` *because of an ask round* — the tail-scan already identifies the decisive `TurnToolResults` carrying a completed non-error `ask_user` result — rebuild `askPending` by parsing the `ask_user` tool-call arguments from that round's assistant turn. Factor the argument parse out of the live `Exec` path (session_tools_ask.go) so live and restore share one parser; the restore leg walks the round's assistant turn(s), collects every `ask_user` call whose result in the decisive turn is non-error, and appends parsed questions in call order (matching the round-global numbering the composers use).

Edge cases, decided:

- **Generic awaiting** (decisive turn is a communicate or a plain assistant final): no rebuild; the set stays empty. Correct — generic awaiting must not hold.
- **Unparseable arguments** (schema drift, hand-edited transcript): rebuild what parses; if nothing parses, leave the set empty, log a warning, and never fail the restore. The session still rests awaiting; the holds are inert — exactly today's behavior as the floor, never worse.
- **Compacted history**: if the ask round survived the tail (it is the decisive turn, so it did), it is parseable; if compaction summarized it away, there is no pending question by the §6 definition, and the derived state will not be ask-awaiting in the first place.
- **Multiple `ask_user` calls in the round**: union, in call order.

**Tests (red-first, driving the real restore entry point):** restart with a pending ask → (a) entry gate holds an `EntryNotification` wake, (b) `SetGoal` arms without kicking, (c) `Compact` returns the pending-question error, (d) `HasPendingAsk()` true; plus the existing green case — restart after a *generic* settle keeps the set empty and all holds released. Update the `ask-restart-rederive` scenario card with a holds-survive-restart step (§9 batch).

## 3. Wire: an additive `pending_ask` bit, daemon-truth end to end

Following the attention model's daemon-truth rule (the daemon reports; layers relay), the discriminator rides the existing status pipeline additively. Codex-sourced threads never set it; every decoder treats absence as false.

- **Daemon**: `StatusInfo` (server/server.go:80-93) gains a `PendingAsk bool` field with JSON tag `pending_ask,omitempty`, populated from the same session snapshot that carries `state` (the value `HasPendingAsk()` already exposes to cmd/serf/serve.go:426). §2 makes it truthful across restarts.
- **Prober**: `statusInfo` (prober.go:19-22) decodes the new field; `LiveEntry` (roster.go:22-26) gains `PendingAsk bool`.
- **Tree**: `hubapi.TreeNode` (hubapi/types.go:67-81) gains `ask_pending,omitempty`; `hubcore.AttentionEntry` (attention.go:17-23) gains `askPending` (camelCase, matching the `AttentionSummary` parallel-type precedent), so `serf/attention/changed` rows carry it to the client without a tree refetch.

## 4. Ranking: three bands inside NeedsYou, existing ranks untouched

Within the NeedsYou tier: **errored → ask-pending → everything else**, oldest-first inside each band (broken beats blocked; blocked beats your-move-whenever). Implementation replaces the errored-first boolean sort (tree.go:659-665) with a band function; `AttentionRank` (tree.go:132-147), `rollupRank` (tree.go:158-173), and `attentionLevel` (attention.go:51-62) are **unchanged** — ask-pending is a band within needs_you, not a new level or rank value, so rollups, tier admission (tree.go:606-651), and WS3's level-keyed machinery are undisturbed. The TUI dashboard ordering (`dashboardRowLess`, hub_dashboard.go:221-234) gains the same band between its rank and recency terms.

## 5. Loud-channel scoping (client; lands with/after WS3)

New versioned preference `loudScope`: `"asks"` (default — questions & errors) | `"all"` (everything needing me).

- `DEFAULT_PREFS` (notifications.js:27) gains `loudScope: "asks"`; `migratePrefs` (notifications.js:63-77) bumps the version and backfills `"asks"` for existing users. Note the asymmetry with channel keys: channels backfill to `false` (off), `loudScope` backfills to its default value.
- Settings pane: a two-option control using the existing `data-notif` commit pattern (settings.js:49-93); no OS-permission coupling (that stays on the `os` channel toggle).
- Gating (notifications.js:238-254): the transition test `into && !was` is unchanged; the loud branch becomes
  `if (prefs.loudScope === "all" || ch.askPending || ch.level === "error") { if (prefs.os) fireOsNotification(ch); if (prefs.sound) playTone(); }`
  Title count, favicon dot, and tab badge behavior are unchanged — they continue to reflect **all** of needs_you. The `hadBaseline`, focus, and Web-Lock leader gates are untouched.
- Re-ask behavior falls out of the transition test: answering flips the row out of needs_you (turn runs), and the next ask or errored settle is a fresh `into && !was` transition. No timers, no re-notify while a question sits unanswered.

## 6. Row markers

- **Web**: NeedsYou rows (templates/partials/sidebar.html:22-29) render an ask marker — `data-ask="true"` on the row plus a distinct affordance on the status dot (a "?" badge form of the amber dot). Final visual treatment belongs to the rebuilt sidebar; this spec hands WS3 two requirements: NeedsYou renders §4's band order, and ask-pending rows carry a visible marker with `data-ask` plumbed.
- **TUI**: `hubRow` (hub_model.go:42-58) gains `askPending bool`; dashboard rows reuse the `◆` glyph the session view already uses for its question chip — one vocabulary for "question waiting". The `◆N` header badge (hub_dashboard_view.go:150-173) keeps counting all needs_you; band ordering surfaces the asks at the top.

## 7. Testing

- §2: the four red-first restore-hold tests plus the generic-settle negative (agent package, real restore path).
- §3: prober decode test; a serve-level `/status` test asserting `pending_ask` true at ask-rest, false after reply, **true again immediately after restart** (the §2 seam observed at the wire).
- §4: band-sort unit tests (tree + TUI `dashboardRowLess`); rollup/AttentionRank pinned unchanged (existing tests already do).
- §5: jstest — migration to the new version backfills `"asks"`; gating fires OS/sound for ask and errored transitions and suppresses generic ones under `"asks"`; fires all under `"all"`; badge/title unchanged in both.
- e2e: extend `ask-cross-session-notify` with the loudScope default assertion once implemented.

## 8. Sequencing and coordination

1. **§2 now** — standalone agent+serve fix, no sidebar contact, restores the shipped contract.
2. **§3+§4 with the tiering feature, after WS3 lands** (Jesse's call). They are server-side and would survive the rebuild, but land as one reviewed unit with §5–6 unless resequenced.
3. **§5+§6 strictly after WS3** — they touch surfaces the rebuild replaces. WS3's plan gains the two §6 requirements as a handoff note.
4. WS2 owns the composer action-state question (standing pointer).

## 9. Companion batch (severable, docs-only): scenario-card hermetics

The Task-17 live run passed all falsification lines but recorded stale pre-merge mechanics in the ask cards (evidence: the e2e results ledger). Fix mechanics only — every falsification line stays byte-identical:

1. Cards written pre-merge poll for `idle` after a reply; post-merge the session rests `awaiting`. Replace idle-polls with turn-advance + `awaiting`-at-rest polls (the pattern the live run substituted).
2. `ask-noninteractive-invisible`: locating a hub-spawned transcript by `working_dir` grep fails on macOS (`/var` vs `/private/var`); locate by session id.
3. `ask-subagent-invisible`: raw grep for the marker strings is polluted by the task prompt echoing them; assert the delegate's actual `communicate` call argument instead. Also note the parent legitimately rests `active` while the delegate's child session lingers as live autonomy.
4. `ask-cross-session-notify`: the `/_partials/sidebar` probe needs the `HX-Request` header, and the expected NeedsYou count is `(2)` not `(1)` under inbox semantics.
5. `ask-tui-answer`: note the tmux em-dash round-trip hazard when asserting the chip text.
