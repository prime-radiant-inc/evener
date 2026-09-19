# Task C6 — move attachmentMarkers into the protocol package

Status: DONE. PR #1227 (non-draft), branch `claude/sdk-c6-attachment-markers`.
Commit: `9915c3982` (single commit). Merge of main: `f6876459e`. Pushed head:
`f6876459e`. Base was `origin/main` `e2c77cc72`; #1226 (C26 displayFormat)
landed as `b9a98151c` and touched the same four plumbing anchors, so a
`git merge --no-ff origin/main` followed. Only `protocol/README.md` conflicted
(both sides added a clause to the one export sentence); resolved keeping both.
index.ts, tsconfig.build.json and qualify-package.mjs auto-merged with both
modules present. test-api-package and test-web re-run green on the merge.

## What landed
- `git mv` of `stores/attachmentMarkers.{ts,test.ts}` to `protocol/`. Both files
  are byte-identical: the module imports nothing, and the test's
  `from "./attachmentMarkers"` stayed valid, so not even the oracle's import
  changed.
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-export of
  `MarkerAttachment` (type) + `translateAttachmentMarkers`, alphabetically
  between askAnswers and client; `qualify-package.mjs` `shippedModules` +
  `rootValues` + `rootTypes` + one real root smoke call.
- `protocol/README.md` export prose gained the module (C10/C11a precedent).
- Importers: web `TurnFailureEndCap.tsx`, `TurnFailureEndCap.test.tsx`,
  `stores/composerInput.ts`; native `mobile-native/src/composerInput.test.ts`.
  Deep relative paths matching neighbours (A4 has not landed).

## Falsification
Confirmed the C13/#1224 shape: with the `shippedModules` entry present and the
`index.ts` re-export deleted, `make test-api-package` fails with
`shipped module unreachable from every published specifier: attachmentMarkers`.
Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck, 10413 tests, lint) ·
test-native PASS (740 + 777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over the touched
`cmd/evener-hub/frontend/src` files only; it reordered three import lines.

## Done differently, and why
1. The brief's importer list missed one prose citation:
   `docs/web-ui/parity/parity-m6-surfaces.md:166` names
   `frontend/src/stores/attachmentMarkers.ts`. Repointed. No scenario card cites
   the file, so `TestScenarioSourceCitationsResolve` was never at risk.
2. Left the module's opening comment alone. It never says where it lives; its
   one file reference, `(textareaMarkers.ts)`, is a bare filename pointing at a
   different module (C7's), still accurate. `TurnFailureEndCap.tsx:76`'s bare
   `(attachmentMarkers.ts)` is likewise still true.
3. `protocol/README.md`'s first paragraph was rewrapped to absorb the new
   clause; the change is whitespace plus the added sentence.
