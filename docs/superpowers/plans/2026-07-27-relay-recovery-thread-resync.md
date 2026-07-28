# Relay Recovery Thread Resync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make an already-open WebUI session rehydrate its authoritative thread snapshot after the hub relay reconnects to a replacement daemon.

**Architecture:** The hub emits one typed, per-thread snapshot-invalidation notification after a downstream relay recovery succeeds. The frontend consumes that hint outside the incremental reducer and reuses its existing buffered `thread/read` hydration for only the affected tracked ref.

**Tech Stack:** Go AppWire protocol and hub relay, generated TypeScript protocol types, Zustand thread store, Vitest FakeClient tests.

## Global Constraints

- Implement kata `mz8j` and the approved design in `docs/superpowers/specs/2026-07-27-relay-recovery-thread-resync-design.md`.
- Emit the resync hint only after a recovery subscription succeeds, never on initial attachment or failed retries.
- Refresh only the affected tracked ref; do not react to broad `serf/tree/changed`.
- Reuse the existing buffered hydration and generation/client guards.
- Keep refresh failure best-effort: retain the stale model and continue relaying notifications.
- Tests must be deterministic and perform no live provider calls.
- Make the smallest coherent change and preserve independent open-thread and watched-thread lifecycles.

---

### Task 1: Targeted Relay-Recovery Resync

**Files:**

- Modify: `appwire/types.go`
- Modify: `appwire/protocol.go`
- Generate: `cmd/serf-hub/frontend/src/protocol/types.gen.ts`
- Modify: `cmd/serf-hub/app_relay.go`
- Test: `cmd/serf-hub/app_rpc_test.go`
- Modify: `cmd/serf-hub/frontend/src/stores/threads.ts`
- Test: `cmd/serf-hub/frontend/src/stores/threads.test.ts`
- Test: `cmd/serf-hub/frontend/src/panes/session/composer/Composer.integration.test.tsx`

**Interfaces:**

- Produces: `appwire.NotifySerfThreadResync = "serf/thread/resync"`.
- Produces:

  ```go
  type ThreadResyncParams struct {
      ThreadID string `json:"threadId"`
      Ref      string `json:"ref"`
  }
  ```

- Consumes: the generated TypeScript notification variant:

  ```ts
  {
    method: "serf/thread/resync";
    params: { threadId: string; ref: string };
  }
  ```

- Consumes: the thread store's existing `hydrateAndSubscribe`,
  `hydrateAndSubscribeWatch`, pending-hydration buffering, replay, and
  wholesale-publish paths.

- [ ] **Step 1: Add a failing hub relay-recovery test**

  Extend the existing scripted relay-recovery harness in
  `cmd/serf-hub/app_rpc_test.go`. Establish the initial relay and assert that
  no resync hint is emitted. Close the initial notification channel, script a
  failed retry followed by a successful replacement channel, and assert that
  the subscribed hub client receives exactly one:

  ```go
  appwire.Notification{
      Method: appwire.NotifySerfThreadResync,
      Params: ThreadResyncParams{
          ThreadID: threadID,
          Ref:      "codex:" + threadID,
      },
  }
  ```

  Then send an agent-message delta through the replacement channel and assert
  it follows the resync hint, proving relay continuity and ordering.

- [ ] **Step 2: Run the hub test and verify RED**

  Run:

  ```bash
  go test ./cmd/serf-hub -run 'TestHubRelay.*Resync' -count=1
  ```

  Expected: compilation fails because `NotifySerfThreadResync` and
  `ThreadResyncParams` do not exist.

- [ ] **Step 3: Add the typed AppWire notification**

  In `appwire/types.go`, add the notification constant and exact
  `ThreadResyncParams` type from the Interfaces section. In
  `appwire/protocol.go`, add it to `Notifications` with a description that it
  is hub-originated and asks clients to re-read one thread after relay
  recovery.

  Regenerate the committed TypeScript catalog:

  ```bash
  go generate ./appwire
  ```

  Confirm generation changes only the expected protocol output.

- [ ] **Step 4: Emit the hint only after relay recovery succeeds**

  In the `notifications == nil` recovery branch of `app_relay.go`, after
  `SubscribeThread` returns a non-nil replacement channel and before any
  replacement-channel notification is forwarded, broadcast:

  ```go
  server.Broadcast(relayKey, appwire.NotifySerfThreadResync, appwire.ThreadResyncParams{
      ThreadID: threadID,
      Ref:      subscribeParams.Ref,
  })
  ```

  Do not emit from the initial `startRelay` subscription and do not emit when
  recovery returns an error or nil channel.

- [ ] **Step 5: Run the hub and protocol tests and verify GREEN**

  Run:

  ```bash
  go test ./cmd/serf-hub -run 'TestHubRelay.*Resync' -count=1
  go test ./internal/appwirets ./appwire -count=1
  ```

  Expected: all selected tests pass, including the generated-file drift test.

- [ ] **Step 6: Add failing frontend store tests**

  In `threads.test.ts`, hydrate `ref_a` with an active model whose snapshot
  explicitly has `capabilities.queue === false`. Emit
  `serf/thread/resync` for `ref_a`, hold the replacement `thread/read`
  response pending, emit a matching live notification during the read, then
  resolve an authoritative active snapshot with `capabilities.queue === true`.
  Assert:

  - exactly one new `thread/read` targets `ref_a`;
  - the model is replaced with queue capability true;
  - the in-flight notification is replayed over the snapshot;
  - a resync for an untracked ref causes no read.

  Add the equivalent focused assertion for a watched ref, preserving its
  current `includeTurns` richness.

- [ ] **Step 7: Run the frontend store tests and verify RED**

  From `cmd/serf-hub/frontend`, run:

  ```bash
  npm test -- src/stores/threads.test.ts
  ```

  Expected: the resync notification causes no replacement `thread/read`, so
  the new assertions fail.

- [ ] **Step 8: Reuse buffered hydration for the targeted ref**

  In `threads.ts`, generalize the current ready/reconnect rehydrate helper to
  accept an optional target ref. Preserve its existing full-set behavior for
  `onReady`; when a `serf/thread/resync` notification arrives, invoke it with
  only `n.params.ref` and return without folding the hint through the
  incremental reducer.

  The target path must:

  - no-op unless the ref is currently open, watched, or both;
  - preserve pending-hydration transfer and notification replay;
  - preserve watched `includeTurns` richness and generation checks;
  - keep failures best-effort;
  - avoid changing `frameTimes` merely because the hint arrived.

- [ ] **Step 9: Run frontend store tests and verify GREEN**

  From `cmd/serf-hub/frontend`, run:

  ```bash
  npm test -- src/stores/threads.test.ts
  ```

  Expected: all store tests pass.

- [ ] **Step 10: Add the composer behavioral regression**

  In `Composer.integration.test.tsx`, mount against a stale active snapshot
  whose explicit queue capability is false. Before resync, submit content and
  assert the client-side unavailable toast appears with no turn mutation.
  Emit `serf/thread/resync`, return a fresh active snapshot with queue true,
  submit again, and assert the composer calls `turn/queue` without remounting
  or reconnecting the FakeClient.

- [ ] **Step 11: Run the composer regression and verify it passes**

  From `cmd/serf-hub/frontend`, run:

  ```bash
  npm test -- src/panes/session/composer/Composer.integration.test.tsx
  ```

  Expected: the new no-reload send recovery test and all existing composer
  integration tests pass.

- [ ] **Step 12: Format and run focused cross-layer verification**

  Run:

  ```bash
  gofmt -w appwire/types.go appwire/protocol.go cmd/serf-hub/app_relay.go cmd/serf-hub/app_rpc_test.go
  go test ./cmd/serf-hub ./appwire ./internal/appwirets -count=1
  cd cmd/serf-hub/frontend
  npm test -- src/stores/threads.test.ts src/panes/session/composer/Composer.integration.test.tsx
  npm run typecheck
  npm run lint
  ```

  Expected: all commands pass without warnings attributable to this change.

- [ ] **Step 13: Commit the implementation**

  Review `git status` immediately before staging. Stage only the files listed
  in this task, then commit with a detailed message explaining the discarded
  relay-recovery snapshot, targeted invalidation, buffered rehydrate, and
  deterministic regression coverage.
