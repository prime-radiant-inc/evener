# Task 111 — PR #1145 RoboRev round 19

Status: done. Four commits, main already current, pushed, PR body appended.

- `c57c984e9` ci: answer a blind probe at cleanup time the way the deadline does
- `2633abc93` ci: refuse to record a group setpgrp did not make
- `3cb6670b1` ci: give the installer's attempt a record of its own
- `fbb0c8e5d` ci: refuse the package-list knobs' old names instead of ignoring them
- Pushed head: `fbb0c8e5d` (`git merge --no-ff origin/main` already up to date at `f39aa2c83`)

Medium 1 — `escalate_blind` moved into the library; the gate's cleanup and the
installer's trap both signal blind and name the group and record path on an
unanswerable probe:
```
before  survivors of the recorded group: 1 ; records left: 1
after   survivors of the recorded group: 0 ; records left: 1
```
Medium 2 — `setpgrp(0, 0) or die` in both wrappers (verified empirically: this
perl returns 1 on success, 0 with errno on failure). Repro: perl stand-in that
makes the attempt a session leader, whose setpgrp really fails with EPERM:
```
before  no setpgrp mention; ran unisolated; reported a package-list timeout and
        advised a cache repair for a failure that was not one
after   FAIL . with "setpgrp: Operation not permitted", refused before exec
```
Medium 3 — the spawn program (`PGROUP_SPAWN_PERL`) and the record reader
(`pgroup_record_value`) moved to the library; both scripts spawn through it, the
installer creates its record before the fork, and its trap waits on the record
when it has no pid yet. Repro: copy with `sleep 2` between the fork and
`attempt_pid=$!`, SIGTERM to the script pid:
```
before  2s after SIGTERM: script alive=no, stalled fetches running=1
after   2s after SIGTERM: script alive=no, stalled fetches running=0
```
Low — old knob names now fail fast with the new names in the message (EXIT=2).

Gates: `bash -n` on both scripts and the library; `MODULES=envvars` PASS 0.40s,
`MODULES=.` PASS 60.12s, `make tools-golangci` against the real network, and
`make lint-generated` PASS.

Concerns:
- The gate and the installer now share the spawn program, the record reader, the
  probes, the stops and the blind escalation; what is still duplicated is the
  six-line "pid or group?" sequencing. If round 20 touches either copy I would
  move that into the library too.
- `escalate_blind` sleeps a full grace, so a cleanup facing a broken `ps` now
  costs 5s per record before it gives up. Bounded, and only on that path.
