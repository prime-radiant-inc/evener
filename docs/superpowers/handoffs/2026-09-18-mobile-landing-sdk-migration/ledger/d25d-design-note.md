# D25d design note: the phone adopts the web's durable pending-turn rows

Jesse's ruling 4 (2026-09-18): "submitted, not yet echoed" rows that survive reconnects and
reloads, and dedupe against the hub's echo. Measurement only, no code. Read: the migration plan's
D25/D25a-D28 rows (`docs/superpowers/plans/2026-09-12-sdk-migration.md`), the package modules now on
main (`appwire-client/typescript/state/mutation/{records,secureUUID,pendingEntries,pendingTurns,
outbox,dispatcher,projection,projectionWork,commitFeed,submission}.ts`), `mobile/src/state/
conversation.ts` (3043 lines), `mobile/src/services/conversation.ts` (1260 lines), and
`mobile-native/src/{draftRepository,draftLibrary,draftDocument,QueueSheet,screens,
ConnectionProvider}.ts(x)`.

## Current phone flow (five lines)

1. `state/conversation.ts`'s `send`/`steer`/`queue`/`interrupt` build an **in-memory**
   `ConversationMutationState` (`kind`/`status`/`draftSnapshot`/`draftRevisionAtSubmit`/
   `generation`/`mutationId` — `:302-311`), clear the draft, then call `service.send()` etc.
   directly. Nothing is written to durable storage before the RPC.
2. `services/conversation.ts`'s `decodeMutationResult` (`:379-475`) accepts only a receipt
   `disposition` of `"applied"`/`"replayed"` (`CANONICAL_MUTATION_DISPOSITIONS`, `:264`); anything
   else — including an outcome the daemon itself cannot vouch for — throws, and the store's catch
   treats every throw identically: `handleMutationError` (`:2979-3043`) marks the mutation
   `status: "failed"` (persists until the next mutation), restores the draft only if
   `draftRevision` hasn't moved since submit, and never retries.
3. Nothing survives an app kill or reload: `pendingMutation` is a plain Zustand field with no
   outbox, so a mutation in flight when the process dies leaves no record and nothing to redispatch.
4. `QueueSheet.tsx`/`screens.tsx` render only the hub's own authoritative `conversation.queue` plus
   a single `pending`/`pendingMutation.status === "pending"` busy flag (`grep pendingSend
   pendingMutation mobile-native/src` finds only busy-gating, never a rendered row) — there is no
   "submitted, not yet echoed" row the way web's `pendingTurnEntries` renders one.
5. On `actionUnavailable`, `requestRehydrate(ref)` re-reads the thread and lets the reducer
   converge; there is no `restoreProvenAbsent`-style reconciliation against an authoritative
   snapshot, because there is no durable outbox to reconcile.

## Target flow

Native adopts the same package stack the web already runs entirely through: `MutationOutboxStorage`
(port) over `expo-sqlite`, `MutationOutbox` for discovery, `MutationDispatcher` for serialized
per-ref dispatch with `blockedUnknown`/no-blind-replay, and `createPendingTurnsStore` +
`submitWithPendingTracking`/`createSubmissionRunner` for the projection and submission lifecycle.
`state/conversation.ts`'s four submit functions become thin callers into the dispatcher/submission
runner; `pendingMutation`/`pendingSend`/`ConversationMutationState` retire. The big finding: **the
dedupe mechanism already exists with zero new work.** `mobile/src/conversation/project.ts`'s
`MobileConversation extends ThreadModel`, produced by the package's own `hydrateThread` — the exact
same function the web uses — and `ItemModel.clientMutationId` (`appwire-client/typescript/model.ts:58`)
is already present at the model layer. `reconcilePendingEntries`/`awaitingFirstFrameSend` (package,
unchanged) already match an outbox/optimistic record against an identified item by that field, so
the moment the daemon's echo lands in the hydrated model, a pending row promotes/disappears with no
new matching logic to write.

## PR split (recommend 4; D25d-1 may need a sub-split — see below)

### D25d-1: native `MutationOutboxStorage` adapter over `expo-sqlite`
New module implementing the port's 13 methods (`enqueueIntent`, `listTargetRefs`, `getOutbox`,
`getOptimistic`, `listOptimistic`, `getRecovery`, `nextDispatchable`, `markAttempted`,
`markUnknown`, `settleReceipt`, `settleApplied`, `restoreProvenAbsent`, `transferToRecovery`),
following `draftRepository.ts`'s established pattern: a `DraftDatabase`-shaped port
(`execSync`/`runSync`/`getFirstSync`, all synchronous) wrapped in resolved promises — `outbox.ts`'s
own comment already anticipates this ("a host with a synchronous store returns resolved promises").
Three new tables (outbox/optimistic/recovery), mirroring the web's `mutationOutboxIndexedDB.ts`
three object stores, with JSON-serialized `payload`/`attachments`/`optimisticDisplay` columns since
SQLite has no native object storage the way IndexedDB does.

- **Estimate: 130-220 non-test lines — the one row likely to blow the 150 ceiling.** The web's
  IndexedDB adapter is 591 lines, but that includes event-callback boilerplate SQLite's synchronous
  API doesn't need; `draftRepository.ts`'s density (303 lines / ~10 methods across 5 concerns,
  ~25-30 lines/method with schema) is the closer comparable, and 13 methods at that density would
  exceed 150. **Cannot size precisely without drafting it** (this row is measurement-only, per the
  brief). If it lands over ~150, split D25d-1a (schema + `enqueueIntent`/`markAttempted`/
  `markUnknown`/`settleReceipt`/`settleApplied` — the write path) from D25d-1b (`getOutbox`/
  `getOptimistic`/`listOptimistic`/`getRecovery`/`nextDispatchable`/`restoreProvenAbsent`/
  `transferToRecovery` — the read path the dispatcher calls).
- **Oracle:** none pre-existing — this is new native code with no web equivalent test to reuse
  directly. New port-conformance tests only, hand-written against the 13 methods (no shared
  cross-host conformance suite exists — see Package gap below).
- **Visible change:** none. Dead code until D25d-2 wires it in.
- **Risk:** SQLite write-then-immediately-read ordering under concurrent enqueue+dispatch (need to
  confirm `runSync`/`getFirstSync` on the same `db` instance are strictly ordered, which
  `journal_mode = WAL` plus a single JS-thread caller should already guarantee, but this is the
  first port implementation to depend on it for correctness rather than just draft persistence).

### D25d-2: native submissions go through the dispatcher + pending-turns store
Wires `MutationOutbox`, `MutationDispatcher`, `createPendingTurnsStore`, and
`submitWithPendingTracking`/`createSubmissionRunner` into `state/conversation.ts`'s `send`/`steer`/
`queue`/`interrupt`, replacing the direct `service.send()` calls with enqueue-then-dispatch.
`ConversationMutationState`/`pendingMutation`/`pendingSend` retire (or shrink to a thin
`submittingRefs`-derived busy flag for the four call sites' UI gating).

- **Estimate: 100-150 non-test lines** (four call sites, each replacing ~20-30 lines of hand-rolled
  `mutationId`/`generation`/`entryErrorRev` bookkeeping with a `submitWithPendingTracking` call of
  roughly the same shape as the web adapter's).
- **Oracle:** `dispatcher.test.ts`/`dispatcher.edge.test.ts` (D27's own suites, 768+ lines, already
  passing against the real class) apply unchanged since the dispatcher itself is untouched. The
  submission lifecycle's oracle is `submission.test.ts`'s 6 package tests (the kata-3p22 tracking
  property, the epoch guard, the reject-if-already-pending guard) — native needs its own
  `FakeClient`-driven integration suite exercising the same properties against its real SQLite
  storage, not a port of the web's jsdom+IndexedDB suite.
- **Visible change — flag for sign-off, not silent parity:** submitting while offline or
  mid-disconnect currently throws immediately (error toast, draft restored per
  `draftRevisionAtSubmit`). After this row, it durably enqueues and waits for dispatch — the error
  surfaces only if the daemon later refuses it, not the instant the socket is down. This is the
  intended behavior (it's why D25d exists) but is a visible change on a flaky connection that Jesse
  should see named, not infer from a diff.
- **Risk:** double-posting if a dispatch's RPC succeeds but its response is lost before settling —
  contained by D27's `blockedUnknown`/no-blind-replay rule, already tested there. The genuinely new
  exposure is app-kill-mid-flight: previously impossible (nothing survived a kill), now a durable
  record can be redispatched on cold start — D25d-4 is what makes that safe; D25d-2 alone leaves a
  window where a killed app's queued-but-undispatched record sits until the next `MutationOutbox`
  start.

### D25d-3: pending rows rendered from `pendingTurnEntries`, deduped by the hub's echo
Adds a native pending-rows UI (the "submitted, not yet echoed" surface the ruling asks for) driven
by `pendingTurnEntries(ref, method)`/`useAwaitingFirstFrameSend`-equivalent reads over
`MobileConversation` — already a `ThreadModel`, so no adapter needed on the dedupe side (see Target
flow above). Retires the `pendingSend`-based busy-only indicator in `QueueSheet.tsx`/`screens.tsx`
in favor of an actual row.

- **Estimate: 80-120 non-test lines** (a rows component + a hook, minus the retired busy-flag
  reads).
- **Oracle:** `pendingEntries.test.ts` (241 lines, package) is the exact reference — the
  reconciliation function is unchanged by this row, so its existing suite is the proof native's UI
  can trust the input it's given. Native adds only rendering-level tests: a row appears once
  `pendingTurnEntries` reports it, and disappears once the model's item carries the matching
  `clientMutationId`.
- **Visible change:** the feature itself — a submitted message shows as a pending row immediately,
  survives reconnects/reloads (once D25d-1/2 land), and resolves into the real item with no
  manual action.
- **Risk — the "never show refresh-to-see-latest" rule** (native-auto-refresh-stale-pages): the
  pending row must resolve into the echoed item on its own, never behind a pull-to-refresh. Web's
  `usePendingTurnEntries` subscribes to BOTH the pending-turns store and the thread store and
  recomputes on either's change (`pendingTurnsStore.ts:249-259` in the package's own consumer) —
  native's binding needs the same dual subscription (native's store subscription primitive, plus
  whatever notifies it of a new hydrated model), not just one.

### D25d-4: reconnect/reload recovery
Wires `MutationDispatcher.restoreProvenAbsent(targetRef, collectAuthoritativeMutationIds(response))`
into native's `requestRehydrate`/reconnect path (the same call `threads.ts` already makes on the
web), and starts `MutationOutbox` discovery (`start()`/`connectionReady()`) on cold start and
reconnect so a durable record left over from a killed app gets redispatched rather than stranded.

- **Estimate: 60-90 non-test lines** (a handful of call sites in the reconnect/rehydrate path plus
  cold-start wiring; `collectAuthoritativeMutationIds` is an existing pure package export
  (`reducer.ts:666`), zero new logic to derive it).
- **Oracle:** none directly portable — native has no reconnect-vs-outbox test today. The reference
  is `restoreProvenAbsent`'s own dispatcher-level coverage (already in D27's suite); this row's new
  test is native-specific: a durable record present at cold start, a reconnect, and a redispatch
  that does not duplicate.
- **Visible change:** a message typed while offline, or mid-send when the app was killed, survives
  and sends once connectivity returns, instead of surfacing an error the user has to retype past.
- **Risk — the highest of the four rows.** This is the one that turns "impossible before" (nothing
  survived a kill) into "must be exactly right" (a redispatch after a kill must not double-post,
  and a stale local record must yield to whatever the authoritative reread says landed). Recommend
  this lands and soaks after D25d-1..3, not alongside them.

## Package gaps found

- **No shared `MutationOutboxStorage` conformance suite.** The web's IndexedDB adapter and any new
  native adapter each hand-write their own tests against the port's 13 methods; nothing in the
  package runs one shared behavioral suite against "any conforming storage." Building one (a
  generic test factory over an injected storage instance) would remove the risk of the two adapters
  silently drifting in what they consider correct, but it's not required to start D25d-1 — rough
  size if built later: ~150-200 lines.
- **`yieldMacrotask` has no native-obvious binding.** Its only consumer today is the web's
  test-only `settlePendingTurnsProjectionForTests`/`flushPendingTurnsProjectionForTests` helpers, via
  a `MessageChannel` hop; React Native has no global `MessageChannel`. Not a package gap — the port
  takes any macrotask-yielding function — but native's binding is a fresh few-line
  `setTimeout(resolve, 0)`, not a port of the web's implementation.
- **No `MutationLifecycleTarget`-shaped wrapper over React Native's `AppState`.** RN's
  `AppState.addEventListener` returns a `{remove()}` subscription, not the DOM
  `addEventListener`/`removeEventListener` pair the port expects. Not required for D25d-1..4 (the
  dispatcher's reconnect path already re-triggers discovery via `connectionReady()`), but if native
  later wants the foreground-triggers-rescan behavior the D25 scout flagged as "web only," that
  adapter is roughly 10 lines and does not exist today.
- **`secureUUID`/crypto: no gap.** `expo-crypto`'s `Crypto.randomUUID()` (already used in
  `ConnectionProvider.tsx`, `credentialStore.ts`, `nativeOrganization.ts`) is a synchronous
  `() => string` — an exact, zero-line-of-adaptation match for `SecureRandomSource.randomUUID`.
- **Draft port: a real adapter, not a gap, but not free either.** Native's `DraftDocument`
  (`mobile-native/src/draftDocument.ts`, 308 lines) already persists drafts over the same
  `expo-sqlite` storage, but its shape (a `submitting`/`unconfirmed`-staged document per
  destination, with its own `draftRecovery.ts` restore logic) is genuinely different from the
  package's `PendingTurnsDraftPort` (`readDraftRevision`/`readComposerDraft`/`clearDraft`, keyed to
  a monotonic revision counter). D25d-2's submission-lifecycle wiring needs a small adapter between
  the two, not a straight reuse — sized within that row's own estimate above, not called out
  separately here since it's the same mechanism (submission lifecycle) as D25d-2, not a fourth PR.

## Headline numbers

4 PRs, 380-580 total non-test lines (130-220 + 100-150 + 80-120 + 60-90), one genuine package gap
worth building later (~150-200 lines, not blocking), zero blocking gaps for D25d-1 to start. D25d-1
is the one row whose estimate may force a sub-split once actually drafted.
