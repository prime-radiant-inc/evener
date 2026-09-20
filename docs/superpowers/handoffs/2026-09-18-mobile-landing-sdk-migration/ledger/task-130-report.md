# Task 130 — PR #1145 RoboRev round 27

Status: done. Three commits, main already current, pushed, PR body appended.

- `f6fb4f2e3` ci: read the record's timestamp GNU-first, and refuse a group when own is unknown
- `ce81dead0` ci: scan the arguments themselves, and guard every path's package list
- `f5af84c39` ci: the Lows: -race=true, a checked rename, and a scratch that may go
- Pushed head: `f5af84c39` (merge already up to date at `eeff54b70`)

GNU-stat stand-in (prints an fs report on `-f`, the epoch on `-c %Y`):
```
GNU-stat stand-in → pgroup_file_mtime: [1700000000]
real stat        → pgroup_file_mtime: [1789281399]
missing file     → status 1
```
Fail-closed own group: `pgroup_signalable(99999)` with `ps` failing → 1, "this
caller own group could not be read...". `-C` scan reads `"$@"` with `set -f`.
Sharded agent with an empty enumeration → `FAIL agent — agent: go list ./...
returned no test packages`. `-race=true → go list argv: list -race=true ./...`.
`stop_attempt` now frees the scratch on status 1 only.

Gates: `bash -n` all three; envvars PASS 0.66s, `.` PASS 131.08s, agent PASS
87.01s; `make tools-golangci` real network plus the slow-curl hand-run (5 members
before, 0 after); `make lint-generated` PASS. Fixtures per 122a.

Concerns:
- The GNU path is still only simulated. Everything Linux-specific in this library
  (`stat -c`, `/proc` absence, `ps` field widths) rests on a stand-in and on the
  ordering another script in this repo already uses. The first real CI run on
  ubuntu is the only thing that will confirm it.
- `require_packages` is now called twice on the same list for the filtering
  branches (once on the raw list, once after the filter). Cheap, and it keeps one
  sentence, but it is two calls where a reader might expect one.
- Round 27 is the fourth round in a row whose findings are in code added by the
  three rounds before it. That pattern, not any single finding, is the argument
  for the real-process self-check now waiting on Jesse.
