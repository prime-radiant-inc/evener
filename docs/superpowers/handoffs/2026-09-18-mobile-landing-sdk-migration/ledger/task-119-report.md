# Task 119 — PR #1145 RoboRev round 23

Status: done. Three commits, main merged, pushed, PR body appended.

- `90bdb9cb3` ci: ask whether a recorded number is still the job's before signalling it
- `06851528d` ci: have the parent say what it just forked
- `6d365f570` ci: allow -workfile through to the enumeration, refuse -C
- Pushed head: `f0d12b199` (merge of `origin/main` @ `0acebbb0d`)

Record state table (now in `scripts/lib/process-group-lib.sh`'s header):
```
(no file)      no job is live for this caller
(empty)        file created, nothing spawned yet, or the spawn died before writing
pid:N:MARKER   N is a pid still in the caller's group — stopped by pid, never -N
pgid:N:MARKER  N is the job's own group, written only once setpgrp said so
survivor:N     took SIGTERM and SIGKILL and was still there; kept, not re-signalled
```
MARKER is the argv0 the job runs under: `go` for the gate,
`install-golangci-lint-attempt` for the installer.

Medium 1 (identity before signalling), with the old library and old runner paired
against a `ps` stand-in reporting the recorded group as a stranger's:
```
before  cleanup took 29s ; record: [pgid:90004] ; "could not be shown to have
        stopped; it still holds 99999(S)" — two graces spent on a stranger's group
after   cleanup took 2s ; record: [] ; nothing signalled
```
Medium 2 (fork/record window), child ignoring TERM and 8s slow to record:
```
before  records at signal time: 1 (empty)    → group survivors: 2 ; stray sleep 300: 1
after   records at signal time: 1 (pid:N:go) → group survivors: 0 ; stray sleep 300: 0
```
Low: `-workfile=/tmp/x.work → go list argv: list -workfile=/tmp/x.work ./...`;
`-C /tmp` exits 2 with a message saying to run the gate from the repo root.

Gates: `bash -n` on all three files before and after the merge; `MODULES=envvars`
PASS 0.40s, `MODULES=.` PASS 97.32s, `MODULES=agent` PASS 24.00s;
`make tools-golangci` real network; `make lint-generated` PASS.

Concerns:
- The parent-side write first shared the child's `.tmp` name, and the two writers
  renamed each other's file away — the gate run caught it as "No such file or
  directory" from inside the wrapper. Fixed (separate temp name) and folded into
  that commit; worth knowing that the record now has two writers at all.
- Trusting the marker means a group whose members do not match is left alone. In
  the fixture that deliberately lies, the real attempt survives — correct under
  the rule, and the reason the rule needs the marker to be right.
