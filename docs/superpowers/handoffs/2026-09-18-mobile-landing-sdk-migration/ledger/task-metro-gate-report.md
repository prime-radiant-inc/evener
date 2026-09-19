# Task: Metro bundle gate (#1244)

Status: DONE, review round 1 addressed. PR #1245, branch `claude/native-metro-gate-1244`, head `4c78b34a0` (two commits, no trailers), base `origin/main` `eeff54b70`.

## Command chosen
`npx expo export --platform ios --output-dir <scratch> --clear`, run from `mobile-native/` under a private `HOME`/`TMPDIR`.

Why: it is the only bundling entry point this toolchain supports offline with no device, simulator, or running packager, and it reads the real `mobile-native/metro.config.js`. `react-native bundle` would need a second entry-point declaration to keep in sync. `--clear` + private temp root means the verdict is never inherited from a warm Metro cache. iOS only (the shipping platform; no `.ios.`/`.android.` source files in the app tree). Default minify+Hermes kept: measured *faster* than `--no-minify --no-bytecode` (7.7s vs 9.8s warm) and additionally proves Hermes compiles the bundle.

The gate tests resolution, not a path, so it is unaffected by #1241 (A3) moving `@evener/appwire-client` to `appwire-client/typescript/`.

## Cost
~9s cold (1581 modules) on a developer Mac. `make test-native` goes ~13s -> ~22s. `EVENER_NATIVE_BUNDLE_TIMEOUT` (default 600s) is a tripwire bound, ~65x the real cost. Bundler runs in its own process group (`set -m`) so the bound kills Metro's worker pool without reading a global process list.

## RED evidence
Unresolvable import appended to `mobile-native/App.tsx` (reverted): `make test-native-bundle` exits non-zero with
`Error: Unable to resolve module @evener/appwire-client/definitely-not-a-real-entry from .../mobile-native/App.tsx` plus the import stack, then `full log: <path>`.
GREEN with it reverted: `PASS  native-bundle (9s, 1581 modules)`.
Also exercised: `--verbose`, unknown arg (exit 2), non-integer timeout (exit 2), `EVENER_NATIVE_BUNDLE_TIMEOUT=2` timeout path (no orphaned bundler).

## Files
- `scripts/native/test-native-bundle.sh` (new, scratch-lib minted, quiet on pass, full log + retained dir on fail, `--verbose`)
- `make/testing.mk`: `test-native-bundle` target with the `##` annotation block; `test-native: test-native-bundle`
- `.github/workflows/ci.yml`: native step renamed only — no new job needed, `make test-native` already runs in CI and in `ios-testflight.yml`'s preflight
- `docs/developing-evener/testing.md`: new "The Native Gate Bundles the App" section + regenerated target table

## Gates
`make test-native` pass; `make lint` all nine pass incl. `lint-generated`; Makefile audits pass (`TestEveryTargetHasASummaryAnnotation`, `TestEveryRuleIsPhony`, `TestEveryGeneratedRegionIsInTheStalenessDiff`, `TestNoScriptFeedsVariableToRecursiveDelete`, etc.); `go test -short -count=1 .` pass. Biome never run over `mobile/` or `mobile-native/`.

## Concerns
1. CI cost is unmeasured on ubuntu-latest — 9s on an M-series Mac could be 60-90s there. If the native job's wall time matters, revisit `--clear` (it is what makes the run cold) or drop Hermes with `--no-bytecode`.
2. A failed run retains its scratch dir under TMPDIR, matching `scripts/web/test-web.sh`'s convention; repeated local failures accumulate directories.
3. `expo export` writes `mobile-native/.expo/` (gitignored). No working-tree dirt observed.

## Review round 1 (both findings real, fixed at 4c78b34a0)
1. Medium — exit paths orphaned the bundler group. One `stop_bundle` now owns every path (`finish`, a new `interrupted` trap handler, the timeout): clears `bundle_pid` before signalling so a reaped pid is never signalled, guards with `kill -0 -- -$pid`, signals only the group the script created.
2. Low — unbounded post-TERM wait. `EVENER_NATIVE_BUNDLE_STOP_GRACE` (default 5s) then `SIGKILL`.

Tests in `native_bundle_gate_interrupt_unix_test.go`, on the `startChild`/`waitForPathOrExit`/`syncBuffer` harness: `TestNativeBundleInterruptStopsBundlerProcessGroup` (TERM/INT/HUP), `TestNativeBundleTimeoutEscalatesPastIgnoredTerm`, `TestNativeBundleDoesNotSignalReapedBundler`. All three proven RED against the matching mutation and GREEN reverted. Fixtures run in their own process group and only ever ask about the worker pid their own fake bundler published.

Gates re-run: `make test-native` pass, `make lint` all nine pass, Makefile audits pass, `go test -short -count=1 .` pass.

### Leftover to flag
A mutation run (mutation 2, before the fixture cleanup existed) leaked one fixture worker: pid 52109, command `sh -c 'trap "" TERM; ... while :; do sleep 0.05; done'`. The test now registers a cleanup that kills the published worker pid, so the class is closed, but that one process is still spinning. I did not kill it: my instructions forbid killing by a number scraped from a global `ps`, and its pid file is gone with the test's TempDir. Jesse or the coordinator should end it.

## Review round 2 (all four real; script db656f04d, tests 0ee8533f1, merge d51d33282 = pushed head)
1. Launch window — `defer_signals`/`consume_interrupt` across the fork, replayed once the pid is registered.
2. Group liveness — two handles: `bundle_pgid` (the only thing ever signalled) and `bundle_pid` (only what is waited on). Completion probes the group after the reap and stops outlived workers, saying so. Issue #1246 cited in the PR body for the eventual `scripts/lib/process-group-lib.sh` adoption.
3. Subshell now runs `trap - EXIT HUP INT TERM` before anything else.
4. Added the non-zero-exit fixture test.
`full log:` renamed to the repo-wide `full logs:` marker so `fullLogsPath` is reused rather than duplicated.

Tests: `TestNativeBundleStopsWorkersThatOutliveTheBundler`, `TestNativeBundleFailingBundlerReportsAndPreservesItsLog`, `TestNativeBundleLogCarriesOnlyBundlerOutput`, `TestNativeBundleInterruptDuringLaunchStopsBundlerProcessGroup`, plus `TestNativeBundleDoesNotSignalReapedBundler` renamed `TestNativeBundleNeverSignalsTheBareReapedPid` (a `-pgid` probe after the reap is now the required mechanism, so only bare-pid naming and post-reap terminating signals are violations).

RED-proven: findings 2 and 4, and the bare-pid-instead-of-group mutation (fails the reaped-pid test and all three interrupt subtests). Round 1's timeout test still RED.

### Two gaps reported in the PR body, not papered over
- Finding 3 does not reproduce on bash 3.2 (macOS resets subshell traps), so the log-contents test passes with or without the `trap -`. The fix pins the behaviour for CI's bash 5; I could not produce the local RED.
- The launch window is a couple of instructions wide and cannot be forced from outside the script. The fixture signals from the bundler's own startup and samples 20 iterations (19 reached a running bundler), but the no-deferral mutation passed all 20. That test guards the launch path rather than pinning the deferral; a deterministic pin would need a seam in the script, which I did not add unasked.

Gates on d51d33282: `make test-native` pass (PASS native-bundle 10s, 1581 modules), `make lint` all nine pass, Makefile audits pass, seven native-bundle tests pass, `go test -short -count=1 .` pass. Merged `origin/main` (#1070 shared notes) with `--no-ff`; re-ran `npm ci --prefix mobile-native` after the merge.

## Review round 3 (both real; script cb248c2a2, tests 27775f9ab, merge d3078d447, docs 4490b3fd3 = pushed head)
1. Abandonable stop — `stopping` reentrancy guard so `interrupted` returns while a stop is in flight, and the group id released only after the loop sees ESRCH. Writing the test found a third instance of the same class the review did not name: a signal inside `finish` exited with its own status (HUP turned a TERM interrupt's 143 into 129), so `finish` sets an `exiting` guard too.
2. Bare-pid poll replaced with Bash's job table (`jobs -pr`), the `owned_job_is_running` shape from test-web.sh. The leader pid is now only compared, never passed to `kill`.

Recording test change: `TestNativeBundleNeverSignalsTheBareReapedPid` armed at the reap, so it could not see the pre-reap `kill -0`. The shadowed `kill` now logs every call from the first one, tagged pre/post, and the shadowed `wait` publishes the leader pid; assertions are no bare-pid naming in either phase, no post-reap terminating signal, and a refusal to pass vacuously.

New tests: `TestNativeBundleSecondInterruptDoesNotAbandonTheStop` (TERM/INT/HUP as the second signal) and `TestNativeBundleInterruptDuringExitPreservesStatus`. Ten native-bundle tests total.

RED proofs, each on a detached worktree copy at the branch head and reverted after: the reviewed shape (id released up front + no guard) → RED on INT and HUP; drop the `exiting` guard → RED (129 vs 143); restore the bare-pid poll → RED.

Worth carrying forward: Bash will not re-enter a trap handler for the signal that handler is already running, so a second SIGTERM during a SIGTERM stop is deferred and the bug stays hidden — only a different second signal exposes it. And a shell that *ignores* a signal hands that ignore to its children where it cannot be trapped, which silenced the fixture worker's TERM marker until the bundler stopped ignoring TERM itself.

#1246 is moot (#1145 reset to a small Go helper 2026-09-13); the script and testing.md now say the gate keeps its own minimal group lifecycle by design, with the reset comment linked from the PR body.

Gates on 4490b3fd3: `make test-native` pass (PASS native-bundle 7s, 1581 modules), `make lint` all nine pass, Makefile audits pass, ten native-bundle tests pass, `go test -short -count=1 .` pass. Mutation worktree removed.

## Review round 4 (all three rulings applied; 00a0a806f + flake fix 5a1b056c2 = pushed head; merge c5da889de)
1. First interrupt's status latched: deferred signal recorded only if none is, and `exiting` raised before the stop so the latch covers the gap after `stopping` resets. `TestNativeBundleInterruptDuringExitPreservesStatus` gained an `after-stop-before-exit` subtest (shadowed `wait` firing once `bundle_pgid` is released and `stopping` is 0). RED-proven on a copy: moving `exiting=1` after the stop → 129 instead of 143.
2. Hermes claim removed from the `##` annotation, the generated testing.md row, and the PR body. No Hermes step added (ruling).
3. `TestNativeBundleInterruptDuringLaunchStopsBundlerProcessGroup` deleted; residual stated next to the deferral in the script and in testing.md (window is code-review-guarded, no seam added).

Flake fixed on sight: `TestNativeBundleStopsWorkersThatOutliveTheBundler` failed ~1 run in 3 on the merged head. Fixture race — the fake bundler backgrounded its worker and exited immediately, so the whole run could finish before the worker wrote its pid/readiness files. The bundler now waits for the worker to publish. 25 consecutive runs and 3 full suite passes clean.

Nine native-bundle tests remain. Gates on 5a1b056c2: `make test-native` pass (PASS native-bundle 8s, 1581 modules), `make lint` all nine pass, Makefile audits pass, `go test -short -count=1 .` pass. Mutation worktrees removed.

## Review round 5 (all three in one commit 8104d510b = pushed head; main had not moved)
1. `latched_signal` holds an interrupt that arrives while a gate-owned stop (timeout, leaked-worker) holds the group; `exit_if_interrupted` after each of those stops makes it the verdict — `FAIL native-bundle (interrupted)` plus the signal's status, never the timeout FAIL, never PASS.
2. `consume_interrupt` clears `pending_signal` only after `interrupted` has claimed it.
3. An EXIT-trap wrapper cannot arm before capture (`exiting=1` destroys `$?`), so arming moved one level out: every path reaching an `exit` arms `exiting` before it, putting `finish`'s capture inside the guard. `finish` still arms as a backstop.

Test: `TestNativeBundleInterruptDuringAGateOwnedStopWins` (subtests timeout-stop, leaked-worker-stop), fixture worker declines SIGTERM so each stop has a grace. RED-proven on a copy: dropping the `latched_signal` line fails both subtests. Findings 2 and 3 are one-command windows with no fixture seam (launch deferral; the capture/arm gap), so they are code-review-guarded like the launch window — stated in the PR body, no seams added.

PR body cites #1263 as where this lifecycle goes (Go bounded-run helper from the #1145 replacement).

Gates on 8104d510b: `make test-native` pass (PASS native-bundle 17s, 1581 modules), `make lint` all nine pass, Makefile audits pass, `go test -short -count=1 .` pass, eleven native-bundle tests pass twice in a row. Mutation worktree removed.

Note for the coordinator's round-6 rule: rounds 3, 4 and 5 have all been signal-latching in the same ~40 lines. If round 6 finds another, stopping and waiting for the #1263 helper is the right call — I agree with that read.

## Round 6: no patch; ported instead (local branch `claude/native-metro-gate-port`, commit 626a34cdf, NOT pushed)
Off #1265's head d03e57a22, with #1245 merged in. Script 316 -> 138 lines; test file 718 -> 296; 9 test funcs / 15 scenarios -> 4 / 4. `evener-dev dev bounded-list -timeout Ns -grace Ns -attempts 1 -- npx expo export ...` replaces set -m, both handles, every signalling trap, stop_bundle, interrupted, consume_interrupt, exit_if_interrupted, both latches and the job-table poll. `make test-native-bundle` gains `build-dev`. Real run PASS 11s/1581 modules; RED (unresolved module) exits 1 with the module named; timeout exits 1 with `bounded-list: npx timed out after 2s on each of 1 attempts.` and no survivors. Gates on the port branch: make test-native, make lint (all nine), Makefile audits, four native-bundle tests — all pass.

Helper gaps to fix in #1265 before it merges (see the reply for detail): stdout dropped on a timed-out attempt (the blocker for a gate whose log is the evidence); stdout buffered in memory rather than streamed; no signal handling at all, so the isolated group survives a TERM to the helper (verified); no distinct exit status for a timeout; subcommand is `evener-dev dev bounded-list` and the top-level usage does not list it.
