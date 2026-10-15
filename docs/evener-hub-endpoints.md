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
