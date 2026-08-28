# model-switch-providers-live: retired daemon-REST scenario

This live scenario previously directed operators to the daemon's retired
`/input`, `/model`, and `/shutdown` HTTP routes. It is retired rather than
rewritten as a shell card because the supported mutation boundary is typed
AppWire: `turn/start`, `thread/model/set`, and `thread/shutdown` over `/rpc`.

The meaningful behavior remains covered at that boundary:

- `server/model_set_test.go` verifies model-switch validation, the model hook,
  and the immediate `thread/read` projection.
- `server/appwire_server_test.go` exercises typed turn, model, and shutdown
  handlers.
- `cmd/evener/serve_model_switch_test.go` drives a real daemon through
  AppWire and verifies a provider switch affects subsequent turns.

For a paid, provider-specific manual run, use an AppWire-capable client against
the daemon's `/rpc` endpoint: start each leg with `turn/start`, wait for the
typed thread state to become idle, send `thread/model/set`, and terminate with
`thread/shutdown`. Do not use the removed HTTP control routes.
