# Task 5 Report: Supervise Established Hub Relays

## Status

DONE

## Base

- Required base: `ac89dc1bc9e595295d9abbb45c2cdcb7d0a29cee`
- Worktree began clean at exactly that commit.

## Commit

- Pending at report-writing time; the final commit hash is appended below after commit creation.

## Changed files

- Created `cmd/serf-hub/app_relay.go`
- Modified `cmd/serf-hub/app_rpc.go`
- Modified `cmd/serf-hub/app_rpc_test.go`
- Modified `cmd/serf-hub/internal/hubcore/config.go`
- Added this report: `.superpowers/sdd/task-5-report.md` (process artifact, outside production-code scope)

No browser, routing, lifecycle, wire, or Task 1–4 files changed.

## Implementation

Relay-specific state and closure construction moved from `app_rpc.go` into `app_relay.go` without changing the public closure signatures for `startRelay`, `startTurn`, `startRelayForThread`, or `stopRelay`.

The relay still performs its first `SubscribeThread` synchronously. The first caller owns one `hubRelayHandle`; concurrent callers wait on the same `ready` channel and observe the same stored error/result. Initial failure cancels and identity-removes that handle, closes `ready`, and returns the same error to all waiters without subscribing the failed callers downstream.

After readiness, one supervisor owns the upstream subscription. A closed upstream channel is converted to a nil subscription and recovered through another `SubscribeThread` call while the browser's existing downstream subscription remains intact. Recovery errors use a cancellation-aware injected `relayRetryClock` and bounded exponential backoff of 100ms, 200ms, 400ms, 800ms, 1.6s, 3.2s, then 5s capped. Backoff resets after a successful subscription and after every notification.

The supervisor continues servicing the existing idle ticker while a retry wait is active. The fake/real clock wait runs under a child context; on relay cancellation or zero-subscriber retirement the supervisor cancels and joins that wait before returning, so no retry goroutine leaks. The existing image enrichment and `Broadcast` path is unchanged.

## Deterministic RED evidence

All behavior tests were written before their matching production behavior. Transitions use channels and injected clock waits; timeouts are deadlock guards only.

1. **Established upstream close recovery**
   - Added `TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose` and the channel-scripted `relaySubscribeResult` source boundary.
   - Command: `go test ./cmd/serf-hub -run '^TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose$' -count=1 -v`
   - Expected RED observed: `app_rpc_test.go:1837: timed out waiting for relay subscribe call` after upstream A closed; no second subscribe call existed.

2. **Cancellation-aware retry clock**
   - Added `TestRelayRetryClockWaitStopsOnCancellation`.
   - RED: build failed with `undefined: newRelayRetryClock`.

3. **Increasing recovery delay and reset**
   - Added `TestHubRPCThreadReadRelayRecoveryBackoffAndReset` with a channel-driven fake retry clock.
   - RED: build failed because `cfg.RelayHooks.RetryWait` did not exist.

4. **Five-second retry cap**
   - Added `TestRelayRetryBackoffCapsAtFiveSeconds` after deliberately removing the cap from the incremental implementation.
   - RED: `Next call 7=6.4s, want 5s`.

5. **Browser close / zero-subscriber retirement while retry waits**
   - Added `TestHubRPCThreadReadClientCloseCancelsRelayRecoveryWait`.
   - RED: `relay did not observe zero subscribers while retry waited`; the supervisor was blocked wholly inside `Wait`.
   - GREEN implementation services idle ticks concurrently with the wait and cancels/joins the wait on retirement.

6. **Canceled recovery cannot resubscribe**
   - Full race verification exposed an upstream-close/cancellation race as `panic: close of closed channel` in `exactRPCSource`, proving an extra post-cancel `SubscribeThread` call.
   - Added a focused regression; before the fix it failed 5 of 20 repetitions with `canceled relay supervisor subscribed again`.
   - The final deterministic regression exercises `subscribeRelayRecovery` with an already-canceled context and proves the external source boundary is not entered.

7. **Required behaviors already covered before new production behavior or preserved as lifecycle regressions**
   - Initial concurrent subscribe failure: `TestHubRPCThreadReadPropagatesInFlightRelaySubscribeFailure`, improved from a 50ms polling assertion to an explicit waiter-joined channel; both callers receive one shared failure and only one subscribe call occurs.
   - Recovery reread dedup: `TestHubRPCThreadReadRereadJoinsRelayRecovery` proves the reread joins the ready handle and no duplicate subscribe occurs while the fake clock blocks.
   - Cancellation removes handle/replacement can start: the client-close test starts a replacement relay after observing idle delete and canceled wait.
   - Zero-subscriber retirement stops retries and downstream replacement retires old relay: `TestHubRPCThreadReadReplacementStopsOldRelayRecovery`.
   - Old cleanup cannot delete replacement: retained `TestHubRPCThreadReadKeepsReplacementRelayTrackedAfterIdleCleanup`.
   - Downstream replacement behavior: retained `TestHubRPCThreadReadReplaceSubscriptionDropsPreviousRelaySubscriber`.

## GREEN and verification evidence

Environment-sensitive commands used:

- Canonical private external root (`pwd -P`): `/private/var/folders/43/prgnkdr95317fd_zbljq8thm0000gn/T/serf-sandbox-96935385/task5`
- `TMPDIR` set to that private directory.
- `GOCACHE` set to its private `gocache` child.
- `PATH` prefixed with `/Applications/GitHub Desktop.app/Contents/Resources/app/git/bin` for full package runs.

The brief's literal shared `GOCACHE=/tmp/serf-gocache` was attempted first and rejected by the sandbox with `operation not permitted`; `/tmp` directory creation was likewise denied. The session-provided external private TMPDIR was therefore canonicalized with `pwd -P` and used instead.

Final results:

- `go test ./cmd/serf-hub -run '^TestHubRPCThreadReadRecoversEstablishedRelayAfterSourceClose$' -count=1 -v`
  - PASS, `ok ... 0.380s`
- `go test ./cmd/serf-hub -run 'Relay|Relays|SubscribeFailure|ReplaceSubscription' -count=1`
  - PASS, `ok ... 1.960s`
- `go test -race ./cmd/serf-hub -run 'Relay|Relays|SubscribeFailure|ReplaceSubscription' -count=1`
  - PASS, `ok ... 3.179s`
- `go test ./cmd/serf-hub -count=1`
  - PASS, `ok ... 41.080s`
- `go test -race ./cmd/serf-hub -count=1 -timeout=4m`
  - PASS, `ok ... 78.843s`
  - The timeout was only a deadlock guard. A prior equivalent full race run also passed in 80.300s before the final lock/test refinement.
- `go vet ./cmd/serf-hub`
  - PASS, no output.
- `git diff --check`
  - PASS, no output.

One combined shell command was stopped by the harness's 120-second command runtime while the full race portion was still silent/running. It produced no test failure. The full race command was rerun independently with an extended runtime and passed; final post-change rerun above also passed.

## Concurrency, locking, cancellation, and identity review

- `relayMu` protects the relay registry, handle identity, `ready` close/publication, stored initial error, and `cancel` publication/read.
- No caller blocks on `ready` while holding `relayMu`.
- Initial `SubscribeThread` is deliberately outside `relayMu`; the registry placeholder ensures concurrent callers join it rather than duplicate it.
- `SubscriberCount` is first checked without `relayMu`, then checked again under `relayMu`, preserving the established double-check that allows a subscriber arriving in the idle window to keep the relay alive.
- Lock ordering remains relay registry then appserver subscriber registry only for the short second idle check. Appserver operations do not acquire `relayMu`, so no reverse order exists.
- Cleanup and idle deletion compare `relayedThreads[relayKey] == relayHandle` before deleting. An old supervisor therefore cannot delete a replacement relay.
- Retry wait cancellation is joined via a buffered result channel before supervisor return. The injected clock contract must honor context; both real and fake implementations do.
- The supervisor checks cancellation before recovery subscription, and `subscribeRelayRecovery` repeats the synchronous check at the external boundary. This closes the cancellation/upstream-close select race found by the full race suite.
- `stopRelay` reads `handle.cancel` under `relayMu`, eliminating the prior unsynchronized cancel-field access.
- Concurrent rereads after readiness observe the same active handle, add/replace only their downstream subscription, and never create a second supervisor.
- Backoff is supervisor-local; success/notification resets cannot race with another relay.
- Existing local image enrichment state remains supervisor-local and is preserved across recovered upstream subscriptions.

## Scope and cleanliness

Before commit, production/test scope was exactly the four allowed files:

- `cmd/serf-hub/app_relay.go`
- `cmd/serf-hub/app_rpc.go`
- `cmd/serf-hub/app_rpc_test.go`
- `cmd/serf-hub/internal/hubcore/config.go`

`config.go` was necessary for a per-server injected retry wait seam, avoiding a shared mutable package global and race-prone test overrides. The only additional file is this required report.

Final clean-worktree evidence and exact committed diff check are appended after the commit.

## Concerns

None.
