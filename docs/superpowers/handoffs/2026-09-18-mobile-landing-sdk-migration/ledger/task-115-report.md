# Task 115 — PR #1145 RoboRev round 21

Status: done. Two commits, main merged, pushed, PR body appended.

- `aa3f30d3e` ci: keep the record of anything not shown to have stopped
- `929ac4a4e` docs: say the bound covers every module
- Pushed head: `f0a9b3c55` (merge of `origin/main` @ `e2c77cc72`)

Medium — one rule for both unconfirmed answers: the record is kept and the
reporting line says where. A group seen alive after SIGTERM and SIGKILL is
rewritten as `survivor:N`; the EXIT cleanup recognises that marking, keeps the
file and spends no further grace re-signalling it. Repro used a `ps` stand-in
that adds an immortal member to the attempt's group once it names itself (a
group outliving SIGKILL cannot be produced on a healthy host):
```
before  record after the run: []
after   record after the run: [survivor:75650]
        "... Not retrying, and not waiting on it. Its record is kept at <path>."
        and at exit "process group 75650 was still alive after SIGTERM and
        SIGKILL; its record is kept at <path>."
```

Low — the script header and testing.md said the bound covered "the root module's
and the agent module's subpackage list" and that perl was needed only for those
two; both now say every scheduled module enumerates through the bound and any run
that schedules a module needs perl. No "root and agent" remnants remain in either
file.

Gates: `bash -n` on both scripts and the library before and after the merge;
`MODULES=envvars` PASS 0.88s, `MODULES=agent` PASS 19.46s, `make lint-generated`
PASS.

Concerns:
- The record format now has three spellings (`pid:`, `pgid:`, `survivor:`) plus
  empty. That is still one file and one reader, but if a round 22 finding lands
  on the record I would write the state table into the header rather than add a
  fourth spelling.
- `survivor:` is only ever written by the attempt path; the cleanup never
  promotes a record to it, so a group that first proves unstoppable during the
  EXIT pass keeps its `pgid:` spelling and would be re-signalled by a later pass
  in the same run. There is no later pass today (cleanup runs once per trap), so
  nothing is wasted — flagging it as the assumption that makes it safe.
