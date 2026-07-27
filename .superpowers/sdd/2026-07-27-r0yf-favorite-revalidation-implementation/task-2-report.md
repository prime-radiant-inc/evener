# Task 2 correction report

## Scope

Corrected the Task 2 implementation rejected at baseline `b3050ce19` on
`wip/kata-favorite-cleanup-policy`. Task 3 was not started.

The tree request now carries one immutable raw navigation snapshot through
tree construction and favorite classification. The snapshot contains rows,
carried projects, source ownership/completeness, malformed/conflicting row
identities, and generation. No read, tree, startup, cache, or revalidation
path mutates `FavoriteStore`.

## Files changed in this correction

- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_favorite_revalidation_test.go`
- `cmd/serf-hub/web.go`
- `cmd/serf-hub/main_background.go`
- `cmd/serf-hub/cov_exact_lifecycle_tree_fuzz_test.go`
- `cmd/serf-hub/internal/hubcore/remotecache.go`
- `cmd/serf-hub/internal/hubcore/remotecache_test.go`
- `cmd/serf-hub/internal/hubcore/treecache.go`
- `cmd/serf-hub/internal/hubcore/treecache_test.go`
- `cmd/serf-hub/internal/hubcore/favorite_authority.go`
- `cmd/serf-hub/internal/hubcore/tree.go`
- `cmd/serf-hub/internal/hubcore/favorite_candidates_test.go`

## Review findings

Validated and fixed: 1, 2, 3, 4's source/ref conflict and malformed
`Serf.ParentRef` paths, 5, 6, 7, 8, 9, 10, 11, and 12.

Finding 4's `ForkedFromID` subclaim was technically rejected. The existing
mapping contract treats `ForkedFromID` as a raw thread ID fallback, while
`Serf.ParentRef` is the ref-valued field. `appThreadTreeParentSessionID` and
`TestAppThreadTreeEntriesPreserveRemoteLineageAndKind` in
`cmd/serf-hub/web_api_tree_test.go` explicitly preserve the raw fork fallback
(`ForkedFromID: "parent"` becomes `remote:parent`). The correction therefore
marks malformed/conflicting source/ref identities and malformed parent refs
incomplete, but does not add a new incompatible rejection rule for valid raw
fork IDs.

## Exact RED evidence

The review regression suite was run before the correction production changes:

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_' -count=1 -v
```

It failed with these missing behaviors:

```text
ConcurrentCacheReadUsesOneSnapshot: first request paired its rows with the later incomplete authority
HealthySourceSurvivesUnrelatedFailure: healthy source favorite was hidden by unrelated failure
EmptyRemoteCacheDoesNotDowngradeLocalClusterAuthority: ... classification to "dormant"
PaginatedSnapshotIncludesCappedAwayFavorite: favorite from terminal page was not presented
LaterPageFailureDormantsLastGoodRows: later-page failure kept a last-good favorite visible
RepeatedPageCursorIsIncomplete: repeated cursor was treated as complete authority
SourceRefConflictAndMalformedRowDoNotHideHealthyIdentity: valid unrelated remote identity was hidden by malformed rows
EndedRemoteCarriedProjectCanBeFavorited: ended remote carried project was absent from tree
ProjectClaimsWithSameIDAreAmbiguous: conflicting project claims were collapsed into a valid favorite
```

The source-field conflict regression was then run against the unfixed path:

```text
go test ./cmd/serf-hub -run TestAPITreeFavoriteRevalidation_SourceFieldConflictWithoutRefIsDormant -count=1 -v
```

```text
source field conflict without ref was treated as valid
FAIL
```

The malformed-parent regression was run with its new incomplete-row check
temporarily removed:

```text
go test ./cmd/serf-hub -run TestAPITreeFavoriteRevalidation_MalformedParentRefIsDormant -count=1 -v
```

```text
malformed parent ref was treated as complete lineage
FAIL
```

The corrected memo regression was run with the rejected scalar
`inputs*2+remoteGeneration` key temporarily restored:

```text
go test ./cmd/serf-hub -run TestAPITreeFavoriteRevalidation_MemoKeyDoesNotCollideAcrossInputsAndRemoteGeneration -count=1 -v
```

```text
memo key reused a different input/generation pair: first=true second=false
FAIL
```

The temporary production reversions were restored before commit.

## Design choices

- `RemoteThreadCache` publishes rows, per-source authority, completeness,
  invalid identities, and generation atomically, with defensive copies.
- Remote list reads consume every `NextCursor`, stop on repeated cursors, and
  retain the prior complete source snapshot on later-page failure.
- Completeness is evaluated for the owning remote source and identity. An
  unrelated source failure does not hide healthy remote or local favorites.
- Ended and live remote rows retain validated carried `ProjectID` and
  `ProjectPath`. Project authority keeps separate canonical path/source claim
  keys so collisions remain ambiguous.
- The tree memo uses `hubcore.TreeCacheKey`, an explicit composite key.
- `FavoriteCandidates` applies explicit archive decisions even when a live row
  has no past metadata; age classification remains limited to metadata-backed
  sessions.
- Stored false, malformed, dormant, confirmed-invalid, and `decided_at` rows
  are read-only during revalidation. Favorite-store read failures still fail
  `/api/tree` rather than becoming an empty decision set.

## Exact GREEN evidence

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_' -count=5
ok   primeradiant.com/serf/cmd/serf-hub

go test ./cmd/serf-hub -run 'Test(FavoriteEndpoint|APITree|Web_APITree|ListThreadsWithFallback)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub

go test ./cmd/serf-hub/internal/hubcore -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore

go test ./cmd/serf-hub -count=1
ok   primeradiant.com/serf/cmd/serf-hub

go vet ./cmd/serf-hub/...
exit 0, no output

golangci-lint run ./cmd/serf-hub/...
0 issues.

git diff --check
exit 0, no output
```

The focused cache/failure/concurrency regressions were also run with
`-count=5` and passed. The source-authority cache test verifies generation,
source metadata, and defensive copies.

## Correction commit

```text
ecc380080
```

The report is committed separately because `.superpowers/` is ignored.
