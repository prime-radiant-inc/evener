# Agent Instructions

## Testing

Before adding or changing tests, read `docs/testing.md`.

Default tests must be deterministic. Do not make `make test` or
`go test ./...` depend on provider credentials, network access, quota, current
model behavior, or ambient developer machine state.

Use this boundary:

- Serf plumbing: use a scripted provider at the LLM boundary and exercise real
  Serf code below it. Examples: CLI wiring, appwire RPC, daemon queues, session
  loops, tool execution, transcript writes, event emission, goal continuation
  routing, hook dispatch, and prompt composition.
- Model behavior or provider API behavior: keep it live, but require explicit
  opt-in such as `SERF_LIVE_TESTS=1` or `SERF_*_E2E=1` in addition to the
  provider credential.

A provider API key by itself must never cause default tests to issue live
requests.
