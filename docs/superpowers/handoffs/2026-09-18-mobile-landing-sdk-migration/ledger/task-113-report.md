# Task 113 — PR #1145 RoboRev round 20

Status: done. Five commits, main merged, pushed, PR body appended.

- `c8afa6953` ci: drop the attempt's record the moment it is reaped
- `a428aa346` ci: keep the scratch when the attempt cannot be shown to have stopped
- `c0301d0c3` ci: bound every module's package discovery, not just two
- `52975c7da` ci: read the numeric knobs in base ten
- `63a43e871` ci: compare the pinned version literally, and keep the agent status
- Pushed head: `5c363f928` (merge of `origin/main` @ `303053dfb`)

High — record and variable both cleared at the reap, before the backoff:
```
before  record during the backoff: pgid:35671 ; process lookups by the trap: 2
after   record during the backoff: absent     ; process lookups by the trap: 0
```
Medium 1 — `stop_attempt` returns non-zero when unconfirmed; the scratch and its
record then stay (sticky, so the EXIT pass after a signal trap cannot undo it):
```
before  scratch dirs left: 0 ; after: 1, with "keeping <scratch>: it holds the
        record of an install attempt that could not be shown to have stopped."
```
Medium 2 — llm/auth/envvars/invariant/identifier now enumerate through the
bounded helper; one discovery path, no exemptions:
```
before  MODULES=envvars, stalling `go list` stand-in: exit=0 after 1s (stand-in
        never called — discovery was inside `go test ./...`)
after   exit=1 after 3s, "go list ./... timed out after 3s on each of 1 attempts."
```
All five pass normally: llm 6.99s, auth 1.32s, envvars 0.55s, invariant 0.37s,
identifier 1.23s.
Low 1 — `10#` normalisation: `EVENER_PACKAGE_LIST_TIMEOUT=08` passes and reports
"timed out after 8s" against a stalling stand-in (before: exit 2).
Low 2 — version compare moved to `[[ ]]`; agent branch returns `$?`.

Gates: `bash -n` on both scripts and the library before and after the merge;
`MODULES=envvars` PASS 0.53s, `MODULES=.` PASS 60.61s, `MODULES=agent` PASS
20.01s, `make tools-golangci` against the real network, `make lint-generated` PASS.

Concerns:
- Low 2's `case` hazard did not reproduce: bash matches a quoted expansion in a
  pattern literally, so a pin of `2.*` was already refused against `version
  2.13.1`. I made the change anyway for legibility and said so in the commit —
  flagging it because the finding was ruled real and my measurement disagrees.
- Bounding every module adds one `go list` per module to every gate run (~0.2-0.5s
  each here). Measured totals above are unchanged within noise, but it is new
  work on the critical path for the five small modules.
