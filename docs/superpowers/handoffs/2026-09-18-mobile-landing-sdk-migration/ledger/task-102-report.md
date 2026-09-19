# Task 102 — PR #1145 RoboRev round 15

Status: done. Two commits, main merged, pushed, PR body appended.

- `d7da61ce1` ci: stop an attempt that has not split into its own group yet
- `c6643215a` ci: keep a record whose attempt never named its group
- Pushed head: `fb580c159` (merge of `origin/main` @ `27503c07d`)

High — the runner's own pgid is read once at startup; the deadline path now has
three cases: the attempt's own group (as before), the runner's group (pre-split:
stopped by pid through a new `stop_package_list_pid`, TERM/grace/KILL, proved
gone by a zombie-aware `package_list_pid_is_running`, then reaped and classified
like any stopped attempt), and anything else (fail closed, record kept). Repro:
copy whose attempt waits 6s between recording and splitting, 2s budget:
```
before  stray go list after the run: 1 ; records left: 0 ; "its process group
        reads as 15896, not 16037, so it cannot be stopped as one."
after   stray go list after the run: 0 ; records left: 0 ; "go list ./... timed
        out after 2s on each of 1 attempts."
```

Low 2 — an empty record after the 5s wait is kept and reported instead of
dropped. Measured with a copy that plants the state before the waves run:
```
before  5s wait, record dropped, nothing said
after   5s wait, record kept: "never named its process group, so it cannot be
        shown to have stopped."
```

Low 1 (no coverage) declined with the standing ruling verbatim.

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.50s,
`MODULES=.` PASS 67.20s, `make lint-generated` PASS.

Concerns:
- I did not add the sidecar pid file the ruling offered for Low 2. An empty
  record means no name exists for the child; the parent's pid could supply one,
  but it may be recycled by then and signalling a recycled pid inside the
  runner's own group could kill a test process. Say the word if you want it.
- The pid stop waits a full grace twice before giving up, so a pre-split attempt
  that ignores both signals costs up to 10s on the deadline path, the same as a
  group that will not die.
- Round 13's installer-traps question is still open (task-97-report.md).
