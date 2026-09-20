# Task 122 (+122a) — PR #1145 RoboRev round 24

Status: done and pushed. Three commits, main merged.

- `4230e313d` ci: make the marker visible, and refuse to signal a group that is not a job's
- `67ae6700d` ci: let survivors decide a group, and the marker decide a pid
- `a3bb50ce9` ci: forward only build flags the toolchain has
- Pushed head: `5f9a11731` (merge of `origin/main` @ `31a5a4370`)

High 1 (marker invisible; installer probe + status-2 preservation):
```
before  pgroup_owned_by(live install attempt) = 1 ; pid_owned_by = 1
after   pgroup_owned_by(live install attempt) = 0 ; pid_owned_by = 0
after   pgroup_owned_by(same group, wrong marker) = 1
```
Library refusals, each pinned: `stop_pgroup(0)`, `(1)`, `(abc)`, `(<own pgid>)`
all refuse with the reason on stderr.

High 2 (ordering), on a group whose marked process exited with a child running:
```
marker asked first    → pgroup_owned_by = 1 ("not this job's") → record dropped
survivors asked first → members = [77906(S)] → stop_pgroup = 0, group empty
```
Low: `go help build` (go1.27.0) has no `-workfile` (`-work` is different);
forwarded set is `-tags -mod -modfile -overlay -pgo -trimpath`, verified
`-tags=fixturetag -workfile=… → go list argv: list -tags=fixturetag ./...`.

Hand-run on the real installer with a slow curl stand-in: attempt group had 4
members before the trap, 0 after, scratch removed. Gates: `bash -n` on all three
files before and after the merge; envvars PASS 0.42s, `.` PASS 61.26s, agent PASS
16.89s; `make tools-golangci` real network; `make lint-generated` PASS.

## 122a — fixture hygiene

Root cause of the killed watchers was mine, not the code: `kill -9 -- -<pgid>`
on four pgids scraped from a global `ps`, a `kill -9 $p $ppid` that hit a
`watch-pr.sh` bash, and repeated `pkill -9 -f '^sleep 60$'` colliding with the
watchers' own 60-second sleeps. The library/gate/installer signalled only groups
their wrappers created (verified in-trace). Fixtures now: own process group,
`pgid != caller` asserted before any signal, cleanup by that group id only, no
`pkill -f`, and stand-in durations chosen not to collide.

Concerns:
- Survivors-first deliberately trades round 23's recycled-group protection for
  never leaking a live attempt; what makes it safe is the structural refusals
  plus the record's short life, not the marker. Worth a maintainer eye.
- The marker now matches any word on the command line, so a group member whose
  arguments happen to contain `go` counts as ours. Inside a recorded group that
  is harmless; it would not be if the marker were ever used to widen a signal.
