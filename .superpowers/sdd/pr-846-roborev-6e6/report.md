# PR #846 Roborev remediation report

## Identity

- Review source: Roborev combined issue comment `5519610674` against PR #846.
- Exact start commit: `6e6c0b61638db071c1dd01fa13a7513a6392c8b8`.
- Branch/worktree: `navigation-entity-deltas`.
- Remote state was not fetched, pushed, merged, rebased, amended, or force-pushed.
- The implementation commit hash is recorded in the final handoff after the append-only commit is created. The report is intentionally written before that commit so it can be included in the same coherent change without self-referential commit metadata.

## Finding dispositions

### M1 — normalized V2 rail sessions lose resource context: fixed

`selectors.ts` now derives project-page tier/project and pin-section context from each normalized resource key, applies it recursively to projected sessions, and includes normalized context in session/model cache keys. This preserves archive/unarchive, pin/unpin, invalidation, and child-row behavior without mutating normalized entities or graphs.

Added behavioral coverage for archived project-page and pinned-section V2 resources, including recursive children, in `RailRow.test.tsx`.

### M2 — same-generation V1-to-V2 reconnect can retain a V1-only conditional base: fixed

`revalidator.ts` now sends a conditional base only when the installed normalized graph version exactly matches the current state version. V1-only state, missing normalized state, and version mismatches omit `base`, forcing a V2 snapshot. Equal-sequence V1-to-V2 reconnect coverage verifies retained entries reload as V2 without a base, install normalized data, and subsequently accept deltas; already-V2 equal reconnect remains a no-op.

### L1 — duplicate string-slice equality helper: fixed

`navigation_delta.go` uses `slices.Equal`; the duplicate `navEqualStrings` helper was removed from `navigation_normalize.go`.

### L2 — redundant `representationVersion` re-decode: fixed

Removed only the redundant map-field `representationVersion` re-decode from `NavigationReadParams.UnmarshalJSON`. Raw-field decoding remains for nested `base` presence/non-null validation; existing strict and V1/V2 validation behavior is retained.

### L3 — dead `gone` history plumbing and latent tombstone baseline: fixed

Removed the `gone` parameter, field, and lookup branch from navigation history. `Remember` now requires a nonnil valid snapshot, and all production/test callers were updated. Direct nil-snapshot rejection and normal history behavior remain covered.

## RED evidence

Before production changes, ran:

```text
npx vitest run src/stores/navigation/store.test.ts src/shell/rail/RailRow.test.tsx --maxWorkers=4
```

The test-only changes failed exactly the four intended new assertions (two M2 base assertions and two M1 context assertions); 240 existing tests passed. This confirmed the targeted defects before implementation.

## GREEN evidence

- `npx biome check --write` on every touched frontend `src/` file: passed using the pinned frontend Biome.
- `npx vitest run src/stores/navigation/store.test.ts src/stores/navigation/selectors.test.ts src/shell/rail/RailRow.test.tsx --maxWorkers=4`: passed, 3 files and 255 tests.
- `npm run typecheck`: passed.
- `go test -race ./appwire ./cmd/evener-hub -run 'Navigation(ReadParams|History|Delta)|navigation|Navigation' -count=1`: passed.
- `go test ./appwire ./cmd/evener-hub -count=1`: passed (`appwire` 0.429s; `cmd/evener-hub` 40.885s).
- Full frontend Vitest phase of `npm test`: passed, 394 files and 7,566 tests.
- `git diff --check`: passed.

## Lint and environmental blocks

`make lint` ran through and passed `lint-naming`, `lint-gofmt`, `lint-evenerfuzz`, `lint-eval`, `lint-internal`, `lint-golangci`, and `lint-generated`, then failed at `lint-fuzz-registry` because the sandbox denied temporary-file creation:

```text
mktemp: mkstemp failed on /var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-fuzz-registry.XXXXXX.zLIhEI6lzx: Operation not permitted
FAIL lint-fuzz-registry (0s)
make: *** [lint-fuzz-registry] Error 1
```

The remaining frontend node-script phase of `npm test` was also environment-blocked by sandbox process restrictions: scripts reported `/bin/ps: Operation not permitted`; browserGuard checks failed for that reason, and the crashpad test observed `pgid: null` instead of expected `87484`. No scripts or assertions were changed to mask these failures.

## Files changed

- `appwire/types.go`
- `cmd/evener-hub/frontend/src/shell/rail/RailRow.test.tsx`
- `cmd/evener-hub/frontend/src/stores/navigation/revalidator.ts`
- `cmd/evener-hub/frontend/src/stores/navigation/selectors.ts`
- `cmd/evener-hub/frontend/src/stores/navigation/store.test.ts`
- `cmd/evener-hub/navigation_delta.go`
- `cmd/evener-hub/navigation_delta_test.go`
- `cmd/evener-hub/navigation_history.go`
- `cmd/evener-hub/navigation_history_test.go`
- `cmd/evener-hub/navigation_normalize.go`
- `cmd/evener-hub/navigation_service.go`

At report creation, `git status --short` showed only these 11 named paths modified; no unrelated files were present. The final append-only commit hash is supplied in the handoff.
