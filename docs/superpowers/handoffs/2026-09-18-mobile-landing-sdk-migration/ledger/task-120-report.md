# Task 120 — PR #1100 round 13

Base `fe7e8102e`; pushed head **`9000bc08d`** (three fix commits plus
`git merge --no-ff origin/main` at `0acebbb0d`). PR body has a "## Review round
13" section.

| Commit | Finding |
|---|---|
| `e489e5da4` | 1, late fragment captures the continuation |
| `15beb088b` | 2, marker-dependent effects after an unpoisoned write failure |
| `ff9fe20b9` | 3, replay copies inflate aggregate totals |
| — | 4, `ToolDefs`: refuted, no code change |

## 1. Late owned fragment — REAL, fixed

Built the reviewer's sequence as a case in the grouping table (active turn, a
late owned `CONTEXT_COMPACTION` fragment, then an unowned assistant). RED:
`logical turn IDs=[turn_active turn_next turn_active], want [turn_active
turn_next turn_active turn_next]` — the assistant landed on the fragment.

The accumulator and the index scan now track the continuation target apart from
the last-opened group, through one shared predicate `startsFragmentGroup`. A
fragment names its own turn and leaves the flow alone; the next continuation
resumes the running turn in a group of its own. The index reconstructs the
target from an indexed prefix (`continuationGroupState`) the way it already
reconstructs the open group, and `turnIndexVersion` goes 16 → 17 because a
cached sidecar holds the old grouping.

A delayed steering **carrier** is deliberately excluded from the fragment rule:
`TestItemReadersHonorSteeringOwner` pins that a carrier owns what follows it, so
the rule is "owned metadata that does not continue a turn". That exclusion was
found by the existing test, not by argument — the first attempt broke it.

## 2. Unpoisoned marker write failure — REAL, fixed

Established first that an unpoisoned error path exists:
`poisonLandedBytesLocked` (agent/transcript/transcript.go:541-559) explicitly
leaves the writer usable when the write transferred no bytes, and
`appendFailureLocked` (:607-616) returns the error without poisoning whenever
the rollback succeeds. So a marker write can fail with nothing to stop the fold.

RED (new `failMarkerWriteFS` failing only the marker line, asserting
`writer.Poisoned()` is false so the test is on the unpoisoned arm): `a fold
whose marker write failed published 2 compaction-turn event(s)`. GREEN with one
guard at the top of `handleCompactionTurnEffects`, which covers both the fold
flush and the `OnCompactionTurn` fallback.

## 3. Replay copies in aggregate totals — REAL, fixed

RED: `usage total = &{InputTokens:200 OutputTokens:20 TotalTokens:220}, want the
round counted once &{100 10 110}` (plus the failure counted twice). One shared
`countsTowardTotals` predicate, consulted by the usage, derived-totals and
failed-tool scans; each subset struct decodes `context_replay`. Copies are still
scanned so a call one announces resolves a later result's name — the new test
writes the result without a name to hold that.

**No TUI or live-projection case is needed here**: replay copies are
transcript-only records. Nothing in `internal/appprojector` or
`cmd/evener-tui` reads `ContextReplay` because no event is ever emitted for a
copy, and the item projections already drop them (`appendProjectedEntry`
returns early; the index records them as adding no items). The overcount was
confined to the three file scans.

## 4. `RoundTimings.ToolDefs` — REFUTED

`allToolDefinitions` is `return s.cachedToolDefs`
(`agent/session_tools.go:1209-1211`) — the definitions are built once at init
(`rebuildToolDefsCache`), not per round. So no tool-definition construction
happens inside the system-prompt window and no time is misattributed to
`SystemPrompt`. The sub-claim "ToolDefs is never assigned" is true, and the
phase measures a field read, so any assignment is structurally zero: I wrote the
measurement, saw round 1 report `0s` because there is no work to measure, and
reverted it rather than land an unfalsifiable `> 0` assertion. The residual
worth filing is the opposite of the finding: a reported phase that names no
work.

## Gates (at `9000bc08d`, after the merge)

`$(go env GOROOT)/bin/gofmt -l` clean; host / `-tags evenerfuzz` /
`GOOS=windows -tags evenerfuzz` vet exit 0 in both modules; agent
`go test -count=1 ./...` zero non-`ok` lines; server, apptranscript,
appprojector, doctor and TUI suites clean; `-race -count=3` on the touched sets
(apptranscript 83.9s, agent 195.3s); golangci-lint 2.13.1 `--concurrency=2
GOGC=50` **0 issues** in both modules; overlap test `-count=20` ok (50.0s).
`internal/selfupdate`'s six `/private/var` failures are #1201, not this branch.

## Concerns

1. The index version bump means every cached sidecar rebuilds on first read
   after this lands — correct, and worth knowing for a large deployment.
2. The steering-carrier exclusion keeps cold grouping at odds with the live
   projector, which never lets an owned announcement take the flow. That is the
   #1152 family; this change narrows it (metadata now behaves the same in both)
   without closing it for carriers.
