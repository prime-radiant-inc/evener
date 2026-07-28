# AppWire Retry-Safe Mutations and Atomic Rejoin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development to implement this plan task-by-task.
> Every task starts with a real behavioral RED test, ends with a focused
> verification run and commit, and receives an independent specification and
> code-quality review before the next task starts.

**Goal:** Make Serf WebUI turn and queue mutations safe across lost responses,
reconnects, daemon restarts, and full page reloads while making
`thread/read(subscribe: true)` an atomic snapshot-to-live handoff.

**Architecture:** AppWire v2 is a flag-day protocol. A per-session durable
mutation state file is the authoritative serialized state machine for the seven
in-scope mutations, their queue projection, reservations, receipts, and
recovery ownership. The Hub forwards mutation identities and typed outcomes
without deciding dynamic session state. The browser persists ordered intents
in IndexedDB before dispatch and reconciles only by mutation identity.
Source-specific projection gates make Serf snapshot cuts atomic and make Codex
converge through full-state replacement instead of overlapping deltas.

**Tech Stack:** Go, JSON AppWire, afero filesystem seams, existing transcript
and projector code, TypeScript, React, Zustand, IndexedDB, Vitest, Testing
Library.

## Binding Constraints

- This is a flag-day change to `serf-appwire-v2`; do not add compatibility
  negotiation, legacy mutation shapes, or fallback behavior.
- The first implementation covers exactly `turn/start`, `turn/steer`,
  `turn/queue`, `turn/drainAsSteer`, `turn/promoteQueuedAsSteer`,
  `turn/cancelQueued`, and `turn/interrupt`.
- Serf and Codex `thread/clear` are unavailable in v2 until replacement has a
  separate retry-safe design.
- The daemon session state machine, not Hub capability projections, owns
  dynamic precondition checks and mutation replay.
- No timer may classify a domain mutation as failed. Request and heartbeat
  timeouts remain transport signals only.
- Default tests use scripted providers, fake transports, injected filesystems,
  and fake browser storage. They do not use provider credentials or network
  access.
- Generated files are changed only with `make generate`.
- Use the smallest coherent changes. Preserve unrelated work on
  `webui-workspace-shell`.
- Do not create or update Linear work for this implementation.

## Task 1: Define the AppWire v2 contract and fail-fast handshake

**Files:**

- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Modify: `appwire/errors.go`
- Modify: `appwire/client.go`
- Modify: `internal/appserver/server.go`
- Modify: `server/appwire_runtime.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/internal/appsource/source.go`
- Modify: `cmd/serf-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/serf-hub/internal/appsource/codex_source.go`
- Test: `appwire/protocol_test.go`
- Test: `appwire/client_test.go`
- Test: `internal/appserver/server_test.go`
- Test: `server/appwire_catalog_test.go`
- Test: `cmd/serf-hub/app_rpc_test.go`
- Test: `cmd/serf-hub/internal/appsource/local_daemon_test.go`
- Generate: `docs/appwire-protocol.md`
- Generate: `cmd/serf-hub/frontend/src/protocol/types.gen.ts`

**Interfaces:**

- Add required `InitializeParams.ProtocolVersion`.
- Set `appwire.ProtocolVersion` to `serf-appwire-v2`.
- Add `ClientMutationID` and required method-specific preconditions to all
  seven parameter types.
- Add `MutationReceipt`, `MutationProjectionState`, typed response structs for
  the six methods that currently return `EmptyResponse`, and common mutation
  error data on `WireError`.
- Add `PendingMutation` and `SerfThread.PendingMutations`.
- Add queue `Revision`, queue-entry `ClientMutationID`, and mutation identity on
  `ThreadItem` and steering notification payloads.
- Require authoritative `ThreadID` and `Ref` on every thread notification,
  including `TurnCompletedParams`.
- Change source interface methods to return the typed receipts.

- [ ] Add protocol and handshake tests first. Prove that missing or mismatched
  v2 versions are rejected before initialization, the Go client rejects a
  mismatched response before sending `initialized`, and each mutation shape
  requires its ID and preconditions. Run:

  ```bash
  go test ./appwire ./internal/appserver ./server -run 'Protocol|Initialize|Mutation|NotificationCatalog' -count=1
  ```

  Expected RED: v1 is still accepted and required fields/types do not exist.

- [ ] Implement the v2 types, catalog changes, error metadata, client
  validation, and server initialize gate. Use exact equality; do not introduce
  a capability-negotiation branch.

- [ ] Change Serf source method signatures and forwarding stubs to retain the
  complete typed responses. Make Codex mutation methods return unsupported
  without issuing upstream mutation calls.

- [ ] Set `ThreadClear` false for Hub/Serf capabilities and return unsupported
  from direct v2 `thread/clear` handlers.

- [ ] Run `make generate`, then:

  ```bash
  go test ./appwire ./internal/appserver ./server ./cmd/serf-hub/internal/appsource ./cmd/serf-hub -count=1
  make lint-generated
  git diff --check
  ```

- [ ] Commit the protocol checkpoint, including both generated files.

## Task 2: Build the durable session mutation state store

**Files:**

- Create: `agent/session_client_mutation.go`
- Create: `agent/session_client_mutation_persist.go`
- Create: `agent/session_client_mutation_test.go`
- Create: `agent/session_client_mutation_persist_test.go`
- Modify: `agent/session.go`
- Modify: `agent/session_init.go`

**Interfaces:**

- Store one atomic JSON snapshot at
  `<StateDir>/mutations/<session-id>.json`.
- The snapshot contains the journal records, materialized client input queue,
  queue revision, stable turn/entry sequence state, budget reservations,
  interrupt fence, and pending execution projection.
- Each nonterminal record contains the canonical full payload, preconditions,
  stable generated IDs, payload hash, operation/execution/projection state,
  result or rejection, and recovery-attempt generation.
- Add injected afero helpers mirroring `saveQueuesFS` so every write boundary
  can fail deterministically.
- A successful update writes temp, syncs/closes as supported, renames, and only
  then publishes the cloned in-memory state.

- [ ] Write table-driven RED tests for unseen reservation, full-payload
  round-trip including image bytes, same-ID/same-hash replay, payload mismatch,
  terminal rejection replay, atomic write failure, restart recovery, active
  owner join, owner release on nonterminal exit, and same-process takeover.

  ```bash
  go test ./agent -run 'TestClientMutation(Store|Persist|Ownership)' -count=1
  ```

  Expected RED: no durable mutation store exists.

- [ ] Implement immutable snapshot cloning and one serializer around
  lookup/validation/commit. Use a process-local owner token plus a durable
  attempt generation; joining is permitted only while the exact owner is
  active.

- [ ] Load the store during both new-session initialization and
  `RestoreSessionFromMetaWithConfig`. Treat a missing file as empty; reject a
  malformed file rather than silently discarding accepted work.

- [ ] Add explicit fault seams immediately after reservation and before/after
  effect snapshot rename. Prove a failed second write leaves recoverable,
  complete input and releases the live owner.

- [ ] Run:

  ```bash
  go test ./agent -run 'ClientMutation|RestoreSession' -count=1
  go test ./agent -count=1
  git diff --check
  ```

- [ ] Commit the durable store independently of handler integration.

## Task 3: Move queue and steering mutations into the durable state machine

**Files:**

- Modify: `agent/session_client_mutation.go`
- Modify: `agent/session_queue.go`
- Modify: `agent/session_queue_persist.go`
- Modify: `agent/session_init.go`
- Modify: `agent/events/payloads.go`
- Modify: `agent/schema/turn.go`
- Test: `agent/session_client_mutation_test.go`
- Test: `agent/session_queue_persist_test.go`
- Test: `agent/session_queue_promote_test.go`
- Test: `agent/session_queue_cancel_test.go`

**Interfaces:**

- Add session methods returning typed receipts for queue, steer, drain,
  promote, and cancel.
- Increment one durable queue revision on enqueue, pop, cancel, promote, drain,
  or recovery restore.
- Validate `expectedTurnId`, `expectedEntryId`, and
  `expectedQueueRevision` under the same serializer as the state transition.
- Queue acceptance reserves one `MaxTurns` slot. Cancel or steering transform
  releases it in the same durable commit; claim converts it to an accepted
  turn.
- Client-authored steering remains durable until transcript incorporation.
- The legacy queue snapshot retains daemon-authored steering only; it must not
  become a second authority for client mutations.

- [ ] Add RED tests for stale expected turn, shifted queue entry, stale drain
  revision, last-slot budget reservation, concurrent final-slot acceptance,
  cancel/transform release, and crash at every transform boundary.

  ```bash
  go test ./agent -run 'TestClientMutation_(Queue|Steer|Drain|Promote|Cancel|Budget)' -count=1
  ```

- [ ] Implement the five state transitions as pure clone-and-commit operations
  before emitting queue/steering events. Store both the creating mutation ID
  and stable queue entry ID.

- [ ] Change queue consumption to durable `accepted -> claimed` rather than
  pop-and-forget. On transcript append, mark the record incorporated; on
  append failure, return it to runnable state under the same identity.

- [ ] Carry mutation and stable turn identity through
  `UserInputData`, `SteeringInjectedData`, and persisted transcript turns.
  Recovery must recognize an already-appended identity and never append it
  twice.

- [ ] Run:

  ```bash
  go test ./agent -run 'Queue|Steer|ClientMutation|MaxTurns' -count=1
  go test ./agent -count=1
  git diff --check
  ```

- [ ] Commit the queue/steering state-machine integration.

## Task 4: Make start and interrupt durable lifecycle operations

**Files:**

- Modify: `agent/session_client_mutation.go`
- Modify: `agent/session_lifecycle.go`
- Modify: `agent/session_state.go`
- Modify: `agent/session_init.go`
- Modify: `agent/session.go`
- Test: `agent/session_client_mutation_test.go`
- Test: `agent/session_lifecycle_test.go`
- Test: `agent/session_budget_test.go`

**Interfaces:**

- Start acceptance persists the complete input, stable turn ID, runnable
  execution state, and budget reservation before waking the runner.
- Claimed start/queue records resume with the same logical turn ID.
- Deterministic pre-append failure writes one failed identity-bearing user
  item, rather than leaving an accepted record invisible.
- Interrupt phase one persists `inFlight` plus `interruptRequested`, releases
  the serializer, signals cancellation, and waits outside the lock.
- The runner or recovery reacquires the serializer and atomically terminalizes
  the target and interrupt receipt. Incompatible mutations honor the fence.

- [ ] Write RED tests for response loss after start commit, crash after start
  reservation, restart of accepted and claimed starts, deterministic
  incorporation failure, and retry replay outside the transcript window.

- [ ] Write a deterministic interrupt RED test with barriers proving the
  terminal transition acquires the serializer while the RPC waits outside it.
  Add crash-after-fence and same-ID retry tests.

  ```bash
  go test ./agent -run 'TestClientMutation_(Start|Interrupt|Incorporation)' -count=1
  ```

- [ ] Implement lifecycle wake/claim APIs and stable identity propagation. Do
  not make acceptance depend on the one-slot server input channel.

- [ ] Implement the two-phase interrupt fence and deferred owner release.
  Ensure terminal recovery cannot resume the fenced execution.

- [ ] Run:

  ```bash
  go test ./agent -run 'ClientMutation|Lifecycle|Budget|Interrupt' -count=1
  go test ./agent -count=1
  git diff --check
  ```

- [ ] Commit start/interrupt lifecycle durability.

## Task 5: Wire the authoritative state machine through the daemon and Hub

**Files:**

- Modify: `server/server.go`
- Modify: `server/appwire_runtime.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf-hub/internal/appsource/source.go`
- Modify: `cmd/serf-hub/internal/appsource/local_daemon.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Test: `server/appwire_mutation_recovery_test.go`
- Test: `server/appwire_server_test.go`
- Test: `cmd/serf/serve_test.go`
- Test: `cmd/serf-hub/app_rpc_test.go`
- Test: `cmd/serf-hub/internal/appsource/local_daemon_test.go`

**Interfaces:**

- Add one typed `server.RetrySafeTurnFunctions` callback bundle installed by
  `cmd/serf/serve.go`; handlers no longer independently validate mutable
  processing/queue state.
- The daemon validates static shape, then delegates journal lookup and dynamic
  compare-and-commit to `agent.Session`.
- The Hub validates static shape/source support only and forwards IDs,
  preconditions, receipts, and mutation error data unchanged.
- A relay/response loss becomes `mutationOutcome: unknown`, never a fabricated
  not-accepted rejection.

- [ ] Add the required boundary RED test first: apply a queued mutation through
  the real daemon AppWire handler, drop the response, reconnect, retry the same
  ID, and prove one queue/transcript effect and a replayed receipt.

  ```bash
  go test ./server -run '^TestAppWireMutationResponseLossRetriesOnce$' -count=1
  ```

- [ ] Add the same replay table for all seven methods, dynamic capability
  changes, payload mismatch, terminal rejection replay, and effect-write
  failure followed by same-process recovery.

- [ ] Install the session callback bundle and mutation wake path in
  `cmd/serf/serve.go`. Remove the v1 direct queue/steer/cancel handler decisions
  from the seven AppWire routes; unrelated REST routes stay unchanged.

- [ ] Change Hub source interfaces and handlers to return typed responses.
  Prove response fields and structured errors survive browser-to-Hub-to-daemon.

- [ ] Run:

  ```bash
  go test ./server ./cmd/serf ./cmd/serf-hub/internal/appsource ./cmd/serf-hub -run 'Mutation|Turn(Start|Steer|Queue|Drain|Promote|Cancel|Interrupt)' -count=1
  go test ./server ./cmd/serf ./cmd/serf-hub/internal/appsource ./cmd/serf-hub -count=1
  git diff --check
  ```

- [ ] Commit the end-to-end mutation RPC path.

## Task 6: Make daemon projection and thread rejoin atomic

**Files:**

- Modify: `internal/appserver/notifier.go`
- Modify: `internal/appserver/server.go`
- Modify: `internal/appserver/subscriptions.go`
- Modify: `server/server.go`
- Modify: `server/appwire_runtime.go`
- Modify: `server/appwire_turns.go`
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appserver/notifier_test.go`
- Test: `internal/appserver/server_test.go`
- Test: `server/appwire_rejoin_test.go`
- Test: `server/appwire_turns_paging_test.go`

**Interfaces:**

- Add a per-source projection commit gate that covers projector mutation,
  internal sequence allocation, retained notification insertion, and
  subscriber-buffer insertion.
- `thread/read(subscribe: true)` registers a hydration-generation buffer and
  clones the projector under that same gate.
- Discard buffered notifications at or before the snapshot cut; release later
  records in producer order after the response.
- `replaceSubscription` swaps ownership under the same gate.
- `thread/read` includes `pendingMutations` and queue revision from the durable
  session store.

- [ ] Write barrier-controlled RED tests pausing before projector mutation,
  between mutation and sequence allocation, before subscriber insertion, and
  during snapshot clone. Use an append-only delta so duplicate delivery is
  observable.

  ```bash
  go test ./internal/appserver ./server -run 'Atomic(Rejoin|Projection)|SnapshotCut|ReplaceSubscription' -count=1
  ```

- [ ] Implement a buffered subscription object in `internal/appserver`; do not
  expose internal sequence as a public cursor.

- [ ] Move `RecordAppEvent` projection, notifier record, and broadcast-buffer
  insertion under the single commit gate. Stamp authoritative ref/thread ID on
  every v2 notification at this egress.

- [ ] Make the read snapshot clone from the same projector/store generation and
  release only post-cut buffered notifications after its response is queued.

- [ ] Run:

  ```bash
  go test ./internal/appserver ./internal/appprojector ./server -count=1
  git diff --check
  ```

- [ ] Commit atomic daemon rejoin.

## Task 7: Make Hub relay rejoin atomic and Codex converge by replacement

**Files:**

- Modify: `cmd/serf-hub/app_rpc.go`
- Modify: `cmd/serf-hub/app_relay.go`
- Modify: `cmd/serf-hub/internal/appsource/source.go`
- Modify: `cmd/serf-hub/internal/appsource/codex_source.go`
- Modify: `cmd/serf-hub/internal/appsource/codex_live_thread.go`
- Test: `cmd/serf-hub/app_rpc_test.go`
- Test: `cmd/serf-hub/internal/appsource/codex_source_test.go`
- Test: `cmd/serf-hub/internal/appsource/transport_seams_test.go`

**Interfaces:**

- Source rejoin returns an atomic read/subscription stream for Serf sources
  instead of Hub `ReadThread` then `SubscribeThread` calls with a gap.
- Hub buffers relay notifications by hydration generation until its response
  is installed, then releases them in source order.
- Codex exposes no mutation or clear capability and forwards no raw
  append-only deltas.
- Codex live events mark a cached thread dirty. A single-flight full-read loop
  commits a qualified replacement, stays dirty on failure, retries forever
  with capped backoff, and performs an unconditional read after reconnect.

- [ ] Add a Serf relay RED test in which a notification lands between source
  subscription and Hub response; prove it is neither lost nor duplicated.

- [ ] Add Codex RED tests for an event racing a read and for the final event's
  read failing once then succeeding without another upstream event.

  ```bash
  go test ./cmd/serf-hub ./cmd/serf-hub/internal/appsource -run 'AtomicRejoin|Codex.*FullState|Codex.*Dirty' -count=1
  ```

- [ ] Introduce the smallest source-level atomic-rejoin interface and implement
  it for `LocalDaemonSource`. Keep adapter-native Codex initialize unchanged.

- [ ] Replace Codex notification mapping with the dirty full-state loop and
  cached qualified replacement/resync notification.

- [ ] Run:

  ```bash
  go test ./cmd/serf-hub/internal/appsource ./cmd/serf-hub -count=1
  git diff --check
  ```

- [ ] Commit Hub/source convergence.

## Task 8: Add the irrevocable host deletion fence

**Files:**

- Create: `cmd/serf-hub/internal/hubcore/deletion_store.go`
- Create: `cmd/serf-hub/internal/hubcore/deletion_store_test.go`
- Modify: `cmd/serf-hub/internal/hubcore/config.go`
- Modify: `cmd/serf-hub/main.go`
- Modify: `cmd/serf-hub/web.go`
- Modify: `cmd/serf-hub/web_api_project_delete.go`
- Modify: `cmd/serf-hub/app_sources.go`
- Modify: `cmd/serf-hub/app_session_resume.go`
- Modify: `cmd/serf-hub/app_launch.go`
- Modify: `cmd/serf-hub/app_threadlifecycle.go`
- Modify: `cmd/serf-hub/app_rpc.go`
- Test: `cmd/serf-hub/web_api_project_delete_test.go`
- Test: `cmd/serf-hub/app_rpc_test.go`

**Interfaces:**

- Persist compact `deleting`/`deleted` records under the Hub state root,
  outside target directories, keyed by stable ref/thread identity and
  generation.
- The first `deleting` write is irrevocable. Every source resolve, resume,
  launch, relay, and mutation path checks it under the same per-session
  ownership boundary and returns `targetDeleted`.
- Cleanup steps are idempotent and resume on the next deletion request or Hub
  startup. Only complete cleanup advances to `deleted`.

- [ ] Write RED tests that inject failure at each current project deletion
  step, restart the Hub state, and prove the target remains fenced and cleanup
  resumes. Add direct mutation/resolve/resume/launch tests for `deleting`.

  ```bash
  go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -run 'Deletion(State|Fence|Resume)|ProjectDelete' -count=1
  ```

- [ ] Implement atomic host records and one idempotent cleanup driver. Do not
  roll a committed deletion back to live.

- [ ] Thread the store through Hub config and all target acquisition paths.
  Transient missing relay/index entries must not create a tombstone.

- [ ] Run:

  ```bash
  go test ./cmd/serf-hub/internal/hubcore ./cmd/serf-hub -count=1
  git diff --check
  ```

- [ ] Commit deletion fencing separately.

## Task 9: Implement the transactional browser mutation outbox

**Files:**

- Create: `cmd/serf-hub/frontend/src/stores/mutationOutbox.ts`
- Create: `cmd/serf-hub/frontend/src/stores/mutationOutboxIndexedDB.ts`
- Create: `cmd/serf-hub/frontend/src/stores/mutationOutbox.test.ts`
- Modify: `cmd/serf-hub/frontend/package.json`
- Modify: `cmd/serf-hub/frontend/package-lock.json`

**Interfaces:**

- Store full request payload and attachment blobs, mutation ID, target ref and
  thread ID, per-target intent sequence, optimistic display data, and
  `submitting`/`blockedUnknown` state.
- In one IndexedDB transaction, allocate sequence and persist intent before any
  RPC.
- Conditional settlement never recreates a missing/transferred record.
  Applied/reflected removes matching outbox and recovery records and dominates
  late unknown.
- Terminal rejection atomically transfers to ordered recovery; target deletion
  atomically transfers to orphaned recovery.
- Recovery resend conditionally consumes one record and creates one new
  mutation in the same transaction.

- [ ] Add `fake-indexeddb` as a pinned dev dependency and write RED tests against
  the real IndexedDB adapter: page reload, blob round-trip, concurrent sequence
  allocation, reversed applied/unknown outcomes, crash-boundary transfer,
  simultaneous two-tab recovery resend, and blocked lower-sequence ordering.

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/stores/mutationOutbox.test.ts
  ```

- [ ] Implement versioned IndexedDB schema and transaction helpers. Generate
  mutation IDs inside the winning enqueue/recovery-transfer transaction.

- [ ] Add `BroadcastChannel` wakeup plus startup, ready, online, focus,
  visibility, and two-second ready-state discovery hooks. The timer only scans;
  it never settles or fails a record.

- [ ] Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/stores/mutationOutbox.test.ts
  npm run typecheck
  npm run lint
  ```

- [ ] Commit the storage layer without UI integration.

## Task 10: Add the ordered dispatcher and identity reconciliation

**Files:**

- Create: `cmd/serf-hub/frontend/src/stores/mutationDispatcher.ts`
- Create: `cmd/serf-hub/frontend/src/stores/mutationDispatcher.test.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.test.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/client.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/errors.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/reducer.test.ts`

**Interfaces:**

- `enqueueIntent` resolves after local commit; an asynchronous per-target
  dispatcher sends records in sequence using the same ID on every retry.
- Transport/request timeout retains `submitting`. Explicit
  `persistenceUnavailable` enters `blockedUnknown`. Only authoritative receipt,
  terminal rejection, or `targetDeleted` advances the target.
- On ready/reload, hydrate pinned refs with atomic `thread/read`, reconcile
  `pendingMutations`/queue/transcript identities, then dispatch unresolved work.
- Pending rendering and authoritative replacement key only by
  `clientMutationId`; remove text-based matching.

- [ ] Write RED tests with `FakeClient` and fake IndexedDB for lost responses,
  reconnect retry, reverse network scheduling, duplicate multi-tab dispatch,
  old-window receipt settlement, blocked sequencing, and late stale hydration
  generations.

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/stores/mutationDispatcher.test.ts src/stores/threads.test.ts src/protocol/reducer.test.ts
  ```

- [ ] Change the seven thread-store actions to enqueue durable intents rather
  than directly await `client.request`. Populate all required preconditions
  from the current authoritative model.

- [ ] Rejoin pinned refs on every ready generation before dispatcher replay.
  Keep the last good model visible while retrying a failed rejoin.

- [ ] Reconcile optimistic rows, queue entries, steering, receipts, and
  snapshots by identity. Receipt-only controls settle without a transcript
  echo.

- [ ] Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/stores/mutationDispatcher.test.ts src/stores/threads.test.ts src/protocol
  npm run typecheck
  npm run lint
  ```

- [ ] Commit dispatcher/store integration.

## Task 11: Integrate composer, queue controls, and recovery UI

**Files:**

- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/pendingReconcile.ts`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.tsx`
- Create: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/recoverytray.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.integration.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/queue/QueueStrip.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/pending/PendingChips.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/recovery/RecoveryTray.test.tsx`

**Interfaces:**

- Clear only the unchanged submitted composer payload after IndexedDB commit.
  Edits made during commit remain.
- Network settlement never restores the main composer.
- Recovery drafts render separately and preserve blobs until their conditional
  transfer commits.
- `blockedUnknown` offers Retry, Copy, and Export only. It has no local
  abandon/unblock action.
- Remove `PendingConfirmationTimeoutError`, `PENDING_TIMEOUT_MS`, timeout
  reapers, accepted-but-stale warnings, and reload instructions.

- [ ] Write RED component tests for local-commit clearing, edits during commit,
  lost response without duplicate composer content, nonempty composer plus
  recovery draft, attachment recovery, simultaneous recovery send, and
  indefinitely pending steer with no warning.

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/panes/session/composer src/panes/session/pending
  ```

- [ ] Replace the pending store's timeout lifecycle with a read-only projection
  of durable outbox plus authoritative pending mutations. Preserve first-frame
  cold-start behavior without a confirmation timer.

- [ ] Wire Composer and QueueStrip to `enqueueIntent`; remove timeout-specific
  catch branches and messages.

- [ ] Implement the recovery tray with durable edit/acknowledge/transfer
  semantics and accessible status/actions.

- [ ] Run:

  ```bash
  cd cmd/serf-hub/frontend
  npm run test -- src/panes/session/composer src/panes/session/pending
  npm run typecheck
  npm run lint
  ```

- [ ] Commit the UI cutover.

## Task 12: Prove full recovery and remove stale protocol behavior

**Files:**

- Modify: focused Go and frontend integration tests identified above
- Modify: `docs/appwire-protocol.md` through `make generate` only
- Modify: `cmd/serf-hub/frontend/src/protocol/types.gen.ts` through
  `make generate` only
- Modify: comments/docs that still describe confirmation deadlines or v1
  mutation behavior

- [ ] Add one deterministic end-to-end scripted-provider test covering:
  durable send, dropped daemon response, Hub/WebSocket reconnect, same-ID
  replay, daemon restart before incorporation, exactly one transcript user
  item, and frontend identity replacement after reload.

- [ ] Add a deterministic cellular-style transport schedule that keeps the
  outer socket alive while the inner relay drops, delays steering incorporation
  beyond the former timeout, re-establishes atomic rejoin, and converges without
  any warning or manual reload.

- [ ] Search for forbidden stale behavior:

  ```bash
  rg -n 'PendingConfirmationTimeout|PENDING_TIMEOUT_MS|didn.t confirm|view did not update|reload before retry|serf-appwire-v1' .
  ```

  Expected: no production matches; any fixture match must be an explicit
  negative assertion.

- [ ] Regenerate and run focused suites:

  ```bash
  make generate
  go test ./appwire ./internal/appserver ./internal/appprojector ./agent ./server ./cmd/serf ./cmd/serf-hub/internal/appsource ./cmd/serf-hub -count=1
  make test-web
  make lint-generated
  git diff --check
  ```

- [ ] Run repository-wide required verification:

  ```bash
  make build
  make test
  make lint
  git status --short
  ```

- [ ] Commit final integration evidence and documentation cleanup.

## Final Review Gate

- [ ] Give one independent reviewer the complete specification, this plan, all
  task reports, and the full branch diff from the pre-implementation base.
- [ ] Require findings first with exact file/line evidence, an explicit
  `APPROVED` or `CHANGES REQUIRED` verdict, and honest test limitations.
- [ ] Resolve all legitimate findings in one coherent fix wave with fresh RED
  regressions, then rerun the same reviewer on the fix diff.
- [ ] Rerun `make build`, `make test`, `make test-web`, `make lint`,
  `make lint-generated`, and `git diff --check` after the final fix commit.
