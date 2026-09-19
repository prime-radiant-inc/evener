# Task 94 — PR #1145 RoboRev round 12

Status: done. Three commits, main merged, pushed, PR body appended.

- `b72af3f71` ci: retry a stopped attempt, not one that failed on its own
- `a138e7a72` ci: stop reporting an unreadable group as an empty one
- `d19f05aa0` ci: keep the record of a group the cleanup could not empty
- Pushed head: `676ba6d27` (merge of `origin/main` @ `d89096907`)

Medium 1 — the reap's status now decides: 143/137 (this script's SIGTERM/SIGKILL)
is a timeout and retries; any other non-zero status is the attempt's own verdict
and goes out at once with its stderr. Covers the zombie the finding names and any
exit between the deadline check and the stop. Repro used a copy carrying `sleep 2`
before the group stop (only mutation, identical in both, deleted after):
```
before  EXIT=1; run-module-tests.sh: go list ./... timed out after 2s on each of 1 attempts.
after   EXIT=1; package-list-standin: cannot load ./broken: no such package
```
Genuine timeouts still retry: stalling stand-in, 2s budget, 2 attempts gives the
retry WARNING then "timed out after 2s on each of 2 attempts".

Low 1 — one helper renders survivors, keeping "<none at the final probe>" and
"<unknown: the process listing would not run>" distinct.

Low 2 — the cleanup keeps a record it could not empty and says so. Repro used a
`ps` stand-in whose full listing works once (the cleanup's own probe) then fails:
```
before  .pgid records left behind: 0 ; cleanup said: (nothing)
after   .pgid records left behind: 1 ; cleanup said: could not be shown to have
        stopped; it still holds <unknown: the process listing would not run>.
```

Medium 2 (no automated coverage) declined with the standing ruling verbatim.

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.68s,
`MODULES=.` PASS 170.66s.

Concerns: the Low 1 branch itself needs a SIGKILL-immune group, so it is proven
only through the shared helper that Low 2's reproduction exercises end to end.
The status rule treats any 137/143 as a stopped attempt, so a `go list` killed by
something else on the host (an OOM kill) is still retried as a timeout — the same
behavior as before this round, and arguably right.
