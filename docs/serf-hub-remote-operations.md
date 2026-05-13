# Serf Hub Remote Operations

This runbook covers running `serf-hub` on a host that is not the laptop/browser
host. It describes the current code, not a future deployment system.

## Trust Boundary

`serf-hub` is a local orchestrator with same-origin checks and CSP, but it does
not currently provide hub-edge authentication. Hub-spawned daemons use an
internal bearer token, but that does not authenticate users at the Hub edge. Do
not expose Hub directly on an untrusted network.

Use one of these access patterns:

- SSH tunnel to the Hub host, keeping Hub bound to loopback.
- VPN/private network plus a host firewall that limits who can reach the Hub.
- Authenticated TLS reverse proxy in front of Hub.

The Hub's same-origin guard checks `Host` and `Origin` against the configured
`addr` and currently compares browser origins as `http://<addr>`. If users browse
to `hubbox.example:9180`, configure Hub with that exact host and port or make
the proxy rewrite `Host` and `Origin` to the configured value. Binding to
`0.0.0.0:9180` is not enough by itself because browser requests will carry a
different `Host`. A TLS-terminating reverse proxy must also account for the
current HTTP-origin comparison before forwarding browser writes to Hub.

## Required Binaries

Build and install matching binaries on the Hub host:

```bash
make build
make build-hub
make build-tui
```

Start Hub with the Serf binary it should spawn:

```bash
serf-hub --config /etc/serf/hub.toml --serf /usr/local/bin/serf
```

Current flags verified from source:

- `serf-hub --config <path>` loads Hub TOML config.
- `serf-hub --addr <host:port>` overrides `addr`.
- `serf-hub --serf <path>` selects the `serf serve` binary Hub launches.
- `serf launch-check --protocol serf-appwire-v1 --model <provider/model> --json`
  validates the binary/protocol/model contract before spawn.
- `serf-tui --hub-addr <url-or-host-port>` connects the TUI to a Hub.
- `serf-tui --no-auto-start-hub` prevents local auto-start, which is usually
  what you want when targeting a remote Hub.

## Hub Config

Example `/etc/serf/hub.toml`:

```toml
addr = "127.0.0.1:9180"
run_dir = "/var/lib/serf/run"
state_glob = "/var/lib/serf/state/serf/projects/*"
past_index_db = "/var/lib/serf/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50

[serf_launch]
sse_ring_size = 4096

[serf_launch.env]
XDG_STATE_HOME = "/var/lib/serf/state"
```

`addr` is the listen address and the same-origin comparison value.

`run_dir` holds daemon rendezvous files. Hub watches this directory and probes
the daemons it finds there. It is runtime state, not the durable transcript
store. Hub-spawned daemons record their internal `hub_token` there so a
restarted Hub can keep talking to already-running daemons. Keep the directory
private to the service user; current `serf serve` writes it as `0700` with
`0600` files.

`state_glob` tells Hub where to find saved sessions. Each match must be a state
directory containing `sessions/*.meta.json` and transcript JSONL files. When
`state_glob` is omitted, Hub uses `$XDG_STATE_HOME/serf/projects/*`, falling
back to `~/.local/state/serf/projects/*`.

`past_index_db` is the SQLite FTS search index. When omitted, Hub uses
`~/.serf/index.db`. The in-memory past index remains the source of truth; if the
SQLite index cannot be opened or rebuilt, Hub falls back to substring search
over the loaded metadata.

`serf_launch.env` overrides the environment inherited by spawned `serf serve`
children. Hub also sets `SERF_HUB_SPAWNED=1`, `SERF_RUN_DIR`,
`SERF_STATE_DIR`, and a generated `SERF_HUB_TOKEN` for each child. Do not set
`SERF_HUB_TOKEN` in config; Hub-owned launches replace it with the generated
per-Hub token.
Launch model choices come from that same Serf launch harness contract
(`serf launch-check --models`) rather than a static model roster in
`hub.toml`.

`serf_launch.sse_ring_size` passes `--sse-ring-size` to Hub-owned Serf daemons.
Increase it when long sessions produce enough token delta events that late SSE
or AppWire clients need a deeper replay window than the daemon default of 1000
events.

Prefer setting `XDG_STATE_HOME` for service deployments so Serf keeps its
per-project state layout:

```toml
state_glob = "/var/lib/serf/state/serf/projects/*"

[serf_launch.env]
XDG_STATE_HOME = "/var/lib/serf/state"
```

Only set `SERF_STATE_DIR` when you deliberately want every spawned daemon to use
one exact state directory. If you do that, `state_glob` must match that exact
directory, otherwise Hub can spawn sessions that it cannot later find in the
past-session index.

## Provider Credentials

Hub validates configured provider/model choices before spawning Serf. Credential
lookup uses the spawned child environment: the Hub process environment first,
then `serf_launch.env` overrides.

Supported current credential checks:

- OpenAI: `OPENAI_API_KEY` or Serf OpenAI login state in the selected state dir.
- Anthropic: `ANTHROPIC_API_KEY`.
- Google/Gemini: `GEMINI_API_KEY` or `GOOGLE_API_KEY`.
- OpenRouter and OpenRouter Anthropic: `OPENROUTER_API_KEY`.
- Minimax: `MINIMAX_API_KEY`.
- Kimi: `KIMI_API_KEY`.
- GLM: `GLM_API_KEY`.
- OpenAI-compatible: `OPENAI_COMPATIBLE_BASE_URL`.
- Ollama: no API key check.

Do not commit secrets in `hub.toml`. For a service, use a root-owned
environment file with restrictive permissions, or a secret manager that injects
environment variables into the Hub process. If secrets are placed directly in
`hub.toml`, keep the file outside the repo and mode it `0600`.

## Codex App-Server Sources

Hub can connect to already-running Codex app-server instances:

```toml
[[codex_sources]]
id = "codex-local"
endpoint = "ws://127.0.0.1:9900"
bearer_token_file = "/run/secrets/codex-token"
```

Hub can also launch a Codex app-server on demand:

```toml
[[codex_launches]]
id = "codex-managed"
binary = "/usr/local/bin/codex"
working_dir = "/srv/work"
listen = "ws://127.0.0.1:0"
timeout = "30s"

[codex_launches.env]
CODEX_HOME = "/var/lib/serf/codex-home"
```

If `binary` is the top-level `codex` CLI and `args` is omitted, Hub runs
`codex app-server --listen <listen>`. If `binary` is a standalone
`codex-app-server` binary, Hub omits the extra `app-server` subcommand. Hub-owned
Codex app-servers currently require a WebSocket listen URL.

## Browser And TUI Access

For an SSH tunnel:

```bash
ssh -N -L 9180:127.0.0.1:9180 hubbox.example
```

Then open `http://127.0.0.1:9180` locally.

For TUI access to a remote Hub:

```bash
serf-tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub
```

If you connect directly over a private network:

```bash
serf-tui --hub-addr http://hubbox.example:9180 --no-auto-start-hub
```

## Health Checks And Manual Verification

Basic health:

```bash
curl -fsS http://127.0.0.1:9180/api/health
```

Manual spawn/resume verification:

1. Open `/new` in the browser.
2. Spawn a Serf session using a configured provider/model.
3. Confirm the transcript updates live before refresh.
4. Refresh the browser and confirm the transcript replays from the past index.
5. Shut down the session from Hub.
6. Send another message to the ended session and confirm Hub resumes it.
7. Open `serf-tui --hub-addr ... --no-auto-start-hub` and confirm the same
   session appears with source label `serf`.
8. If Codex is configured, spawn or open a Codex source and confirm unsupported
   Serf-only actions are hidden or return structured action-unavailable errors.

## Logs, Restarts, And Backups

Run Hub under a supervisor and capture stdout/stderr. Hub logs config errors,
past-index rebuild errors, roster watch errors, and child-process launch
diagnostics there.

Hub uses `~/.serf/hub.lock` for one Hub process per host user. A second Hub for
the same user exits instead of sharing the same run directory.

Stopping Hub does not make saved transcripts disappear. Saved sessions live
under the state directories matched by `state_glob`; back those up. The
`run_dir` rendezvous files are runtime discovery data and can be recreated by
running daemons or pruned after failed probes.

Hub-launched Codex app-server processes are stopped when Hub shuts down.
Hub-spawned Serf daemons are independent `serf serve` processes; they continue
until the session is shut down or the process exits.

## Kata Tracker

Kata is not part of Hub runtime configuration. This repo currently has no
Hub-specific `KATA_SERVER` or `.kata.local.toml` requirement. For tracker work,
run `kata quickstart` in the workspace and follow its project binding.
