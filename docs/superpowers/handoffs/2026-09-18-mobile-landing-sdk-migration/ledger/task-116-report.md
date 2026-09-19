# Task 116 — depth-placeholder generations (PR #1179 lane)

**Status: BLOCKED on a decision — the fix is committed and green, but #1179 is already merged and
does not contain it (or Task 114's).** Commit `6f7230659` on `claude/activity-oversized-entry-1172`,
on top of `e1de98893`. **Not pushed; no PR body edit.**

## What changed under us
`gh pr view 1179`: state MERGED at 2026-09-13T03:20:08Z, squash `ce1c8ed01`, **headRefOid
`20cdd8a3b`** — the merge was taken from the branch state *before* my Task 114 push. main's
`agent/jobs_activity.go` still has `root.live == nil &&` at :359 and `collectActivityJobsEpochs`,
and none of round 8's tests. So main is missing both `e1de98893` (round-8 Medium: live root cannot
detect a closed child's journal being rewritten) and `6f7230659` (this task). Pushing to a merged
branch, reopening #1179, or opening a follow-up PR are all yours to call — I stopped here.
My `git merge --no-ff origin/main` conflicted in three files for exactly this reason (main carries
the squashed form of the same work); I aborted it and left the worktree clean.

## The fix (committed, 188 lines, no wire-shape change)
`foldcache.Cache.Epoch(path)` reports the generation the next fold would attach without folding:
the recorded counter, or 0 for a path never folded. `activityPlaceholderEpochs` uses it at
placeholder creation — a live child gets 0,0 (what `loadLiveActivityBase` reports), a historical one
gets its jobs journal's counter and the counter of the delegate journal its metadata names — and the
placeholder carries them, so `markActivityDelegateTruncated` mints what the resume compares against.
Cost: one `schema.LoadSessionMeta` read plus two map lookups per depth-boundary delegate; **no
journal fold**, which is what the depth bound exists to avoid.

## RED → GREEN
- `TestLoadSessionJobActivityTree_DepthContinuationResumesIntoAnUntouchedChild` (33-hop chain, shared
  delegate journal restamped so its generation is past zero): RED with the placeholder minting zeros
  — `depth continuation into an untouched child was rejected: activity continuation is stale: the underlying journal changed` — GREEN after.
- `TestLoadSessionJobActivityTree_DepthContinuationRefusedAfterTheChildIsRewritten`: the child is
  folded once, then its journal is restamped between mint and resume; the resume is refused. Passes,
  and guards the fix from becoming a rubber stamp.
- Live-root depth case is covered by construction (0,0 on both sides) rather than by fixture: a
  33-deep live chain is not buildable in a unit test.

## The two answers
- **A never-loaded session reports 0 from both caches**, and 0 is exactly what its first fold assigns:
  `foldcache` increments only when it discards a fold it already had, and a path with no recorded
  state has nothing to discard (`Get`'s refresh path skips the bump when `st == nil`).
- **Yes, the historical-root case was already broken on main**, independently of round 8: at
  `25a17a99b` the check was `root.live == nil && (mismatch)`, which for a historical root is the
  identical comparison, and the placeholder already minted 0,0. It bites as soon as the shared
  delegate journal has been rewritten once. My RED run is that exact configuration.

## Gates (on `6f7230659`, pre-merge-attempt tree)
Toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` all 0 in
the agent and root modules; `go test -count=1 ./...` zero non-ok lines in agent and appprojector;
`GOGC=50 golangci-lint run --concurrency=2 ./...` `0 issues.`; `-race -count=3 -timeout 30m` over the
activity tests was still running when I stopped — I will report it when it lands.
