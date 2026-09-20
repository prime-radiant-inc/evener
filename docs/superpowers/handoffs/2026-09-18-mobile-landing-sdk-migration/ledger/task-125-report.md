# Task 125 — PR #1100 round 14

Base `9000bc08d`; pushed head **`951f8fa24`** (three fix commits + a merge of
main at `d7ff88653`). PR body carries "## Review round 14" with the #1200
deferral stated and linked.

| Commit | Items |
|---|---|
| `2c43213f0` | 2 + 3, self-minted turn name lifetime and the interrupt marker |
| `0048bcd96` | 4, fold steering announced after a failed write |
| `e6778c358` | 5, a late owned timing record is a fragment |
| — | 1, #1200: no code, per the standing ruling |

## 1. #1200 — not in this PR

No code, no test. The PR body states the deferral and links the issue. I did
not add a failing or skipped restart test.

## 2 + 3. The name a turn mints for itself — REAL, fixed

Probed first: after `ProcessInput` the field is clear (the loop drops it), but
after a direct `processOneInput` it still holds
`turn_direct_01M2CKXB…` — the leak is that the *loop* owns the clear, not the
turn. RED (a/b/c): `after the turn ended, "turn_direct_…" still owns everything
published next`. RED (d): `the interrupt marker entry is owned by "", want the
turn it interrupted "turn_direct_…"`. GREEN for both.

`endSelfMintedTurn` is the one place the name ends; `processOneInput`'s unwind
calls it on every path out, including a return between the mint and the run.
The single exception is an interrupted turn, whose last record — the marker
ProcessInput's loop appends after the turn returns — belongs to the turn it
interrupted; that path ends the name after writing it. Case (c), a turn whose
input could not be written, passed before and after and is kept as a pin.

## 4. Steering announced after a failed write — REAL, fixed

RED: `steering events after a failed write = 1, want 0`. GREEN with `continue`
after the warning.

**What happens to the model's copy:** it keeps the steering. The fold appended
these records to live history in `runPreCompactHook`, before publication, so
the text reaches the next model request whatever the transcript did. The
divergence is therefore real and now narrower: the warning reports it, a reload
resolves it by not replaying the line, and no client is told about a record no
reader can get back. Removing it from the published history instead would mean
unwinding a fold that has already committed.

## 5. Late owned `ROUND_TIMINGS` — REAL, fixed

RED: `logical turn IDs=[turn_active turn_next turn_active], want [turn_active
turn_next turn_active turn_next]`. The fragment rule now excludes only
`carriesTheFlow` — steering — instead of every continuation kind, so timings
join checkpoints, summaries, compaction records and hook completions as
metadata about work that is over.

**It does change cached sidecars**, so `turnIndexVersion` goes 17 → 18: the
classification moves the `StartsGroup`/`TurnID` a scan records for the
continuation that follows such a record. (v17 exists only on this branch, never
in a release, so the bump costs nothing beyond a rebuild.)

One wrinkle worth recording: the table's timing fixture had no measurements, so
its record projected no item and its group rendered as nothing, hiding the
boundary. The fixture now carries a payload — a case that cannot show what it
claims is worse than no case.

## Gates (at `951f8fa24`, after the merge)

`$(go env GOROOT)/bin/gofmt -l` clean; host / `-tags evenerfuzz` /
`GOOS=windows -tags evenerfuzz` vet exit 0 in both modules; agent
`go test -count=1 ./...` zero non-`ok` lines; server, apptranscript,
appprojector, doctor and TUI suites clean; `-race -count=3` on the touched sets
(apptranscript 84.8s, agent 220.0s); golangci-lint `--concurrency=2 GOGC=50`
**0 issues** in both modules; overlap test `-count=20` ok (70.0s).
`internal/selfupdate`'s six `/private/var` failures remain #1201.

## Concerns

1. Two index-version bumps in two rounds (16 → 17 → 18). Both are honest, and
   the cost is one rebuild, but a third would be worth batching.
2. The interrupt marker now carries the interrupted turn's owner only when that
   turn named itself. A turn named by a client mutation has its durable name
   released in the same unwind, so its marker is still unowned — the same rule
   applied there needs the release to move too, which is a wider change than
   this item asked for.
