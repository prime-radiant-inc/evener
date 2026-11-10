# Spec 3: Transcript-read serving facts and the status-consistency contract

Every Manager transcript read states which view served it, the moment that
view reflects, and its newest execution turn — facts the caller compares
against the fleet picture. The hub states facts; the Manager computes
freshness.

## Problem

Tonight, the Manager read a delegate's transcript through the hub and
received a turn that was 11 hours old as the newest turn, presented with
no marker, while `job_status` for the same delegate reported a run started
minutes earlier with fresh activity. Two tools, one session, contradictory
answers, no field admitting the disagreement.

The read path has not one silent-staleness seam but three, all grounded
below: a live read that FAILS falls back to the hub's saved-transcript
index; a live read that SUCCEEDS EMPTY (a daemon that does not own the ref
answers with an empty thread) falls through to the same index on the
Manager path; and a daemonless session is served by the index outright.
Worse than unmarked: on the succeeded-empty seam the saved items are
merged under the LIVE daemon's BootGeneration (grounding below), so the
response's own identity field claims liveness for saved content. And
`Authoritative: true` is stamped on saved serves in contradiction of its
documented meaning — a flag the typed clients key held-item replacement
on (grounding below), which makes the lie load-bearing, not cosmetic.

Round-2 spikes reproduced the incident and pinned its mechanism: the
fallback path's catch-up is real (every Manager-path serving point runs
it), but its authority is the transcript FILE's end — and a live
mid-run daemon holds newer turns in memory, not yet persisted. So a
perfectly caught-up saved view can serve an hours-old turn while the
daemon is minutes fresh, silently. No oracle the fallback can reach
makes it "current"; what was missing is the visibility to SEE that the
served view is the file-end view, next to facts about the live session
that say otherwise.

This is the read-side sibling of the delivery defects the fleet
root-caused (stale quiet re-feeds, fabricated frames), whose delivery
side is owned by the parked reliability PR (5662). A Manager that cannot
trust a transcript read to describe what it actually served builds
backstops against its own tools; every lane in the fleet did exactly
that tonight.

## Current behavior (grounded at origin/main 2be411cd8f)

- **Manager read path and its three serving points.** The hub branch
  (`agent/session_tools_hub.go:705-730`) maps session refs to
  `ThreadTurnsListParams`. The hub serves them
  (`cmd/evener-hub/session_reads.go:56-92`,
  `cmd/evener-hub/app_rpc.go:1376-1429`): live daemon first; on live read
  failure the fallback fires for every non-subscribe read
  (`allowsPastFallbackAfterLiveReadFailure`, `app_rpc.go:903-909`,
  unconditionally true); on a cursorless Manager read whose live answer is
  empty, the saved-index page silently replaces it
  (`cmd/evener-hub/session_tools.go:573-582`, the swap at :594-601); and
  daemonless sessions are served by the index outright. All three produce
  a response shaped exactly like a live answer.
- **The two fallback seams stamp DIFFERENT identity lies.** The
  failed-live seam serves the saved page's own daemonless stamp
  (spike-observed: `BootGeneration` daemonless, `Authoritative` true).
  The succeeded-empty seam is worse:
  `fallbackHistoryIdentity` (`cmd/evener-hub/app_rpc.go:355-369`) merges
  the saved page's items under the LIVE daemon's BootGeneration and
  epoch whenever the live read named one — "the response merges under
  the daemon's generation and epoch rather than replacing as a
  daemonless read would" — so saved content presents under a live
  identity. A provenance field derived from the response's
  BootGeneration is therefore false on exactly this seam; only the
  serving branch knows the truth.
- **`Authoritative` is documented, load-bearing, and misstamped.** The
  field's doc: "true when this response can settle uncertain state (a
  live daemon read), false for a saved-transcript read that cannot"
  (`appwire/types_turn.go:395-398`). Saved serves stamp true. The typed
  clients key held-item replacement on it
  (`appwire-client/typescript/transcriptHistory.ts:830-833,1081-1084`),
  with daemonless behavior pinned in `reducer.history.test.ts:533-567`.
  A bare flip would change client behavior; the resolution must be a
  designed migration, not a stamp change.
- **The index catches up to the transcript file's end — that is its
  ceiling.** `withPastTranscriptIndex` catches the sidecar up to the
  session transcript file's end before serving
  (`cmd/evener-hub/app_threadread.go:373-393`; both fallback paths reach
  it; `CatchUp` itself returns only an error,
  `internal/transcriptindex/index.go:367-382`). The local transcript file
  is the authority the index can reach; a live daemon's in-memory turns
  sit ahead of the file until persisted. The catch-up stats the file
  and indexes to that size
  (`internal/transcriptindex/index.go:367-374`), so entries appended
  DURING the catch-up are not covered: the honest completeness moment
  is the file-stat moment that bounded the catch-up, not the later
  completion moment. `CatchUp` returns only an error today
  (:367-382); exposing the covered length/moment is owned work,
  stamped by the serving path with its own clock — no new stored
  state.
- **The status side is live but not a hub-side oracle, and carries no
  turn id on any shape.** For the Manager's own delegates, `job_status`
  computes from the in-process controller snapshot
  (`agent/session_tools_job_status_stop.go:343-395`); for other refs the
  Manager's `job_status` hub branch issues `ThreadReadParams{Ref}`
  WITHOUT `IncludeTurns` (`agent/session_tools_hub.go:742-746`) — a
  metadata-shaped read that carries no frontier (grounding below).
  Neither shape carries a turn id.
- **The frontier anchor exists on ONE shape and is not computed
  everywhere.** `ThreadReadResponse` carries `LatestExecutionTurnID`
  (`appwire/types_turn.go:372-374`), populated only under `IncludeTurns`
  (`server/appwire_read_handlers.go:233-242`);
  `ThreadTurnsListResponse` does not (:557-571), the live turns handler
  does not populate one (`handleAppThreadTurnsList`,
  `server/appwire_read_handlers.go:406`), the
  conversion to the Manager's shape drops it
  (`cmd/evener-hub/session_reads.go:56-62`), the saved path lacks it
  (`app_threadread.go:216-239`), and `Before` windows compute no anchor
  (`server/history_read.go:75-100`,
  `internal/transcriptindex/window.go:232-251`). The index defines the
  anchor as the latest EXECUTION turn even when later gap/delivery
  turns exist (`window.go:33-35`, pinned by
  `execution_anchor_test.go:20-26`).
- **Typed staleness exists for every class except this one.** Cursor
  mismatch (`TranscriptItemCursorStale`), rebuild refusal
  (`TranscriptIndexRebuilding`, `TranscriptHistoryFailed`), lock
  (`TranscriptIndexLocked`) — and the fallback's catch-up failure maps to
  those typed errors rather than serving
  (`answerPastTranscriptIndexError`, `app_threadread.go:409-455`).
  The saved-index serving points are the one staleness class served
  silently.
- **No remote saved serve exists.** `pastEntryForRead`/`localPastThreadID`
  resolve only local transcript references
  (`app_threadread.go:579-606`); `RemoteThreadCache` is navigation-only.
  A nonlocal ref whose host is unreachable fails the live read and STAYS
  failed — today's mapped error, e.g. `SessionUnavailable`.
- **The served view carries its own instance identity — on the shapes
  that carry a thread row.** The thread row projection stamps
  `EvenerThread.InstanceID` from the serving cut
  (`server/appwire_thread_projection.go:119`), independent of the
  roster's separately-sampled identity (`main_background.go:59-72`,
  which serves the roster and frames, not reads). But the turns/list
  page shapes carry NO instance identity at all: the separate
  `ReadThread` metadata call supplies display fields only (thread
  id, session id, CWD — used for image enrichment,
  `cmd/evener-hub/app_rpc.go:441-465`), and the page's own identity
  stamp covers only BootGeneration/Epoch/Snapshot/Authoritative
  (`HistoryIdentity.StampPage`, `cmd/evener-hub/internal/appsource/source.go:86-92`)
  — nothing on the page names the instance that served it. Making
  every page shape stamp its identity from the serving
  cut is owned wire work (scope below).
- **Overlaps.** PR 5662 owns `cmd/evener-hub/session_observation.go`
  and `docs/job-control.md`; PR 5641 owns the adjacent watch surface. The
  read path is distinct; both rebase under.

## Scope

1. **Serving facts on every Manager-served transcript read of a session
   ref.** Owned here, consumed by specs 1, 2 and 4:
   - `servedFrom`: `live-daemon` | `saved-index`, derived from the
     SERVING BRANCH the hub took — the code path that decided (live
     read served vs past page swapped in vs daemonless serve) — never
     from the response's BootGeneration, which the succeeded-empty
     seam stamps live for saved content (grounding above). A fallback
     serve additionally carries `fallbackReason`: `live-read-failed` |
     `live-read-empty` — facts the serving point knows. A daemonless
     session serves `saved-index` with no fallback reason: that is its
     normal path.
   - `asOf`: the moment the served view was established — for a live
     serve, the read moment; for a saved-index serve, the moment of
     the file stat that bounded this serve's catch-up (the
     covered-length moment, not the later completion moment — entries
     appended during catch-up are not covered; grounding above). The
     serving path stamps its own clock; no new stored state. Never
     absent on a served view. `asOf` bounds file-COMPLETENESS at
     serve time, never content recency: whether the served content is
     current is judged by `frontierTurnId` against the fleet picture —
     the guides state this distinction explicitly.
   - `frontierTurnId`: the newest EXECUTION turn in the served view, in
     the index's pinned anchor semantics — later gap/delivery turns do
     not move it. This is owned machinery, honestly scoped per shape.
     `thread/read` populates it today under `IncludeTurns`; the owned
     work adds it to `turns/list`, cursor pages and the metadata-shaped
     read (the `job_status` hub read) from the same view the items came
     from. For page shapes and saved views the index stores the anchor
     per snapshot. The metadata-shaped read is the one shape with no
     history projection to read: it builds a thread row only
     (`ThreadReadParams{Ref}` without `IncludeTurns`, and
     `LatestExecutionTurnID` is assigned only inside the `IncludeTurns`
     branch — grounding above), so its source is the ROW: the row
     projection gains the latest-execution-turn field as owned work —
     sourced like every other live-session value, from the MATERIALIZED
     ENVELOPE the bridge maintains. For the CHILD rows this
     read copies (`server/appwire_read_handlers.go:350-364`)
     the source is the descendant event route's install —
     grounding below — which records at event time,
     outside the response cut, so the invariant holds
     there unchanged. The projection's own invariant
     forbids the direct session read: "every live-session value comes
     from the materialized envelope ... It must stay that way" — it
     runs under the response cut, and reaching into the session's
     history from there would block the whole event bridge
     (`server/appwire_thread_projection.go:13-19`). The bridge stamps
     the envelope's frontier on the write-then-emit event rows that
     follow an EXECUTION-OPENING entry's persist — the same moments
     it already maintains the envelope's other session facts:
     `EventUserInput` (the user-input execution's own turn is
     recorded when the row fires — durable on the durable
     outcomes, recorded-and-adopted on the retained edge below —
     `agent/session_lifecycle.go:2349-2361`),
     `EventSteeringInjected` (the steering/notification
     execution-opening persist: a steering carrier deliberately
     writes no `TurnUserInput` (`acceptSteeringCarrierInput`'s own
     doc, `agent/session_lifecycle.go:2553-2558`); the queue
     delivery persists `TurnSteering` and then emits the row
     (`agent/session_queue.go:1060-1074` — the same row spec 4's
     ask-facet refresh keys on); and the notification path persists
     its steering BEFORE `s.emit(TurnStartedData)` fires
     (`agent/session_lifecycle.go:2510-2529`)) and
     `EventAssistantTextEnd` (kept: the assistant entry precedes
     the emit — `appendAssistantTurn` before the row
     (`agent/session_model_call.go:1033-1056`), durable on the
     durable outcomes and recorded-and-adopted on the retained
     edge below — so it covers
     model-response-opened turns, tool-call-bearing responses
     included, harmless within a turn), and `EventGoalContinuation`
     — a THIRD execution-opening entry mode the row set's earlier
     form missed. `acceptContinuationInput` opens a goal
     continuation execution (the goal-execution naming begins at
     `agent/session_lifecycle.go:1697-1714`; the function's own
     doc, `:2392-2401`) and TODAY emits its marker at `:2415`
     BEFORE `recordTurn` persists the execution's steering entry
     at `:2416-2419`, with no `SteeringInjected` when no pending
     steering exists (`:2425-2426`) — so a clean goal continuation
     publishes no post-persist row at all, and the frontier would
     name the PREVIOUS execution through the whole provider wait.
     The design MOVES the emission to after the persist —
     write-then-emit, the rule every other frontier row follows —
     preserving the marker's content (the reorder moves timing,
     not content); the facet map's existing `EventGoalContinuation`
     row (`server/thread_envelope.go:300`, goal/queue/ask facets
     today) gains the frontier facet. The RETAINED EDGE, stated
     once for all four rows: the row premise is RECORDED, not
     durable — the row fires when the file holds the whole
     line, and on the synced door's retained outcome the pair
     helper adopts it and returns nil
     (`agent/session.go:2567-2588`), so the row fires on an
     entry that is recorded but not yet durable (the
     user-input emission's own comment asserts a durability
     the outcome breaks, `agent/session_lifecycle.go:2350-2351`
     — today's artifact; this spec is the design). The
     FRONTIER FACET ADVANCES WITH THE ROW: the frontier is the
     newest execution in the served view, and the served view
     is RECORDED — `Recorded` means the whole line is in the
     file (`agent/transcript/record.go:32-34`), the session
     publishes recorded length even for retained lines
     (`agent/session.go:2765-2766`), and the history index's
     anchor names the newest recorded execution — so both
     serving shapes agree through the retained window AND
     through the ordinary buffered window (the goal row's
     first entry is buffered, `agent/session.go:2668`, and
     inside the one-second interval recorded-but-not-durable
     is its ordinary outcome; freezing the metadata frontier
     alone would disagree with the history view — the exact
     defect this spec exists to eliminate). Durability keys
     NOTHING in the signal path: the signal fires at the
     commit — with the row, on the same contract —
     and its facet row refreshes the frontier once,
     idempotently — the signal is the closure backstop
     including for the rows' retained edge, the role this
     row set already assigns it. The envelope's facetAll
     and turn-end resample key on the same recorded anchor —
     no durability-qualified frontier fact exists anywhere in
     this design. The steering
     rows and the assistant row share the premise and the fix;
     the goal row's moved emission shares them too. And the refresh set is
     closed over the ENTRY MODES THAT CAN SUPPLY an execution's
     FIRST PERSISTED ENTRY — the population rule, not a row list
     to assume: the frontier refreshes on rows that follow an
     execution's first entry, so every path that can supply that
     first entry needs a post-persist row. The four rows above
     cover the synchronous paths; the fifth is ASYNCHRONOUS and
     reachable, proved by the round-13 review: an attention-only
     notification wake is admitted with no job notifications and
     no steering (`agent/session_lifecycle.go:2459-2484`), and
     the notification reminder is written only when job
     notifications exist (`:2509-2523` — the code's own comment:
     "the steering-only and attention-only wakes emit no reminder
     at all"), so the notification execution can hold NO
     persisted entry when its first provider call is held; a
     second delegate completing during the hold persists its
     attention through `writer.Record(..., PlaceAsync)` with no
     session event (`agent/session_attention.go:616-642`), and
     `PlaceAsync` joins the RUNNING execution
     (`agent/transcript/placement.go:117-121`) — that attention
     turn becomes the execution's FIRST persisted entry and
     advances the history anchor
     (`internal/transcriptindex/build.go:483-490`), while no
     configured row fires: the metadata frontier names the
     PREVIOUS execution through the rest of the hold and history
     already names the new one. THE CLOSURE IS CENTRALIZED, not
     per-site: the placement layer already computes first-ness —
     `openTurn`'s join stamps `TurnKind` "if it is t's FIRST
     recorded entry" through the fresh flag
     (`agent/transcript/placement.go:73-77`), and the join is
     where the running execution's id and kind are stamped into
     an entry (`:72-84`). The mode list shows the closure is
     over the ENTRY POPULATION, not over modes: `placeAsync` and
     `placeSession` join the running execution when one exists
     (`:117-128`), `placeCompletion` can never be a first entry —
     it skips while the running turn is still fresh
     (`:104-108`) — and the verbatim and named-turn placements
     never join a running execution (`:102-103`, `:112-116`); an
     entry that reached the running turn outside the join would
     carry an empty `TurnKind`, a layer-contract violation the
     layer PERMITS today through the named-turn placement, so
     the boundary the signal inherits is the join plus the
     caller survey below — a contract held by named callers,
     not an invariant the layer enforces. The design: the fresh-join
     fact is already ON THE ENTRY — the join stamps the open
     turn's span kind on its first recorded entry
     (`agent/transcript/placement.go:72-77`); the running
     execution's first entry carries `TurnSpanExecution`
     (`:117-128`), a prelude or gap first carries its own kind
     (`TurnSpanPrelude`, `TurnSpanGap`, `:129-139`), so the
     boundary needs no side channel — and the session emits ONE
     signal only when the RUNNING execution's first entry
     lands: a new event, `EventExecutionFirstRecorded`,
     with one row in the facet map carrying the frontier facet.
     THE EMISSION AND THE FRONTIER STAMP BOTH LIVE AT THE
     COMMIT, inside the write — not at the producer after
     `Record` returns. `commitBatchLocked`
     (`agent/transcript/transcript.go:854-866`) takes the
     ordinal, advances the recorded length and fires the file's
     recorded-entry hook at `:863-864`, and the hook sees EVERY
     recorded entry of EVERY writer on the file
     (`agent/transcript/record.go:109-118`) — the four
     synchronous rows, the compaction path, the attention
     content site (`agent/session_attention.go:616-642`), the
     free resolution helper (`:1924-1944`, which holds no
     `*Session` and needs none: its records flow through the
     file's hook like every other writer's), and the stop
     stabilization write (`:1891-1893`), with NO producer
     signature change anywhere. The commit is also the earliest
     moment the served view learns the entry: the history hook
     (`server/thread_history.go:254-285`) stages the record and
     wakes its projector under the same append lock at the same
     moment, and the index anchors on the recorded length — so
     a signal placed any later is a window in which history
     names the new execution while metadata still names the
     previous one. On the synced door that window is UNBOUNDED,
     not a dequeue delay: a retained line (the append's own
     fsync failed and the rollback truncate failed) is committed
     and projected, and THEN `recordSynced` calls
     `EstablishDurability` before returning the outcome
     (`agent/transcript/record.go:83-95`, the barrier's
     `file.Sync()` with no deadline at
     `agent/transcript/transcript.go:881-883`) — so the round-20
     review proved a producer-side outcome handler cannot emit
     while the barrier blocks, and the round-19 placement of the
     emission at the producer's returned outcome is WITHDRAWN
     as one window too late. The mechanism: the session
     installs ONE dispatcher into the file's hook slot
     (`agent/transcript/record.go:109-125` — a single-slot
     REPLACING setter) — the dispatcher fans out to the
     internal frontier listener and the externally-set
     callback, so the history projector the daemon wires
     (`SetTranscriptRecordedFunc`,
     `server/appwire_histories.go:49-59`) survives every
     setter call, attachment, reopen, and descendant wiring
     (`agent/session_init.go:505`): the external half keeps
     the setter's replace semantics, the internal half is
     never replaced. The SETTER is the fourth writer of
     the slot surface, and it recomposes rather than
     replaces: `SetTranscriptRecordedFunc`
     (`agent/session_execution.go:241-246`) today writes
     the field AND calls `s.transcript.OnRecorded(fn)`
     directly — a plain replacement of the hook slot —
     and production wires the history callback AFTER
     construction on every daemon start
     (`cmd/evener/serve.go:727`, `:758` build, `:819`
     wires, `:1152` rewires on replacement), so the
     dispatcher must survive that call or the internal
     listener is uninstalled at every daemon start until
     a reopen. The fixed setter writes the field, then
     installs the dispatcher through the compose path —
     the same install `setTranscriptLocked` performs,
     reading the field's current value as the external
     half — instead of calling `OnRecorded(fn)`
     directly; that is the mechanism behind "survives
     every setter call." The install site is `setTranscriptLocked`
     (`agent/session_execution.go:248-258`): it composes the
     dispatcher on EVERY attach, reading the field's CURRENT
     value as the external half at that moment — so the three
     writers that set `s.transcriptRecorded` directly, never
     through the setter (`agent/session_init.go:505`,
     `:1144`, and `bindDescendantTranscriptPublication` at
     `agent/session_conversation_provenance.go:226-230`),
     compose too: what they leave in the field becomes the
     external half at the next install rather than a bare
     callback that would leave the internal listener
     uninstalled. The listener runs under the hook's leaf
     contract (`:113-118` — no appends, no lock an appender
     may hold), so it takes ONLY its own mutex, stamps the
     frontier field with the committed first entry's turn id,
     and queues the refresh event — then wakes the session's
     emitter, which runs OUTSIDE the append lock on its own
     goroutine, RELEASES the listener's leaf before
     enqueueing (`s.emit` takes `Session.mu` through
     `activeCausalProvenance`,
     `agent/session_provenance.go:8-12`; the history drain's
     release-before-project is the precedent,
     `server/thread_history.go:525-546`), and enqueues the
     event into the session stream — the same queue-and-wake
     shape the history hook already uses at the same moment
     under the same constraint; ONE emitter goroutine
     starts at the FIRST dispatcher install — installs
     are idempotent for the goroutine, so the compose
     path's re-runs on setter calls and reattaches
     never start a second — and it stops at session
     close by joining the same boundary the history
     drain joins (`closeEventStream` joins `sendersWG`
     before closing the channel,
     `agent/session_close.go:521-542`). The join and
     the queued refresh's delivery are best-effort
     WITHIN THE SHARED CLOSE BUDGET:
     `joinWithinCloseBudget` gives up when the budget
     expires and the straggler's remaining events are
     dropped (`agent/session_close.go:70-97` — "a
     nicety, not a safety property"; the round-25
     drive measured `closeReturnedBeforeEmitter=true`,
     `queuedRefreshDelivered=0`). Journey 8's delayed
     delivery holds its callback after the sample (the
     proof's shape) and never rides this drain. The
     refresh is therefore IN FLIGHT before any durability
     work — during a blocked recovery `Sync` the bridge can
     already process it, and the remaining lag is ordinary
     dequeue processing. The signal fires ONCE per execution,
     on the listener's PREDICATE — the session's own
     bookkeeping rule, already in code at
     `noteRecordedLocked` (`agent/session_execution.go:129-139`):
     stamp and signal only when `rec.Recorded` AND
     `rec.Turn.OriginalOrdinal == nil` AND
     `rec.Turn.TurnKind == TurnSpanExecution`. The
     `OriginalOrdinal` exclusion is load-bearing: a
     compaction fold's verbatim replay re-records retained
     entries with their ORIGINAL kind stamps
     (`agent/session_compaction.go:417-476` copies the
     persisted turn and adds only `OriginalOrdinal`;
     `PlaceVerbatim` stamps nothing,
     `agent/transcript/placement.go:102-103`), so a
     kind-only filter would re-fire and regress the field to
     the copied execution while the index skips the copies
     (`internal/transcriptindex/build.go:154-161`) — the
     layer's own rule states why
     (`agent/transcript/turn_status.go:5-12`). The hook fires
     once per recorded entry in ordinal order, so the second
     entry does not re-fire and a mode row's own facet refresh
     samples the same stamped field idempotently. ANY door, with
     or without an error: the commit runs on the retained path
     and the clean path alike (`agent/transcript/transcript.go:796` follows the
     retained return at `:842-846`), `Recorded` means the whole
     line is in the file (`agent/transcript/record.go:32-34`),
     the same fact the served view's anchor keys on, and the
     caller's adoption of a retained error — the pair helper's
     documented contract (`agent/session.go:2567-2588`) —
     changes nothing about the signal.
     THE FIELD IS SEEDED AT ATTACH: a restored session has
     recorded executions but no in-process commit — the
     reopen entry carries no execution kind
     (`agent/session_execution.go:76-79`), crashed completions
     write empty `TurnKind` (`:346-353`), and the initial
     envelope sample runs before the bridge
     (`cmd/evener/serve.go:1199-1204`) — so at writer attach
     (the `attachTranscript` seam,
     `agent/session.go:2873-2895`) the session seeds the field
     from the validated existing view with the INDEX's anchor
     semantics: the newest execution among existing entries,
     under the same `OriginalOrdinal` exclusion. The seed's
     data path has TWO inputs, one per constructor: the
     Writer exposes no decoded-entries accessor
     (its surface is the append doors, `Header`, the
     durability barriers and the diagnostics), and the
     validated `transcriptEntries` exists only in the
     RESTORE function (`agent/session_init.go:1015`,
     filled `:1023`, in scope at the restored attach at
     `:1427`) — the fresh attach at `:696` sits in
     `NewSession`, which holds no such local, so the
     single-input form fails to compile there. The
     contract: FRESH construction passes nil (a fresh
     transcript has no prior executions of its own —
     the held boundary turn at `:556` and the inherited
     context at `:690` record via `PlaceSession`, which
     re-stamps kinds,
     `agent/transcript/placement.go:72-84`), RESTORE passes
     its validated entries, and reopens
     (`agent/session_attention.go:1836`, `:1911`)
     reinstall the dispatcher without reseeding — the
     field is already populated. Each constructor passes
     its input INTO the attach seam, and the seed runs
     under the same only-if-empty guard. The seed is
     serialized with the listener under the listener's mutex
     and writes ONLY when the field is empty and no committed
     fact is queued — the guard is what the journey pins, by
     pre-stamping the field or unit-calling the seed: no
     real schedule can race a commit against the seed at
     attach (the only attach-time commits are the held-turn
     flushes, which join the PRELUDE —
     `agent/session.go:2882-2886`,
     `agent/transcript/append_tail.go:118-125` — and
     `BeginExecution` is a nil-writer no-op before attach),
     so an older seed can never overwrite a stamp. The seed
     emits no event: the
     pre-bridge sample and `SessionStart`'s resample read it
     like any stamped value.
     The row set and the
     signal now share ONE contract — commit-then-emit — so
     every first recorded entry refreshes the frontier on
     every path — the async attention and compaction cases
     included, and the descendant route's cached child rows
     included through the consumer the mechanism chapter
     names — with no path left holding the metadata at the
     previous execution while the
     index names the new one. The post-durability requirement this design
     carried since round 14 is WITHDRAWN as the wrong invariant: it
     contradicted the served view's recorded semantics and manufactured an
     escape every round — the held id, the discharge enumeration, and the
     clear-without-emit class built across rounds 16-18 are withdrawn with
     it. Durability is the WRITER's own documented debt — the pair helper's
     adoption contract ("the turn is adopted here (never re-appended, which
     would duplicate it on restart) and the next fsync settles the debt")
     with Close the terminal settlement — and the doors table remains the
     writer's accounting: DOORSYNCED proves the whole file (the barrier)
     when its outcome carries no error — a retained synced line whose
     barrier also failed returns `Recorded` WITH
     `*RetainedUnsyncedError` (`agent/transcript/record.go:83-95`),
     its proof pending; DOORDURABLE proves it when its own
     fsync SUCCEEDS and DOORBUFFERED when the interval's
     attempt SUCCEEDS — an elapsed or zero interval causes the
     attempt, it is not proof (`agent/transcript/transcript.go:781-788`),
     and both doors' RETAINED-UNSYNCED face is the same: a
     whole line whose sync failed is recorded as dirty debt,
     the door returns nil ("recorded-or-error is their whole
     contract", `:589-603`), the diagnostic reported once
     through DrainWarnings — proof pending like the synced
     door's; CLOSE settles dirty debt with no
     Record outcome — with the table's failure faces named: a clean
     ROLLBACK truncates and fsyncs after a failed force-synced append
     (`:790-817`, `:1000-1030`), settling earlier debt while the caller
     sees only the hard error; a NON-FINAL Close (the deferred
     stabilization close, `agent/session_attention.go:1871-1879`, the
     old-writer close after a reopen, `:1839`) settles debt mid-session;
     and two mid-session barriers settle the whole file outside any
     Record outcome — the terminal-job-notification round
     (`agent/session_tool_round.go:377`) and retirement preparation
     (`agent/session_retirement_prepare.go:238`), both
     `EstablishDurability`, both changing behavior on failure.
     These are the writer's own settlements; no manager-visible signal keys
     on them. The recorded view is the honest one — the file holds the
     whole line, and an OS-crash loss is the writer's documented risk for
     every buffered write, stated as the accepted cost, matching today's
     behavior. The read-back and mismatch paths keep TODAY's
     content-verification behavior, with NO signal interaction: the failed
     read-back removes the unverified turn from memory
     (`removeUnverifiedDelegateAttentionTurn`, `:1604-1625`, "A deletion,
     not an append"), the retry re-derives through the fold's
     existing-content branch after durable verification (`:600-614`), and a
     genuinely conflicting second entry fails the fold read typed ("has
     conflicting content", `:237-240`) — the entry itself stays recorded
     and the signal it already fired stays fired, because the signal
     reports the RECORD and the verification reports the CONTENT.
     The
     population claim is then closed by a PRODUCER SWEEP of
     every writer that can record into a running execution
     — the join is where the running execution's id and
     kind are stamped, and every writer that can supply its
     first persisted entry either funnels through the join
     or is named and surveyed: the four synchronous rows
     above; asynchronous attention's content site
     (`PlaceAsync` joining the running execution, the site at
     `agent/session_attention.go:616-642`); the attention
     RESOLUTION writers, live producers by the resolver's
     own comment — delivery and stop goroutines reach the
     running execution's turn "at any time"
     (`agent/session_attention.go:1743-1784`, the
     delegate-tree stop cleanup the live trigger,
     `agent/delegate_tree_restore.go:719-755`) — writing
     through the free helper and the stabilization site the
     emission list above already owns; and the compaction
     path the round-14 review proved: an attention-only
     notification whose request preparation runs automatic
     compaction persists its checkpoint turns through
     `PlaceSession` (`agent/session_model_call.go:476-500` →
     `agent/session_compaction.go:932-946` →
     `agent/session.go:2732-2734`), which JOINS the running
     execution as its first entry
     (`agent/transcript/placement.go:125-128`), while the
     compaction events that fire afterward
     (`agent/session_compaction.go:972-978`) carry
     context/meta facets only
     (`server/thread_envelope.go:288-289`) — no configured
     row refreshes the frontier on that path today. The
     verbatim and named-turn placements are excluded, each
     way scoped to the class it actually holds for. The
     compaction replay's copies are excluded TWO ways: they
     never join and never flip the fresh fact (the CALLER
     SURVEY the seats re-verify each round —
     `placeVerbatim`'s compaction caller replays
     already-recorded turns,
     `agent/session_compaction.go:369-393`), and the
     listener's `OriginalOrdinal` PREDICATE excludes them on
     the record itself
     (`agent/session_compaction.go:467-472` stamps the
     ordinal; the copies otherwise keep their original kind
     stamps) — for that class the predicate, not the survey,
     is what the hook-side filter keys on. The survey ALONE
     guards the verbatim class the predicate CANNOT see,
     nil-`OriginalOrdinal` copies: the fork's prefix copy
     records parent entries through `PlaceVerbatim` with
     `OriginalOrdinal` nil and kinds preserved
     (`agent/fork.go:267-272`) — such a copy WOULD satisfy
     the predicate — and it is inert today only because
     `agent/fork.go:249-293` builds the child transcript on a
     bare, unhooked writer; the survey-maintained
     invariant, that no `placeVerbatim` production caller
     that records NIL-`OriginalOrdinal` copies runs under
     a live session hook, is what keeps a future caller
     from regressing the frontier — scoped to that copy
     class because the compaction callers DO run
     `PlaceVerbatim` under the live hook
     (`agent/session_compaction.go:371`, `:390`),
     excluded only by the predicate's `OriginalOrdinal`
     check (the predicate at
     `agent/session_execution.go:131-139`; the copies'
     ordinal stamped at
     `agent/session_compaction.go:467-472`).
     `PlaceInTurn`'s one production
     caller completes an already-recorded abandoned
     execution (`agent/session_execution.go:347-353`),
     reached only while the session is idle (the restore
     crash and abandoned paths, and the was-open branch of
     `recoverClientMutationFailures`,
     `agent/session_client_mutation_queue.go:1621-1623`);
     attach-time flushing (`agent/session.go:2883`) and
     inherited-context recording
     (`agent/session_init.go:690`) run before any execution
     exists. None can supply a fresh running execution's
     first entry — and any future entry mode that records
     into a running execution inherits the closure: it joins
     (`:72-84`).
     The signal's PAYLOAD is the first entry's stable turn id —
     provenance only, never a frontier claim: a placed attention
     entry may be a delivery turn, and the payload carries the
     placed entry's own id. The frontier VALUE is fixed at the
     commit and read at sample time — one mechanism, no
     disagreement between the moments: the listener stamps the
     frontier field at the commit (the persist moment), and the
     sampler READS the stamped field; it never derives the
     frontier from a source that lags the commit. `s.history`
     and `s.execution` are NON-sources, each for a verified
     reason: the history insert lags the record
     (`agent/session_attention.go:621-627`; the pair helper's
     `appendLocked` at `agent/session.go:2584` after `write()` at
     `:2580`), so a dequeue racing the retain would sample the
     previous execution; and the execution state clears at
     completion BEFORE the completion entry
     (`agent/session_execution.go:89-93`;
     `agent/transcript/placement.go:104-111`) while `TURN_ENDED`
     resamples every facet (`server/thread_envelope.go:238`),
     so an execution-sourced frontier would blank at every turn
     end. The sampler takes `Session.mu` and then the field's
     own leaf lock, in that order, never `attentionMu` — and
     THE RULE IS AMENDED, not merely cited: "A method on this
     interface may take Session.mu. It may take no other lock
     that lives in package agent"
     (`agent/session_envelope_sampling.go:22-26`) gains the
     frontier field's leaf as its second allowed lock, in the
     same change that adds the frontier method, with the
     no-cycle argument in the rule's own terms — the listener
     takes only the leaf and never `Session.mu`, so a sampler
     holding `Session.mu` then the leaf cannot cycle — and the
     interface's proof test gains the new method plus the
     leaf-safety statement. The listener runs under the append
     lock (under `attentionMu` too at the attention site),
     which is safe precisely because the sampler takes
     neither.
     REGISTRATION is owned additive work against the sealed
     set: the payload type with its `eventKind()` marker, the
     kind constant, the payload→kind table entry and the
     compile-time assertion
     (`agent/events/eventdata.go:5-24`,
     `agent/events/events.go:15-180` — the stream is public to
     projectors, CLI renderers and the bridge). The consumer
     disposition: job-watch fanout
     (`agent/session_events.go:249-255`) and the bridge
     projector (`server/bridge.go:116-134`) see the kind;
     wildcard watches accept only their supported subset
     (`agent/watch_fire.go:150-155`) and the activity meter's
     whitelist ignores other kinds
     (`server/activity_meter.go:93-107`) — default-ignore, no
     new consumer obligation EXCEPT the descendant route,
     the one consumer that OWES an install. Descendants
     have no daemon or authoritative event consumer of
     their own: their events forward through
     `descendantEvent` (`agent/session_events.go:505-506`)
     to `RecordDescendantAppEvent`
     (`cmd/evener/serve_bridge.go:191-204`), and that
     route's own doc states it "deliberately does not
     sample or stamp the root session envelope" and that
     `refreshFacets` "samples and installs only the root
     envelope" (`server/appwire_record_events.go:365-373`)
     — the root's facet machinery never runs there, and
     the child projector returns nil for the first-record
     kind today
     (`internal/appprojector/appwire_projection.go:435-436`),
     so the child's CACHED ROW never receives the frontier
     while the child's own field and full-history read
     name the execution. The consumer: on the typed
     first-record kind the route samples the CHILD
     session's stamped field and installs the sampled
     value on the cached child row, BOTH steps OUTSIDE
     the projection gate. The round-22 pass's
     install-point claim is WITHDRAWN: the
     `ThreadStartedParams` arm it named
     (`server/appwire_record_events.go:428-445`) lives
     INSIDE the route's `CommitProjection` closure
     (`:386-387`), and `CommitProjection` holds
     `projectionMu` across the commit
     (`internal/appserver/server.go:173-180`) — a
     child-field sample taken there would be a session
     call under the projection gate, exactly the
     zero-calls invariant `server/thread_envelope.go:15-21`
     pins (asserted at
     `server/thread_envelope_test.go:679-681`, "want 0").
     The child is RESOLVED through construction-bound
     sampler closures, not a live-tree walk: a binder
     propagated beside `spawn.descendantEvent` and the
     child's own accessor registered at attach, looked
     up by the event's `SessionID` — `subagent.mu` at
     `agent/jobs_nested.go:194-216` is not a permitted
     sampler lock. The whole first-record arm runs
     AFTER the `CommitProjection` call returns: the
     route's other event kinds commit their row changes
     inside the closure, but on the typed first-record
     kind the route completes the call FIRST and does
     its sample and install outside it — otherwise
     `projectionMu`, held across the entire closure
     (`internal/appserver/server.go:174-181`), would
     put the sample back under the gate the withdrawal
     just exited. The route RELEASES `Server.mu`,
     samples the child's `Session.mu` then the frontier
     field's leaf, re-reads the row under the
     re-acquired `s.mu`, rechecks the owner identity,
     and writes `projection.thread` as a plain row
     write — no commit arm, nothing under the gate.
     The lock order
     stands in its own terms: the sampler takes only
     the child's two locks, no server lock is held
     across the sample, the child's own commit holds
     the append lock plus the leaf — no cycle. The
     amended sampling rule is INAPPLICABLE to this
     path, not load-bearing on it: its own doc scopes
     sampling to the drain goroutine
     (`agent/session_envelope_sampling.go:19-20`),
     while the descendant route runs synchronously on
     the child's emitter goroutine
     (`agent/session_events.go:501-507`); the rule's
     proof tests cover the child accessor once it is
     added. The install is FENCED by the child's
     runtime incarnation: the reclamation path unhooks
     an idle delegate's runtime BEFORE teardown and
     accepts a racing send that cold-restores a
     replacement reusing the SAME child session ID
     (`agent/delegate_tree_reclaim.go:656-679` — the
     unhook-before-teardown order is the path's own
     documented contract); `SessionEvent` carries no
     session handle
     (`agent/events/events.go:186-192`), the route
     fences only the ROOT identity
     (`server/appwire_record_events.go:386-390`), the
     cached child row's `InstanceID` is the OWNER's
     (`:409-411`), and `SessionStartData` carries no
     child-incarnation token
     (`agent/events/payloads.go:18-54`) — so without a
     fence a delayed old-runtime callback regresses the
     row: the old runtime queues frontier A, idle
     release unhooks it, a cold restore advances the
     row to frontier B, and the delayed A write passes
     the root-only fence. The fence is a SERVER-OWNED
     INCARNATION REGISTRY, not a value bound at
     registration: the registry maps each child session
     ID to one immutable entry `{SessionID, boot
     generation, sampler, registration identity}`.
     Attach registers the sampler with the entry's
     generation EMPTY — the constructors register at
     attach (fresh `agent/session_init.go:696`,
     restored `:1427`) and return at `:721`/`:1594`,
     while spec 4's boot-generation initialization
     mints only AFTER construction returns (the
     owners sit at `agent/subagents.go:890` and the
     cold restore at
     `agent/delegate_restore_idle.go:161-165`), so a
     registration-time capture would bind the unset
     value. Instead the owner PUBLISHES the minted
     generation into the current registry entry before
     any child work — the publish closure PRESENTS its
     held registration identity and a mismatch is a
     no-op, so a publish can never complete a
     replacement's entry (publication COMPLETES the
     entry atomically under `Server.mu`, and the entry is
     immutable once complete; a later attach (a
     replacement's registration) REPLACES the map's
     entry with a new one rather than mutating any
     carried reference, which is the property that
     makes carried-vs-current meaningful. The
     attach-before-mint interval needs no ordering
     argument, because constructor-window writes are
     CURRENT-runtime writes: the restored constructor
     CAN record a first execution entry before it
     returns — `recoverClientMutationFailures` runs
     inside the constructor
     (`agent/session_init.go:1472`), begins its own
     execution
     (`agent/session_client_mutation_queue.go:1629`)
     and persists the recovered input through the
     hooked writer (`:1682-1715`), all before the
     return at `:1594` — and such a write carries the
     entry its own runtime just registered:
     carried == current, the same identity, and it
     proceeds legitimately (it samples the current
     field and installs the current frontier). Every
     stale shape mismatches on identity at every
     moment, so the fence needs no interval argument
     at all; the mint and publication complete the
     entry when the owner takes possession after the
     constructor returns, and a FAILED construction
     releases the incomplete entry through a
     CONSTRUCTOR-LOCAL conditional release defer
     armed at registration and disarmed when the
     completion flag flips at constructor success
     (the caller adopts only after, so the
     pre-adoption funnels below own the post-success
     gap): the existing
     `!initComplete`/`!restoreComplete` failure-defer
     conditions are the home (`agent/session_init.go:448-474`
     fresh, `:903-1036` restored — the defers already
     own the incomplete session when a post-attach
     step fails), while the previously named callers
     cannot release anything — the restore caller's
     error branch at
     `agent/delegate_restore_idle.go:161-164` returns
     nil with no child, and the spawn caller at
     `agent/subagents.go:887-903` receives nil and
     disposes only environment scratch. The
     constructors' own post-attach error returns
     (fresh `agent/session_init.go:697-709`, restored
     `:1448-1522`) and the pre-adoption funnels —
     `discardRestoredCandidate`
     (`agent/session_lifecycle.go:369`, the
     restore-side funnel for the validation discards
     at `agent/delegate_restore_idle.go:167-215` and
     the owed-bootstrap caller
     `agent/delegate_owed_bootstrap.go:194`) and
     `disposeUnadoptedSubagentSession`
     (`agent/subagents.go:322-350`, the create-path
     twin, via `disposeUnadopted` at `:900-926`) —
     carry the same conditional release. The
     ADOPTED runtime's own teardown is the writer
     the set was still missing: every ordinary
     teardown path funnels through
     `teardownChildSessionWithPolicy`
     (`agent/subagents.go:205-236` — the root-close
     cascade `agent/session_close.go:172-176` →
     `closeRuntimeTree` → `teardownChildSession`,
     retirement's non-terminal policy, an explicit
     child close, and `manage_worktree dispose`'s
     `disposeStableExecute`
     `agent/session_tools_worktree_dispose.go:227-232`),
     which today reaches only `releaseRuntime` and
     environment cleanup — so an identity-conditional
     release presenting the Session's retained
     closure runs there IDEMPOTENTLY before the
     runtime release. Without it the entry stays
     current across the teardown (the round-27 drive
     measured `entriesAfterRootClose=1` while the
     descendant rows went to zero), the retained
     sampler pins the closed Session, and a
     first-record callback already delivered to the
     route before the stream close still matches
     its CURRENT registration and installs on the
     torn-down child's row — the token check cannot
     reject it, because the check drops only STALE
     identities. The funnel release and the
     reclamation unhook are the same idempotent
     operation in both orders;
     A-release-never-removes-B holds. Without the
     constructor-local release the failed runtime's
     queued first-record callback still installs
     after the nil return — the failure defer never
     closes the session, so the open gate at
     `agent/session_events.go:505-506` does not stop
     it; the round-26 caller-only drive measured the
     install (`postReturnInstallDelta=1`, `drops=0`)
     and the defer closing it (`0`, `drops=1`) —
     bounded by any later replacement (identity
     still fences), real until then. The token is the
     same fsynced, never-reused per-session generation
     spec 4's design mints on every NewSession caller
     (`schema.NextBootGeneration`,
     `agent/schema/boot_generation.go:25-37`;
     `4-question-surfacing.md:279-293`) — never the
     root-derived descendant string, which is
     identical across a replacement
     (`appwire/boot_generation.go:19-25`,
     `server/appwire_histories.go:143-168`). The
     registration identity is minted at REGISTRATION —
     a `Server.mu`-guarded counter at attach, unique
     per registration by construction, its form
     otherwise opaque — and stored in the entry. The
     registration is ADDITIVE OWNED WORK extending
     the existing per-descendant binder's
     DISCIPLINE, not a mechanism that factory
     already performs: `TranscriptPublicationHooks`
     carries only `Recorded` and `Published`
     (`agent/session_conversation_provenance.go:209-215`),
     binds `fn(s.id)` keyed by the durable session
     id (`:225-230`), and is invoked at
     `agent/session_init.go:530`/`:1189` BEFORE
     attach, which `attachTranscript`
     (`agent/session.go:2867-2901`) never invokes;
     the factory's per-invocation-fresh comment
     ("Each invocation binds fresh hooks, so a
     retired producer cannot publish into its
     replacement") is the precedent the registration
     extends. The extended factory returns register,
     publish and release closures beside the two
     hooks: the Server-side register closure mints
     the identity and installs the sampler
     generation-empty at attach (`:696`/`:1427`),
     and a NIL closure set means NO registration
     and NO release — the register step skips nil,
     so the superseded-root zero-value hooks
     (`server/appwire_histories.go:56-57`) and any
     partial wrap stay coherent — and the row
     consequence follows from the generic MISS rule
     below: a superseded root's descendants register
     nothing, so their first-record installs take the
     lookup MISS and drop (dead projections, matching
     today's replaced-tree contract at
     `server/appwire_histories.go:72-82`), and the
     publish closure's absent-entry arm is a no-op;
     the closures are
     RETAINED on the constructing
     Session, and every release site presents the
     RUNTIME-HELD identity through that retained
     release closure — never a `childSessionID`
     lookup, which would select the replacement's
     entry. The round-26 prototype drove exactly
     this shape green: the replacement and no-mint
     drops, and A's release never removed B.
     The route
     resolves the event's registry entry ONCE at the
     start of its processing and CARRIES it across the
     sample; the outside-gate row write compares the
     CARRIED entry against the registry's CURRENT
     entry under `Server.mu` — and the registration
     identity is THE FENCE: a mismatch = DROP,
     unconditionally, holding with no generation at
     all (the round-25 drive dropped the no-mint
     replacement on identity alone); the generation is
     the DURABLE CORROBORATION, compared when present,
     never the sole gate, never current-vs-current.
     Release removes an entry by its registration
     identity only — the identity the released
     runtime holds through its retained release
     closure, the carrier the reclamation entry
     itself lacks (`delegateRuntimeReclamationEntry`
     holds `{delegateID, childSessionID, runtime,
     ownerRuntime, depth}`,
     `agent/delegate_tree_reclaim.go:29-40`, its
     builder `:308-320`; the runtime pointer is the
     transport) — landing at the two sites that own
     a runtime's end: `unhookReclaimedRuntimeRecord`
     (`agent/delegate_tree_reclaim.go:435-443`, the
     seam both reclamation paths already share —
     called at `:466` capacity, `:673` idle) and the
     ordinary-teardown funnel release above, the
     SAME idempotent operation in both orders, so
     the old runtime's teardown cannot unregister
     its replacement and no path removes an entry
     twice. A lookup MISS — the registry holds
     no entry for the event's session — drops the
     event's install. Absent-when-missing (README
     rule 2/6) is a MISS DISTINCTION, not an unfenced
     proceed: spec 4's field not landed means the
     entry's generation stays EMPTY and the identity
     check still drops every stale write — spec 3's
     lane closes the replacement window ON ITS OWN,
     and the absent field loses only the durable token
     form. There is no unfenced proceed; the earlier
     "known cost" and "pinned by the no-mint red"
     sentences are WITHDRAWN. Draining the emitter
     before the runtime unhook is REJECTED as a design
     change to the reclamation path's bounded-release
     contract — its own comment accepts the callback
     race rather than joining unbounded callback
     sources, and the token check inside the consumer
     respects that contract BECAUSE both release
     sites run before the teardown they race: the
     reclamation unhook before its teardown, the
     ordinary-teardown funnel release before the
     runtime release — so a callback that already
     carries the released runtime's entry fails the
     comparison and drops, a callback that resolves
     in the emptied interval meets a MISS and drops,
     and a callback that begins route processing
     only after a replacement's fresh registration
     resolves and carries that replacement's entry,
     sampling and installing only the replacement's
     own frontier (the round-28 before-route drive
     measured the resample: the old callback
     installed the replacement's frontier,
     `regression=false`). The guaranteed property
     is that a callback already carrying a released
     runtime's entry can never install it — not
     that every late callback drops. The install is deliberately
     NOTIFICATION-LESS: no commit arm means no sequenced
     notification for the row change. The bounded-loss
     analogy is WITHDRAWN: the facet machinery it cited
     is ROOT-ONLY, the descendant projector emits
     field-scoped notifications with the whole thread
     riding only `ThreadStarted`
     (`internal/appprojector/appwire_projection.go:81-183`,
     `:435-436`), and the async-attention case fires no
     mode row — so a live subscriber's cached child row
     keeps the old frontier until a full metadata read
     or the child's next `ThreadStarted` (a
     replacement): potentially INDEFINITE, a known
     cost the round-25 drive measured (the hydrated
     subscriber's frontier stayed stale,
     `newFrontierNotifications=0`, and a subsequent
     routed status event carried no frontier). The
     doctrine is cited as the DEVIATION it is
     (`server/thread_envelope.go:31-40`: the
     notification stream carries the truth — the
     carrier this install lacks); the metadata read
     always carries the truth
     (`server/appwire_read_handlers.go:350-365`). The
     route's no-race
     invariant is amended in step: the install samples the
     CHILD's field, never the root envelope, so the root's
     no-race argument is untouched. The four mode rows
     above KEEP
     their frontier facets: a first entry that also fires a
     mode row refreshes the frontier twice, and both
     refreshes sample the same source idempotently — the
     signal is the closure backstop, not the rows'
     replacement. Journey 8's held-attention case (below) is
     the attention-path red, now asserting the signal, its
     held-compaction sibling (below) is the closure red, and
     its failed-read-back sibling (below) is the one-shot
     survival red. This
     event is the bridge's refresh row, not an ask-delivery or
     resolution event: spec 4's no-new-event-kind rule governs
     the question-surfacing channel and is untouched. The four
     mode rows already map to facets in `facetsByEvent`
     (`server/thread_envelope.go:222`; `EventSteeringInjected` at
     :266, `EventAssistantTextEnd` at :286, `EventGoalContinuation`
     at :300); the signal's row is new and lands in the map with
     the frontier facet. `EventToolCallEnd` is
     deliberately NOT a frontier row: it is the one row whose
     write-then-emit ordering does NOT hold — the facet map's own
     comment documents that its result persistence occurs afterward
     (`server/thread_envelope.go:271-278`) — so stamping there
     would name a frontier whose evidence is not yet durable. The
     map's
     `EventTurnStarted`→`facetWork` row (`:265`) is deliberately NOT
     the frontier's row either, and the reason is precise: it is a
     START row, not a persist row. A user-input execution precedes
     the first persisted entry, so stamping there would name the
     PREVIOUS frontier mid-turn — against this spec's own
     in-flight-window grounding; but a notification execution
     ALREADY holds a persisted steering entry when `TurnStarted`
     fires (the notification path's own ordering above), so the
     start moment is not even a reliable pre-persist marker — the
     frontier keys on PERSIST moments only, never start moments. The
     projection copies the stamped frontier onto the row
     with the rest. The landing zones are named, because neither
     carries a frontier today: the envelope's event→facet map has no
     frontier facet on those post-persist rows, and the bridge's
     sampling
     interface has no frontier method (`ThreadEnvelopeSource`, `:144`,
     implemented over the session as `agent.EnvelopeSampling`) — the
     interface's own doc states that adding a method is a concurrency
     decision governed by the sampling rule on the agent side, so the
     frontier method is declared beside that rule, reading the
     field the listener stamps at the commit (the persist moment,
     the mechanism above), and the new facet row carries it to the
     projection. The metadata read names the frontier from the
     row it already builds — no new query, contract 4's cost bound
     intact, and no session read under the response cut.
     The CHILD rows the same metadata read copies
     (`server/appwire_read_handlers.go:350-364`) gain the
     frontier on the descendant route — the consumer
     disposition above — and a RESTORED child gains it at
     its own `SessionStart` row construction
     (`internal/appprojector/appwire_projection.go:87-123` —
     the projector is a pure event consumer, reading
     `event.Data` at the `SessionStart` arm and returning
     nil for kinds it does not know, so it cannot read the
     child session's field): the child's ATTACH-SEEDED value
     rides the `SessionStart` payload as an additive field —
     the same payload discipline the first-record kind itself
     uses — and the row construction installs it from the
     payload, never a session read: a root `facetAll` resample
     does not touch descendants, so the child's own seed is
     the restored child's only bootstrap.
     A page names its VIEW's frontier, not the page's newest
     item. The wire field is the EXISTING name:
     `LatestExecutionTurnID`/`latestExecutionTurnId`
     (`appwire/types_turn.go:372-374`), reused on every shape;
     `frontierTurnId` is this spec's concept term for it.
   - `instanceId`: the instance that served this read, stamped from
     the serving cut on EVERY shape — not the roster's
     independently-sampled identity. On shapes that carry a thread row
     it is the row's own `InstanceID` (grounding above); on the page
     shapes, which carry no row, the serving source stamps it from the
     same cut (owned wire work, grounding above) — the separate
     metadata read keeps supplying display fields (thread id, session
     id, CWD), never identity. Absent on saved-index serves, which
     hold no instance identity. Absent never means fabricated.
2. **The `Authoritative` contradiction resolves as a designed
   migration, not a bare flip.** Order of work: `servedFrom` lands as
   the serving-point signal; the typed clients' saved-view replacement
   contract is redesigned around it and the pinned reducer test
   (`reducer.history.test.ts:533-567`) is updated deliberately; the
   doc-conformant stamping (false on saved serves, per the field's own
   documented meaning) lands WITH the client change in the same
   release. The redesign's replacement OUTCOMES — the NEW method's
   contract; the legacy method is frozen at today's behavior, named
   below — keyed on the serving facts this spec owns (today's clients
   delete held in-window items
   only when `authoritative` — grounding above; the new contract
   states when deletion happens):
   | Serve | Held in-window items |
   | --- | --- |
   | `servedFrom: live-daemon` | Replaced (today's live semantics: the live serve settles uncertain state) |
   | `servedFrom: saved-index`, no `fallbackReason` (normal daemonless) | Replaced (the saved index is the authority for a daemonless session; the pinned daemonless test's deletion behavior is preserved) |
   | `servedFrom: saved-index` with `fallbackReason` (either seam) | RETAINED (a fallback serve is best-effort evidence about a session whose live authority was unreachable — a stale fallback merged under live identity must never erase newer held items; the caller re-reads) |
   The RETAIN row is reachable only through the identity machinery: a
   client's `identityDisposition` applies `compareBootGeneration`
   BEFORE any authoritative or provenance consideration
   (`appwire-client/typescript/transcriptHistory.ts:770-779`), and a
   daemonless fallback page against a held live numeric generation
   compares as REPLACE (`bootGeneration.ts:29-36`) — on the
   failed-live seam, the primary seam of the incident, the stale page
   would keep erasing the newer held live history with the truth
   table's row unreachable. The ONE designed change this spec makes to
   the merge machinery: a fallback-marked response (`servedFrom:
   saved-index` with `fallbackReason` present) never reaches the
   generation comparison — it RETAINS held history at identity level
   (no replace, no authoritative drop), the response's thread fields
   and serving facts still update the model, and the caller re-reads;
   every unmarked response keeps today's disposition unchanged.
   Delivery is version-gated on the client surface so deployed strict
   validators never decode unknown keys: the existing
   `ThreadReadResponse`/`ThreadTurnsListResponse` shapes stay
   byte-for-byte, and the serving facts ride a NEW client-facing read
   method (naming follows the repo's method idiom at implementation);
   a new client's history reads use it. A new client against an OLD
   hub receives the typed method-not-found, degrades once per hub
   connection to today's method, and shows no facts — absent-when,
   never an error loop. An OLD client never calls the new method, so
   its exact-key validators and cached deferred pages stay valid
   against the new hub byte-for-byte — `hasOnlyKeys` is exact and
   `validDeferredPage` gates a cached page's fields on the generated
   allowlist
   (`appwire-client/typescript/transcriptValidators.ts:291-293,652-659`),
   which names exactly six `ThreadTurnsListResponse` fields
   (`types.gen.ts:5774`), and `decodeCurrentTranscriptState`
   (`appwire-client/typescript/transcriptCache.ts:153-165`) rejects
   the whole cached state on any field outside it. The absent-facts
   row is therefore named for both directions, keyed on WHICH METHOD
   the client calls — the freeze is exact: the legacy read method keeps
   TODAY's stamping and behavior byte-for-byte (true-on-saved stamps,
   today's merge dispositions on every seam; the pinned daemonless
   reducer test stays green through the migration because it pins the
   legacy method, which is frozen), and the corrected stamping, the
   serving facts and the fallback-marked identity exemption live ONLY
   on the new method. A new client on an old hub degrades to the old
   method and sees today's stamps and behavior. An old client on a new
   hub keeps calling the old method and keeps today's stamps and
   behavior — including the two costs the migration accepts rather
   than fixes in place, both stated as TODAY's behavior with the
   removal condition the old method's retirement: on the failed-live
   seam a daemonless fallback page against a held live numeric
   generation still compares as REPLACE and still erases the newer
   held live history (`identityDisposition` orders the generation
   comparison before any `authoritative` consultation,
   `transcriptHistory.ts:770-779`; `bootGeneration.ts:29-36`), and a
   normal daemonless serve still replaces per the pinned reducer
   test. No old client ever sees false-on-saved: the corrected
   stamping never touches the method old clients call.
   On the Manager tool path the facts reach the tool result
   additively — the agent-side schema and the hub ship in one release,
   and a Manager session on an older daemon simply gets no facts.
   The legacy method's frozen behavior — including the failed-live
   erase above — is named in the PR as the migration's known cost.
   The merged identity fields (BootGeneration/epoch under which the
   client merge tracks the still-streaming daemon) are untouched
   beside the fallback-marked exemption above:
   they describe merge machinery; `servedFrom` describes content
   provenance; two facts, one response, both true.
3. **Staleness is computed by the caller, never asserted by the hub.**
   The hub states the facts above. A Manager detects the night's
   disagreement by comparing them with the fleet picture (spec 1's
   running-turn facts when landed, or the daemon's own thread row): a
   saved serve whose `frontierTurnId` and `asOf` trail the roster's
   live facts is stale BY THE PAYLOAD, visible without trust. There is
   deliberately no `stale` verdict and no `storeFrontierTurnId` in this
   spec: a verdict the hub cannot always compute (the unreachable-store
   case) was round-2's falsification, and facts the hub CAN state make
   the verdict unnecessary.
4. **Catch-up failure keeps today's honest failure.** A locked or
   rebuilding index on the fallback path maps to the existing typed
   errors and the read FAILS (`app_threadread.go:409-455`); the read
   never serves uncaught-up content silently — that is the defect this
   spec removes, not a behavior it preserves.
5. **Remote refs keep today's failure, documented — and a reachable
   remote serve states whose serve it was.** No saved serve
   exists for nonlocal refs (grounding above); an unreachable remote
   read stays the mapped live error (`SessionUnavailable`). The
   serving facts apply to served views only. A reachable remote
   serve's provenance comes from the REMOTE hub's own serving
   branch, never derived from the local branch — which reads "live
   source" for a response the remote hub may have filled from its
   own saved index, the false-provenance class contract 2 forbids.
   The remote source's rebuild forwards the remote hub's own serving
   facts at the response-translation surface in
   `cmd/evener-hub/internal/appsource/remote_hub_refs.go` — the
   response-translation switch's `ThreadReadResponse` and
   `ThreadTurnsListResponse` cases, beside `translateThreadRaw`
   (`:541`), where read responses are remapped today; the capability
   filtering (`forwardedThreadCapabilitiesFor`, `:164`) is adjacent
   work only, NOT this landing zone; where the remote hub
   carries none, the facts are absent-when with the reason, stated.
   The owning docs state the
   Manager-side rule this implies: never infer freshness from silence —
   an errored remote read is an unknown, not a stale serve.

## Non-goals

- No change to the delivery/notification channel (PR 5662's lane).
- No zero-lag guarantee: a saved view trails the live daemon's memory by
  construction; the contract is that the trail is stated, not prevented.
- No new durable state beyond the shared session boot-generation
  store spec 4's design owns: the descendant fence reads that field
  and adds no store of its own. Every other fact derives from the
  serving branch, the serve-time catch-up moment, the view's anchor
  and the response's own instance identity.
- No background projection healing beyond the serve-time catch-up.
- No change to the client-merge identity machinery beyond the ONE
  designed exemption the migration names: a fallback-marked response
  retains held history at identity level. Every unmarked response's
  disposition (BootGeneration/epoch apply, ignore, replace, discard)
  is untouched (the merging itself is 5662-adjacent client contract;
  this spec adds provenance beside it, and changes no other merge
  semantics).
- No change to subscribe/incremental reads (`HeldSnapshot`,
  `HistoryChanges`): a different contract, untouched.
- No validation against the Manager's in-process delegate state: it is
  unreadable from the hub, and the transcript file is the authority that
  exists on both sides.

## Behavior contract

1. **Facts on every serve.** Every Manager-served transcript read of a
   session ref carries `servedFrom`, `asOf` and `frontierTurnId` on
   paths that can state them — absent-when per contract 6, never
   fabricated, and no serving point is unmarked;
   `fallbackReason` rides exactly the two fallback seams;
   `instanceId` rides live serves, sourced from the served thread row.
   No field is ever fabricated to fill a shape.
2. **The observed failure is explainable, not hidden.** Serving an
   hours-behind view as if newest, unmarked — through the failed-live
   seam, the succeeded-empty seam (whose live-stamped identity makes it
   the sharper lie), or a replaced instance — violates the contract.
   Post-change, the same serve states what it is: the saved view's
   frontier, its file-end moment, and the fallback reason. The red
   tests fail on exactly today's unexplained serves (spike template).
3. **No silent repair of meaning.** Facts reinterpreting nothing: the
   caller decides whether to consume the served window, re-read, or act
   on the disagreement. A read may always serve less data, labeled; it
   may never silently serve a different view than its shape claims.
4. **Cost.** The live path adds no oracle call. The fallback's catch-up
   is the existing index mechanism bounded to the file's end, not a
   rebuild; its failures keep today's typed errors. The frontier anchor
   computes from data the serving path already holds.
5. **Rotation coherence.** A rotation racing a read resolves by the same
   rule: one coherent view, honestly labeled. A response mixing two
   projections' items is a violation.
6. **Older daemons.** Serving facts are hub-side projections of
   knowledge the serving path holds; where a fact has no source on an
   older path (a saved serve predating the anchor, say), the fact is
   absent, never guessed. New wire fields land with regenerated
   AppWire types and updated appwire-client validators in the same
   change.

## Recovery and preservation

Nothing durable changes; there is nothing to preserve. The caller's
recovery story is the facts: a Manager that receives a saved serve
trailing the fleet picture knows exactly what it does not know — which
view, which moment, which frontier — and re-reads or drills down at its
own cadence. A crash between reads loses nothing; the next read
restates the facts. A fallback read that fails typed (locked,
rebuilding) loses no data either: the live session's state is
untouched by a read. The `Authoritative` migration is the one place
client behavior changes, and it changes by design, in the same
release: on the new method, with the legacy method frozen
byte-for-byte at today's behavior (its known costs named in the
migration, scope 2).

## Journey test plan

The spike reproduced the defect through both seams with existing fakes
(`scriptedAppSource` `web_test.go:313`, `relayLifecycleSource`/
`pastFallbackRelaySource` `cmd/evener-hub/app_rpc_helpers_test.go:25-40`,
`scriptedRelaySessionLease` `app_relay_test.go:3312`,
`buildRPCParentSession`/`newRebuiltPastIndex`
`cmd/evener-hub/app_rpc_helpers_test.go:393,50`); the journeys reuse that harness.

1. **Red through the failed-live seam.** A session with a live daemon and
   a stale saved index; inject a live read failure; read the transcript.
   Red (today, spike-observed): the index's hours-old turn serves as
   newest, `BootGeneration` daemonless, `Authoritative: true`, no
   reason, no moment. Green: the serve carries `servedFrom:
   saved-index`, `fallbackReason: live-read-failed`, `asOf` naming
   this serve's catch-up moment, `frontierTurnId` naming the old turn —
   the same serve, now self-describing.
2. **Red through the succeeded-empty seam, the sharper lie.** A daemon
   that does not own the ref answers empty; the Manager-path
   fall-through fires. Red: saved items serve under the LIVE
   BootGeneration with no provenance field — content provenance and
   merge identity disagree, silently. Green: the same response carries
   `servedFrom: saved-index` and `fallbackReason: live-read-empty`
   beside its live-stamped merge identity (both facts, both true), with
   `asOf` and `frontierTurnId` as above.
3. **Disagreement made visible (the incident).** A live daemon runs a
   turn started minutes ago (roster facts); the fallback serve carries
   an hours-old `frontierTurnId` and a file-end `asOf`. Assert: both
   instruments state their facts, the facts disagree, and neither
   payload pretends otherwise — the disagreement the fleet had to
   reconcile by hand is now computable from the payloads.
4. **Catch-up failure stays typed.** With the index locked, the
   fallback read fails with the existing typed error; it never serves
   uncaught-up content silently.
5. **Replaced instance, including before roster refresh.** Replace a
   session (the Clear/replacement class); read the old ref shape —
   once with the roster already refreshed, once immediately after
   Clear, before the next roster publication — and once through a
   CURSOR PAGE, where the identity must come from the serving cut,
   not the separate metadata read (grounding above): a Clear racing
   the page must not stamp the new instance for the old page's items.
   A live serve's `instanceId` names the instance that served it in
   every case; a saved serve carries no instance fact. Serving a
   stranger's transcript as the requested session's present, unmarked,
   fails the test.
6. **Unreachable remote.** For a remote-attached session with the host
   down, the read fails with today's mapped error; no facts are
   fabricated for an unserved view. And for a REACHABLE remote lane:
   the read carries the remote hub's own serving facts (forwarded by
   the rebuild) or the facts are absent-when with the reason — never
   a locally derived `live-daemon` stamp for remote-hub content;
   asserted.
7. **Rotation race.** Rotate the projection concurrently with a read
   (deterministic ordering via the existing harness); the response is
   coherent-and-labeled, never mixed.
8. **Frontier on every shape.** `thread/read`, `turns/list`, a cursor
   page, and the `job_status` hub read of the same view all name the
   same frontier through the same reused wire field
   (`latestExecutionTurnId`, scope 1); today the non-`thread/read`
   shapes drop it (grounding above), and the red asserts those drops.
   The BLOCKED-CARRIER case pins the steering persist as the frontier
   moment: a steering input is admitted and persisted, the turn's
   provider wait blocks its next entry, and a metadata read taken
   DURING the wait names the new execution as the frontier — the
   `EventSteeringInjected` persist moved it, not `TurnStarted`
   (scope 1's rows), asserted. The GOAL-MODE sibling pins the third
   entry mode: a clean goal continuation (no pending steering),
   its first provider response held at the existing
   `heldLegAdapter.Complete` seam
   (`agent/session_drain_as_steer_turn_boundary_test.go:48-55`),
   the steering entry ALREADY persisted when the hold begins —
   `acceptContinuationInput` records it
   (`agent/session_lifecycle.go:2416-2419`) before the provider
   call (`:1814-1829`) — so the full-history read, deriving its
   anchor from the first execution entry
   (`internal/transcriptindex/build.go:483-490`), ALREADY names
   the new execution during the hold, while the metadata read
   still names the PREVIOUS execution: the two-shapes
   disagreement in its real polarity, red before the emission
   reorder lands, green after, both shapes naming the new
   execution. The journey must force the marker's PRE-PERSIST
   sample, and a drain cannot: emissions are ENQUEUED
   (`agent/session_events.go:438-440`, into a 256-entry
   buffer — `agent/session_init.go:519`) and the bridge
   samples at DEQUEUE (`server/thread_envelope.go:352-357`),
   so draining after the hold proves the consumer processed
   the marker, not that it sampled before the persist — the
   round-14 review reproduced the GREEN-shaped observation on
   pre-reorder code by gating the consumer's drain until the
   provider hold. The barrier sits in the EMIT-TO-RECORD gap
   instead: block the transcript record between the emission
   and the persist, drain the authoritative consumer while
   the record is blocked — the one schedule in which the
   bridge's sample can only be the stale, pre-persist marker —
   release the record, then hold the provider, and assert
   metadata previous / history new: red before the reorder,
   green after. The signal journeys are ABSENCE-deterministic
   and need no barrier: before the signal exists there is
   nothing to drain, and the stale metadata frontier persists
   by absence. The HELD-ATTENTION case pins the asynchronous
   first-persist path (scope 1): an
   attention-only notification turn's first provider call held,
   a second delegate's attention delivered during the hold —
   the metadata read names the PREVIOUS execution before the
   first-record signal lands and the NEW execution after,
   while the full-history read already names the new execution
   from the first attention persist: red before the signal
   exists, green after. The HELD-COMPACTION sibling is the
   closure red: an attention-only notification turn (no job
   notifications, no steering — the scope 1 grounding) whose
   request preparation runs automatic compaction, then holds
   its first provider call; the checkpoint turn is the
   execution's FIRST persisted entry (kind CHECKPOINT,
   `PlaceSession` joining the running execution), history
   names the new execution from that entry, and the metadata
   read names the PREVIOUS execution before the first-record
   signal lands and the NEW execution after — no configured
   row fires on the compaction events at all, so red before
   the signal exists, green after. The COMMIT-BOUNDARY cases
   pin the one contract through every producer face (scope
   1): an attention-only wake whose first provider call is
   held while another delegate's attention records through
   the content site — the signal fires at the commit
   of that first entry, retained or durable alike, and the metadata
   read during the hold names the NEW execution (red if it
   names the previous one: the async gap), exactly once
   (red if it fires twice on the same execution); a
   compaction checkpoint fires it on the buffered commit
   inside the interval (red if it waits for the interval);
   a direct user input's retained first Record fires it at
   the commit, the pair helper's adoption notwithstanding
   (red if the signal waits for a later write); the
   resolution writers and the stop stabilization write
   fire it at their own commits (red if the free helper's
   records ship without it — the hook sees them like every
   other writer's) — the pinned ambiguous-sync
   fixtures (`newAttentionAmbiguousSyncFS`; the
   live-notification and live-resolution retry tests at
   `agent/session_attention_test.go:1177-1237` and
   `:1240-1275`) are the retained faces' existing seam
   evidence, and the stabilization case needs a RUNNING
   EXECUTION in the fixture so the resolution joins it as
   a first entry (reopened writers share the OS file's
   append tail, `agent/transcript/append_tail.go:15-44`).
   Three more cases pin the commit-boundary placement itself
   (scope 1). The RECOVERY-BARRIER sibling: the ambiguous-sync
   fixture already models the append's own fsync and the later
   barrier as two distinct syncs with rollback failure forced
   (`agent/session_attention_test.go:4672-4677`, `:4680-4686`,
   `:4720-4726`); gate the barrier's `Sync` and hold it — the
   refresh event is queued at the commit, so the metadata read
   names the NEW execution while the gate still holds (red if
   it waits for the barrier: the pre-return window), and
   nothing re-fires after the release (red if a second event
   lands). The TURN-ENDED-RESAMPLE case: after the execution
   completes, a facetAll resample still names the completed
   execution — the source is the stamped field, not the
   cleared execution state (red if the resample blanks it).
   The DEQUEUE-RACE case: a consumer processing the refresh
   before the attention site's `retainDelegateAttentionTurn`
   (`agent/session_attention.go:627`) still reads the NEW
   execution — the sampler reads the stamped field, never
   history (red if it can read the previous one).
   The round-21 cases pin the predicate and the seed: the
   PRESERVED-TAIL case — a real compaction whose retained tail
   holds older executions' first entries; the listener's
   `OriginalOrdinal` predicate must keep the field and the
   signal on the CURRENT execution while the replay lands (red
   if the field regresses to the copied execution or the
   signal re-fires: the copy is a second recorded entry for
   the same execution); the IDLE-RESTART case — a session
   restored over a transcript holding a completed execution,
   with no new execution opened: the metadata read names the
   completed execution from the attach seed (red if the
   frontier is blank), and the seed's serialization guard
   is pinned by pre-stamping the field or unit-calling the
   seed — no real schedule can race a commit against the
   seed at attach (the held-turn flushes join the prelude
   and `BeginExecution` is a nil-writer no-op before
   attach), so the red is the constructed interleaving
   (red if a stale seed overwrites a stamp).
   The round-22 cases pin the descendant route: the
   ADDRESSED-CHILD case — a real spawned child whose
   typed first-record event has RETURNED from the
   descendant route: the child's metadata read names the
   same execution the child's own field and its
   full-history read name (red while the consumer is
   missing: the cached child row never receives the
   frontier); the IDLE RESTORED-CHILD case — a restored
   child holding a completed execution, with no new
   execution opened: the child's `SessionStart` payload
   carries its attach-seeded frontier and the row
   construction installs it, and the metadata read names
   the completed execution (red if the restored child's
   row is frontierless — a root `facetAll` resample does
   not touch descendants); the REPLACEMENT case — the
   reclamation race the incarnation registry exists
   for: the old runtime queues frontier A, an idle
   release unhooks it before teardown
   (`agent/delegate_tree_reclaim.go:656-679`), a cold
   restore advances the row to frontier B, and the
   delayed old callback's A write is DROPPED by the
   carried-vs-current identity comparison at the row
   write — the case holds the callback after its
   sample, never riding the close drain, and is a
   spec-3 lane corpus red in BOTH builds: the
   identity fence holds with or without spec 4's
   generation (red if A lands and the row regresses;
   the landed build additionally asserts A and B are
   nonzero and distinct generations, and the
   comparison-disabled control is the paired red
   that admits A); the FAILED-CONSTRUCTION case —
   a restored constructor registers at attach, its
   recovery records and queues a first-entry
   callback, then a post-recovery step fails and
   the constructor returns nil: the
   constructor-local release defer must drop the
   orphaned registration before the queued
   callback installs (red if the install lands
   after the nil return — the round-26
   caller-only drive measured exactly that; green
   with the defer, `drops=1`), and a later cold
   restore of the same child still fences on
   identity either way; the WORKTREE-DISPOSE case —
   a stable resident delegate holds a first-record
   callback past its sample, `manage_worktree
   dispose` tears the child down through the
   ordinary teardown funnel, then the callback
   releases: the funnel's pre-teardown release must
   make the entry a MISS (red if the install lands
   with the entry still current after disposal;
   green with the release, the drop asserted beside
   the entry removal). The BEFORE-ROUTE RESAMPLE
   case pins the corrected racing-callback
   sentence: an old callback paused before entering
   the descendant route (the reachable delay at
   `agent/session_events.go:501-506`, outside
   `eventsMu`) resumes only after a replacement's
   cold restore has registered and recorded — it
   resolves the replacement's current entry and
   installs the replacement's own frontier, never
   the retired runtime's (red if the retired
   runtime's value lands or the row regresses;
   green prints the replacement's frontier — the
   round-28 drive measured exactly this schedule).
   The read-back failure paths keep today's
   content-verification behavior and assert NO signal
   interaction: the failed read-back removes the
   unverified turn from memory and the retry re-derives
   through the existing-content branch — the signal fired
   once at the record and stays fired (red if any
   verification step re-fires or suppresses it).
9. **The `Authoritative` migration.** The reducer's saved-view replacement
   behavior is updated around `servedFrom` and re-pinned, one pinned
   test per truth-table row — replace on a live serve, replace on a
   normal daemonless serve, RETAIN on both fallback seams — including
   the identity-level case that makes the failed-live row reachable:
   a fallback-marked daemonless page against a held live numeric
   history retains the held items (today `compareBootGeneration`
   compares that pair as replace, `bootGeneration.ts:29-36`; the
   marker exempts the response before the comparison). Doc-conformant
   stamping (false on saved serves) lands with it, on the new method
   only. An older-client simulation against the new hub reads through
   the legacy method and reproduces TODAY's behavior exactly —
   true-on-saved stamps, and on the failed-live seam the daemonless
   fallback page still replaces the held live numeric history: the
   freeze's stated known cost, asserted as today's behavior, not as a
   safe direction. Delivery, asserted in both
   directions: the old method's responses stay byte-for-byte — an
   OLD client's validators accept a new hub's old-method responses,
   and a deferred page served by the new hub persists and reloads
   through the OLD client's decoder (`validDeferredPage`,
   `decodeCurrentTranscriptState`); a NEW client against an old hub
   degrades on method-not-found to today's reads with no facts.

## Dependencies and interfaces

- Owns the serving-fact vocabulary (`servedFrom`, `fallbackReason`,
  `asOf`, `frontierTurnId`, `instanceId`) consumed by specs 1, 2 and 4;
  those specs assert it conditionally (README rule 6).
- Shares the read path with spec 2; either lands first; spec 2's window
  and fetch machinery does not depend on the facts and vice versa.
- Spec 1's probe-side running-turn facts give the roster comparison its
  live side; the facts here stand alone without them (the comparison
  degrades to whichever instruments the caller holds). Spec 1's
  in-flight-window rule (running turn ahead of persisted view)
  cross-references this spec's frontier semantics.
- The typed-client saved-view replacement redesign and the
  `reducer.history.test.ts` re-pin land in this spec's release (the
  Authoritative migration), with regenerated AppWire types and
  appwire-client validators.
- Adjacent to PR 5662's delivery work (same root-cause class, opposite
  side); the implementer documents the relationship and rebases under
  it.

## Acceptance criteria

- The two fallback-seam journeys and the replaced-instance journeys are
  red pre-change and green post-change, asserting on behavior (fact
  fields, served turn identity), never on prose.
- The rotation-race test passes under the race detector in the affected
  package.
- The serving-fact contract lands in `docs/product/history-content.md`
  (the owning guide for read content and freshness) and
  `docs/automation.md` teaches the Manager-facing facts and the
  caller-computes rule, in the same change. The Manager prompt
  template (`agent/testdata/messagingprompt/manager-on.md`) gains the
  same teaching — the spec-set contract (README) requires every changed
  Manager tool surface to update it, and today's prompt teaches paging
  without serving facts: it tells a Manager what `servedFrom`, `asOf`
  and `frontierTurnId` mean, and that the MANAGER computes freshness
  by comparing them with the fleet picture, never trusting a served
  view to be current.
- The `Authoritative` doc/behavior contradiction resolves in the same
  release: clients keyed on `servedFrom`, stamping doc-conformant on
  the new method, pinned test updated, and the legacy method's frozen
  behavior — including the failed-live erase it keeps, and its removal
  condition — named in the PR.
- The PR description names the older-daemon direction (facts absent,
  never guessed) and its removal condition.

## Alternatives considered

- **A hub-asserted staleness verdict.** Rejected after round-2
  falsification: the verdict the hub cannot always compute (unreachable
  store, unprobeable daemon) forces a third state or a fabricated
  answer, and every state duplicates what the facts already state. The
  caller holds the fleet picture; let it compare.
- **Deriving provenance from the wire's identity fields.** Falsified in
  round 3: the succeeded-empty seam merges saved items under the live
  BootGeneration (`fallbackHistoryIdentity`), so the stamp lies on
  exactly one of the two fallback seams. Only the serving branch is a
  truthful source, so the serving branch is the source.
- **A bare `Authoritative` flip.** Rejected in round 3: the typed
  clients key held-item replacement on the flag, with daemonless
  behavior pinned by test — the flag is load-bearing machinery, and
  changing it without redesigning the client contract trades one lie
  for a silent behavior change. The migration is designed, tested, and
  skew-safe.
- **Refuse stale reads.** Rejected: fail-closed reads break every sweep
  during the exact rotations and outages when the Manager most needs to
  see something; the pulse-meter precedent serves absence or truth,
  never refusal-as-safety. (Catch-up failure still errors — that is the
  existing typed path for a read that cannot honestly serve, not a new
  refusal.)
- **TTL-based hub caching.** Rejected: a TTL guesses at staleness; the
  serve-time catch-up and its moment are real.
- **Make `job_status` the oracle.** Rejected after grounding: its store
  is the Manager's in-process state for owned delegates (unreadable from
  the hub), and its hub branch is a metadata-shaped `thread/read`
  carrying no frontier. The transcript file's end is the authority both
  sides can read.
- **Sourcing `instanceId` from the roster.** Rejected in round 3: the
  roster identity is sampled on an independent loop and can describe a
  different instance than the one that served the read (Clear before
  refresh); the served thread row carries the serving instance in the
  response itself.
