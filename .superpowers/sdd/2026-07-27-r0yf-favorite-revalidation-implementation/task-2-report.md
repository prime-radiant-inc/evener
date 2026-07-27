# Task 2 correction report

## Scope

Corrected the Task 2 implementation rejected at baseline `b3050ce19` on
`wip/kata-favorite-cleanup-policy`, then applied the second review correction
after the prior report commit `b181ac37c`. Task 3 was not started.

The tree request now carries one immutable raw navigation snapshot through
tree construction and favorite classification. The snapshot contains rows,
carried projects, source ownership/completeness, malformed/conflicting row
identities, and generation. No read, tree, startup, cache, or revalidation
path mutates `FavoriteStore`.

## Files changed across the Task 2 correction

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

The second review correction changed only:

- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_favorite_revalidation_test.go`

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

The second review's High finding was technically valid. The previous
predicate treated any parsed ID absent from `remoteSources` as complete and
did not check exact membership in a complete source snapshot. The correction
derives immutable exact-ref remote ownership from `fetch.threads`; only
proven remote candidates require an existing complete owning source and an
exact canonical member in that source's `Threads`. Malformed, conflicting, or
ambiguous ownership is incomplete. Unobserved colon-bearing local metadata,
including legitimate `cluster:<id>` session IDs, remains complete without any
prefix heuristic.

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

### Second review RED evidence

The four focused endpoint regressions were added before the provenance
correction and run against the then-current production predicate:

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_(RemoteCandidate|LocalCandidate)' -count=1
```

```text
--- FAIL: TestAPITreeFavoriteRevalidation_RemoteCandidateWithoutOwningSourceSnapshotIsDormant (0.00s)
    web_api_tree_favorite_revalidation_test.go:662: remote candidate without owning source snapshot was pinned
--- FAIL: TestAPITreeFavoriteRevalidation_RemoteCandidateMissingExactSourceMembershipIsDormant (0.00s)
    web_api_tree_favorite_revalidation_test.go:697: remote candidate absent from exact source membership was pinned
FAIL
FAIL	primeradiant.com/serf/cmd/serf-hub	0.460s
```

The first attempted source-only fix also failed the existing no-prefix
compatibility regression, proving that parsed colon syntax cannot establish
remote provenance:

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_(RemoteCandidate|LocalCandidate|ClusterSpelling)' -count=5
```

```text
--- FAIL: TestAPITreeFavoriteRevalidation_ClusterSpellingDoesNotDecideValidity
    web_api_tree_favorite_revalidation_test.go:212: legitimate cluster-shaped session = ... Favorite:false ..., want favorite=true
FAIL
```

That intermediate production change was discarded in favor of request-owned
remote ownership derived from the global fetched thread rows. The retained
`ClusterSpellingDoesNotDecideValidity` test is GREEN with the final fix.

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
- Remote ownership is keyed by exact canonical ref, not by a source/ref prefix.
  Duplicate owners and malformed or source-conflicting rows mark that ref
  incomplete. Source quality then independently requires the owning source to
  be complete and to contain the same canonical ref in `source.Threads`.

## Exact GREEN evidence

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_(RemoteCandidate|LocalCandidate)' -count=5
ok  	primeradiant.com/serf/cmd/serf-hub	0.566s

go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_' -count=5
ok  	primeradiant.com/serf/cmd/serf-hub	1.647s

go test -race ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_ConcurrentCacheReadUsesOneSnapshot' -count=5
ok  	primeradiant.com/serf/cmd/serf-hub	1.914s

go test ./cmd/serf-hub -run 'Test(FavoriteEndpoint|APITree|Web_APITree|ListThreadsWithFallback)' -count=1
ok  	primeradiant.com/serf/cmd/serf-hub	1.538s

go test ./cmd/serf-hub/internal/hubcore -count=1
ok  	primeradiant.com/serf/cmd/serf-hub/internal/hubcore	0.707s

go test ./cmd/serf-hub -count=1
ok  	primeradiant.com/serf/cmd/serf-hub	25.546s

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
18b805ef7
```

This second-review report update is committed separately because
`.superpowers/` is ignored. After that commit, `git status --short --branch`
must report only the branch header:

```text
## wip/kata-favorite-cleanup-policy
```

## Fresh whole-branch review correction

The fresh whole-branch review rejected tip `7349c9ea3` with two Important
findings. The review was technically valid; both findings were reproduced with
deterministic tests before production changes.

### Finding 1: tree/cache generation split across mutation interleavings

Root cause A was that `memoTreeWithAuthority` read the navigation/archive
inputs and only then loaded `InputsVersion`. A mutation could rebuild and bump
between those operations, causing an old snapshot to be cached under the new
key. Root cause B was that the tree cache stored only Tree and AttentionSummary,
while live entries and FavoriteAuthority were returned from the current request
snapshot. Past/Roster publish their new data before their onChange callback
bumps InputsVersion, so an old cache hit could return an old tree with new
live/authority data. A version retry alone would not close that publish-before-
bump gap.

The RED command was:

```text
go test ./cmd/serf-hub -run 'Test(APITreeFavoriteRevalidation_MemoCapturesInputsVersionBeforeSnapshot|APITreeFavoriteRevalidation_MemoReturnsOneGenerationDuringPastRebuildGap|FavoriteProjectAuthorities_)' -count=1 -v
```

It failed on the intended contracts: the following request retained the old
tree (`old true new false`), the rebuild-gap authority contained the new row
alongside the cached old generation, the reversed malformed-row order was
classified valid, and empty-ID-only plus empty-ID-plus-valid-local projects
were classified valid instead of dormant.

The correction captures InputsVersion before navigation/archive reads and makes
TreeCache store/return one read-only TreeCacheValue containing Tree,
AttentionSummary, live entries, and FavoriteAuthority computed from one
navigationSnapshot. TreeCache still keys on InputsVersion, RemoteGeneration,
and the 30-second time bucket. Slice/map ownership was audited: each composite
value owns per-snapshot data and callers only read it.

### Finding 2: project/source claim aggregation was order-dependent

Root cause was last-row-wins aggregation in `favoriteProjectAuthorities`: a
complete row could overwrite incomplete or malformed evidence for the same
canonical project/source claim. Empty IDs were mapped to the local claim and
could therefore validate a project. The correction aggregates each claim
monotonically: at least one non-empty governed identity is required, every
contributing row must be complete, and incomplete or ambiguous evidence
dominates regardless of row order. Valid colon-bearing identities and
independent source claims remain supported; conflicting canonical paths remain
ambiguous. The implementation also initializes the first evidence row directly
before conservative merging, avoiding a zero-value quality downgrade.

### GREEN evidence and correction

Code and tests were corrected in commit `6958ff887`:

```text
go test -race ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_Memo(CapturesInputsVersionBeforeSnapshot|ReturnsOneGenerationDuringPastRebuildGap)' -count=20
ok   primeradiant.com/serf/cmd/serf-hub  1.573s

go test ./cmd/serf-hub -run 'TestFavoriteProjectAuthorities_' -count=20
ok   primeradiant.com/serf/cmd/serf-hub  0.688s

go test -race ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_ConcurrentCacheReadUsesOneSnapshot' -count=10
ok   primeradiant.com/serf/cmd/serf-hub  1.896s

go test ./cmd/serf-hub -run 'TreeFavorite' -count=5
ok   primeradiant.com/serf/cmd/serf-hub  0.644s

go test ./cmd/serf-hub/internal/hubcore -run '(Favorite|TreeCache|Remote)' -count=5
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.263s

go test ./cmd/serf-hub/internal/hubcore -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore  0.476s

go test ./cmd/serf-hub -count=1
ok   primeradiant.com/serf/cmd/serf-hub  26.538s

go vet ./cmd/serf-hub/...
exit 0, no output

golangci-lint run ./cmd/serf-hub/...
0 issues.

git diff --check
exit 0, no output
```

The blocking tests use bounded waits, `sync.Once` release functions, cleanup
restoration for the global snapshot hook, and cleanup joins for both the memo
goroutine and the blocked Past.Rebuild goroutine.
