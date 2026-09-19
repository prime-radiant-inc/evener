# Task 1265 round 1 — wiring fixes on PR #1265

Pushed head: `78bd1ae49` (branch `claude/ci-bounded-list-and-retry`, worktree `ci-bounded-list`).

| Finding | Commit | What changed |
| --- | --- | --- |
| 1 High, `go run` outside the bound (#1267) | `70a01b916` | `make tools` builds `evener-dev`; `test`/`test-race` take `build-dev` as a prerequisite; the gate execs the binary or stops with the make target to run. The agent shard runner is the same binary now, which restores its 129/130/143 exits. #1267 closed with the commit. |
| 2 High, `go list -C` placement | `b5adff0a9` | Not reproducible on go1.27.0: `go list -C agent ./...` and `go -C agent list ./...` both print the same 42 packages, which is why the agent gate run passed. `-C` is gone regardless — the enumeration runs in the module's own directory, which `run_wave` had already entered. Nothing was masking anything, so nothing had to be removed. |
| 3 High, unbounded `go test ./...` | `b5adff0a9` | One enumeration path: every module enumerates through the helper and is handed the list. |
| 4 Medium, survivors after the reap | `67345f6eb` | Every attempt ends by probing the group and stopping what it finds (TERM, grace, KILL). Safe after the reap precisely while the group is non-empty: a pid cannot be reused while it is still a live group's id. New test: a leader that exits at once while its child traps TERM with the pipe closed; mutation-proved (without the sweep the group is still there 3s later). |
| 5 Medium, renamed knobs | `2f3fe7318` | `EVENER_ROOT_PACKAGE_LIST_TIMEOUT`/`_ATTEMPTS` stop the gate with a message naming their replacement, exit 2. No fallback — that is backward compatibility and Jesse has not been asked. |
| 6 Low, `-grace` and stale `--help` | `67345f6eb` | `-grace` validated with `-timeout`/`-attempts`; both help surfaces read one sorted subcommand list (`dev.SubcommandNames`). |

Gates, all green: root audits (`go test -short -count=1 .`), `go test ./cmd/evener-dev/...`, `go vet -tags evenerfuzz ./...` and the `GOOS=windows` repeat, `make lint` (8 modules), `make lint-generated`, `bash -n` on both scripts, gate runs on `.` (66s), `envvars`, `agent` sharded (58s) and `AGENT_SHARDS=0` (437s), and `make tools` then `make tools-golangci` against the real network (built evener-dev, installed golangci-lint v2.13.1).

## The Metro gate's port (same round, follow-up commits)

| Item | Commit | What landed |
| --- | --- | --- |
| 1 Blocker, timed-out stdout dropped | `b0531c890` | Output is written on every outcome, after the diagnostic. Test: a child that prints then sleeps past the bound; mutation-proved (without the write, stdout is empty). |
| 2 Regression, no signal forwarding | `b0531c890` | TERM/INT/HUP go to the group, then the grace, then SIGKILL; reaped; exit 128+signal; never retried. Tests: a TERM-ignoring child interrupted mid-run leaves an empty group and exit 143, and a SIGINT run exits 130 with no "retrying" line. Mutation-proved (with the channel dropped the run retries and ends 124). |
| 3 Distinct timeout status | `b0531c890` | 124, coreutils' convention, documented in the new usage text alongside the other statuses. `a9b37ded7` has the gate print cache-repair advice for 124 alone. |
| 4 Cosmetic | `67345f6eb`, `b0531c890` | `bounded-list` is in the top-level usage (round-1 commit); the diagnostic reads "on 1 attempt" / "on each of N attempts". |

Not done, as asked: no streaming to a file, no `-stdout` flag.

Gates re-run after the follow-up commits: `go test ./cmd/evener-dev/...`, root audits, both vets, `make lint`, `make lint-generated`, `bash -n`, `make build-dev`, gate runs on `.`, `envvars`, and `agent` both sharded (18s) and `AGENT_SHARDS=0` (338s, throttled to `-p 1 -parallel 1` by the load-aware budget on a busy host), plus a hand run of the binary showing the 124 status, the singular diagnostic, and the pass-through of a timed-out command's output.

Residual: the compile of `evener-dev` still reads the caches, now under `make`, once per invocation. A host whose caches are stalled fails there instead of in the gate, with make's own output rather than a bound diagnostic.
