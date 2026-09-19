# Task 133 — PR #1100 round 16

Base `46f4dd547`; pushed head **`7130dbeae`** (three fixes + a merge of main at
`42d27f946`, shared session notes #1070). PR body carries "## Review round 16"
including the conflict list. **turnIndexVersion moved: 18 → 19.**

| Commit | Item |
|---|---|
| `c86e94de6` | 1 (and 4), per-marker landing |
| `9db0f91f3` | 2, the resumed walk past a closed group |
| `c73b4b06c` | 3, the doctor's tool-round boundary |

## 1 + 4. Per-marker landing — REAL, fixed

RED: `compaction-turn events = [], want the summary's alone: the checkpoint is
not in the transcript and the summary is` (new `failCheckpointWriteFS` fails
only the CHECKPOINT line, unpoisoned; the summary lands). GREEN, with the
summary's event published, the session named once, the fold's steering
published and one steering entry in the reloaded transcript.

`compactionTurnLanded []bool` replaces both the fold-wide `markerLanded` and
the write-only `compactionTurnWithheld`: each record's effects follow its own
result. The separate question the fold's steering and reminder need — does a
reader coming back find an anchor at all — is `foldAnchored`, true when any
marker landed (the last one to land is what `ResumeHistory` stops at) and true
for a fold that writes no marker.

**Item 4's disposition: used, not deleted.** The flag becomes that per-record
result, which is the role it was missing; nothing named `compactionTurnWithheld`
or `markerLanded` remains.

## 2. The resumed walk — REAL, fixed

RED, with the reviewer's sequence added to the incremental-vs-full table:
indexed `…boundary:1:0, turn_other:2:0, turn_active:3:0` against full
`…boundary:1:0, turn_other:2:0, turn_other:2:1` — a duplicate `turn_active`
with different keys, exactly as described. GREEN.

`continuationGroupState` now returns "" on a record that left no group open,
asked before the kind is inspected: a persisted `TurnID` is never empty, so an
unowned record of an owned kind read as a fragment and was walked past.

**The index version moved to 19**: a sidecar written by the old walk holds
names this walk would not produce. That is the second bump in three rounds, the
batching cost I flagged last round; it is stated in the PR body.

## 3. The doctor's tool-round boundary — REAL, fixed

RED: `exit 1: evener-doctor reconstruct: archived tool result at ordinal 6
crosses a tool-round boundary` (fixture: a call, then a ROUND_TIMINGS and a
CONTEXT_COMPACTION record, then the result). GREEN, with the result present in
the reconstruction.

## Merge with #1070

Two conflicts, both resolved by keeping both intents:
`agent/session_lifecycle.go` (main's notes projection at turn start + this
branch's preseeded-turn naming) and `agent/session_namer.go` (main's
`resetNotesProjectionAfterCompaction` + this branch's `OwningTurnID` on the
compaction-turn event).

## Gates (at `7130dbeae`, after the merge)

`$(go env GOROOT)/bin/gofmt -l` clean; host / `-tags evenerfuzz` /
`GOOS=windows -tags evenerfuzz` vet exit 0 in both modules; agent
`go test -count=1 ./...` zero non-`ok` lines; server, apptranscript,
appprojector, **doctor** and TUI suites clean; `-race -count=3` on the touched
sets (apptranscript 98.9s, doctor 10.7s, agent 217.9s); golangci-lint
`--concurrency=2 GOGC=50` **0 issues** both modules; overlap test `-count=20` ok
(73.2s), and clean again in two earlier captured batches.

## Concerns

1. Before the merge, one full agent run failed
   `TestInitInside_SymlinkSpelledCwdCanonicalizesStoredPathAndLockKey` with the
   `/private/var` vs `/var` mismatch — the same macOS canonicalization family
   as #1201, in a test this branch does not touch. Three full agent runs after
   it were clean, as was the post-merge run. Worth attaching to #1201 rather
   than treating as new.
2. Three index-version bumps now live on this branch (17, 18, 19). Only the
   final number ships, so the cost is one rebuild, but the count is a symptom:
   grouping rules are still being corrected one reviewer case at a time.
