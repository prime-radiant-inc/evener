# Evener Hub Remote Operations

This runbook covers running `evener hub` on a host that is not the laptop/browser
host. It describes the current code, not a future deployment system.

## Trust Boundary

`evener hub` authenticates its web edge with a long-lived random capability
token. On startup, Hub loads or creates `auth-token` under `hub_state_root`
(`~/.local/state/evener/auth-token` by default) and prints a browser authorization URL. A new
token file is created with mode `0600`; Hub does not repair the mode of an
existing file. Visiting that URL sets an HTTP-only, SameSite=Lax cookie;
scripted clients may instead send the token as `Authorization: Bearer <token>`.
Only the `/auth/<token>` bootstrap, `/api/health`, and the PWA icons are
available without the token.

The capability token is the Hub's user-authentication boundary. Hub-spawned
daemons separately use the current Hub process's bearer token for Hub-to-daemon
requests.
Do not publish the capability URL, token file, or bearer token.

Hub serves plain HTTP itself. A direct connection over an untrusted network can
expose the capability token or session cookie to a network observer, so do not
expose Hub directly to the public Internet without authenticated encryption.

Use one of these access patterns:

- SSH tunnel to the Hub host, keeping Hub bound to loopback.
- VPN/private network, such as Tailscale, plus a host firewall that limits who
  can reach the Hub.
- Authenticated TLS reverse proxy in front of Hub.

To accept both loopback and private-network connections, bind Hub to
`0.0.0.0:9180` (or `[::]:9180`). The startup log replaces a wildcard address in
the printed authorization URL with the machine hostname. If that hostname is
not resolvable from the client, replace only the hostname in the URL with the
machine's LAN or VPN address; preserve the token path segment exactly.

When TLS terminates at a reverse proxy, construct the external authorization
URL with the proxy's `https://` origin instead of the `http://` URL Hub prints.
Hub emits its cookie with `Secure=false` because it also supports direct HTTP;
configure the proxy to add the `Secure` attribute to `Set-Cookie` before using
the Hub over an untrusted network.

## Required Binaries

Build and install matching binaries on the Hub host:

```bash
make build
make build-hub
make build
```

Start Hub with the Evener binary it should spawn:

```bash
evener hub --config /etc/evener/hub.toml --evener /usr/local/bin/evener
```

For loopback plus LAN/VPN access:

```bash
evener hub --addr 0.0.0.0:9180 --evener /usr/local/bin/evener
```

Authorize each browser once with the startup log's `/auth/<token>` URL. For a
remote TUI, pass the same capability out of band:

```bash
EVENER_HUB_AUTH_TOKEN='<token>' \
  evener tui --hub-addr http://hubbox.example:9180 --no-auto-start-hub
```

The TUI otherwise looks for the local machine's `~/.local/state/evener/auth-token`, which is
usually not the remote Hub's token.

### Ad hoc macOS background launch

For a user-session job that survives the invoking shell but not logout or
reboot, `launchctl submit` can keep Hub running without a plist:

```bash
launchctl submit \
  -l com.example.evener-hub \
  -p /absolute/path/to/evener \
  -o /absolute/path/to/evener-hub.log \
  -e /absolute/path/to/evener-hub.log \
  -- /absolute/path/to/evener hub \
  -addr 0.0.0.0:9180 \
  -evener /absolute/path/to/evener
```

The executable appears both after `-p` and as the first item after `--` because
that first item becomes the submitted process's `argv[0]`. Omitting it shifts
the first Hub flag into `argv[0]` and causes the remaining flag value to be
reported as an unexpected positional argument.

Stop this ad hoc job with:

```bash
launchctl remove com.example.evener-hub
```

Current flags verified from source:

- `evener hub --config <path>` loads Hub TOML config.
- `evener hub --addr <host:port>` overrides `addr`.
- `evener hub --evener <path>` selects the `evener serve` binary Hub launches.
- `evener launch-check --protocol evener-appwire-v1 --model <provider/model> --json`
  validates the binary/protocol/model contract before spawn.
- `evener tui --hub-addr <url-or-host-port>` connects the TUI to a Hub.
- `evener tui --auth-token <token>` overrides `EVENER_HUB_AUTH_TOKEN` and the local
  token file.
- `evener tui --no-auto-start-hub` prevents local auto-start, which is usually
  what you want when targeting a remote Hub.

## Hub Config

Example `/etc/evener/hub.toml`:

```toml
addr = "127.0.0.1:9180"
run_dir = "/var/lib/evener/run"
state_glob = "/var/lib/evener/state/evener/projects/*"
past_index_db = "/var/lib/evener/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50

[evener_launch]
sse_ring_size = 4096

[evener_launch.env]
XDG_STATE_HOME = "/var/lib/evener/state"
```

`addr` is the listen address. When it is a wildcard address, Hub substitutes
the machine hostname in the authorization URL printed at startup.

`run_dir` holds daemon rendezvous files. Hub watches this directory and probes
the daemons it finds there. It is runtime state, not the durable transcript
store. Hub-spawned daemons record their internal `hub_token` there so a
restarted Hub can keep talking to already-running daemons. Keep the directory
private to the service user; current `evener serve` writes it as `0700` with
`0600` files.

`state_glob` tells Hub where to find saved sessions. Each match must be a state
directory containing `sessions/*.meta.json` and transcript JSONL files. When
`state_glob` is omitted, Hub uses `$XDG_STATE_HOME/evener/projects/*`, falling
back to `~/.local/state/evener/projects/*`.

Each directory under `projects/` is keyed by a readable canonical project ID,
not a legacy fixed-width hash. The main checkout and its linked worktrees share
that bucket; a distinct clone has a distinct bucket. The identifier migration
is a clean break: Hub leaves inert old state untouched for manual removal.

`past_index_db` is the SQLite FTS search index. When omitted, Hub uses
`~/.local/state/evener/index.db`. The in-memory past index remains the source of truth; if the
SQLite index cannot be opened or rebuilt, Hub falls back to substring search
over the loaded metadata.

`evener_launch.env` overrides the environment inherited by spawned `evener serve`
children. Hub also sets `EVENER_HUB_SPAWNED=1`, `EVENER_RUN_DIR`,
`EVENER_STATE_DIR`, and a generated `EVENER_HUB_TOKEN` for each child. Do not set
`EVENER_HUB_TOKEN` in config; Hub-owned launches replace it with the generated
per-Hub token.
Launch model choices come from that same Evener launch harness contract
(`evener launch-check --models`) rather than a static model roster in
`hub.toml`.

`evener_launch.sse_ring_size` passes `--sse-ring-size` to Hub-owned Evener daemons.
Increase it when long sessions produce enough token delta events that late SSE
or AppWire clients need a deeper replay window than the daemon default of 1000
events.

Prefer setting `XDG_STATE_HOME` for service deployments so Evener keeps its
per-project state layout:

```toml
state_glob = "/var/lib/evener/state/evener/projects/*"

[evener_launch.env]
XDG_STATE_HOME = "/var/lib/evener/state"
```

Only set `EVENER_STATE_DIR` when you deliberately want every spawned daemon to use
one exact state directory. If you do that, `state_glob` must match that exact
directory, otherwise Hub can spawn sessions that it cannot later find in the
past-session index.

## Provider Credentials

Hub validates configured provider/model choices before spawning Evener. Credential
lookup uses the spawned child environment: the Hub process environment first,
then `evener_launch.env` overrides.

Supported current credential checks:

- OpenAI: `OPENAI_API_KEY` or Evener OpenAI login state in the user state dir.
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

Evener-owned OpenAI OAuth is user-scoped by default. With the service layout above,
`evener openai login` stores auth under `/var/lib/evener/state/evener/auth/openai.json`;
spawned sessions still store transcript/runtime state under per-project
directories.

## Browser And TUI Access

For an SSH tunnel:

```bash
ssh -N -L 9180:127.0.0.1:9180 hubbox.example
```

Then open `http://127.0.0.1:9180` locally.

For TUI access to a remote Hub:

```bash
evener tui --hub-addr http://127.0.0.1:9180 --no-auto-start-hub
```

If you connect directly over a private network:

```bash
evener tui --hub-addr http://hubbox.example:9180 --no-auto-start-hub
```

## Health Checks And Manual Verification

Basic health:

```bash
curl -fsS http://127.0.0.1:9180/api/health
```

Manual spawn/resume verification:

1. Open `/new` in the browser.
2. Spawn a Evener session using a configured provider/model.
3. Confirm the transcript updates live before refresh.
4. Refresh the browser and confirm the transcript replays from the past index.
5. Shut down the session from Hub.
6. Send another message to the ended session and confirm Hub resumes it.
7. Open `evener tui --hub-addr ... --no-auto-start-hub` and confirm the same
   session appears with source label `evener`.

## Logs, Restarts, And Backups

Run Hub under a supervisor and capture stdout/stderr. Hub logs config errors,
past-index rebuild errors, roster watch errors, and child-process launch
diagnostics there.

Hub uses `~/.local/state/evener/hub.lock` for one Hub process per host user. A second Hub for
the same user exits instead of sharing the same run directory.

Stopping Hub does not make saved transcripts disappear. Saved sessions live
under the state directories matched by `state_glob`; back those up. The
`run_dir` rendezvous files are runtime discovery data and can be recreated by
running daemons or pruned after failed probes.

Hub-spawned Evener daemons are independent `evener serve` processes; they continue
until the session is shut down or the process exits.

### Restarting an ad hoc Hub

If Hub is running under a supervisor you configured, restart it that way and
skip this section. This is for a Hub that is only running — started ad hoc
(see [Ad hoc macOS background launch](#ad-hoc-macos-background-launch)) or
inherited from someone else's session — with no launch command written down
anywhere.

If Hub was submitted to launchd as its own job, `scripts/ops/deploy-hub.sh`
automates the rebuild-and-restart sequence below for the current worktree: it
finds the job's label, confirms its binary matches this checkout, builds
`evener hub` (leaving the old process running untouched if the build fails),
`kickstart -k`s only that job, and polls `/api/health` to confirm the restart
actually took (an interrupted `kickstart` can report failure even when the
restart succeeded, so the health probe — not `kickstart`'s exit status — is
the real check). Run `scripts/ops/deploy-hub.sh --help` for details. The rest of
this section is the manual version of that recipe, and is also what a bare
backgrounded process (no launchd job at all) needs.

First rule out a forgotten `launchctl` job (a `launchctl submit` job, unlike a
bare backgrounded process, is still discoverable even with no plist on disk):

```bash
launchctl list | grep evener-hub
```

A hit gives you the full recipe directly, no further archaeology needed:

```bash
launchctl print "gui/$(id -u)/<label>"
```

Its `program`, `arguments`, `stdout path`, and `stderr path` fields are the
exact launch command and log destination. Stop it with `launchctl remove
<label>` and resubmit with the same values, edited as needed.

If that comes up empty, it's a bare process. Recover it in four steps:

**1. Find the pid.**

```bash
pgrep -x evener
```

Hub takes an exclusive `flock` on `hub.lock` under `hub_state_root` at startup
(`~/.local/state/evener/hub.lock` by default; see [Hub
Config](#hub-config)) — so exactly one match is the healthy case; more than
one is itself a finding worth stopping to investigate rather than restarting
past. If nothing turns up but something is answering on the expected port,
cross-check by port instead (substitute the port if `--addr` didn't use the
default):

```bash
lsof -ti :9180 -sTCP:LISTEN
```

**2. Recover the exact flags.**

```bash
ps -p <pid> -ww -o pid,ppid,etime,command
```

The repeated `-ww` forces an untruncated command line regardless of terminal
width. This recovers `-config`, `-addr`, and `-evener` — the three flags Hub
parses (see [Required Binaries](#required-binaries)) — exactly as they were
passed.

It does **not** recover environment variables the process was started with (a
`EVENER_PROVIDERS_CONFIG` or `XDG_STATE_HOME` override, say). Don't reach for a
Linux-style `ps eww`/`ps -E` to fill that gap: verified on macOS 26.5.1
(Darwin 25.5.0) that neither flag surfaces another process's environment on
this platform, even a same-user one. Check the recovered `hub.toml`'s
`[evener_launch.env]` table instead (see [Hub Config](#hub-config)); an
override that is in neither place is not recoverable by inspection.

**3. Find where its log is going.**

```bash
lsof -p <pid> -a -d 1,2
```

If fd 1 (stdout) and fd 2 (stderr) both resolve to the same regular file,
that's the log to preserve in step 4. If they resolve to a tty instead
(nothing was redirected at launch), there is no persisted log to preserve.

**4. Stop it, rebuild if needed, and relaunch preserving the log.**

Plain `kill <pid>` (SIGTERM) triggers Hub's graceful shutdown — stopping active
connections and the HTTP server, for up to 5 seconds — before it releases the
lock; with nothing actively streaming, the lock is in practice free again within
milliseconds. `kill -9 <pid>` skips the drain and
frees the lock the instant the process is torn down. Either way, a relaunch
that lands in the gap fails with:

```
[hub] flock /path/to/.local/state/evener/hub.lock: resource temporarily unavailable (another evener hub may already be running; a disposable hub needs its own HOME)
```

That's the death overlap, not corruption or a stuck lock; wait a moment and
retry.

If you're also deploying new code, rebuild now (`make build` or `make
build-hub`). Rebuilding replaces the binary **file**; a Hub already running
keeps executing the copy it already has open regardless, with nothing
indicating the drift. `/api/health`'s `version` field only changes once a
fresh process is actually running it — compare it against `git rev-parse
--short HEAD` in the checkout you built from (plus a `-dirty` suffix if the
tree had uncommitted changes at build time) to tell whether a given restart
picked up the rebuild.

Relaunch with the flags from step 2, appending (`>>`, not `>`) to the log
from step 3:

```bash
nohup /path/to/evener hub -addr 0.0.0.0:9180 -evener /path/to/evener \
  >>/path/to/evener-hub.log 2>&1 &
```

Confirm: `pgrep -x evener` shows a new pid, `/api/health`'s `started_at` is
after the restart (and its `version` matches if you rebuilt), and the log
file is still growing.

## Kata Tracker

Kata is not part of Hub runtime configuration. This repo currently has no
Hub-specific `KATA_SERVER` or `.kata.local.toml` requirement. For tracker work,
run `kata quickstart` in the workspace and follow its project binding.
