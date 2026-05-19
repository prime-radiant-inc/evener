# auth-device-autodetect: serf openai login auto-picks device vs browser

**What this covers**: commits `4f93712` (device-code login), `8b7762c`
(auto-detect). Verifies the `isHeadlessLoginFor` decision table:
`SERF_LOGIN_HEADLESS` env override, `SSH_CONNECTION`/`SSH_TTY`,
platform defaults, `--device` / `--no-device` explicit flags, and the
conflicting-flags error path.

## Pre-state

- Repo built: `./serf` exists.
- A clean shell where the test can set / unset env vars (`t.Setenv`
  equivalent: use a subshell or `env -u`).

## Steps

Each step launches `./serf openai login` with a controlled environment
and reads ONLY the first line of stdout (`auth_mode=...`). Use a
2-second timeout — the command will start the login flow and block;
we only care about the mode it announces. The device URL will leak a
fresh device code each run; that's expected and harmless.

1. **SERF_LOGIN_HEADLESS=1 forces device**:
   `timeout 2 bash -c "SERF_LOGIN_HEADLESS=1 ./serf openai login 2>&1 | head -1"`
   Expect: `auth_mode=device auth_mode_reason=auto_no_display`
2. **SERF_LOGIN_HEADLESS=0 forces browser**:
   `timeout 2 bash -c "SERF_LOGIN_HEADLESS=0 ./serf openai login 2>&1 | head -1"`
   Expect: `auth_mode=browser auth_mode_reason=auto`
3. **--device explicit**:
   `timeout 2 bash -c "./serf openai login --device 2>&1 | head -1"`
   Expect: `auth_mode=device auth_mode_reason=forced`
4. **--no-device explicit**:
   `timeout 2 bash -c "./serf openai login --no-device 2>&1 | head -1"`
   Expect: `auth_mode=browser auth_mode_reason=forced`
5. **Conflicting flags**:
   `./serf openai login --device --no-device; echo EXIT=$?`
   Expect: stderr `serf openai: conflicting flags: --device and
   --no-device cannot both be set`. EXIT=1.
6. **No env override, default behavior on this box** (Linux,
   typically no `$DISPLAY` over SSH):
   `timeout 2 bash -c "env -u SERF_LOGIN_HEADLESS ./serf openai login 2>&1 | head -1"`
   On a headless box: `auth_mode=device auth_mode_reason=auto_...`. On a graphical
   Linux: `auth_mode=browser auth_mode_reason=auto`.

## Expected

- Each step prints the predicted `auth_mode=...`. Falsification: a
  mode mismatch means the decision table regressed.
- Step 5 error message must mention BOTH flag names so the user can
  see which two conflict.

## Cleanup

- `timeout 2` kills the polling process before any device code
  becomes redeemable. No state to clean.
- If a test took longer than expected and the device code WAS
  authorized in a browser, you'd end up with stale OAuth state.
  Unlikely but worth knowing.

## Sharp edges

- Background `serf openai login` invocations may produce orphaned
  pollers. The 24p1 fix (concurrent-login detection) and the
  15-minute poll cap mean these self-terminate, but if you're rapid-
  firing this scenario, check `ps aux | grep 'serf openai login'`
  and reap.
- `SSH_CONNECTION`/`SSH_TTY` env vars override headless detection.
  If you're testing locally with these set, your "no env override"
  step (#6) will report `auth_mode=device auth_mode_reason=auto_ssh`. That's
  correct behavior; just adjust the expected outcome to match your
  setup.
- The device flow on macOS would default to browser regardless of
  `$DISPLAY` (because `runtime.GOOS == "darwin"` falls through). The
  decision table tests this in unit tests (`TestIsHeadlessLoginForDecisionTable`).
