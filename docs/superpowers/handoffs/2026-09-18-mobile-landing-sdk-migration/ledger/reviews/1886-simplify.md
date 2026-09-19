# /simplify review — PR #1886 (D25e C4, head f60f0dc93)

Scope: reuse, simplification, efficiency, altitude only. Correctness is RoboRev's (clean).
Reviewed `git diff -M origin/main...pr-1886-review`: `appwire-client/typescript/state/mutation/{commitFeed,projectionWork}.{ts,test.ts}`,
`state/mutation/index.ts`, `tsconfig.build.json`, `cmd/evener-hub/frontend/src/panes/session/composer/queue/pendingTurnsStore.ts`.

## Must fix before merge

### 1. `appwire-client/typescript/state/mutation/commitFeed.test.ts:7-25` — third byte-identical copy of `outboxRecord`/`recoveryRecord`, while the subpath already has a shared-fixtures module

The two builders are character-for-character the same as `projection.test.ts:10-28`, and the same content as
`pendingTurns.test.ts:21-59`. `state/mutation/testing.ts` exists for exactly this ("Test-only builders shared by
this subpath's suites … rather than each suite hand-rolling (or casting past) the shape") but today only holds
`threadModel`.

- Cost: every future required field on `MutationOutboxRecord`/`MutationRecoveryRecord` has to be added in three
  test files instead of one, and the copies can drift apart silently (a stale default in one suite still compiles).
- Simpler form: move `outboxRecord` and `recoveryRecord` into `testing.ts` next to `threadModel` and import them
  in `commitFeed.test.ts` (and, in the same commit, delete the two now-redundant copies in `projection.test.ts`
  and `pendingTurns.test.ts` — pure fixture moves, no assertion changes). ~20 lines net deletion.
- Severity: must-fix-before-merge (duplicates an existing helper; the repo rule is extract-and-share rather than
  copy).

## Follow-up

### 2. `commitFeed.test.ts:27-37` — `testStore()` re-hand-rolls `pendingTurns.test.ts`'s fake ports

`fakeThreadsPort` / `fakeDraftPort` / an identity stub already exist in `pendingTurns.test.ts:65-86` and
`:11-19`; `testStore()` inlines the same three literals with variation (draft port as an inline object, identity
as `() => true`).

- Cost: a fourth shape of "a pending-turns store wired to nothing", so a port signature change edits N suites.
- Simpler form: export the fake ports (or a `testPendingTurnsStore(overrides)`) from `testing.ts` and call it
  here. Same change as finding 1, same commit.
- Severity: follow-up (fold into 1 if that is done now).

### 3. `appwire-client/typescript/state/mutation/projectionWork.ts:18-19` — timer id typed `unknown` forces a cast at every binding site, and diverges from the outbox timer-port convention

`outbox.ts:141-142` types its timer pair concretely (`setInterval?: (callback, ms) => number; clearInterval?:
(intervalId: number) => void`). The new port types the handle as `unknown`, so each binding must cast it back:
`pendingTurnsStore.ts:77`, `projectionWork.test.ts:7`, `projectionWork.test.ts:45` — three
`as ReturnType<typeof setTimeout>` casts in this diff alone, and `let timer: unknown` inside `settle()`.

- Cost: every host and every test that binds the port writes a cast that type-checking no longer helps with;
  native's binding will add a fourth.
- Simpler form: make the handle a type parameter and let it infer, e.g.
  `interface MutationProjectionWorkPorts<TimerId = unknown> { setTimeout(cb: () => void, ms: number): TimerId;
  clearTimeout(timerId: TimerId): void; yieldMacrotask(): Promise<void> }` with
  `createMutationProjectionWorkTracker<TimerId>(ports: MutationProjectionWorkPorts<TimerId>)`. Every cast then
  disappears (the web infers `number`, the vitest fake infers `NodeJS.Timeout`) and nothing about the
  "a timer is a pair" convention changes.
- Naming nit while there: this subpath names a grouped host bundle `…Deps` (`PendingTurnsStoreDeps`) or
  `…Options` (`MutationOutboxOptions`), and a single port `…Port`. `MutationProjectionWorkPorts` is a third
  convention for the same thing.
- Severity: follow-up.

### 4. `projectionWork.ts:47-70` — `settle()` builds the 4s tripwire even when nothing is outstanding

`outstanding` is empty on the last round of every settle loop (the web's flush helper repeats until it reports
zero), yet the call still allocates a `setTimeout`, a never-resolving promise and a `clearTimeout`.

- Cost: one timer + two promises per settled flush across the web suite; more importantly it reads as if a
  stall were possible with no work tracked.
- Simpler form: `if (outstanding.length === 0) { await ports.yieldMacrotask(); return 0; }` before the race.
  Behaviour identical (an empty `allSettled` already wins the race in the same microtask).
- Severity: follow-up, low.

### 5. `projectionWork.ts:1-16, 59` — a host-agnostic package module explains itself, and names itself, in the web test harness's terms

The module comment carries React `act()`, vitest and issue #1187, and the thrown message is
`pending-turns projection work stalled: …` — but the tracker is generic over any host's durable projection work
and will be bound by native next.

- Cost: native's first stall will report itself as a "pending-turns" failure, and the comment sends a reader to
  a React-specific incident to understand a module that names no framework.
- Simpler form: keep the mechanism note ("work whose completion never lands must fail loudly"), move the
  act()/vitest/#1187 paragraph to the web adapter's comment where it is true, and either drop `pending-turns`
  from the message or take a label in the ports. The web oracle (`pendingTurnsStore.test.ts:502`) matches
  `/projection work stalled: …/`, so the prefix is free to change.
- Severity: follow-up, low.

### 6. Altitude — the tripwire and the macrotask yield are test-harness policy, and they are what forced the port bundle

`createMutationProjectionWorkTracker`'s only two non-trivial behaviours (a 4-second bound, and one macrotask hop
after the wait) exist to serve `settlePendingTurnsProjectionForTests`; they are why the package now needs three
host ports at all. A tracker that exposed `track`, `clear` and `whenIdle(): Promise<number>` (no timers, no
yield) would need zero ports, and the host's test helper would keep its own 4s bound and its own
`MessageChannel` drain — both of which are statements about vitest/React, not about mutation projection.

- Cost as shipped: three ports every future host must bind, plus a test-only policy constant living in shipped
  package code.
- Note: lifting the tripwire is the D25e C4 row's stated scope, so this is a question for the row's owner rather
  than a defect. Worth deciding before native binds the same ports in D25d/C5.
- Severity: follow-up (design question).

### 7. `cmd/evener-hub/frontend/src/stores/threads.ts:642-645` — the web's private `MutationCommit` is now a duplicate of the package's exported one

This PR makes `MutationCommit` a package type; the web keeps its own identical local interface (and satisfies the
port structurally).

- Cost: two declarations of one wire-ish shape; a field added to one is silently absent from the other until a
  structural mismatch surfaces at the `subscribe` call site.
- Simpler form: `import type { MutationCommit } from "@evener/appwire-client/state/mutation"` in `threads.ts`
  and delete the local interface.
- Caveat / why not must-fix: the same structural duplication already exists on main for
  `MutationPersistenceSnapshot` (`threads.ts:911-915` vs `projection.ts:33-37`), and `projection.ts`'s comment
  treats "the web's own already has this exact shape" as the intended convention. Cleaning both together in one
  follow-up is the coherent move.
- Severity: follow-up.

## Checked and clean

- No dead code left in the web adapter: every import is used, `trackProjectionWork` is a deliberate 1-line alias
  with 5 call sites, and the old `inFlightProjectionWork` set / `awaitOutstandingProjectionWork` /
  `SETTLE_STALL_TRIPWIRE_MS` are fully deleted with no dangling references anywhere in the tree.
- No existing yield/macrotask port, commit-feed helper or promise-set tracker in the package for the new code to
  reuse (grepped `yieldMacrotask|MessageChannel|macrotask`, `wire*`, `subscribe*` across
  `appwire-client/typescript`); `MutationProjectionRefresh` is a refresh *result*, not a refresh callback, so
  `commitFeed.ts`'s inline `refresh: (ref?: string) => void` is not re-declaring it.
- `commitFeed.ts`'s single-record map insert is genuinely not `replaceTargetRecords`' whole-target replacement;
  no duplication there.
- Efficiency: no repeated work introduced (the commit fast path and the per-ref refresh fan-out are moved
  verbatim); the two long-lived closures capture only the store/fence/feed/refresh and a `Set`, nothing large.
- `index.ts` and `tsconfig.build.json` rows are in the existing alphabetical order.

## Verdict

Follow-up only, with one cheap must-fix: seven findings, six of them follow-ups and none of them behavioural.
The lift itself is honest — the web adapter is a real adapter, the moved code is verbatim, the deleted code is
fully deleted, and the two new port interfaces sit in the right module. The one thing worth fixing in this PR is
finding 1: `commitFeed.test.ts` adds a third byte-identical copy of `outboxRecord`/`recoveryRecord` when
`state/mutation/testing.ts` exists precisely to hold this subpath's shared builders, and the fix is a ~20-line
net deletion with no assertion changes (finding 2, the duplicated fake ports, rides along for free). Everything
else — the `unknown` timer handle that costs three casts and diverges from `outbox.ts`'s typed timer pair, the
tripwire built even when nothing is outstanding, the React/vitest-specific comment and "pending-turns" message
now living in a host-agnostic module, the web's duplicate `MutationCommit`, and the altitude question of whether
a test-only tripwire and macrotask yield belong in shipped package code at the cost of three host ports — is
follow-up material, and the last one is a design call for the row's owner before native binds the same ports.
