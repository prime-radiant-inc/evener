# Task 82 — PR #1145 RoboRev round 9

Status: done. Fix committed, main merged, branch pushed, PR body appended.

- Commit: `491613359` — `ci: stop a package-list group that outlived its leader`
- Pushed head: `cadfa4e5f` (merge of `origin/main` @ `9dcbcb4e6`)

Fix: `stop_recorded_package_list_groups` in `scripts/gate/run-module-tests.sh`
now asks `package_list_group_survivors "$recorded"` whether the group still has
live members and stops the group when it does, instead of skipping whenever the
leader pid is gone. Pid-reuse guard kept as "leader gone OR leader's pgid is
itself"; a probe that cannot run signals nothing.

Reproduction (stand-in `go list` spawns a `sleep 300` child then exits; runner
started in its own group and that group sent SIGTERM; both runs used a copy of
the script with the package-list poll interval widened 0.1s -> 5s, the only
mutation, identical in both, copies deleted after):

```
before  runner pid/pgid: 56057 / 56057; attempt pgid: 56100 (leader exited, child 56148 alive)
        runner still running? no
        RESULT go list leader survivors: 0 ; sleep 300 survivors: 1 ; ppid now: [1]
after   runner pid/pgid: 56214 / 56214; attempt pgid: 56260 (leader exited, child 56313 alive)
        runner still running? no
        RESULT go list leader survivors: 0 ; sleep 300 survivors: 0 ; ppid now: []
```

Round-8 shape (leader itself stalled) re-checked with the same harness: 0
survivors. Gates: `bash -n` clean on both scripts before and after the merge;
`MODULES=. WEB=0 scripts/gate/run-module-tests.sh -short -count=1` green
(`PASS  .  89.02s`). No automated test, per the maintainer ruling and
`docs/developing-evener/testing.md:145-155`.

Concerns: the widened-poll-interval copy is needed because the real window
between leader exit and the runner's reap is one 0.1s poll; the fix itself is
unmutated in both runs. `rm` is blocked in this environment, so the repro copies
were moved out of the repo rather than deleted; tree is clean.
