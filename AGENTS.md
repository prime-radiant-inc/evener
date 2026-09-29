# Agent Instructions

## Worktree preference

Always create an isolated worktree for feature, bug-fix, and PR work. You have permission.
Keep unrelated changes in the current checkout out of the worktree and PR.

## Product behavior and evergreen documentation

Read [the product guide](docs/product/README.md) and the relevant row in
[the subsystem map](docs/product/subsystems.md) before changing product behavior.
Evener should help the user achieve their aims with minimal friction. Prefer
automatic recovery, preservation of the user's work, and continued use of healthy
parts of the product. A banner or a repair button is not a substitute for recovery.

Do not treat "fail closed" or "fully locked down" as a default design goal. Explain
the concrete user intent or guarantee a restriction serves, the work it prevents,
and who restores operation when the triggering condition clears. Honor the user's
chosen boundaries and reconcile uncertain outcomes without losing or duplicating
their work. Discuss consequential product choices instead of inventing additional
restrictions or silently changing existing promises.

Keep the product docs evergreen in the same change as the code. Update subsystem
responsibilities, source-of-truth and recovery ownership, affected client surfaces,
and linked contracts when they change. Use stable filenames and current behavior;
keep dated narratives, rollout logs, and PR progress out of these references.

[The friction punchlist](docs/product/friction.md) contains cases for individual
product discussion, not approved implementation instructions. Keep evidence and
proposed behavior distinct. After an agreed fix is implemented and verified,
update its owning guide and remove the resolved case from the open list without
renumbering other cases. Verify recovery and preservation contracts with meaningful
behavior tests, including the transition back to useful operation.

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

The full suite runs faster in CI than on this workstation, the race, fuzz, and
frontend lanes included. Do not block a push on a long local full-suite run:
run the fast, targeted tests for what you changed, push, and let CI run the
full suite as the source of truth. Reserve a local full run (`make test`,
`make merge-approval-gate`) for when you need it, such as reproducing a CI
failure.

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

Run `make hooks` once per clone: the pre-commit hook formats staged TypeScript
with each tree's own Biome and re-stages it (`docs/developing-evener/README.md`).

Biome's enforced scope is `cmd/evener-hub/frontend/src` and
`appwire-client/typescript` (the gate runs `biome ci src
../../../appwire-client/typescript`; see cmd/evener-hub/frontend/package.json).
Never run `npx biome` from the repository root: no `biome` binary is installed
there (the frontend's pinned `@biomejs/biome` lives in
`cmd/evener-hub/frontend/node_modules`, and `mobile-native` carries its own copy
for the native tree), so a root `npx biome` resolves an unrelated `biome@0.3.3`
package that ignores its arguments and exits 0 — a root-scoped invocation checks
nothing while reporting success. Use
`make lint-biome` (part of `make lint`), or run Biome from the frontend
directory: `cd cmd/evener-hub/frontend && npm run lint` to check or
`npm run check` to fix. Before the gate, run
`cd cmd/evener-hub/frontend && npx biome check --write <touched paths>` on
touched files under `src/` and on touched files in the AppWire TypeScript
package. Files outside those two directories, such as the `scripts/layoutguard`
harness HTML, deliberately reproduce component markup that trips a11y lint
rules, so an explicit-path Biome run over them reports violations the gate does
not enforce. Do not "fix" those to satisfy an out-of-scope invocation. Use
`make test-web` as the canonical frontend unit, typecheck, and Biome gate; on
Chrome-capable hosts, also run `make test-web-browser` for real geometry and
browser guards. CI checks Biome formatting. Avoid `noNonNullAssertion` and
array-index-key violations.

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
code. `make test-web` avoids that specific case — a real, non-symlinked
`node_modules` left behind by a `package-lock.json` change — because its
preflight repairs the install first. Prefer `make test-web` to reproduce the
gate by hand rather than running `npm ci` yourself: an agent worktree's
`node_modules` is often a symlink to a shared install other worktrees use, and
`npm ci` deletes the existing `node_modules` before installing — through a
symlink that deletes the shared install out from under everyone else.
`web-preflight.sh` validates that case first: when `node_modules` is a symlink
it compares the symlink target's own `package-lock.json` with this worktree's
and refuses if they differ, before applying the `-nt` freshness shortcut (it
follows symlinks, so a shared install newer than this worktree's lockfile would
otherwise skip the comparison entirely). If you do run `npm ci` by hand, still
check `[ -L node_modules ]` yourself first; don't rely on the script to catch
it for you. The same
symlink risk applies to `appwire-client/typescript`, whose install
`api-package-preflight.sh` now owns for `make test-api-package`: like
`web-preflight.sh` it `npm ci`s a real, non-symlinked install that is missing
or older than the lockfile, and for a symlinked shared install it compares
lockfiles and refuses when they differ before the `-nt` shortcut, so it never
runs `npm ci` through one. It applies too
to `mobile-native` (`native-preflight.sh` refuses a symlinked install
outright — the bundler resolves no module through one, whatever the lockfiles
say — and never runs `npm ci` itself; it fails loudly and names the command
instead). Never run `npm ci` through a
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

## Shepherding a PR through RoboRev

- **Simplify-code*
  Before submitting a PR, you should always use the /simplify-code skill to
  improve the code quality.

- **Shepherd-PR**
  You should use the /shepherd-pr skill if it is installed.
- **One push, one CI run, one review, one wait.** Make the whole round's edits
  (fixes for every finding), then push once, then wait once with the
  shepherd-pr settle detector. The detector exits when the head stops moving,
  no check is outstanding, and roborev's combined review names your exact
  head. Run it as its own background job: a `&` inside a job orphans it, and
  an orphaned job once killed both the comment post and the detector silently.
- **Green is not clean.** `roborev_check=SUCCESS` says the review *ran*, not
  that it found nothing — the combined comment's body carries the findings.
  Read it every round, plus the per-commit surface: `roborev list --open` in
  the checkout; findings against your own commits can sit there unseen. Close
  a per-commit review only when its findings are genuinely resolved; never
  blanket-close, and leave other lanes' reviews alone.
- **Triage each finding by evidence, not reflex.** Fix at the root
  red-test-first: watch the reviewer's scenario fail, then pass, and post both
  runs in a PR comment — the repo's convention is one evidence comment per
  round covering every finding. When the finding is wrong, refute it with the
  code's own construction (e.g. Summarize counts every non-done/cancelled
  status as remaining, so the aggregate cannot claim completion) or the
  pinned toolchain (tsc 6.0.3 types `Element.textContent` as non-null,
  matching the DOM spec — verify a claimed typecheck failure against
  `make test-web` before "fixing" it). When two reviewers conflict, the
  reconciliation usually is that both are right about different cases; pin
  both cases.
- **Merge the base before asking for approval.** If the repo dismisses stale
  reviews, a post-approval base merge voids the approval and demands another;
  merge `origin/main` first so the review and the approval land on the same
  final head.
- **Merge approval**
  If every CI check is green and RoboRev notes only 'low' findings or findings 
  you have conclusively refuted, you are authorized to admin squash merge to main.

- **Gates run from the repo root.** `make test-web`'s target lives in the root
  Makefile; running it from `cmd/evener-hub/frontend` dies with "No rule to
  make target". Run Biome's autofix on every touched file before the gate —
  including `appwire-client/typescript`, which the gate checks too; two gate
  runs in one loop died to comment rewraps alone. Biome must run from the
  frontend directory (see above), just not with a `cwd` that strands `make`.
