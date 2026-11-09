# Spec 1: Manager fleet snapshot

One `job_list` discovery call returns every live lane's operational state —
working, quiet, waiting on the human, failed, or crashed — so a Manager
stops rebuilding the fleet picture from N per-session reads.

## Problem

The Manager hub's `job_list` has three modes today — `discovery`
enumerates sessions, `tasks` reads one session's task page, `activity`
reads one session's delegate/job/watch rows — and no single call answers
the question every fleet sweep starts with: which lanes are working,
idle, quiet, waiting on a question, or finished with an outcome I have
not processed. Tonight the Manager answered it by reading each lane's
transcript individually, every 30 minutes, at five-plus lanes per sweep,
each read dragging full tool output. The information itself is about one
line per lane.

Most of that line already exists somewhere in the product: the discovery
row already carries ask flags, the pending question's own text, failure,
last-turn-ended and subagent tallies (grounded below); the daemon's wire
already stamps the running turn's id and start moment; the roster already
retains a crashed session's last facts. What is missing is the last hop
in every case: the hub's probe drops the turn facts, the row projection
computes no quiet clock, a crashed lane is dropped from discovery
outright, a source that fails to answer leaves no trace in the response,
no attention level rides the row, and nothing tells the Manager any of
this exists. This spec closes the hops; it does not duplicate a single
fact the row already carries.

## Current behavior (grounded at origin/main 2be411cd8f)

- **Manager tool layer.** `job_list`'s hub branch
  (`agent/session_tools_hub.go:627-756`) decodes `discovery` into
  `ThreadListParams`, `tasks` into `SessionTasksRequest`, `activity` into
  `SessionActivityRequest{ref, resource, scope, cursor, limit}`; schema at
  `managerReadDefinition` (:36-64). The whole Manager read is capped at
  256KB and fails whole if exceeded (`managerReadMaxJSONBytes`,
  `cmd/evener-hub/session_reads.go:241-253`, called from
  `cmd/evener-hub/session_tools.go:398-401`).
- **Discovery rows already carry most of the story.** The hub's local row
  builder `threadFromEntry`
  (`cmd/evener-hub/internal/appsource/local_daemon.go:1488-1504`) sets, on
  every local row: `Evener.AskPending`, `Evener.PendingQuestion` (the
  question's own text, options and count, bounded to
  `MaxQuestionTextRunes = 200`, `appwire/excerpt.go`), `PendingEscalations`,
  `Failure`, `LastTurnEndedAt`, `LastMessage`, `Tasks`, `Subagents`, and a
  jobs/watches diagnostics block when non-empty, plus the thread's own
  `status.type`
  ("awaiting" while a question waits). Rows pass to the Manager through the
  hub merge, which enriches them — `mergePastMetadataForList` and
  `applyHubCapabilities` fill empty metadata and rewrite page cursors
  (`cmd/evener-hub/app_threadlist.go:200-204`; harmless to the ask
  facts) — before the size check applies (`managerReadWithinCap`,
  `session_tools.go:399-401`). The same facts
  reach the Manager's `job_status` thread read (pinned at
  `cmd/evener-hub/e2e_manager_session_test.go:150`). Merged rows for
  sessions no daemon serves come from the disk index and carry no live
  facts.
- **The running-turn facts are on the wire but the probe drops them.**
  `ProbeResultFromThread`
  (`cmd/evener-hub/internal/hubcore/prober.go:165-196`) fills `LiveEntry`
  from the thread row the daemon already serves. The daemon's thread
  projection stamps `EvenerThread.ActiveTurnID` and `ActiveTurnStartedAt`
  (`server/appwire_thread_projection.go:122,140`) — but `ProbeResult` has
  no fields for them, so `LiveEntry` drops them. The change this spec
  needs is hub-side probe plumbing only. One shape note: a new execution
  has its `ActiveTurnID` before its first persisted transcript entry
  (`agent/session_execution.go:54-76`), so a running turn can
  legitimately be ahead of the persisted view.
- **The quiet clock is computed hub-side for the roster, but not for
  rows.** The daemon stamps the meter sample on the root row
  (`server/appwire_read_handlers.go:67-69`) and it rides the probe
  (`cmd/evener-hub/internal/hubcore/prober.go:190,218`); separately, the hub's roster view already
  computes tree-wide quiet time at read time from an entry's activity
  facts (`cmd/evener-hub/app_activity.go:79-101`). Discovery rows
  carry neither.
- **A failed probe serves retained facts with no marker, and no existing
  field distinguishes them.** `classifyMissedProbe` retains the previous
  record when a probe of a still-live daemon fails, clearing only
  lifecycle freshness
  (`cmd/evener-hub/internal/hubcore/roster.go:851-868`), and discovery
  reads those records through `localDaemonEntriesFromRoster`
  (`cmd/evener-hub/app_rpc.go:122-149`). A local source can therefore
  answer successfully while a lane's per-session facts are one or more
  probes stale, with nothing in the response saying so. Worse, the one
  candidate marker mislabels: `missReused` publishes the retained entry
  with only `LifecycleFresh=false`; `missRetained` publishes no entry
  at all (no case in the roster's publish switch); and a healthy daemon
  whose lifecycle sub-probe fails also gets `LifecycleFresh=false`
  (`cmd/evener-hub/internal/hubcore/prober.go:146-147,258-262`) — so `LifecycleFresh` cannot be the
  probe-state source; the state is new roster-owned work.
- **Crashed lanes vanish from discovery; the vocabulary has two layers.**
  `localDaemonEntriesFromRoster` skips crashed roster entries outright
  (`app_rpc.go:119-127`), while the roster retains the crashed session's
  last record with `Status: "errored"` and the `Crashed` flag set
  (`cmd/evener-hub/internal/hubcore/roster.go:907-916` — the roster's
  retained vocabulary is the string `errored` plus a `Crashed` bool).
  The row's own `status.type` vocabulary has no `errored`: `ThreadStatus`
  is idle/active/awaiting/warning/closed/notLoaded/systemError/
  restartRequired (`appwire/types_protocol.go:365-371`); `errored` is the
  hub's normalized state (`hubcore/tree.go:566-567`) and a client
  attention state. The row projection maps unknown values to idle
  (`cmd/evener-hub/internal/appsource/local_daemon.go:1659-1679`), and
  nothing on `LocalDaemonEntry` marks a crashed entry, so simply
  removing the adapter's skip would make crashed entries routable for
  live RPC — a discriminator has to travel with the row. Routing and
  display are SEPARATE lists today, and no existing filter covers
  routing: `ListThreads` iterates `listedEntries`, its own doc's
  display list — "for display, without admitting them to the live
  RPC routes" (`local_daemon.go:1443-1444`) — while the route-eligible
  set `liveEntries` serves `localEntryForRefMode` (`:1410`, with the
  read-only-alias parameterization) and `ListModels` (`:1022`) only,
  and session-tool binding (`SessionToolsThread`,
  `local_daemon.go:218-228`) and session-tool route publication
  (`RunSessionTools`, `local_daemon.go:280-286`) iterate the RAW entry
  set with inline alias skips — a crashed entry retained for display
  is routable on both of those today, and the route-eligible set
  itself carries no crashed discriminator.
- **Failed sources contribute nothing on the implicit fan-out — and
  explicit reads fail whole.** On an implicit read a source error is
  swallowed and the merge skips the source (`app_threadlist.go:186-190`);
  the per-source outcome exists only as an internal error value
  (`sourceResult`, `app_threadlist.go:82-86`) and the merged response
  carries data only (:235). An EXPLICIT read is different today: when
  `sourceExplicitlyRequestedForList` is true — the same `SourceIDs`
  membership the selection itself uses (`app_threadlist.go:300-309`) —
  the source's error is RETURNED whole and no response is served
  (`app_threadlist.go:183-190`). Explicit failure is fail-whole, so no
  per-source response state can appear on a failed explicit read. The
  response's `Sources` list is static configuration
  (`cmd/evener-hub/session_reads.go:40-51`) with no answered or
  failed concept. The schema's `statusOnly`/`includeSubagents` params
  (`session_tools_hub.go:47`) exist but their hub-side behavior is
  unverified; the implementer verifies and preserves them.
- **The attention derivation is archive-blind and remote-inclusive for
  the Manager.** `watchHubAttention` derives the Manager roster from the
  same remote-inclusive navigation snapshot the tree uses, passing nil
  archive decisions — "Archiving clears human attention, not the
  Manager's live population" (`cmd/evener-hub/main_background.go:63-72`),
  pinned by `cmd/evener-hub/attention_roster_archive_test.go:14-90`.
  The derivation itself excludes non-top-level sessions (subagents);
  it excludes neither archived nor remote lanes. The daemon roster
  behind this spec's probe facts is a different, LOCAL-ONLY
  population: sessions on other hosts reach the hub only through the
  remote-thread cache (`main_background.go:63-66`), so every
  probe-sourced fact below is a local-lane fact. The Manager's own ref
  is excluded from its roster watch (`docs/automation.md:135-137`);
  discovery itself lists it like any row.
- **PR overlaps.** 5659 and 5641 touch the same `session_tools_hub.go`
  schema block; this spec extends it additively and rebases under both.

## Scope

Close the last hops, hub-side, so one discovery read is the fleet
picture. Row fields, all sourced from facts established above — the
two turn fields already exist on the wire type and are populated by
this spec, the rest are new:

| Field | Meaning | Source (existing today at that layer) | Absent when |
| --- | --- | --- | --- |
| `attentionLevel` | The Manager roster's own level for the row | attention derivation (spec 4 owns the row wiring) | Spec 4 not landed; subagent row; no roster entry (disk-index row). NOT absent for archived or remote lanes — the Manager roster includes them |
| `activeTurnId` | The id of the turn currently running on the lane | the EXISTING `EvenerThread.ActiveTurnID` wire field (`appwire/types_thread.go:91`), which the daemon already stamps (`server/appwire_thread_projection.go:122`) and the hub's `threadFromEntry` never populates — populate it, mint no new key; carried through the probe that already feeds on the row (`LiveEntry` gains the field, `cmd/evener-hub/internal/hubcore/prober.go:165-196`) | No turn is running; older daemon; retained probe record; disk-index row; crashed row; remote lane (no local probe) |
| `activeTurnStartedAt` | Unix ms the running turn started | the EXISTING `EvenerThread.ActiveTurnStartedAt` (`appwire/types_thread.go:161`; stamped at `server/appwire_thread_projection.go:140`) — the same populate-don't-mint rule and the same probe carry | Same as `activeTurnId` |
| `quietForMs` | Tree-wide quiet clock | Computed at the read moment by the roster's own rule (`localSessionActivity`, `app_activity.go:79-101`) from the entry's activity facts — `LocalDaemonEntry` carries no `Activity` field today, so the entry field and the roster→entry copy (`localDaemonEntriesFromRoster`, `app_rpc.go:128-148`) are named landing zones | Not working, or a subagent runs (the meter's own rule); retained probe record; disk-index row; crashed row; remote lane (the local hub holds no activity facts for it) |
| `probeState` | Whether the row's facts come from this cycle's probe | a NEW roster-owned probe-state field: `fresh` on the probe path (`liveEntryFromProbe`), `retained` on `missReused` and the crashed miss path; `missRetained` entries are not listed at all. `LifecycleFresh` is explicitly NOT the source (it mislabels fresh rows; grounding above) | Never on a locally probed listed row: `fresh` or `retained` (disk-index and remote rows carry none — remote lanes have no local probe) |

Ask-pending keeps the row's existing `Evener.AskPending` as its one
fact; this spec adds no ask field, and spec 4 adds the attention row
fields — `attentionLevel` and its `attentionAsOf` moment —
`needsResponse` is deliberately NOT a row field (its
tested meaning, `awaiting && !askPending` at `hubapi/attention.go:75-78`,
is the opposite of "the Manager should look here"; the row carries the
components and the guides state the decision rule).

Presence is the semantics: `activeTurnId` present means a turn is
running. There is deliberately no separate `turnStatus` field — a
completed lane already names its last turn through the existing
`LastTurnEndedAt` and `LastMessage`.

The response envelope gains one field, `builtAt`: the moment the hub
built the response, stamped once per discovery read, never absent.
Every row's `quietForMs` is computed against that same moment, so a
reader can bound the age of the whole picture from the payload alone.
`builtAt` is this spec's own stamp and is never compared with spec 3's
per-serve `asOf` (a view moment, a different clock — the docs say so
explicitly).

Population changes, both hub-side:

- **Crashed lanes become rows, in the existing vocabulary.** A crashed
  roster entry (`Crashed` set) is included as a discovery row: the
  row's `status.type` projects the EXISTING `systemError` — the
  `ThreadStatus` value that hub and client normalize to `errored`
  (grounding above; no new status value is minted) — beside a projected
  `crashed` discriminator on the row and its entry, the roster's
  retained durable facts (last turn ended, last message, subagent
  tallies, and `Failure` exactly when the last row carried one), and
  `probeState: retained`. Routing excludes crashed entries through
  ONE shared eligibility predicate, new hub-side work, with the
  display/routing split as its spine: `LocalDaemonEntry`
  gains the `Crashed` flag, and the predicate serves every live-route
  consumer — `localEntryForRefMode` (`:1398-1410`, whose
  read-only-alias bool becomes the predicate's parameter),
  `ListModels` (`:1022`), session-tool binding (`SessionToolsThread`,
  `:218-228`) and session-tool route publication (`RunSessionTools`,
  `:280-286`, both of which iterate the raw entry set with inline
  alias skips today) — while `ListThreads` KEEPS its display list
  (`listedEntries`, `local_daemon.go:1443-1444`: display preserves
  incompatible owners WITHOUT admitting them to live RPC routes), so
  the crashed row stays listed. The predicate's SCOPE is named so no
  consumer's population silently shifts: it owns the CRASHED
  discriminator and the read-only-alias purpose — one gate for the
  concern this spec is about. `liveEntries`' protocol-compatibility
  and restart-required filters (`local_daemon.go:1432-1442`) are a
  SEPARATE existing concern of the ref/model routes and stay exactly
  where they are; the predicate composes beside them and absorbs
  neither. For non-alias entries the two session-tools consumers'
  adoption changes nothing else (their inline alias skips become the
  predicate's parameter). The ONE deliberate behavior change is this
  spec's own purpose, stated as a change and not as a uniformity
  that already exists: crashed entries, today routable through those
  two consumers' raw iteration, become REFUSED there — pinned by
  journey 4's refused-on-every-route case. A crashed row stays
  displayable while
  no route can send a
  request to its dead daemon; there are no per-route checks to forget
  because there is one gate. The status projection must not fall
  through `localDaemonThreadStatus`'s unknown-to-idle mapping for this
  class. This is distinct from a disk-index row (no daemon, no roster
  record), which keeps today's fact-free shape.
- **Source outcomes become visible.** Each `Sources` entry gains a
  state: `answered`, `attempted-failed`, `not-selected`, or
  `skipped-unattached`. The catalog's POPULATION keeps today's
  `discover` rule exactly (grounding above: local plus every
  configured host, whatever the caller's filter), so no read shape
  changes what `Sources` lists. The states keep the two skip reasons
  apart, because the `allowed` set skips for two different reasons
  (grounding above): on an EXPLICIT read (`sourceIds` non-empty) a
  source the filter did not name reads `not-selected`; on an IMPLICIT
  read a configured, unattached remote reads `skipped-unattached`. A
  source never carries both labels — explicit reads skip only by the
  filter, implicit reads only by attachment — so state derived from
  `allowed` names the reason that put the source outside it. The
  plumbing is named owned work: `hubThreadListWithSourceTimeout` today
  swallows a non-explicit source's error and returns only the merged
  threads (grounding above), so it RETURNS per-source outcomes to
  `discover`. The explicit-failure contract is PRESERVED, not migrated:
  an explicit read in which any named source fails keeps today's
  fail-whole error (`app_threadlist.go:183-190`) — no partial response,
  no `attempted-failed` on the wire — so `attempted-failed` is a state
  of the IMPLICIT fan-out only. The explicit truth table: every named
  source answers → each reads `answered`, unnamed catalog entries read
  `not-selected`; any named source fails → the read fails whole with
  that source's typed error, whether one source or several was named.
  The mixed named-source case fails whole deliberately: a Manager that
  named two sources and received one source's rows would silently
  mis-scope its sweep.
- **Remote lanes appear with their own facts, not the local probe's.**
  The attention roster's population includes remote lanes (grounding
  above), and a remote row carries its own thread-row facts —
  `status.type`, `Failure`, `AskPending`, `PendingQuestion`,
  `LastTurnEndedAt` — the row the remote hub serves, forwarded by the
  remote source's field-by-field rebuild (a named landing zone beside
  spec 2's capability bit). The local hub's probe-derived additions —
  `activeTurnId`, `activeTurnStartedAt`, `quietForMs`, `probeState`,
  the `crashed` discriminator — are ABSENT on remote lanes: the daemon
  roster behind them is local-only (grounding above), a dead remote
  host is a source error (`attempted-failed`), never a local crashed
  row, and `localSessionActivity` has no activity facts for a lane it
  never probed. `attentionLevel` (spec 4's wiring) IS present on
  remote lanes — the one enrichment whose roster is
  remote-inclusive.

No field the row already carries is duplicated or renamed: `AskPending`,
`PendingQuestion`, `Failure`, `LastTurnEndedAt`, `Subagents` and
`status.type` keep their names and meanings, and this spec adds nothing
beside them. Non-root rows — a descendant's read-only alias row — keep
today's shape and carry no fleet facts. The read stays one call, ordered
as today, no new pagination, and the completeness boundary is the
existing 256KB whole-read cap: an over-cap response keeps today's
refusal. Acceptance asserts the full encoded response, new fields
included, under the cap.

## Non-goals

- No daemon protocol change: every new row fact is carried by the thread
  row already on the wire, or already held in the hub (roster record,
  activity facts, read-time clock, missed-probe classification). The
  change is hub-side probe plumbing, row projection and discover
  merging.
- No new durable state.
- Not a client behavior change; the wire fields this spec adds land with
  regenerated AppWire types and updated typed-client validators, per
  the repo's generated-output gates.
- No subtree rows; per-delegate drill-down stays with `activity` and
  `job_status`.
- No watch events; this spec is pull.
- No second fan-out; enrichment rides the probe and the existing
  discovery fan-out.

## Behavior contract

1. **One call, one picture.** A single discovery read classifies every
   lane the Manager roster covers as working, quiet, question-pending,
   approval-pending, failed, crashed, or idle-without-new-outcome, with
   no second call: working via `status: active` (`quietForMs` present
   only when no subagent runs, per the meter's own rule — it is not
   the working signal for subagent-running lanes), quiet via
   `quietForMs`, idle
   recency via the existing `LastTurnEndedAt` (non-working lanes — the
   meter is silent for them by its own rule), question- and
   approval-pending via the existing ask facts plus `attentionLevel`
   (spec 4's wiring), needs-response rest via `status: awaiting` with
   `AskPending: false` and no escalations (the `NeedsResponse` state,
   `hubapi/attention.go:75-78`; the row carries the components, no new
   field), failed via the existing `Failure`, crashed via
   the crashed row. The roster's population is the contract's
   population: archived and remote lanes are included
   (`main_background.go:63-72`); subagents are not. The full
   classification is computable for LOCALLY probed lanes; a remote
   lane carries its own row's classification (its `status.type`, ask
   facts and `Failure`), and its quiet and crashed story belongs to
   its owning hub (scope above). The night's sweep
   loop — one heavyweight transcript read per lane — becomes this one
   read plus targeted reads only where warranted.
2. **Shared derivation.** `attentionLevel` comes from the same attention
   derivation the Manager roster consumes (spec 4's wiring, absent when
   that spec has not landed); ask-pending stays the row's existing
   `Evener.AskPending`; `quietForMs` is the roster's own read-time rule,
   computed against the response's `builtAt`. No second copy of any
   fact is introduced.
3. **Agreement with per-session reads.** For any live lane, the row's
   `activeTurnId` agrees with a fresh transcript read of that lane
   when the running turn has persisted entries; inside the in-flight
   window (a new execution has its turn id before its first persisted
   entry, `session_execution.go:54-76`) the row may legitimately name a
   turn the persisted view does not show yet, and each side's field
   says what it means — the row names the running turn, the read's
   frontier (spec 3's terms, when landed) names the newest persisted
   execution turn. Timing comparisons are interval-tolerant, never
   equality. `job_status` carries no turn id on either of its shapes
   (the owned-delegate result, and the hub branch's
   `ThreadReadParams{Ref}` without `IncludeTurns` — grounding above and
   in spec 3), so it agrees on lifecycle status and activity facts
   only; turn identity is witnessed by the transcript read, never by
   `job_status`.
4. **Absent, not wrong, and fresh or marked.** On an IMPLICIT read a
   failed or timed-out source contributes no rows for its sessions
   (today's behavior) and its `Sources` entry reads `attempted-failed`;
   a source an explicit
   read's filter did not name reads `not-selected`; a configured,
   unattached remote an implicit read did not query reads
   `skipped-unattached`. An explicit read never carries
   `attempted-failed`: when any named source fails, the read fails
   whole with today's typed error (scope above). An older daemon's
   fields are absent, never zero or stale-filled. A row served from a
   retained (missed-probe) record keeps its durable facts and drops
   the volatile ones (`activeTurnId`, `activeTurnStartedAt`,
   `quietForMs` absent), with `probeState: retained` saying why — the
   volatile facts
   would otherwise claim a present that is not established.
5. **Crash and restart honesty.** A crashed lane appears as a crashed
   row — `status.type: systemError` (normalized to `errored` by hub and
   client), the `crashed` discriminator, the retained durable facts,
   `Failure` present exactly when the last row carried one
   (absent-when-none — a mid-run kill has none), and never routed for
   live RPC (the one shared eligibility function, scope above); a
   daemon restart fabricates nothing — the next successful
   probe replaces the retained record (`probeState` returns to
   `fresh`), and `activeTurnId` stays absent until a turn actually
   runs.
6. **Cost bound.** Enrichment from the hub's own roster and probe costs
   no new query; nothing extends a source's budget; what cannot be
   gathered within budget is absent for that row.
7. **Read-only, non-blocking.** The read mutates nothing, wakes nobody,
   and does not adopt the roster watch's self-exclusion (the Manager's
   own row appears as today).

## Recovery and preservation

The read is pure; its failure mode is absence, labeled per contracts 4
and 5. A Manager that gets a partial picture keeps what its previous
reads established — the pulse clients' own per-row fallback discipline.
Facts are derived per read from live state, so there is nothing to
reconcile after a crash except by reading again, which `builtAt` makes
safe to decide. A crashed lane's retained facts are the roster's own
record; a row built from them names its facts' moments
(`LastTurnEndedAt`), never pretending they are current.

## Journey test plan

Seed one hub with: a session mid-turn with tool output flowing; a session
idle awaiting a wake; a session resting needs-response (awaiting, no
pending question, no escalation); a session that just completed a run; a
session
holding a pending question; an archived session still holding a pending
question (the roster's pinned population); a session whose daemon
predates the probe's carrying of the new fields; a stopped session
present only in the disk index; a remote lane covered by the roster
(served by the remote source); a source that fails to answer; and a
target session whose daemon will be killed mid-run.

1. One discovery read: every top-level session appears once; the
   mid-turn row reports `activeTurnId` and `activeTurnStartedAt` with a
   `quietForMs` bounded by real elapsed time; the question row reports
   its existing `AskPending`/`PendingQuestion` plus `attentionLevel`
   (asserting absence before spec 4 lands, per the README's order
   rule); the archived question row behaves the same (the roster
   includes it); the completed row reports through unchanged status
   fields with `activeTurnId` absent (no turn runs); the old-daemon
   row carries legacy fields with new ones absent; the disk-index row
   carries no fleet facts; the remote row appears with its own row
   facts and `attentionLevel` (asserting absence before spec 4 lands,
   per the README's order rule) while `activeTurnId`,
   `activeTurnStartedAt`, `quietForMs` and `probeState` are absent; the
   awaiting-rest row classifies via `status: awaiting` with
   `AskPending: false` and no escalations; the alias descendant row
   carries no fleet facts; the response carries `builtAt` once.
2. The failed source contributes no rows and its `Sources` entry reads
   `attempted-failed`; a configured-but-unattached source reads
   `skipped-unattached`. A second discovery read with an explicit
   `sourceIds` filter naming one source: the named source reads
   `answered`, every
   other catalog entry reads `not-selected`, and no entry reads
   `skipped-unattached` — the filter, not attachment, decided the
   fan-out. The catalog's population is identical to the unfiltered
   read's. A third read names one healthy and one down-daemon source
   together: the whole read fails with the down daemon's typed error
   and serves no rows — fail-whole preserved, including the mixed
   named-source case (scope's explicit truth table).
3. Cross-instrument: for a live lane with a persisted running turn, the
   row's `activeTurnId` matches a fresh transcript read (spec 3's
   frontier terms, asserted in a build where both specs have landed);
   for a lane inside the in-flight window, the row names the running
   turn and the read's frontier names the prior turn, both honestly
   labeled; `job_status` for the same lane agrees on status and
   activity facts.
4. Kill the daemon of the mid-turn session: today the row disappears
   (red asserts the drop); post-change the row appears with
   `status.type: systemError` (the value hub and client normalize to
   `errored`), `Failure` ABSENT (the mid-run lane's last row was
   `active` with no failure; absent-when-none asserted), the `crashed`
   discriminator, `probeState: retained`, `activeTurnId` absent — and
   a read attempt routed at the crashed row is refused at every
   live-route consumer — ref-mode resolution, model listing,
   session-tool binding, route publication — by the one shared
   eligibility predicate, never sent to a dead daemon, while the
   discovery list still displays the row: the display/routing split
   asserted in both halves (the row remains listed; the route refuses
   it). A second
   seed — a lane that failed a turn and THEN crashed — asserts the
   retained `Failure` does travel when the last row carried one. A
   restart does not resurrect a stale `activeTurnId`.
5. Probe failure with the daemon alive: inject a probe failure for a
   working lane; the next read serves the lane's durable facts with
   the volatile ones absent and `probeState: retained`; on probe
   recovery the volatile facts return and `probeState` reads `fresh`.
6. Red-first: today's rows lack `attentionLevel`, `quietForMs`,
   `probeState` and any crashed discriminator; no discovery row
   carries its lane's running turn identity even though the wire type
   already defines `activeTurnId`/`activeTurnStartedAt`
   (`appwire/types_thread.go:91,161`) — the hub's `threadFromEntry`
   never populates them; crashed lanes are absent from discovery
   entirely; `Sources` carries no per-source state. The red asserts
   exactly these absences and omissions (and asserts the fields that
   DO exist today,
   `AskPending` and `PendingQuestion`, still travel — the
   anti-regression half of the red).

## Dependencies and interfaces

- Consumes spec 4's attention wiring for `attentionLevel`
  (absent-when not landed); consumes spec 3's frontier vocabulary for
  the agreement clause (conditional, in-flight rule stated); shares
  nothing else — `builtAt` is this spec's own stamp.
- Produces the probe-side carrying of running-turn facts; no other spec
  orders on it (spec 3's oracle does not depend on this spec landing).
- Rebases under 5659 and 5641 in the shared schema block; verifies the
  unverified `statusOnly`/`includeSubagents` behavior before preserving
  it.
- Wire-schema obligation: the genuinely new row and response fields
  (`quietForMs`, `probeState`, the `crashed` discriminator, `builtAt`,
  the `Sources` per-source state, and spec 4's `attentionLevel` and
  `attentionAsOf`) land with regenerated AppWire types
  and updated appwire-client validators in the same change;
  `activeTurnId`/`activeTurnStartedAt` already exist on the wire type
  and need no schema change — only population.

## Acceptance criteria

- The journey passes red-first with the red asserting only genuinely
  absent fields; one discovery read then carries the full fleet picture
  for roster-covered rows, with the complete encoded response asserted
  under the 256KB cap.
- `docs/automation.md`'s discovery contract gains the new fields, the
  crashed row (`status.type: systemError` plus the `crashed`
  discriminator — the existing vocabulary) and the `Sources` states
  (extending, not replacing, the documented
  lines); the Manager prompt template
  (`agent/testdata/messagingprompt/manager-on.md`) teaches the fleet-read
  shape; the owning subsystem row names the enrichment.
- The PR description names the older-daemon direction it tolerates (rows
  absent-when) and its removal condition (fields served by every
  supported daemon version).

## Alternatives considered

- **Extend the AppWire pulse read and have Managers poll it.** Rejected:
  Managers do not speak the client surface; the meter's fields are
  presentation-shaped (bars, intent) rather than operational (turn ids,
  attention); the shared substrate (meter facts on the probe, thread-row
  turn facts) is reused instead of duplicated.
- **A new standalone tool or activity resource.** Rejected: discovery
  already owns enumeration in the Manager surface; a second enumeration
  or a ref-bound resource for a fleet-wide, unpaginated fact set drifts
  from day one. One door, extended.
- **Duplicating the ask/attention facts on rows under new names.**
  Rejected: the row already carries `AskPending` and
  `PendingQuestion`; a second spelling creates two owners for one fact.
  This spec adds only what is genuinely absent.
- **A `needsResponse` row field.** Rejected across rounds 2 and 3: its
  tested meaning (`awaiting && !askPending`, `hubapi/attention.go:75-78`)
  is false exactly when a question waits — the opposite of what a
  sweeping Manager needs. The row carries the components; the guides
  state the rule.
- **Carrying a completed-turn fact on rows.** Rejected after round-2
  review: the probe's source field (`ActiveTurnID`) clears when a turn
  ends, so a completed-turn identity has no source on this path without
  a protocol change; `LastTurnEndedAt`/`LastMessage` already answer
  "what did it do last" for idle lanes.
- **Serving volatile facts from retained probe records.** Rejected in
  round 3: a missed probe's retained `activeTurnId` would claim a
  present that is not established; the volatile facts go absent with
  `probeState: retained` naming why, per the absent-not-fabricated
  rule.
- **Push-only state events.** Rejected as primary: push without
  trustworthy pull is how the fleet ended up with backstop timers
  everywhere. Pull is the foundation.
