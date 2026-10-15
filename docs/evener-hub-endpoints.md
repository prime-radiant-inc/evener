# Hub listeners and advertised origins

The hub listener and a client destination are different settings. `evener hub --addr` (or `-addr`) and top-level `addr` in `hub.toml` choose where the server binds. `EVENER_HUB_LISTEN_ADDR` overrides TOML. A client URL is an HTTP(S) origin and does not choose the server listener.

The server resolves the listener in this order: supplied `--addr`/`-addr` > present `EVENER_HUB_LISTEN_ADDR` > present top-level `addr` > `127.0.0.1:9180`. Presence matters: `EVENER_HUB_LISTEN_ADDR=` and `--addr=` fail validation rather than falling back. The listener is `host:port`; port `0` asks the operating system for an available port. Wildcard listeners such as `0.0.0.0:9180` are allowed. The configured address stays the configured address after binding; the bound address may differ if the port was zero.

For example:

```toml
addr = "0.0.0.0:9180"
public_url = "https://evener.example.com"
```

`public_url` is an optional **advertised origin**, not a TLS switch or a listener override. A reverse proxy can serve HTTPS while the hub binds local HTTP. Its accepted form is `http://host[:port]` or `https://host[:port]`, with an optional root slash, no credentials, path, query or fragment. Tokens stay in their separate credential fields. The configured public origin is currently used for pairing destinations; the startup-link publication change is separate.

For a one-off server override, use `evener hub --addr 127.0.0.1:0`. For a persistent environment override, set `EVENER_HUB_LISTEN_ADDR=127.0.0.1:9280` before starting the server. A supplied flag wins over that variable even if it is empty and invalid. The server still accepts both `--addr=value` and `-addr value`.

`mobile_base_url` has been removed; replace it with `public_url` in `hub.toml`. `EVENER_HUB_ADDR` has also been removed; for a server listener use `EVENER_HUB_LISTEN_ADDR`, and for a client destination use `EVENER_HUB_URL` when the client supports it. The server refuses a retired setting even if a new setting is present. No config file or running service is rewritten automatically. Avoid putting credentials in these settings or in copied diagnostics.

## Inspecting the actual listener

`evener hub endpoint --json [--config PATH] [--addr HOST:PORT]` inspects the selected configuration without starting a hub, creating a lock or reading a credential. `--addr` affects the **configured** listener in this inspection; it does not relocate a running hub. The one-object JSON result has `schema_version: 1`, `state` (`stopped`, `starting`, `running`, `unavailable`), `config_path`, `config_exists`, `hub_state_root`, `configured_listen_addr`, `listen_source` (`flag`, `env`, `config`, `default`), and optional `public_url`. A non-running result has no `running` record and may give a machine-readable `reason`. A held lock without a published record is `starting`; a held lock with invalid or unreachable metadata is `unavailable`. A free lock is `stopped` even when a crashed hub left an old record. An unbound `:0` has no discoverable assigned port.

The hub publishes `<hub_state_root>/hub-endpoint.json` with mode `0600` after binding the listener and constructing the HTTP handler, immediately before serving. The file contains its `boot_id`, PID, health start time, selected config path and state root, requested listener and source, **actual** bound address, local origin, optional literal-loopback origin, and optional advertised public origin. It contains no token. The hub keeps `hub.lock` on the same inode while it serves, replaces stale endpoint metadata only while holding that lock, and retires only its own published generation before releasing the lock. An edit to `hub.toml` can change the report's configured listener while `running` still describes the original live listener. Changing `hub_state_root` selects a different owner; it does not search other roots.

Discovery reports `running` only after checking the held lock, a bounded private same-user record, a numeric socket assigned to this machine, and token-free `/api/health` on that socket. Health must identify the same `boot_id`, start time and bound address, and the record generation must remain unchanged. `public_url` is never a probe target. A specific interface remains the local origin; wildcard IPv4 and IPv6 use their same-family loopback address. A scoped IPv6 bind has no portable local origin and is reported unavailable for automatic discovery rather than being rewritten. This protects client token-file eligibility from a stopped hub's stale record or a different server reusing its port. A client that will send an automatically selected token must repeat the token-free check on **each authenticated connection itself**, drain the health response, then reuse that exact open connection for its authenticated request. A separate probe followed by a new unchecked connection is not sufficient.
