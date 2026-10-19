# Remote hub operations

Run the hub near the repositories and tools it manages, then connect your browser through a protected connection. This guide covers both a browser reaching one remote hub and a hub managing additional execution hosts. Credentials, paths, and processes belong to the execution host—not the browser's machine.

## Choose the topology

```mermaid
flowchart LR
  Browser[Your browser] --> Tunnel[SSH tunnel or protected TLS edge]
  Tunnel --> Hub[Remote hub]
  Hub --> Local[Sessions on hub host]
  Hub --> SSH[SSH connection to managed host]
  SSH --> Remote[Host hub and session daemons]
```

For one remote machine, start with an SSH tunnel to its loopback hub. For several machines, configure managed hosts in the hub. A tunnel forwards a port; it does not copy your repository, provider credentials, or laptop environment.

## Connect to one remote hub

Prerequisites: SSH access, an installed Evener build on the remote host, a repository there, and a provider connection available to the remote user. Use the [installation guide](getting-started.md) rather than assuming a source checkout is an installed runtime.

On the remote host, start the hub on loopback:

```sh
evener hub --addr 127.0.0.1:9180
```

The startup authorization URL contains a bearer credential. Keep the terminal and supervisor logs private. Do not copy that URL into a ticket or chat.

On your laptop, open the tunnel:

```sh
ssh -N -L 9180:127.0.0.1:9180 hubbox.example
```

Replace `hubbox.example` with your SSH host. If local port 9180 is occupied, use a different **local** port, such as `19180:127.0.0.1:9180`. Visit the remote hub's authorization URL through `http://127.0.0.1:9180` (or your chosen local port), preserving its private token path.

Check transport without displaying credentials:

```sh
curl -fsS http://127.0.0.1:9180/api/health
```

Then open New session, select the remote working directory, and [connect a provider](connecting-a-provider.md) on that host. Confirm the host, directory, and model before starting. A healthy HTTP endpoint is not proof of provider access.

## Trust boundary

The hub uses a long-lived capability token stored at `<hub_state_root>/auth-token`. New token files are mode `0600`; an existing file's mode is not automatically repaired. `/auth/<token>`, `/api/health`, `/api/device-pairing`, and PWA icons are public exceptions; other routes require authorization. [Device pairing](device-pairing.md) uses an invitation and requires approval before releasing credentials, rather than requiring the hub token or cookie. The cookie is HTTP-only and SameSite=Lax. This is full hub access, not per-user roles.

Hub serves plain HTTP. Do not expose it directly to an untrusted network. A private VPN also needs access controls and a firewall. If you deliberately bind to `0.0.0.0:9180` or `[::]:9180`, every permitted interface can accept connections; set those controls **before** changing the bind address.

For a TLS reverse proxy, use its `https://` origin in the authorization URL. Hub's cookie uses `Secure=false` to permit direct loopback HTTP; configure the proxy to add `Secure` before offering access over an untrusted network. Protect WebSocket connections as well as ordinary HTTP. Never put the capability in a public proxy access log.

## Configure the service host

Use the same service user for the hub, repositories, provider configuration, and diagnostics. Set XDG paths in its supervisor environment before starting the hub. A GUI login shell and a system service need not have the same environment.

This complete `/etc/evener/hub.toml` example assumes the service user owns private directories beneath `/var/lib/evener`:

```toml
addr = "127.0.0.1:9180"
hub_state_root = "/var/lib/evener/state/evener"
run_dir = "/var/lib/evener/state/evener/run"
state_glob = "/var/lib/evener/state/evener/projects/*"
past_index_db = "/var/lib/evener/state/evener/index.db"
spawn_timeout = "30s"
past_index_rebuild_interval = "60s"
past_results_per_page = 50
api_log = false
```

Start it through your supervisor with `XDG_STATE_HOME=/var/lib/evener/state` and an appropriate private `XDG_CONFIG_HOME`. The paths in TOML are literal; TOML does not expand `$HOME`.

```sh
evener hub --config /etc/evener/hub.toml --evener /usr/local/bin/evener
```

Adjust the executable path to the installed build. `--addr` overrides the configured listener; `--evener` selects the runtime used for local daemons. Use [Configuration](configuration.md) for launch defaults; do not create a second credential hierarchy in `hub.toml`.

`run_dir` contains rendezvous records and logs, including daemon access tokens. Keep it private. `state_glob` must match the actual durable session directories. An invocation-level `EVENER_STATE_DIR` override can put sessions outside the usual project layout; changing only the index glob does not move those sessions.

## Managed execution hosts and deployment

A managed host needs SSH connectivity, the required tools, and repository/configuration paths on that host. Use host-scoped settings for provider connections and launch defaults. Check the selected host before saving a credential or trusting a repository launch file.

**Attaching can install software.** By default, an Evener hub offers its own executable to a same-platform host and may replace the host's Evener build. It then verifies the running build before attachment. Do not treat “connect host” as a read-only operation.

Add a host through the hub's host settings, or merge a `[[hosts]]` entry into the controller's `hub.toml`. This complete TOML example names one SSH target and an advisory project root:

```toml
[[hosts]]
name = "buildbox"
ssh = "buildbox.example"
user = "developer"
roots = ["/srv/projects"]
```

Replace these example values with paths and an account on the remote machine. Optional `evener_path`, `config_path`, and `addr` identify a nondefault remote executable, hub config, and listener. `key_path` selects an SSH private-key file; it is a path, never the key contents. Preserve machine-managed host records when editing the file. The exact fields are defined in [HostConfig](../cmd/evener-hub/config.go).

| Hub startup option | Effect |
| --- | --- |
| `--deploy-binary /absolute/path/to/evener` | Use a prebuilt artifact; target and Evener identity are checked |
| `--build-source /absolute/path/to/checkout` | Cross-compile from an Evener source tree on the controller |
| `--no-deploy` | Disable push and installer deployment paths |

`--no-deploy` wins over the other options; `--deploy-binary` wins over `--build-source`. A cross-platform host cannot run the controller's own executable, so the hub may use a published release/snapshot installer instead. Development, dirty, or unstamped builds cannot always be pinned to a published artifact. A protocol-compatible existing host can remain usable with deployment disabled; a host that needs provisioning cannot.

The [hub deployment reference](evener-hub.md#deploying-to-remote-hosts) retains the exact artifact, dirty-build, atomic replacement, identity, and retry rules. Review it before using an automatic deployment path. Do not repeatedly attach after an interrupted deployment until you have checked the remote health/build: the replacement may have completed even if your client lost the response.

## Restart and upgrade safely

Record the actual supervisor job, executable, flags, and config paths when provisioning. Keep environment sources private. Restart that exact service rather than killing every process named `evener`; session daemons can share the same executable name.

Stopping the hub does not stop every session daemon. Existing daemons keep their running build after the executable on disk is replaced. Finish or stop them deliberately if an upgrade requires all sessions to use the new build. The hub lock prevents two hubs sharing one state root; a lock error is not a reason to delete the lock file.

After restart:

1. Check `/api/health` through the same route the browser uses.
2. Compare its version and startup time with the intended deployment.
3. Reconnect to an existing session and confirm its transcript is visible.
4. Start a small, explicitly authorized new task only when provider access needs a live check.

The first three checks do not validate model generation. See [upgrades and migration](evener-hub.md#upgrading-and-migrating-safely) and [storage and backups](security.md#storage-and-backups). A backup of the controller does not automatically back up managed hosts.

## Recover a failed connection

| Failure | Next check |
| --- | --- |
| Tunnel refuses connection | Remote hub listener, SSH forwarding, local port collision |
| Browser unauthorized | Correct hub's private capability and origin |
| Host unavailable | SSH authentication and connectivity before provider debugging |
| Build mismatch | Deployment source, target platform, stamped build, `--no-deploy` policy |
| Model absent only remotely | Credentials and endpoint variables on the selected host |
| Settings saved on wrong machine | Host selector and file paths, not browser storage |
| Session disappeared after restart | Effective `run_dir`, `state_glob`, XDG paths, and daemon health |

Keep logs private: startup output contains authorization URLs. For a report, include sanitized host labels, build identities, timestamps, and the exact failure—not tokens, full environments, or whole rendezvous records. [Troubleshooting](troubleshooting.md) gives read-only diagnostic commands.
