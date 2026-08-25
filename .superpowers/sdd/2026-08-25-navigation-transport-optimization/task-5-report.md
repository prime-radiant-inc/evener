# Task 5 Report — Revisioned Navigation Snapshots

## Commit

`feat(hub): own revisioned navigation snapshots` (this report is included in that commit)

## Implementation

- Created `cmd/evener-hub/navigation_service.go` with a per-server `NavigationService`.
  - Captures source input revisions, builds immutable core projections, re-reads revisions/invalidations before publishing, and retries stale captures.
  - Assigns a crypto-random 128-bit lowercase-hex generation ID by default; tests can inject a generator.
  - Tracks semantic resource fingerprints and revisions, retaining revisions across no-op refreshes and refusing resource/sequence values beyond JavaScript `MAX_SAFE_INTEGER`.
  - Uses the Task 4 representation cache to cache exactly one JSON and gzip encoding for a versioned resource.
  - Retains last-good core state on capture failures and maps context cancellation to a typed 503 availability error.
  - Exposes `VersionedKey(ctx, key)`, which atomically returns a canonical resource key with current generation and semantic revision for Task 6 HTTP routing.
  - Implements exact resource targets, including `all_loaded_projects`, and a lifecycle scheduler that uses the nearest 24-hour or 14-day tier cutover.
- Created focused service tests in `cmd/evener-hub/navigation_service_test.go` for no-op stability, targets/wildcard, stale-build retry, concurrent refresh coalescing, last-good/cancellation, generation/overflow, atomic versioned key lookup, and scheduler boundaries.
- Wired `WebServer.navigation` in `cmd/evener-hub/web.go` before AppWire server construction.
- Updated `cmd/evener-hub/app_rpc.go` and `internal/appserver/server.go` so each initialize request dynamically calls `NavigationService.Capability()` rather than advertising construction-time capability metadata. Added the focused dynamic-initialize test in `internal/appserver/server_test.go`.
- Started the service scheduler from the main lifecycle context in `cmd/evener-hub/main.go`.

## Verification

Passed using the available shared offline module cache:

```text
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestNavigationService|TestWeb.*TreeCache' -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test -race ./cmd/evener-hub -run TestNavigationService -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub/internal/hubcore -run TestTreeCache -count=1
# PASS; package reported [no tests to run] for TestTreeCache
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run TestNavigationCache -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./internal/appserver -run TestInitialize -count=1
GOMODCACHE=/Users/jesse/go/pkg/mod go test ./cmd/evener-hub -run 'TestNavigationService|TestNavigationCache|TestHubInitializeAdvertisesNavigationCapability' -count=1
# all PASS

git diff --check
# PASS
```

The two requested main lifecycle checks were attempted but could not pass in this sandbox because TCP bind is denied:

```text
TestRunMainHandsServeAnHonestlyNilCompanionWithNoCodexLaunches:
listen tcp 127.0.0.1:0: bind: operation not permitted

TestRunMainAddrZeroReportsAndBindsTheRealPort:
deps.serve was never reached (after 5s)
```

The latter failure remains when the newly added scheduler-start line is temporarily removed, so it is not caused by this task's lifecycle wiring. It needs rerun in an environment that permits loopback listeners.

## Self-review

- Resource revisions are based on payload fingerprints built with revision zero, so transport revision fields cannot create false invalidations.
- All version/key/map reads that Task 6 needs are behind the service mutex; HTTP consumers should use `VersionedKey`, not combine `Capability` and `CurrentRevision`.
- The service only creates a scheduler timer from a captured nearest 24-hour/14-day boundary and exits cleanly on the lifecycle context.
- No producer hooks were added; Task 7 remains responsible for source/mutation invalidation wiring.
