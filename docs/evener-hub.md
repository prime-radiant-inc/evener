# Evener Hub

`evener hub` is Evener's orchestrator. It serves the web UI — Evener's
default interactive surface — where you start sessions, watch the agent work,
and steer it across many concurrent sessions; the `evener tui` terminal
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
`/api/health`, and the PWA icons. At startup it loads
`<hub_state_root>/auth-token`; the default `hub_state_root` is
`${XDG_STATE_HOME:-$HOME/.local/state}/evener`, and `hub.toml` may override it.
It creates a fresh 256-bit token when the file is absent. A newly created token
file is written mode 0600; a non-empty existing file is accepted after
surrounding whitespace is trimmed, without format or mode enforcement. The hub
logs an authorization URL:

```
[hub] auth URL (visit once per browser): http://127.0.0.1:9180/auth?token=...
```

A browser authorizes by visiting that URL once; the hub sets a long-lived
`SameSite=Lax` cookie and slides its expiry forward on each visit. Scripted
clients pass the token as `Authorization: Bearer`. Treat the token as a
credential: anyone holding it has full hub access.

Daemons spawned by the hub authenticate to it with a separate per-hub bearer
token recorded in their rendezvous file, and are forced to loopback. Direct
`evener serve` also defaults to loopback, but `--addr` can override it. The hub
serves a strict content security policy. When you expose it further, put it
behind an SSH tunnel, VPN or private-network firewall, or an authenticated
reverse proxy — the token is a bearer capability, not a per-user login system.

## Build and install

From the repo root:

```bash
make install
```

This builds `evener`, `evener hub`, `evener tui`, `evener doctor`, and
`evener migrate`, installs the binaries under `~/.local/share/evener/bin`, and
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
install time. Paths below use the XDG defaults; when those variables are
unset, the corresponding defaults are under `$HOME/.local/state` and
`$HOME/.config`.

- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/run` holds each live daemon's
  rendezvous file — a small JSON document, named by PID, that tells the hub
  how to reach and authenticate to that daemon — plus per-daemon logs.
- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/projects/*` is durable per-project
  Evener state and saved transcripts.
- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/index.db` is the hub's SQLite
  search index.
- `${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth-token` is the hub's
  capability token (see Trust Boundary).
- `${XDG_CONFIG_HOME:-$HOME/.config}/evener/skills` and
  `${XDG_CONFIG_HOME:-$HOME/.config}/evener/plugins` are user extension roots
  created by Evener startup.

Those extension roots are not active just because they exist. Add standalone
skill paths to `skills_dirs` and plugin roots to `plugin_dirs` in the layered
launch config, or pass the equivalent CLI flags for a single launch.

## Hub config

`${XDG_CONFIG_HOME:-$HOME/.config}/evener/hub.toml` is optional. If it is absent,
the hub uses defaults: `hub_state_root =
${XDG_STATE_HOME:-$HOME/.local/state}/evener`, `run_dir` = the rendezvous
directory described above, `state_glob` =
`${XDG_STATE_HOME:-$HOME/.local/state}/evener/projects/*`, and `past_index_db` =
`${XDG_STATE_HOME:-$HOME/.local/state}/evener/index.db`. Missing config does not
create a default launch stanza; launch defaults still come from the layered
launch files described below.

Create `hub.toml` when you need to override those paths or the listen
address. The shell expands `hub_config` and the `$HOME` values in the heredoc
before writing the file; TOML itself does not expand shell variables.

```bash
hub_config="${XDG_CONFIG_HOME:-$HOME/.config}/evener/hub.toml"
mkdir -p "$(dirname "$hub_config")"
cat > "$hub_config" <<EOF
addr = "127.0.0.1:9180"
hub_state_root = "$HOME/.local/state/evener"
run_dir = "$HOME/.local/state/evener/run"
state_glob = "$HOME/.local/state/evener/projects/*"
past_index_db = "$HOME/.local/state/evener/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50
EOF

chmod 600 "$hub_config"
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

- **Global**: `${XDG_CONFIG_HOME:-$HOME/.config}/evener/launch.toml` — hub-wide
  defaults (model, agent, reasoning effort, skills/plugin dirs, MCP servers,
  etc.). Editable from the hub UI's Launch settings tab or by hand.
- **In-repo**: `<cwd>/.evener/launch.toml` — per-project config shipped in
  the working directory. Trust-on-first-use: the hub UI prompts to review
  and approve before applying. Untrusted in-repo files are skipped.
- **Local per-project**: `<cwd>/.evener/launch.local.toml` — personal
  per-project defaults. Keep this file out of version control; this repo's
  `.gitignore` ignores it while still allowing shared `.evener/launch.toml`.
  Existing `${XDG_CONFIG_HOME:-$HOME/.config}/evener/projects/<id>/launch.toml`
  files are read as a fallback until the project layer is saved in the new
  location.
- **Per-launch overrides**: `launchOverrides` on `ThreadStart` — applied to
  a single spawn only.

Layers merge in order: global → in-repo → project → per-launch.
- **Scalars** (model, reasoning_effort, etc.): most-specific value wins.
- **Lists** (skills_dirs, plugin_dirs, mcps, mcp_configs): concatenate in
  layer order; no dedup. Two exceptions replace instead of concatenating:
  `system_prompt_append` collapses to its first file-mode entry, and
  `model_fallbacks` takes the most-specific value.
- **Env map** (`[env]`): merge by key; most-specific wins per key. Credential-like
  keys are rejected in every launch-config layer; use the credentials file, a
  supported provider environment variable exported before starting the hub, or
  OpenAI OAuth (the `openai-codex` instance) instead.

See the
[launch config design spec](superpowers/specs/2026-05-16-hub-evener-launch-config-design.md)
for the full schema and semantics.

### Session plugin selection

Plugin selection is a new-session control. The default state leaves the
selection omitted, so the session uses every otherwise-loadable plugin from the
resolved explicit directories and globally enabled installed plugins. Choosing
an individual plugin, **All**, or **None** changes the launch to an explicit
allow-list of manifest names; **None** sends an explicit empty list and loads no
plugins. Globally disabled plugins are never selectable, and this control does
not change persistent plugin state.

In the desktop new-session pane, the summary appears between the working
directory/model/effort controls and Advanced options:

```text
Plugins for this session
5 of 6 will load · session only                                      v
```

Expanding it shows a filter, **All** and **None** actions, one accessible switch
per candidate, source metadata (marketplace or directory), component counts,
and any preview diagnostics. Diagnostics identify invalid or duplicate
candidates without hiding the remaining valid rows. A stale or unavailable
explicit selection is a blocking error that must be corrected before Start.

On mobile, the **Plugins — N of M** row below the existing launch settings opens
a sheet with the same filter, actions, metadata, and diagnostics. The sheet
stays open while switches are changed; **Done** applies the selection and
**Cancel** restores the selection from before the sheet opened.

The TUI new-session form has a `Plugins: N/M enabled` field after `Dir` and
before `Prompt`. Enter opens the `Plugins for this session` picker; Space
toggles a row, `A` selects all visible rows, `N` selects none, Enter applies, and
Escape cancels. This field is separate from the global `/plugins` manager.

The launcher previews the effective candidates for the current working directory
and launch overrides. While inspection is pending it shows `Inspecting
plugins…`; a failed inspection shows `Couldn't inspect plugins` with retry and
never invents a zero count. Preview diagnostics are nonblocking candidate
warnings, while missing names in an explicit allow-list remain blocking and
are revalidated at Start.

The selection applies only to the session being created. After a successful
Start, the new-session form resets to the current defaults; a failed Start keeps
the explicit selection for correction. The created session stores its resolved
plugin directories, so resumes, forks, asides, direct children/subagents, and
durable delegates inherit the same plugin set. `--resume` and `--resume-last`
restore that frozen set rather than accepting a replacement; `--resume-with`
creates a new session and may choose a new set. Global enable/disable changes do
not mutate an existing session.

## Provider credentials

> Architecture reference:
> [`llm-providers.md`](llm-providers.md) (the registry, layers, instances,
> resolution) and
> [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
> (credentials, OAuth, and the hub launch/spawn model).

The hub manages `${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`
(chmod 600), keyed by **instance name**, not provider type. The file's
format is a small TOML document:

```toml
schema = 1

[providers.anthropic]
api_key = "sk-ant-..."

[providers.openai]
api_key = "sk-..."

[providers.openrouter]
api_key = "..."
```

A section name matches either an implicit instance's id (`anthropic`,
`openai`, `openrouter`, …) or a custom instance you defined in
`providers.toml`.

The hub UI (`/credentials`) or TUI (`/credentials`) writes this file via
the `evener/auth/apiKey/set` RPC. Process-env credentials (e.g.,
`ANTHROPIC_API_KEY` exported in the shell) still work as a fallback when no
file entry exists for the instance — matching the `hub.env` style for users
who prefer external secret management.

If `EVENER_PROVIDERS_CONFIG` points to a non-default `providers.toml`,
`credentials.toml` is relocated beside that file, unless
`EVENER_CREDENTIALS_CONFIG` names a different path. Otherwise it is beside
the default providers config under the XDG config root. Keep both files
private.

### OpenAI credential resolution

The platform API and the ChatGPT/Codex subscription are two separate
**instances**, not one instance with two credential sources: `openai` (an
API key, stored in `credentials.toml` like any other instance, or via
`OPENAI_API_KEY`) and `openai-codex` (OAuth only — sign in via
`evener/auth/login/start`; state stored per instance at
`${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth/openai-codex.json`).
`openai-codex` precedes `openai` in the default-instance ranking, so a
fresh sign-in becomes the default the same way a stored OAuth record used
to win — but by ranking between two instances, not by a precedence check
within one.

The two instances hit **different backends**: `openai-codex` routes to the
ChatGPT/Codex backend (`OPENAI_CODEX_BASE_URL`), while `openai` routes to
the standard OpenAI API backend (`OPENAI_BASE_URL`). They are not
interchangeable credentials for one endpoint, and signing in with
`evener openai login` no longer touches the `openai` instance at all. See
[`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
for the full resolution detail.

## Start the hub

Foreground run with logs:

```bash
set -o pipefail
umask 077
hub_config="${XDG_CONFIG_HOME:-$HOME/.config}/evener/hub.toml"
hub_state_root="${XDG_STATE_HOME:-$HOME/.local/state}/evener"
# If hub.toml sets hub_state_root, set hub_state_root above to that same value.
log_file="$hub_state_root/log/hub.log"
mkdir -p "$(dirname "$log_file")"
chmod 700 "$(dirname "$log_file")"
touch "$log_file"
chmod 600 "$log_file"
evener hub --config "$hub_config" 2>&1 | tee -a "$log_file"
```

The hub listens on `127.0.0.1:9180` unless `addr` in `hub.toml` says
otherwise, and logs the authorization URL described under Trust Boundary on
every start. That URL contains the bearer credential: protect supervisor
stdout/stderr and this log as secrets, and do not paste them into tickets or
chat.

If you manage credentials via environment variables rather than
`${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml`, source your env file
before starting:

```bash
set -a; source "${XDG_CONFIG_HOME:-$HOME/.config}/evener/hub.env"; set +a
```

This Bash recipe uses `pipefail` so a hub failure is not hidden by `tee`.
Production deployments should preferably execute `evener hub --config
"$hub_config"` directly under a supervisor and let it capture stdout/stderr.
The hub logs config errors, past-index rebuild errors, roster watch errors,
child-process launch diagnostics, and the auth URL there.

The TUI's `--log-file` option also captures the auto-started hub's full
authorization URL, including its bearer token. Run the TUI with a restrictive
`umask` and choose a private log path; do not place this log in a shared
directory or expose it through a supervisor's broadly readable logs.
On macOS, [`scripts/ops/deploy-hub.sh`](../scripts/ops/deploy-hub.sh) builds
while the old hub remains running, kickstarts its launchd job to replace it,
then verifies health; see
`scripts/ops/deploy-hub.sh --help`.

The hub acquires a `flock` on `hub.lock` in its state root, so one hub process runs
per `hub_state_root` — one per user under the default layout.

### Trace browser AppWire traffic

Use `--appwire-trace` to diagnose excessive browser WebSocket traffic. The flag
records every AppWire data frame on each browser `/rpc` connection:

```bash
trace_dir="$(mktemp -d)"
trace_file="$trace_dir/hub-appwire.jsonl"
evener hub --config "$hub_config" --appwire-trace "$trace_file"
```

The hub creates `trace_file` with mode `0600` and refuses to overwrite an
existing path. Omit the flag during normal operation; tracing is off by default.

Each JSONL record contains a UTC `timestamp`, a process-local `connection`
(`conn-N`), and an `event` (`open`, `frame`, or `close`). Frame records also
contain a hub-relative `direction` (`browser_to_hub` or `hub_to_browser`), the
exact `bytes` count, and the complete raw JSON `frame`. The trace covers AppWire
data frames, not WebSocket ping, pong, or close control frames. It excludes the
hub's separate connections to Evener daemons.

The raw frames can contain prompts, transcripts, paths, tool arguments, and
credentials entered through Settings. Keep the file private, inspect it before
sharing it, and never commit it. Stop the traced hub after reproducing the issue,
then analyze or share that single file.

For a compact first pass, list each frame's connection, direction, size, request
ID, and method:

```bash
jq -r '
  select(.event == "frame") as $record
  | ($record.frame | fromjson) as $frame
  | [$record.connection, $record.direction, $record.bytes,
     ($frame.id // "-"), ($frame.method // "response")]
  | @tsv
' "$trace_file"
```

To find the connections and message types producing the most traffic, aggregate
frame counts and bytes before inspecting individual payloads:

```bash
jq -s -r '
  [ .[]
    | select(.event == "frame")
    | . + {message: (.frame | fromjson)}
  ]
  | group_by([.connection, .direction, (.message.method // "response")])
  | map({
      connection: .[0].connection,
      direction: .[0].direction,
      method: (.[0].message.method // "response"),
      frames: length,
      bytes: (map(.bytes) | add)
    })
  | sort_by(-.bytes)
  | .[]
  | [.connection, .direction, .method, .frames, .bytes]
  | @tsv
' "$trace_file"
```

The columns are connection, direction, method (or `response`), frame count, and
total bytes, ordered by total bytes. Use the process-local connection ID to
correlate an `open` record, its frames, and the final `close` record.

## Browser and TUI

Browser: visit the authorization URL logged at startup. It sets the auth
cookie; after that, plain `http://127.0.0.1:9180` works in that browser.

TUI:

```bash
evener tui \
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

Other API routes need the auth token. Model enumeration is AppWire-only:
connect to `ws://127.0.0.1:9180/rpc`, authenticate with the token, send
`initialize`, then send this typed request (see
`docs/developing-evener/agentic-testing.md`, "Driving AppWire directly"):

```json
{"id":2,"method":"model/list","params":{"cwd":"/path/to/project"}}
```

The response's `result.data` contains the spawn-scoped descriptors;
`result.recent` is the optional global Recent group.

Manual verification:

1. Authorize the browser via the startup auth URL, then open `/new`.
2. Pick a working directory and spawn an Evener session.
3. Confirm the transcript streams live before refresh.
4. Refresh and confirm the transcript replays from saved state.
5. Open `evener tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub` and
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
- Do not blanket-prune `run_dir`, and never delete a live rendezvous file or
  log. The hub owns cleanup. For a specific suspected stale rendezvous file,
  inspect its recorded PID, verify it is the expected Evener daemon, and
  confirm that process is no longer alive (for example, `kill -0` fails; a PID
  can be reused). Re-check before removing only that file. Leave logs in place
  for diagnosis.
  Saved transcripts live under the state directories matched by `state_glob`.
- Keep `hub.toml`, env files, the auth token, run directories, and state
  directories private to the service user.
