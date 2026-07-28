# Appwire Reconnect Adversarial Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the four Appwire client gaps found by the adversarial review:
prevent reentrant reconnect handshakes, prove failed-handshake sockets cannot
emit stale notifications, keep `close()` terminal before the first connection,
and make the heartbeat cleanup assertion test the behavioral contract instead
of an incidental close argument.

**Architecture:** Keep the existing single-client reconnect state machine and
its retired-socket handler detachment. Add only the missing state guard needed
to serialize reconnect attempts and the terminal-state check needed by the
first `connect()` call. Extend the existing `FakeSocket` protocol tests to
control every interleaving deterministically. The remaining two tasks are
test-contract improvements and should not require production changes.

**Tech Stack:** TypeScript, Vitest, fake timers, and the existing
`src/protocol` `FakeSocket` seams.

**Global constraints for every task:**

- Read `AGENTS.md` and `docs/testing.md` before editing.
- Work only in this existing worktree and branch. Do not create a worktree,
  merge, rebase, or touch Linear.
- Use strict TDD: observe a meaningful RED test, implement the smallest fix,
  observe GREEN, then perform the requested mutation check and restore the
  production code.
- Use fake sockets and fake timers. Do not add sleeps, real network access, or
  dependence on ambient machine state.
- Assert behavioral contracts, not error wording, generated text, or incidental
  argument values.
- Preserve the established reconnect backoff, manual retry, heartbeat cleanup,
  handler retirement, notification delivery, and `connect()` idempotency
  behavior outside the exact contract under test.
- Run focused tests from `cmd/serf-hub/frontend`:
  `npx vitest run src/protocol/client.test.ts src/protocol/reconnect.test.ts --no-file-parallelism --maxWorkers=1`.
- Commit each task independently with a detailed message. Never skip hooks.

## Task 1: Kata `2mg2` — serialize reentrant reconnect attempts

**Files:**

- Modify: `cmd/serf-hub/frontend/src/protocol/reconnect.test.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/client.ts`

- [ ] Add a deterministic regression test for the exact reentrant sequence:
  a ready client receives close code `1006`; its `reconnecting` state listener
  calls `retryNow()` synchronously; the immediate second socket remains in its
  handshake; advancing the original base-delay timer must not create a third
  socket.
- [ ] Run the focused test and record the expected RED result showing the third
  overlapping socket.
- [ ] Add the smallest state-machine guard that ensures at most one reconnect
  attempt owns the socket, handshake, and pending connection work at a time.
  Account explicitly for synchronous state listeners firing while
  `scheduleReconnect()` is still on the stack.
- [ ] Prove that a manual retry still cancels backoff and starts promptly when
  no handshake is in flight, and preserve the existing successful reconnect
  path.
- [ ] Run the focused protocol tests and record GREEN.
- [ ] Mutation-check the new test by temporarily disabling the serialization
  guard, confirm the test returns to RED, restore the implementation, and
  rerun GREEN.
- [ ] Self-review the diff for timer ownership, handler retirement, and
  reconnect-attempt lifecycle leaks; commit the task.

## Task 2: Kata `nbnh` — cover stale notifications from failed handshakes

**Files:**

- Modify: `cmd/serf-hub/frontend/src/protocol/reconnect.test.ts`

- [ ] Add a deterministic regression test in which initialization rejects on
  one socket, a replacement socket reaches ready, and the rejected socket later
  attempts to deliver a notification. Assert that only the live replacement
  can notify the client.
- [ ] Demonstrate RED without changing production behavior by temporarily
  removing or bypassing the failed-handshake handler detachment in
  `teardownFailedSocket`; restore production before continuing.
- [ ] Run the focused protocol tests with the existing production detachment
  and record GREEN.
- [ ] Repeat the mutation check once after the final test shape is settled:
  removing the detachment must fail the new notification assertion; restore it
  and rerun GREEN.
- [ ] Confirm the committed diff is test-only and does not weaken the existing
  heartbeat-retirement regression; commit the task.

## Task 3: Kata `tam4` — make pre-connect close terminal

**Files:**

- Modify: `cmd/serf-hub/frontend/src/protocol/client.test.ts`
- Modify: `cmd/serf-hub/frontend/src/protocol/client.ts`

- [ ] Add a deterministic test that calls `close()` on a fresh client and then
  calls `connect()`. Choose and document through assertions the terminal
  contract: `connect()` rejects with the existing typed closed-connection error
  and creates no socket. Do not assert error-message wording.
- [ ] Run the focused test and record RED, including proof that the current
  client attempts to create a socket after close.
- [ ] Add the smallest terminal-state check to the first-connect path while
  preserving ordinary `connect()` promise idempotency.
- [ ] Add or retain assertions proving repeated normal `connect()` calls still
  share the established connection operation and do not create extra sockets.
- [ ] Run the focused protocol tests and record GREEN.
- [ ] Mutation-check by temporarily removing the terminal-state check, confirm
  the new test returns to RED, restore it, and rerun GREEN.
- [ ] Self-review for promise rejection handling and close-after-connect
  behavior; commit the task.

## Task 4: Kata `05ws` — assert heartbeat cleanup behavior, not close syntax

**Files:**

- Modify: `cmd/serf-hub/frontend/src/protocol/reconnect.test.ts`

- [ ] Replace the heartbeat cleanup assertion that requires
  `closeRequests === [undefined]` with a behavioral assertion that exactly one
  socket close invocation occurred, without prescribing an omitted close code.
- [ ] Run the focused test and record GREEN for the contract-preserving test
  refinement.
- [ ] Mutation-check by temporarily removing the relevant production
  `socket.close()` call. The test must become RED because no close invocation
  occurred; restore production and rerun GREEN.
- [ ] Confirm the existing reconnect and ready-state assertions remain intact
  so the test still covers the cleanup path, then commit the task.

## Final branch gate

- [ ] Generate one review package for the complete range from the pre-Task-1
  base through final `HEAD` and obtain a fresh adversarial read-only review for
  spec compliance and code quality.
- [ ] If the review finds an Important or Critical issue, dispatch one fix
  agent, generate a scoped review package, and obtain one scoped re-review.
- [ ] Run the focused protocol tests, `make test-web`, `make test`, and
  `make build` without skipping hooks or tests.
- [ ] Record the commits and verification on each kata, close all four katas,
  and verify their final state.
