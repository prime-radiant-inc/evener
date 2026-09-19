# Task 131 — PR #1231 round 4

**Status: DONE.** SHAs `0a02c1c8e` (no unfenced token), `97d6f9fea` (lookup failures surface),
`6e5279180` (comment split). **Pushed head `6e5279180`.** origin/main had not moved, so no new merge.
PR body has "## Review round 4" stating round 3's refused-once answer is withdrawn.

1. **Unfenced `(0,0)` token — withdrawn and replaced.** At budget exhaustion the depth-boundary
   delegate is truncated with no continuation and a diagnostic: `load budget exhausted; request
   session "<id>" directly`. **Field: `JobActivityDelegate.Diagnostics` (appwire/types.go:1865)** —
   already on the wire and already projected (`delegate.Diagnostics` is used for
   "delegate terminal metadata is invalid"), so no wire-shape change and no new TUI case. The old
   "refused once" comment and test expectation are gone.
   RED `TestProjectStableActivityDelegate_DepthBoundarySaysWhyWhenTheBudgetRanOut`:
   `0 delegates reported the exhaustion, want 3` → GREEN. The test folds every child first (so an
   unfenced token would genuinely be refused) and follows the diagnostic's own advice, asserting the
   direct request succeeds.
2. **Swallowed lookup failures — fixed.** `activityPlaceholderEpochs` returns the error from the
   delegate-index read. An unresolvable child link and a live child still answer zero, classified in
   place (`//nolint:nilerr` with the reason: the caller records that failure per child in
   `snapshot.Errors`) — golangci-lint's nilerr flagged it, and the classification is the honest answer
   rather than propagating a per-child condition into a whole-tree failure.
   RED `TestBuildActivityFullSnapshot_PlaceholderLookupFailuresSurface`:
   `err = <nil>, want the journal read's own failure` and `err = <nil>, want context.Canceled` → GREEN.
3. **Comment split — done.** `refresh` has its doc comment back above the func; `freshness` keeps its
   own starting with its name.

## Gates (on 6e5279180)
toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` 0 in both
modules; `go test -count=1 ./...` agent, appprojector, hub server and **cmd/evener-tui** — zero
non-ok lines; `-race -count=3 -timeout 30m` activity tests **`ok 758.761s`**; `-race -count=3`
foldcache (`ok 14.594s`) and delegates (`ok 224.636s`); `GOGC=50 golangci-lint run --concurrency=2
./...` `0 issues.`
