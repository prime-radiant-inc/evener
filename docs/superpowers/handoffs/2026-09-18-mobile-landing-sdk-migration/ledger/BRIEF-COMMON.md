# Common brief for evener queue implementers

You are an implementer on one lane of the evener repo (prime-radiant-inc/evener). Work ONLY in the
worktree named in your lane brief (absolute path). Never `cd` to the main checkout. The human
owner is Jesse; the coordinator (me) posts dispositions and merges. You do not merge.

## Non-negotiable rules (Jesse's)
- TDD: write the failing test first, watch it fail, then the fix. The existing tests are oracles;
  never delete or weaken a test to make it pass.
- Smallest reasonable change. Never rewrite an implementation. Never add backward compatibility.
  If you think either is needed, STOP and report back instead.
- Measure before refuting or accepting a review finding: reproduce the construct in the code
  (a failing test, a script, a trace), then decide. Never argue from reading alone.
- Comments describe what the code does now, never history ("was", "used to", "D23 changed").
- No commit trailers (no Co-Authored-By, no Generated-by). Commit messages: `type(scope): what`.
- Never `rm -rf`. Never bare `git stash` (the stash is shared across worktrees); if you must set
  work aside, make a WIP commit. Never `git checkout -- <file>` to restore a falsification; use
  `git diff | git apply -R`.
- The native app never shows a "refresh to see the latest" affordance; it auto-refreshes.
- Match the surrounding code style. Do not touch whitespace that does not affect output.
- Never run `npx biome` from the repo root (an unrelated biome 0.3.3 resolves and exits 0).
  Run `npm run lint` in `cmd/evener-hub/frontend`, and `npx biome ci ../../../appwire-client/typescript`
  from that same directory. Never run biome over `mobile/` or `mobile-native/`.
- Go: use the toolchain gofmt `$(go env GOROOT)/bin/gofmt`, never the PATH gofmt.
- Every residual, smell or design question you notice and do not fix: report it to the
  coordinator in your final message under "Residuals" (the coordinator files the issues).
- Estimate work in lines of code, never wall-clock.

- Dropped-assertion proof: if you remove or change an existing assertion, say which one and where the
  same behaviour is still asserted; a coverage drop is worse than a red test.
- Bounded load only: never leave background processes, CPU burners or synthetic load running; kill
  what you start; no local synthetic load above what a single test needs.
- Never delete directories recursively with force (no recursive-force rm). Never bare `git stash`
  (the stash is shared across worktrees). Prefer a WIP commit.
- Falsify only from a committed base and restore with `git diff | git apply -R` or `git checkout HEAD -- <files>`.
- Ask for the failure output before "fixing" a test another lane reports as failing; a passing test is
  not fixed, it is measured.
- Never write into the coordinator's scratchpad (the directory this brief is mirrored to); use your worktree.

## Refresh procedure (when your PR is CONFLICTING or behind main)
1. `git fetch origin main`
2. `git merge --no-ff origin/main` (the repo config refuses a plain merge on divergence).
3. Union resolutions on the shared files: the plan's status table in
   `docs/superpowers/plans/2026-09-12-sdk-migration.md` (theirs then ours, keep every row),
   `appwire-client/typescript/README.md` subpath prose, `appwire-client/typescript/scripts/qualify-package.mjs`
   manifest, `appwire-client/typescript/index.ts`, `tsconfig.build.json`. Keep both sides' rows.
4. `git diff origin/main...HEAD --stat` MUST show only your row's files. Paste that stat in your report.
4b. After a union merge, grep for conflict markers AND check braces: a union inside a function or test
   body can leave an unclosed brace with zero markers; run the typecheck/compile before committing.
4c. If the clone has gone shallow (`ls <repo>/.git/shallow` exists), run `git fetch --unshallow origin`
   once and retry; never resolve with `--allow-unrelated-histories`.
4d. After a squash-merge of a predecessor, every file it touched must diff EMPTY against origin/main
   on your refreshed branch; git can duplicate a whole function silently with no markers.
5. Run the gates that read merged files (below) in the FOREGROUND. Never wait on a background gate.
6. Push. Confirm with `gh pr view N --json mergeable,headRefOid` that it reads MERGEABLE.

## node_modules is SHARED across worktrees: never run `npm ci` by hand
Agent worktrees symlink node_modules (cmd/evener-hub/frontend, mobile-native, appwire-client/typescript)
to one shared install; a bare `npm ci` through the link deletes and rebuilds it under every other
lane's running gates. Check `[ -L node_modules ] && echo SYMLINK` before ever installing; if it prints
SYMLINK, do not install. In a fresh worktree with NO node_modules at all, run the package's make gate
once so its preflight (scripts/web/web-preflight.sh) does the install, and say so in your report.

## Gates = targeted local checks + CI (Jesse, 2026-09-17: "lean on the CI runner")
CI builds the PR's MERGE with main and runs the whole matrix (make test-web, test-native, the browser
guards, Go -race, make lint, secret-scan). Do NOT run those locally; they cost hours per push and
prove macOS only. Run ONLY the targeted checks below, in the foreground, then push and let CI be the
full suite. A red CI = read the log, fix, push again.

Package / web PR (appwire-client/typescript, cmd/evener-hub/frontend):
- vitest on the touched test files (from cmd/evener-hub/frontend; for mobile/ and mobile-native/
  tests use mobile-native's vitest config)
- `npm run typecheck` in cmd/evener-hub/frontend, and `npm run check` (tsc) in mobile-native whenever
  the change touches a port, an exported type, or index.ts
- `npx biome ci ../../../appwire-client/typescript` and `npm run lint` from cmd/evener-hub/frontend
  (never biome over mobile/ or mobile-native/: no config there)
- `make lint-package-imports`
- `make test-api-package` PLUS the index.ts falsification (delete your row's re-export, the qualifier
  must fail with "shipped module unreachable...", restore with `git diff | git apply -R`) only when
  the PR adds or moves a package module or touches index.ts / tsconfig.build.json / the qualifier

Go PR:
- `$(go env GOROOT)/bin/gofmt -l <changed dirs>` (the toolchain gofmt, never the PATH one)
- `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...` per touched module
- `golangci-lint run` on the touched module (see the module map below)
- `go test -count=1` on the touched packages, plain and `-tags evenerfuzz`; `make fuzz-seeds` only if seeds change
- root `go test -short -count=1 .` only when make/*.mk or a make target changed

Not run locally any more: make test-web, make test-native, make test-web-browser, go test -race,
make lint, secret-scan. If a reviewer's finding is about synchronization, run -race on the ONE test
that exercises it, not the package.

## When done
- Push to the PR branch. Post a PR comment `## Round N fixes (<sha9>)` listing each finding, what you
  did, the measurement that justified it, and the gate results (one line each).
- Final message to the coordinator: head SHA (full 40 chars), `gh pr view N --json mergeable` result,
  the `git diff origin/main...HEAD --stat` output, gate results, deviations from the brief, and Residuals.
- If anything in the brief is wrong once you measure it, say so and stop at a clean pushed point
  rather than guessing.

## Lows never cost the big PR a review round (Jesse, 2026-09-17)
When a RoboRev round returns only Low findings, do not push a fix to the PR. Report the Lows to the
coordinator; the PR merges as-is (CI green + simplify done) and the Lows go in a small follow-up PR
opened from main, because a small PR is much cheaper to review than another round on the big one.
This holds for held PRs too. Mediums and above are fixed in the PR as before.

## Wire-shaped tests: fake messages must match what the Go encoder emits
When a test feeds a notification or response into a client store, build it in the shape the daemon
actually sends: read the Go type's json tags in appwire/types.go (omitempty means the field is ABSENT
when zero or empty, never `[]` or `0`), or reuse a recorded fixture from the existing tests. On
2026-09-17 #1705 shipped an "empty queue retires records" fix whose tests passed with
`clientMutationIds: []` / `depth: 0` while the real wire sends neither, so the fix was inert in
production. Post a message -> tags -> shape-when-empty table in the disposition whenever a fix
turns on a wire field being present or absent.

## Five review rounds means decompose (Jesse, 2026-09-17)
A PR that reaches its fifth RoboRev round does not get a sixth. Stop, report to the coordinator, and
expect the change to be split into smaller PRs that each land quickly. When you are dispatched to
build a piece of such a split, keep it under ~150 lines of non-test diff (Jesse, 2026-09-17 22:55 PDT: 'a stack of tiny PRs'; 80-150 is the target, 400 is the hard ceiling that needs the coordinator's explicit OK before pushing) and one mechanism per PR, stacked so each PR builds on the previous and can land alone.

## Go PRs also run golangci-lint on the touched module (added 2026-09-17)
CI's lint-golangci gate runs golangci-lint across every go.work module and it has rules gofmt/vet do
not (e.g. tagliatelle: json tags in the agent package's internal event payloads are snake_case;
wire-level appwire types are camelCase; modernize: `for i := range n`). Before pushing a Go change,
run golangci-lint on the touched module and fix findings. Module map: `agent/` is its own go.work
module (run from agent/); `internal/`, `server/`, `appwire/`, `cmd/evener-hub/`, `cmd/evener-tui/`
are the ROOT module (run from the repo root, e.g. `golangci-lint run ./internal/plugins/...`).

## A package change that touches a port or an exported type runs the native typecheck too (added 2026-09-17)
mobile-native imports @evener/appwire-client by name, so widening a port method (void -> boolean),
renaming an export, or changing a parameter type breaks the phone's build even when no
mobile-native file is in your diff. Before pushing any appwire-client change to a port, an
interface, or index.ts, run `npm run check` (tsc) in mobile-native as well as the frontend
typecheck, and fix the native side in the same PR with the smallest honest change.

## Adding a wire field: update the phone's exact-key validators (added 2026-09-17)
mobile/src/services/conversation.ts validates receipts and responses with exact key allowlists. A new
field on any appwire type makes the phone reject a response the server already applied. When you add
a wire field: grep mobile/ and cmd/evener-hub/frontend for exact-key validators of that type, extend
them in the same PR, and add a daemon-shaped fixture test (key absent when empty, present otherwise).

## Falsification recipe (state the result in every disposition)
A new test proves nothing until you have watched it fail without the fix. From a committed base:
`git revert --no-commit HEAD -- <production files of the commit>` (or `git checkout <base> -- <files>`),
run the ONE test, it must FAIL (paste the failing assertion line into the disposition), then
`git checkout HEAD -- <files>` and `git status --porcelain` must be empty. If the test passes without
the fix, the test is not load-bearing: rewrite it to observe what the fix changes; never post a
rationale for why it still passes.

## This file's canonical home
The canonical copy lives in the coordinator's ledger directory (this path); the session scratchpad
copy is a mirror. Lanes must never write into the coordinator's scratchpad; use your own worktree
or your own scratch directory.
