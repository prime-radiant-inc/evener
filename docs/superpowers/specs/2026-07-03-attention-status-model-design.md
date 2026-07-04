# Attention & Status Model — Design (v5)

Date: 2026-07-03 (v5 after four adversarial review rounds)
Status: Implemented; merged to main 2026-07-04 (`1a4633095..2ed534949` + symlink-test fix, main `a20e681f8`)
Workstream: 1 of 6 from the 2026-07-03 web-UI UX diagnostic

## Problem

When an agent ends its turn — question or not — the thread shows plain gray **Idle**: `agent.SessionState` collapses "waiting for your reply" and "genuinely idle" into one value (agent/session_state.go:13), so the wire `ThreadStatusAwaiting` is unreachable for serf sessions. The NeedsYou tier never lists them, notifications key on impossible transitions behind a 5s poll with channels off, and nothing pushes sidebar updates for unopened sessions.

## Decisions (Jesse, 2026-07-03)

1. Inbox semantics; 2. no read-tracking (punted); 3. never decay; 4. title+favicon default ON, OS+sound opt-in; 5. **daemon-truth**: the daemon reports `awaiting` on the existing Codex-shaped status field. Four review rounds confirmed the pivot's core (the `"awaiting"` pipeline and send-gating work today) while forcing precision on everything around it.

**Owned consequence (sign-off carried).** Every live, settled, unarchived session you haven't replied to is amber; the badge counts them all; completed turns re-arm and re-notify. Async wakes re-arm by design. Archive/shutdown are the triage verbs; mark-seen is the future pressure valve.

## Semantics

| Level | Color | Normalized status |
|---|---|---|
| `working` | blue | `active` — including a parent whose delegated children are still running (below) |
| `needs_you` | amber | `awaiting`, `warning` |
| `error` | red | `errored` |
| `idle` | gray | everything else, including all non-live sessions |

**needs_you:** live, top-level, unarchived, status `awaiting`/`warning`. **Clearing:** send or steer, archive, shutdown; queue is not a clearing action; opening does not clear; never decays; un-archive/re-probe re-arms — intended. **Ranking:** `errored` above `awaiting`, then oldest first. Subagents carry no independent attention (tree.go:614). Live-only rule carried: ended sessions are `idle`.

## The `errored` lane — full enumeration (rounds 3–4)

`NormalizeState` stops collapsing `systemError → "awaiting"` (tree.go:247) and emits `"errored"`. Because reviewers found the collapse's consumers one by one, v5 enumerates every site the lane touches — this is the honest cost of a distinct red level:

- **hubcore:** NormalizeState (+ golden flip, tree_test.go:897); NeedsYou filter admits `awaiting|warning|errored` **and gains the archive filter it lacks today** (tree.go:597-636 never consults decisions — pre-existing latent bug: an archived live-awaiting session shows in the tier; the filter aligns tier, badge summary, and the archive clearing verb); tier node `State` becomes the real state (tree.go:619); tier sort = rank desc, oldest first; `AttentionRank` **and** the separate `rollupRank` (tree.go:154) gain `errored` above `awaiting`; rollup count switch (tree.go:463) gains the case; `/api/tree` rollup stays consistent (web_api_tree.go:90).
- **sidebar template + CSS:** the tier row and dot hard-code `data-state="awaiting"` (sidebar.html:23,29) — both become `{{.State}}`; new `.sb-row[data-state="errored"]` tint/left-border (style.css:582 family); new `.project-rollup-dot[data-state="errored"]`; a **distinct dot shape** for errored (the colorblind double-channel at style.css:974 currently shapes only awaiting/warning/active/idle — errored must not collide with active's disc).
- **subagent rows:** `subagentDone` (web.go:62) treats anything non-{active,awaiting,warning} as done — without an `errored` case, an errored child renders as a ✓ under "Completed (N)". It gains the case; the glyph lane gets `✕`-style errored treatment (sidebar.html:245).
- **web labels:** `stateLabel` (web_format.go:178) gains `awaiting → "Needs you"` and `errored → "Error"` (today it would echo raw lowercase strings).
- **TUI (raw-status path):** `stateLabel`/`statusDot` already handle `awaiting` (hub_dashboard_view.go:507,492); add `systemError`/`errored` to label, dot, `attentionRankLabel` (:544, currently rank 0), `stateColor` (:256, currently TextDim), the row-tint gate (:308), and a new `StateError` theme token (tokens.go has none).
- **test churn named:** NormalizeState goldens, app_rpc systemError iteration (app_rpc_test.go:1108), sidebar template tests.

## Daemon: the `awaiting` state

`SessionState` gains `SessionAwaiting`. Round 4 corrected the mechanism (findings A2/A3/B4/B5):

- **The boundary function stays untouched — genuinely.** `finishProcessingAtBoundary` runs inside `processOneInput` (session_tool_round.go:400 et al.), *before* `armGoalContinuation` (session_lifecycle.go:582), and all its callers — success, provider-error, turn-failure, interrupt — pass the identical `SessionIdle`. It cannot compute awaiting. It keeps writing `idle`.
- **Arming is a new upgrade step at the drain-loop settle** (session_lifecycle.go:624-642): after `armGoalContinuation`, before the `EventSessionEnd` emit, upgrade `idle → awaiting` iff:
  1. the drained entry's turn **completed normally** (the drain loop knows the outcome; interrupted and failed turns never upgrade — this, not the boundary arg, is what keeps interrupt → idle),
  2. it produced user-visible agent output,
  3. no goal continuation was just scheduled (now correctly ordered — the decision exists by settle time),
  4. the notification buffer and input queue are empty.
  The upgraded state rides `EventSessionEnd{State}` (emitted at :634) to the bridge, and the serve-loop mirror (serve.go:421) reads the same value after `ProcessInputKind` returns. Interrupted ends stay accurate: the interrupt end event hard-codes idle (session_lifecycle.go:490) and the bridge drops interrupted ends (bridge.go:37) — the serve mirror carries interrupt→idle, and no upgrade occurred, so nothing leaks.
- **Suppressor reads are sequential independent snapshots** — each signal (goal store, notification buffer, queue) is read under its own lock, one at a time, no nesting (round-4 A11 flagged lock-order risk; the settle site + snapshot discipline avoids it). Benign races self-heal: a child finishing exactly at settle wakes the session anyway, and the next settle re-evaluates.
- **Delegating parents are `working`, not idle and not amber (round-4 A6).** Background delegation defaults on (job_delegate.go:374); the parent's turn ends while children run in `jm.running`. In-agent `SessionState` stays `idle` between turns, but the **daemon's wire projection** (`appStatus`/status handler — the daemon owns `jm.running`) reports `active` while live children or undelivered job notifications exist. The parent reads blue for the child's runtime, flips to amber only when the wake-turn completes with nothing else in flight. A hung child shows blue indefinitely — same visibility as any hung active session today; extending the wedge heuristic to child-stalls is noted future work.
  **Precedence (2026-07-04 external flag, folded post-merge): a pending ask outranks this projection.** The ask-user-question design ends the asking turn with the session resting `awaiting`, and its entry gate refuses autonomous wakes until the user replies. A parent that asked while children run therefore holds `awaiting` through child completions and their queued notifications; projecting `active` there would mask the question as "working" forever, and the amber flip could never fire — the wake-turn it waits on is itself gated behind the answer. The projection upgrades **idle only**, never `awaiting`. `WireState`'s idle-only guard is the normative encoding; `TestWireState_AwaitingOutranksAutonomy` pins it.
- **Resume (explicit, round-3 A2 / round-4 A12):** queues and notification buffers are not persisted, restored goals are deliberately not re-kicked (session_init.go:405), and children are never restored live (session_init.go:400 recovers them terminal). Resume evaluates the transcript tail only: agent moved last → `awaiting`. A restored mid-goal session is amber **on purpose** — amber is what surfaces the stall.
- **Interrupt** → idle; **compaction** never touches state; **dormant** → idle. Old daemons keep reporting `idle` and stay quiet.
- **Gating (round-3 B11):** not-busy gates key on `!processing`, so awaiting sessions already send/steer everywhere; a verification checklist pins this with tests (appwire Send, web composer, TUI modes, spawn/fork/resume).

## Hub

- **Wedge unification:** the stale-stream heuristic (app_threadlist.go:254) moves to shared hubcore, applied by tree build and list/read enrichment; the watcher probes only sessions sampled `active` beyond a **new** hub constant (3 min, mirroring the client's `LIVENESS_STALL_MS` — round-4 B6 caught v4 attributing this to a heuristic constant that doesn't exist). Small set, bounded tail reads.
- **Watcher:** per-source async collectors with per-source timeouts; debounced fsnotify; empty-success hysteresis; differ over `id → (state, archived, topLevel)` fed by a **meta-projection cache** (`id → {isSubagent, project, lastActivity}`) refreshed on past-index rebuilds, rendezvous events, and archive-handler pushes.
- **Broadcast:** `serf/attention/changed` via `BroadcastAll` with `changed[]` + `summary`; **summary is authoritative for badge counts**, computed from the tier-eligible set — which, with the tier archive filter above, is now genuinely the same definition. Restart re-seeds silently.

## Web client

- Sidebar/NeedsYou/rollups: the errored-lane edits; `serf/attention/changed` joins the refresh allowlist.
- **notifications.js:** delete the 5s poll. Baseline = one `/api/tree` fetch on connect; thereafter counts come from `summary` in each broadcast (no per-change refetch). **Per-tab latency, stated honestly (round-4 A5/B12):** `thread/status/changed` arrives id-less (appwire.js:827) on a single-thread subscription, so only the tab with the thread open can attribute it — that tab adjusts its own badge instantly for its own thread (it knows which thread it's subscribed to); other tabs, a TUI-only open, and a non-owner leader tab reconcile at the ≤5s broadcast. The instant-clear invariant is: *the tab you replied in clears instantly; everything else within a tick.* No baseline → no edge-firing. OS + sound on transitions into `needs_you`/`error`; focused-tab suppression; multi-tab **leader election** (Web Locks, localStorage-CAS fallback) for OS/sound only — badges stay per-tab.
- **Prefs migration:** versioned one-time backfill (existing blob → absent keys as explicit `false`; absent blob → new defaults ON). Settings copy + pill labels use the unified vocabulary.

## TUI

Errored-lane items above; `◆` needs-you count in the dashboard. No wire changes.

## Error handling

Old daemons degrade to today's behavior. Watcher: per-source isolation, panic recovery, baseline kept on failed cycles. Suppressor snapshot discipline above; resume recompute is defensive after crash-mid-turn.

## Testing

- Drain-settle upgrade table: {completed, interrupted, failed, provider-error} × {output, no-output} × {goal scheduled/not, notif buffered/not, queue pending/not, children live/not} → expected state, asserted at `/status` and appwire.
- Wire-projection test: parent `active` while children run / notifications undelivered; flips per rule when drained.
- Resume matrix: mid-goal (→ awaiting), agent-last (→ awaiting), user-last (→ idle), dormant (→ idle).
- Gating checklist tests (send/steer/queue-fallback/spawn/fork on awaiting, all three surfaces).
- Errored lane: every enumerated site, plus the cross-surface agreement test (sidebar tree vs list/read vs TUI) including an **archived-live-awaiting** construction (round-4 A4's blind spot) and an errored-subagent row (not ✓).
- Watcher: one broadcast per change; re-seed silent; per-source isolation; hysteresis; debounce; wedge probes only past threshold; summary == tier-eligible count including the archive axis.
- jstest: baseline-before-edge, summary-driven counts, own-thread instant adjust, prefs migration matrix, leader election, focused suppression.
- e2e card (single-tab happy path, plus named variants): base = spawn → turn completes → row/tier amber + title count within a tick → reply in open tab → that tab's row and badge clear instantly, broadcast reconciles. Variants exercised in integration rather than e2e where flakiness would dominate: goal-loop session (no amber between iterations; amber on completion), background-delegate parent (blue during child run), second-tab reconciliation (≤5s).

## Out of scope

Read-tracking (punted; protocol-clean path recorded: hub-inferred cursor + hub-terminated `serf/thread/seen/set`), ask detection (job-control's `ask` lands as another `awaiting` producer; its background jobs fit the working-projection rule **subject to the ask-precedence rule above**), subagent attention propagation, child-stall wedge extension, per-message read receipts, background push, workstreams 2–6 (recorded: keep ⌘↵ + setting; project-level delete only).

## Estimate

~1,200–1,600 loc including tests: daemon (settle upgrade + wire projection + resume + gating tests) ~350–500; hubcore errored lane + archive filter + watcher/differ/cache ~400–550; web (templates/CSS/labels/notifications.js) ~300–400; TUI ~80–120.

## Review log

**Round 1** (v1→v2): derivation input didn't exist; single shared derivation; watcher beyond roster; snapshot RPC; queue dropped from clearing; warning mapped; ⟳/subagent/interrupt corrections; per-key defaults; consequence owned; estimate raised.

**Round 2** (v2→v3, 6–6): read path couldn't carry the sidecar bool; relay ingestion unimplementable; string-keyed sites wouldn't consume a parallel field (decisive pro-pivot); automation false-amber; settings claim false; watcher cost; snapshot race; multi-tab dupes. v3 pivoted to daemon-truth, deleted the overlay.

**Round 3** (v3→v4, 6–6): NormalizeState collapse made the red level unimplementable (→ un-collapse); arming mislocated vs the boundary/bridge; async wakes and running children defeat flash-suppression; goals aren't re-kicked on resume; wedge vs map-only differ; differ needs metas; open-thread badge lag; audit largely vacuous (good news).

**Round 4** (v4→v5, 7–7): tier amber-locked in template+node so errors-first was dead (A1/B1 → un-hardcode both; full render enumeration incl. `rollupRank` as a second rank function, row/rollup/shape CSS, `subagentDone`, labels, TUI color token — B2/B3/A7/A8/A9/B8-B10); boundary runs before `armGoalContinuation` and callers all pass `SessionIdle` (A2/A3/B4/B5 → drain-settle upgrade gated on turn outcome; boundary function truly untouched; serve-mirror leak closed by outcome gate); NeedsYou lacks an archive filter so summary≠tier and archive didn't clear the tier (A4/B7 → tier archive filter); id-less per-connection relay event limits instant-clear to the owning tab (A5/B12 → honest per-tab invariant); default-on background delegation made the suppressor produce false-idle (A6 → wire projection reports working while children live); wedge "~3min heuristic constant" was fabricated — it's the client liveness constant, adopted as a new hub constant (B6); lock-order risk (A11 → sequential snapshot reads); vacuous resume guard + golden churn (A12); estimate raised again.

**Post-merge external flag (2026-07-04, colleague via Jesse):** ask × autonomy collision — under the ask-user design, a parent that asks while children run rests `awaiting` with autonomous wakes gated on the reply; if the delegating-parent projection outranked `awaiting`, the question would read blue forever and the amber flip could never fire (the wake-turn it waits on is refused until the answer arrives). Folded as the precedence rule in the wire-projection bullet. The shipped `WireState` guard (idle-only upgrade) already encoded the correct precedence, but its comment claimed awaiting and autonomy can never coexist — an invitation to widen the guard and create exactly this deadlock. Comment corrected; behavior pinned by `TestWireState_AwaitingOutranksAutonomy` (mutation-checked: fails under the widened guard). The ask spec independently pins the other half (`/status` stays `awaiting` after a job completes while awaiting).
