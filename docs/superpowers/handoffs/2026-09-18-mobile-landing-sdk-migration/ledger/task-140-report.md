# Task 140 — PR #1231 CI failure + round 6

**Status: DONE.** SHAs `dbbc7edc9` (CI cause), `5afe4f511` (Medium), merge `49f4b136f`.
**Pushed head `49f4b136f`.** PR body has "## Review round 6" with the CI cause stated.

## The CI failure
Job `tests`, run 34773862425. **Failing test: `TestCache_EpochNeverExceedsTheNextFoldsGeneration`,
assertion at `foldcache_test.go:929`** — the **concurrent sweep**, not the deterministic hook half:
the message `Epoch named generation 3 while the fold that followed carried 2 -- a generation no fold
produces refuses a resume nothing invalidated` is the sweep's, while the hook half says `across an
interleaved fold`. (The 0.00s is elapsed time; it fails in the first sweep iterations.)
**Cause: my test, not the cache.** The sweep's writer used `os.WriteFile`, which truncates before it
writes, so `Epoch`'s stat could land inside the rewrite and see a file shorter than the completed
content the next fold reads — a shrink by the cache's own rules, so the name came back a generation
above the fold. Linux hits that window; twenty local runs with the rewrite restored did not
reproduce it on macOS. Fix: the sweep appends (the case the invariant is about) and its writer no
longer calls `t` from its goroutine; rewrites stay in the sequenced deterministic half. The
assertion is unchanged.

## The Medium
`Epoch` returns `(uint64, error)`: a path that is not there answers generation 0; every other stat
or tail-probe failure is returned. The placeholder path records that as the child's own failure —
no placeholder, no continuation, the branch carries the error — while failures belonging to the
traversal (the shared delegate index, cancellation) still fail the load, which Task 131's ruling
requires; the two are told apart by `activityChildJournalError` rather than by error kind.
RED `TestBuildActivityFullSnapshot_UnreadableBoundaryChildJournalReportsTheError`:
`no error recorded for "unreadableboundarychild"; a placeholder here mints a continuation whose resume dies on the same unreadable journal` → GREEN.
RED `TestCache_EpochSeparatesAMissingPathFromAnUnreadableOne`: `Epoch answered for a path it could
not stat; a generation named there is a fold promised and not kept` → GREEN, with the absent-journal
half asserting generation 0 and no error.

## Gates (on 49f4b136f)
toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` 0 in both
modules; `go test -count=1 ./...` agent, appprojector, hub server, cmd/evener-tui — zero non-ok
lines; `-race -count=3 -timeout 30m` activity tests **`ok 716.487s`**; `-race -count=3` foldcache
(`ok 14.607s`) and delegates (`ok 215.748s`); `GOGC=50 golangci-lint run --concurrency=2 ./...`
`0 issues.`
