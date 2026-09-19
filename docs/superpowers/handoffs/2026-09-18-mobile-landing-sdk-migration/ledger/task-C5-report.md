# Task C5 — move composerInput into the protocol package

Status: DONE. PR #1229 (non-draft), branch `claude/sdk-c5-composer-input`.
Commit: `1452e07d5` (single commit). Pushed head: `1452e07d5`.
Base: `origin/main` `2b1e02939`; main had not moved at push time, no merge needed.

## What landed
- `git mv` of `stores/composerInput.ts` to `protocol/composerInput.ts`. Only the
  two imports changed inside the file: `./attachmentMarkers`, `./types.gen`.
  **There is no `composerInput.test.ts` in the web tree** — the brief's "if it
  exists" check came back empty. The oracle is `mobile-native/src/composerInput.test.ts`,
  which stays put with only its specifier repointed.
- Plumbing: `tsconfig.build.json` `files` (beside `attachmentMarkers.ts`);
  `index.ts` re-export of `InputAttachment` + `buildComposerInput`/`buildInput`,
  alphabetical between clientLike and displayFormat; `qualify-package.mjs`
  `shippedModules` + 2 `rootValues` + 1 `rootTypes` + 2 real root smoke calls.
  No `\n` needed in the smoke inputs, so C26's template-literal trap did not bite.
- Importers: web `panes/spawn/startThread.ts` and `stores/threads.ts` (both its
  import and its `export type { InputAttachment }` re-export, kept as the store's
  public interface per rule 4); native `composerCommand.ts`, `composerInput.test.ts`,
  `draftDocument.ts`, `draftImages.ts`, `newSession.ts`, `screens.tsx`.
- Repo-wide grep over web, `mobile/`, `mobile-native/`, `test/scenarios`,
  `docs/web-ui`: no prose citation, no `vi.mock` of the path, no scenario card.
  The brief's importer list was exactly right; nothing extra to repoint.
- `protocol/README.md` export prose gained the module.

## Falsification
Two-step, and worth recording: deleting only the `index.ts` re-export fails on
the root-export assertion first (the names are still in `rootValues`), not on
reachability. Removing its manifest names too, with `shippedModules` kept, gives
the #1224 shape: `shipped module unreachable from every published specifier:
composerInput`. Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 shared tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over the four touched
`cmd/evener-hub/frontend/src` files only; it sorted two import lines.

## Done differently, and why
1. Rewrapped `README.md`'s first paragraph (textwrap at 76) after the added
   clause overflowed two lines. Whitespace plus the added clause, nothing else.
2. Left the module's comments alone — none names a directory. `attachmentMarkers.ts`'s
   bare reference to `buildInput` is now a same-directory neighbour, so it reads
   better than before.
3. `startThread.ts:8` still takes `InputAttachment` from `../../stores/threads`.
   Left as is: threads.ts re-exports it, and repointing that consumer is not
   this row's scope.

## Merge round 1
`2a7a09c4c` merge of `origin/main` `c867646c4` (#1225 C11a toolCallText landed
ahead). One conflict: `README.md`'s export paragraph — both sides added a clause
to the same sentence. Resolved keeping both (composer input assembly + the
tool-call text helpers) and rewrapping at 76. `index.ts`,
`tsconfig.build.json` and `qualify-package.mjs` auto-merged with both modules
present. Post-merge `make test-api-package` PASS, `make test-web` PASS
(typecheck/test/lint). Pushed head `2a7a09c4c`.

## Merge round 2
`dba19a5d9` merge of `origin/main` `0acebbb0d` (#1230 C15 catalogCommands).
One conflict, `README.md`'s export sentence again; kept both clauses, rewrapped.

## Merge round 3
`c15c6c4ca` merge of `origin/main` `314281cd5` (#1232 C11b askShared +
deriveAskQuestions), taken before round 2 was pushed. Two conflicts:
`README.md` (same sentence, three clauses now) and `index.ts` — both sides added
a re-export block right after clientLike; kept both, composerInput before
deriveAskQuestions. `tsconfig.build.json` and `qualify-package.mjs` auto-merged
with every module present. Gates after each round: `make test-api-package` PASS,
`make test-web` PASS. Final pushed head `c15c6c4ca`; PR #1229 MERGEABLE.
