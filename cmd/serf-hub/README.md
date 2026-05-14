# Serf Hub

`serf-hub` is the browser and AppWire orchestrator for `serf serve` daemons.
Run it as the only process your browser and `serf-tui` talk to; Hub launches
Serf daemons, watches their rendezvous files, indexes saved sessions, and can
connect to or launch Codex app-server sources.

This README is the production-style local runbook. For non-local hosts, see
[`docs/serf-hub-remote-operations.md`](../../docs/serf-hub-remote-operations.md).

## Trust Boundary

Hub has same-origin checks and a strict CSP, and Hub-spawned daemons use an
internal bearer token. Hub does not currently provide user authentication at the
Hub edge. Keep it on loopback unless it is behind an SSH tunnel, VPN/private
network firewall, or authenticated reverse proxy.

## Build And Install

From the repo root:

```bash
make build-all

mkdir -p "$HOME/.local/bin"
install -m 0755 ./serf ./serf-hub ./serf-tui "$HOME/.local/bin/"
```

Start Hub with an explicit `--serf` binary so every Hub-owned daemon uses the
binary you just installed:

```bash
"$HOME/.local/bin/serf-hub" \
  --config "$HOME/.serf/hub.toml" \
  --serf "$HOME/.local/bin/serf"
```

## Runtime Directories

```bash
mkdir -p "$HOME/.serf/run" "$HOME/.serf/log" "$HOME/.local/state/serf"
chmod 700 "$HOME/.serf" "$HOME/.serf/run" "$HOME/.local/state/serf"
```

- `~/.serf/run` is runtime rendezvous state for live daemons.
- `~/.local/state/serf/projects/*` is durable per-project Serf state and saved
  transcripts.
- `~/.serf/index.db` is Hub's SQLite search index.

## Hub Config

Create `~/.serf/hub.toml`. This command expands `$HOME` before writing the file;
TOML itself does not expand shell variables.

```bash
cat > "$HOME/.serf/hub.toml" <<EOF
addr = "127.0.0.1:9180"
run_dir = "$HOME/.serf/run"
state_glob = "$HOME/.local/state/serf/projects/*"
past_index_db = "$HOME/.serf/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50

[serf_launch]
sse_ring_size = 4096

[serf_launch.env]
XDG_STATE_HOME = "$HOME/.local/state"

[[codex_launches]]
id = "codex-local"
binary = "codex"
working_dir = "$HOME/Documents/GitHub"
listen = "ws://127.0.0.1:0"
timeout = "30s"

[codex_launches.env]
CODEX_HOME = "$HOME/.codex"
EOF

chmod 600 "$HOME/.serf/hub.toml"
```

Use `codex_launches` when Hub should own the Codex app-server lifecycle. Use
`codex_sources` instead when a Codex app-server is already running:

```toml
[[codex_sources]]
id = "codex-local"
endpoint = "ws://127.0.0.1:9900"
bearer_token_file = "/run/secrets/codex-token"
```

## Provider Credentials

Do not commit secrets in `hub.toml`. Prefer a private environment file loaded
by the supervisor that starts Hub:

```bash
cat > "$HOME/.serf/hub.env" <<'EOF'
export OPENAI_API_KEY='...'
export ANTHROPIC_API_KEY='...'
export GEMINI_API_KEY='...'
export OPENROUTER_API_KEY='...'
EOF

chmod 600 "$HOME/.serf/hub.env"
```

Supported Serf launch credential checks:

- OpenAI: `OPENAI_API_KEY` or Serf OpenAI login state in the user state dir.
- Anthropic: `ANTHROPIC_API_KEY`.
- Google/Gemini: `GEMINI_API_KEY` or `GOOGLE_API_KEY`.
- OpenRouter and OpenRouter Anthropic: `OPENROUTER_API_KEY`.
- Minimax: `MINIMAX_API_KEY`.
- Kimi: `KIMI_API_KEY`.
- GLM: `GLM_API_KEY`.
- OpenAI-compatible: `OPENAI_COMPATIBLE_BASE_URL`.
- Ollama: no API key check.

For Serf-owned OpenAI OAuth, log in once for the user account. The default auth
file is `$XDG_STATE_HOME/serf/auth/openai.json`, or
`$HOME/.local/state/serf/auth/openai.json` when `XDG_STATE_HOME` is not set:

```bash
XDG_STATE_HOME="$HOME/.local/state" "$HOME/.local/bin/serf" openai login
```

Hub's provider picker asks the Serf launch harness for the models visible to
the selected spawn working directory. Session state remains per project, but
Serf-owned OpenAI OAuth is user-scoped by default.

## Start Hub

Foreground run with logs:

```bash
set -a
source "$HOME/.serf/hub.env"
set +a

"$HOME/.local/bin/serf-hub" \
  --config "$HOME/.serf/hub.toml" \
  --serf "$HOME/.local/bin/serf" \
  2>&1 | tee -a "$HOME/.serf/log/hub.log"
```

Production deployments should run the same command under a supervisor and
capture stdout/stderr. Hub logs config errors, past-index rebuild errors, roster
watch errors, and child-process launch diagnostics there.

Only one Hub process can run per host user because Hub takes `~/.serf/hub.lock`.

## Browser And TUI

Browser:

```bash
open http://127.0.0.1:9180
```

TUI:

```bash
"$HOME/.local/bin/serf-tui" \
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

Check the spawn-scoped Serf model list:

```bash
curl -fsS \
  "http://127.0.0.1:9180/api/models?cwd=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' /path/to/project)" \
  | jq .
```

Manual verification:

1. Open `/new` in the browser.
2. Pick a working directory and spawn a Serf session.
3. Confirm the transcript streams live before refresh.
4. Refresh and confirm the transcript replays from saved state.
5. Open `serf-tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub` and
   confirm the same session appears with source label `serf`.
6. If Codex is configured, switch the harness to `codex-local`, spawn a Codex
   session, and confirm Serf-only actions are hidden or report structured
   action-unavailable diagnostics.

## Operations Notes

- Hub-spawned Serf daemons keep running if Hub exits. Stop sessions from Hub or
  kill the daemon processes when you intentionally want them gone.
- Hub-launched Codex app-server processes are stopped when Hub shuts down.
- Existing daemons keep the `serf` binary they were spawned from. Rebuild and
  restart sessions to pick up a new binary.
- `run_dir` is runtime state and can be pruned after failed probes. Saved
  transcripts live under the state directories matched by `state_glob`.
- Keep `hub.toml`, env files, run directories, and state directories private to
  the service user.
