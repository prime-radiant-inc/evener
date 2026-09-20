# Task A3 report — move the AppWire TypeScript package

**Status: done.** PR #1241 (not draft), branch `claude/sdk-a3-package-move`, head `d32a1d479`, off `origin/main` `d7ff88653`.

Commits: `a0a137c68` git mv · `15ee5837f` build/gate/CI paths · `ceb6ad02a` seam (32 web stubs + 3 mobile seds) · `450899b16` generator/Go/cards/docs · `39cc6f4ac` the three risk fixes · `d32a1d479` gate assertion on the package's test-file count (the coordinator's mid-task ruling; its other four points were already what I had built).

Gates, all green: `make test-web` (451 files / 10413 tests), `make test-web-browser` (layoutguard, overflowguard, shellguard, spawnguard, transcriptscrollguard), `make test-native` (7 files / 777 tests on the shared stream), `make test-api-package`, `make lint` incl. `lint-generated`, `WEB=0 make test` (all Go modules, both audits), `make build-web`, `make generate` (writes the new path, no diff).

Test-file counts (`vitest list --filesOnly`): without the `test.include` entry 430 files / **0** under the package; with it 457 / **27**. `fs.allow` before/after through the guards' own Vite config: `GET /@fs/.../appwire-client/typescript/errors.ts` **403 → 200**. Biome over the package: **5 errors → 0**. Coverage: `web 96.1` floor **not** moved — measured 96.79% lines, 28 of 29 package source modules in the denominator via `coverage.allowExternal` + an absolute include glob.

## Where the scout report was wrong or short

- **27 test files, not 25**, and **855 import sites, not 847** (138 mobile-native, not 130) — C-row landings since `31a5a4370`. Distinct web specifiers are 32, not 30 (`clientLike`, `model`, `reconcileBatches`, `slashCompletion` are web-only).
- **Two package test files import the web app** — `tokenFlood.test.tsx` (6 imports of `panes/`, `shell/`, `stores/`) and `slashCompletion.test.ts` (1 type import). The scout's "package-internal files that move but need no edit" missed this entirely; it is the only thing in the move that needed hand work. Seven specifiers repointed, no logic change. Filed as **#1242**.
- **Bare imports break at run time too, not only in `tsc`** (R3 only described `tsc`). `react` and `@testing-library/react` need `resolve.alias` entries in `vite.config.ts` or `tokenFlood.test.tsx` fails to load; `vitest` resolves itself.
- **`editorial-preview.test.mjs:33` asserts the exact `fs.allow` array** and fails on the R1 fix. Not in the scout's list; updated in the same commit.
- **Two more scenario citations than the scout's 12** (`web-queue-then-completes.md:128`, `tui-effort-command.md:147`), unanchored so not audited; repointed anyway. 14 across nine cards.
- **`biome.jsonc` `files.includes` cannot use `../`** — a `**/`-anchored pattern works. The scout left the mechanism open.
- **R1's blast radius is narrower than stated**: with the package outside `fs.allow`, shellguard still passed. The 403 is real (proved directly), but no guard currently fails on it, so `fs.allow` is correct rather than load-bearing for today's guards.
- Sizing held: ~360 planned, 400 changed lines outside the pure move and the 166 sed lines.

## Concerns

1. **#1243** — `coverage.include` no longer enumerates *unloaded* package files, only ones a test loads. A new untested package module scores ABSENT rather than 0%, which is the false green that `include` line exists to prevent. `src/` is unaffected.
2. **Metro has no gate.** The `resolveRequest` branch resolves all three specifiers to files that exist (checked directly, not through a bundle), but nothing in CI bundles the native app, so a future break there ships green. A4's packed-consumer fixture does not cover Metro either.
3. `docs/superpowers/{plans,specs}` untouched as instructed, so they still describe `src/protocol/`.
