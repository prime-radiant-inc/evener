# Evener Hub

`evener-hub` is Evener's orchestrator. It serves the web UI — Evener's
default interactive surface — where you start sessions, watch the agent work,
and steer it across many concurrent sessions; the `evener-tui` terminal
dashboard talks to the same API. The hub launches and supervises
`evener serve` daemons, indexes saved sessions for search, and connects to or
launches Codex app-server sources.

**First time here?** [Getting started](getting-started.md)
walks from install to your first session. This document is the
production-style local runbook: config files, credentials, supervised
operation, and smoke checks. For non-local hosts, see
[`evener-hub-remote-operations.md`](evener-hub-remote-operations.md).

## Trust boundary

The hub requires a capability token on every route except `/auth`,
`/api/health`, and the PWA icons. At startup it generates or loads a 256-bit
token at `${XDG_STATE_HOME:-~/.local/state}/evener/auth-token` (mode 0600)
and logs an authorization URL:

```
[hub] auth URL (visit once per browser): http://127.0.0.1:9180/auth?token=...
```

A browser authorizes by visiting that URL once; the hub sets a long-lived
`SameSite=Lax` cookie and slides its expiry forward on each visit. Scripted
clients pass the token as `Authorization: Bearer`. Treat the token as a
credential: anyone holding it has full hub access.

Daemons spawned by the hub authenticate to it with a separate per-hub bearer
token recorded in their rendezvous file, and the hub serves a strict content
security policy. The hub binds to loopback by default. When you expose it
further, put it behind an SSH tunnel, VPN or private-network firewall, or an
authenticated reverse proxy — the token is a bearer capability, not a
per-user login system.

## Build and install

From the repo root:

```bash
make install
```

This builds `evener`, `evener-hub`, `evener-tui`, `evener-doctor`, and
`evener-migrate`, installs the binaries under `~/.local/share/evener/bin`, and
symlinks them into `~/.local/bin`. The hub, TUI, and doctor workflows resolve
sibling binaries through the symlink targets, so the installed commands find
each other without extra flags. `make install-home` is an alias for the same
layout.

For a system-style install under `/usr/local`:

```bash
sudo make install-system
```

## Runtime directories

Evener creates runtime and config directories on first use, not at
install time. Paths below use the XDG defaults; `${XDG_STATE_HOME:-...}` and
`${XDG_CONFIG_HOME:-...}` forms apply throughout.

- `${XDG_STATE_HOME:-~/.local/state}/evener/run` holds each live daemon's
  rendezvous file — a small JSON document, named by PID, that tells the hub
  how to reach and authenticate to that daemon — plus per-daemon logs.
- `${XDG_STATE_HOME:-~/.local/state}/evener/projects/*` is durable per-project
  Evener state and saved transcripts.
- `${XDG_STATE_HOME:-~/.local/state}/evener/index.db` is the hub's SQLite
  search index.
- `${XDG_STATE_HOME:-~/.local/state}/evener/auth-token` is the hub's
  capability token (see Trust Boundary).
- `${XDG_CONFIG_HOME:-~/.config}/evener/skills` and
  `${XDG_CONFIG_HOME:-~/.config}/evener/plugins` are user extension roots
  created by Evener startup.

Those extension roots are not active just because they exist. Add standalone
skill paths to `skills_dirs` and plugin roots to `plugin_dirs` in the layered
launch config, or pass the equivalent CLI flags for a single launch.

## Hub config

`${XDG_CONFIG_HOME:-~/.config}/evener/hub.toml` is optional. If it is absent,
the hub uses defaults: `hub_state_root =
${XDG_STATE_HOME:-~/.local/state}/evener`, `run_dir` = the rendezvous
directory described above, `state_glob` =
`${XDG_STATE_HOME:-~/.local/state}/evener/projects/*`, and `past_index_db` =
`${XDG_STATE_HOME:-~/.local/state}/evener/index.db`. Missing config does not
create a default launch stanza; launch defaults still come from the layered
launch files described below.

Create `hub.toml` when you need to override those paths or the listen
address. This command expands `$HOME` before writing the file; TOML itself
does not expand shell variables.

```bash
mkdir -p "$HOME/.config/evener"
cat > "$HOME/.config/evener/hub.toml" <<EOF
addr = "127.0.0.1:9180"
hub_state_root = "$HOME/.local/state/evener"
run_dir = "$HOME/.local/state/evener/run"
state_glob = "$HOME/.local/state/evener/projects/*"
past_index_db = "$HOME/.local/state/evener/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50
EOF

chmod 600 "$HOME/.config/evener/hub.toml"
```

Codex app-server integration is a separate, optional block. Use
`codex_launches` when the hub should own the Codex app-server lifecycle:

```toml
[[codex_launches]]
id = "codex-local"
binary = "codex"
working_dir = "/path/to/projects"
listen = "ws://127.0.0.1:0"
timeout = "30s"

[codex_launches.env]
CODEX_HOME = "/path/to/.codex"
```

Use `codex_sources` instead when a Codex app-server is already running:

```toml
[[codex_sources]]
id = "codex-local"
endpoint = "ws://127.0.0.1:9900"
bearer_token_file = "/run/secrets/codex-token"
```

## Launch configuration

`evener serve` daemons spawned by the hub get their flags from a layered config:

- **Global**: `${XDG_CONFIG_HOME:-~/.config}/evener/launch.toml` — hub-wide
  defaults (model, agent, reasoning effort, skills/plugin dirs, MCP servers,
  etc.). Editable from the hub UI's Launch settings tab or by hand.
- **In-repo**: `<cwd>/.evener/launch.toml` — per-project config shipped in
  the working directory. Trust-on-first-use: the hub UI prompts to review
  and approve before applying. Untrusted in-repo files are skipped.
- **Local per-project**: `<cwd>/.evener/launch.local.toml` — personal
  per-project defaults. Keep this file out of version control; this repo's
  `.gitignore` ignores it while still allowing shared `.evener/launch.toml`.
  Existing `~/.config/evener/projects/<id>/launch.toml` files are read as a
  fallback until the project layer is saved in the new location.
- **Per-launch overrides**: `launchOverrides` on `ThreadStart` — applied to
  a single spawn only.

Layers merge in order: global → in-repo → project → per-launch.
- **Scalars** (model, reasoning_effort, etc.): most-specific value wins.
- **Lists** (skills_dirs, plugin_dirs, mcps, mcp_configs): concatenate in
  layer order; no dedup. Two exceptions replace instead of concatenating:
  `system_prompt_append` collapses to its first file-mode entry, and
  `model_fallbacks` takes the most-specific value.
- **Env map** (`[env]`): merge by key; most-specific wins per key.

See the
[launch config design spec](superpowers/specs/2026-05-16-hub-evener-launch-config-design.md)
for the full schema and semantics.

## Provider credentials

> Architecture reference:
> [`llm-providers.md`](llm-providers.md) (provider routing,
> profiles, adapters) and
> [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
> (credentials, OAuth, and the hub launch/spawn model).

The hub manages `${XDG_CONFIG_HOME:-~/.config}/evener/credentials.toml`
(chmod 600). The file's format is a small TOML document:

```toml
schema = 1

[providers.anthropic]
api_key = "sk-ant-..."

[providers.openai]
api_key = "sk-..."

[providers.openrouter]
api_key = "..."
```

The hub UI (`/credentials`) or TUI (`/credentials`) writes this file via
the `evener/auth/apiKey/set` RPC. Process-env credentials (e.g.,
`ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
file entry exists for the provider — matching the `hub.env` style for users
who prefer external secret management.

### OpenAI credential resolution

OpenAI supports both an API key (stored in `credentials.toml` like any other
provider, or via `OPENAI_API_KEY`) and OAuth (sign in via
`evener/auth/login/start`; state stored in
`${XDG_STATE_HOME:-~/.local/state}/evener/auth/openai.json`). An explicit
OAuth sign-in wins over the file key, which in turn shadows the environment
variable.

The two routes hit **different backends**: OAuth routes to the
ChatGPT/Codex backend (`OPENAI_CHATGPT_BASE_URL`), while an API key routes to
the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
interchangeable credentials for one endpoint. See
[`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
for the full resolution detail.

## Start the hub

Foreground run with logs:

```bash
mkdir -p "$HOME/.local/state/evener/log"
"$HOME/.local/bin/evener-hub" 2>&1 | tee -a "$HOME/.local/state/evener/log/hub.log"
```

The hub listens on `127.0.0.1:9180` unless `addr` in `hub.toml` says
otherwise, and logs the authorization URL described under Trust Boundary on
every start.

If you manage credentials via environment variables rather than
`${XDG_CONFIG_HOME:-~/.config}/evener/credentials.toml`, source your env file
before starting:

```bash
set -a; source "$HOME/.config/evener/hub.env"; set +a
```

Production deployments should run the same command under a supervisor and
capture stdout/stderr. The hub logs config errors, past-index rebuild errors,
roster watch errors, and child-process launch diagnostics there. On macOS,
[`scripts/ops/deploy-hub.sh`](../scripts/ops/deploy-hub.sh) builds this
checkout's `evener-hub` and restarts its launchd job without stopping the old
process until the new one is built and healthy; see
`scripts/ops/deploy-hub.sh --help`.

The hub takes an `flock` on `hub.lock` in its state root, so one hub process runs
per `hub_state_root` — one per user under the default layout.

## Browser and TUI

Browser: visit the authorization URL logged at startup. It sets the auth
cookie; after that, plain `http://127.0.0.1:9180` works in that browser.

TUI:

```bash
"$HOME/.local/bin/evener-tui" \
  --hub-addr http://127.0.0.1:9180 \
  --no-auto-start-hub
```

Use `--no-auto-start-hub` for production-style runs so the TUI does not start a
separate local hub with default config.

## Smoke checks

The smoke checks use `jq` and `python3`; install both first. Basic health
(this route is auth-exempt):

```bash
curl -fsS http://127.0.0.1:9180/api/health | jq .
```

Other API routes need the auth token. Check the spawn-scoped Evener model
list:

```bash
curl -fsS \
  -H "Authorization: Bearer $(cat "$HOME/.local/state/evener/auth-token")" \
  "http://127.0.0.1:9180/api/models?cwd=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' /path/to/project)" \
  | jq .
```

Manual verification:

1. Authorize the browser via the startup auth URL, then open `/new`.
2. Pick a working directory and spawn an Evener session.
3. Confirm the transcript streams live before refresh.
4. Refresh and confirm the transcript replays from saved state.
5. Open `evener-tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub` and
   confirm the same session appears with source label `evener`.
6. If Codex is configured, switch the harness to `codex-local`, spawn a Codex
   session, and confirm Evener-only actions are hidden or report structured
   action-unavailable diagnostics.

## Operations notes

- Evener daemons spawned by the hub keep running if the hub exits. Stop sessions
  from the hub or kill the daemon processes when you want them stopped.
- Codex app-server processes launched by the hub stop when the hub shuts down.
- Existing daemons keep the `evener` binary they were spawned from. Rebuild and
  restart sessions to pick up a new binary.
- `run_dir` is runtime state and can be pruned after failed probes. Saved
  transcripts live under the state directories matched by `state_glob`.
- Keep `hub.toml`, env files, the auth token, run directories, and state
  directories private to the service user.
