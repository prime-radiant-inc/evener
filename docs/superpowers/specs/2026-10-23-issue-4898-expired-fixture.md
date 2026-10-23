# Issue #4898: deterministic session metadata fixture

## Scope and acceptance

Fix `stores requested aliases separately from resolved identity with typed session metadata`
in `cmd/evener-hub/frontend/src/stores/sessionEntryCache.test.ts` without changing
product code or cache TTL behavior. The test must keep proving committed metadata,
persisted aliases, resolved identity and exact typed metadata round-trip.

The user authorizes a fixed/injected test clock and a regression against the real
clock. This specification chooses the existing constructor `now` seam, not a new
clock abstraction or fake timer scheduler. Only this test and this spec need change;
shared test support remains unchanged unless verification proves it necessary.

## Failure and exact boundary

The fixture supplies `savedAt: 1_791_550_000_000` and
`accessedAt: 1_791_550_012_345`. The latter is
`2026-10-09T12:46:52.345Z`. Its cache constructor omits `now`, selecting `Date.now`.
The unchanged default TTL is `14 * 24 * 60 * 60 * 1000 = 1_209_600_000` ms.

`SessionEntryCache.#enforcePolicy` expires a finite positive timestamp when
`timestamp + ttlMs <= now`. It prefers finite `accessedAt` to `savedAt`, so the
fixture is deleted during the write at and after **2026-10-23T12:46:52.345Z**
(`1_792_759_612_345`). A successful transaction can therefore return `committed`
while both subsequent metadata reads return `undefined`.

On main `0f537f9f2914c76677379f4716e0b00cc2889be2`, merged into this lane before
this specification, the unchanged named test reproduced that failure: exit 1,
one failed and 74 filtered-out tests. The persisted-row assertion and
`getSessionMetadata` assertion fail at original lines 1369 and 1371.

## Fixed-time model and regression

1. Define the fixture's explicit cache time as `1_791_550_012_345`
   (`2026-10-09T12:46:52.345Z`). Inject `now: () => fixtureNow` into this test's
   `SessionEntryCache` constructor, using the existing seam.
2. Derive `savedAt` as `fixtureNow - 12_345` and `accessedAt` as `fixtureNow`.
   These retain the original values and preserve the earlier-save/later-access
   relationship. The default 14-day TTL remains in force.
3. Parameterize the existing named test over two hostile ambient `Date.now`
   values: the exact original expiry instant (`2026-10-23T12:46:52.345Z`) and
   `2100-01-01T00:00:00.000Z`. Install a `Date.now` spy returning each value
   before constructing the cache, because the default constructor captures the
   function. Do not fake timers, `setImmediate`, or IndexedDB scheduling.
4. Keep every existing assertion and real cache/IndexedDB operation. The existing
   `afterEach` restores mocks and closes/deletes the test-owned databases.

The fixture cannot expire with the injected clock: its access age is always zero,
regardless of calendar time or test duration. The hostile ambient clock makes the
same metadata expired without injection. Both parameter cases must fail if the
constructor's `now` option is removed or retention falls back to `Date.now`.
This proves clock independence behaviorally, rather than asserting spy call counts
or merely moving the fixture's deadline forward.

## Preservation, recovery and non-goals

The server transcript remains authoritative and IndexedDB remains a derived cache,
as specified in subsystem S02. No user-visible behavior, affected-client contract,
TTL comparison, LRU policy, storage schema, quota, production clock default or
recovery owner changes. The existing TTL tests continue to cover real expiration.
No assertions are weakened, no test is skipped, and no full local suite is run.
No date bump, increased TTL, unlimited TTL, global fake clock or new test-support
API is part of this change. Unrelated findings belong in separate GitHub issues.

## Verification and delivery

Run from `cmd/evener-hub/frontend` with lockfile-matched dependencies. Never run
`npm ci` through a symlinked `node_modules`. Use an absolute task-owned
`EVENER_FRONTEND_VITE_CACHE_DIR` and `--configLoader runner` to avoid writing into
the shared install.

- Red, already reproduced before editing the test:
  `./node_modules/.bin/vitest run --configLoader runner --maxWorkers=2 src/stores/sessionEntryCache.test.ts -t 'stores requested aliases separately from resolved identity with typed session metadata'`.
- Add the hostile-clock parameterization before injection and run the same
  command: both cases must fail with missing metadata. Then inject the fixed
  clock and derive the fixture timestamps.
- Run that named-test command at least three times successfully. Each invocation
  must execute both ambient-clock cases, not skip them.
- Run the whole file with
  `./node_modules/.bin/vitest run --configLoader runner --maxWorkers=2 src/stores/sessionEntryCache.test.ts`.
- Run `npm run typecheck` (`tsc --noEmit --incremental false`).
- Run touched-file formatting and checking from the frontend directory:
  `./node_modules/.bin/biome check --write src/stores/sessionEntryCache.test.ts`,
  then `./node_modules/.bin/biome ci src/stores/sessionEntryCache.test.ts`.
- Freeze this spec and get two independent competing PAR reviews before
  implementation. Review the immutable implementation diff with PAR, then run
  simplify-code and repeat affected checks after any edits.
- Commit, run roborev-review-branch or the documented
  `roborev review --branch --wait` equivalent, and inspect the actual findings.
- Fetch/merge current `origin/main` before the final push if it moved. Push one
  reviewed round, open a PR against main, and shepherd matching-head CI/reviews.
  Admin squash merge only after every required check is green and RoboRev is
  clean. Verify GitHub's merged state and merge SHA.

Report the issue, PR URL, merge SHA, clock model, changed paths, red/green command
results, review findings and dispositions, and anything unverified. Product docs
stay unchanged because no product behavior or ownership changes.
