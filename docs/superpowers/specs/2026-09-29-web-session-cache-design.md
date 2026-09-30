# Web session-history cache (IndexedDB)

**Date:** 2026-09-29
**Branch:** `web-session-cache`
**Status:** design (under review on PR #3406) → implementation
**Parent specs:** `2026-09-25-transcript-read-model-design.md` (v6 read model),
`2026-09-01-atomic-transcript-item-paging-design.md`

## Problem

Reload the web UI and every session pane the layout restores starts from an
empty store: the recorded history, the held snapshot identity it was read
under, and every older page the user scrolled back to are all gone. Each
restored pane then pays two costs. The pane shows its loader until a
full latest-window `thread/read` round trip lands, and everything the user
had scrolled back to re-arrives over the wire, one `thread/turns/list`
page at a time, as they scroll again.

The browser's HTTP cache cannot hold any of this. Session content rides the
appwire socket; the HTTP cache only ever sees hashed static assets, sha-addressed
images, and doc files. No header changes that. The only browser-local store
that fits is IndexedDB, which the web app already uses for the mutation outbox
(`stores/mutationOutboxIndexedDB.ts`).

The volume is real: on this machine the evener project's state directory holds
about 10 GB of session JSONL, 875 files over 1 MB. The latest window is
bounded (40 items), so the loader wait is the perceived cost and the
scroll-back pages are the volume cost; both are what a local cache removes.

## What already exists (why this is a small design)

The v6 read model already implements everything a cache needs to reconcile
against the server; only persistence is missing.

- A `thread/read` with `includeTurns` over a v6 history carries the snapshot
  identity `{incarnation, length}`, plus `epoch` and `bootGeneration`
  (`appwire/types.go`). A lean read (`includeTurns: false`, or no history)
  carries no snapshot identity, so everything below keys on content-bearing
  reads only.
- On the daemonless path (the hub serving a past session from its transcript
  index), a read whose `heldSnapshot` names what the client holds gets back
  the latest window plus `changes` (`HistoryChanges`) covering everything an
  entry at or past the held length created or touched — not just held items
  that changed, every item past the held length
  (`internal/transcriptindex`'s `ChangedSince`, `app_threadread.go`'s
  `pastEntryLatestItems`, `index.LatestSince(limit, held)`). A held snapshot
  the server's update log no longer reaches is rejected with
  `TranscriptItemCursorStale`, and the store already retries once without a
  held snapshot for a full latest-window replacement
  (`shouldRetryWithoutHeldSnapshot`, both hydrate call sites).
- On the daemon-served path (a live session), the read handler ignores
  `heldSnapshot` entirely and returns the window with no `changes`
  (`server/appwire_runtime.go` `finishAppThreadRead`; `HistoryChanges` is
  documented daemonless-only). The client sends the held snapshot either way;
  the server decides what it means. The consequence for this cache is
  bounded in its own section below.
- Request generations are monotonic across boot changes
  (`model.ts`'s `HistoryState.issuedGeneration`, "never reset"): the
  generation lives in the model, so a model loaded from disk carries the
  counter forward and fresh reads still get strictly increasing generations.
  A history-bearing model passed to `beginThreadHydration` is published
  again as a generation-bumped copy (`issuedGenerationFor` →
  `issueLatestWindowRead` returns a new object, and the store's
  `baseModel !== model` guard publishes it by design).
- Older pages the model holds already keep their cursor across a merge:
  `applyReadResponse`'s merge branch keeps `model.olderCursor` when the held
  turns extend below the fresh window (reducer.ts, "Older pages the client
  holds keep their own cursor", added by 792c379eca). A replace disposition
  (another incarnation, a newer epoch, a higher boot generation, a resync
  push, or invalidation on reconnect) takes the response's cursor. No reducer
  change is needed for this design; a test pins the existing rule in the
  reload scenario.
- Stale scroll-back cursors are rejected and refreshed
  (`loadOlderTurns`'s `isStaleCursorError` path).

The design below makes the cached record flow through exactly the machinery an
in-memory held model already flows through. A cached model must be valid
wherever an in-memory held model is valid, and that equivalence is the
invariant the tests pin.

## Design

### The persist unit

One record per `ref`:

```ts
interface CachedSessionRecord {
  ref: string;              // primary key
  threadId: string;
  name: string;
  modelProvider: string;
  model: string;
  imageSessionId?: string;   // sha-addressed image fallbacks rebuild from it
  olderCursor?: string;      // where scroll-back continues from
  savedAt: number;
  history: {
    bootGeneration: string;
    epoch: number;
    incarnation: string;
    length: number;
    appliedGeneration: number;
    issuedGeneration: number;
    turns: TurnModel[];      // window plus every older page merged so far
  };
}
```

Everything here is the recorded history and the display fields a pane needs to
render it before the socket answers. Nothing live is persisted: no overlay,
no `queue`, `tasks`, `pendingMutations`, `diagnostics`, `delegates`, `skills`,
`jobsUpdatedAt`, `lastFrameAt`, `modelRetry`, no `instanceId`, no
`pendingEscalations`, no `status`. The load path resets the transient
invalidation fields (`invalidatedAtGeneration`, `awaited`, `pendingIncarnation`,
`deferredPages`, `failed`): a reload starts a clean read, never a carried
failure.

`threadModelFromCache` fills every remaining required `ThreadModel` field
with its zero value (empty capabilities, null queue and tasks, absent jobs
and escalation fields, a neutral status). The constructor is exhaustive by
construction: a type-level test pins that no required field is left
undefined, so a future `ThreadModel` field fails the build here until the
record or the constructor accounts for it.

`TurnModel` is plain data; records round-trip through JSON. A schema-version
fence (the outbox adapter's pattern, `DATABASE_VERSION` with a compatibility
comment) guards the shape across releases.

### The load seam: the cached shell

On `ensureThread`, when the store holds no model for the ref, the store runs
one cache lookup before arming the hydration. Two properties are load-bearing:

- **The lookup is bounded by its own short deadline** (250 ms,
  `Promise.race`), not the outbox adapter's 10-second storage timeout
  (`STORAGE_WAIT_MS = 10_000`). It captures the current clear epoch when
  it starts — the durable epoch row read in the lookup's own transaction,
  never the in-memory view, which a tab that has not yet received the
  clear's channel message cannot trust — and re-reads the durable epoch
  row immediately before publishing, in the same synchronous step with
  no await between, discarding on any difference, and requires the
  captured epoch to equal the tab's in-memory epoch at publish — a
  same-tab clear bumps the in-memory epoch synchronously, so whatever
  order the lookup, the re-read, the clear, and the publish land in, a
  same-tab clear never resurrects a shell. IndexedDB cannot make the
  publish itself atomic with another tab's transaction, so a sibling's
  clear committing between the re-read and the publish can still paint
  pre-clear content for the pane's opening moment — bounded staleness,
  not resurrection: the write seam's in-transaction epoch check keeps
  that shell from ever re-persisting, and the next fenced read replaces
  it. The
  same captured value is the lease's epoch: the arming records it, and
  the clear's suppression uses it to tell a lease that predates a
  clear from one opened after it (the clear section). On deadline, open failure, miss, or an epoch
  change, the
  hydration proceeds exactly as today, with `beginThreadHydration` receiving
  `undefined` as its model. No pane ever waits on storage longer than 250 ms.
  The lookup is serial by necessity, and the trade is stated rather than
  hidden: the held identity must exist before the request goes out (the
  store's own contract — `issueLatestWindowRead` runs synchronously before
  the wire call), and issuing the read concurrently would cold-read every
  cache hit, forfeiting the daemonless `changes` and replacing the shell's
  pages wholesale. The serial cost is a warm IndexedDB get, single-digit
  milliseconds; the 250 ms bound is reached only in the pathological
  wedged-open case, after which behavior is exactly today's.
- **The lookup has no pre-existing slot to occupy.** `ensureThread`'s
  sequence is check-then-arm (`threads.has(ref)` short-circuit, then
  `beginThreadHydration`'s synchronous `pendingThreadHydrations.set`). The
  cache lookup therefore carries its own per-ref inflight map, mirroring
  `inflightHydrates`: a concurrent `ensureThread` for the same ref joins
  the shared lookup rather than starting its own, and only the lookup's
  creator proceeds to publish and arm — joiners return once they see a
  model or a hydration in flight, so two callers cannot double-arm. After
  the await the creator rechecks: the ref may have been released
  (refcount zero), deleted, already hydrated by a concurrent holder, or
  the lookup may have lost its own deadline race — and in every one of
  those cases the lookup result publishes nothing and arms nothing,
  because from the deadline onward the cold path it abandoned is
  authoritative.

On a hit, the store publishes the cached shell through `putThreadModel`
immediately, then arms the pending hydration with the shell as its base
model. `beginThreadHydration` publishes the shell a second time as the
generation-bumped base (its own guard, above) — content-identical, and the
pane's loader clears on the first publication either way. Consequences:

- The threads map holds the bumped base, not the shell. The read carries
  `heldSnapshot {incarnation, length}` and a request generation above the
  record's `issuedGeneration`; monotonicity survives the reload for free
  because the counter lives in the model.
- The shell renders turns immediately. Its admission state is new, not an
  existing one: capabilities are the empty set, so every capability-gated
  action (send, steer, queue, stop, resume) admits nothing until the
  authoritative read lands. Actions that are not capability-gated can still
  run: instance-fenced mutations fall back to the model's `threadId`
  (`threadInstanceID`, threads.ts), so a Clear from the shell dispatches
  fenced on the cached `threadId`, which the hub rejects if the session was
  replaced and admits if it was not; clearing the session record itself is
  the intent either way. The shell carries no `instanceId` and no
  `pendingEscalations`.
- **A shell never writes back, keyed on explicit shell-lineage state.**
  Publishing the shell sets a per-ref shell flag; the first authoritative
  read's publish for that arming clears it. The flag — not object
  identity — is what the write gate reads, because identity covers only
  the pending window: a failed read dissolves the pending entry, a live
  notification then folds a new object onto the shell, and that object
  holds unverified, possibly hole-bearing turns that identity would
  happily write. The flag stays set through the failure and the fold; the
  retry's success clears it, and writes resume from a verified model.
- The reconciling read then merges through `applyReadResponse(base, response, now)`
  or, on the stale-held-snapshot path, replaces history via
  `hydrateThread` after the existing retry-without-held-snapshot. A new
  incarnation or a replacing epoch replaces the history outright; a
  below-log-floor identity degrades to a full window. Both paths exist
  today and are untouched.
- **A failed reconciling read is specified, not left open.** A transport
  failure follows the store's existing retry ladder (retry-forever per
  ref): the shell's content stays visible, capabilities stay empty, and
  nothing writes — the shell flag holds through the failure, so a
  notification folded onto the shell while the retry is pending cannot
  write unverified content either. A
  history-failed answer applies the existing one-diagnostic rule to the
  base model (`applyHistoryReadFailure`), and the failed gate refuses
  writes. A fenced deletion takes the existing fence path and deletes the
  record. In every failure shape the pane degrades no further than a pane
  whose first hydration keeps failing today, except that it shows its
  cached content while it waits — which is the point.

`watchThread` loads nothing from the cache and writes nothing. Watched
models live in `watchedThreads`, published through
`putWatchedThreadModel` and the notification handler's own fan-out; the
write hook never sees them, and an expanded delegate card (a rich watch
with turns) must not change that. The hook's threads-map-only placement is
what enforces decision 5, so a test pins that a rich watch model publishes
without producing a cache write.

The shell assembly lives in the shared package next to `hydrateThread`
(`reducer.ts`, exported, `threadModelFromCache(record, now)`), because
`reducer.ts` owns every `HistoryState` invariant and the mobile store can
adopt the same function later.

### The write seam: a subscription, not a funnel

There is no single publish funnel for open-pane models. Publications flow
through `putThreadModel` (read merges, reconnect invalidations),
`putThreadModels` (clear), and the notification handler's direct
`threadsStore.setState` (every live `history/updated`, `thread/status/changed`
and friends, which is most of a live session's content). A hook on any one
function would miss the others. The write seam is therefore a **subscription
to the store's `threads` map** that debounces per ref (1 s trailing with a
5 s max-wait, both injected) — a trailing-only debounce would starve a
continuously streaming session until quiescence, and a crash would then
lose the whole live growth, not the tail. When the debounced callback
fires it re-reads the
store's current model for the ref and applies every gate then — never a
model captured at scheduling time — so a write scheduled before a clear,
a delete, or an invalidation evaluates against the state that exists when
it fires. The gates:

- `history.incarnation` is present (a completed v6 content-bearing read),
- the ref's shell flag is clear (the shell skip, above),
- the ref is not in `deletedRefs` (the deletion fence, re-checked at fire
  time),
- the ref is not in the clear-suppression set (a ref a clear suppressed:
  re-checked at fire time, ended only by the ref's final release, so a
  pane still open across a clear cannot re-persist its pre-clear content
  under the new epoch — a deliberate re-open establishes the fresh lease
  the clear section defines),
- the history is live: `invalidatedAtGeneration` is unset, the one marker
  whose presence means the next latest-window response replaces rather
  than merges (the same single-field test the reducer's own liveness
  check makes). The markers travel together — the reducer's invalidation
  entry always sets `invalidatedAtGeneration` and `awaited`, adding
  `pendingIncarnation` only when the invalidation names a different
  incarnation, and its deferred pages accumulate only inside that state —
  so an invalidated, reconnect-resyncing, or identity-awaiting history is
  never persisted until the resync read settles it. A settled-but-stale
  pair is ordinary cache content; a mid-transition one is not,
- `history.failed` is unset.
- the adapter's connection is open. A write firing while the connection
  is still opening skips; the next debounced publication retries it.
  Without this gate a write could pass every check above, await the
  late open, and hand a deletion the third interleaving the invariant
  below excludes — its transaction deleting nothing because the record
  is not yet written, the write then committing the ref back.

Gate evaluation and the transaction's open are one synchronous step — no
await sits between them — and the connection gate is what makes that a
fact rather than an assumption: the lookup's 250 ms race already treats
a still-opening connection as a timeout, and the write side now matches
it, opening its transaction on an already-open connection or not at
all. So a deletion (the action's success handler,
the fence, or a received per-ref message) meets a write in only two
orders: its task runs before the callback's, and the gates refuse the
ref, or it runs after the transaction opened, and IndexedDB's
serialization places the deletion after the write, where it removes the
record the write just committed. A timer that already fired but has not
yet run is the queued shape of the first case: cancelling it cannot
unqueue its task, which is exactly what the fire-time `deletedRefs` gate
refuses.

The subscription sees every publication, so a `history/updated` that grows
the recorded history refreshes the record like any read merge.

The flush is load-bearing, not a nicety: live content arrives by
notification between reads, and a tab closed without a flush (a crash, a
killed tab) loses the tail. `releaseThread` flushes a pending write, and
so does the pinned-mutation drain (`dropUnpinnedModel`, the path a
pinned ref's model finally leaves through, since `releaseThread` returns
early while pinned). The flush runs **before** removal: both paths
snapshot the model, evaluate the gates on that snapshot, write, and only
then let the model leave the map — a flush after removal would read an
empty map and drop the tail on every graceful close. A tab destroyed
anyway loses its tail: accepted, because the
lost tail costs only a larger first delta on the next reload, never
correctness. Correctness never depends on write freshness: a record is a
`(model, identity)` pair written atomically, and the server reconciles any
older pair with a proportionally larger `changes`.

Three events invalidate rather than write:

- **Clear** (`applyClearResponse`): the cleared model is a bare hydrate with
  no history, so it would never match the write gates. The cache deletes the
  ref's record in the same step, or a cleared session's next reload paints
  pre-clear content from the shell.
- **Session delete** (the UI's shared `deleteSession` action): the record
  is deleted on the action's success path (`result.deleted`), the ref
  joins `deletedRefs` in the same step, mapped the way
  `closePanesForDeletedSessions` maps it — `result.deleted` carries
  bare thread ids, not pane refs, so an id already carrying a source
  prefix passes through and any other becomes the `local:<id>` ref, or
  the write gate never matches a `local:` pane — the deleting tab
  arms its own
  fence, because a BroadcastChannel never delivers the sender its own
  message — and the ref's pending debounce timer is cancelled with it.
  The flush skips refs closed by deletion, so closing a deleted
  session's pane cannot re-persist it. Deletion also propagates cross-tab: one BroadcastChannel
  message per deleted ref, the same channel and the same shape the clear
  uses. On receiving it a sibling does two things, not one: it adds the
  ref to its suppression set — re-checked at write-fire time — and it
  deletes the ref's record from storage, idempotently. That second step
  heals the one interleaving that can beat the message: a sibling write
  that started after the deleting tab's removal but before the message
  arrives still passes its gates and re-creates the record, and the
  message's arrival deletes that resurrection in the same step that
  arms the suppression — a heal that cannot be lost, because the
  message's delete transaction cannot start until an in-flight write's
  transaction completes, so it always runs after the record it must
  remove. The remaining residual is a sibling that dies
  between its racing write and the message's arrival: nothing is left
  to hear the message, and the record it re-created waits for the
  14-day expiry or the next fenced read of the ref — the same stated
  residual class as sessions no tab ever reads again. That bounded
  window is the argument against the durable per-ref tombstone a
  reviewer proposes: a durable per-ref generation checked inside every
  write transaction is machinery for a window measured in channel
  latency, the clear epoch stays the only durable generation, a deleted
  session's pane releases on close, and a re-open of a deleted session
  fails its read through the existing fence, which keeps the record
  gone. A record delete whose transaction aborts leaves the stale
  record to that same fence — the deleted session's own re-open
  triggers it — or to the 14-day expiry; the durable fact is the
  server-side deletion, and the fence is the cache's correction, so
  the deletion never depends on the cache transaction succeeding. An earlier draft of this spec refuted a per-ref tombstone as
  over-building and claimed the sibling "learns of the deletion through
  the fence on its next read" — that retraction is recorded because it
  was wrong: a sibling tab holding the session open receives
  `history/updated` pushes, not a rejected read, so the fence never
  fires there and its continuously scheduled writes would resurrect the
  record indefinitely, which is not the one-time residual the draft
  claimed.
- **The deletion fence** (`markThreadDeletedIfFenced` setting `deletedRefs`):
  deletes the ref's record, cancels its pending debounce timer, and
  publishes the same per-ref propagation — out-of-band deletions (another
  machine, a project delete) must reach the tab that holds the session
  open just as UI deletions do, and the `deletedRefs` gate above makes
  every later fire-time write refuse the ref. A record delete whose
  transaction aborted retries on the fence's next firing — the ref is
  already in `deletedRefs`, so the retry is idempotent and costs at
  most one stale shell on a re-open. Records for sessions no tab
  ever reads again are removed by the 14-day expiry; that residual is
  stated here rather than papered over.

### Scroll-back: the deepest held cursor

Older pages merged by `mergeOlderItemPage` land in `history.turns`, so the
record's `turns` covers window plus everything paged in, and `olderCursor`
marks where scrolling continues. The existing reducer rule already keeps
that cursor across a merge when held turns extend below the fresh window,
and a replace disposition resets it. With that rule, a reload renders every
previously fetched page from the shell, and `loadOlderTurns` continues from
where the last session left off: the pages the user already read are never
requested again, on reload or after a reconnect re-read. A cursor the server
no longer honors answers with `TranscriptItemCursorStale`; the existing
refresh path replaces the window and the cursor with it.

The shell qualifies that with one rule: while the shell flag is set — the
first authoritative read still pending — `loadOlderTurns` is disabled.
Paging below a shell the gap rule is about to replace races that
replacement, and the reconciled window owns the cursor afterwards: a
merge keeps the deepest held cursor by the existing rule above, a
replacement takes the response's. Scroll-back therefore waits for
whichever window wins and never fetches against a cursor that is about
to be discarded.

### The two serving paths, the live gap, and its rule

A record's turns are contiguous by construction: pages are fetched
sequentially backward from the window, and every read merge keeps the
newest boundary. The two serving paths differ in what fills the space
between that boundary and a later window:

- **Daemonless** (past sessions, the hub's `transcriptindex`): the
  reconciling read honors `heldSnapshot` and returns window plus `changes`,
  and `changes` reaches every item past the held length, so every gap the
  session's growth opened is filled in the same response. A reload costs the
  window plus `changes`; held pages are version-refreshed in place.
- **Daemon-served** (live sessions): the read ignores `heldSnapshot` and
  returns the window only. If the session grew by less than a window, the
  fresh window overlaps the record's newest boundary and the merge is
  gapless. If it grew by a window or more, the fresh window starts entirely
  above the record, nothing on the live path can fill the middle (live
  notifications push only newer items; back-paging goes the other way), and
  the daemonless `changes` backstop arrives only at shutdown.

The design therefore adds one store-level rule at the shell's reconcile
point, where it owns the risk: **a cached-shell reconciling read that
carries no `changes` and whose fresh window starts entirely above the
shell's held turns is applied as a replacement (`hydrateThread`, the path
the stale-snapshot retry already uses), not a merge.** The pages are
dropped, the response's cursor is taken, and the user re-pages if they want
the older content back — the honest alternative to rendering a transcript
with a silent hole in it. A response carrying `changes` merges as usual.
The predicate is exact, and its anchor is captured, not derived. The
shell's lineage state records, at shell-build time, the newest item
position in the record (`threadModelFromCache`'s input — pure record
data, fixed before any live merge can touch the model). The rule: it
applies only when the read's disposition is `merge` (the existing rules
already answer replace and discard), the record held at least one item
(an empty record takes the ordinary cold merge), and the fresh window's
first item position is strictly greater than that captured position —
`comparePositions` on `ThreadItemPosition`, position against position.
One more term guards the live fold: the response's newest item position
must be at or above the model's current newest. A `history/updated`
landing while the read is in flight advances that newest past a
response cut before it — the captured anchor cannot see it — and a
replacement whose window ends below folded content would move the
transcript's newest backward and discard a turn the daemon itself just
pushed. When the term fails, the fold is newer than the response, and the held
turns cannot be assumed contiguous from the anchor — a fold pushes
only its own items and does not fill the gap a window starting above
the anchor opens — so an ordinary merge could preserve exactly the
hole this rule exists to prevent, and the first authoritative read's
publish clears the shell flag, which would let the write seam persist
that hole. The rule therefore still replaces, then replays: the
window applies via `hydrateThread`, and the model's items above the
window's end — the fold tail; they cannot be cached pages, since the
anchor sits below the window's start — merge onto it. The result is
contiguous from the window's start through the folded newest; the loss
below the window's start is the rule's stated re-pageable cost, and a
gap inside the fold tail itself — a dropped notification between two
folds — is the live path's ordinary staleness, healed by the next
read, not a cache regression. The fold is data already in hand, so
the replay needs no deferral machinery. An earlier draft keyed the anchor on "the newest held item below
`history.length`"; that was dimensionally wrong — `SnapshotIdentity.length`
is the transcript's covered byte count (the index's own Window doc:
"the transcript bytes it covered"), not an entry ordinal, so comparing
a position ordinal against it degenerates to the newest held item,
which is exactly what a post-failure live fold can push past a real
hole. The captured position cannot move: it is read from the record
before the shell publishes, a `history/updated` merge only adds items
to the model (it never rewrites the lineage state), and
`history.length` itself is written only by a read response's identity
(`readIdentity`) — a merge returns `{ ...held, turns }` without
touching it. One
cost is accepted and stated: a window that begins exactly at the
captured position's successor replaces too, because the position model
has no predecessor function to distinguish one-item adjacency from a
one-item hole — the cached pages re-fetch rather than risk a hole, a
bounded loss of one window's worth of growth. The check lives at a
seam the design already owns; no reducer or protocol change is
involved.
### Eviction, cap, and cross-tab

- One IndexedDB database per hub origin (browsers scope IndexedDB to the
  origin), `evener-session-cache`, with two object stores: `records`,
  keyed by `ref`, and `meta`, holding one `{bytes, savedAt}` row per
  record — the enumeration surface, so the cap never reads a record body —
  plus the clear-epoch row under a reserved key. Both stores are written
  in the same transaction as the record they describe.
- `SESSION_CACHE_MAX_BYTES = 32 MB`, with no per-record cap. A per-record
  ceiling with oldest-page trimming was cut: a trimmed record would keep
  one `olderCursor` naming a boundary below the dropped pages, cursors are
  opaque server tokens the client cannot mint, and restoring the invariant
  would mean persisting a per-page cursor chain — machinery the total cap
  makes unnecessary.
- One oversize rule closes the loop the cut left: a record whose own
  serialized length exceeds `SESSION_CACHE_MAX_BYTES` is skipped whole,
  never written, and a stored row for the same ref is deleted with it —
  a session that outgrew its cache leaves nothing stale behind and keeps
  today's behavior. A proposed window-only fallback for oversize records
  was rejected with the same evidence as the trimming cut: the model
  carries no window-boundary marker (`mergeOlderItemPage` folds pages
  into one flat turns array), so a "window-only record" needs persisted
  page-boundary metadata — machinery for a rare case whose accepted cost
  is one session keeping today's behavior. Without this rule a
  monster record would evict every neighbor forever and still not fit.
  The meta row's `bytes` is the encoded length of the stored record, and
  the record body carries no `bytes` field of its own: a length stored
  inside the body it measures cannot be exact, because its own digits
  change the body's length. The cap and the oversize rule both count the
  meta row's value, exact by construction. The cap bounds payload
  bytes; IndexedDB's structural overhead rides above it and is bounded,
  finally, by the browser quota and the clear action.
- Records expire `SESSION_CACHE_TTL_DAYS = 14` after their last write
  (`savedAt`). An expired record is dead everywhere: the load seam reads
  it as a miss and deletes the row it found, and the same enumeration
  that enforces the cap removes the rest — at every adapter open and
  inside every write transaction. The guarantee is exact: an expired
  record is never served and does not survive any storage access (an
  open, a write, or a read that finds it). Between accesses its bytes
  may sit on disk in a long-lived tab until the next access touches the
  store. The cap bounds volume; the TTL bounds lifetime. No in-app sweep
  timer is added for the between-accesses window — it would be a timer
  whose only job is to hasten a deletion the next storage access performs
  anyway.
- Every removal — a delete, an expiry, an eviction, an oversize skip —
  removes the `records` row and its `meta` row in the same transaction, so
  accounting never leaks a deleted record's bytes, and both the cap's
  and the expiry's enumerations skip the reserved epoch key. That row is
  not a record's meta row and carries no `savedAt`, so the epoch is
  exempt from expiry by construction: a 14-day-old clear still kills
  writes scheduled before it, and the epoch is never evicted or
  double-counted.
- The cap is enforced atomically per write, and the clear epoch is
  verified in the same transaction: the durable epoch read, the expiry
  deletions, the enumeration of the `meta` store's rows, the insert, and
  any evictions run in one read-write IndexedDB transaction. A write
  carries the epoch it was scheduled under and aborts — writing nothing —
  when that transaction observes a newer one. IndexedDB serializes
  read-write transactions on the same stores, so the interleaving has
  exactly two orders: a write transaction that runs after the clear's
  reads the incremented epoch and aborts, and one that runs before it has
  its records deleted by the clear's own sweep. No scheduled write can
  commit a record after the clear, whatever the in-memory timing. The
  channel message that carries the epoch to sibling tabs is a latency
  optimization, not the guard — delivery can lag arbitrarily, and the
  in-transaction read is what makes a pre-clear write harmless.
  A write transaction that observes a newer durable epoch updates the
  tab's in-memory epoch view and arms the suppression the missed
  channel message would have armed: every open lease whose captured
  epoch predates the observed one is suppressed through its final
  release, so a tab that never receives the message still cannot
  re-persist pre-clear content — the aborted write is itself the
  tab's proof that a clear happened, and a deliberate re-open after
  release starts a fresh lease and caches again, exactly as if the
  message had arrived. The message speeds the view up, but no lease
  waits on it.
- Cross-tab: last write wins per ref. A tab holding only the window can
  replace a sibling's deeper record; the dropped pages simply re-fetch on
  a later reload. This design deliberately rejects a no-shrink comparison
  rule: it inverted the LRU (deep records' `savedAt` stopped advancing and
  they evicted first), and an incarnation-only comparison froze records at
  content a same-incarnation epoch or boot-generation replacement had
  already superseded. Any older record still reconciles correctly; none of
  this is correctness machinery, so it stays simple.

### The clear-cached-sessions setting

Settings gains a storage row with one action, "Clear cached session
content". The row renders a state, not an estimate: **empty**,
**unavailable** (a failed open or an aborted clear transaction, with a
retry — never shown as empty, so the privacy remedy cannot silently
claim to have worked), or **cleared** — which shows only when the
clear's transaction committed, and yields the moment that fact goes
stale: the next record this tab writes, a reload, or the settings pane
rendering the row again — its enumeration runs per render — recomputes
it, so a cleared badge never misstates the remedy the way an empty
badge over a failed open does. What it reports is this tab's own last
clear, deliberately not a cross-tab lock: cache writes do not
broadcast, so a sibling re-opening a session refills the shared store
silently, and a cleared badge over that refill is bounded staleness of
a report — the clear did empty the store — until the row renders
again. Broadcasting writes for a settings badge would add a message
type the cache does not otherwise need.
The clear is durable against racing writers, in the only order that
works, since in-memory timer state cannot commit transactionally:
first, synchronously and in memory, bump the local epoch view, arm the
suppression, and cancel every pending debounce timer. A write scheduled
before this moment dies by the timer cancellation if it has not yet
fired, by the in-transaction epoch check if it fired but its transaction
is still to run, or by the suppression gate if its callback is racing
this synchronous step; a write scheduled after it carries the new
epoch, so the gate that stops it is the suppression one. Then one
read-write transaction deletes every record — including anything the
race let slip through and commit — and increments the durable clear
epoch held in the store's `meta` row. Every write transaction
carries the epoch it was scheduled under and aborts when its own
transaction reads a newer one (the eviction section defines the
mechanism), so a sibling tab's write that started before the clear cannot
commit a record after it. The BroadcastChannel message (the crossTabSync
pattern the tree already uses) carries the epoch so their scheduled
writes drop the same way, and arms each sibling tab's suppression set
for the leases whose captured epoch predates the clear — the same
per-ref arming the deletion path uses. The message is sent only after
the transaction's commit is observed, never on abort — a sibling never
arms suppression for a clear that did not happen, so an aborted clear
leaves no sibling state to revert. A lease records the durable
epoch its arming lookup observed (the load seam's capture), so the
arming is exact: a ref still open across the clear captured an older
epoch and is suppressed — a sibling still receiving `history/updated`
pushes cannot re-persist its pre-clear pages under the new epoch
either — while a ref closed before the clear and re-opened after its
transaction committed captured the clear's own epoch and is a fresh
lease by construction, not suppressed, so the re-open starts caching
again even if the message has not arrived yet. The suppression is the
fire-time gate above, not a UI state. It attaches to the leases a
clear found open — those whose captured epoch is older — and ends with
each one's final release: a ref deliberately re-opened afterwards
establishes a fresh cache lease and caches again — that is the feature
working, not the remedy failing — while the durable epoch still kills
anything scheduled before the
clear. Deletion is the storage adapter's own operation so a wedged
open degrades to the unavailable state rather than a failed button.
A clear that never reaches a definite commit is a clean no-op in every
tab, because IndexedDB commits the deletions and the epoch increment
as one unit and the broadcast is commit-gated: nothing changed
anywhere. There is no third state to handle: an IndexedDB transaction
has exactly two definite terminal states, an open or transaction
creation that fails is an abort that happened before there was a
transaction to abort, and every request settles — there is no
timeout whose commit status is unknown, so the rule is simply revert
unless a commit is observed. Even a revert taken over a commit that
landed unobserved self-heals: the tab's in-memory epoch sits below the
durable one, its next write aborts on the in-transaction read and
arms the suppression, and the row's re-clear is an idempotent no-op.
The one path with no terminal event at all — a wedged transaction,
the same pathology as the wedged open above — degrades the same way:
the row never hears its commit and stays **unavailable**, and the
premature suppression decays with final release of each open ref —
bounded staleness of a report, not a silently blocked cache. The row
returns to **unavailable** with its retry and the
clear action reverts what it did optimistically in memory — the epoch
bump restores the durable value and the suppression it armed disarms,
so open refs resume caching at their next publication (a cancelled
debounce timer simply re-arms on the next write-scheduled
publication). The cache never claimed the content was gone, so
nothing it did not claim can resurrect. What the row called
**cleared** is a committed fact, and a per-record cache epoch would
add nothing: an atomic transaction leaves no state in which the epoch
advanced while a record survived. No per-session management
and no cap slider; both are YAGNI until someone asks.

### Failure and degradation

The adapter copies the outbox adapter's failure discipline
(`mutationOutboxIndexedDB.ts`): injected `IDBFactory`, a storage timeout with
the neutral message, an observational `onOpenDiagnostic` seam (defaulting to
one greppable `console.warn` line, silent in tests via the same
`import.meta.env.MODE` branch), and version-fence `VersionError` handling.
Every failure is a cache miss and the 250 ms load-seam deadline above bounds
the open path. No transcript surface shows a storage error, and the loader
path is exactly today's. Quota errors during write drop the write; the next
debounced window retries, and a persistently full profile simply stops
caching until the clear action runs. Test debounce uses fake timers'
`advanceTimersByTime` on the injected interval (never `nextMacrotask`,
which never resolves under faked `setTimeout`).

## What the cache does not change

The latest window is always on the wire. A `thread/read` with `includeTurns`
returns the window by protocol contract, held snapshot or not; the 40-item
window is the authoritative latest state and re-arrives on every reload. The
cache removes the loader wait and the scroll-back re-fetches, not that one
bounded read. An additive protocol extension that lets an unchanged window
be omitted is a possible follow-up; it is out of scope here and the design
must not depend on it.

## Security and privacy

The cache writes transcript content to browser-local disk, where it sits until
evicted or cleared. Transcripts can contain secrets that passed through tool
output. Three facts bound the exposure:

- The hub already serves this exact content to this exact origin over the
  socket; the cache stores a subset of what the tab has already received.
- The mutation outbox already persists composer inputs (queued send and
  steer intents, with their text) in the same storage, so the app already
  writes user content to IndexedDB today. Drafts, by contrast, live in
  localStorage (`draftStorage.ts`), which is also unencrypted browser-local
  storage.
- The cap bounds the footprint; a session deleted from this UI removes its
  record on the spot, and the per-ref propagation deletes a racing
  sibling's resurrection the moment its message lands (a sibling that
  dies in that window leaves the stated 14-day residual); the deletion
  fence removes records a read proves deleted; and the setting clears
  everything in one action.

What the cache deliberately never holds: credentials (auth lives in the hub
and the outbox's own rows), attachments, drafts, and the human-client
`pendingEscalations` set. Out-of-band deletions that are never re-opened
rely on the cap, the 14-day expiry, and the clear action, as stated in the
eviction and write-seam sections. A shared
browser profile shares the cache, the same way it shares every origin's
storage; that is a browser-account boundary, not an app one. The hub this
design targets is single-principal: the appwire protocol carries no user
or account identity anywhere in `thread/read` — the only "account" on the
wire is a provider billing id — and the hub's auth gates the owner's
devices, not distinct humans, so every authenticated device of the origin
sees every session and there is no principal to partition records by. A
future multi-principal hub must revisit this section before shipping the
cache. If a deployment serves multiple people behind one browser profile
today, the clear action is the remedy and the deployment should not rely
on the cache being private.

## Testing (TDD)

All tests are deterministic: the fake client (`@evener/appwire-client/testing`)
scripts socket responses, `new IDBFactory()` fakes storage exactly as the
outbox tests do, fake timers drive the injected debounce interval, and no
test touches the network or sleeps
(`docs/developing-evener/testing.md`). Per-test ceiling 3 s.

Adapter (new `stores/sessionCacheIndexedDB.ts`):

1. Round-trip: a write is readable with identical history and identity;
   the meta row's `bytes`/`savedAt` update; a second write for the same
   ref replaces.
2. Cache miss and bounded open: an absent ref returns nothing; a stalled or
   failed open (timeout, blocked, VersionError) returns nothing and never
   throws; the diagnostic seam records the open failure; a lookup still
   resolves as a miss at its own 250 ms deadline; a record past
   `SESSION_CACHE_TTL_DAYS` reads as a miss and the row it found is
   deleted; a lookup that began before a clear discards its result.
3. Cap and expiry: writing past `SESSION_CACHE_MAX_BYTES` evicts the
   least-recently-saved record; the enumeration reads the `meta` store's
   rows without touching record bodies, and enumeration, insert, and
   eviction share one transaction (a sibling tab's interleaved write is
   either fully seen or fully unseen); a record whose own length exceeds
   the cap is skipped whole and its stored row deleted; a record past
   `SESSION_CACHE_TTL_DAYS` expires at adapter open and inside every write
   transaction; the reserved epoch row survives both enumerations when
   every record expires around it; a forced write during quota failure
   drops silently.
4. Clear: deletes every record and resets the accounting; an aborted
   clear transaction changes nothing — every record and the epoch
   remain, the row reads **unavailable**, the suppression disarms, and
   a reload serves the content again: the honest no-op.

Store integration (`stores/threads.test.ts` additions and a new
`stores/threads.sessionCache.test.ts`):

5. Reload replay, daemonless, the acceptance case: hydrate a ref against a
   scripted server, let the debounced write land, then simulate the reload
   (fresh store, same adapter). The pane paints the recorded turns from the
   cache before any read resolves; the next `thread/read` carries
   `heldSnapshot` matching the record and a request generation above the
   record's `issuedGeneration`; the scripted response (fresh window plus
   empty `changes`) merges without dropping or duplicating items, and the
   window's arrival is asserted rather than assumed away.
6. Reload replay, live, growth under a window: the scripted reply carries
   the window and no `changes`; an overlapping window merges, the held
   pages below survive, and the deepest cursor is kept (this also pins the
   existing reducer rule in the reload scenario; it lands green as a
   characterization of 792c379eca). A window beginning exactly at the
   captured position's successor replaces instead — the accepted abut
   cost — dropping the pages, and the scenario asserts that outcome too.
7. Reload replay, live, growth past a window: the fresh window starts
   entirely above the record's captured position; the reconcile applies as a
   replacement; the pages are dropped, the response's cursor is taken, and
   the rendered transcript has no gap. The same scenario with a failed
   first read and a live `history/updated` folded onto the shell before
   the retry succeeds: the folded items cannot move the captured anchor;
   the retry replaces when its window covers the fold — its response
   cut after the fold — and when the fold is newer than the response
   it replaces and replays the fold tail onto the fresh window, so the
   newest boundary never regresses and no hole persists either way;
   a window starting above the anchor with a fold above the window's
   end demonstrates the replay — the version-refresh merge an earlier
   draft allowed here left a hole the write seam could then persist.
8. Stale identity: the server rejects the held snapshot
   (`TranscriptItemCursorStale`); the store retries without it and history
   is fully replaced; the cached record's turns do not survive the
   replacement.
9. Cached shell safety: the shell's capabilities admit no composer action
   before the authoritative read lands; a Clear from the shell fences on
   the cached `threadId`; the shell and its bumped base publish without a
   write, and the first read merge resumes writes. A failed reconciling
   read keeps the content visible and writes nothing — including after a
   live notification has folded onto the shell, which is the case the
   shell flag exists for — and the retry's success resumes writes from a
   verified model. A history-failed answer shows the one diagnostic and
   refuses writes.
10. Watch exclusion: a rich watched upgrade (`includeTurns`) publishes a
    turns-bearing model into `watchedThreads` and produces no cache write.
11. Live-notification capture: a `history/updated` merge (the notification
    handler's direct `setState`) refreshes the record without any
    `putThreadModel` call, pinning the subscription seam; and a rapid
    sub-second notification run still writes within the max-wait, so a busy
    session's record does not starve until quiescence.
12. Scroll-back, the volume case: a ref hydrated, paged back twice,
    reloaded; the pane renders window plus both pages from the shell;
    `loadOlderTurns` requests only the page before the cached boundary (the
    fake client asserts the cursor it received); the pages already held are
    never requested again. A `loadOlderTurns` issued while the shell is
    unverified fetches nothing; after the reconcile — merge or
    replacement — it continues from the settled window's cursor, with no
    hole or duplication.
13. Load-seam races: a never-resolving open with a concurrent second
    `ensureThread` (which joins the shared lookup, never double-arms), a
    release during the lookup (publishes nothing), a lookup that lost its
    own deadline race resolving late (publishes nothing, arms nothing),
    a same-tab clear committing between the lookup's transaction and its
    publish discarding the result through the second durable read, one
    committing after that read but before the publish discarding it
    through the in-memory epoch check, a sibling-tab clear in that same
    window publishing a stale shell that never re-persists and that the
    next fenced read replaces, and
    hydration still completing within the 250 ms bound.
14. Deletion and clearing, one focused test per interleaving rather
    than one bundling them (the per-test ceiling makes a monolith opaque
    and its failures unreadable). (a) Deletion — a `deleteSession`
    success removes the record and cancels the ref's pending debounce
    timer; the deletion propagates through the channel, and a sibling
    tab holding the session open — continuously receiving
    `history/updated` and scheduling writes — stops writing without a
    reload and never re-creates the record; a write scheduled before
    the fence or delete and firing after it is refused by the
    `deletedRefs` gate; the deletion fence removes the record on a
    fenced read; the release flush skips a ref closed by deletion; in
    the deleting tab, a debounce callback whose task was already
    queued when the action's success handler ran is refused by its own
    `deletedRefs` arming, and a write whose transaction was already
    open commits first only for the deletion serialized after it to
    remove it; a record delete whose transaction aborted is retried by
    the fence's next firing and costs at most one stale shell on a
    re-open; a deletion message received by a sibling holding the ref
    open both suppresses its writes and deletes the record a racing
    write re-created before the message arrived. (b) Clear ordering —
    the settings clear empties the adapter in the specified order
    (in-memory epoch and timer cancellation before the awaited
    transaction); a write scheduled under an older epoch skips itself,
    including a timer armed before the clear and firing after it; a
    write whose transaction runs after the clear's aborts itself on
    the in-transaction epoch read; the channel message is observed only
    after the transaction committed, and a clear that never reaches a
    definite commit — an aborted transaction, a failed open, a
    failed transaction creation — sends none and reverts its
    in-memory effects the same way, so a sibling never arms
    suppression for a clear that did not happen and this tab's open
    refs resume caching. (c) Suppression lifecycle — an open
    pane's post-clear notification write is refused by the suppression
    gate; a sibling tab armed by the clear's channel message refuses
    its next scheduled `history/updated` write without a reload; a ref
    the sibling closed before the clear and re-opened after its
    transaction committed is not suppressed — its lease captured the
    clear's epoch — and caches again; the suppression ends with the
    final release of each open ref, and a deliberate re-open of a
    cleared ref starts caching again. (d) The missed message —
    repeated notifications between a tab's first aborted stale write
    and the delayed channel delivery never re-persist the ref: the
    aborted write armed the suppression itself, so a tab that never
    hears the message still stops.
15. Write gating: `failed` history writes nothing; an invalidated
    history — `invalidatedAtGeneration` set, with `awaited`,
    `pendingIncarnation`, and a deferred page present — writes nothing
    until the resync read settles it and the settled pair then persists
    (the delayed-reconciliation case); a write firing while the adapter's
    connection is still opening skips and retries on the next debounced
    publication, so a deletion landing during the open window meets no
    in-flight write — the invariant's precondition; a zero-turn
    record's reload takes
    the ordinary cold merge (the gap rule's empty case).
16. Flush: `releaseThread` commits a pending debounced write, ordered
    before removal (the gates evaluate on the pre-removal snapshot); the
    pinned drain (`dropUnpinnedModel`) commits it too; a flush skipped
    because the connection is not yet open is the crash-loss class —
    the session re-hydrates on the next open; a reload after an
    unflushed crash still reconciles to the same content.
17. Degradation: the same replay with an adapter that always fails leaves
    behavior identical to today's reload (full window read, no heldSnapshot,
    scroll-back pages fetched as before).

Settings: the row renders empty vs unavailable truthfully and the clear
action works (scenario 14 covers the adapter side); `cleared` exits on
the next write, a reload, or a re-render of the row — its per-render
enumeration keeps a sibling's refill from outliving the next render.

Reducer: `threadModelFromCache` is pinned by a type-level test that no
required `ThreadModel` field is left undefined, and by unit tests that the
transient invalidation fields are reset and the history identity round-trips.

## Decisions (locked)

1. **IndexedDB, not the HTTP cache.** The socket bypasses the HTTP cache by
   construction; an ETag'd HTTP read endpoint would fork the read path the
   daemon, hub, mobile store, and TUI all share and still pay a round trip
   per read. Ruled out.
2. **Cache at the store seam, not inside the reducer.** The reducer stays
   pure; persistence is a store-layer subscription so the same reducer
   serves consumers that opt out.
3. **Persist the recorded history plus display fields only.** Live state,
   instance identity, and escalation cards are never written.
4. **The cached shell is action-disabled for capability-gated actions until
   the first authoritative read**; non-gated actions still fence on the
   cached `threadId`, and scroll-back waits for that read — its cursor
   belongs to the window the reconcile settles on.
5. **Open-pane models only**, enforced by the write seam's threads-map-only
   placement; watched/rail reads never load or write.
6. **32 MB whole-record LRU cap, one atomic read-write transaction per
   write, a durable clear epoch every write verifies, cross-tab
   last-write-wins.** A no-shrink rule was considered and
   rejected: its failure modes (LRU inversion, frozen superseded content)
   cost more than the pages it saved. A per-record cap with oldest-page
   trimming was cut with it: trimming dangles the single `olderCursor`
   below the dropped pages, and repairing it needs a per-page cursor chain
   the total cap makes unnecessary.
7. **Zero reducer changes.** The deepest-held-cursor rule already exists
   (792c379eca) and is pinned, not re-implemented. The one new behavioral
   rule — a no-`changes` reconcile whose window starts above the held pages
   replaces instead of merging — lives in the store's shell seam, where the
   design owns the risk. Everything else is additive: the adapter, the
   store subscription, the bounded load seam, the shared
   `threadModelFromCache`, the invalidation hooks, and the settings row.
8. **The live-session gap rule over a protocol extension.** Fixing the gap
   client-side at the shell seam costs a pair of position checks; teaching the
   daemon `heldSnapshot` is a protocol change with four consumers.

## Out of scope

- Mobile-native adoption of the same port (follow-up; the seam exists).
- Offline mode: serving cached content while the hub is unreachable, with a
  staleness banner. The shell mechanism makes it possible later; nothing here
  depends on it.
- Daemon-side `heldSnapshot` support or omitting an unchanged latest window
  from the wire (additive protocol extensions).
- Caching anything already served with HTTP caching (assets, images, docs).
- Per-session cache management, cap configuration, encryption at rest.
- Caching the rail/navigation reads, which the generation/revision delta
  protocol already keeps small on the wire.
