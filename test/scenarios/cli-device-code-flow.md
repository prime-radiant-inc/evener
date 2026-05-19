# cli-device-code-flow: serf openai login --device round trip

**What this covers**: kata `p16h`. The end-to-end device-code login chain
(`RequestDeviceCode → PollDeviceAuth → ExchangeDeviceCode → SaveAuth`)
against the real OpenAI servers (`auth.openai.com`). Sibling coverage:
`auth-device-autodetect.md` (mode-selection table), `device_test.go`
(protocol shape, mocked), kata `xd51` (concurrent-login detection).

The scenario is split into **Part A** (fully scriptable, no browser
needed) and **Part B** (requires either a human or an AI agent with
browser access AND OpenAI credentials). Part A is the part that runs in
CI-style automation. Part B documents the manual round-trip for when a
real end-to-end check is wanted.

## Pre-state

- Repo built: `./serf` exists at repo root.
- Network reachable to `auth.openai.com`.
- A clean shell where the test can set env vars.
- The user is NOT currently mid-login (no other `serf openai login`
  process running — `pgrep -f 'serf openai login'` should be empty).

## Part A — automatable portion

Goal: prove `RequestDeviceCode` works against the live endpoint, the
CLI prints the expected machine-readable lines, and the poller exits
cleanly when killed. Stops BEFORE authorization to avoid burning real
state.

### Steps

1. **Set up an isolated state dir** so a leaked successful auth (Part B
   later, or a stray browser tab) doesn't pollute the user's real
   `~/.local/state/serf`:
   ```bash
   tmpdir=$(mktemp -d)
   export XDG_STATE_HOME="$tmpdir/serf-state"
   ```
   Note: `DefaultStateDir()` reads `XDG_STATE_HOME` and appends
   `/serf`, so the actual auth file would land at
   `$tmpdir/serf-state/serf/auth/openai.json`.

2. **Spawn the device login in the background** and capture both
   streams to files:
   ```bash
   ./serf openai login --device \
     > "$tmpdir/login.stdout" 2> "$tmpdir/login.stderr" &
   PID=$!
   ```

3. **Wait briefly** (3–5s is plenty; the device-code request is one
   round trip to `auth.openai.com`):
   ```bash
   sleep 5
   ```

4. **Read stdout** and confirm the three expected lines, in order:
   ```bash
   cat "$tmpdir/login.stdout"
   ```
   Expect exactly:
   ```
   auth_mode=device auth_mode_reason=forced
   device_code_url=https://auth.openai.com/codex/device
   device_code=XXXX-XXXXX
   ```
   The `device_code` value is a fresh code each run (4 chars, hyphen,
   then 5 chars — e.g. `7JN1-VX9XM`).

5. **Read stderr** and confirm the human-readable prompt:
   ```bash
   cat "$tmpdir/login.stderr"
   ```
   Must contain:
   - `To sign in, open this URL on any device:`
   - the verification URL
   - `Device codes are a common phishing target. Never share this code.`
   - `Waiting for authorization (this command will exit automatically)...`

6. **Probe the verification URL** to confirm OpenAI's endpoint is up
   (this proves the CLI isn't just printing a stale-but-unreachable
   URL):
   ```bash
   curl -sI https://auth.openai.com/codex/device | head -1
   ```
   Expect `HTTP/2 302` (or `HTTP/2 200`). A 302 to
   `…/api/accounts/deviceauth/authorize` is the current shape.

7. **Kill the poller cleanly** — we are intentionally NOT going to
   authorize, so we end the wait early instead of letting the 15-min
   timeout run:
   ```bash
   kill $PID
   wait $PID
   echo "exit=$?"
   ```
   Expect `exit=143` (SIGTERM) within ~1s.

8. **Confirm no auth state was written** (we never authorized, so
   nothing should have been persisted):
   ```bash
   ls "$tmpdir/serf-state/serf/auth/openai.json" 2>&1
   ```
   Expect `No such file or directory`.

### Part A — Cleanup

```bash
rm -rf "$tmpdir"
unset XDG_STATE_HOME
```

### Part A — Expected (falsification)

- **Missing `auth_mode=device auth_mode_reason=forced`**: the mode-selection table
  regressed. Cross-check with `auth-device-autodetect.md`.
- **Missing `device_code_url=https://auth.openai.com/codex/device`**:
  either the constant changed (check
  `internal/auth/openai/device.go:20`) or the device endpoint moved.
- **`device_code=` line missing or malformed**: the
  `RequestDeviceCode` call failed silently, or `oaitest`-style framing
  leaked. The code format is `[A-Z0-9]{4}-[A-Z0-9]{5}` today.
- **Verification URL returns 4xx/5xx**: OpenAI's device endpoint
  changed. Update the constant and re-record.
- **`wait $PID` blocks longer than ~5s**: poll-cancellation regressed
  (the device poller should react to context cancellation
  immediately).

## Part B — manual / interactive portion

Goal: complete a real round-trip and verify `SaveAuth` writes a valid
record. Requires either a human at a keyboard or an AI agent with
browser access AND working OpenAI credentials.

### Steps

1. Re-set the isolated state dir (do NOT skip this — see Sharp edges):
   ```bash
   tmpdir=$(mktemp -d)
   export XDG_STATE_HOME="$tmpdir/serf-state"
   ```

2. Launch the login in the foreground so you can watch it complete:
   ```bash
   ./serf openai login --device
   ```

3. Copy the printed `device_code_url=…` and open it in a browser.

4. When prompted, paste the `device_code=…` value (without the
   `device_code=` prefix).

5. Sign into the OpenAI account you want serf to use, and authorize
   the device.

6. Watch the CLI. Within a few seconds of authorization, the poller
   should hit the success branch and the process should exit. The
   final stdout line should look like:
   ```
   state=signed-in source=oauth email=YOU@EXAMPLE.COM expiry=2026-... needs_refresh=false needs_login=false
   ```

7. Confirm the auth file landed in the override dir, not the user's
   home:
   ```bash
   ls -la "$tmpdir/serf-state/serf/auth/openai.json"
   ```
   File must exist, be `chmod 600`, and parse as JSON with a
   non-empty `access_token` and `refresh_token`.

8. Run `serf openai status` with the same override and confirm the
   signed-in state matches:
   ```bash
   ./serf openai status --state-dir "$tmpdir/serf-state/serf"
   ```
   (Or keep `XDG_STATE_HOME` exported and omit `--state-dir`.)
   Expect the same `state=signed-in source=oauth email=…` line.

### Part B — Cleanup

```bash
rm -rf "$tmpdir"
unset XDG_STATE_HOME
```

This deletes the test-only auth record. The user's real
`~/.local/state/serf/auth/openai.json` is untouched.

### Part B — Expected (falsification)

- CLI prints `concurrent_login=detected` on stdout: a previous login
  on this account beat this one. Not a regression — kata `xd51`
  surface. Treat as success if existing auth is valid.
- CLI exits with a 4xx-ish error after authorization: token exchange
  regressed. Check `ExchangeDeviceCode` against OpenAI's current
  device-code spec.
- `auth/openai.json` written but `serf openai status` reports
  `signed-out`: storage write OK but `Status()` read path regressed.
- `auth/openai.json` ends up under `~/.local/state/serf/` instead of
  the override dir: the `XDG_STATE_HOME` plumbing regressed (or you
  forgot the export — see Sharp edges).

## Sharp edges

- **Device codes expire in 15 minutes**. Don't pause Part B midway
  through; if you do, re-run from Part A step 1 to get a fresh code.
- **The `XDG_STATE_HOME` override applies to ALL subsequent `serf`
  invocations in the same shell** — `status`, `logout`, the hub, etc.
  This is what we want during the test but it's a footgun: if you
  `unset XDG_STATE_HOME` between login and status, status will look
  at the user's real state dir and report a misleading `signed-out`.
- **A real OAuth authorization writes to the override state dir, but
  if you forget the override, it writes to the user's real
  `~/.local/state/serf/auth/openai.json` and overwrites whatever was
  there.** Always export `XDG_STATE_HOME` BEFORE running
  `./serf openai login` in this scenario.
- The polling process must be reaped explicitly (`kill $PID; wait
  $PID`). Without `wait`, the shell may show `[1]+ Terminated …`
  noise interleaved with later output.
- The 15-minute poll cap means an abandoned poller eventually
  self-terminates, but during tight iteration you can stack up
  background pollers. `pgrep -f 'serf openai login'` to check;
  `pkill -f 'serf openai login'` to reap.
- `curl -I` on the verification URL returns a 302 redirect, not a
  200. That's the current shape (May 2026). The redirect target is
  `https://auth.openai.com/api/accounts/deviceauth/authorize`. If
  this drifts to a different redirect chain, step 6 still passes as
  long as the first hop is 2xx or 3xx.
- This scenario uses `--device` to force the device flow. If you drop
  the flag, Part A will pick the mode based on environment
  (`SSH_CONNECTION`, `$DISPLAY`, etc.) — see
  `auth-device-autodetect.md`. On a graphical workstation without
  SSH, that means `auth_mode=browser auth_mode_reason=auto` instead and the rest of
  this scenario does not apply.
