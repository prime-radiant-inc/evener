# r0yf Task 1 Implementation Report

## Status

DONE. Task 1 adds the pure hubcore authority/revalidation seam and focused behavior tests. The controller-owned fresh acceptance review remains the next external gate.

Scope stayed limited to hubcore authority classification. No tree API integration, remote I/O, tree-generation ownership, project deletion, frontend code, cleanup path, FavoriteStore behavior, or SQLite schema changes were made.

No `NEEDS_CONTEXT` condition was found: the seam can accept the required canonical inputs and explicit completeness/ambiguity facts without owning collection or lifecycle boundaries.

## Files and interfaces

Created `cmd/serf-hub/internal/hubcore/favorite_authority.go`:

- `FavoriteAuthorityQuality` with explicit `complete`, `incomplete`, and `ambiguous` values.
- `FavoriteSessionAuthority` for canonical session IDs, caller-supplied local/ref aliases, top-level status, lineage quality, and source quality.
- `FavoriteProjectAuthority` for canonical `identifier.Project.ID` facts.
- `FavoriteNodeAuthority` and `FavoriteNodeKind` for current node classification, including collision-checked synthetic clusters.
- `FavoriteAuthority` as the pure in-memory input bundle.
- `FavoriteDecisionState` with explicit `valid`, `confirmed-invalid`, and `dormant` states.
- `FavoriteDecisionClassification` and `FavoriteRevalidation` as the read-time result.
- `ClassifyFavoriteDecisions(decisions map[ArchiveKey]bool, authority FavoriteAuthority) FavoriteRevalidation`.

The classifier accepts already-collected facts only. It does not read or mutate `FavoriteStore`, perform persistence, resolve projects, perform remote work, inspect tree generations, infer completeness from caps, or reserve string prefixes. Callers are responsible for deriving `TopLevel` from the full metadata set with the existing `TopLevelSessionIDs`/`nestedSessionIDs` policy.

Created `cmd/serf-hub/internal/hubcore/favorite_authority_test.go` with deterministic fixed timestamps, `hubtest.SessionID`/`hubtest.ProjectDir` identifiers, and structured assertions for all Task 1 behaviors.

## RED evidence

Initial behavior tests were written before production code.

Command:

```text
gofmt -w cmd/serf-hub/internal/hubcore/favorite_authority_test.go && go test ./cmd/serf-hub/internal/hubcore -run 'TestClassifyFavoriteDecisions' -count=1
```

Result: exit 1. The package failed at the missing seam, including:

```text
undefined: FavoriteAuthority
undefined: ClassifyFavoriteDecisions
undefined: FavoriteDecisionValid
undefined: FavoriteSessionAuthority
undefined: FavoriteRevalidation
undefined: FavoriteDecisionState
FAIL
```

After the independent review identified the cluster-alias collision gap, regression tests were added first.

Command:

```text
gofmt -w cmd/serf-hub/internal/hubcore/favorite_authority_test.go && go test ./cmd/serf-hub/internal/hubcore -run 'TestClassifyFavoriteDecisions' -count=1
```

Result: exit 1, with the expected missing behavior:

```text
--- FAIL: TestClassifyFavoriteDecisions_ClusterAliasCollisionsAreDormant
    favorite_authority_test.go:167: classification for {session cluster:alias-collision} = "confirmed-invalid", want "dormant"
FAIL
```

## GREEN evidence

Focused authority tests after the minimal implementation and collision fix:

```text
gofmt -w cmd/serf-hub/internal/hubcore/favorite_authority.go cmd/serf-hub/internal/hubcore/favorite_authority_test.go && go test ./cmd/serf-hub/internal/hubcore -run 'TestClassifyFavoriteDecisions' -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.292s
exit_code=0
```

Full hubcore package after the final fix:

```text
go test ./cmd/serf-hub/internal/hubcore -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.438s
exit_code=0
```

Relevant module linter after the final fix:

```text
golangci-lint run ./cmd/serf-hub/internal/hubcore
0 issues.
exit_code=0
```

The first linter run found only two Go 1.25 modernization findings (`slices.Contains` and `for i := range 51`); both were fixed while tests were green, then the linter was rerun successfully.

Formatting and whitespace check:

```text
git diff --check
exit_code=0
```

## Important finding and fix

The independent review found that cluster collision detection considered only canonical session IDs. A cluster ID that matched a supplied session alias could therefore be confirmed-invalid, and an alias resolving to a canonical ID that collided with a cluster could be valid. Both cases violate the collision-dormant rule.

The fix indexes cluster collisions against both canonical IDs and aliases, and checks the resolved canonical target for collision status before returning valid. Regression tests cover both directions. The same review also identified that `FavoriteStore.Favorites()` filters false rows, so the persistence test now reads the actual SQLite `favorited` and `decided_at` fields before and after classification.

## Self-review

- Ended/offline local top-level sessions resolve valid without a live entry.
- Full authority inputs are independent of the rendered tier cap.
- Complete nested subagent and fork-superseded parent facts resolve confirmed-invalid; orphan forks remain valid.
- Incomplete source/lineage evidence, absent metadata, ambiguous aliases, duplicate identities, and node collisions resolve dormant.
- Synthetic cluster invalidity requires a current complete cluster node with no identity collision; no `cluster:` prefix check exists.
- Local/ref aliases project only through a one-to-one alias map and preserve the stored key.
- Project decisions resolve only against canonical project IDs.
- False decisions are excluded from presentation, and the input decision map plus SQLite row values remain unchanged.
- The classifier has no persistence or mutation dependency; `FavoriteStore` and its schema are untouched.

## Concerns

Task 2 must construct these authority facts from the same complete, uncapped navigation snapshot used for tree construction and must pass the existing full-snapshot lineage result into `FavoriteSessionAuthority`. This task intentionally does not provide that integration, remote source collection, or generation coordination.

The controller-owned fresh acceptance review was requested after the final fix and is not included in this report's local command evidence.

## Commits

- `648e674ad` — `feat(hubcore): add pure favorite authority classification`
- `5af07b630` — `fix(hubcore): keep alias cluster collisions dormant`

## Acceptance review follow-up: Important finding

Status: fixed.

Finding: `cmd/serf-hub/internal/hubcore/favorite_authority.go` treated zero or unknown `FavoriteNodeKind` values as non-cluster nodes. With a complete node sharing a valid session authority, that allowed the session decision to classify valid. Unknown and zero kinds must conservatively be ambiguous so affected decisions remain dormant. Only `FavoriteNodeSession`, `FavoriteNodeSubagent`, `FavoriteNodeFork`, and `FavoriteNodeCluster` are accepted kinds.

### RED evidence

Regression test added first:

```text
gofmt -w cmd/serf-hub/internal/hubcore/favorite_authority_test.go && go test ./cmd/serf-hub/internal/hubcore -run 'TestClassifyFavoriteDecisions_UnknownNodeKindCollisionIsDormant' -count=1
```

Result: exit code 1, with both complete-node cases incorrectly classified as valid:

```text
--- FAIL: TestClassifyFavoriteDecisions_UnknownNodeKindCollisionIsDormant (0.00s)
    --- FAIL: TestClassifyFavoriteDecisions_UnknownNodeKindCollisionIsDormant/zero (0.00s)
        favorite_authority_test.go:202: classification for {session 033wbPGJIeaTz5nAoeq0r8} = "valid", want "dormant"
    --- FAIL: TestClassifyFavoriteDecisions_UnknownNodeKindCollisionIsDormant/unknown (0.00s)
        favorite_authority_test.go:202: classification for {session 033wbPGJIid9RPIRsyxpxA} = "valid", want "dormant"
FAIL
FAIL	primeradiant.com/serf/cmd/serf-hub/internal/hubcore	0.372s
FAIL
exit_code=1
```

### Fix and GREEN evidence

The smallest fix adds an explicit switch over the four accepted node kinds. The default branch marks the complete node identity ambiguous, reusing the existing dormant classification path. Known non-cluster kinds retain their prior behavior, and cluster handling is unchanged.

Focused classifier tests:

```text
gofmt -w cmd/serf-hub/internal/hubcore/favorite_authority.go cmd/serf-hub/internal/hubcore/favorite_authority_test.go && go test ./cmd/serf-hub/internal/hubcore -run 'TestClassifyFavoriteDecisions' -count=1
ok  	primeradiant.com/serf/cmd/serf-hub/internal/hubcore	0.308s
exit_code=0
```

Full hubcore package:

```text
go test ./cmd/serf-hub/internal/hubcore -count=1
ok  	primeradiant.com/serf/cmd/serf-hub/internal/hubcore	0.476s
exit_code=0
```

Hubcore lint:

```text
golangci-lint run ./cmd/serf-hub/internal/hubcore
0 issues.
exit_code=0
```

Whitespace verification:

```text
git diff --check
exit_code=0
```

### Follow-up commit

- `e5fb25f43` — `fix(hubcore): make unknown favorite node kinds dormant`
