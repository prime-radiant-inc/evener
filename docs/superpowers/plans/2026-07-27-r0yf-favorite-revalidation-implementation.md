# r0yf Favorite Revalidation Implementation Plan

Date: 2026-07-27
Status: Approved for implementation
Design authority: `docs/superpowers/specs/2026-07-26-legacy-favorite-cleanup-design.md`
Kata: `r0yf`

## Global constraints

- Never automatically delete, rewrite, migrate, quarantine, compact, or age out a favorite decision row.
- Revalidation is read-only and must not change `decided_at`, including for false, malformed, dormant, ambiguous, or confirmed-invalid decisions.
- Absence is never positive invalidity evidence. UI caps, stale remote caches, failed remote lists, missing metadata, malformed lineage, and identity ambiguity produce a dormant result.
- A decision is valid only against a complete, uncapped, canonical navigation and lineage snapshot. Tree projection and favorite revalidation must consume the same snapshot generation.
- New writes retain the existing `ndr0` validation boundary.
- `FavoriteStore.Delete` is allowed only after explicit user-confirmed canonical artifact deletion has successfully removed the matching governed artifacts.
- Tests are deterministic and local, use fixed clocks and `hubtest` identifiers, assert structured behavior, and follow `docs/testing.md`.
- Make the smallest reasonable changes; keep persistence storage separate from presentation classification.

## Task 1: Add the pure authority and revalidation seam

### Goal

Create the smallest pure `hubcore` seam that classifies stored favorite decisions for presentation without mutating persistence.

### Test-first requirements

Add focused tests before production code and verify they fail for the missing behavior:

1. A persisted ended local top-level session is valid even when not live.
2. A top-level session beyond the rendered tier cap remains valid.
3. A completely observed nested subagent and a fork-superseded parent are confirmed invalid for presentation.
4. An orphan fork that is independently top-level is valid.
5. Failed/unavailable remote authority, missing metadata, conflicting aliases, malformed lineage, and identity collisions are dormant.
6. A current synthetic cluster identity is confirmed invalid only from current collision-free classification, never from a `cluster:` prefix.
7. Canonical local/ref aliases resolve only when one-to-one; ambiguous matches remain dormant.
8. Project decisions use canonical project identity.
9. False decisions never enter presentation and remain untouched.
10. The classifier has no persistence dependency or mutation path.

### Implementation constraints

- Reuse the same `nestedSessionIDs` / `TopLevelSessionIDs` lineage policy used by tree construction.
- Represent valid, confirmed-invalid, and dormant explicitly in memory.
- Represent source completeness and ambiguity explicitly; do not infer completeness from rendered slices.
- Keep `FavoriteStore` persistence-only and do not change the SQLite schema.
- Do not reserve string prefixes.

### Verification and commit

- Run the new focused hubcore tests and the existing favorite/tree hubcore tests.
- Run `go test ./cmd/serf-hub/internal/hubcore -count=1`.
- Run the relevant module linter.
- Commit the task with its tests and report the exact red/green evidence.

## Task 2: Integrate revalidation into one tree snapshot

### Goal

Make `/api/tree` revalidate favorites from the same complete raw navigation snapshot used to build the tree, before clustering and tier caps.

### Test-first requirements

Add endpoint tests before production code and verify they fail for the missing behavior:

1. Valid ended/offline sessions retain favorite flags and pinned eligibility.
2. A valid favorite beyond the 50-row presentation cap can still appear in the pinned projection.
3. Confirmed nested/subagent and fork-superseded decisions are omitted from favorite flags and pinned results while their store rows remain byte-for-byte unchanged.
4. A failed remote list with last-known-good rendering leaves an absent favorite dormant and stored; a later successful authoritative list makes it visible again without a new write.
5. Missing or ambiguous authority produces dormant presentation without mutation.
6. Current synthetic clusters and legitimate `cluster:`-shaped real IDs follow classification, not spelling.
7. Archived rows remain outside the pinned tier under existing archive rules.
8. A favorite-store read failure returns a tree error instead of a successful empty favorite set.
9. Tree and favorite authority cannot observe different source generations.

### Implementation constraints

- Build the authority index from full pre-presentation inputs.
- Apply revalidation before cluster folding and tier caps.
- Build pinned candidates from the complete valid top-level unarchived set, not only rendered `Current` and `Recent` slices.
- Do not add a dormant-favorites frontend surface or change the frontend's last-successful-tree behavior.
- Do not call `FavoriteStore.Set` or `FavoriteStore.Delete` from tree/read/revalidation code.

### Verification and commit

- Run focused tree API tests, including repeated runs of source-failure and cap cases.
- Run existing favorite endpoint, tree endpoint, and hubcore tree tests.
- Run the relevant Go module tests and linter.
- Commit the task with exact red/green evidence.

## Task 3: Tighten explicit deletion and write-boundary contracts

### Goal

Ensure decision rows are physically removed only after explicit canonical artifact deletion succeeds, while preserving the existing `ndr0` write boundary.

### Test-first requirements

Add or strengthen tests before production changes and verify the intended failures:

1. Every project artifact-removal failure, including late API-log removal failure, preserves matching session and project decision rows.
2. A partially skipped project retains its project favorite and every skipped session decision.
3. Successful canonical deletion removes only decisions for artifacts actually removed.
4. Project ID/working-directory mismatch and live/locked targets mutate neither artifacts nor decisions.
5. A favorite-store deletion failure is reported after artifact removal and leaves the row as harmless dormant state.
6. Existing `ndr0` rejected targets still perform no write.
7. Existing accepted top-level, capped-away, and orphan targets still persist canonical decisions.

### Implementation constraints

- Order every matching `FavoriteStore.Delete` after all governed artifact operations required for that identity.
- Never broaden a deletion target using read-time revalidation.
- Preserve project rows whenever any governed session is skipped or fails.
- Do not add backward-compatibility aliases, schema changes, cleanup jobs, or a new deletion endpoint.

### Verification and commit

- Run focused project-delete and favorite endpoint tests, including repeated failure-path runs.
- Run all `cmd/serf-hub` tests, `make vet`, and the relevant linters.
- Commit the task with exact red/green evidence.

## Final branch verification

- Audit the branch diff against every global constraint and the approved design.
- Prove no read/tree/startup/revalidation path calls a favorite mutation.
- Run focused hubcore, favorite endpoint, tree endpoint, and project-delete tests.
- Run `make test`, `make vet`, `make lint`, and `make build` sequentially.
- Run a fresh whole-branch Luna xhigh review before integration.
