# Task C11a — move the tool-call text helpers into the protocol package

Status: DONE. PR #1225 (non-draft), branch `claude/sdk-c11a-tool-helpers`.
Commit: `f787c70b1` (single commit). Pushed head: `f787c70b1`.
Base: `origin/main` `303053dfb`; main had not moved at push time, no merge needed.

## What landed
- `git mv` of `panes/session/transcript/tools/helpers.{ts,test.ts}` to
  `protocol/toolCallText.{ts,test.ts}`. Test file is the oracle; only its import
  specifier changed. Name kept as the brief proposed: `toolCallText` states the
  domain (text and argument helpers for rendering a tool call).
- Plumbing: `tsconfig.build.json` `files`; `index.ts` re-export of all eleven
  functions, alphabetical between stableDelegate and transport;
  `qualify-package.mjs` `shippedModules` + eleven `rootValues` + eleven root
  smoke calls (including parseArgs, str, clip, formatByteCount). No `rootTypes`
  entry — the module declares no exported type.
- 15 web importers rewritten to deep relative `protocol/toolCallText`. No native
  importer exists.
- `protocol/README.md` export prose gained the module (C10's precedent).

## Falsification
Confirmed the C13 shape (#1224): with the `shippedModules` entry present and the
`index.ts` re-export deleted, `make test-api-package` fails with
`shipped module unreachable from every published specifier: toolCallText`.
Restored; green.

## Gates
test-api-package PASS · test-web PASS (typecheck/test/lint) · test-native PASS
(777 tests + tsc) · test-web-browser PASS (5 guards) ·
`go test -run TestScenarioSource .` PASS. Biome over touched
`cmd/evener-hub/frontend/src` files only; it rewrapped three long import lines
and moved two file-header comments off the first import.

## Done differently, and why
1. Rewrote the module's opening comment sentence. It read "helpers for the
   per-tool descriptors in this directory", which the move makes false. A lying
   comment is worse than a moved file; the rest of the file is byte-identical.
2. Repointed two prose citations the brief did not list: the tailFold comment in
   `widgets/codeblock/index.tsx:48` and `docs/web-ui/decisions.md:345`. No
   scenario card cites the file, so `TestScenarioSourceCitationsResolve` was
   never at risk, but both would have pointed at nothing. Dated
   `docs/superpowers/plans/` records left alone per A3's rule.
3. Flagged in the PR body, not changed: `clip` and `str` are very generic names
   to publish at a package root, and C11a moves `toolCallText` into the package
   with a single consumer app — it qualifies only under rule 2's co-move
   allowance, so C11b (askShared + deriveAskQuestions) needs to follow.

## Review round 1
- `27217b334` `refactor(web): point the remaining comments at toolCallText`:
  `tools/bodies.tsx:3`, `tools/webTools.test.tsx:225`,
  `widgets/codeblock/codeblock.test.tsx:216` repointed at
  `protocol/toolCallText.ts`; `tools/index.ts:14` drops `helpers.ts` from its
  list of this directory's non-registering files instead of renaming it, since
  the file is no longer in the directory. Grep of
  `cmd/evener-hub/frontend/src` for `helpers.ts` / `tools/helpers` is clean.
- `bd896dbc5` merge of `origin/main` `e2c77cc72` (#1222 submitRouting, #1223
  activityRows landed ahead). Four conflicts, all "keep both": `index.ts`,
  `tsconfig.build.json`, `qualify-package.mjs` (shippedModules, rootValues,
  smoke calls) and `README.md`. Post-merge `make test-api-package` PASS,
  `make test-web` PASS. Pushed head `bd896dbc5`.
