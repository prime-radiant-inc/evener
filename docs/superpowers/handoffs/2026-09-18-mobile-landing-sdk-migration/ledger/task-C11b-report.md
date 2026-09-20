# Task C11b — move askShared and deriveAskQuestions into the protocol package

Status: DONE. PR #1232 (non-draft), branch `claude/sdk-c11b-ask-shared`.
Commit: `91ce8dd37` (single commit). Pushed head: `91ce8dd37`.
Base: `origin/main` `c867646c4`; main had not moved at push time, no merge.

## What landed
- `git mv` of `panes/session/askShared.{ts,test.ts}` and
  `composer/askDock/deriveAskQuestions.{ts,test.ts}` into `protocol/`. Both test
  files existed and both moved. Inside the files only `./model`,
  `./toolCallText`, `./askShared`, `./types.gen`. askShared imports nothing
  outside protocol/ — the brief's STOP condition did not fire.
- Plumbing: `tsconfig.build.json` (both, after askAnswers); `index.ts` six
  symbols in two module blocks, alphabetical; `qualify-package.mjs`
  `shippedModules` + 3 `rootValues` + 2 `rootTypes` + four real smoke calls
  (parsed question, malformed-JSON fallback, `liveAskQuestions` key off an acked
  call, `answeredAskUserSuffix` recap off an `[answers]` reply with `\\n`
  escaped); `README.md` export prose.
- Importers: web askUser.tsx and the five askDock files; native
  `questionBatches.ts` (hand-sorted, biome never over mobile-native).

## Falsification
Two-step per module (#1224): `shippedModules` kept, `index.ts` re-export plus
the module's `rootValues`/`rootTypes`/smoke names removed →
`shipped module unreachable from every published specifier: askShared`, then the
same for `deriveAskQuestions`. Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over the 13 touched
`cmd/evener-hub/frontend/src` files only; it sorted imports and inserted the
blank line between each moved file's header comment and its imports.

## Done differently, and why
1. Three scenario-card citations named the old paths
   (`panes/session/askShared.ts:118-150` ×2, `askDock/deriveAskQuestions.ts:60-90`).
   `TestScenarioSourceCitationsResolve` resolves by path suffix, so all three
   would have failed. Repointed with full paths, matching the neighbouring
   `protocol/askAnswers.ts` citation, and recomputed the deriveAskQuestions
   range (62-97) after biome's blank line shifted it.
2. Repointed five comments the move falsified: askShared's header (it described
   itself as a leaf module sitting between transcript/tools and composer),
   askUser.tsx, askUser.test.tsx, reconcileBatches.ts, and askDock/index.ts —
   which drops deriveAskQuestions from its list of this directory's files rather
   than renaming it (C11a's precedent). reconcileBatches.ts's opening paragraph
   reflowed as a result (13 lines from 15); that is the only whitespace churn.
3. `mobile/src/conversation/project.ts:183` said its parser "mirrors the Hub's
   parseAskUserQuestions (askShared.ts)" — now `protocol/askShared.ts`, and
   "the Hub's" dropped since the module is shared. Left the comment's claim
   alone; native still keeps its own copy (that is a later D-phase row).
