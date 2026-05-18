# auth-device-poll-concurrent: device-code poll detects parallel login

**What this covers**: kata `24p1`. When `serf openai login --device` is
mid-poll (no human has entered the user code), a parallel login —
possibly from a `--no-device` browser flow in another shell, possibly
from a different machine refreshing the same XDG state dir over a
shared FS — that writes a fresh `auth.json` to disk should be
detected by the watcher in the original poller, which then exits
cleanly with the existing OAuth state instead of running the full
15-minute device-code timeout.

The test simulates "a parallel login succeeded" by writing a
fake-but-validation-passing `auth.json` directly to the override
state dir while the device poller is running. No real browser flow
is needed; the watcher only cares about the on-disk record's
`source` (`"oauth"`) and `obtained_at` (>= the moment the device
flow started). Sibling coverage: `cli-device-code-flow.md` (the
happy-path round trip), `auth-device-autodetect.md` (mode picker),
`device_test.go::TestLoginWithDeviceDetectsConcurrentLogin` (mocked
unit).

## Pre-state

- Repo built: `./serf` exists at the repo root.
- No other `serf openai login` is running:
  `pgrep -f 'serf openai login'` is empty. (If any are running,
  `pkill -f 'serf openai login'` first — the auto-detect scenario
  is a common source of orphaned pollers.)
- Network reachable to `auth.openai.com` — `RequestDeviceCode` is
  a real HTTPS call before the poll begins.
- The watcher polls `auth.json` once per second
  (`defaultConcurrentLoginWatchInterval` in
  `internal/auth/openai/service.go`). Expect detection within
  ~1–2s of the on-disk write.

## Steps

1. **Create an isolated state dir** and point serf at it via
   `XDG_STATE_HOME`:
   ```bash
   tmpdir=$(mktemp -d)
   export XDG_STATE_HOME="$tmpdir"
   ```
   `DefaultStateDir()` will append `/serf`, so the watcher reads
   `$tmpdir/serf/auth/openai.json`.

2. **Spawn the device login in the background**, capturing
   stdout+stderr together:
   ```bash
   ./serf openai login --device > /tmp/login-out.txt 2>&1 &
   PID=$!
   ```

3. **Wait for the poller to be live** — the `device_code=…` line
   on stdout is the marker that `RequestDeviceCode` returned and
   `PollDeviceAuth` (plus the concurrent-login watcher goroutine)
   is now running:
   ```bash
   until grep -q "device_code=" /tmp/login-out.txt; do sleep 0.5; done
   ```
   Typical: device code appears within ~1–2s.

4. **Write a fake-but-valid `auth.json` directly to the override
   state dir**, simulating a parallel login that just completed.
   `obtained_at` MUST be at or after the moment the device flow
   started (which is `s.now()` captured inside `LoginWithDevice`
   immediately after `RequestDeviceCode` returns) — using "now"
   here is safe because we only reach this step *after* step 3
   confirmed the poller is up:
   ```bash
   mkdir -p "$tmpdir/serf/auth"
   NOW_UTC=$(date -u +%Y-%m-%dT%H:%M:%S.000000000Z)
   EXPIRY=$(date -u -d "+1 hour" +%Y-%m-%dT%H:%M:%S.000000000Z)
   cat > "$tmpdir/serf/auth/openai.json" <<JSON
   {
     "version": 1,
     "provider": "openai",
     "source": "oauth",
     "obtained_at": "$NOW_UTC",
     "token_type": "Bearer",
     "scope": "openid profile email offline_access",
     "access_token": "fake-access-token",
     "refresh_token": "fake-refresh-token",
     "expiry": "$EXPIRY",
     "email": "parallel@example.com"
   }
   JSON
   write_ts=$(date +%s%N)
   ```
   Every field on this JSON is load-bearing for `AuthRecord.Validate()`:
   `version=1`, `provider="openai"`, non-empty `source`,
   `access_token`, `refresh_token`, `token_type`, non-zero
   `expiry`, non-zero `obtained_at`. Drop any one and the
   watcher's `LoadAuth` will reject the file and keep polling.

5. **Wait for the process to exit and time it**:
   ```bash
   wait $PID
   exit_code=$?
   exit_ts=$(date +%s%N)
   elapsed_ms=$(( (exit_ts - write_ts) / 1000000 ))
   echo "exit code: $exit_code"
   echo "elapsed write->exit: ${elapsed_ms} ms"
   ```

6. **Inspect the captured stdout+stderr** for the
   concurrent-login signal:
   ```bash
   cat /tmp/login-out.txt
   ```

## Expected

- After step 5: `exit code: 0`. **Falsification**: exit 1 with a
  `device auth timed out after 15 minutes` message means the
  watcher never fired (or fired but `notifyConcurrentLogin` /
  `cancelPoll` is wired wrong). Any non-zero exit means kata
  `24p1` regressed.
- After step 5: `elapsed_ms` < 5000 (typical: ~1000–2000ms,
  bounded by the 1s watcher tick + filesystem propagation).
  **Falsification**: > 30 000ms means the watcher is not running
  during the poll, or the `obtained_at >= startedAt` gate is
  rejecting the record on a clock skew.
- After step 6: stdout contains, in order:
  ```
  auth_mode=device (forced)
  device_code_url=https://auth.openai.com/codex/device
  device_code=XXXX-XXXXX
  concurrent_login=detected
  state=signed-in source=oauth email=parallel@example.com expiry=… needs_refresh=false needs_login=false
  ```
- After step 6: stderr (interleaved with stdout in this capture)
  contains:
  ```
  Detected concurrent login; using existing OAuth state.
  ```
  This is the human-readable companion to the machine-readable
  `concurrent_login=detected` line.
- **Falsification**: missing `concurrent_login=detected` on
  stdout means the CLI swallowed the detection and we lost the
  signal scripts depend on. Missing the stderr line means the
  human-facing UX regressed.

## Cleanup

```bash
rm -rf "$tmpdir"
unset XDG_STATE_HOME
rm -f /tmp/login-out.txt
```

The `kill $PID` step is NOT needed here — the process exits on
its own once the watcher fires. If the test fails and the process
hangs, `pkill -f 'serf openai login'` it.

## Sharp edges

- **`obtained_at` must be at or after `startedAt`** (where
  `startedAt = s.now()` is captured inside `LoginWithDevice` right
  after `RequestDeviceCode` returns). Pre-existing records are
  intentionally ignored — see
  `TestLoginWithDeviceIgnoresPreExistingState`. If you write the
  fake `auth.json` BEFORE step 3 (before the poller is live),
  whether your timestamp is "now" or earlier is a race against
  `startedAt`. Always do the on-disk write strictly after the
  `device_code=` line appears.
- **`source` must be exactly `"oauth"`**. The watcher skips
  records whose source is `"env"` or `"signed-out"` because those
  represent the env-var fallback, not a real parallel login.
- **All required `AuthRecord` fields must validate**
  (`internal/auth/openai/storage.go::Validate`). `version=1`,
  `provider="openai"`, non-empty `source` / `access_token` /
  `refresh_token` / `token_type`, non-zero `expiry` and
  `obtained_at`. A malformed file is treated identically to a
  half-written file (mid-rename): the watcher logs nothing and
  keeps polling. Failed tests where the watcher "never fires"
  are usually a missing field.
- **The watcher tick is 1s by default**, so detection latency
  has a floor of ~1s even with a near-instant filesystem. Don't
  tighten the expected timing below ~500ms.
- **Don't background the `wait $PID`** — if you stash it in a
  background subshell you lose the exit code and the timing
  measurement. The whole scenario runs to completion in well
  under 30s; just let the foreground `wait` block.
- **Cloudflare can throttle `RequestDeviceCode`** if you run this
  scenario back-to-back (the device-code endpoint is bot-protected).
  If step 3 fails with `device code request failed with status
  403`, wait a few minutes before re-running.
- **`XDG_STATE_HOME` affects every subsequent `serf` invocation
  in the same shell** — `unset` it during cleanup or you will
  pollute later commands' view of the auth file.
