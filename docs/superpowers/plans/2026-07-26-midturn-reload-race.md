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

