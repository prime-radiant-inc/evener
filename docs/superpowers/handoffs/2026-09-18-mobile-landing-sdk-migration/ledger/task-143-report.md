# Task 143 — PR #1231 round 7

**Status: DONE.** SHAs `60ab615e1` (High: torn stat), `a58cbdd83` (Medium x2: one validation rule),
`4fa228e99` (Low: hook off the globals), merge `3436b1e78`. **Pushed head `3436b1e78`.** PR body has
"## Review round 7" stating round 6's absent-journal answer is corrected.

## Race failure rate, before and after
CI's `race-modules / agent` hit it roughly **one run in four**. Locally I could not reach the window
at all: with the pre-fix code and the sweep raised to 2000 rounds, **0 failures in 20 `-race` runs**
on macOS. So the regression test is deterministic rather than statistical —
`TestFreshnessOf_TornAppendStatKeepsTheGeneration` builds the exact torn observation (`os.Stat`'s
size from before an append with the mtime from after it) and asserts the generation holds.
**After: 0 failures**, foldcache green at `-race -count=10` (48.0s) and again at `-count=10` after
the merge.

## RED → GREEN
- `TestFreshnessOf_TornAppendStatKeepsTheGeneration`: `generation moved to 1 on a torn append stat,
  want it to stay at 0 -- the fold that follows reads the whole append and keeps the old one` → GREEN.
- `TestBuildActivityFullSnapshot_MissingBoundaryChildJournalReportsTheLoadersMessage`: `no error
  recorded for "missingjournalchild"; generation 0 and a continuation here hand the reader a page
  whose resume fails` → GREEN, asserting the loader's exact message.
- `TestCache_EpochSeparatesAMissingPathFromAnUnreadableOne`: `Epoch answered generation 0 for a
  never-folded path no fold could read` → GREEN.
- **Does any journal stay at generation 0 when absent?** Only one the loader tolerates missing, which
  is a request's own root (`required` false). Every child, including a depth-truncated one, is loaded
  with `required` set, so absence there is always the loader's failure.

## Fixture consequence worth knowing
Restamping an mtime no longer moves a generation — correct, since the probe answers for identical
content — so four fixtures that used `os.Chtimes` to force one now take the journal away, let a read
observe it gone, and put the same bytes back (`bumpFoldGeneration`). For a jobs journal that read has
to go straight at the fold cache: a session load stats it and skips a missing one.

## Gates (on 3436b1e78)
toolchain gofmt clean; `go vet ./...`, `-tags evenerfuzz`, `GOOS=windows -tags evenerfuzz` 0 in both
modules; `go test -count=1 ./...` agent, appprojector, hub server, cmd/evener-tui — zero non-ok
lines; `-race -count=3 -timeout 30m` activity tests **`ok 699.108s`**; `-race -count=10` foldcache
(`ok 48.030s`); `-race -count=3` delegates (`ok 215.645s`); `GOGC=50 golangci-lint run
--concurrency=2 ./...` `0 issues.`
