# Agent Instructions

## Testing

Before adding or changing tests, read `docs/testing.md`.

Default tests must be deterministic. Do not make `make test` or
`go test ./...` depend on provider credentials, network access, quota, current
model behavior, or ambient developer machine state.

Use this boundary:

- Evener plumbing: use a scripted provider at the LLM boundary and exercise real
  Evener code below it. Examples: CLI wiring, appwire RPC, daemon queues, session
  loops, tool execution, transcript writes, event emission, goal continuation
  routing, hook dispatch, and prompt composition.
- Model behavior or provider API behavior: keep it live, but require explicit
  opt-in such as `SERF_LIVE_TESTS=1` or `SERF_*_E2E=1` in addition to the
  provider credential.

A provider API key by itself must never cause default tests to issue live
requests.

## Frontend gates

Before the gate, run `npx biome check --write` on touched frontend files
under `src/`. Biome's enforced scope is `src/` only (the gate runs `biome ci
src`; see cmd/evener-hub/frontend/package.json) — files outside it, such as the
`scripts/layoutguard` harness HTML, deliberately reproduce component markup
that trips a11y lint rules, so an explicit-path Biome run over them reports
violations the gate does not enforce. Do not "fix" those to satisfy an
out-of-scope invocation. Use `make test-web` as the canonical frontend unit,
typecheck, and Biome gate; on Chrome-capable hosts, also run `make
test-web-browser` for real geometry and browser guards. CI checks Biome
formatting. Avoid `noNonNullAssertion` and array-index-key violations.
