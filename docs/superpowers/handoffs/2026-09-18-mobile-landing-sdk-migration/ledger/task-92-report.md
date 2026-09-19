# Task 92 — PR #1145 RoboRev round 11

Status: done. Four commits, main merged, pushed, PR body appended.

- `762b29075` ci: record the attempt's group before it splits into one
- `8ef9fd2c7` ci: drop the group record when the attempt gives up on it
- `568cdde70` ci: keep a group record until the cleanup has acted on it
- `ec5769129` docs: bound what the installer actually bounds, and say when perl is needed
- Pushed head: `2ee54f5ef` (merge of `origin/main` @ `cc9298832`)

Medium 1 — the perl wrapper writes the marker (tmp file + rename) before
`setpgrp(0,0)`. Before the split a signal reaches the child through the runner's
own group; after it, the record already exists. Widened-window repro, `sleep 2`
in one spawn window per run (only mutation, copies deleted after):
```
before      attempt wrapper 72320; .pgid files at signal time: 0
            RESULT attempt group survivors: 2 ; stray sleep 300: 1
after-pre   attempt wrapper 72470; .pgid files at signal time: 1
            RESULT attempt group survivors: 0 ; stray sleep 300: 0
after-post  attempt wrapper 72677; .pgid files at signal time: 1
            RESULT attempt group survivors: 0 ; stray sleep 300: 0
```

Low 1 (fail-fast returns clear the marker) and Low 2 (cleanup keeps a record it
could not act on), measured with a stalling stand-in and a `ps` that will not
run:
```
Low 1  before fail-fast: probes after the attempt gave up: 1 ; after: 0
Low 2  before signal:    records left behind: 0, probes: 1 ; after: 1 record, 2 probes
```

Medium 2 (no automated coverage) declined with the standing ruling verbatim.
Lows 3/4: linting.md now bounds only the installer fetches plus backoff;
testing.md says perl is required when a bounded enumeration runs.

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.69s
and `MODULES=.` PASS 89.84s; `make lint-generated` PASS.

Concerns: the SIGKILL-immune "group will not die" case that Low 1's 10s cost
describes cannot be produced on a real host, so that commit's evidence is the
cleanup's probe count, not a stopwatch. Keeping a record when the probe fails
(Low 2) means a run whose `ps` is broken now carries the marker into the EXIT
pass — intended, and it is bounded by that one extra pass.
