# Task C9 — move slashCompletion into the protocol package

Status: DONE. PR #1234 (non-draft), branch `claude/sdk-c9-slash-completion`.
Commit: `cd386f9c6`; merge `c9feb6990` of `origin/main` `314281cd5` (#1232 C11b
landed ahead). Pushed head: `c9feb6990`. Base at branch time: `0acebbb0d`.

## What landed
- `git mv` of `panes/session/composer/slashCompletion.{ts,test.ts}` into
  `protocol/`. Module imports only `./catalogCommands` and `./types.gen` — the
  brief's STOP condition did not fire. The test keeps
  `import type { ScopedCommand } from "../shell/palette/commands"` so the web
  palette's real type still flows into `mergeSlashCommands`; `tokenFlood.test.tsx`
  is the standing precedent for a package *test* reaching into the app tree.
- Plumbing: `tsconfig.build.json`; `index.ts` 5 types + 5 values in two blocks
  between sessionErrors and stableDelegate; `qualify-package.mjs` `shippedModules`
  + 5 `rootValues` + 2 `rootTypes` (SlashToken, SlashMenuItem — the parameter
  types of spliceSlashCommand and filterSlashMenuItems) + 6 real root smoke
  calls (parse, newline-rejection with `\\n` escaped, merge of builtin+plugin+skill,
  filter by the parsed query, evaluateSlashLabel run length, splice); README prose.
- Importers: web Composer.tsx, SlashCompletionMenu.tsx, Spawn.tsx; native
  commandCatalog / composerCommand / composerCommand.test / CommandCompletion /
  screens. `commandCatalog.ts`'s block hand-sorted after `protocol/errors`.
  Repo-wide grep found no vi.mock, no scenario card, no path citation; the
  remaining prose hits name the bare filename and are still accurate.

## License finding (the C9 risk cell)
`cmd/evener-hub/frontend/LICENSES/beautiful-ui.txt` is the **web app's**
attribution, not this module's: a dozen widgets cite it and
`panes/settings/sections/about.test.tsx` asserts the embedded const matches it on
disk. So it stays. Per the row the package got its own copy at
`protocol/LICENSES/beautiful-ui.txt`, `package.json` `files` gained `LICENSES`,
and the runner gained both a `package/LICENSES/beautiful-ui.txt` expectation and
a `package/LICENSES/` clause in the unexpected-path allowlist (confirmed the
gate fails on the entry without it).

## Falsification
Two-step per #1224: `shippedModules` kept, `index.ts` re-export plus the
module's `rootValues`/`rootTypes` names removed →
`shipped module unreachable from every published specifier: slashCompletion`.
Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Post-merge test-api-package and
test-web re-run PASS. Biome over the six touched
`cmd/evener-hub/frontend/src` files only; it moved two import blocks.

## Done differently, and why
1. The package's license copy reproduces the MIT text **verbatim** but rewrites
   the descriptive preamble. The web file's preamble claims the evener web UI's
   "visual design language (palette, elevation system, type choices, and the
   chrome patterns applied across widgets)" is adapted from Beautiful UI — false
   of a zero-dependency protocol client shipping one text parser, and it would
   be published in the npm tarball. The new preamble names what this package
   actually ports. Flagged in the PR body.
2. Left the module header's "Composer.tsx wires these ... (stores/commandCatalog.ts)"
   paragraph alone: it names consumers, not the module's own old directory, and
   it was already incomplete on main (Spawn.tsx and mobile-native also wire it).
   Its "see LICENSES/beautiful-ui.txt" now resolves to the package's own copy,
   which is the right file, so no edit there either.

## Merge round 2
`ea74ce97d` merge of `origin/main` `31a5a4370` (#1229 C5 composerInput landed).
One conflict: `README.md`'s export paragraph — both sides added a clause to the
same sentence. Resolved keeping both (composer input assembly + the inline
slash-completion parser/merge/filter/splice) and rewrapping at 76 with
`break_on_hyphens=False` so "slash-completion" stays whole; the Beautiful UI
attribution paragraph survives. `index.ts`, `tsconfig.build.json` and
`qualify-package.mjs` auto-merged with both modules present — verified
shippedModules, rootValues, rootTypes, smoke calls, and the LICENSES
expected-file entry plus allowlist clause all intact. Post-merge
`make test-api-package` PASS, `make test-web` PASS (typecheck/test/lint).
Pushed head `ea74ce97d`; PR #1234 now MERGEABLE.

## Merge round 3
`67792fade` merge of `origin/main` `a0e594122` (#1233 C12 reconcileBatches).
Two conflicts. `README.md`: same export sentence again — kept main's paragraph
(now naming batch reconciliation and composer input assembly) and re-added the
inline slash-completion clause, rewrapped at 76, attribution paragraph intact.
`mobile-native/src/screens.tsx`: main re-sorted the import block and moved
`humanizeState` below the two new `protocol/` imports while my side repointed
slashCompletion; resolved by taking main's ordering and placing the
slashCompletion block after `protocol/reconcileBatches` (hand-sorted — biome
never runs over mobile-native). `index.ts`, `tsconfig.build.json` and
`qualify-package.mjs` auto-merged carrying reconcileBatches, composerInput and
slashCompletion, with the LICENSES expected-file entry and allowlist clause
intact. Post-merge `make test-api-package` PASS, `make test-web` PASS, and
`make test-native` PASS (re-run because screens.tsx was a conflict file).
Pushed head `67792fade`; PR #1234 MERGEABLE.
