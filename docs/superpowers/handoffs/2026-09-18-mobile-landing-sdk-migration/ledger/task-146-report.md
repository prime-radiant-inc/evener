# Task 146 — reset the activity-continuation work to the simpler design

PR **#1269** — `fix(agent): fence continuations with the target session's generations, and page depth-boundary children directly`
Branch `claude/activity-continuation-generations`, head `f206969c0`, base `aefc56a48`. Not a draft. Supersedes #1231.

Worktree: `/Users/jesse/git/prime-radiant-inc/evener/.claude/worktrees/activity-generations`.

## Three commits

1. `6ddb2777d fix(agent): mint a continuation with the generations of the session it names`
   `collectActivitySessionEpochs` returns `activitySessionEpochs{jobs, delegates}` per session; every trim mint
   carries the trimmed session's own pair; the `root.live == nil &&` exemption on the stale check is deleted, so a
   closed child under a live root is fenced by both generations and the revision.
2. `8462274fb fix(agent): report the session to request at the depth bound, not a token`
   The depth-boundary delegate is decided before the child dereference: `Branch.Truncated = true`, no continuation,
   and `Diagnostics` gets `depth limit reached; request session "<id>" directly`. The whole non-folding generation
   predictor is deleted (see below). The loader's depth branch is a plain `continue` with no placeholder.
3. `f206969c0 fix(agent): ask the tail probe before calling a same-size file rewritten`
   `foldcache.refresh` treats a same-size stat as ambiguous (os.Stat does not snapshot size and mtime together) and
   asks `tailProbeMatches` before moving the generation. Real `Get`-path fix; this was the CI failure's root cause.

## Deleted, not ported

`foldcache.Cache.Epoch` + its test hook, `freshnessOf`, `epochInterleave`, `activityPlaceholderEpochs`,
`currentHistoricalJobsEpoch`, `currentHistoricalDelegatesEpoch`, `activityChildJobJournalReadable`,
`activityChildJournalError`, the placeholder generation fields and their work-unit charge, and
`markActivityDelegateTruncated`. Verified absent by grep on the branch.

Tests deleted: `TestMarkActivityDelegateTruncated_EmbedsEpochsInContinuation`,
`TestLoadSessionJobActivityTree_DepthBoundaryContinuationIsSubmittable`, plus #1231's predictor tests that never
reached main (`TestActivityPlaceholderEpochs_*`, `TestBuildActivityFullSnapshot_DepthPlaceholders*`,
`TestBuildActivityFullSnapshot_*BoundaryChild*`, `TestCache_Epoch*`).
Rewritten to the new shape: `TestProjectStableActivityDelegate_DepthTruncated`,
`TestLoadSessionJobActivityTree_BoundsRecursionDepth` (which now also requests the named child directly and asserts
it gets the page the bound withheld).
New: `TestCache_TornAppendStatKeepsTheGeneration` (torn `os.FileInfo`: old size, newer mtime).

## Size

Branch vs main, agent module only: **379 insertions / 318 deletions across 5 files**.
#1231 at `3436b1e78` vs main: 1537 insertions / 232 deletions across 6 files.

## Gates (all green)

- Toolchain gofmt (`$(go env GOROOT)/bin/gofmt -l`) clean on all five touched files.
- `go vet` host, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` — both modules, 0.
- `go test -count=1 ./...` for agent, appprojector, `cmd/evener-hub`, `cmd/evener-tui` — zero non-ok lines.
- `-race -count=3 -timeout 30m` over the activity tests: focused `ok 260.405s`, broad agent package `ok 730.653s`.
- `-race -count=10` over foldcache: `ok 45.500s`.
- `GOGC=50 golangci-lint run --concurrency=2 ./...`: `0 issues.`

One failure appeared in a re-run of the agent suite executed concurrently with the race gate
(`TestSession_ProjectDocsCachedAtInit`, context deadline exceeded at 7s). It passes `-count=3` alone; it is machine
load, unrelated to this change.
