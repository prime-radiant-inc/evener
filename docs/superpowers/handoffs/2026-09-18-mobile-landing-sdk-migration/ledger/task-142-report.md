# Task 142 — PR #1145 RoboRev round 33

Status: done. Four commits, main merged, pushed, PR body appended.

- `c3d0c86f0` ci: signal nothing when nothing can be read, and keep an unconfirmed record
- `39de576ba` ci: let the self-check's trap see what the self-check spawned
- `87b5c7572` fix(evener-dev): -p is the build's parallelism
- Pushed head: `8044fa225` (merge of `origin/main` @ `04889ba31`)

Reproductions:
```
1  before  unreadable listing → status 2, record kept, job alive: no
   after   unreadable listing → status 2, record kept, job alive: yes
2  unconfirmed → status 1, record kept, "pid N could not be shown to have
              stopped, so its record is kept at <path>."   (stop replaced by
              hand: no real process survives SIGKILL)
   confirmed   → status 0, record removed
3  deliberate assertion failure → selfcheck exits 1; sleep 30 processes before
   the failing run: 0, after: 0 (the trap stopped what it spawned)
4  -p 4 → [-p 4] ; --p=6 → [-p=6] ; -parallel 8 -race → [-race]
```
`escalate_blind` is deleted outright rather than narrowed: with it gone there is
no path that signals a number nothing could verify, which is what row 9 of the
state table says. The header carries that sentence now.

Gates: root audits ok; `bash -n` on all four scripts; envvars PASS 0.68s, `.`
PASS 221.55s, agent PASS 32.36s; `go test ./cmd/evener-dev/...` ok; `make lint`
green end to end (lint-process-group 7s); `make lint-generated` PASS.

Concerns: I lost my uncommitted self-check edits once this round by running
`git checkout` on the file to undo a deliberate mutation — re-applied, and worth
naming: use a copy for mutation runs, never checkout over live work.
