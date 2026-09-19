# Task 121 — PR #1231 round 1 (2 Medium, 2 Low)

**Status: DONE.** SHAs `63300a877` (budget), `7f68c014b` (Epoch/Get), `624bfe04b` (docs), merge
`f94c310ea` of origin/main. **Pushed head `f94c310ea`.** PR body has "## Review round 1".

1. **Unbounded I/O at the depth boundary — fixed.** The placeholder lookup now checks `cache.ctx`
   and pays one `activityConsumeWorkUnit`, the same unit a loaded child pays; an exhausted budget
   leaves the child out entirely. RED `TestBuildActivityFullSnapshot_DepthPlaceholdersChargeTheWorkBudget`:
   `minted 6 placeholders on a budget of 2 units` → GREEN, plus
   `..._StopsOnCancellation` asserting `context.Canceled` surfaces.
2. **Legacy V1 tokens — declined**, in the PR body and in the stale-check comment: a continuation is
   a client-held page cursor, the error already names the recovery, and a shim for tokens minted by a
   build that no longer runs is backward compatibility Jesse has not approved.
3. **`Epoch` vs `Get` after a delete — fixed by changing `Epoch`.** `Get` returns the zero Result for
   a missing file (foldcache.go:216), so 0 is what a load-path reader derives; the tombstone is only
   meaningful for a path that comes back, which is exactly what
   `TestCache_DeletedFileEpochSurvivesAcrossRecreation` pins (it asserts the *recreated* generation
   advances and says nothing about the missing-file answer). So `Epoch` stats first and reports 0 for
   a path that is gone. RED `TestCache_EpochAgreesWithGetAcrossDeletionAndRecreation`:
   `Epoch = 1 for a deleted path while Get reports 0` → GREEN. The fold-delete-mint-resume walk the
   ruling sketched cannot exist at the activity level: a continuation target whose jobs.jsonl is gone
   fails earlier with "child session unavailable" (required=true), so the pair is pinned at the
   foldcache seam instead.
4. **Docs — done.** `DelegatesEpoch` names the target session's journals (not `RootID`'s) and states
   the live-root/closed-child fencing; `Revision` says the same from its side; `collectActivitySessionEpochs`
   and the trim's parameter list are no longer stale; `foldcache.Epoch` has its own comment and
   `Stats` has its doc comment back (my earlier insert had orphaned it).

## Gates (on f94c310ea, after the merge)
toolchain gofmt clean on six touched files; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags
evenerfuzz` 0 in both modules; `go test -count=1 ./...` agent, appprojector and hub server with zero
non-ok lines; `-race -count=3 -timeout 30m` activity tests **`ok 747.008s`**, plus `-race -count=3`
over `internal/foldcache` (`ok 14.570s`); `GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.`
