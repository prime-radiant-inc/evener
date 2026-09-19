# Task 84 — PR #1145 RoboRev round 10

Status: done. Three commits, main merged, pushed, PR body appended.

- `0e48e36fb` ci: ask for perl only where the bounded enumeration needs it
- `deeb9ca01` ci: have the package-list attempt record its own process group
- `4e9468e69` docs: say what the golangci-lint install actually retries
- Pushed head: `99865ad8e` (merge of `origin/main` @ `12c22f587`)

Medium 1 — perl check moved into `run_bounded_package_list`, before its first
spawn (single encoding of "who needs perl", same message):
```
before  MODULES=envvars, perl off PATH: EXIT=2, perl diagnostic, no tests run
after   MODULES=envvars, perl off PATH: EXIT=0, PASS  envvars  1.14s
after   MODULES=.,       perl off PATH: EXIT=1, FAIL  ., same diagnostic
```

Medium 2 — the perl wrapper now writes the pgid file itself between
`setpgrp(0,0)` and `exec`, so no window exists where the group is live and
unrecorded; the parent-side write is gone. Real window is sub-millisecond, so
both runs used a copy carrying `sleep 2` where the parent-side write used to be
(only mutation, identical in both, copies deleted after):
```
before  attempt group 86740 live; .pgid files at signal time: 0
        RESULT stalled go list survivors: 1 ; child 86744 survivors: 1
after   attempt group 86907 live; .pgid files at signal time: 1
        RESULT stalled go list survivors: 0 ; child 86911 survivors: 0
```

Medium 3 — declined, maintainer's standing ruling quoted in the PR body.
Low — linting.md now says "makes up to three attempts (two retries)".

Gates: `bash -n` clean on both scripts before and after the merge; `MODULES=.
WEB=0 scripts/gate/run-module-tests.sh -short -count=1` green (`PASS  . 91.83s`);
`make lint-generated` PASS.

Concerns: a missing perl with the root module scheduled now exits 1 with the
diagnostic in the failing module's output instead of exiting 2 at startup — the
trade for not refusing runs that never need perl. The child-side pgid write adds
one new failure mode (unwritable pgid path fails the attempt loudly), which is
better than an untracked group but is new behavior.
