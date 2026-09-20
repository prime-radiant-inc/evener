# Task 150 — PR #1269 review round 2

Worktree `activity-generations`, branch `claude/activity-continuation-generations`. `origin/main` merged first
(`62c5d1d00`).

## Commits

| SHA | Item | Message |
| --- | --- | --- |
| `62c5d1d00` | — | `Merge origin/main into claude/activity-continuation-generations` |
| `7bc4335c7` | 1 (High) | `fix(agent): record the size the fold consumed, not the size a torn stat reported` |
| `5c2cbee8b` | 3 (Medium) | `fix(agent): report the session when a page cannot name a path back to it` |
| `f034b8910` | 4 (Low) | `fix(agent): ask the probe again after the second stat reports growth` |
| `d659acc8c` | — | `fix(agent): write the recorded-size clamp as max` (golangci-lint modernize) |

Item 2 declined per the coordinator's ruling; no behaviour change, frontend affordance is #1270.

## Item 1 — High

RED before the fix (`TestCache_TornStatAppendRecordsTheSizeItActuallyFolded`):
```
value = {sum:1725 lines:50}, want the rewritten content read in full (sum 3839)
 -- a fold resumed past the end of the new file reads none of it
```
The torn stat published `size` from before the append while the fold consumed the whole larger file, leaving
`offset > size` — impossible for an honest file. The next stat read as growth, the growth path trusted the
surviving tail, resumed at an offset the file had already reached, read nothing, and kept the generation.

Fix: publish `max(stat size, consumed offset)`. Deliberately not a post-fold re-stat: a file that grows *after* the
stat (rather than during it) must keep recording the smaller stat size, or the next look reads an unchanged length
and serves a hit over unread bytes. `max` fixes only the direction that was wrong.

## Item 3 — Medium, measured first as instructed

`TestLoadSessionJobActivityTree_ResumedPageMintsADecodablePath` — 35-session chain, resume two hops down, trim
forced at the deepest session. **The finding reproduces:**
```
the page minted a token its own decoder refuses: continuation path length 34 exceeds 33
```
Cause confirmed in the code: `buildActivityContinuationAt` makes the continuation target projection's own depth 0
regardless of how many hops led there, so the depth budget is depth-relative, while both mint sites build `Path`
absolutely from the tree root through the filtered ancestor chain. A page resumed `len(Path)` hops down reaches
`len(Path)+activityMaxNewDepth`.

Fix: `activityContinuationPathFits` is checked at both mint sites — `mintActivityTrimContinuation` (size trim) and
`markActivitySessionTruncated` (mid-list cutoff). When the path will not fit, the session stays `Truncated`, mints
nothing, and carries `continuation path limit reached; request session "<id>" directly`. Constant unchanged; its
comment now states that the bound is depth-relative while the minted path is absolute.
`TestMarkActivitySessionTruncated_ReportsTheSessionWhenThePathCannotBeNamed` covers the second mint site directly
(at the limit it still mints and the token still decodes; one hop past it reports the session).

## Item 4 — Low

`TestCache_RewriteInsideTheSecondStatWindowBumpsTheGeneration`, RED without the re-probe:
```
generation = 0, want 1 -- the file grew, but the prefix this cache folded is gone
```
The probe runs before the second stat, so a rewrite that also appends lands between them: growth alone then reads
as the completed append a torn first stat implies. The probe is asked again once the length is known.

The ordering is injected through a new `stat` field on `Cache` (defaulted to `os.Stat` in `New`), because nothing
runs between the probe and the stat that a fixture could otherwise drive. This is the only production seam added.

## Wire shape

Unchanged. `JobActivitySession.Diagnostics` already existed and is already projected; `git diff` over `appwire/`,
`internal/appprojector/` and `cmd/` is empty for this round. The TUI suite was run anyway and is clean.

## Gates

Toolchain gofmt clean; `go vet` host + `-tags evenerfuzz` + `GOOS=windows -tags evenerfuzz` on the agent module, 0;
`golangci-lint` `0 issues.`; `cmd/evener-tui` clean; `go test -count=1 ./...` in agent clean;
`go test -race -count=3 ./internal/foldcache/... .` in agent — see the note below.

**Timeout note for future briefs:** `-race -count=3` over the whole agent package needs far more than a 30m
`-timeout`. The first attempt died with `panic: test timed out after 30m0s`; that was the cap, not a hang — the
test named as running (`TestLoadSessionJobActivityTree_StopsRecursingOnceWorkBudgetExhausted`) completes in ~95s
under `-race` together with the new deep-chain test, and the new test costs 1.01s under `-race` on its own. Re-run
at `-timeout 150m`.
