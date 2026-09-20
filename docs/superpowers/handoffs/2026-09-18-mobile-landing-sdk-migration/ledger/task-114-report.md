# Task 114 — PR #1179 round 8 (one Medium)

**Status:** DONE. Commit `299c6d441`, replayed as **`e1de98893`** onto the branch head the coordinator
had pushed (20cdd8a3b) — my local merge of origin/main collided with theirs, so I dropped my
redundant merge commit and cherry-picked the fix rather than merging two merges. **Pushed head:
`e1de98893`.** PR body has a "## Review round 8" section.

## The defect and the fix
A mint took the delegates generation from the PAGE ROOT's snapshot. A live root has none, so a token
naming a closed child carried zeros, and `loadActivitySnapshotForParamsWithCache` had to skip
generation validation for live roots to keep that branch readable. What that gave up was the only
evidence of the child's journal being rewritten: the root's clock moves for what the daemon does
under it, never for a file rewritten out of band.

`collectActivityJobsEpochs` becomes `collectActivitySessionEpochs`, returning
`activitySessionEpochs{jobs, delegates}` per session; `trimActivityTreeToFit`,
`trimActivityTrailingEntry` and `mintActivityTrimContinuation` take that one map in place of the
`delegatesEpoch uint64, jobsEpochs map[string]uint64` pair, and each mint carries the trimmed
session's own pair. Depth truncation mints `child.DelegatesEpoch` instead of the page root's. The
`root.live == nil &&` exemption is deleted, and the comment block now states the rule that remains.
~25 lines of code plus comments; no wire-shape change.

## RED → GREEN
- `TestJobActivityTree_LiveRootChildContinuationRejectedAfterTheChildIsRewritten` (live root, closed
  child, child's jobs.jsonl restamped between mint and resume):
  RED `resumed into a closed child whose journal was rewritten; want the continuation refused` → GREEN.
- `TestJobActivityTree_LiveRootChildContinuationSurvivesHistoricalEpochs` (round 5's test, kept as the
  "readable at all" case): an untouched closed child still resumes under a live root. Its hand-minted
  token now carries the child's own delegates generation, matching what the production mint emits;
  the assertion (accepted) is unchanged.
- `TestTrimActivityTrailingEntry_MintsTheTrimmedSessionsOwnDelegatesEpoch` (new) and
  `TestCollectActivitySessionEpochs` (renamed, now covering both generations) pin the rule directly.

## Gates
`$(go env GOROOT)/bin/gofmt -l` clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags
evenerfuzz` all 0 in the agent and root modules; `go test -count=1 ./...` zero non-ok lines in agent
and appprojector, re-run after the merge; `-race -count=3` over the activity tests
`ok primeradiant.com/evener/agent 669.645s` (the first attempt hit go test's default 10m timeout —
not an assertion failure — so it was re-run with `-timeout 30m`); `GOGC=50 golangci-lint run
--concurrency=2 ./...` `0 issues.`

## Concerns
- Depth-limit placeholders still mint zeros for both generations (jobs_activity.go:454-461 -> :1118):
  the placeholder never loads the child, and with the exemption gone a live root's depth-truncated
  continuation into a closed child can now be refused where it previously was not. Same #1195 family,
  still not a local read.
- Task 101's open question stands: in the corner where a page has less slack than the advanced token
  costs, the response goes over the cap rather than looping.
