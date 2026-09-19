# Task 97 — PR #1145 RoboRev round 13

Status: done. Two commits, main merged, pushed, PR body appended.

- `bde30084e` ci: have the record exist before the attempt does
- `71c542320` ci: clean the installer's scratch on a signal, not only on exit
- Pushed head: `5a11f853d` (merge of `origin/main` @ `9db3a1e99`)

Medium — smaller shape than the two-part one suggested: the parent creates the
`.pgid` file empty before the fork and the child fills in the pid before it
splits, so an empty record means "an attempt is spawning" and the cleanup waits
(the stop's grace) for the pid instead of finding nothing to stop. No parent-side
blocking wait was needed. Repro: SIGTERM to the runner's pid (descendant-walk
path), copy whose attempt takes 2s to record and ignores TERM until then:
```
before  records at signal time: 0 ; attempt group survivors: 2 ; stray sleep 300: 1
after   records at signal time: 1 ; attempt group survivors: 0 ; stray sleep 300: 0
```

Low — HUP/INT/TERM traps added beside the EXIT trap. The leak did NOT reproduce
here: bash 3.2 runs the EXIT trap on a fatal signal, so the scratch was already
removed in both versions:
```
before  group SIGTERM: exit=143, 1s, scratch left 0 ; pid SIGTERM: 1s, 0
after   group SIGTERM: exit=143, 1s, scratch left 0 ; pid SIGTERM: 8s, 0
```

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.57s,
`MODULES=.` PASS 66.21s, `make lint-generated` PASS.

Concerns for the coordinator:
- The Low's premise is unproven on CI's bash 5: I could not test it (the Docker
  daemon is not running here) and bash's documented behavior does not settle
  whether EXIT traps run on a fatal signal. The traps make cleanup independent of
  that, at the cost of bash deferring a trapped signal until the foreground
  command returns — 8s in the repro, up to curl's 60s in reality, but only for a
  signal aimed at the script alone; a group-signalled cancellation is unchanged.
  Say the word and I will revert that commit.
- The cleanup's wait for an empty record can add up to 5s to a cancelled run in
  the mid-spawn case only.
