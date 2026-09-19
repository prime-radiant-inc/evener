# Task C10 — move submitRouting into the protocol package

Status: DONE. PR #1222 (non-draft), branch `claude/sdk-c10-submit-routing`.
Commit: `eeb638058` (single commit). Pushed head: `eeb638058`.
Base: `origin/main` `303053dfb`; main had not moved at push time, no merge needed.

## What landed
- `git mv` of `panes/session/composer/submitRouting.{ts,test.ts}` to
  `protocol/submitRouting.{ts,test.ts}`; intra-package import fixed to
  `./sendQueueAvailability`. No logic change; the test file is byte-unchanged.
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-export (types +
  functions, alphabetical between stableDelegate and transport);
  `qualify-package.mjs` `shippedModules` + `rootValues` + `rootTypes` + three
  root smoke calls.
- Importers: web `Composer.tsx` → `../../../protocol/submitRouting`; native
  `mobile-native/src/composerSteering.ts` → retargeted deep path.

## TDD evidence
`make test-api-package` passed in silence after the move alone (the hazard the
phase-C rule names). With the module in `shippedModules` but no `index.ts`
re-export it failed: `shipped module unreachable from every published
specifier: submitRouting`. Green after the re-export.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS ·
test-web-browser PASS (5 guards) · `go test -run TestScenarioSource .` PASS.
Biome: touched frontend files only, no fixes needed. mobile-native untouched by biome.

## Done differently, and why
- The brief's importer list was incomplete. Four scenario cards cite the file by
  directory; `TestScenarioSourceCitationsResolve` resolves by path suffix, so
  they were repointed to `protocol/submitRouting.ts` or the audit goes red.
  Bare `submitRouting.ts:NN` citations resolve either way and were left alone,
  as were the dated `docs/superpowers/` records (A3's rule: not swept).
- Added one line to `protocol/README.md`, whose prose enumerates the package's
  exports and would otherwise be stale.
- Added both route types (`SubmitRoute`, `SteerRoute`) to `rootTypes`, not the
  one the file's comment asks as a minimum.
- The plan docs are not on main (PR #1182 still open); read them from
  `refs/pull/1182/head` rather than from another worktree.
- Fresh worktree needed `npm ci` in protocol, frontend and mobile-native first.
