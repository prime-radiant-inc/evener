# Hub listeners and advertised origins

The hub listener and a client destination are different settings. `evener hub --addr` (or `-addr`) and top-level `addr` in `hub.toml` choose where the server binds. `EVENER_HUB_LISTEN_ADDR` overrides TOML. A client URL is an HTTP(S) origin and does not choose the server listener.

The server resolves the listener in this order: supplied `--addr`/`-addr` > present `EVENER_HUB_LISTEN_ADDR` > present top-level `addr` > `127.0.0.1:9180`. Presence matters: `EVENER_HUB_LISTEN_ADDR=` and `--addr=` fail validation rather than falling back. The listener is `host:port`; port `0` asks the operating system for an available port. Wildcard listeners such as `0.0.0.0:9180` are allowed. The configured address stays the configured address after binding; the bound address may differ if the port was zero.

For example:

```toml
addr = "0.0.0.0:9180"
public_url = "https://evener.example.com"
```

`public_url` is an optional **advertised origin**, not a TLS switch or a listener override. A reverse proxy can serve HTTPS while the hub binds local HTTP. Its accepted form is `http://host[:port]` or `https://host[:port]`, with an optional root slash, no credentials, path, query or fragment. Tokens stay in their separate credential fields. The hub publishes this origin in startup output, default device invitations and browser login links; an explicit invitation origin takes precedence. Without it, browser login uses the actual listener's advertised-host fallback and pairing keeps its reachable-origin discovery. Unsafe or unreachable invitation origins fail invitation generation without disabling direct clients.

For a one-off server override, use `evener hub --addr 127.0.0.1:0`. For a persistent environment override, set `EVENER_HUB_LISTEN_ADDR=127.0.0.1:9280` before starting the server. A supplied flag wins over that variable even if it is empty and invalid. The server still accepts both `--addr=value` and `-addr value`.

`mobile_base_url` has been removed; replace it with `public_url` in `hub.toml`. `EVENER_HUB_ADDR` has also been removed; for a server listener use `EVENER_HUB_LISTEN_ADDR`, and for a client destination use `EVENER_HUB_URL` when the client supports it. The server refuses a retired setting even if a new setting is present. No config file or running service is rewritten automatically. Avoid putting credentials in these settings or in copied diagnostics.

## Inspecting the actual listener

`evener hub endpoint --json [--config PATH] [--addr HOST:PORT]` inspects the selected configuration without starting a hub, creating a lock or reading a credential. `--addr` affects the **configured** listener in this inspection; it does not relocate a running hub. The one-object JSON result has `schema_version: 1`, `state` (`stopped`, `starting`, `running`, `unavailable`), `config_path`, `config_exists`, `hub_state_root`, `configured_listen_addr`, `listen_source` (`flag`, `env`, `config`, `default`), and optional `public_url`. A non-running result has no `running` record and may give a machine-readable `reason`. A held lock without a published record is `starting`; a held lock with invalid or unreachable metadata is `unavailable`. A free lock is `stopped` even when a crashed hub left an old record. An unbound `:0` has no discoverable assigned port.

The hub publishes `<hub_state_root>/hub-endpoint.json` with mode `0600` after binding the listener and constructing the HTTP handler, immediately before serving. The file contains its `boot_id`, PID, health start time, selected config path and state root, requested listener and source, **actual** bound address, local origin, optional literal-loopback origin, and optional advertised public origin. It contains no token. The hub keeps `hub.lock` on the same inode while it serves, replaces stale endpoint metadata only while holding that lock, and retires only its own published generation before releasing the lock. An edit to `hub.toml` can change the report's configured listener while `running` still describes the original live listener. Changing `hub_state_root` selects a different owner; it does not search other roots.

The selected hub root must use local storage. Best-effort OS classification
refuses recognized nonlocal storage before credentials or mutation, including
for endpoint inspection. Refusal exits with an error, not a `stopped` result or
bootstrap authorization. A missing admitted root stays missing on inspection.
Linux ownership combines exclusive flock with an OFD marker on the same
descriptor, while discovery uses a non-owning `F_GETLK` query. Darwin queries
its flock without the Linux marker. Coordinate upgrades of all owners and
inspectors, do not mix flock-only Linux owners with the new query. See
[local state and manual relocation](evener-hub-state.md) for preservation,
recovery and the limits of detection and platform evidence.

On startup, human diagnostics identify the configured listener and source, actual bound listener, advertised connection/public origin and locally usable origin. A credential-bearing browser link goes only to the actual stderr descriptor when it is a terminal. Files, pipes and service logs receive the plain origin and a retrieval hint, not the token. The hub never opens another terminal to bypass redirection.

`evener hub login-url [--config PATH]` deliberately writes one browser login link and a newline to stdout, including if you redirect it. Treat that link as a **password**: anyone with it can log in. The command selects a verified running instance, reads its existing `<hub_state_root>/auth-token`, and uses that instance's published `public_url` or actual bound address; edited but un-restarted TOML cannot change the link. It does not start a hub, make a token, open a browser or edit config. If the hub is stopped, unavailable or its token is unreadable, it returns an error without a link.

Discovery reports `running` only after checking the held lock, a bounded private same-user record, a numeric socket assigned to this machine, and token-free `/api/health` on that socket. The health check verifies that the answering process publishes the recorded `boot_id`, start time and bound address on the exact numeric socket recorded for this instance, on this machine. It does not prove that the answering process holds the bearer token. The record generation must remain unchanged. `public_url` is never a probe target. A specific interface remains the local origin; wildcard IPv4 and IPv6 use their same-family loopback address. A scoped IPv6 bind has no portable local origin and is reported unavailable for automatic discovery rather than being rewritten. A client that will send an automatically selected token must repeat the token-free check on **each authenticated connection itself**, drain the health response, then reuse that exact open connection for its authenticated request. A separate probe followed by a new unchecked connection is not sufficient.

Another local account that can bind the hub's port after it stops and replay a captured `/api/health` response can receive a bearer from an automatic local client. Run the hub only on a machine whose local accounts are trusted to bind its port. The SSH bridge performs the same identity check on the remote host and does not remove this limit. This limitation is accepted for this cleanup.

## SSH attachment

`evener hub attach --stdio [--config PATH]` discovers the selected running hub on the host. It reads that instance's existing token only after live discovery and uses its literal-loopback HTTP origin for the AppWire bridge. It does not start a hub. `--hub-url ORIGIN` is an optional explicit destination that must normalize to the selected instance's loopback origin. An empty or mismatched URL fails before token selection. The former attach `--addr` flag is rejected, while the server's `--addr` remains supported.

The bridge bypasses proxies, rejects redirects and checks the published instance identity on the actual WebSocket connection with token-free health before sending the bearer, with the same token-possession limitation above. A non-loopback-only listener cannot be attached through this bridge, even if direct clients can reach it. Neither `public_url` nor a bootstrap listener override supplies the bridge destination.

The SSH manager probes `launch-check --json` without `--protocol` and requires `hub_endpoint_schema: 1` independently of AppWire compatibility. An installed binary without supported endpoint capability requires manual migration before automatic install or restart. A verified absent executable may still use the authorized fresh-install path. Transport and cancellation errors are not migration or absence.

Ensure, automatic deploy, explicit deploy and restart resolve the selected executable/config and state root. Config-only host registrations need no paired `addr`. Presence comes from selected report state and independent process/service ownership, not a guessed default port. Before an install writes the executable, the manager authorizes and retains the predecessor's capability and launch intent. A new on-disk binary cannot prove the old running image capable. Readiness polls resolve fresh reports and verify the successor's PID/birth, boot, build and service association before attachment.

`[[hosts]].addr` and the internal `HubListenAddr` option are listener overrides only for an explicitly authorized fresh ad-hoc bootstrap. The per-host value wins. An existing selected service keeps its launch definition, and intentional restart uses current TOML unless retained flags or environment override it. Port zero may assign a new destination on each start. Failed starts and stopped replacements retain selected intent for revalidation and retry in the manager's memory, not as durable operation storage. An unrelated listener neither authorizes nor blocks the selected lifecycle by port alone.

The [disposable SSH lifecycle check](evener-hub-remote-operations.md#disposable-ssh-lifecycle-check)
covers default and explicit config selection, custom state roots, wildcard
listeners and a forced port-zero successor-port change. It independently checks
the selected process/socket and post-restart authenticated traffic while an
unrelated default-port hub remains unchanged. This is actual Alpine SSH/ad-hoc
Linux coverage, not systemd or launchd qualification.

## Worktree launchd deployment

`scripts/ops/deploy-hub.sh [--label LABEL] [--config PATH] [--timeout N]` builds
and replaces one existing launchd-managed worktree hub. Its former `--addr`
health override is rejected. The optional config must agree with the selected
service, including default-path selection under that service's environment.
Node/npm are existing build prerequisites; no jq or Python runtime is needed
by the deployment script.

Selection binds the launchd definition, effective user, PID/birth, argv, working
directory and config/state root before build, then revalidates before restart.
Only HOME, XDG_CONFIG_HOME, XDG_STATE_HOME and EVENER_HUB_LISTEN_ADDR are carried
into endpoint inspection. Caller overrides do not redirect it. The executable
validates listener syntax and supplies live readiness through
`hub endpoint --json`; the script neither parses a second URL grammar nor
probes a guessed/default/public URL. Success requires the selected successor's
new generation, boot, expected version/backend SHA and original root.
Readiness re-queries reports, so wildcard/IPv6 and fresh port-zero assignments
do not become persisted launch settings. Current TOML listener changes remain
intentional inputs, subject to unchanged config/root selection.

An unresponsive or damaged selected service can recover with independently
corroborated root and old-image capability. A replaced disk pathname alone
cannot supply that evidence. A stopped selected job can start with a stopped
report and capable installed image. Unknown ownership/capability refuses
without build/restart; unsupported images require manual migration. An
interrupted kickstart is successful only if successor verification succeeds.
See [the operations runbook](evener-hub-remote-operations.md#worktree-launchd-deployment)
for bounds, recovery evidence and failure-state reporting. Build failures retain
bounded stdout/stderr in a named owner-only temporary file, never in console
output; remove its private directory after inspection. Successful builds remove
it automatically. Treat the file as potentially secret-bearing. The script's
harness uses disposable command fixtures, not actual macOS or SSH deployment.
