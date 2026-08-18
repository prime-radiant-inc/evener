# Evener Hub

`evener-hub` is the browser and AppWire orchestrator for `evener serve` daemons.
Run it as the only process your browser and `evener-tui` talk to; Hub launches
Evener daemons, watches their rendezvous files, indexes saved sessions, and can
connect to or launch Codex app-server sources.

This README is the production-style local runbook. For non-local hosts, see
[`docs/evener-hub-remote-operations.md`](../../docs/evener-hub-remote-operations.md).

## Trust Boundary

Hub has same-origin checks and a strict CSP, and Hub-spawned daemons use an
internal bearer token. Hub does not currently provide user authentication at the
Hub edge. Keep it on loopback unless it is behind an SSH tunnel, VPN/private
network firewall, or authenticated reverse proxy.

## Build And Install

From the repo root:

```bash
make install
```

This builds `evener`, `evener-hub`, `evener-tui`, and `evener-doctor`, installs the
binaries under `~/.local/share/evener/bin`, and symlinks them into
`~/.local/bin`. Hub, TUI, and doctor workflows resolve sibling binaries through
the symlink targets, so the installed commands find each other without extra
flags. `make install-home` is an alias for the same layout.

For a system-style install under `/usr/local`:

```bash
sudo make install-system
```

## Runtime Directories

The installer does not create runtime/config directories. Evener creates them on
first use.

- `~/.evener/run` is runtime rendezvous state for live daemons.
- `~/.local/state/evener/projects/*` is durable per-project Evener state and saved
  transcripts.
- `~/.evener/index.db` is Hub's SQLite search index.
- `~/.config/evener/skills` and `~/.config/evener/plugins` are user extension
  roots created by Evener startup.

Those extension roots are not active just because they exist. Add standalone
skill paths to `skills_dirs` and plugin roots to `plugin_dirs` in the layered
launch config, or pass the equivalent CLI flags for a single launch.

## Hub Config

`~/.evener/hub.toml` is optional. If it is absent, Hub uses defaults:
`hub_state_root = ~/.evener`, `run_dir` from the rendezvous default,
`state_glob = ~/.local/state/evener/projects/*`, and `past_index_db =
~/.evener/index.db`. Missing config does not create a default launch stanza;
launch defaults still come from the layered launch files described below.

Create `~/.evener/hub.toml` when you need to override those paths or configure
Codex app-server sources/launches. This command expands `$HOME` before writing
the file; TOML itself does not expand shell variables.

```bash
cat > "$HOME/.evener/hub.toml" <<EOF
addr = "127.0.0.1:9180"
hub_state_root = "$HOME/.evener"
run_dir = "$HOME/.evener/run"
state_glob = "$HOME/.local/state/evener/projects/*"
past_index_db = "$HOME/.evener/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50

[[codex_launches]]
id = "codex-local"
binary = "codex"
working_dir = "$HOME/Documents/GitHub"
listen = "ws://127.0.0.1:0"
timeout = "30s"

[codex_launches.env]
CODEX_HOME = "$HOME/.codex"
EOF

chmod 600 "$HOME/.evener/hub.toml"
```

Use `codex_launches` when Hub should own the Codex app-server lifecycle. Use
`codex_sources` instead when a Codex app-server is already running:

```toml
[[codex_sources]]
id = "codex-local"
endpoint = "ws://127.0.0.1:9900"
bearer_token_file = "/run/secrets/codex-token"
```

## Launch Configuration

Hub-spawned `evener serve` daemons get their flags from a layered config:

- **Global**: `~/.evener/launch.toml` — hub-wide defaults (model, agent,
  reasoning effort, skills/plugin dirs, MCP servers, etc.). Editable from
  the Hub UI's Launch settings tab or by hand.
- **In-repo**: `<cwd>/.evener/launch.toml` — per-project config shipped in
  the working directory. Trust-on-first-use: the Hub UI prompts to review
  and approve before applying. Untrusted in-repo files are skipped.
- **Local per-project**: `<cwd>/.evener/launch.local.toml` — personal
  per-project defaults. Keep this file out of version control; this repo's
  `.gitignore` ignores it while still allowing shared `.evener/launch.toml`.
  Existing `~/.evener/projects/<id>/launch.toml` files are read as a fallback
  until the project layer is saved in the new location.
- **Per-launch overrides**: `launchOverrides` on `ThreadStart` — applied to
  a single spawn only.

Layers merge in order: global → in-repo → project → per-launch.
- **Scalars** (model, reasoning_effort, etc.): most-specific value wins.
- **Lists** (skills_dirs, plugin_dirs, mcps, mcp_configs,
  system_prompt_append): concatenate in layer order; no dedup.
- **Env map** (`[env]`): merge by key; most-specific wins per key.

See `docs/superpowers/specs/2026-05-16-hub-evener-launch-config-design.md`
for the full schema and semantics.

## Provider Credentials

> Architecture reference:
> [`docs/llm-providers.md`](../../docs/llm-providers.md) (provider routing,
> profiles, adapters) and
> [`docs/llm-provider-config-and-launch.md`](../../docs/llm-provider-config-and-launch.md)
> (credentials, OAuth, and the hub launch/spawn model).

Hub-managed at `~/.evener/credentials.toml` (chmod 600). The file's format
is a small TOML document:

```toml
schema = 1

[providers.anthropic]
api_key = "sk-ant-..."

[providers.openai]
api_key = "sk-..."

[providers.openrouter]
api_key = "..."
```

The Hub UI (`/credentials`) or TUI (`:credentials`) writes this file via
the `evener/auth/apiKey/set` RPC. Process-env credentials (e.g.,
`ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
file entry exists for the provider — matching the `hub.env` style for users
who prefer external secret management.

### OpenAI credential resolution

OpenAI supports both an API key (stored in `credentials.toml` like any other
provider, or via `OPENAI_API_KEY`) and OAuth (sign in via
`evener/auth/login/start`; state stored in `~/.evener/auth/openai.json`).

The effective credential is resolved by precedence:

1. **OAuth** record (`openai.json`), if signed in;
2. **file** key (`credentials.toml`);
3. **`OPENAI_API_KEY`** env var.

The file layer shadows env, like other providers; an explicit OAuth sign-in
wins over both. The two routes hit **different backends**: OAuth routes to the
ChatGPT/Codex backend (`OPENAI_CHATGPT_BASE_URL`), while an API key routes to
the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
interchangeable credentials for one endpoint.

## Start Hub

Foreground run with logs:

```bash
mkdir -p "$HOME/.evener/log"
"$HOME/.local/bin/evener-hub" 2>&1 | tee -a "$HOME/.evener/log/hub.log"
```

If you manage credentials via environment variables rather than
`~/.evener/credentials.toml`, source your env file before starting:

```bash
set -a; source "$HOME/.evener/hub.env"; set +a
```

Production deployments should run the same command under a supervisor and
capture stdout/stderr. Hub logs config errors, past-index rebuild errors, roster
watch errors, and child-process launch diagnostics there.

Only one Hub process can run per host user because Hub takes `~/.evener/hub.lock`.

## Browser And TUI

Browser:

```bash
open http://127.0.0.1:9180
```

TUI:

```bash
"$HOME/.local/bin/evener-tui" \
  --hub-addr http://127.0.0.1:9180 \
  --no-auto-start-hub
```

Use `--no-auto-start-hub` for production-style runs so the TUI does not start a
separate local Hub with default config.

## Smoke Checks

Basic health:

```bash
curl -fsS http://127.0.0.1:9180/api/health | jq .
```

Check the spawn-scoped Evener model list:

```bash
curl -fsS \
  "http://127.0.0.1:9180/api/models?cwd=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' /path/to/project)" \
  | jq .
```

Manual verification:

1. Open `/new` in the browser.
2. Pick a working directory and spawn a Evener session.
3. Confirm the transcript streams live before refresh.
4. Refresh and confirm the transcript replays from saved state.
5. Open `evener-tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub` and
   confirm the same session appears with source label `evener`.
6. If Codex is configured, switch the harness to `codex-local`, spawn a Codex
   session, and confirm Evener-only actions are hidden or report structured
   action-unavailable diagnostics.

## Operations Notes

- Hub-spawned Evener daemons keep running if Hub exits. Stop sessions from Hub or
  kill the daemon processes when you intentionally want them gone.
- Hub-launched Codex app-server processes are stopped when Hub shuts down.
- Existing daemons keep the `evener` binary they were spawned from. Rebuild and
  restart sessions to pick up a new binary.
- `run_dir` is runtime state and can be pruned after failed probes. Saved
  transcripts live under the state directories matched by `state_glob`.
- Keep `hub.toml`, env files, run directories, and state directories private to
  the service user.
