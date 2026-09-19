# Task 105 — PR #1145 RoboRev round 16

Status: done. Two commits, main merged, pushed, PR body appended.

- `8dd08f823` ci: stop the group an attempt formed after it was signalled by pid
- `19ab74c84` ci: stop the install attempt the signal was meant for
- Pushed head: `4fcbc38aa` (merge of `origin/main` @ `99fa1882f`)

Medium 1 — after a confirmed pid stop the deadline path now probes the group the
pid names and group-stops it if one formed; the record goes only once both are
settled, and an unanswerable probe keeps it and fails closed. Repro: copy whose
attempt splits 3s after recording and which waits 4s between the group read and
the signal, 2s budget; stand-in `go list` leaves a child behind:
```
before  go list descendants after the run: 1 ; records left: 0
after   go list descendants after the run: 0 ; records left: 0
```

Medium 2 — each install attempt runs backgrounded in its own process group (perl
setpgrp wrapper, no `set -m`), is waited on, and the traps TERM/KILL the pid and
the group and reap before removing the scratch; pipefail moved into the attempt's
shell; perl named as a requirement. Repro: stalling curl stand-in, SIGTERM to the
script pid only:
```
before  2s after SIGTERM: script alive=yes, stalled fetches running=1; exit=143 after 121s
after   2s after SIGTERM: script alive=no,  stalled fetches running=0; exit=143 after 3s
```
This also closes the round-13 deferral concern I raised — a cancellation no
longer waits out the fetch, so that open question is now moot.

Gates: `bash -n` clean before and after the merge; `MODULES=envvars` PASS 0.70s,
`MODULES=.` PASS 180.73s (loaded host), `make lint-generated` PASS, and
`make tools-golangci` against the real network installed the pinned version in
2.7s.

Concerns:
- The installer now depends on perl, like the gate. It is present on macOS and
  the CI image; the script fails loudly with a named message if it is not.
- The gate's deadline path is getting long: three group cases, a pid stop, a
  post-stop group probe. It is still one function; if round 17 touches it again
  I would rather extract the stop decision than keep threading branches.
