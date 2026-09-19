# Task 107 — PR #1145 RoboRev round 17

Status: done. Four commits (not five — see concerns), main merged, pushed, PR
body appended.

- `7fd120678` ci: put the whole deadline stop decision in one function
- `e24edb6da` ci: send the last signal even when nothing can be seen
- `30719e772` ci: say whether a record holds a pid or a group
- `c8ca5dcaa` ci: stop the install attempt the way the gate stops its own
- Pushed head: `5dad727f7` (merge of `origin/main` @ `cb211c5f8`)

Extraction: `stop_package_list_attempt` owns every case (own group, pre-split,
unreadable, post-stop re-probe, escalation) and answers 0/1/2/3; the caller only
frames the reason and decides about the record. Rounds 14, 15 and 16 scenarios
all re-run green against it.

Medium 1 (blind probe sent nothing): every blind answer escalates — TERM to group
and pid, the grace, then KILL — before the fail-closed status.
```
before  stalled attempt survivors after the run: 1 ; records left: 1
after   stalled attempt survivors after the run: 0 ; records left: 1
```
Medium 2 (pid recorded as pgid): records now read `pid:N` / `pgid:N`; a pid record
goes to the unified decision and is deleted only on "gone" or "stopped".
```
before  attempt group survivors: 2 ; stray sleep 300: 1
after   attempt group survivors: 0 ; stray sleep 300: 0
```
Mediums 3+4 (installer): shared rules moved to `scripts/lib/process-group-lib.sh`
(zombie-aware group probe, survivor report, group/pid stops with grace,
`pid_leads_pgroup`); the gate sources them, the installer's cleanup uses them —
group signal only while the kernel says the pid leads it, otherwise the child and
then the group it may have formed, bounded grace, survivors named, no unbounded
wait.
```
before  exit=143 after 3s ; scratch dirs left: 0
after   exit=143 after 8s ; scratch dirs left: 0   (the 8s is the grace)
pre-split signal (perl stand-in delaying setpgrp 2s): exit=143 after 9s, scratch gone
```

Gates: `bash -n` on both scripts and the new lib before and after the merge;
`MODULES=envvars` PASS 1.07s, `MODULES=.` PASS 194.73s, `make tools-golangci`
against the real network installed the pinned version, `make lint-generated` PASS.

Concerns:
- Installer Mediums 3 and 4 landed in one commit: they are the same six lines,
  and splitting them leaves an intermediate tree that hangs on a pre-split child
  (a blind group signal finds no members and the reap then blocks). Flagging the
  deviation rather than shipping a broken step.
- Neither installer finding's harm is producible here (uninterruptible sleep; a
  recycled pid mid-cleanup). What is measured is that the stop still works,
  including the pre-split case.
- The new library is sourced by two scripts and documented in itself; no doc page
  references it yet. Say the word if it should be listed in the tooling docs.
