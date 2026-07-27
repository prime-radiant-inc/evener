# Mid-Turn Reload Race Investigation Plan

> **For agentic workers:** This is an investigation log for kata 3ekx. Follow the repository's systematic-debugging and deterministic-testing rules; do not implement a production change until an interleaving below is reproduced or otherwise proven.

**Goal:** Determine whether a reload/reconnect can leave a live mid-turn transcript stale, and either fix the proven root cause with a regression or leave an evidence-backed no-change audit.

**Architecture:** Trace the browser `AppwireClient` and `threads` store, the hub relay and `WithoutCancel` lifecycle, and `internal/appserver` connection/subscription registration. Compare live reducer folds with `thread/read` hydration/replay. Use exact boundary gates and fake transports/clients first; use raw CDP/network throttling only as supplemental evidence.

**Tech Stack:** TypeScript/Vitest frontend tests, Go tests for hub/appserver/source seams, WebSocket Appwire protocol, deterministic channel gates.

## Global Constraints

- Kata: `3ekx`; branch: `wip/kata-midturn-reload`; base: `b1b05cc1a`.
- Read `AGENTS.md` and `docs/testing.md` before changing tests; default tests remain deterministic and offline.
- Do not touch rail, Steering, task-state, diff-color, or tooling-security areas; do not close or merge the kata.
- First hypotheses are tested one at a time; no production change before a failing regression or an equivalent proof.

## Phase 1: Root-cause probes

1. **Frontend snapshot-clobber hypothesis:** pause a reconnect `thread/read` after its subscription boundary, inject a matching live mid-turn notification, then resolve the older snapshot. Expected failure if `handleReady` replaces the live-updated model with the stale snapshot.
2. **Old/new browser connection hypothesis:** use two real `AppwireClient` instances or a controlled fake socket pair; keep the old socket live, attach the new client, and prove old notifications are detached while new notifications hydrate and route.
3. **Hub relay/source window hypothesis:** pause source subscription and appserver registration separately; force a notification during each gap and record whether hydration, relay broadcast, and the new appserver connection receive it. Include old-connection teardown delayed until after new registration.
4. **Server registry hypothesis:** exercise monotonic connection IDs, `Subscribe`/`ReplaceSubscriptions`, `Broadcast`, and delayed `unregisterConnection` with exact channel gates; compare the result with the existing teardown/idle-race tests.

## Boundary evidence to capture

- Browser client object/socket identity and server `conn-N` identity for old and new connections.
- `ensureThread`/`handleReady` request order and the exact `{subscribe:true, replaceSubscription:false}` parameters.
- Source `SubscribeThread` start/return, relay placeholder/readiness/registration/supervisor transitions, and `context.WithoutCancel` survival after browser teardown.
- Appserver subscription register/remove ordering and which connection receives each notification.
- Live reducer state before/after each injected notification and after hydration; replay/hydration state must agree with live state once the event is durable.

## Success criteria

- A deterministic regression names the exact lost event and fails on the current revision, or every forced interleaving above is shown to preserve the live/replay contract.
- If reproduced, apply one root-cause fix only after the failing regression, then run focused tests plus frontend typecheck/lint/build and full Go gates.
- If not reproduced, keep only a deterministic test that protects a real existing contract, document exact interleavings, reviewed paths, tests, and limitations, and make no production change.

## Initial evidence

- `kata show 3ekx` records three negative raw-CDP reproductions over clean localhost reloads and specifically calls out delayed/blackholed old WS or throttled new connection as untested.
- Current code already has frontend client-swap/reconnect tests and Go relay/appserver teardown tests, but no test that injects a matching live notification while `handleReady`'s snapshot request is unresolved.

## Phase 2: Forced results

The first regression was committed RED as `ddc1c57ae` before the production change. It uses `FakeClient` to hold the reconnect `thread/read` response unresolved, injects matching `item/completed` and bare `turn/completed` notifications, then resolves the stale in-progress snapshot. The old implementation applied the notifications to `threads` and then let `handleReady` replace the map entry with `hydrateThread(snapshot)`, restoring the spinner and empty output.

The controlled harness now covers these exact interleavings without sleeps:

1. Initial `ensureThread`: the notification arrives after `thread/read` is issued but before any model is published. The event is buffered by explicit `ref` (and bare `turn/completed` is queued for reducer-side active-turn routing), then replayed onto the stale snapshot.
2. Reconnect: the old model remains unchanged while a reconnect `thread/read` is pending; matching live events are buffered and folded onto the replacement snapshot before publication.
3. Overlapping clients: client A's old response is held after client B is wired. B's snapshot plus buffered completion publishes first; A's late response is ignored by the pending hydration token and client identity check.
4. Watched child thread: the same initial snapshot window is forced through `watchedThreads`, proving the additive watcher path does not retain the wholesale-snapshot clobber.

The root cause is frontend snapshot publication ordering, not notification routing or server connection-ID reuse. `threads.handleNotification` correctly routes and folds live events; `handleReady`, `ensureThread`, and `watchThread` previously published a wholesale snapshot after that fold. The fix buffers notifications per pending hydration, replays them through the same reducer, preserves frame-time bookkeeping, and publishes atomically. Superseded client/lifecycle responses cannot publish.

The server paths were audited and the existing exact-boundary tests were run. `app_rpc.go` reads the source snapshot before `startRelay`; `app_relay.go` keeps the relay context alive with `context.WithoutCancel`, subscribes the source before registering the appserver connection, and serializes existing-relay registration against idle retirement. `internal/appserver` uses monotonic production WebSocket IDs, pointer-checked teardown, and connection-scoped subscription registration. Focused passing coverage includes `TestHubRPCThreadReadRereadJoinsRelayRecovery`, `TestHubRPCThreadReadRetiresRelayWhenClientDisconnects`, `TestHubRPCThreadReadKeepsRelayWhenSubscriberArrivesDuringIdleRetirement`, `TestHubRPCThreadReadSerializesRereadRegistrationAgainstIdleRetirement`, `TestStaleConnectionTeardownPreservesSameIDReplacement`, `TestStaleBroadcastFailurePreservesSameIDReplacement`, `TestContextSubscriptionRegistrationRejectsRemovedConnection`, and `TestContextSubscriptionRegistrationSerializesWithConnectionTeardown`.

No raw-CDP throttling was added after the deterministic fake harness reproduced the missing window directly. No kata issue was created: `kata search 3ekx` returned no matches.

## Phase 3: Verification status

- Frontend focused store suite: 126 tests passed.
- Frontend `npm run typecheck`: passed.
- Frontend `npm run lint`: passed.
- Go focused suites `go test ./internal/appserver` and `go test ./cmd/serf-hub -run '^(TestHubRPCThreadRead|TestHubRelay)'`: passed.
- Remaining work: full frontend test/build and repository Go gates, final diff/clean-worktree verification.
