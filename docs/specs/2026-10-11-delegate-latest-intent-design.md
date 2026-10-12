# Subagent latest-intent rows (design)

**Status:** Approved by Jesse 2026-10-11, with the intent publication floor
amended to five seconds (revision 4.1).

**Status note:** This revision folds in three adversarial review rounds.
Round 1 (revision 1 → 2): Reviewer A (6 serious) and Reviewer B (4 serious)
aggregated 8 findings; A took the round's 5 points. Round 2 (revision 2 → 3):
Reviewer D (5 serious) and Reviewer C (2 serious) aggregated 6 findings; D took
the round's 5 points. Round 3 (revision 3 → 4): Reviewer E (3 serious) and
Reviewer F (0 serious, 7 minor) aggregated 3 findings; E took the round's 5
points, and F verified all ~45 of the spec's citations accurate end to end.
Every reported finding across the three rounds was verified against the code
and judged legitimate; no reviewer was disqualified. All are resolved below.

**Goal:** While a subagent runs, the compact agent rows show the newest
tool-call `intent` the child's own current turn stated ("Searching handlers for
request routing…"), in place of the bare live status word. The changed
surfaces, exhaustively: the web Activity sidebar's Agents tab; the responsive
ActivitySidebar the phone viewport mounts (the same component,
cmd/evener-hub/frontend/src/shell/activitybar); the session-pane activity tree
and the recursive ActivityPanel (ActivityTree.tsx, ActivityPanel.tsx), whose
delegate nodes are projected from the same `SessionDelegate` rows
(appwire-client/typescript/sessionActivityPresentation.ts:63-64); and
mobile-native's Subagents screen activity rows
(mobile-native/src/subagents/SubagentRowView.tsx, through the subagentModel
adapter whose tree consumes the same shared store). When no intent applies,
every row reads exactly as it does today.

**Jesse's rulings (2026-10-11), binding on this spec:**

1. Own-session only: a row shows its own session's tool intents, never a
   descendant's.
2. Batch winner doesn't matter; take the meter's precedent (newest started
   call wins, a call stating none leaves the previous line standing).
3. Journaled: the intent is durable delegate-journal state, so a retained row
   shows the last intent of a run a restart interrupted.
4. Frame merges ride the existing `reportPreview` capability gate; no new
   capability field.

**Related:** [session activity product guide](../product/session-activity.md),
[reportPreview design](2026-10-09-session-activity-push-updates-design.md)
(whose machinery this feature rides: the capability-gated frame merge, the
additive-wire discipline, the push/read parity rule),
[root-meter precedent](../../server/activity_meter.go) (`noteIntent`:
the root session's `LatestIntent`, shown by the phone Board in place of the
running job's command), [read projection](../../agent/session_activity_delegates.go).

## Problem and evidence

The sidebar's agent row renders one meta line:
`AgentRowBody` (cmd/evener-hub/frontend/src/shell/activitybar/activityRows.tsx:74)
shows `agentStateText(sub)` — for a live stable delegate that is the status word
only (`SessionDelegate.Status` is the lifecycle, `running`/`idle`,
agent/session_activity_delegates.go:221; the native word is "Running",
mobile-native/src/subagents/subagentModel.ts:106; "working" is the
session-aggregate value, agent/jobs_activity.go:1538). Shell-job rows already
carry what a job is for (`EvenerJobInfo.Intent`, captured at launch,
appwire/types.go:1629), but delegate rows carry no current-work text at all.

The data and the pipe both exist:

- Each tool call's `intent` is promoted onto the tool-call event's description
  (`ToolCallStartData.Description`; the root meter reads exactly this,
  server/activity_meter.go:136-157, bounding with `appwire.Excerpt` at
  `appwire.MaxIntentRunes` = 200 and clearing at `EventExecutionStarted`).
- A running child already reports work to its owner's delegate controller:
  `noteParentJobActivity` (agent/subagents.go:1363) through
  `cfg.spawn.parentJobActivity` (bound at agent/delegate_runtime.go:639-652 —
  this binding is the transport; the capture site below is per-call), into
  `ReportActivityPhase` (agent/delegate_runtime.go:129), which coalesces
  plain-activity publications to one per second per delegate
  (`delegateActivityPublishInterval`, agent/delegate_runtime.go:119) and
  publishes `events.DelegateUpdatedData` (agent/events/payloads.go:731) as the
  `evener/delegate/updated` frame.
- The shared store already merges those frames into loaded rows behind the
  `reportPreview` capability gate
  (appwire-client/typescript/sessionActivityStore.ts:732,
  `delegateRowFromFrame` at :1254: "a winning frame supplies every mutating
  field, absent optionals included, so it clears what it omits"; the join takes
  a frame's snapshot fields only when its `projectionRevision` is strictly
  greater than the applied one, :942-955).
- The read already merges live state over the folded aggregate
  (agent/session_activity_delegates.go:196).

## Constraints

- Additive wire change only: new fields are `omitempty`; no existing field
  changes meaning; no new read method, subscription, or capability.
- The activity reads stay the authority for membership and counts; a frame
  patches a loaded row in place, never invents one, and the merge stays behind
  the existing `reportPreview` + live gate (ruling 4). With the gate closed,
  intent still arrives through reads.
- **One bound rule everywhere: `appwire.Excerpt` at `appwire.MaxIntentRunes`
  (200)** — the helper that flattens every whitespace/control run (line breaks
  included) to one space and word-break cuts; the root meter's exact rule
  (server/activity_meter.go:150). The projector applies the same helper: the
  leaf-package detour from the reportPreview design does not apply here because
  `internal/appprojector` already imports package `appwire`, where `Excerpt`
  lives. `agent/activitybound` is not involved. Push and read must produce
  identical strings for the same intent (parity test with newline- and
  multi-space-laden values).
- **The intent channel is display-only state and must not move any supervision
  clock.** Intent reports never advance `live.activityAt` (the quiet
  watchdog's baseline), `live.productiveActivityAt` (the drain-grace clock),
  or rearm the quiet cadence — all of which every accepted
  `ReportActivityPhase` call does today (agent/delegate_runtime.go:142-157;
  the quiet window is ten minutes, agent/job_watch.go:118). The existing
  batch-level `jobPhaseToolRunning` activity report
  (agent/session_lifecycle.go:2484, once per batch) is unchanged, so the
  quiet and drain semantics keep today's cadence exactly.
- Per-generation discipline: intent belongs to the open run generation and
  clears when that generation settles. A resumed run never inherits the
  previous run's line; a row remains one whole snapshot per the guide's
  run-generation ordering.
- No new collection demand; no navigation change (the nav summary still
  carries no subagent detail); no change to attention, quiet-watchdog, stop,
  or delivery semantics.
- Scope is the stable delegate rows (`SessionDelegate` reads,
  `evener/delegate/updated` frames, the store's loaded rows). Turn-container
  activity-tree entries keep their existing status text.
- Web, native, and the shared store migrate together.

## Wire contract

1. `events.DelegateUpdatedData` (agent/events/payloads.go:731) gains
   `LatestIntent string json:"latest_intent,omitempty"`: the open generation's
   newest stated intent. Empty whenever no run is open — including after the
   run settles — so the frame field and the read field agree for every row
   state.
2. `appwire.EvenerDelegateInfo` gains
   `LatestIntent string json:"latestIntent,omitempty"`, filled by
   `internal/appprojector.appwireDelegateInfo` from the frame payload and
   bounded with `appwire.Excerpt` at `appwire.MaxIntentRunes` — the read's
   exact rule, per the parity constraint. Roster rows keep it: it is status
   text like `status`/`phase`, not a report payload, so `SlimDelegateForRoster`
   (appwire/delegate_roster.go:16) keeps clearing only report payloads and the
   logical-owner id. A roster test must pin that. The hub's thread-read
   diagnostics rows (`appwireDelegateFromAgentStatus` +
   `SlimDelegateForRoster`, cmd/evener-hub/app_threadread.go:1061) are NOT
   required to fill it — only the frame path fills the field; those rows
   simply omit it.
3. `appwire.SessionDelegate` (appwire/session_activity.go:113) gains
   `LatestIntent string json:"latestIntent,omitempty"`: the display value per
   the join rule below — the controller's live value when non-empty, else the
   folded aggregate's value.
4. Regenerate `appwire-client/typescript/types.gen.ts` and
   `docs/appwire-protocol.md` with `make generate`.

The 200-rune cap keeps the default 50-row page well inside the 256 KiB
response budget (a worst-case 200-rune intent is ~1.2 KiB on the wire after
JSON escaping); a maximum 200-row page of intent-heavy rows approaches the
budget, and the read's existing byte admission pages it — the same mechanism
as any other wide page.

## Producer (agent, delegate controller, journal)

- **A separate intent channel.** The controller gains an intent-report entry
  distinct from `ReportActivityPhase` (e.g. `ReportIntent(lease, intent)`).
  An unchanged folded intent is a no-op — no append, no publication, mirroring
  `ReportActivityPhase`'s own short-circuit (agent/delegate_runtime.go:145-148).
  A changed one does three things and only those, in order, stopping at the
  first failure: (a) appends the journal event inside the lease-admitted
  section (`admitLeaseLocked`, agent/delegate_runtime.go:137) — if the append
  fails, nothing else happens and the error returns to the caller, which
  discards it as today's binding does; (b) sets the live intent on
  `delegateLiveState`; (c) publishes a `delegate/updated` snapshot. It
  advances no activity clock and rearms nothing (see the constraint above).
  It admits only a running lease, like `ReportActivityPhase`; through the
  documented settling/drain window a report — statement or clear — is
  stale-lease-rejected and the row keeps the pre-report intent, which the
  run's settlement then retires.
- **Capture.** The child reports each started tool call's stated intent through
  the existing parent binding at the tool-call start path, where the intent is
  already promoted to the event description
  (agent/session_tools.go:859-871) — so the reported line and the transcript's
  own intent line are the same string, `Excerpt`-bounded at the report site.
  The batch site (agent/session_lifecycle.go:2484) is where today's activity
  report fires and is NOT the capture site. A call stating no intent reports
  nothing and the previous line stands. Descendant sessions never report onto
  this row: the fence is the per-child closure capture at the binding
  (agent/delegate_runtime.go:648-652 binds each child's callback to its own
  lease; `noteParentJobActivity` itself only checks that a parent id is set,
  agent/subagents.go:1367-1369) — and that fence requires a behavioral test.
- **Clear.** The child clears its reported line when its execution starts —
  the `EventExecutionStarted` boundary the root meter clears on, emitted at
  agent/session_execution.go:68, where no activity hook fires today — through
  the same intent channel. The controller clears at run start **and run
  finish**.
- **Journal.** New delegatestore event `delegate_intent_stated`
  `{Generation uint64, Intent string}` (agent/internal/delegatestore/event.go),
  appended whenever the open generation's folded intent changes — at most once
  per stated tool call, plus once per turn that retires a standing line. The
  fold mirrors `applyUpdatePosted`'s open-run guard (requireExactOpenRun +
  PhaseRunning, agent/internal/delegatestore/fold.go:463-486) with one
  deliberate divergence: an empty `Intent` folds as a clear, where
  `applyUpdatePosted` rejects empty payloads. `applyRunFinished` also clears
  the aggregate's intent, so a settled run leaves the folded value empty —
  old journals are unaffected because the field did not exist before them.
  **The intent field joins `publicProjection`'s whitelist**
  (agent/internal/delegatestore/fold.go:813-833): an intent change must
  advance `ProjectionRevision`, which is what lets the forced frame win the
  store's strictly-greater-revision join — without it, the pushed frame
  carries the already-applied revision and silently no-ops while reads alone
  serve the value. The append is a durable sync write under the controller's
  mutex (the AppendBatch-per-event discipline, agent/internal/delegatestore/store.go:273)
  — the same per-event cost class `UpdatePosted` pays, at a higher rate;
  accepted per ruling 3, whose point is durability. The journal's header
  version **stays 1**: `UpdatePosted` rode the same additive path without a
  bump (agent/internal/delegatestore/event.go:5). The compact delegates read
  never traverses child transcripts (the guide's boundary), so the delegate
  journal is the only durable source that serves it.
- **Publication cost.** Bounded by the child's tool-call rate, not its token
  rate — and it keeps a five-second floor (Jesse's amendment on approval)
  with a never-drop guarantee. An
  intent change publishes immediately only when at least five seconds have
  passed since the intent channel last published a frame: a quiescent gap,
  which is exactly the multi-minute-tool-call case immediacy exists for, so
  its intent lands the moment the call starts — even beside the batch-start
  activity report that published the same instant, because during a single
  long call nothing else publishes and a floor the activity channel could arm
  would shelve the line until the next event. An intent change inside the
  window is journaled and
  held pending, and rides the next publication of any kind (the next round's
  activity report, or the next intent change after the window) — it is never
  dropped, unlike plain motion. A parallel burst therefore costs at most one
  intent-caused publication per five seconds instead of one per call. Each publication re-samples
  the owner's diagnostics as every `delegate/updated` does today, and emits
  the per-thread `evener/thread/activity/changed` invalidation every
  publication already emits; those invalidations ride the store's existing
  coalescing/skip rules — with the capability gate open the frame merge
  applies without a read, and with the gate closed the invalidation read path
  applies, bounded as today. One more cost of the revision coupling below: an
  intent event advances `ProjectionRevision` per stated tool call, so the
  store's defensive re-read path for an id that names more than one loaded row
  (bounded to one read per strictly newer revision by the guide) tracks the
  child's tool-call rate there instead of today's phase/attention rate —
  bounded by the guide's existing rule, and only in the ambiguous-id case.
- **Read join — and the frame uses the same join.**
  `captureSessionActivityDelegate` projects the folded value,
  `Excerpt`-bounded at capture like every other journal-sourced text field
  there (agent/session_activity_delegates.go:221-224), then the capture site
  applies: **live value when non-empty, else the folded value.** The frame
  builder applies the same join, so a frame published during the restored
  window (live empty, folded standing) carries the standing intent — the
  frame field and the read field agree for every row state. This is
  deliberately NOT `LatestActivityAt`'s conditional strictly-newer recency
  join (agent/session_activity_delegates.go:196) — a string has no order, and
  the folded value is the durable fallback that surfaces on retained cold
  reads.
- **Restore and restart — ruling 3's exact window.** The journaled intent
  shows on retained reads: the cold `retainedActivityRead` path
  (agent/session_activity.go:115) folds the journal with no live controller,
  while the interrupted generation is still open in the journal. When the
  owner session's runtime restores, bootstrap reconcile settles a
  runtime-lost generation as failed `"runtime_lost"`
  (agent/delegate_tree_restore.go:787-845, from agent/delegate_runtime.go:3144),
  and the run-finish clear retires the intent with the outcome — correct
  per-generation behavior. When a whole tree restores with its live binding
  re-established, the generation stays open, and the journaled intent serves
  until the child's next report. Live reporting resumes wherever the activity
  hook is bound today (agent/delegate_runtime.go:823, 958, 1730, 2235).

## Consumers (shared client, web, native)

- **No new decode layer, no row drops.** The producer's `Excerpt` bound is
  the contract. The store does add a field-level bound/validity check at the
  two application sites — the rune-count walk of the S5 decoder's shape
  (appwire-client/typescript/sessionActivity.ts:22-30) — but the S5
  decoder's entry-drop does NOT carry over: an absent, over-bound, or invalid
  `latestIntent` is treated exactly like an omitted field — apply the empty
  value — and never drops a row or frame.
- **Store.** `delegateRowFromFrame` copies `latestIntent`; absent clears. The
  merge stays behind the existing `reportPreview` + live gate (ruling 4).
- **Rows — one shared helper.** All changed surfaces display through one
  helper in `appwire-client` (e.g. `delegateWorkText(delegate)`), so the rows
  cannot drift: a row shows the intent in place of the status word when the
  row is non-terminal and the intent is non-empty; every other row state
  keeps today's text. `activityDelegateState` keeps returning `status`
  unchanged (it drives sorting and aggregation); the renderers call the
  helper for display. Glyph tone computation is untouched. The stable-row
  wire carries no attention state to key on — its phase vocabulary is the
  delegatestore lifecycle verbatim (idle/running/settling/stopping/closed,
  agent/internal/delegatestore/record.go:17-21, projected unchanged at
  agent/session_activity_delegates.go:221), the `awaiting_*` values the web
  checks defensively (activityRows.tsx:58) are emitted by no Go producer, and
  `NeedsAttention` exists only on frames, not on `SessionDelegate` rows — so
  attention presentation on these rows is unchanged because the row shape
  cannot carry it today, and nothing regresses.
- **Accessibility.** The row's status word must remain reachable when the
  intent replaces it. The sidebar's `AgentRow` gains a title/aria carrying
  the runtime status (activityRows.tsx:66-94 has none today; `JobRow` shows
  the pattern at :106). The tree rows need active seam-splitting, not
  preservation: `rowStatusText` (ActivityTree.tsx:97-104) is one function
  feeding the visible meta segments (:192,:195), the row's aria-description
  (:397,:413) and the glyph-tone classifier (`jobStatusDotState(statusText,…)`,
  :399), so switching that one function to the helper would put the intent
  into the aria and the tone classifier at once. Pin the implementation: the
  visible meta segment switches to the helper; the aria-description and the
  glyph-tone input keep the status word. The native sheet is genuinely
  separate (SubagentRowView.tsx:45 keeps `subagentStateWord` in the
  accessibility label independent of the displayed line). This is new work,
  not preservation, in both places.
- **Unchanged surfaces, named so an implementer need not guess:** the
  transcript's inline delegate entity card
  (cmd/evener-hub/frontend/src/panes/session/transcript/EntityRef.tsx:120-146 —
  a detail surface; it keeps its status word and its classifyJobStatus
  treatment); the ActivitySheet fed by the mobile/ shared service
  (mobile/src/services/activity.ts projects `EvenerDelegateInfo` diagnostics
  into view models for an out-of-repo consumer — the diagnostics shape, whose
  hub-side producers fill no intent); the expanded delegate detail (report
  preview); the rail HoverCard; navigation summaries; the root session's
  pulse `LatestIntent`; the TUI; the jobs/list diagnostic delegate entries.

## Compatibility

- Older daemon: field absent everywhere; rows fall back to today's status word.
- Older hub / remote source: typed passthrough as today (the source adapters
  decode into the same struct, cmd/evener-hub/internal/appsource/session_activity.go),
  same fallback.
- **Journal versions.** The header stays 1. A pre-feature journal folds
  identically under the new reader (test it). A newer journal read by an older
  daemon fails that source loudly — the fold's unknown-kind error
  (agent/internal/delegatestore/fold.go:181-182) and the page scanner's strict
  decode (read_page.go:95) — which is the same typed source-unavailable issue
  with bounded retry every additive journal event kind already costs
  (the guide maps corrupt journals to exactly that handling,
  docs/product/session-activity.md:186); this feature accepts that cost
  rather than inventing a compat shim.
- **Cold-read budget.** The new event adds one fold work unit per stated tool
  call, so a tool-heavy run (say 500 calls) adds ~500 units to retained
  establishments of that delegate journal, under the shared cold budget of
  2,000 event/projection units and 4 MiB per pass
  (docs/product/session-activity.md:271-283). Some retained reads that
  completed in one pass become valid multi-page continuations — degraded
  paging, not corruption, and the guide's existing recovery owns it. A domain
  test must show a journal dense with intent events pages correctly under the
  cold budget with no silently excluded rows.
- The feature adds only display state. An absent or over-bound value renders
  today's status word; it never blocks a read, changes membership or counts,
  or alters attention, quiet, stop or delivery behavior. A genuinely corrupt
  journal fails exactly as delegate journals already do today.

## Test plan (whole journey)

- **agent domain:** child states an intent mid-long-call → the frame publishes
  without waiting out the 5-second floor after a quiescent gap, and an intent
  change inside the window is journaled and rides the next publication
  without being dropped (plain motion still coalesces); an unchanged intent
  report is a no-op — no journal event, no publication; a failed journal
  append leaves live state, rows and frames untouched; **a nested subagent's
  tool call never lands on its parent's delegate row (ruling 1's own-session
  fence, exercised through a real grandchild)**; the intent report
  advances NO clock — the quiet baseline, productive-liveness and rearm are
  byte-for-byte today's cadence when only intent reports fire; new generation
  clears; run settle clears fold and live and the two wire fields agree;
  a turn start clears; a no-intent call leaves the previous line standing;
  newest started call wins within one batch; bound enforcement (200 runes,
  single line, ellipsis, whitespace flattened) at the report site, the
  frame projection and the read capture; stale lease cannot report;
  during the settling window a rejected clear or statement leaves the
  standing intent, and settlement retires it; a pre-feature journal folds
  identically; the
  header version stays 1; `publicProjection` includes the intent field (two
  successive intent events within one run advance the revision twice); a
  tool-call-dense journal pages correctly under the cold budget.
- **restore:** a retained cold read of an interrupted open generation shows
  the journaled intent; runtime-lost reconcile retires it with the failed
  outcome; a whole-tree restore shows it until the child's next report; and
  during that restored window a frame published by an unrelated
  revision-advancing event carries the standing folded intent — the frame
  and the read agree, and a live-only frame builder fails this test.
- **wire/projector:** `appwireDelegateInfo` bounds `LatestIntent` with the
  same `Excerpt` the read uses — parity test over newline- and multi-space
  laden values; the roster row keeps the field; over-bound values apply as
  absent, never dropping a row.
- **store:** a frame merge under the gate sets and clears it; **two successive
  intent frames within one run both land** (the second wins because its
  revision advanced); gate closed → the existing read path applies;
  generation fencing — a previous generation's frame never writes onto a
  newer run's row.
- **web:** the sidebar `AgentRow`, the activity tree rows and the ActivityPanel
  show the intent through the shared helper; terminal rows unchanged; the
  sidebar row carries the status word in its new title/aria; and a tree row's
  aria-description and glyph tone still read the status word while its
  visible meta shows the intent. `make test-web`.
- **native:** the shared helper covered by appwire-client tests; the
  Subagents screen activity rows show intent with fallback; extend the
  compile-time exhaustive delegate fixture
  (mobile-native/src/subagents/sessionActivityTestUtils.ts:19-49 fails to
  compile until the adapter covers the new field); regenerate the recorded
  producer outcomes only if their outputs actually change (the subagentwire
  recordings are jobs/list answers and should not). `make test-native`.
- **journal envelope:** add the new kind to the delegatestore envelope fuzz
  table and `validateEventEnvelope`
  (agent/internal/delegatestore/envelope_fuzz_test.go:11-25).
- **gates:** `make lint`, `make vet`, `make test`, plus `make test-web` and
  `make test-native` for the touched surfaces.
- **docs:** update `docs/product/session-activity.md` (the delegate reports
  section) and the subsystem map in the same change, keeping the guide's
  invariants true (no new reads, no nav change, per-generation snapshot
  discipline).

## Deliberately open for implementation (not spec gaps)

- The exact Go signature of the intent-report entry and the child-side
  callback shape — the channel's behavior is pinned (three effects, no
  clocks, running-lease admission, empty-folds-as-clear).
- The exact fold field placement for the new event in
  `delegatestore.Aggregate`.
