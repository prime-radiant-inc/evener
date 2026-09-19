# Task C13 — move activityRows into the AppWire package

Status: DONE. PR #1223 (non-draft), branch `claude/sdk-c13-activity-rows`.
Commit: 6b28b0de2 (single commit). Pushed head: 6b28b0de2. Base: origin/main
303053dfb — main had not moved at push time, so no merge was needed.

## What landed
- `git mv` of `panes/session/chrome/activityRows.ts` (208) and
  `activityRows.test.ts` (397) into `protocol/`. Only edits inside those two
  files: `../../../protocol/{activityData,stableDelegate}` → `./…`.
- Import check passed the brief's gate: the module imported nothing outside
  `protocol/`, so no NEEDS_CONTEXT.
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-exports 4 runtime
  symbols + 6 types; `qualify-package.mjs` gains `activityRows` in
  `shippedModules`, the 4 symbols in `rootValues`, `ActivityRow` in
  `rootTypes`, and 4 real smoke calls.
- Importers: web `ActivityTree.tsx`, `ActivityRowDetail.tsx`, **and
  `ActivityRowDetail.test.tsx`** (a third web importer the brief did not list);
  native `mobile-native/src/ActivitySheet.tsx`. Repo-wide grep clean.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(740 + 777 tests, tsc) · test-web-browser PASS (5 guards).

## Done differently, and why
1. The brief's TDD step ("runner FAILS before the manifest entry") does not
   happen the way it reads: with the file moved and only tsconfig+index done,
   `make test-api-package` stays GREEN. The tarball allowlist admits anything
   under `package/dist/`, so an unlisted module is invisible. I falsified the
   other way instead — added the manifest entry, then deleted the `index.ts`
   re-export and confirmed `shipped module unreachable from every published
   specifier: activityRows`, then restored. Later C rows should expect the same
   shape; the `shippedModules` entry is what arms the assertion.
2. The plan docs are not on main (PR #1182 still open); I read them from
   `refs/pull/1182/head`.
3. Left the historical 2026-08-05 / 2026-08-12 plan docs' `activityRows`
   mentions alone — they record where the file was created, and a separate lane
   owns plan docs.
4. Fresh worktree needed `npm ci` in `src/protocol` and `mobile-native`.
