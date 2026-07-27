# Task 2 implementation report

## Scope

Implemented only Task 2 of kata r0yf on `wip/kata/favorite-cleanup-policy`:
read-only favorite revalidation now uses the same raw navigation/source
generation as `/api/tree`, before presentation caps, with no favorite-store
mutation.

## Files changed

- `cmd/serf-hub/web_api_tree.go`
- `cmd/serf-hub/web_api_tree_favorite_revalidation_test.go`
- `cmd/serf-hub/web.go`
- `cmd/serf-hub/main_background.go`
- `cmd/serf-hub/internal/hubcore/tree.go`
- `cmd/serf-hub/internal/hubcore/remotecache.go`
- `cmd/serf-hub/internal/hubcore/remotecache_test.go`
- `cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`
- `cmd/serf-hub/cov_session_residue_pass5_fuzz_test.go`
- `cmd/serf-hub/cov_web_tree_session_fuzz_test.go`

The last two files only update coverage-driver calls for the explicit
error-returning favorite read helper.

## Exact RED evidence

Command:

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_' -count=1 -v
```

The test run correctly failed before production changes. The five missing
behaviors were:

```text
web_api_tree_favorite_revalidation_test.go:78: capped valid favorite missing from pinned favorites: []
web_api_tree_favorite_revalidation_test.go:143: last-known-good remote favorite remained visible after source failure
web_api_tree_favorite_revalidation_test.go:175: ambiguous local/ref authority appeared in pinned favorites
web_api_tree_favorite_revalidation_test.go:221: synthetic cluster row was presented as favorite
web_api_tree_favorite_revalidation_test.go:256: favorite read failure status=200 ... want 500
FAIL .../cmd/serf-hub
```

The ended/offline, confirmed-invalid preservation, archive, and one-source-
generation cases passed in the RED run because those contracts were already
partly satisfied by the baseline.

## Exact GREEN evidence

```text
go test ./cmd/serf-hub -run 'TestAPITreeFavoriteRevalidation_' -count=5
ok   primeradiant.com/serf/cmd/serf-hub 0.530s

go test ./cmd/serf-hub -run 'Test(FavoriteEndpoint|APITree|Web_APITree|ListThreadsWithFallback)' -count=1
ok   primeradiant.com/serf/cmd/serf-hub 0.707s

go test ./cmd/serf-hub/internal/hubcore -count=1
ok   primeradiant.com/serf/cmd/serf-hub/internal/hubcore 0.466s

go test ./cmd/serf-hub -count=1
ok   primeradiant.com/serf/cmd/serf-hub 26.265s

go vet ./cmd/serf-hub/...
exit 0, no output

golangci-lint run ./cmd/serf-hub/...
0 issues.

git diff --check
exit 0, no output
```

The remote-cache authority-generation test is registered in the hubcore
scenario driver and passes with the full hubcore suite.

## Design choices

- Navigation input collection records remote source completeness and a
  monotonically changing source generation beside the cached rows.
- Tree construction and favorite authority classification consume one raw
  snapshot; cache keys include the source generation so a last-known-good tree
  cannot be paired with a different authority generation.
- Stored decisions are classified through the accepted Task 1 pure authority
  seam. Only valid decisions reach presentation; dormant and confirmed-invalid
  rows remain untouched.
- Pinned candidates come from uncapped `Current`/`Recent` data retained by the
  tree builder, including sessions folded inside a presentation cluster.
  Archived projects and effectively archived live rows remain excluded.
- Current synthetic cluster kinds are supplied by the tree's uncapped node
  facts. Real session identity, including a legitimate `cluster:`-shaped ID,
  is not classified by string spelling.
- Favorite-store read errors propagate as tree endpoint errors; no empty map is
  substituted on the HTTP path.
- No read, tree, startup, cache, or revalidation path calls
  `FavoriteStore.Set` or `FavoriteStore.Delete`.

## Known limitations

- Remote source failure is conservatively applied to the remote IDs present in
  that last-known-good snapshot; those rows remain renderable but are dormant
  until a complete source snapshot succeeds.
- The existing `summary=1` response remains attention-only and does not read or
  project favorite decisions.
- No frontend surface or persistence schema was added, as required by the
  approved design.

## Commit

Production and test implementation commit:

```text
cb949ba63
```

## Clean-status evidence before report commit

Immediately before creating this ignored report, `git status --short --branch`
reported only:

```text
## wip/kata-favorite-cleanup-policy
```

The report itself is intentionally ignored under `.superpowers/` and will be
force-added as the separate report commit.
