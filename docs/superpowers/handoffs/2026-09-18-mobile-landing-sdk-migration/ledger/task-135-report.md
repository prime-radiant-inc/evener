# Task 135 — PR #1231 round 5

**Status: DONE.** SHAs `14733e889` (Epoch ordering), `aee52167b` (unresolvable child), plus
`50041bb6b` (lint follow-up: `WaitGroup.Go`, landed after the merge rather than amended across it)
and merge `1abd1e4f3`. **Pushed head `50041bb6b`.** PR body has "## Review round 5".

1. **`Epoch` stat-before-state — confirmed, fixed.** The state is read under the lock first; a fold
   landing between the readings can then only leave the state older than the file, the case already
   answered for. RED `TestCache_EpochNeverExceedsTheNextFoldsGeneration`:
   `Epoch named generation 2 across an interleaved fold while the next fold carried 1` → GREEN.
   The deterministic half uses a nil-by-default hook (`epochInterleave`) that runs between the two
   readings — the plain concurrent sweep could not reproduce the window — and the test also runs a
   200-round sweep against a goroutine folding and rewriting, kept under `-race`.
2. **Unresolvable depth-boundary child — confirmed, fixed.** Resolution moved to the loader beside
   the non-placeholder path; failures are recorded in `snapshot.Errors` and reported by projection's
   existing check, with no placeholder and no continuation. `activityPlaceholderEpochs` now takes the
   resolved locator, so zero generations mean only a live child, and the `//nolint:nilerr` that the
   old swallowing needed is gone. RED
   `TestBuildActivityFullSnapshot_UnresolvableDepthBoundaryChildReportsTheError`:
   `no error recorded for "unresolvablechild"; a placeholder here mints a continuation that fails on resume for this same reason` → GREEN.

## Gates (on 50041bb6b)
toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` 0 in both
modules; `go test -count=1 ./...` agent, appprojector, hub server, cmd/evener-tui — zero non-ok
lines; `-race -count=3 -timeout 30m` activity tests **`ok 790.193s`**; `-race -count=3` foldcache
(`ok 14.926s`, re-run after the lint fix: `ok 1.323s`) and delegates (`ok 260.478s`);
`GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.`
