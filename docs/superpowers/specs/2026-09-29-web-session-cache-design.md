# Web session-history cache (IndexedDB)

**Date:** 2026-09-29
**Branch:** `web-session-cache`
**Status:** design (revision 3, after two adversarial review rounds) → review → implementation
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
  bytes: number;             // serialized encoded length, for the cap
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
  (`STORAGE_WAIT_MS = 10_000`). On deadline, open failure, or miss, the
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
to the store's `threads` map** that debounces per ref (1 s trailing, the
interval injected). When the debounced callback fires it re-reads the
store's current model for the ref and applies every gate then — never a
model captured at scheduling time — so a write scheduled before a clear,
a delete, or an invalidation evaluates against the state that exists when
it fires. The gates:

- `history.incarnation` is present (a completed v6 content-bearing read),
- the ref's shell flag is clear (the shell skip, above),
- `history.failed` is unset.

The subscription sees every publication, so a `history/updated` that grows
the recorded history refreshes the record like any read merge.

The flush is load-bearing, not a nicety: live content arrives by
notification between reads, and a tab closed without a flush (a crash, a
killed tab) loses the tail. `releaseThread` flushes a pending write, and so
does the pinned-mutation drain (`dropUnpinnedModel`, the path a pinned
ref's model finally leaves through, since `releaseThread` returns early
while pinned). A tab destroyed anyway loses its tail: accepted, because the
lost tail costs only a larger first delta on the next reload, never
correctness. Correctness never depends on write freshness: a record is a
`(model, identity)` pair written atomically, and the server reconciles any
older pair with a proportionally larger `changes`.

Three events invalidate rather than write:

- **Clear** (`applyClearResponse`): the cleared model is a bare hydrate with
  no history, so it would never match the write gates. The cache deletes the
  ref's record in the same step, or a cleared session's next reload paints
  pre-clear content from the shell.
- **Session delete** (the UI's shared `deleteSession` action): the record is
  deleted on the action's success path (`result.deleted`). Without this the
  deletion fence below is the only cleanup, and it never fires for a
  session no tab reads again. The flush skips refs closed by deletion, so
  closing a deleted session's pane cannot re-persist it.
- **The deletion fence** (`markThreadDeletedIfFenced` setting `deletedRefs`):
  deletes the ref's record. This is the out-of-band cleanup — a session
  deleted on another machine, or by a project delete — and it only runs when
  a tab actually reads the deleted ref. Out-of-band deletions that are never
  re-opened keep their records until the cap evicts them or the user clears
  everything; that residual is stated here rather than papered over.

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
The predicate is exact, and it anchors on the verified watermark, not on
the newest item the shell happens to hold. The record's items all sit at
entry ordinals below its `history.length` (they came from a completed
window read and its pages); items a live notification merged after a
failed read sit at or above it and establish nothing. The anchor is
therefore the newest held item whose position is below the held
`history.length`. The rule: it applies only when the read's disposition
is `merge` (the existing rules already answer replace and discard), the
anchored set is non-empty (an empty record takes the ordinary cold
merge), and the fresh window's first item position is strictly greater
than the anchored item's position — `comparePositions` on
`ThreadItemPosition`, so only a window that starts above everything the
record verified replaces. One cost is accepted and stated: a window that
begins exactly at the verified boundary's successor replaces too, because
the position model has no predecessor function to distinguish one-item
adjacency from a one-item hole — the cached pages re-fetch rather than
risk a hole, a bounded loss of one window's worth of growth. The check
lives at a seam the design already owns; no reducer or protocol change
is involved.

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
  never written, and that session keeps today's behavior. Without this a
  monster record would evict every neighbor forever and still not fit.
  `bytes` is the serialized record's encoded length — the exact length
  the write stores, and what the cap counts.
- The cap is enforced atomically per write: the enumeration of the `meta`
  store's rows, the insert, and any evictions run in one read-write
  IndexedDB transaction, which the database serializes across tabs, so no
  writer evicts against a stale total.
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
**unavailable** (a failed open, with a retry — never shown as empty, so
the privacy remedy cannot silently claim to have worked), or **cleared**.
The clear is durable against racing writers, in this order, inside one
read-write transaction: delete every record, cancel every pending
debounce timer, and increment a durable clear epoch held in the store's
`meta` row. Every write transaction carries the epoch it was scheduled
under and skips itself when it sees a newer one — so a sibling tab's
write that started before the clear cannot commit a record after it.
The suppression set (every ref open in this tab until the next reload,
the write gate above) covers publications after the clear, and it
propagates to sibling tabs through one BroadcastChannel message (the
crossTabSync pattern the tree already uses) carrying the epoch, so their
open panes neither re-create records from new publications nor complete
stale scheduled ones. A fresh `ensureThread` of a ref the user
deliberately re-opens caches again — that is the feature working, not
the remedy failing. Deletion is the storage adapter's own operation so a
wedged open degrades to the unavailable state rather than a failed
button. No per-session management and no cap slider; both are YAGNI
until someone asks.

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
  record on the spot; the deletion fence removes records a read proves
  deleted; and the setting clears everything in one action.

What the cache deliberately never holds: credentials (auth lives in the hub
and the outbox's own rows), attachments, drafts, and the human-client
`pendingEscalations` set. Out-of-band deletions that are never re-opened
rely on the cap and the clear action, as stated in the write seam. A shared
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
   `bytes`/`savedAt` update; a second write for the same ref replaces.
2. Cache miss and bounded open: an absent ref returns nothing; a stalled or
   failed open (timeout, blocked, VersionError) returns nothing and never
   throws; the diagnostic seam records the open failure; a lookup still
   resolves as a miss at its own 250 ms deadline.
3. Cap: writing past `SESSION_CACHE_MAX_BYTES` evicts the
   least-recently-saved record; the enumeration reads the `meta` store's
   rows without touching record bodies, and enumeration, insert, and
   eviction share one transaction (a sibling tab's interleaved write is
   either fully seen or fully unseen); a record whose own length exceeds
   the cap is skipped whole; a forced write during quota failure drops
   silently.
4. Clear: deletes every record and resets the accounting.

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
   verified boundary's successor replaces instead — the accepted abut
   cost — dropping the pages, and the scenario asserts that outcome too.
7. Reload replay, live, growth past a window: the fresh window starts
   entirely above the record's verified items; the reconcile applies as a
   replacement; the pages are dropped, the response's cursor is taken, and
   the rendered transcript has no gap. The same scenario with a failed
   first read and a live `history/updated` folded onto the shell before
   the retry succeeds: the notification-merged items do not anchor
   contiguity, the retry replaces, and no hole persists — the watermark
   anchor's own case.
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
    `putThreadModel` call, pinning the subscription seam.
12. Scroll-back, the volume case: a ref hydrated, paged back twice,
    reloaded; the pane renders window plus both pages from the shell;
    `loadOlderTurns` requests only the page before the cached boundary (the
    fake client asserts the cursor it received); the pages already held are
    never requested again.
13. Load-seam races: a never-resolving open with a concurrent second
    `ensureThread` (which joins the shared lookup, never double-arms), a
    release during the lookup (publishes nothing), a lookup that lost its
    own deadline race resolving late (publishes nothing, arms nothing),
    and hydration still completing within the 250 ms bound.
14. Deletion and clearing: a `deleteSession` success removes the record;
    the deletion fence removes the record on a fenced read; the release
    flush skips a ref closed by deletion; the settings clear empties the
    adapter, cancels pending debounce timers, and increments the epoch —
    a write scheduled under an older epoch skips itself, including a
    timer armed before the clear and firing after it — the suppression
    reaches sibling tabs through the channel carrying the epoch (their
    open refs stop writing without a reload), and a re-open of a cleared
    ref starts caching again.
15. Write gating: `failed` history writes nothing; a zero-turn record's
    reload takes the ordinary cold merge (the gap rule's empty case).
16. Flush: `releaseThread` commits a pending debounced write; the pinned
    drain (`dropUnpinnedModel`) commits it too; a reload after an unflushed
    crash still reconciles to the same content.
17. Degradation: the same replay with an adapter that always fails leaves
    behavior identical to today's reload (full window read, no heldSnapshot,
    scroll-back pages fetched as before).

Settings: the row renders empty vs unavailable truthfully and the clear
action works (scenario 14 covers the adapter side).

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
   cached `threadId`.
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
   client-side at the shell seam costs one position check; teaching the
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
