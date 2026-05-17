# auth-oauth-wins: stored OAuth state beats OPENAI_API_KEY env

**What this covers**: commit `8e8994f` (daemon adapter), `f824379`
(hub UI). Before the fix, `OPENAI_API_KEY` env var always won over a
stored OAuth record — a user who ran `serf openai login` while also
having the env var set silently kept getting routed through the API
key. The fix inverts the priority: a valid OAuth record on disk wins;
env is fallback only.

## Pre-state

- A valid OAuth record exists at `~/.local/state/serf/auth/openai.json`
  (`./serf openai status` shows `source=oauth`). If not, run
  `./serf openai login` first.
- The hub binary is rebuilt and running (so the hub-side priority
  also took effect, per `cmd/serf-hub/app_auth.go`).

## Steps

1. `./serf openai status` — baseline. Confirm `source=oauth`.
2. `OPENAI_API_KEY="sk-test-not-real" ./serf openai status` — env
   var set in the test invocation.
3. Open the hub `/credentials` page in a browser (or hit the JSON
   endpoint). Confirm the OpenAI auth panel shows `source=oauth` /
   `Configured via OAuth — <email>`. This validates the hub-side
   priority (kata `f824379`).

## Expected

- Step 1: `source=oauth`.
- Step 2: still `source=oauth`. Falsification: `source=env` means
  the env var won, the fix regressed.
- Step 3: hub UI labels OAuth as effective. If you see "Configured
  via env API key" or similar, the hub UI priority regressed.

## Cleanup

- None.

## Sharp edges

- `t.Setenv("OPENAI_API_KEY", "")` shielding in tests doesn't apply
  here — we deliberately set the env var to a NON-empty bogus value
  to make sure the priority code actually distinguishes "env is set"
  from "env is empty".
- If the OAuth record has expired (`needs_refresh=true` AND refresh
  fails), the fallback to env is intentional — the test should be
  done with a fresh record.
- Inverse test (`auth-env-fallback.md`) covers the no-OAuth case to
  make sure we didn't break env-only operation.
