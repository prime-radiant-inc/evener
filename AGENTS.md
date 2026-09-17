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

The typecheck step runs `npm run typecheck` (`tsc --noEmit --incremental
false` from `cmd/evener-hub/frontend`), which reads that directory's
`tsconfig.json` and its `include` — `src` and `../../../appwire-client/typescript`
— covering both trees in one program: this is the same file set a bare
`tsc --noEmit -p tsconfig.json` from the same directory resolves (verified
byte-identical as a point-in-time measurement on #1676; re-check with
`tsc --listFilesOnly` rather than trusting that count to stay current).
`make test-web` runs the typecheck behind `web-preflight.sh`, which `npm
ci`s the frontend install whenever `package-lock.json` is newer than
`node_modules`. A bare `tsc -p tsconfig.json` skips that check: right after a
merge that changed `package-lock.json` but before an `npm ci`, it type-checks
against a stale install and can report real-looking errors (missing types,
unresolved modules) that are an artifact of the stale install, not of the
code — `make test-web` never hits this because its preflight repairs the
install first. Prefer `make test-web` to reproduce the gate by hand. If you
run `npm ci` yourself instead, check `[ -L node_modules ]` first: an agent
worktree's `node_modules` is often a symlink to a shared install other
worktrees use, and `npm ci` deletes the existing `node_modules` before
installing — through a symlink that deletes the shared install out from
under everyone else. `web-preflight.sh` guards exactly this (comparing the
symlink target's own `package-lock.json` instead of running `npm ci` through
it); read it before reproducing its `npm ci` by hand. The same symlink risk
applies to `appwire-client/typescript` (no preflight script owns its
install; `make test-api-package` runs `npm run qualification` directly) and
to `mobile-native` (`native-preflight.sh` checks the install's freshness and
health but never runs `npm ci` itself, precisely to avoid this — it fails
loudly and names the command instead). Never run `npm ci` through a
symlinked `node_modules` in any of the three.

## Importing the AppWire TypeScript package

The shared client lives at `appwire-client/typescript` and every consumer in
this repository imports it by name, never by a relative path into that
directory:

- `@evener/appwire-client` for the root exports,
- `@evener/appwire-client/<subpath>` for the subpaths its `package.json`
  `exports` map publishes (the package README lists them and says when a
  module gets a subpath instead of a root export), and
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
