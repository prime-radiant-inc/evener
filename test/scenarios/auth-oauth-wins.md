# auth-oauth-wins: stored OAuth state beats OPENAI_API_KEY env

**What this covers**: commit `8e8994f` (daemon adapter), `f824379`
(hub UI). Before the fix, `OPENAI_API_KEY` env var always won over a
stored OAuth record — a user who ran `serf openai login` while also
having the env var set silently kept getting routed through the API
key. The fix inverts the priority: a valid OAuth record on disk wins;
env is fallback only.

## Pre-state

Fully hermetic — this card never reads or writes Jesse's real
`~/.local/state/serf/auth/openai.json`. The precedence rule under test is
decided entirely from disk (`Service.Status`, `auth/openai/service.go`, and
the hub's `openAIInstanceStatus`, `cmd/serf-hub/app_auth.go`, both make no
network calls), so a synthetic record with a future expiry exercises it
exactly as a real sign-in would.

```bash
# Isolated everything, per docs/agentic-testing.md's Setup checklist.
run=$(mktemp -d -t serf-e2e-oauth-wins-XXXXXX)
go build -o "$run/serf" ./cmd/serf
go build -o "$run/serf-hub" ./cmd/serf-hub

export HOME="$run/home"
mkdir -p "$HOME"
unset XDG_STATE_HOME    # else an ambient value outranks the scratch $HOME

# A fabricated OAuth record in the scratch state root — same shape
# `serf openai login` writes (auth/openai/storage.go: AuthRecord, and
# Validate's required fields). No login, no network, no real token.
expiry=$(date -u -v+1d +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ)
mkdir -p "$HOME/.local/state/serf/auth"
cat > "$HOME/.local/state/serf/auth/openai.json" <<EOF
{
  "version": 1,
  "provider": "openai",
  "source": "oauth",
  "obtained_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "token_type": "Bearer",
  "scope": "openid profile email",
  "access_token": "scenario-not-a-real-access-token",
  "refresh_token": "scenario-not-a-real-refresh-token",
  "expiry": "$expiry",
  "email": "scenario@example.com"
}
EOF
chmod 600 "$HOME/.local/state/serf/auth/openai.json"

# The hub carries the bogus env key: that IS the layer OAuth must shadow,
# and it is also what makes the hub materialize an `openai` instance in the
# scratch providers.toml (cmdutil.MaterializeProvidersConfig seeds from the
# environment when the file is absent).
OPENAI_API_KEY="sk-scenario-not-a-real-key" \
  "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" 2>"$run/hub.log" &
HUBPID=$!
for i in $(seq 1 50); do
  PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub.log" 2>/dev/null | grep -oE '[0-9]+$') || true
  [ -n "$PORT" ] && break
  kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub.log" >&2; exit 1; }
  sleep 0.1
done
[ -n "$PORT" ] || { echo "hub never logged a listening port" >&2; exit 1; }
HUB=http://127.0.0.1:$PORT
TOKEN=$(cat "$HOME/.serf/auth-token")
```

## Steps

1. `"$run/serf" openai status` — baseline. Confirm `source=oauth`.
2. `OPENAI_API_KEY="sk-scenario-not-a-real-key" "$run/serf" openai status`
   — env var set in the test invocation.
3. Open the isolated hub's credentials page:
   `$HUB/auth?token=$TOKEN&next=/credentials`. Confirm the OpenAI instance
   row shows the OAuth layer marked `effective`, labelled
   `Configured via OAuth (scenario@example.com)`, with
   `Configured via environment variable` listed below it as `shadowed`.
   This validates the hub-side priority (commit `f824379`).

## Expected

- Step 1: `state=signed-in source=oauth email=scenario@example.com`.
- Step 2: still `source=oauth`. Falsification: `source=env` means
  the env var won, the fix regressed.
- Step 3: hub UI labels OAuth as effective. If the env row is the
  effective one, the hub UI priority regressed.

## Cleanup

```bash
kill "$HUBPID" 2>/dev/null
rm -rf "$run"
```

## Sharp edges

- The OAuth record is synthetic: it proves precedence, never that the
  token works. Do not extend this card with a real completion call — the
  access token is a placeholder and any live request will fail with 401,
  which says nothing about the priority logic.
- `t.Setenv("OPENAI_API_KEY", "")` shielding in tests doesn't apply
  here — we deliberately set the env var to a NON-empty bogus value
  to make sure the priority code actually distinguishes "env is set"
  from "env is empty".
- `expiry` must stay in the future. `openAIStatusFromRecord` maps an
  expired record to `needs_login=true` and the fallback to env becomes
  intentional — a stale `expiry` in the fixture looks exactly like the
  regression this card hunts.
- `unset XDG_STATE_HOME` is load-bearing. `DefaultStateDir()` prefers it
  over `$HOME/.local/state`, so an ambient value left over from another
  scenario would send both the fixture write and the lookup somewhere the
  scratch `$HOME` doesn't reach.
- Step 3 needs the frontend built into the binary. `frontend/dist` is not
  tracked, so a `go build ./cmd/serf-hub` in a fresh checkout embeds
  nothing and every page route answers `503 serf-hub web app not built:
  run 'make build-web' and rebuild`. Run `make build-web` once before the
  `go build` above (steps 1 and 2 need only the `serf` binary and are
  unaffected).
