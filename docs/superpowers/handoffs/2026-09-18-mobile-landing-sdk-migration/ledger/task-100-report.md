# Task 100 — PR #1145 RoboRev round 14

Status: done. Two commits, main merged, pushed, PR body appended.

- `9fa08b645` ci: fail closed when the attempt's group cannot be read
- `e1ee9e654` ci: ask whether the attempt is running before stopping it
- Pushed head: `aac8ae8c3` (merge of `origin/main` @ `2d3731615`)

Medium — the descendant-walk fallback and the leader-only kill are gone; an
unreadable group is now reported as not-shown-stopped and keeps its record, so
the EXIT cleanup stops the group by name. The record is dropped in exactly one
failure case: the confirmed-survivors one, where the diagnostic already names
them. Repro: `ps` stand-in failing the group lookup and the parent/child listing,
stalling `go list`, TERM-ignoring child:
```
before  stalled attempt survivors after the run: 1 ; records left: 0
after   stalled attempt survivors after the run: 0 ; records left: 0
```

Low — the deadline branch now probes the group's live members (zombies excluded)
before signalling: nothing running means the attempt finished on its own and its
own status and stderr are reported. Round 12's 143/137 test stays only for the
sliver between that probe and the stop.
```
self-inflicted 143 (before and after): reported as its own failure, no retry
genuine timeout (after): retry WARNING then "timed out after 2s on each of 2 attempts"
```

Medium 2 (no behavioral coverage) declined with the standing ruling verbatim.

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.70s,
`MODULES=.` PASS 85.16s, `make lint-generated` PASS.

Concerns:
- The Low has no producible before/after: reaching the classification with a
  finished attempt needs a held-open zombie. Evidence is that both promised
  outcomes hold, plus the probe's own logic.
- An unreadable group now leaves a live `go list` running until the EXIT cleanup
  stops it (seconds later, same run) instead of killing the leader immediately.
  That is the fail-closed trade the ruling asked for.
- Round 13's installer-traps question is still open (see task-97-report.md).
