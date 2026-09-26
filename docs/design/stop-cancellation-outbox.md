# Stop-cancellation design (v2): cancellation as durable outbox record state

Status: for Jesse's sign-off, 2026-09-17. Supersedes `stop-cancellation-design.md`
(the in-memory settle-step), whose durability premise RoboRev's High 1 falsified and
whose author retracted it in the handoff.

Evidence base: a read-only enumeration of the `ux-1393-cancellation` worktree at
`5f9f21845`, every claim below with file:line. `$F` = `cmd/evener-hub/frontend/src`.

---

## 1. The defect, stated once

A Stop should permanently cancel the sends it was meant to cancel. Three in-memory
iterations of this mechanism failed because the cancellation fact lived in per-tab
module memory while the thing it governs, the IndexedDB outbox, is durable and
shared across tabs. Every race finding on the current PR head (High 2, Medium 4, 5,
6) is a timing artifact of that mismatch. Every persistence finding (High 1) is the
mismatch itself.

The fix is not a better in-memory mechanism. It is to store the cancellation fact
where the record it governs already lives.

## 2. What the enumeration established

- **The state union admits a new state for free.** `MutationOutboxState` is
  `"submitting" | "blockedUnknown"` (`$F/stores/mutationOutbox.ts:3`). Records are
  whole-object `put`s; the `upgradeneeded` handler is purely additive
  (`mutationOutboxIndexedDB.ts:445-471`). Adding `"canceled"` to the union is a
  one-line type change with zero schema migration — but not zero version cost:
  a mixed-version tab cannot share this database, so `DATABASE_VERSION` is
  bumped to 3 (see §8, corrected).
- **The cancellation plumbing already exists; only its storage is missing.**
  `restoreProvenAbsent` (`mutationOutboxIndexedDB.ts:295-314`) is the *only* site
  that reopens a record to `submitting`. It reopens only `blockedUnknown` rows
  (:306), skips authoritative IDs (:307), and *already honors* a cancellation set
  (:308). Its sole caller (`threads.ts:1724`) passes the in-memory
  `canceledMutationIds`. A durable `canceled` state slots into the same check at
  :306: a row in `canceled` is structurally unreachable by reopen, in every tab,
  after every reload, with no set to propagate.
- **The reload hazard is real and located.** After a reload,
  `dispatchableMutationRefs` starts empty (`threads.ts:612`) and re-arms only
  through hydration (`threads.ts:1683`), whose reconciliation calls
  `restoreProvenAbsent` (`threads.ts:1724`). That is the exact moment a previously
  canceled `blockedUnknown` row gets resurrected, because the new tab's cancellation
  set is empty. The comment at `threads.ts:926-927` claiming the page re-derives
  cancellations from the hub's recovery state on reload is aspirational; no such
  code exists.
- **Cross-tab fencing is currently luck.** Two tabs share one IndexedDB database
  with readwrite transaction ordering and a BroadcastChannel wakeup that carries
  only `{targetRef}` (`mutationOutbox.ts:186-194`). Tab B's reopen is fenced against
  tab A's cancellation only by transaction-ordering accident.
- **The UI's actual Stop records nothing.** `interrupt` (`threads.ts:2873-2887`),
  which the Composer's Stop button calls (`Composer.tsx:1379`) and the command
  palette calls (`shell/palette/commands.ts:393`), awaits only the durable enqueue
  of `turn/interrupt`. It never records cancellations. Only `forceStop`
  (`threads.ts:3050`) and `shutdown` (`threads.ts:3064`) call
  `recordCanceledMutations`, and they do it *after* the stop RPC resolves, while
  dispatch stays enabled.
- **No GC exists.** Every outbox removal is receipt- or reconciliation-driven
  (`settleReceipt` :219, `settleApplied` :237, `transferToRecovery` :327,
  `discardRecovery` :383). A new persistent state needs a named removal path or its
  rows live forever.

## 3. Decision 1 (placement): a `canceled` state on the outbox record

| Option | Verdict |
|---|---|
| **A. New `canceled` record state (recommended)** | One authority per record, stored where the record lives. Reopen-proof by construction at `mutationOutboxIndexedDB.ts:306` in all tabs and after reload. Deletes the side-set and its growth problem (Low 9). No schema migration. |
| B. Durable side-set in IndexedDB | Second source of truth about record state, with its own staleness and reconciliation questions. This is the shape RoboRev literally suggested ("persist cancellation state in IndexedDB"), but a side-set is the weakest reading of it; the record's own state field is the same suggestion with one authority instead of two. |
| C. Hub-owned cancellation | Wrong home. The hub's `SessionRecoveryState` says "session is stopped"; it cannot say "the user's Stop killed these specific mutation IDs", it clears on explicit Resume, and the hub has no knowledge of client outbox rows that never reached it. |

Recommendation: **A**.

## 4. Decision 2 (timing): write cancellation at user intent, before the stop action

The failed iterations wrote cancellation *after* acknowledgment, which created the
race window RoboRev keeps finding. The durable write should happen **first**:

- **`interrupt`:** mark the ref's rows `canceled` in the same readwrite transaction
  that enqueues the `turn/interrupt` record (one new storage method,
  e.g. `enqueueInterruptAndCancel`). Both durable or neither. Medium 4 dissolves:
  the UI's real Stop path *is* the cancellation path, and the tests exercise it
  directly.
- **`forceStop` / `shutdown`:** mark rows `canceled` first, then issue the RPC. If
  the storage write fails, the stop is aborted and reported; the daemon is still
  running and the user can retry. High 2's second half ("storage failure after a
  successful Stop leaves no recovery obligation, so `forceStop` can fail while the
  daemon is already stopped") becomes structurally impossible: the dangerous order
  (stopped first, write second) no longer exists.
  The write's success notifies the owning runtime's projection on every
  outcome, zero canceled rows included (2026-09-19, the fresh review's Low):
  zero is exactly what a sibling tab's earlier Stop leaves this tab, the raw
  write announces nothing over the BroadcastChannel, and the notify is what
  refreshes this tab's projection and pins now rather than at the next
  discovery scan — the same zero-included rule the discard paths carry.
  Boundary (ratified 2026-09-18): this abort applies to a *real store whose write
  fails*. When no mutation store exists at all (IndexedDB unavailable to the tab),
  there are no durable rows to cancel and the tab can neither resurrect nor reopen
  any, so write-first has nothing to protect and forceStop/shutdown proceed.
- **Mid-reconciliation Stops (Medium 5, 6, and the unfenced awaits in
  `publishAndReconcileThreadHydration`, `threads.ts:1703-1732`):** dissolved by
  durability. `restoreProvenAbsent` reads record state inside its own transaction;
  whenever the Stop landed, the `canceled` state is what the next reopen attempt
  sees. The per-operation generation baselines and settle-step machinery proposed
  in v1 are no longer needed for outbox cancellation.

  **The one interleave transaction ordering cannot fence** (added 2026-09-18,
  the RoboRev in-flight-enqueue finding): an enqueue clicked before the Stop whose
  durable write had not yet been *issued* when the Stop's cancel transaction
  committed — a cold tab still opening its connection while a warm tab's Stop
  lands — commits *after* the cancel scan, so the scan never sees it. Same-tab
  writes cannot reach that order (the send and stop chains serialize on the same
  `runtime.start`/`#open` promises, and IndexedDB runs same-scope transactions in
  creation order), but two tabs hold two connections, and the engine's ordering is
  exactly what puts the late write after the cancel. The fix is a durable
  **stop-epoch barrier**: every Stop's cancel transaction bumps a per-ref
  `stopEpoch` (riding the `sequences` store's row for that ref, so no schema
  change), the enqueueing tab reads the epoch at click time, and the enqueue's
  own transaction compares the two — a Stop that landed in between makes the
  record commit born-`canceled`: announced for the canceled queue strip, never
  dispatched, released only by explicit Retry, exactly like any other canceled
  row. The Stop's own interrupt record passes no barrier (it *is* the click the
  epoch records), and explicit Retry / recovery resends are equally exempt — they
  are the user deliberately sending after a Stop. The barrier's comparison is
  commit-order, not click-order: a Stop whose durable write lands after a later
  send's capture still cancels that send — the conservative direction, retryable,
  the same reversibility §4 stands on.

  **The capture's own boundary** (added 2026-09-19, the fresh review's
  stop-epoch capture race): the capture is a *read*, and a read must wait for
  the tab's own IndexedDB connection. The enqueue requests it at true click
  time — inside the click's synchronous prefix, before any startup wait in the
  enqueue chain — so the click itself issues a cold connection's open and owns
  the first transaction on the reopened connection, and every Stop whose
  durable write is *created* after that request is fenced: it either commits
  before the enqueue's own write (the comparison reads its bump; the row
  commits born-`canceled`) or after it (the Stop's cancel scan cancels the
  committed row directly). What no client-side mechanism can fence is the
  narrower order that remains: a Stop whose durable write is created *before
  the capture read's transaction can exist* — during a cold connection's
  opening wait, or in the engine's own scheduling sliver between the request
  and the read's first store access on a warm one. That Stop commits before
  the capture resolves, the capture reads the post-Stop epoch, the comparison
  passes equal, and the row commits `submitting` past a Stop whose scan already
  ran. The database holds no record of the click, and a cold tab holds no
  connection to read one with, so this Stop is indistinguishable from §9 item
  10's deliberate post-Stop send — which must send. Canceling every capture
  that cannot prove it predates some Stop would refuse exactly that
  deliberate send, so the window is bounded, not closed: the click requests
  the capture (nothing the enqueue chain does can widen the window), and the
  boundary test in §9 item 10 pins the residual's shape so nothing can claim
  it closed without amending this section first.

  **The release fence** (added 2026-09-19, the fresh review's Retry-path
  finding): the explicit Retry that releases a canceled row is the one other
  deliberate send, and its release is the one transition that can resurrect
  a row a newer Stop claimed — the Stop's cancel scan skips a row already
  `canceled`, so nothing but the release can bring it back. The Retry click
  therefore captures the ref's stop epoch the same way an enqueue does, and
  the capture rides the click's own first storage observation: the ref is
  unknown before the row is read, so the capture cannot precede that read —
  it joins it, the row and the epoch read in ONE transaction requested in the
  click's synchronous prefix (`getOutboxWithStopEpoch`). The release's own
  write transaction compares the stored epoch against that capture and
  refuses when it advanced, leaving the row canceled: a newer Stop outranks
  an earlier Retry, the same commit-order rule the enqueue barrier carries.
  The "deliberate post-Stop send" exemption is therefore bounded for Retry
  exactly as for enqueue — a Stop whose durable write commits before the
  capture read's transaction can exist is invisible to it (the capture reads
  the post-Stop epoch, the comparison passes equal, the release proceeds),
  indistinguishable in the database from the Retry §9 item 7 protects, which
  must send. Recovery resends stay barrier-free: they carry no user click to
  timestamp.

The honest boundary: a row already `attempted` (dispatcher flipped it in a
transaction before transport, `mutationDispatcher.ts:113`) may be in flight to the
daemon. Cancellation cannot unsend it. The dispatcher's existing pre-transport
re-read (`mutationDispatcher.ts:107-110`) sees `canceled` and aborts the send in
every other case. The residual window is "the packet was already on the wire when
the user clicked", which no client-side mechanism can close and none should claim
to.

This is a semantic change worth stating plainly: **the user's click is the cancel
moment, not the daemon's acknowledgment.** The click is reversible (explicit retry);
the ack-timed version was the source of every race finding.

## 5. Decision 3 (scope): cancel all non-attempted rows for the ref

When a Stop lands for a ref, transition every outbox row for that ref whose state
is `submitting` or `blockedUnknown` and which has not been attempted, to `canceled`.
Attempted rows are reported as in-flight/uncertain, not canceled. This matches the
prior mechanism's scope (all rows on the ref) minus the rows it could not honestly
cancel.
The same transaction bumps the ref's stop epoch — the fence §4's in-flight
barrier compares against — so the scan and the fence commit as one durable fact.

## 6. Lifecycle of a `canceled` row

- **Release:** only an explicit user Retry transitions `canceled` to `submitting`
  (new transition in `retryBlockedMutation`, replacing the current delete-from-set
  release at `threads.ts:1001`), fenced by §4's release barrier: the Retry's
  click-time capture refuses the release when a newer Stop's epoch bump
  intervened. "Cleared only on explicit retry" per RoboRev's
  High 1.
- **Removal:** `canceled` rows are deleted when the thread is cleared or deleted
  (existing `clearThread`/deletion paths), and on explicit Retry. No TTL, no GC
  pass. Growth is bounded by the number of sends a user cancels and never retries,
  which is user-visible and small.
  The removal also reconciles across tabs: a published replacement instance is
  the one clear signal every tab observes on its own, so a clear settled by
  another tab - whose response and best-effort removal never reached this one -
  is completed by whichever tab observes the transition, scoped to the instance
  the transition provably replaced (the BroadcastChannel wakeup is a timing
  hint, never the authority).
  "The instance the transition provably replaced" is the fencing identity
  itself — `instanceId ?? threadId`, the same `expectedInstanceId` every
  payload carries — never the thread id alone: a replacement can rotate the
  instance while retaining the thread id, and every durable row carries its
  enqueue-time instance so the cleanup (and Retry's press-time refusal, §6's
  release fence) compares the identity the daemon would actually fence with.
- **Note rows are the exception with a third exit.** QueueStrip deliberately
  leaves `notes/human/set` rows to the note editor, whose only retry branch is
  gated on `blockedUnknown` - a canceled note row would otherwise sit pinned
  (with its note text) until the thread goes away. The user's next save is the
  retry: when a newer `notes/human/set` for the ref reaches canonical
  settlement (`settleReceipt`/`settleApplied`), a best-effort write right after
  the settlement commits discards the ref's earlier canceled note rows, mirroring
  the existing `#discardSupersededNoteRecovery` supersede for refused note
  recovery rows. (After the commit, not inside it: an in-transaction scan delayed
  the commit boundary the note editor's parked-save replay stages against.)
  A failed discard leaves the rows for the next settle, clear, or delete.
  The discard is fire-and-forget by design, but not silent: the settle's own
  notification has already passed by the time its write commits, so the
  storage itself notifies the owning runtime when the cleanup completes —
  zero-deletion cleanups included, the same rule the removal paths carry —
  and the pin re-derivation that follows lets a later `releaseThread` drop a
  model whose row just left. The pin half only: the discard's completion has
  no authority over whether the ref is dispatchable, and a full pin refresh
  here can de-arm a dispatch an enqueue mid-chain already scheduled.
  Delivery-uncertain note rows are never discarded this way - a newer save
  supersedes nothing that may already be on the wire.
- **Display:** `canceled` rows surface in the same UI slot as today's blocked rows
  with "Canceled by Stop" copy and the Retry affordance. Small Composer change.

## 7. What this deletes from the current PR head

- `canceledMutationIds` (`threads.ts:923`) and `recordCanceledMutations`
  (`threads.ts:928-941`), including both call sites.
- The third (`canceledIds`) parameter of `restoreProvenAbsent`
  (`mutationOutboxIndexedDB.ts:295-314`); the `:306` state check subsumes it.
- The mid-flight undo in `retryBlockedMutation` (`threads.ts:1011-1022`) and the
  baseline/`checkStopped` machinery that exists only for outbox cancellation.
- `stoppedRetryRefs` remnants, if any survive.

What does **not** change: the resume fence (`userIntentStopGenerations`,
`cancelPendingUserIntents`, `resumeStopFence`, the client's `beforeRequest` hook),
`restartBlockingObligations`, the dispatcher's `markAttempted`/re-read protocol,
and the BroadcastChannel (payload unchanged; correctness never depends on it).

## 8. Compatibility

- No data migration. Existing `blockedUnknown` rows stay `blockedUnknown`; the new
  state is only written going forward. The version bump's upgrade handler is
  unchanged and purely additive: opening a version-2 database at version 3 runs
  no store mutations, and the existing rows survive intact.
- Mixed-version tabs fail closed and reload. This design's earlier draft claimed
  an old tab merely never surfaces canceled rows ("invisible … the safe
  direction"), which was wrong about dispatch: the old `nextDispatchable`
  returns nothing unless the ref's FIRST record is `submitting`, so a
  `canceled` row at the head of the FIFO silently stalls that ref's whole queue
  in an old tab. `DATABASE_VERSION` is therefore bumped to 3: an old tab's
  version-2 open against this database refuses with `VersionError`, every
  outbox read and write in that tab fails loudly, and projections degrade to
  "storage unavailable" — the safe mixed-version state is a reload, never a
  silent share.
- Native conforms at the storage-port level (2026-09-19): `MutationOutboxSQLite`
  implements `enqueueInterruptAndCancel`, skips `canceled` rows in
  `nextDispatchable`, and honors the stop barrier over an additive `stop_epoch`
  column on its `mutation_sequence` rows (migrated in place, default 0), and
  persists the enqueue-time `instanceId` — §6's fused fencing identity — through
  an additive `instance_id` column on its record tables, migrated in place the
  same way (existing rows read as identity-less, falling back to their
  threadId). The phone's Stop/UX flows do not call the combined write yet —
  adopting it there is a follow-up, so nothing native issues a Stop's durable
  write today.
  The Retry release fence (2026-09-19) changed no port signature:
  `releaseCanceled` is a web-store method the `MutationOutboxStorage` port has
  never declared — native has no canceled row to release while nothing native
  issues a Stop's durable write — so the comparison rides the web store's own
  release method, and the eventual native adoption of the canceled lifecycle
  takes the release with the same click-time capture.

## 9. Test plan

Real store, real flows, no mocks of the mechanism under test:

1. Composer Stop (the `interrupt` path) cancels pending rows durably: a later
   discovery scan in the same tab does not resend. (The requester-protected
   invariant, now wired to the UI's actual button.)
2. Reload: cancel, reload the page, run hydration/reconciliation; the rows stay
   `canceled`, nothing dispatches.
3. Cross-tab: cancel in tab A; tab B's reconciliation cannot resurrect the rows;
   tab B reads `canceled` on its next scan.
4. Post-resume: cancel, explicitly Resume, run reconciliation; still no resend.
   Retry after Resume dispatches.
5. Storage failure before the stop RPC aborts the stop and reports it; daemon
   untouched; retry works.
6. An `attempted` row in flight at click time is reported in-flight/uncertain, not
   canceled; a `submitting` row caught before `markAttempted` is canceled and the
   dispatcher's re-read aborts its send.
7. Explicit Retry transitions `canceled` to `submitting` and dispatches; background
   (non-user) retry modes do not.
8. Canceled rows are removed on thread clear/delete.
9. Every wait-helper in the new tests fails loudly when its condition is never
   reached (the Low-8 lesson).
10. An enqueue clicked before the Stop whose durable write issues after the
    Stop's cancel committed lands born-`canceled` and never dispatches (the §4
    in-flight barrier): the storage-level test interleaves a real write past a
    completed second-connection Stop, and the store-level test gates the real
    write at the seam. A capture taken after the Stop still sends, and the epoch
    survives later enqueues, a reload, and both stop paths. The capture-race
    pair (added 2026-09-19): a click whose connection is still opening must
    request the capture before any other write can take the queue position, so
    a Stop committed during that connection setup lands its bump where the
    enqueue's comparison reads it; and the residual window has its own pin —
    a Stop committed before the capture read's transaction can exist refreshes
    the baseline invisibly, and the row commits `submitting` exactly like a
    deliberate post-Stop send, §4's bounded boundary.
11. The Retry release's own barrier (added 2026-09-19, the fresh review's
    Retry-path finding): a second connection commits a Stop between the
    Retry's click-time capture and its release — the release refuses, the row
    stays `canceled`, and it never dispatches; the capture is requested in
    the click's synchronous prefix, so a Stop committed during a cold
    connection's setup is fenced too; and a Retry pressed after the newest
    Stop (a fresh capture) still releases and dispatches, item 7's deliberate
    post-Stop send.

## 10. Open questions for Jesse

1. Ratify Decision 2's semantic change: click is the cancel moment, not the
   daemon's ack. (Recommended.)
2. Ratify Decision 3's scope: all non-attempted rows for the ref, including queued
   sends. The alternative, canceling only `blockedUnknown` (delivery-uncertain)
   rows, lets queued sends dispatch into a stopping daemon and bounce to
   `blockedUnknown` *after* the cancellation write, resurrecting the hazard one
   step later. (Recommended: all non-attempted rows.)
3. `forceStop`/`shutdown` move to write-first ordering like `interrupt`, for one
   uniform rule. (Recommended.) The alternative keeps them ack-timed and re-opens a
   small version of the High-2 window on those paths only.
