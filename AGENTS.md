# Agent Instructions

## Testing

Before adding or changing tests, read `docs/developing-evener/testing.md`.
For the rest of the dev-facing docs (building, linting, coverage, fuzzing,
environment, worktrees), see `docs/developing-evener/README.md`; for every
`make` target with a one-line summary, run `make help`.

Default tests must be deterministic. Do not make `make test` or
`go test ./...` depend on provider credentials, network access, quota, current
model behavior, or ambient developer machine state.

The gates: `make lint` (golangci-lint across every module, TOML naming,
tagged compile floors, generated-output freshness, secret scan), `make vet`,
`make test` (all modules + the frontend gate). `make merge-approval-gate`
is the canonical pre/post-merge sequence. Tool versions are pinned in
`.tool-versions` — `make tools` installs what CI runs.

Use this boundary:

- Evener plumbing: use a scripted provider at the LLM boundary and exercise real
  Evener code below it. Examples: CLI wiring, appwire RPC, daemon queues, session
  loops, tool execution, transcript writes, event emission, goal continuation
  routing, hook dispatch, and prompt composition.
- Model behavior or provider API behavior: keep it live, but require explicit
  opt-in such as `EVENER_LIVE_TESTS=1` or `EVENER_*_E2E=1` in addition to the
  provider credential.

A provider API key by itself must never cause default tests to issue live
requests.

## Frontend gates

Before the gate, run `npx biome check --write` on touched frontend files
under `src/` and on touched files in the AppWire TypeScript package. Biome's
enforced scope is those two directories (the gate runs `biome ci src
../../../appwire-client/typescript`; see cmd/evener-hub/frontend/package.json)
— files outside them, such as the `scripts/layoutguard` harness HTML,
deliberately reproduce component markup that trips a11y lint rules, so an
explicit-path Biome run over them reports violations the gate does not
enforce. Do not "fix" those to satisfy an
out-of-scope invocation. Use `make test-web` as the canonical frontend unit,
typecheck, and Biome gate; on Chrome-capable hosts, also run `make
test-web-browser` for real geometry and browser guards. CI checks Biome
formatting. Avoid `noNonNullAssertion` and array-index-key violations.

## Importing the AppWire TypeScript package

The shared client lives at `appwire-client/typescript` and every consumer in
this repository imports it by name, never by a relative path into that
directory:

- `@evener/appwire-client` for the root exports,
- `@evener/appwire-client/docContent` for the doc-pane data layer, the one
  subpath the package publishes, and
- `@evener/appwire-client/testing/<module>` for the fakes and fixtures. That
  specifier is in-repo only — it is absent from `package.json` `exports` and
  from the tarball — so it belongs in test and dev-support files and nowhere
  else: `*.test.*`/`*.spec.*`, `__tests__/`, `src/dev/`, and `*TestUtils.*`.
  `check-package-tests` fails a production file that imports it.

The name resolves through `tsconfig` `paths`, the Vite and vitest configs, and
Metro's `resolveRequest`; the frontend and `mobile-native` declare no npm
dependency on the package. `make lint-package-imports` fails on a path import,
because nothing else would notice one; it exempts only the resolver configs and
`mobile-native/src/metroResolver.test.ts`, which asserts that mapping, each
named one by one.

`mobile-native` needs the `paths` in **`tsconfig.json`**, not only in
`tsconfig.check.json`: `tsc --noEmit` is passed the latter explicitly, but
`tsx` reads the former, and the `scripts/*.mts` tools the README tells you to
run load app modules that import the package by name. `npm run check:scripts`
(part of `make test-native`) resolves those scripts' module graphs without
loading them, since each opens a socket the moment its body runs.
