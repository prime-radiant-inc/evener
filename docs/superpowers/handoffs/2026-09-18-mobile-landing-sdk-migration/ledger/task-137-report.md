# Task 137 — PR #1145 RoboRev round 31

Status: done. Two commits, main merged, pushed, PR body appended.

- `cdeadc098` ci: stop the recorded groups first, and bound the stream waits
- `c072356c9` fix(evener-dev): read -short the way go's flag package does
- Pushed head: `739616ee6`

Reproductions:
```
ROOT_FULL=1 -short=true          → test argv: test -count=1 -p 5 …          (dropped)
ROOT_FULL=1 -short=false         → test argv: test -short=false -count=1 …  (kept)
ROOT_FULL=1 -short=false -short  → test argv: test -count=1 -p 5 …          (last wins: true)
```
Item 1 has no reproduction: the pids the cleanup waits on are bash subshells,
which never ignore SIGTERM here, so I could not build an unresponsive stream.
Measured instead: cancellation completes in 1s with the enumeration group gone,
and the group is now stopped before any stream wait rather than after.

Gates: `bash -n` all three; envvars PASS 1.31s, `.` PASS 278.25s, agent PASS
16.28s; `go test ./cmd/evener-dev/...` ok; `make lint-generated` PASS.

Concerns: the boolean rule for `-short` is now encoded in both the shell and Go
(#1247). The stream stop now costs up to two graces per unresponsive stream on
cancellation, where the old code would have waited indefinitely — bounded, but
slower than the common case, which returns as soon as the stream dies.
