# Task C12 — move reconcileBatches into the protocol package

Status: DONE. PR #1233 (non-draft), branch `claude/sdk-c12-reconcile-batches`.
Commit: `1661e49ae` (single commit). Pushed head: `1661e49ae`.
Base: `origin/main` `314281cd5`; main had not moved at push time, no merge.

## What landed
- `git mv` of `askDock/reconcileBatches.{ts,test.ts}` into `protocol/`. The
  module's full import list is one line — `AskQuestionRef` from
  `deriveAskQuestions` — so the brief's STOP condition did not fire; rewritten
  to `./deriveAskQuestions`. No logic changes.
- Plumbing: `tsconfig.build.json` (after `deriveAskQuestions.ts`); `index.ts`
  `AskBatch` + `reconcileBatches`, between the `model` and `reducer` blocks;
  `qualify-package.mjs` `shippedModules` + 1 `rootValues` + 1 `rootTypes` + a
  real smoke call feeding `liveAskQuestions`' output through `reconcileBatches`
  (no `\n` in the inputs, so the template-literal trap did not bite);
  `README.md` export prose (tail rewrapped at 78, the file's own width).
- Importers: web `askDockStore.ts`, `AskDock.tsx`; native `questionBatches.ts`
  **and `screens.tsx`** — the brief listed only the first (see below).

## Falsification
Two-step (#1224): `shippedModules` kept, `index.ts` re-export plus the module's
`rootValues`/`rootTypes`/smoke names removed → `shipped module unreachable from
every published specifier: reconcileBatches`. Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over the 8 touched
`cmd/evener-hub/frontend/src` files; it only re-sorted the AskDock.tsx import.

## Done differently, and why
1. `mobile-native/src/screens.tsx:33` was a second native importer the brief did
   not name (`import type { AskBatch }`). Repointed and hand-sorted into place
   (tabs, no biome over mobile-native).
2. No scenario card and no `vi.mock` cites this path — the repo-wide grep found
   only `docs/superpowers/plans/wave5-report.md`, a dated record the plan says
   is not swept. Left alone.
3. Repointed four comments the move falsified: `deriveAskQuestions.ts` named
   `askDock/reconcileBatches.ts`; `askDockStore.ts` (header and `reconcileRef`)
   and `AskDock.test.tsx` named a bare `reconcileBatches.ts` that is now across
   the boundary; and the moved file's own "see askDockStore.ts" now says
   "the web dock's own askDock/askDockStore.ts". Each reflowed at its file's
   width — that is the only whitespace churn.
4. `askDock/index.ts` drops `reconcileBatches` from its file list rather than
   renaming it (C11a/C11b precedent); its paragraph reflowed as a result.
5. The moved header still says "protocol/deriveAskQuestions.ts" for what is now
   a sibling. Left it: the path is still correct from the repo root, and
   shortening it would reflow a paragraph for no gain.

## Merge round 1
`3dbfb81b2` merge of `origin/main` `31a5a4370` (#1229 C5 composerInput squashed
ahead). One conflict: `README.md`'s export paragraph — both sides added a clause
after the same anchor. Kept both (batch reconciliation + composer input
assembly), rewrapped at 76 (main's width) with `[image N]` protected from
breaking. `index.ts`, `tsconfig.build.json` and `qualify-package.mjs`
auto-merged with both modules present and correctly ordered. One hand fix the
auto-merge could not make: `mobile-native/src/screens.tsx` ended up with C5's
`protocol/composerInput` import sorted after `shell/rail/sessionState` — moved
it above `protocol/reconcileBatches`. Post-merge `make test-api-package` PASS,
`make test-web` PASS (typecheck/test/lint), `make test-native` PASS (777 + tsc).
Pushed head `3dbfb81b2`.
