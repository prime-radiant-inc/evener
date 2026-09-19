# Task 128 — PR #1100 round 15

Base `951f8fa24`; pushed head **`46f4dd547`** (three fixes + a merge of main).
PR body carries "## Review round 15". **turnIndexVersion did not move (18).**

| Commit | Item |
|---|---|
| `83185f4d6` | 1, one anchor answer gating every marker-dependent effect |
| `77fb6a798` | 2, full read vs indexed reader on a resuming owner |
| `fece8f69b` | 3, the cancellation gate reads the outcome, not the context |

## 1. Marker-dependent effects — REAL, fixed

It is a second publication site, not Task 120's guard reached late: Task 120
gated `handleCompactionTurnEffects` (the event and the naming), while the
fold's steering was gated on the *replay tail* (`anchor`) and the transcript
reminder on nothing at all.

RED, extending the round-14 failed-marker test with a pinned note so the fold
has steering of its own: `a fold whose marker write failed published 1 steering
event(s): … Kind:"note-handoff" …; the compaction they describe is not in the
transcript`, plus the reload assertion that no steering entry is in the file.
GREEN.

`markerLanded` is computed once, where the marker write returns — false when
the anchor was withheld and false when its own write failed, the same absence
reached two ways — and the compaction event, the session name, the fold's
steering (both its write and its announcement) and the reminder all read it.
`steeringWithheld` is gone, subsumed. A fold that writes no marker keeps
`markerLanded` true: nothing of its is missing.

## 2. Full read vs indexed reader — REAL, fixed

Reproduced in the incremental-vs-full table with the reviewer's combination
(`metadata for the running turn resumes it`): indexed keys ended
`…turn_active:2:0, turn_active:2:1` while the full read gave
`…turn_active:2:0, turn_active:3:0` — `[Y X Y]` against `[Y X Y Y]`.

The indexed answer is the right one: after a fragment for an earlier turn, the
running turn's own metadata opens a group that IS that turn resuming, so its
next record belongs there. The accumulator now adopts a fragment as the
continuation target when the fragment's owner is the target itself; the index
scan reaches the same answer by id and is untouched.

**No index bump**: nothing the scan records changed, so no cached sidecar is
stale, and the batching concern I raised last round does not come due.

## 3. The cancellation gate — REAL, fixed

RED: `finished under a cancelled context: name outlives the turn = true, want
false`. GREEN with `err != nil && isTurnCancellation(...)`.

An end-to-end test of the case cannot be built honestly: a turn that returns
nil with a cancelled context needs the cancel to land between the last round
and the unwind, and every way I could force the cancel from the public path
made the turn fail instead (`test setup: the turn failed (context canceled)`).
So the rule is a named function, `selfMintedNameOutlivesTurn`, driven directly
by a four-case table — falsifiable (it fails against the old gate) rather than
a race dressed up as a scenario.

## Gates (at `46f4dd547`, after the merge)

`$(go env GOROOT)/bin/gofmt -l` clean; host / `-tags evenerfuzz` /
`GOOS=windows -tags evenerfuzz` vet exit 0 in both modules; agent
`go test -count=1 ./...` zero non-`ok` lines; server, apptranscript,
appprojector, doctor and TUI suites clean; `-race -count=3` on the touched sets
(apptranscript 140.6s, agent 229.7s); golangci-lint `--concurrency=2 GOGC=50`
**0 issues** in both modules.

Overlap test: the first `-count=20` batch FAILED and I lost its output to a
`tail -2` — my error. Five further batches (100 runs) are clean, including four
run back to back afterwards. This test's only failure family on this branch and
on main is the `round_timings :8:2` vs `:8:3` interleaving of #1150, and the
failing batch ran immediately after two long race suites had saturated the
machine — but I did not see the text, so I am not claiming the shape.

## Concerns

1. The unidentified overlap failure above. If it matters before merge, the
   cheap answer is a capture-everything loop; #1150 remains the open mechanism
   either way.
2. `markerLanded` now decides four effects from one place, which is the
   improvement — but it is computed inside `commitTranscriptsLocked` and read
   in `flush`, so the two are coupled through a captured variable like the
   write-error slices beside it. A struct holding the transaction's outcome
   would say it better; not worth churn in a review round.
