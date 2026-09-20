# Task 129 — PR #1231 round 3

**Status: DONE.** SHAs `b768763d7` (depth-boundary truncation), `86b232bb8` (one delegate-generation
reading), merge `54c3d1427`. **Pushed head `54c3d1427`.** PR body has "## Review round 3".

1. **Depth-boundary delegate lost its continuation — confirmed, fixed.** With the load budget spent
   there is no placeholder, and projection dereferenced the child before deciding truncation, so the
   branch came back "child session unavailable" with nothing to continue to. `projectStableActivityDelegate`
   now decides truncation first and mints either way — the child's generations when they were named,
   none when they were not, which the resume refuses once and the client answers by restarting. The
   load-time comment describing the old ordering is corrected.
   RED `TestProjectStableActivityDelegate_DepthBoundaryTruncatesWithoutTheChild`:
   `delegate "dlg_boundarytruncchild1" reports "child session \"boundarytruncchild1\" unavailable" instead of a truncated branch` → GREEN, and the minted token resumes.
   Swept the other `snapshot.Children[` uses (jobs_activity.go:419, 480, 517): the loader's own
   dedupe check and its two writes. No other decision sits behind a dereference.
2. **Two readings of the delegate generation — confirmed, fixed.** The placeholder read the fold
   cache directly while the loader reads the traversal's delegate index; they disagree for a journal
   with a line too long to scan, where the loader degrades to an empty set at generation 0 while the
   cache still holds the last good fold's. The placeholder now reads that same index, and
   `currentHistoricalDelegatesEpoch` is gone rather than left as a second answer.
   RED `TestActivityPlaceholderEpochs_MatchesTheLoaderOnADegradedDelegateJournal`:
   `placeholder names generation 1 while the loader reports 0` → GREEN.

## Gates (on 54c3d1427, after the merge of origin/main)
toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` 0 in both
modules; `go test -count=1 ./...` agent, appprojector, hub server — zero non-ok lines;
`-race -count=3 -timeout 30m` activity tests **`ok 850.051s`**; `-race -count=3` foldcache
(`ok 14.505s`) and delegates (`ok 302.474s`); `GOGC=50 golangci-lint run --concurrency=2 ./...`
`0 issues.`
