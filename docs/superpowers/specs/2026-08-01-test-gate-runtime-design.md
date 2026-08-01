# Test-gate runtime reduction design

Date: 2026-08-01
Status: Implemented
Base: `webui-workspace-shell` at `96e1fc6ef`

## Problem

The post-merge gate runs `make lint`, `make build`, `make test`, and then
`go test ./...`. The root module therefore runs twice: once with `-short` in
`make test`'s protected first wave, then again without `-short` as the final
gate. In addition, all script self-tests finish before the protected root wave
starts even though they are mostly wait-bound and safe to overlap with the
second wave.

An idle-box measurement on the base commit produced:

| Component | Wall time |
| --- | ---: |
| `make lint` | 11.36s |
| `make build` | 5.66s |
| `make test` | 99.43s |
| `go test ./...` | 50.94s |
| Total | 167.39s |

The ledger's earlier approximately 90-second projection understated the agent
stream: its printed 10.67s verdict was only the final subpackage phase. The
preceding shard phase took 33.01s, so the agent stream's actual critical path
was about 43.68s.

The original success target was a total gate time at or below 100 seconds, a
reduction of at least 50% from the approximately 200-second directive-time
baseline. During the final measurement Jesse revised publication acceptance to
demonstrable improvement from the measured 167.39-second idle stack, with the
approximately 200-second directive baseline retained as secondary context.
Green, complete coverage still outranks the runtime target.

## Approved approach

Implement the two structural levers already identified in the fleet ledger:

1. Let the root module run its full suite in the existing protected first wave.
2. Start script self-tests only after that root wave, alongside wave two.
3. Once equivalence is proven, remove the now-duplicate standalone
   `go test ./...` from the operational post-merge gate.

The optimized gate command sequence becomes:

```text
make lint
make build
ROOT_FULL=1 make test
```

## Runner interface

### Full root suite

Add `ROOT_FULL`, defaulting to disabled, to `scripts/run-module-tests.sh`.
When `ROOT_FULL=1`, the runner removes the exact `-short` argument only from
the root module's `go test` invocation. All other flags and all non-root module
invocations are unchanged. Direct runner callers and ordinary `make test`
remain backward-compatible because the default stays disabled; the optimized
gate opts in explicitly.

The root module remains alone in wave one. No module or self-test work may be
moved into that wave. The existing concurrent frontend stream is unchanged.

### Self-test stream

Add `SELFTEST`, defaulting to disabled, to `scripts/run-module-tests.sh`.
When enabled, the runner starts `${MAKE:-make} selftest` after wave one has
fully joined and immediately before wave two starts. It joins the self-test
stream after wave two and reports one `PASS` or `FAIL` verdict with its wall
time, using the same captured-log behavior as the frontend stream.

Change the Makefile's `test` target from a serial `selftest` prerequisite to a
runner invocation with `SELFTEST=1`. The standalone `make selftest` target and
direct runner behavior remain available and unchanged.

If a scheduled Go module uses the reserved stream name `selftest` while the
self-test stream is enabled, the runner must refuse the ambiguous schedule
before starting any stream, just as it already does for the reserved `web`
stream name.

## Failure behavior

Every started stream still owes exactly one verdict. A failing self-test stream
must make the runner exit nonzero and replay its complete captured output.
Wave-one failure does not skip later coverage: the runner records it, then
continues to the self-test and wave-two streams before returning the aggregate
failure.

Timing output is diagnostic only. Pass or fail always comes from each command's
bare exit status, never from matching log text or reading pipeline status.

## Test-driven implementation

Extend `scripts/run-module-tests-selftest.sh` before changing the runner:

1. RED: with `ROOT_FULL=1`, require root's fake `go test` call to omit exact
   `-short`, retain `-count=1`, and require a non-root call to retain `-short`.
2. RED: with `SELFTEST=1`, use blocking fixture seams to prove self-tests do not
   start during wave one and do start while wave two is active.
3. RED: require a failing self-test stream to produce one `FAIL selftest`
   verdict, replay its diagnostic, and make the aggregate command fail.
4. RED: require a `selftest` module-name collision to fail before any stream
   starts.

Then implement only enough runner and Makefile behavior to make those contracts
green. The tests assert process ordering, arguments, verdicts, and failure
evidence rather than matching rendered shell source.

## Verification and rollout

1. Run the runner self-test directly.
2. Run `make selftest` to verify the aggregate tooling wave.
3. Run the legacy four-gate stack once, including standalone `go test ./...`,
   to prove the scheduler change did not lose coverage.
4. Run the optimized three-command stack on an otherwise idle box, capturing
   bare exits and per-command wall times.
5. Repeat the optimized measurement three times. Use the median total for the
   performance claim; all three cycles must be green and pristine.
6. Update `docs/testing.md`, the fleet ledger, and the controller gate helper
   only after the equivalence cycle and timing runs pass.

If the median does not improve on 167.39 seconds, do not weaken coverage or add
contention to wave one. Profile the new critical path and bring the next
structural change back for design approval.

## Measured result

The legacy equivalence cycle ran `make lint`, `make build`, `make test`, and
`go test ./...` serially. Their bare exits were `0`, `0`, `0`, and `0`, and all
four captured logs were pristine. This proves that the optimized stack retains
the root, non-root module, script self-test, and frontend coverage formerly
split across the four commands.

Three optimized cycles ran only after an idle-box check returned exit 1 with no
matching test process. All nine gate commands had bare exit 0 and pristine
captured output:

| Cycle | `make lint` | `make build` | `ROOT_FULL=1 make test` | Total |
| --- | ---: | ---: | ---: | ---: |
| 1 | 11.20s | 6.02s | 77.28s | 94.50s |
| 2 | 11.45s | 5.89s | 81.21s | 98.55s |
| 3 | 10.73s | 7.39s | 82.44s | 100.56s |

The median total is 98.55 seconds. That is 68.84 seconds, or 41.13%, below the
measured 167.39-second idle stack and 101.45 seconds, or 50.73%, below the
approximately 200-second directive baseline. The revised performance criterion
therefore passes without reducing coverage or changing the protected wave-one
contention policy.

The complete logs are under
`/private/tmp/claude-501/-Users-jesse-prime-radiant-toil-suite-serf--claude-worktrees-webui-workspace-shell/a25329dd-a50c-4efe-bd9b-e36a57c5e538/scratchpad/task-4-logs/`.

## Out of scope

- Widening any timeout or wall-clock budget.
- Moving work into the protected root wave.
- Removing tests, script self-tests, frontend checks, or module coverage.
- Changing agent sharding, Vitest pooling, or production code in this change.
- Treating timing text, grep output, or pipeline status as a gate verdict.
