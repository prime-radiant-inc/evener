# Bounded Manager fixture reap repair

**Status: DONE.** The fixture now preserves the original ten-second graceful-proof failure and retained cleanup marker, then performs a bounded TERM/KILL reap of only processes matching the exact executable and config. The acceptance fixture now attempts its separate daemon reaper even when hub proof fails; root removal remains gated on proven graceful shutdown and successful daemon cleanup.

## Red/green evidence

The new regression uses the existing real listener/exit-barrier subprocess. Before the helper change, this command failed after graceful proof timed out with the owned PID still held:

```sh
env -u EVENER_SSH_E2E -u EVENER_SSH_E2E_DEPLOY go test ./cmd/evener-hub -run '^TestStopHostListenerReapsOwnedProcessAfterProofFailure$' -count=1 -v -timeout=2m
```

Captured red output:

```text
=== RUN   TestStopHostListenerReapsOwnedProcessAfterProofFailure
    manager_readiness_cleanup_test.go:186: stopping the deploy check's host hub on 127.0.0.1:57196 (config .../hub.toml): shutdown on 127.0.0.1:57196 is unproved after 10s
--- FAIL: TestStopHostListenerReapsOwnedProcessAfterProofFailure (10.62s)
FAIL
...
```

The red test's deferred SIGUSR1 release and `Wait` reaped its own helper. Its complete captured output was `manager-cleanup-red.log` in session scratch during the run; the relevant output is copied above. The regression now runs the fixture in an isolated child, requires the child test to fail with structural markers for proof failure, retained state, and process exit, and removes only the verified child test root.

After the repair, the focused regression passed. It verifies the fixture listener had closed while the process stayed behind its barrier, the original graceful error remained a child-test failure, the retention marker existed, and the helper process exited by SIGKILL after bounded escalation:

```text
=== RUN   TestStopHostListenerReapsOwnedProcessAfterProofFailure
--- PASS: TestStopHostListenerReapsOwnedProcessAfterProofFailure (14.04s)
PASS
ok  primeradiant.com/evener/cmd/evener-hub 15.914s
```

## Required checks

Both commands ran serially with SSH opt-ins unset:

```sh
env -u EVENER_SSH_E2E -u EVENER_SSH_E2E_DEPLOY go test ./cmd/evener-hub -run '^(TestStopHostListener|TestHostHub|TestHostListener|TestRetainHostCleanup|TestHostCleanupRoot|TestHostDaemon|TestManagerReadinessCleanup)' -count=1 -v -timeout=5m
```

Passed. This included the existing graceful exit-barrier test, the new bounded-reap regression, different-config identity preservation, failed-`ps` rejection, existing daemon-scope check, and cleanup-root retention tests. Command ended with `PASS` and package `ok`.

```sh
env -u EVENER_SSH_E2E -u EVENER_SSH_E2E_DEPLOY go test ./cmd/evener-hub -run '^(TestE2E_ManagerReadiness|TestManagerReadiness)' -count=1 -v -timeout=5m
```

Passed. `TestE2E_ManagerReadinessOwnHubFirstInput` passed. `TestE2E_ManagerReadinessRemoteApprovalSSH`, `TestE2E_ManagerReadinessSavedAttributionAndReplaySSH`, and `TestE2E_ManagerReadinessOwnHubFirstInputSSH` skipped because SSH opt-ins were deliberately unset. No SSH was run.

## Changes and review

- `cmd/evener-hub/app_host_deploy_e2e_test.go`: added a bounded process scan/reaper. It carries executable/config through environment values, double-checks both identity fields immediately before each TERM/KILL, errors on failed scans, and requires a successful empty scan after signals. It does not rely on listener state or signal delivery alone.
- `cmd/evener-hub/e2e_manager_acceptance_test.go`: runs daemon cleanup regardless of graceful hub-proof result; removes the host root only when graceful proof and daemon cleanup both succeed.
- `cmd/evener-hub/manager_readiness_cleanup_test.go`: added real-process failure/reap coverage, identity preservation, and scan-failure assertion. Existing proof-only barrier remains unchanged.

`git diff --cached --check` passed before commit. Only those three owned source paths were staged. The two frozen approval files were not staged or changed:

- `cmd/evener-hub/manager_readiness_approval_test.go`: SHA-256 `c0e37a70dfb1b8d2f7ce2c56b2141ea648a50d9d4333bc2de49707178e8ca336`
- `cmd/evener-hub/manager_readiness_attribution_test.go`: SHA-256 `35933b3bb305185c771e559074195d8326f28b06238f54a677e4fe13e658446c`

Implementation commit: `37172d7ccc3a301cd721f340c9e44fa93c819068` (`test: reap owned manager fixture after proof failure`). Its tree is `aca287e5ebacdae48aa380d4c4d9a3d75e51c8e2`.

The historical slow-shutdown cause remains unproved and was not investigated with another diagnostic run. Physical acceptance scans were not run; they remain after parent review. The focused build/test slot is released.
