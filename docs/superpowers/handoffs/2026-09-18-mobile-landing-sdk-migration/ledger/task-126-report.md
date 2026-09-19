# Task 126 — PR #1145 RoboRev round 26

Status: one of two Mediums done and pushed; the other needs a ruling (facts below).

- `42262a2d1` ci: enumerate under the sanitiser flags the tests build with
- Pushed head: `a4c330c75` (merge of `origin/main` @ `d7ff88653`)

Medium 2 (done): `-race`, `-msan`, `-asan` each set a build tag, so they join the
forwarded set.
```
-race -short -count=1 → go list argv: list -race ./...
-short -count=1       → go list argv: list ./...
```

Medium 1 (not done — platform facts, established by hand on this machine):
```
EVENER_JOB_MARKER=zz-marker-999 /bin/sleep 25 &
/bin/ps -E -ww -p <pid> -o pid,command  → "sleep 25"  (no environment shown)
/bin/ps eww -p <pid>                    → "sleep 25"  (no environment shown)
man ps documents -E as "Display the environment as well"; macOS shows it for no
same-user process here. /proc does not exist on macOS.
ps -o sess= -p <any pid>                → 0 for every process
```
So on macOS a foreign same-user process exposes pid, ppid, pgid, start time and
command line — not its environment, not a usable session id. None of those can
separate an orphaned member of our group (its marker-carrying parent has exited)
from a stranger in a reused group. Linux `/proc/<pid>/environ` is the documented
mechanism and readable same-user, but I could not verify it here (no Linux, Docker
daemon down) and will not claim it untested.

The stated fallback ("keep the record, signal nothing") would leave every
timed-out `go list` running with the cache locks — the failure this branch exists
to remove — so I left the code as it is and asked rather than shipping either
extreme. My recommendation is in the reply: keep survivors-first, delete or narrow
the now-dead probes, and correct the header to claim only what is enforced.

Also of note: `pgroup_signalable`'s session check is a no-op on macOS, since
`ps -o sess=` prints 0 for everything. The pgid-0/own-group/non-number refusals
still work.

Gates: `bash -n` all three before and after the merge; envvars PASS 0.72s, `.`
PASS 180.09s, agent PASS 34.10s; `make tools-golangci` real network plus the
slow-curl hand-run (5 members before the trap, 0 after); `make lint-generated`
PASS. Fixtures kept to the 122a rules.
